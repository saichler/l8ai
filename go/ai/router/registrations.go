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
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// registrationId identifies a browser component's live registration on a
// public service: one record per aaaId + registerId + service.
func registrationId(aaaId, registerId, publicService string) string {
	return aicommon.DeterministicID("reg|" + aaaId + "|" + registerId + "|" + publicService)
}

// instanceService maps a registered model to the instance service holding it.
func (this *Router) instanceService(queryText string) string {
	pb, err := object.NewQuery(queryText, this.vnic.Resources())
	if err != nil {
		return ""
	}
	q, err := pb.Query(this.vnic.Resources())
	if err != nil || q == nil || q.RootType() == nil {
		return ""
	}
	switch q.RootType().TypeName {
	case "ChatMessage":
		return aicommon.ChatShMsgService
	case "ChatConversation":
		return aicommon.ChatShCnvService
	}
	return ""
}

// record keeps a routed registration so it can be replayed when its
// conversation moves or an instance joins (plan §4.1).
func (this *Router) record(pq *l8api.L8Query, aaaId, publicService, conversationId string) {
	row := &l8ai.ChatRegistration{RegistrationId: registrationId(aaaId, pq.RegisterId, publicService),
		AaaId: aaaId, RegisterId: pq.RegisterId, ConversationId: conversationId,
		FanOut: conversationId == "", QueryText: pq.Text}
	this.reg.Post(object.New(nil, row), this.vnic)
}

// unregister passes an unregister to wherever the registration was routed and
// drops its record. Without a record it goes to every instance.
func (this *Router) unregister(pq *l8api.L8Query, aaaId, publicService, instanceService string) {
	id := registrationId(aaaId, pq.RegisterId, publicService)
	targets := this.allInstances()
	resp := this.reg.Get(object.New(nil, &l8ai.ChatRegistration{RegistrationId: id}), this.vnic)
	if resp != nil && resp.Error() == nil {
		if row, ok := resp.Element().(*l8ai.ChatRegistration); ok && row != nil {
			if !row.FanOut {
				if owner := this.ownerOf(row.ConversationId); owner != "" {
					targets = []string{owner}
				}
			}
			this.reg.Delete(object.New(nil, &l8ai.ChatRegistration{RegistrationId: id}), this.vnic)
		}
	}
	for _, target := range targets {
		this.unicast(target, instanceService, ifs.GET, pq, aaaId)
	}
}

// replayMoved re-registers, on their new owners, the registrations of
// conversations that moved.
func (this *Router) replayMoved(moved map[string]string) {
	for _, v := range this.reg.Collect(func(v interface{}) (bool, interface{}) {
		row, ok := v.(*l8ai.ChatRegistration)
		if !ok || row.FanOut {
			return false, v
		}
		_, ok = moved[row.ConversationId]
		return ok, v
	}) {
		row := v.(*l8ai.ChatRegistration)
		this.replay(row, moved[row.ConversationId])
	}
}

// replayFanOut re-registers every fan-out registration on an instance that joined.
func (this *Router) replayFanOut(instance string) {
	for _, v := range this.reg.Collect(func(v interface{}) (bool, interface{}) {
		row, ok := v.(*l8ai.ChatRegistration)
		return ok && row.FanOut, v
	}) {
		this.replay(v.(*l8ai.ChatRegistration), instance)
	}
}

// replay sends a recorded registration to an instance with its original aaaId.
func (this *Router) replay(row *l8ai.ChatRegistration, target string) {
	service := this.instanceService(row.QueryText)
	if service == "" || target == "" {
		return
	}
	pq := &l8api.L8Query{Text: row.QueryText, Register: l8api.L8Register_L8_REGISTER_REGISTER,
		RegisterId: row.RegisterId, AaaId: row.AaaId}
	this.unicast(target, service, ifs.GET, pq, row.AaaId)
}
