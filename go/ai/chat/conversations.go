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
	"strings"
	"time"

	"github.com/google/uuid"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"github.com/saichler/l8types/go/types/l8web"
)

// conversationsService is ChatShCnv: the headers of the owned conversations.
//   - POST from the router with a session: a user creates a conversation.
//   - POST from the router without a session: the router assigns conversations
//     to this instance, as headers (cold start) or as ChatConvIndex rows whose
//     headers are loaded from chat-orm (an instance was lost) (plan §4.3).
//   - PATCH from the router: read position, title, LLM profile, or group membership.
//   - GET from the router: ChatConversation lists and the ChatPeer sidebar view.
type conversationsService struct {
	handlerBase
}

func (this *conversationsService) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *conversationsService) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	if !this.inst.fromRouter(msg) {
		return object.NewError("chat: conversations are accepted only from the router")
	}
	// The payload type says what it is, never the AAA id (the security
	// provider fills an empty AAA id with the sender's alias):
	// ChatConvIndex rows are the router moving conversations to this instance
	// (the public ChatConv route forwards ChatConversation elements only), and
	// a ChatConversation is always a user creating one. Each element is
	// handled on its own, so one bad element does not drop the rest.
	var firstErr error
	for _, e := range pb.Elements() {
		switch v := e.(type) {
		case *l8ai.ChatConversation:
			if err := this.inst.createConversation(v, msg); err != nil {
				this.inst.vnic.Resources().Logger().Error("chat: create conversation ", v.ConversationId, ": ", err.Error())
				if firstErr == nil {
					firstErr = err
				}
			}
		case *l8ai.ChatConvIndex:
			this.inst.assignById(v.ConversationId)
		}
	}
	if firstErr != nil {
		return object.NewError(firstErr.Error())
	}
	return object.New(nil, &l8web.L8Empty{})
}

func (this *conversationsService) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: use PATCH to change a conversation")
}

func (this *conversationsService) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	if this.inst.fromAgent(msg) {
		// A ChatAgent instance's compaction result (plan §7.5).
		for _, e := range pb.Elements() {
			if v, ok := e.(*l8ai.ChatConversation); ok && v != nil {
				this.inst.applyCompaction(v)
			}
		}
		return object.New(nil, &l8web.L8Empty{})
	}
	if !this.inst.fromRouter(msg) {
		return object.NewError("chat: conversations are accepted only from the router or a ChatAgent instance")
	}
	userId, err := this.inst.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	for _, e := range pb.Elements() {
		v, ok := e.(*l8ai.ChatConversation)
		if !ok || v == nil {
			continue
		}
		if v.ConversationId == "" && v.GroupId != "" {
			this.inst.updateGroupMembers(v, userId)
			continue
		}
		if err = this.inst.updateConversation(v, userId); err != nil {
			return object.NewError(err.Error())
		}
	}
	return object.New(nil, &l8web.L8Empty{})
}

func (this *conversationsService) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: conversations cannot be deleted")
}

func (this *conversationsService) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	if !this.inst.fromRouter(msg) {
		return object.NewError("chat: reads are accepted only from the router")
	}
	q, err := pb.Query(vnic.Resources())
	if err != nil || q == nil {
		return object.NewError("chat: invalid conversation query")
	}
	if q.Register() == l8api.L8Register_L8_REGISTER_UNREGISTER {
		this.inst.cnvCache.Fetch(0, 0, q)
		return object.New(nil, &l8web.L8Empty{})
	}
	// The router leader's ownership sync: the conversations this instance
	// holds. The public ChatConv route refuses ChatConvIndex queries, so only
	// the router's own sync reaches this.
	if q.RootType() != nil && q.RootType().TypeName == "ChatConvIndex" {
		return this.inst.ownedIndex()
	}
	userId, err := this.inst.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	if q.RootType() != nil && q.RootType().TypeName == "ChatPeer" {
		return this.inst.peers(userId, int(q.Limit()))
	}
	if !this.inst.scopedToRequester(q, userId) {
		return object.NewError("chat: a conversation query must select members.userId=<you> or a conversationId you belong to")
	}
	limit := int(q.Limit())
	elems, metadata := this.inst.cnvCache.Fetch(int(q.Page())*limit, limit, q)
	result := make([]interface{}, 0, len(elems))
	for _, e := range elems {
		if h, ok := e.(*l8ai.ChatConversation); ok && hasMember(h, userId) {
			result = append(result, h)
		}
	}
	return object.NewQueryResult(result, metadata)
}

