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

package turn

import (
	"errors"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8web"
)

var errNoMessage = object.NewError("ChatAgent requires the incoming message")

// Service is the ChatAgent service handler. A turn is accepted only from a
// chat instance; it runs in the background and the reply goes back to that
// instance by Unicast, so the dispatching Unicast returns at once.
type Service struct {
	agent *Agent
}

func (this *Service) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	for _, arg := range sla.Args() {
		if a, ok := arg.(*Agent); ok {
			this.agent = a
			return nil
		}
	}
	return errors.New("ChatAgent activated without its *Agent argument")
}

func (this *Service) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	source := msg.Source()
	if _, ok := vnic.Resources().Services().GetParticipants(aicommon.ChatShMsgService, aicommon.ServiceArea)[source]; !ok {
		return object.NewError("ChatAgent: turns are accepted only from a chat instance")
	}
	aaaId := msg.AAAId() // copied: the message is valid only during this call
	for _, e := range pb.Elements() {
		if t, ok := e.(*l8ai.ChatAgentTurn); ok && t != nil && t.TurnId != "" {
			go this.agent.run(t, source, aaaId)
		}
	}
	return object.New(nil, &l8web.L8Empty{})
}

func (this *Service) PutMessage(ifs.IElements, ifs.IVNic, *ifs.Message) ifs.IElements {
	return errNoMessage
}
func (this *Service) PatchMessage(ifs.IElements, ifs.IVNic, *ifs.Message) ifs.IElements {
	return errNoMessage
}
func (this *Service) DeleteMessage(ifs.IElements, ifs.IVNic, *ifs.Message) ifs.IElements {
	return errNoMessage
}
func (this *Service) GetMessage(ifs.IElements, ifs.IVNic, *ifs.Message) ifs.IElements {
	return errNoMessage
}

func (this *Service) DeActivate() error                             { return nil }
func (this *Service) Post(ifs.IElements, ifs.IVNic) ifs.IElements   { return errNoMessage }
func (this *Service) Put(ifs.IElements, ifs.IVNic) ifs.IElements    { return errNoMessage }
func (this *Service) Patch(ifs.IElements, ifs.IVNic) ifs.IElements  { return errNoMessage }
func (this *Service) Delete(ifs.IElements, ifs.IVNic) ifs.IElements { return errNoMessage }
func (this *Service) Get(ifs.IElements, ifs.IVNic) ifs.IElements    { return errNoMessage }
func (this *Service) TransactionConfig() ifs.ITransactionConfig     { return nil }
func (this *Service) WebService() ifs.IWebService                   { return nil }
func (this *Service) Failed(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	vnic.Resources().Logger().Error("ChatAgent: failed to deliver message to ", msg.ServiceName())
	return nil
}
