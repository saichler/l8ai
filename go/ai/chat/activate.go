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
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

// The chat instance services receive the incoming message (F6).
var (
	_ ifs.IServiceMessageHandler = (*messagesService)(nil)
	_ ifs.IServiceMessageHandler = (*conversationsService)(nil)
	_ ifs.IServiceMessageHandler = (*syncService)(nil)
)

// ActivateChat activates a chat instance on the vnic: ChatShMsg, ChatShCnv and
// ChatShSync, plus the write-behind, turn watchdog and maintenance loops.
// A nil cfg uses the defaults. Call Shutdown on SIGTERM.
func ActivateChat(cfg *aicommon.ChatConfig, vnic ifs.IVNic) *Instance {
	if cfg == nil {
		cfg = aicommon.NewChatConfig()
	}
	aicommon.RegisterTypes(vnic.Resources())
	inst := newInstance(cfg, vnic)
	// Reload this pod's own conversations before the services are activated,
	// so the router only sees the instance once it holds them.
	inst.reclaim()

	activateService(vnic, inst, &messagesService{}, aicommon.ChatShMsgService,
		&l8ai.ChatMessage{}, &l8ai.ChatMessageList{}, "MessageId")
	activateService(vnic, inst, &conversationsService{}, aicommon.ChatShCnvService,
		&l8ai.ChatConversation{}, &l8ai.ChatConversationList{}, "ConversationId")
	activateService(vnic, inst, &syncService{}, aicommon.ChatShSyncService,
		&l8ai.ChatSync{}, &l8ai.ChatSyncList{}, "ConversationId")

	for i := 0; i < assignWorkers; i++ {
		go inst.assignWorker()
	}
	go inst.writer.run()
	go inst.watchdog()
	go inst.maintenance()
	return inst
}

// activateService activates one chat instance service. The handler reaches
// the shared Instance through the SLA arguments. The services are activated as
// stateful only so that the framework registers this node as a participant:
// the instances publish their services in their health record, which the
// router reads (common.Participants). The instance keeps its own in-memory
// state; nothing uses the services' leader.
func activateService(vnic ifs.IVNic, inst *Instance, handler ifs.IServiceHandler, name string,
	item, itemList interface{}, primaryKey string) {
	vnic.Resources().Registry().Register(handler)
	sla := ifs.NewServiceLevelAgreement(handler, name, aicommon.ServiceArea, true, nil)
	sla.SetServiceItem(item)
	sla.SetServiceItemList(itemList)
	sla.SetPrimaryKeys(primaryKey)
	sla.SetArgs(inst)
	if _, err := vnic.Resources().Services().Activate(sla, vnic); err != nil {
		vnic.Resources().Logger().Error("chat: activate ", name, ": ", err.Error())
	}
}
