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

package router

import (
	"sort"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// msgRoute is ChatMsg: messages, routed to the conversation's owner.
type msgRoute struct{ routeBase }

func (this *msgRoute) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *msgRoute) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	resp = accepted()
	for _, e := range pb.Elements() {
		if m, ok := e.(*l8ai.ChatMessage); ok && m != nil {
			resp = this.r.routeWrite(aicommon.ChatMsgService, aicommon.ChatShMsgService, ifs.POST,
				m.ConversationId, m, msg, false)
			if resp != nil && resp.Error() != nil {
				return resp
			}
		}
	}
	return resp
}

func (this *msgRoute) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: messages are append-only")
}

func (this *msgRoute) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	pq, q, err := parseRead(pb, vnic)
	if err != nil {
		return object.NewError(err.Error())
	}
	if q.Register() == l8api.L8Register_L8_REGISTER_UNREGISTER {
		this.r.unregister(pq, msg.AAAId(), aicommon.ChatMsgService, aicommon.ChatShMsgService)
		return accepted()
	}
	conversationId, _ := aicommon.CriteriaValue(q, "conversationId", "=")
	resp = this.r.routeRead(aicommon.ChatMsgService, aicommon.ChatShMsgService, conversationId, pq, msg)
	if q.Register() == l8api.L8Register_L8_REGISTER_REGISTER && resp != nil && resp.Error() == nil {
		this.r.record(pq, msg.AAAId(), aicommon.ChatMsgService, conversationId)
	}
	return resp
}

// convRoute is ChatConv: conversation headers. Writes go to the owner, a new
// conversation is assigned first; group membership updates go to every
// instance; a list read fans out to every instance and is merged (plan §4.2).
type convRoute struct{ routeBase }

func (this *convRoute) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *convRoute) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	resp = accepted()
	for _, e := range pb.Elements() {
		if h, ok := e.(*l8ai.ChatConversation); ok && h != nil {
			resp = this.r.routeWrite(aicommon.ChatConvService, aicommon.ChatShCnvService, ifs.POST,
				h.ConversationId, h, msg, true)
			if resp != nil && resp.Error() != nil {
				return resp
			}
		}
	}
	return resp
}

func (this *convRoute) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	resp = accepted()
	for _, e := range pb.Elements() {
		h, ok := e.(*l8ai.ChatConversation)
		if !ok || h == nil {
			continue
		}
		if h.ConversationId == "" && h.GroupId != "" {
			// Group membership: every instance updates the group's conversations it owns (plan §4.4).
			for _, target := range this.r.allInstances() {
				this.r.unicast(target, aicommon.ChatShCnvService, ifs.PATCH, h, msg.AAAId())
			}
			continue
		}
		resp = this.r.routeWrite(aicommon.ChatConvService, aicommon.ChatShCnvService, ifs.PATCH,
			h.ConversationId, h, msg, false)
		if resp != nil && resp.Error() != nil {
			return resp
		}
	}
	return resp
}

func (this *convRoute) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	pq, q, err := parseRead(pb, vnic)
	if err != nil {
		return object.NewError(err.Error())
	}
	if q.Register() == l8api.L8Register_L8_REGISTER_UNREGISTER {
		this.r.unregister(pq, msg.AAAId(), aicommon.ChatConvService, aicommon.ChatShCnvService)
		return accepted()
	}
	conversationId, _ := aicommon.CriteriaValue(q, "conversationId", "=")
	if conversationId != "" {
		resp = this.r.routeRead(aicommon.ChatConvService, aicommon.ChatShCnvService, conversationId, pq, msg)
	} else {
		resp = mergeConversations(this.r.fanOut(this.r.allInstances(), aicommon.ChatShCnvService, pq, msg.AAAId()), int(q.Limit()))
	}
	if q.Register() == l8api.L8Register_L8_REGISTER_REGISTER && resp != nil && resp.Error() == nil {
		this.r.record(pq, msg.AAAId(), aicommon.ChatConvService, conversationId)
	}
	return resp
}

// peerRoute is ChatPeer: the sidebar, merged from every instance's part.
type peerRoute struct{ routeBase }

func (this *peerRoute) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *peerRoute) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: ChatPeer is read-only")
}

func (this *peerRoute) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: ChatPeer is read-only")
}

func (this *peerRoute) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	pq, _, err := parseRead(pb, vnic)
	if err != nil {
		return object.NewError(err.Error())
	}
	parts := this.r.fanOut(this.r.allInstances(), aicommon.ChatShCnvService, aicommon.PlainQuery(pq), msg.AAAId())
	return mergePeers(parts, this.r.cfg.PeerListSize)
}

// syncRoute is ChatSync: gap fill goes to the owner; "latest only" is grouped
// by owner, one request per owner (plan §5.7).
type syncRoute struct{ routeBase }

func (this *syncRoute) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *syncRoute) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: ChatSync is read-only")
}

func (this *syncRoute) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: ChatSync is read-only")
}

func (this *syncRoute) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	pq, q, err := parseRead(pb, vnic)
	if err != nil {
		return object.NewError(err.Error())
	}
	pq = aicommon.PlainQuery(pq)
	if _, gapFill := aicommon.CriteriaInt64(q, "notifSeq", ">"); gapFill {
		conversationId, _ := aicommon.CriteriaValue(q, "conversationId", "=")
		return this.r.routeRead(aicommon.ChatSyncService, aicommon.ChatShSyncService, conversationId, pq, msg)
	}
	owners := make(map[string]bool)
	for _, id := range aicommon.CriteriaList(q, "conversationId") {
		if owner := this.r.ownerOf(id); owner != "" {
			owners[owner] = true
		}
	}
	targets := make([]string, 0, len(owners))
	for owner := range owners {
		targets = append(targets, owner)
	}
	return object.NewQueryResult(this.r.fanOut(targets, aicommon.ChatShSyncService, pq, msg.AAAId()), &l8api.L8MetaData{})
}

// mergeConversations merges the instances' conversation lists, newest first.
func mergeConversations(parts []interface{}, limit int) ifs.IElements {
	byId := make(map[string]*l8ai.ChatConversation)
	for _, e := range parts {
		if h, ok := e.(*l8ai.ChatConversation); ok {
			byId[h.ConversationId] = h
		}
	}
	list := make([]*l8ai.ChatConversation, 0, len(byId))
	for _, h := range byId {
		list = append(list, h)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastMessageAt > list[j].LastMessageAt })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return object.NewQueryResult(list, &l8api.L8MetaData{})
}

// mergePeers merges the instances' peer rows: the newest message wins and the
// unread conversations are united.
func mergePeers(parts []interface{}, limit int) ifs.IElements {
	byId := make(map[string]*l8ai.ChatPeer)
	for _, e := range parts {
		p, ok := e.(*l8ai.ChatPeer)
		if !ok {
			continue
		}
		row, exists := byId[p.PeerRowId]
		if !exists {
			byId[p.PeerRowId] = p
			continue
		}
		if p.LastMessageAt > row.LastMessageAt {
			row.LastMessageAt = p.LastMessageAt
			row.LastPreview = p.LastPreview
		}
		if row.DisplayName == "" {
			row.DisplayName = p.DisplayName
		}
		row.UnreadConversationIds = append(row.UnreadConversationIds, p.UnreadConversationIds...)
	}
	list := make([]*l8ai.ChatPeer, 0, len(byId))
	for _, row := range byId {
		list = append(list, row)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastMessageAt > list[j].LastMessageAt })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return object.NewQueryResult(list, &l8api.L8MetaData{})
}