// ownedIndex lists the conversations this instance holds as index rows. The
// router leader rebuilds its index from these lists when it takes over, so
// it never reads chat-orm and nothing moves.
func (this *Instance) ownedIndex() ifs.IElements {
	local := this.vnic.Resources().SysConfig().LocalUuid
	owned := this.owned()
	rows := make([]*l8ai.ChatConvIndex, 0, len(owned))
	for _, c := range owned {
		c.mtx.Lock()
		rows = append(rows, &l8ai.ChatConvIndex{ConversationId: c.header.ConversationId, OwnerUuid: local})
		c.mtx.Unlock()
	}
	return object.New(nil, rows)
}

// scopedToRequester is true when the query can only match conversations of the
// requester, so neither its results nor its live registration reach anyone else.
func (this *Instance) scopedToRequester(q ifs.IQuery, userId string) bool {
	if member, ok := aicommon.CriteriaValue(q, "members.userId", "="); ok {
		return member == userId
	}
	if conversationId, ok := aicommon.CriteriaValue(q, "conversationId", "="); ok {
		c := this.conversation(conversationId)
		if c == nil {
			return false
		}
		c.mtx.Lock()
		defer c.mtx.Unlock()
		return c.isMember(userId)
	}
	return false
}

// createConversation creates a conversation for the requester. Creating one
// that already exists is a no-op for its members, so default conversations
// with deterministic IDs can be "opened" by POSTing them (plan §4.1).
func (this *Instance) createConversation(h *l8ai.ChatConversation, msg *ifs.Message) error {
	if !this.accepting.Load() {
		return errors.New("chat: instance is shutting down")
	}
	userId, err := this.requester(msg)
	if err != nil {
		return err
	}
	if h.ConversationId == "" {
		return errors.New("chat: ConversationId is required")
	}
	if existing := this.conversation(h.ConversationId); existing != nil {
		existing.mtx.Lock()
		defer existing.mtx.Unlock()
		if !existing.isMember(userId) {
			return errors.New("chat: conversation " + h.ConversationId + " already exists")
		}
		return nil
	}
	now := time.Now().Unix()
	header := &l8ai.ChatConversation{ConversationId: h.ConversationId, PeerKind: h.PeerKind, GroupId: h.GroupId,
		Title: h.Title, LlmProfileId: h.LlmProfileId, CreatedBy: userId, CreatedAt: now, LastMessageAt: now,
		OwnerPod: this.pod}
	memberIds, deterministicId, err := newConversationMembers(h, userId, header)
	if err != nil {
		return err
	}
	// A deterministic (UUIDv5) ID may only be used by the conversation it is
	// derived from; client-created conversations use random IDs (plan §4.1).
	if parsed, perr := uuid.Parse(h.ConversationId); perr != nil {
		return errors.New("chat: ConversationId must be a UUID")
	} else if parsed.Version() == 5 && h.ConversationId != deterministicId {
		return errors.New("chat: ConversationId does not match the conversation")
	}
	for _, id := range memberIds {
		header.Members = append(header.Members, &l8ai.ChatMember{UserId: id, JoinedAt: now})
	}
	if this.takeOwnership(header) {
		this.writer.header(header.ConversationId)
	}
	return nil
}

// newConversationMembers validates a new conversation's peer and returns its
// members and the deterministic ID of its default conversation.
func newConversationMembers(h *l8ai.ChatConversation, userId string, header *l8ai.ChatConversation) ([]string, string, error) {
	switch h.PeerKind {
	case l8ai.ChatPeerKind_CHAT_PEER_KIND_USER:
		parts := strings.Split(h.PeerKey, "|")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == parts[1] {
			return nil, "", errors.New("chat: PeerKey of a direct conversation is \"<userId>|<userId>\"")
		}
		if parts[0] != userId && parts[1] != userId {
			return nil, "", errors.New("chat: a direct conversation must include the requester")
		}
		header.PeerKey = aicommon.DirectPeerKey(parts[0], parts[1])
		return parts, aicommon.DirectConversationID(parts[0], parts[1]), nil
	case l8ai.ChatPeerKind_CHAT_PEER_KIND_GROUP:
		if h.GroupId == "" {
			return nil, "", errors.New("chat: GroupId is required for a group conversation")
		}
		header.PeerKey = h.GroupId
		ids := uniqueMembers(h.Members)
		if !contains(ids, userId) {
			return nil, "", errors.New("chat: a group conversation must include the requester")
		}
		return ids, aicommon.GroupConversationID(h.GroupId), nil
	case l8ai.ChatPeerKind_CHAT_PEER_KIND_AGENT:
		if h.PeerKey != "" && h.PeerKey != aicommon.AgentPeerKey(userId) {
			return nil, "", errors.New("chat: a Tanto conversation belongs to the requester only")
		}
		header.PeerKey = aicommon.AgentPeerKey(userId)
		header.AgentState = l8ai.ChatAgentState_CHAT_AGENT_STATE_IDLE
		return []string{userId, aicommon.TantoUserId}, aicommon.AgentConversationID(userId), nil
	}
	return nil, "", errors.New("chat: PeerKind is required")
}

