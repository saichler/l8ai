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
	"github.com/saichler/l8types/go/types/l8api"
	"github.com/saichler/l8types/go/types/l8web"
)

// messagesService is ChatShMsg: the messages of the owned conversations.
//   - POST from the router: a user's message (plan §5.2).
//   - POST from a ChatAgent instance: Tanto's reply to the current turn (plan §7.1).
//   - GET from the router: a page of a thread, from the ring or chat-orm (plan §5.3).
type messagesService struct {
	handlerBase
}

func (this *messagesService) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *messagesService) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
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
			err = this.inst.postAgentMessage(m)
		} else {
			err = this.inst.postUserMessage(m, msg)
		}
		if err != nil {
			vnic.Resources().Logger().Error("chat: message ", m.MessageId, " rejected: ", err.Error())
			return object.NewError(err.Error())
		}
	}
	return object.New(nil, &l8web.L8Empty{})
}

func (this *messagesService) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: messages are append-only")
}

func (this *messagesService) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: messages are append-only")
}

func (this *messagesService) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: messages are append-only")
}

func (this *messagesService) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	if !this.inst.fromRouter(msg) {
		return object.NewError("chat: reads are accepted only from the router")
	}
	q, err := pb.Query(vnic.Resources())
	if err != nil || q == nil {
		return object.NewError("chat: invalid message query")
	}
	// An unregister (e.g. sent when the browser's websocket closes) carries no
	// conversation; the cache removes the subscription and fetches nothing.
	if q.Register() == l8api.L8Register_L8_REGISTER_UNREGISTER {
		this.inst.msgCache.Fetch(0, 0, q)
		return object.New(nil, &l8web.L8Empty{})
	}
	userId, err := this.inst.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	conversationId, _ := aicommon.CriteriaValue(q, "conversationId", "=")
	c := this.inst.conversation(conversationId)
	if c == nil {
		return object.NewError("chat: conversation " + conversationId + " is not owned by this instance")
	}
	c.mtx.Lock()
	if !c.isMember(userId) {
		c.mtx.Unlock()
		return object.NewError("chat: not a member of the conversation")
	}
	err = this.inst.ensureRing(c)
	complete := c.ringComplete
	c.mtx.Unlock()
	if err != nil {
		return object.NewError(err.Error())
	}

	limit := int(q.Limit())
	elems, metadata := this.inst.msgCache.Fetch(int(q.Page())*limit, limit, q)
	if limit > 0 && len(elems) < limit && !complete {
		// The ring does not cover the page: read it from chat-orm (plan §5.3).
		if pq := aicommon.PQueryOf(pb); pq != nil {
			ormResp := vnic.LeaderRequest(aicommon.ChatMsgDbService, aicommon.ServiceArea, ifs.GET,
				aicommon.PlainQuery(pq), this.inst.cfg.ReadTimeoutSeconds)
			if ormResp != nil && ormResp.Error() == nil {
				return ormResp
			}
		}
	}
	return object.NewQueryResult(elems, metadata)
}

// postUserMessage appends a user's message (plan §5.2 step 4).
func (this *Instance) postUserMessage(m *l8ai.ChatMessage, msg *ifs.Message) error {
	if !this.accepting.Load() {
		return errors.New("chat: instance is shutting down")
	}
	userId, err := this.requester(msg)
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
	c := this.conversation(m.ConversationId)
	if c == nil {
		return errors.New("chat: conversation " + m.ConversationId + " is not owned by this instance")
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if !c.isMember(userId) {
		return errors.New("chat: not a member of the conversation")
	}
	if err = this.ensureRing(c); err != nil {
		return err
	}
	if this.hasMessage(m.MessageId) {
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
	m.Seq = c.header.LastSeq + 1
	c.recordMessage(m)
	this.publishMessage(c, m)
	if c.isAgentConversation() {
		this.requestTurn(c, &turnRequest{aaaId: msg.AAAId(), requesterId: userId,
			profileId: m.LlmProfileId, triggerSeq: m.Seq})
	}
	this.publishHeader(c, false)
	return nil
}

// postAgentMessage appends Tanto's reply (or the agent's SYSTEM message) to the
// conversation's current turn and ends the turn (plan §7.1 step 3).
func (this *Instance) postAgentMessage(m *l8ai.ChatMessage) error {
	if !this.accepting.Load() {
		return errors.New("chat: instance is shutting down")
	}
	c := this.conversation(m.ConversationId)
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
	if err := this.ensureRing(c); err != nil {
		return err
	}
	if this.hasMessage(m.MessageId) {
		return nil
	}
	m = cloneMessage(m)
	m.SenderId = aicommon.TantoUserId
	m.SentAt = time.Now().Unix()
	m.Seq = c.header.LastSeq + 1
	c.recordMessage(m)
	this.publishMessage(c, m)
	this.endTurn(c, l8ai.ChatAgentState_CHAT_AGENT_STATE_IDLE)
	this.publishHeader(c, false)
	return nil
}

// postSystemMessage appends a SYSTEM message from the owner itself, e.g. when a
// turn fails. The caller holds c.mtx and publishes the header afterwards.
func (this *Instance) postSystemMessage(c *conversation, messageId, body string) {
	if err := this.ensureRing(c); err != nil {
		this.vnic.Resources().Logger().Error("chat: system message for ", c.header.ConversationId, ": ", err.Error())
		return
	}
	if this.hasMessage(messageId) {
		return
	}
	m := &l8ai.ChatMessage{MessageId: messageId, ConversationId: c.header.ConversationId,
		SenderId: aicommon.TantoUserId, Kind: l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM,
		Body: body, SentAt: time.Now().Unix(), Seq: c.header.LastSeq + 1}
	c.recordMessage(m)
	this.publishMessage(c, m)
}

// hasMessage reports whether the message is already in a ring.
func (this *Instance) hasMessage(messageId string) bool {
	v, err := this.msgCache.Get(&l8ai.ChatMessage{MessageId: messageId})
	return err == nil && v != nil
}
