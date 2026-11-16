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
	"runtime/debug"

	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

// errNoMessage is returned by the plain CRUD methods. Chat instance services
// are ifs.IServiceMessageHandler handlers: the service manager always calls the
// *Message variants, which carry the requester's aaaId and the sending node.
var errNoMessage = object.NewError("chat instance services require the incoming message")

// handlerBase holds what the three chat instance services share: the
// instance, and the IServiceHandler methods that do not depend on the service.
type handlerBase struct {
	inst *Instance
}

func (this *handlerBase) activate(sla *ifs.ServiceLevelAgreement) error {
	for _, arg := range sla.Args() {
		if inst, ok := arg.(*Instance); ok {
			this.inst = inst
			return nil
		}
	}
	return errors.New("chat instance service activated without its *Instance argument")
}

func (this *handlerBase) DeActivate() error { return nil }

func (this *handlerBase) Post(ifs.IElements, ifs.IVNic) ifs.IElements   { return errNoMessage }
func (this *handlerBase) Put(ifs.IElements, ifs.IVNic) ifs.IElements    { return errNoMessage }
func (this *handlerBase) Patch(ifs.IElements, ifs.IVNic) ifs.IElements  { return errNoMessage }
func (this *handlerBase) Delete(ifs.IElements, ifs.IVNic) ifs.IElements { return errNoMessage }
func (this *handlerBase) Get(ifs.IElements, ifs.IVNic) ifs.IElements    { return errNoMessage }

func (this *handlerBase) Failed(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	vnic.Resources().Logger().Error("chat: failed to deliver message to ", msg.ServiceName())
	return nil
}

// TransactionConfig is nil: chat instances are in-memory single writers (plan §4.3).
func (this *handlerBase) TransactionConfig() ifs.ITransactionConfig { return nil }

// WebService is nil: chat instances are internal and reached only through the router.
func (this *handlerBase) WebService() ifs.IWebService { return nil }

// guard turns a panic in a request handler into an error response, so one bad
// request cannot kill the instance (plan §5.4).
func guard(vnic ifs.IVNic, resp *ifs.IElements) {
	if r := recover(); r != nil {
		vnic.Resources().Logger().Error("chat: recovered from panic: ", r, "\n", string(debug.Stack()))
		*resp = object.NewError("chat: internal error")
	}
}
