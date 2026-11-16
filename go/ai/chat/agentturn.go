/*
 * © 2025 Sharon Aicler (saichler@gmail.com)
 *
 * Layer 8 Ecosystem is licensed under the Apache License, Version 2.0.
 * You may obtain a copy of the License at:
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package chat

import (
	"hash/fnv"
	"sort"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

const (
	turnFailedText      = "Tanto could not answer this time. Please try again."
	turnInterruptedText = "Tanto's answer was interrupted by a server change. Please send your message again."
)

// turnRequest is what a user message asks of Tanto. The aaaId is kept in
// memory only, for the turn's dispatch: tools run as the requester (plan §7.4).
type turnRequest struct {
	aaaId       string
	requesterId string
	profileId   string
	triggerSeq  int64
}

// agentTurn is the conversation's in-flight turn (at most one, plan §7.5).
type agentTurn struct {
	turnId       string
	req          *turnRequest
	attempt      int32
	dispatchedAt time.Time
	// orphan marks a turn this owner inherited from a previous owner: its
	// requester's session is not known here, so it cannot be re-dispatched.
	orphan bool
}

// requestTurn starts a turn for a new user message in a Tanto conversation, or
// marks it pending if a turn is in flight; pending messages are answered
// together by the next turn. The caller holds c.mtx and publishes the header.
func (this *Instance) requestTurn(c *conversation, req *turnRequest) {
	if c.turn != nil {
		c.pending = req
		return
	}
	this.startTurn(c, req)
}

func (this *Instance) startTurn(c *conversation, req *turnRequest) {
	t := &agentTurn{turnId: ifs.NewUuid(), req: req, attempt: 1}
	c.turn = t
	c.header.AgentState = l8ai.ChatAgentState_CHAT_AGENT_STATE_RUNNING
	c.header.AgentTurnId = t.turnId
	this.dispatchTurn(c, t)
}

// dispatchTurn sends the turn to the agent instance chosen by the hash of the
// conversation ID, moved on by one instance per attempt (plan §7.5). With no
// agent instance available the watchdog retries when the deadline passes.
func (this *Instance) dispatchTurn(c *conversation, t *agentTurn) {
	t.dispatchedAt = time.Now()
	target := this.agentFor(c.header.ConversationId, int(t.attempt-1))
	if target == "" {
		this.vnic.Resources().Logger().Error("chat: no ChatAgent instance for turn ", t.turnId)
		return
	}
	turn := &l8ai.ChatAgentTurn{TurnId: t.turnId, ConversationId: c.header.ConversationId,
		RequesterId: t.req.requesterId, LlmProfileId: t.req.profileId,
		TriggerSeq: t.req.triggerSeq, Attempt: t.attempt}
	if err := this.vnic.Unicast(target, aicommon.ChatAgentService, aicommon.ServiceArea, ifs.POST, turn, t.req.aaaId); err != nil {
		this.vnic.Resources().Logger().Error("chat: dispatch of turn ", t.turnId, ": ", err.Error())
	}
}

// agentFor picks an agent instance by the conversation's hash, for cache affinity.
func (this *Instance) agentFor(conversationId string, offset int) string {
	participants := aicommon.Participants(this.vnic.Resources(), aicommon.ChatAgentService)
	if len(participants) == 0 {
		return ""
	}
	ids := make([]string, 0, len(participants))
	for id := range participants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := fnv.New32a()
	h.Write([]byte(conversationId))
	return ids[(int(h.Sum32()%uint32(len(ids)))+offset)%len(ids)]
}

// endTurn closes the current turn and starts the pending one, if any.
// The caller holds c.mtx and publishes the header.
func (this *Instance) endTurn(c *conversation, state l8ai.ChatAgentState) {
	c.turn = nil
	c.header.AgentState = state
	c.header.AgentTurnId = ""
	if c.pending != nil {
		req := c.pending
		c.pending = nil
		this.startTurn(c, req)
	}
}

// failTurn ends the current turn with a SYSTEM message. The message has the
// turn's deterministic reply ID, so it takes the place of the reply.
func (this *Instance) failTurn(c *conversation, body string) {
	this.postSystemMessage(c, aicommon.AgentReplyMessageID(c.turn.turnId), body)
	this.endTurn(c, l8ai.ChatAgentState_CHAT_AGENT_STATE_FAILED)
	this.publishHeader(c, false)
}

// checkTurns is the turn watchdog: a turn past its deadline is re-dispatched
// to the next agent instance, and fails after AgentMaxAttempts (plan §7.5).
func (this *Instance) checkTurns() {
	for _, c := range this.owned() {
		c.mtx.Lock()
		t := c.turn
		switch {
		case t == nil:
		case t.orphan:
			this.failTurn(c, turnInterruptedText)
		case time.Since(t.dispatchedAt) < this.cfg.AgentTurnTimeout:
		case int(t.attempt) < this.cfg.AgentMaxAttempts:
			t.attempt++
			this.dispatchTurn(c, t)
		default:
			this.failTurn(c, turnFailedText)
		}
		c.mtx.Unlock()
	}
}
