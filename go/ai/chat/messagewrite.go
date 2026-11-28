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
	"errors"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8web"
)

// PostMessage handles a create: a user's message from the router (plan §5.2),
// or Tanto's reply from a ChatAgent instance (plan §7.1). Requests from any
// other node are rejected.
func (this *ChatMessageServiceHandler) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	fromAgent := this.inst.fromAgent(msg)
	if !fromAgent && !this.inst.fromRouter(msg) {
		return object.NewError("chat: messages are accepted only from the router or a ChatAgent instance")
	}
	for _, e := range pb.Elements() {
		m, ok := e.(*l8ai.ChatMessage)
		if !ok || m == nil {
			continue
		}
		var err error
		if fromAgent {
			err = this.postAgentMessage(m)
		} else {
			err = this.postUserMessage(m, msg)
		}
		if err != nil {
			vnic.Resources().Logger().Error("chat: message ", m.MessageId, " rejected: ", err.Error())
			return object.NewError(err.Error())
		}
	}
	return object.New(nil, &l8web.L8Empty{})
}

// postUserMessage appends a user's message (plan §5.2 step 4). The server
// sets the sender, the time and the seq; a resend of an accepted message is a
// no-op. In a conversation with Tanto, the message starts (or queues) a turn.
func (this *ChatMessageServiceHandler) postUserMessage(m *l8ai.ChatMessage, msg *ifs.Message) error {
	if !this.inst.accepting.Load() {
		return errors.New("chat: instance is shutting down")
	}
	userId, err := this.inst.requester(msg)
	if err != nil {
		return err
	}
	if m.MessageId == "" {
		return errors.New("chat: MessageId is required")
	}
	if m.Kind == l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_UNSPECIFIED {
		m.Kind = l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_TEXT
	}
	// AGENT and SYSTEM messages never come through the router (plan §7.1).
	if m.Kind != l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_TEXT {
		return errors.New("chat: only text messages can be sent")
	}
	if m.Body == "" && len(m.Attachments) == 0 {
		return errors.New("chat: message is empty")
	}
	c := this.inst.conversation(m.ConversationId)
	if c == nil {
		return errors.New("chat: conversation " + m.ConversationId + " is not owned by this instance")
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if !c.isMember(userId) {
		return errors.New("chat: not a member of the conversation")
	}
	if this.hasMessage(m.ConversationId, m.MessageId) {
		return nil // a resend of a message already accepted (plan §5.2 step 5)
	}
	m = cloneMessage(m)
	m.SenderId = userId
	m.SentAt = time.Now().Unix()
	m.TokenCount = 0
	m.AgentTurnId = ""
	if m.LlmProfileId == "" {
		m.LlmProfileId = c.header.LlmProfileId
	}
	this.publishMessage(c, m)
	if c.isAgentConversation() {
		this.inst.requestTurn(c, &turnRequest{aaaId: msg.AAAId(), requesterId: userId,
			profileId: m.LlmProfileId, triggerSeq: m.Seq})
	}
	this.inst.publishHeader(c, false)
	return nil
}

// postAgentMessage appends Tanto's reply (or the agent's SYSTEM message) to
// the conversation's current turn and ends the turn (plan §7.1 step 3).
func (this *ChatMessageServiceHandler) postAgentMessage(m *l8ai.ChatMessage) error {
	if !this.inst.accepting.Load() {
		return errors.New("chat: instance is shutting down")
	}
	c := this.inst.conversation(m.ConversationId)
	if c == nil {
		return errors.New("chat: conversation " + m.ConversationId + " is not owned by this instance")
	}
	if m.Kind != l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_AGENT && m.Kind != l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM {
		return errors.New("chat: an agent message must be AGENT or SYSTEM")
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.turn == nil || c.turn.turnId != m.AgentTurnId || m.MessageId != aicommon.AgentReplyMessageID(m.AgentTurnId) {
		return errors.New("chat: agent reply does not match the conversation's current turn")
	}
	if this.hasMessage(m.ConversationId, m.MessageId) {
		return nil
	}
	m = cloneMessage(m)
	m.SenderId = aicommon.TantoUserId
	m.SentAt = time.Now().Unix()
	this.publishMessage(c, m)
	this.inst.endTurn(c, l8ai.ChatAgentState_CHAT_AGENT_STATE_IDLE)
	this.inst.publishHeader(c, false)
	return nil
}

// publishMessage records a new message, pushes it to the browsers subscribed
// to its conversation and queues it for chat-orm. The caller holds c.mtx.
func (this *ChatMessageServiceHandler) publishMessage(c *conversation, m *l8ai.ChatMessage) {
	this.record(c, m, this.inst.cfg.NotifRetain)
	this.push(m)
	this.persist(m)
}

// record gives the message the conversation's next seq and notification
// seq, adds it to the conversation's window (dropping the oldest past the
// window size) and moves the conversation's header forward (LastSeq, last
// message time, preview, the sender's read position). The seq comes from the
// header, so a trimmed or evicted window does not affect numbering. The push
// is retained for the gap fill (plan §5.7). The caller holds c.mtx, so a
// conversation's messages are added in seq order. Every accepted write marks
// the conversation used, for the rebalance and the idle eviction.
func (this *ChatMessageServiceHandler) record(c *conversation, m *l8ai.ChatMessage, retain int) {
	m.Seq = c.header.LastSeq + 1
	c.header.NotifSeq++
	m.NotifSeq = c.header.NotifSeq
	c.lastUsed = time.Now()
	this.mtx.Lock()
	w := this.conversations[m.ConversationId]
	if w == nil {
		w = &messageWindow{}
		this.conversations[m.ConversationId] = w
	}
	w.add(m, this.size)
	this.mtx.Unlock()
	c.recordMessage(m)
	c.retain(&notification{notifSeq: m.NotifSeq, message: cloneMessage(m)}, retain)
}

// postSystemMessage appends a SYSTEM message from the owner itself, e.g. when
// a turn fails (Instance.failTurn). It has the turn's reply ID, so it takes
// the place of the reply; a second one for the same turn is a no-op. The
// caller holds c.mtx and publishes the header afterwards.
func (this *ChatMessageServiceHandler) postSystemMessage(c *conversation, messageId, body string) {
	if this.hasMessage(c.header.ConversationId, messageId) {
		return
	}
	this.publishMessage(c, &l8ai.ChatMessage{MessageId: messageId, ConversationId: c.header.ConversationId,
		SenderId: aicommon.TantoUserId, Kind: l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM,
		Body: body, SentAt: time.Now().Unix()})
}

// hasMessage reports whether the conversation's window holds the message. A
// resend is of a recent message, so the window is searched newest first.
func (this *ChatMessageServiceHandler) hasMessage(conversationId, messageId string) bool {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	w := this.conversations[conversationId]
	if w == nil {
		return false
	}
	for i := len(w.messages) - 1; i >= 0; i-- {
		if w.messages[i].MessageId == messageId {
			return true
		}
	}
	return false
}

// persist queues a new message for chat-orm on the instance's write-behind,
// which sends a clone and flushes what it holds on a graceful shutdown.
func (this *ChatMessageServiceHandler) persist(m *l8ai.ChatMessage) {
	this.inst.writer.message(m)
}
