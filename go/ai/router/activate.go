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
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8services/go/services/base"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"github.com/saichler/l8types/go/types/l8web"
	"github.com/saichler/l8utils/go/utils/web"
	"google.golang.org/protobuf/proto"
)

// The public router services receive the incoming message (F6).
var (
	_ ifs.IServiceMessageHandler = (*msgRoute)(nil)
	_ ifs.IServiceMessageHandler = (*convRoute)(nil)
	_ ifs.IServiceMessageHandler = (*peerRoute)(nil)
	_ ifs.IServiceMessageHandler = (*syncRoute)(nil)
)

// ActivateRouter activates a chat-router replica: the ChatIdx and ChatReg
// DCaches and the public ChatMsg, ChatConv, ChatPeer and ChatSync services.
// All of them are in one service group, so the replicas elect one leader.
// A nil cfg uses the defaults.
func ActivateRouter(cfg *aicommon.ChatConfig, vnic ifs.IVNic) *Router {
	if cfg == nil {
		cfg = aicommon.NewChatConfig()
	}
	aicommon.RegisterTypes(vnic.Resources())
	r := newRouter(cfg, vnic)
	r.idx = activateDCache(vnic, aicommon.ChatIdxService, &l8ai.ChatConvIndex{}, &l8ai.ChatConvIndexList{},
		"ConversationId", &indexCallback{router: r})
	r.reg = activateDCache(vnic, aicommon.ChatRegService, &l8ai.ChatRegistration{}, &l8ai.ChatRegistrationList{},
		"RegistrationId", nil)

	writable := func(item proto.Message, actions ...ifs.Action) func(ws ifs.IWebService) {
		return func(ws ifs.IWebService) {
			for _, action := range actions {
				ws.AddEndpoint(item, action, &l8web.L8Empty{})
			}
		}
	}
	activateRoute(vnic, r, &msgRoute{}, aicommon.ChatMsgService, &l8ai.ChatMessageList{},
		writable(&l8ai.ChatMessage{}, ifs.POST))
	activateRoute(vnic, r, &convRoute{}, aicommon.ChatConvService, &l8ai.ChatConversationList{},
		writable(&l8ai.ChatConversation{}, ifs.POST, ifs.PATCH))
	activateRoute(vnic, r, &peerRoute{}, aicommon.ChatPeerService, &l8ai.ChatPeerList{}, nil)
	activateRoute(vnic, r, &syncRoute{}, aicommon.ChatSyncService, &l8ai.ChatSyncList{}, nil)

	go r.releaseWorker()
	go r.monitor()
	return r
}

// activateDCache activates an internal router DCache: a full copy on every
// replica, written by the leader and replicated by the framework.
func activateDCache(vnic ifs.IVNic, name string, item, itemList interface{}, primaryKey string,
	callback ifs.IServiceCallback) ifs.IServiceHandlerCache {
	sla := ifs.NewServiceLevelAgreement(&base.BaseService{}, name, aicommon.ServiceArea, true, callback)
	sla.SetServiceGroup(aicommon.ChatRouterGroup)
	sla.SetServiceItem(item)
	sla.SetServiceItemList(itemList)
	sla.SetPrimaryKeys(primaryKey)
	sla.SetVoter(true)
	sla.SetTransactional(false)
	h, err := base.Activate(sla, vnic)
	if err != nil {
		vnic.Resources().Logger().Error("router: activate ", name, ": ", err.Error())
	}
	return h.(ifs.IServiceHandlerCache)
}

// activateRoute activates a public router service with its web endpoints:
// GET with an L8Query, plus the write endpoints added by endpoints.
func activateRoute(vnic ifs.IVNic, r *Router, handler ifs.IServiceHandler, name string, itemList proto.Message,
	endpoints func(ifs.IWebService)) {
	ws := web.New(name, aicommon.ServiceArea, 0)
	if endpoints != nil {
		endpoints(ws)
	}
	ws.AddEndpoint(&l8api.L8Query{}, ifs.GET, itemList)

	vnic.Resources().Registry().Register(handler)
	sla := ifs.NewServiceLevelAgreement(handler, name, aicommon.ServiceArea, true, nil)
	sla.SetServiceGroup(aicommon.ChatRouterGroup)
	sla.SetServiceItemList(itemList)
	sla.SetWebService(ws)
	sla.SetArgs(r)
	if _, err := vnic.Resources().Services().Activate(sla, vnic); err != nil {
		vnic.Resources().Logger().Error("router: activate ", name, ": ", err.Error())
	}
}
