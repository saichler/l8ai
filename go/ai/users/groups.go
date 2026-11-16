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

package users

import (
	"errors"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8services/go/services/base"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8web"
	"google.golang.org/protobuf/proto"
)

// groupService is ChatGroup: groups. A DCache persisted through ChatGrpDb.
// Creating a group also creates its General conversation through the router;
// changing its members propagates to every group conversation (plan §4.4).
type groupService struct {
	base.BaseService
	u *Users
}

func (this *groupService) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	this.u = usersArg(sla)
	if this.u == nil {
		return errors.New("ChatGroup activated without its *Users argument")
	}
	return this.BaseService.Activate(sla, vnic)
}

func (this *groupService) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	return this.write(pb, vnic, msg, true)
}

func (this *groupService) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	return this.write(pb, vnic, msg, false)
}

func (this *groupService) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	return this.write(pb, vnic, msg, false)
}

func (this *groupService) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat-users: groups cannot be deleted")
}

// GetMessage returns only the groups the requester belongs to. An internal
// read (no session, e.g. the framework's replica recovery) gets every group.
func (this *groupService) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	resp = this.BaseService.Get(pb, vnic)
	if msg.AAAId() == "" || resp == nil || resp.Error() != nil {
		return resp
	}
	userId, err := this.u.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	result := make([]interface{}, 0)
	for _, e := range resp.Elements() {
		if g, ok := e.(*l8ai.ChatGroup); ok && g != nil && contains(g.MemberIds, userId) {
			result = append(result, g)
		}
	}
	return object.NewQueryResult(result, resp.Metadata())
}

func (this *groupService) write(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message, create bool) ifs.IElements {
	userId, err := this.u.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	for _, e := range pb.Elements() {
		g, ok := e.(*l8ai.ChatGroup)
		if !ok || g == nil {
			continue
		}
		if create {
			err = this.create(g, userId, msg.AAAId(), vnic)
		} else {
			err = this.update(g, userId, msg.AAAId(), vnic)
		}
		if err != nil {
			return object.NewError(err.Error())
		}
	}
	return object.New(nil, &l8web.L8Empty{})
}

func (this *groupService) create(g *l8ai.ChatGroup, userId, aaaId string, vnic ifs.IVNic) error {
	if g.Name == "" {
		return errors.New("chat-users: a group needs a name")
	}
	if g.GroupId == "" {
		g.GroupId = ifs.NewUuid()
	} else if get(this, &l8ai.ChatGroup{GroupId: g.GroupId}, vnic) != nil {
		return errors.New("chat-users: group " + g.GroupId + " already exists")
	}
	group := &l8ai.ChatGroup{GroupId: g.GroupId, Name: g.Name, MemberIds: withCreator(g.MemberIds, userId),
		CreatedBy: userId, CreatedAt: time.Now().Unix()}
	if resp := this.BaseService.Post(object.New(nil, group), vnic); resp != nil && resp.Error() != nil {
		return resp.Error()
	}
	this.persist(group, ifs.POST, vnic)
	// The General conversation has a deterministic ID; it is created with the
	// creator's session, so the creator is its created_by (plan §4.4).
	general := &l8ai.ChatConversation{ConversationId: aicommon.GroupConversationID(group.GroupId),
		PeerKind: l8ai.ChatPeerKind_CHAT_PEER_KIND_GROUP, GroupId: group.GroupId, Title: group.Name,
		Members: members(group.MemberIds)}
	this.toRouter(ifs.POST, general, aaaId, vnic)
	return nil
}

// update applies the creator's change to the name or the members.
func (this *groupService) update(g *l8ai.ChatGroup, userId, aaaId string, vnic ifs.IVNic) error {
	current, _ := get(this, &l8ai.ChatGroup{GroupId: g.GroupId}, vnic).(*l8ai.ChatGroup)
	if current == nil {
		return errors.New("chat-users: group " + g.GroupId + " not found")
	}
	if current.CreatedBy != userId {
		return errors.New("chat-users: only the group's creator can change it")
	}
	next := proto.Clone(current).(*l8ai.ChatGroup)
	nameChanged := g.Name != "" && g.Name != current.Name
	if nameChanged {
		next.Name = g.Name
	}
	membersChanged := false
	if len(g.MemberIds) > 0 {
		next.MemberIds = withCreator(g.MemberIds, current.CreatedBy)
		membersChanged = !sameSet(next.MemberIds, current.MemberIds)
	}
	if !nameChanged && !membersChanged {
		return nil
	}
	if resp := this.BaseService.Post(object.New(nil, next), vnic); resp != nil && resp.Error() != nil {
		return resp.Error()
	}
	this.persist(next, ifs.PUT, vnic)
	if membersChanged {
		this.toRouter(ifs.PATCH, &l8ai.ChatConversation{GroupId: next.GroupId, Members: members(next.MemberIds)}, aaaId, vnic)
	}
	if nameChanged {
		this.toRouter(ifs.PATCH, &l8ai.ChatConversation{ConversationId: aicommon.GroupConversationID(next.GroupId),
			Title: next.Name}, aaaId, vnic)
	}
	return nil
}

func (this *groupService) persist(g *l8ai.ChatGroup, action ifs.Action, vnic ifs.IVNic) {
	if err := vnic.Leader(aicommon.ChatGrpDbService, aicommon.ServiceArea, action, g); err != nil {
		vnic.Resources().Logger().Error("chat-users: persist group ", g.GroupId, ": ", err.Error())
	}
}

// toRouter sends a conversation change to the chat router with the requester's session.
func (this *groupService) toRouter(action ifs.Action, h *l8ai.ChatConversation, aaaId string, vnic ifs.IVNic) {
	resp := vnic.LeaderRequest(aicommon.ChatConvService, aicommon.ServiceArea, action, h, this.u.cfg.ReadTimeoutSeconds, aaaId)
	if resp != nil && resp.Error() != nil {
		vnic.Resources().Logger().Error("chat-users: group conversation update: ", resp.Error().Error())
	}
}

func members(ids []string) []*l8ai.ChatMember {
	result := make([]*l8ai.ChatMember, 0, len(ids))
	for _, id := range ids {
		result = append(result, &l8ai.ChatMember{UserId: id})
	}
	return result
}

// withCreator returns the unique, non-empty IDs, with the creator included.
func withCreator(ids []string, creator string) []string {
	result := []string{creator}
	for _, id := range ids {
		if id != "" && !contains(result, id) {
			result = append(result, id)
		}
	}
	return result
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, id := range a {
		if !contains(b, id) {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
