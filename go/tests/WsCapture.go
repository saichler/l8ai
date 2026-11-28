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

package tests

import (
	"sync"

	"github.com/saichler/l8services/go/services/base"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8notify"
)

// wsCapture stands in for the web server's websocket service ("websock",
// area 0): it records every client notification multicast to it, so a test
// can see which sessions a push was addressed to. The service manager
// activates a new object of the handler's type, so the record is package
// state.
type wsCapture struct{}

var captured = struct {
	mtx  sync.Mutex
	sets []*l8notify.L8NotificationSet
}{}

func activateWsCapture(nic ifs.IVNic) {
	sla := ifs.NewServiceLevelAgreement(&wsCapture{}, base.WsServiceName, base.WsServiceArea, false, nil)
	if _, err := nic.Resources().Services().Activate(sla, nic); err != nil {
		panic(err)
	}
}

// pushesFor returns the captured pushes of a message (by its key).
func pushesFor(messageId string) []*l8notify.L8NotificationSet {
	captured.mtx.Lock()
	defer captured.mtx.Unlock()
	result := make([]*l8notify.L8NotificationSet, 0)
	for _, set := range captured.sets {
		if set.ModelKey == messageId {
			result = append(result, set)
		}
	}
	return result
}

func (this *wsCapture) record(pb ifs.IElements) ifs.IElements {
	for _, e := range pb.Elements() {
		if set, ok := e.(*l8notify.L8NotificationSet); ok && set != nil {
			captured.mtx.Lock()
			captured.sets = append(captured.sets, set)
			captured.mtx.Unlock()
		}
	}
	return nil
}

func (this *wsCapture) Activate(*ifs.ServiceLevelAgreement, ifs.IVNic) error { return nil }
func (this *wsCapture) DeActivate() error                                    { return nil }
func (this *wsCapture) Post(pb ifs.IElements, _ ifs.IVNic) ifs.IElements     { return this.record(pb) }
func (this *wsCapture) Put(pb ifs.IElements, _ ifs.IVNic) ifs.IElements      { return this.record(pb) }
func (this *wsCapture) Patch(pb ifs.IElements, _ ifs.IVNic) ifs.IElements    { return this.record(pb) }
func (this *wsCapture) Delete(pb ifs.IElements, _ ifs.IVNic) ifs.IElements   { return this.record(pb) }
func (this *wsCapture) Get(ifs.IElements, ifs.IVNic) ifs.IElements           { return nil }
func (this *wsCapture) Failed(ifs.IElements, ifs.IVNic, *ifs.Message) ifs.IElements {
	return nil
}
func (this *wsCapture) TransactionConfig() ifs.ITransactionConfig { return nil }
func (this *wsCapture) WebService() ifs.IWebService               { return nil }