// updateConversation applies a member's change: their own read position, the
// title, or (Tanto conversations) the LLM profile.
func (this *Instance) updateConversation(v *l8ai.ChatConversation, userId string) error {
	c := this.conversation(v.ConversationId)
	if c == nil {
		return errors.New("chat: conversation " + v.ConversationId + " is not owned by this instance")
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	mine := c.member(userId)
	if mine == nil {
		return errors.New("chat: not a member of the conversation")
	}
	changed := false
	for _, m := range v.Members {
		if m == nil || m.UserId != userId {
			continue // a member can move only their own read position
		}
		seq := m.LastReadSeq
		if seq > c.header.LastSeq {
			seq = c.header.LastSeq
		}
		if seq > mine.LastReadSeq {
			mine.LastReadSeq = seq
			changed = true
		}
	}
	if v.Title != "" && v.Title != c.header.Title {
		c.header.Title = v.Title
		changed = true
	}
	if v.LlmProfileId != "" && c.isAgentConversation() && v.LlmProfileId != c.header.LlmProfileId {
		c.header.LlmProfileId = v.LlmProfileId
		changed = true
	}
	if changed {
		this.publishHeader(c, false)
	}
	return nil
}

// applyCompaction stores a compacted summary only if the conversation's
// summary position is still the one the compaction started from
// (compare-and-set): of two racing compactions, the later one is discarded.
func (this *Instance) applyCompaction(v *l8ai.ChatConversation) {
	c := this.conversation(v.ConversationId)
	if c == nil {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if v.ExpectedSummaryUpToSeq != c.header.SummaryUpToSeq || v.SummaryUpToSeq <= c.header.SummaryUpToSeq ||
		v.SummaryUpToSeq > c.header.LastSeq || v.Summary == "" {
		return
	}
	c.header.Summary = v.Summary
	c.header.SummaryUpToSeq = v.SummaryUpToSeq
	this.publishHeader(c, false)
}

// updateGroupMembers applies a group's new member list to every owned
// conversation of the group (plan §4.4). Only the conversation's creator may
// change its members. Removed members lose their live registrations on it.
func (this *Instance) updateGroupMembers(v *l8ai.ChatConversation, userId string) {
	wanted := uniqueMembers(v.Members)
	if len(wanted) == 0 {
		return
	}
	now := time.Now().Unix()
	for _, c := range this.owned() {
		c.mtx.Lock()
		if c.header.GroupId != v.GroupId || c.header.CreatedBy != userId {
			c.mtx.Unlock()
			continue
		}
		removed := make([]string, 0)
		kept := make([]*l8ai.ChatMember, 0, len(wanted))
		for _, m := range c.header.Members {
			if m != nil && contains(wanted, m.UserId) {
				kept = append(kept, m)
			} else if m != nil {
				removed = append(removed, m.UserId)
			}
		}
		added := false
		for _, id := range wanted {
			if !hasMemberIn(kept, id) {
				kept = append(kept, &l8ai.ChatMember{UserId: id, JoinedAt: now})
				added = true
			}
		}
		if added || len(removed) > 0 {
			c.header.Members = kept
			this.publishHeader(c, false)
		}
		conversationId := c.header.ConversationId
		c.mtx.Unlock()
		if len(removed) > 0 {
			this.dropSubscriptions(conversationId, removed)
		}
	}
}

// dropSubscriptions removes the live registrations that removed members hold
// on the conversation's messages and header.
func (this *Instance) dropSubscriptions(conversationId string, userIds []string) {
	probeMsg := &l8ai.ChatMessage{ConversationId: conversationId}
	probeHeader := &l8ai.ChatConversation{ConversationId: conversationId}
	for _, sub := range this.msgCache.Subscribers() {
		if user, ok := this.users.Cached(sub.AAAId); ok && contains(userIds, user) && sub.Query.Match(probeMsg) {
			this.msgCache.UnregisterSubscription(sub.AAAId, sub.RegisterId)
		}
	}
	for _, sub := range this.cnvCache.Subscribers() {
		if user, ok := this.users.Cached(sub.AAAId); ok && contains(userIds, user) && sub.Query.Match(probeHeader) {
			this.cnvCache.UnregisterSubscription(sub.AAAId, sub.RegisterId)
		}
	}
}

func uniqueMembers(members []*l8ai.ChatMember) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		if m != nil && m.UserId != "" && !contains(ids, m.UserId) {
			ids = append(ids, m.UserId)
		}
	}
	return ids
}

func hasMember(h *l8ai.ChatConversation, userId string) bool {
	return hasMemberIn(h.Members, userId)
}

func hasMemberIn(members []*l8ai.ChatMember, userId string) bool {
	for _, m := range members {
		if m != nil && m.UserId == userId {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
