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

// statService is ChatStat: presence. It embeds the BaseService DCache; the
// *Message methods add the requester's identity, and the framework's
// replication still reaches the plain methods.
type statService struct {
	base.BaseService
	u *Users
}

func (this *statService) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	this.u = usersArg(sla)
	if this.u == nil {
		return errors.New("ChatStat activated without its *Users argument")
	}
	return this.BaseService.Activate(sla, vnic)
}

func (this *statService) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return this.PatchMessage(pb, vnic, msg)
}

func (this *statService) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return this.PatchMessage(pb, vnic, msg)
}

func (this *statService) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat-users: presence cannot be deleted")
}

// GetMessage serves presence to anyone (plan §5.5), including live registrations.
func (this *statService) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return this.BaseService.Get(pb, vnic)
}

// PatchMessage is the heartbeat and the user's own status change. Only a
// transition is written to the DCache (and so notified); an unchanged
// heartbeat only updates the leader's heartbeat map (plan §5.5, DR17).
func (this *statService) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	userId, err := this.u.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	for _, e := range pb.Elements() {
		s, ok := e.(*l8ai.ChatUserStatus)
		if !ok || s == nil {
			continue
		}
		if err = this.update(s, userId, vnic); err != nil {
			return object.NewError(err.Error())
		}
	}
	return object.New(nil, &l8web.L8Empty{})
}

func (this *statService) update(s *l8ai.ChatUserStatus, userId string, vnic ifs.IVNic) error {
	if s.UserId == "" {
		s.UserId = userId
	}
	if s.UserId != userId || userId == aicommon.TantoUserId {
		return errors.New("chat-users: a user can change only their own status")
	}
	presence := s.Presence
	if presence == l8ai.ChatPresence_CHAT_PRESENCE_UNSPECIFIED || presence == l8ai.ChatPresence_CHAT_PRESENCE_OFFLINE {
		presence = l8ai.ChatPresence_CHAT_PRESENCE_ONLINE // a heartbeat
	}
	this.u.heartbeat(userId)

	next := &l8ai.ChatUserStatus{UserId: userId}
	current, _ := get(this, &l8ai.ChatUserStatus{UserId: userId}, vnic).(*l8ai.ChatUserStatus)
	if current != nil {
		next = proto.Clone(current).(*l8ai.ChatUserStatus)
	}
	customChanged := presence == l8ai.ChatPresence_CHAT_PRESENCE_CUSTOM &&
		(next.CustomText != s.CustomText || next.CustomColor != s.CustomColor)
	if current != nil && current.Presence == presence && !customChanged {
		return nil
	}
	if customChanged {
		next.CustomText = s.CustomText
		next.CustomColor = s.CustomColor
	}
	next.Presence = presence
	next.LastSeenAt = time.Now().Unix()
	if resp := this.BaseService.Post(object.New(nil, next), vnic); resp != nil && resp.Error() != nil {
		return resp.Error()
	}
	if customChanged {
		// The custom status survives a full restart (plan §5.5, Q3).
		if err := vnic.Leader(aicommon.ChatStatDbService, aicommon.ServiceArea, ifs.PUT, next); err != nil {
			vnic.Resources().Logger().Error("chat-users: persist status of ", userId, ": ", err.Error())
		}
	}
	return nil
}

func usersArg(sla *ifs.ServiceLevelAgreement) *Users {
	for _, arg := range sla.Args() {
		if u, ok := arg.(*Users); ok {
			return u
		}
	}
	return nil
}
