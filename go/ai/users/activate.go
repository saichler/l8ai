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
	"reflect"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8services/go/services/recovery"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"github.com/saichler/l8types/go/types/l8web"
	"github.com/saichler/l8utils/go/utils/web"
	"google.golang.org/protobuf/proto"
)

// The chat-users services receive the incoming message (F6).
var (
	_ ifs.IServiceMessageHandler = (*statService)(nil)
	_ ifs.IServiceMessageHandler = (*groupService)(nil)
)

// ActivateUsers activates a chat-users replica: ChatStat and ChatGroup, in one
// service group, plus the leader's presence sweeper. A nil cfg uses the defaults.
func ActivateUsers(cfg *aicommon.ChatConfig, vnic ifs.IVNic) *Users {
	if cfg == nil {
		cfg = aicommon.NewChatConfig()
	}
	aicommon.RegisterTypes(vnic.Resources())
	u := newUsers(cfg, vnic)

	tanto := &l8ai.ChatUserStatus{UserId: aicommon.TantoUserId, Presence: l8ai.ChatPresence_CHAT_PRESENCE_ONLINE}
	activateService(vnic, u, &statService{}, aicommon.ChatStatService, &l8ai.ChatUserStatus{},
		&l8ai.ChatUserStatusList{}, "UserId", []interface{}{tanto}, ifs.POST, ifs.PATCH)
	activateService(vnic, u, &groupService{}, aicommon.ChatGroupService, &l8ai.ChatGroup{},
		&l8ai.ChatGroupList{}, "GroupId", nil, ifs.POST, ifs.PUT, ifs.PATCH)

	go u.sweep()
	return u
}

// activateService activates a chat-users DCache service with its web endpoints.
func activateService(vnic ifs.IVNic, u *Users, handler ifs.IServiceHandler, name string, item, itemList proto.Message,
	primaryKey string, initItems []interface{}, writes ...ifs.Action) {
	ws := web.New(name, aicommon.ServiceArea, 0)
	for _, action := range writes {
		ws.AddEndpoint(item, action, &l8web.L8Empty{})
	}
	ws.AddEndpoint(&l8api.L8Query{}, ifs.GET, itemList)

	vnic.Resources().Registry().Register(handler)
	sla := ifs.NewServiceLevelAgreement(handler, name, aicommon.ServiceArea, true, nil)
	sla.SetServiceGroup(aicommon.ChatUsersGroup)
	sla.SetServiceItem(item)
	sla.SetServiceItemList(itemList)
	sla.SetPrimaryKeys(primaryKey)
	sla.SetVoter(true)
	sla.SetTransactional(false)
	sla.SetWebService(ws)
	sla.SetArgs(u)
	if initItems != nil {
		sla.SetInitItems(initItems)
	}
	if _, err := vnic.Resources().Services().Activate(sla, vnic); err != nil {
		vnic.Resources().Logger().Error("chat-users: activate ", name, ": ", err.Error())
		return
	}
	// A new replica copies the DCache from the others, as base.Activate does.
	go recovery.RecoveryCheck(name, aicommon.ServiceArea, reflect.TypeOf(item).Elem().Name(), vnic)
}
