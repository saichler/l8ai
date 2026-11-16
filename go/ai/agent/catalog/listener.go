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

package catalog

import (
	"errors"
	"strings"
	"time"

	"github.com/saichler/l8bus/go/overlay/health"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8web"
)

// endPointsDelay lets the node's own services settle before it asks every
// node to re-send its web descriptors, as the web server does.
const endPointsDelay = 2 * time.Second

// descriptorListener is the descriptor listener of plan §7.3: every service
// with web endpoints multicasts its descriptor (service, area, endpoint types)
// on ifs.WebService, area 0, when it activates. The web server registers its
// REST routes from them; this listener only indexes model → (service, area)
// into the catalog. It serves no HTTP and loads no plugins.
type descriptorListener struct {
	catalog *Catalog
}

// ActivateDescriptorListener starts indexing the cluster's web descriptors
// into c, and asks every node to re-send the descriptors of services that
// were activated before this node.
func ActivateDescriptorListener(c *Catalog, vnic ifs.IVNic) {
	vnic.Resources().Registry().Register(&l8web.L8WebService{})
	vnic.Resources().Registry().Register(&descriptorListener{})
	sla := ifs.NewServiceLevelAgreement(&descriptorListener{}, ifs.WebService, 0, false, nil)
	sla.SetArgs(c)
	if _, err := vnic.Resources().Services().Activate(sla, vnic); err != nil {
		vnic.Resources().Logger().Error("catalog: descriptor listener: ", err.Error())
		return
	}
	go func() {
		time.Sleep(endPointsDelay)
		vnic.Multicast(health.ServiceName, 0, ifs.EndPoints, nil)
	}()
}

func (this *descriptorListener) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	for _, arg := range sla.Args() {
		if c, ok := arg.(*Catalog); ok {
			this.catalog = c
			return nil
		}
	}
	return errors.New("descriptor listener activated without its *Catalog argument")
}

// Post receives one service's web descriptor.
func (this *descriptorListener) Post(pb ifs.IElements, vnic ifs.IVNic) ifs.IElements {
	for _, e := range pb.Elements() {
		if ws, ok := e.(*l8web.L8WebService); ok && ws != nil {
			if model := modelOf(ws); model != "" {
				this.catalog.addDescriptor(model, ws.ServiceName, ws.ServiceArea)
			}
		}
	}
	return object.New(nil, nil)
}

// modelOf returns the model a service serves: the item type of its GET
// response list ("NetworkDeviceList" → "NetworkDevice"), else its POST body.
func modelOf(ws *l8web.L8WebService) string {
	if ep := ws.Endpoints[int32(ifs.GET)]; ep != nil {
		for _, response := range ep.Body2Response {
			if name := strings.TrimSuffix(response, "List"); name != response && name != "" {
				return name
			}
		}
	}
	if ep := ws.Endpoints[int32(ifs.POST)]; ep != nil {
		if ep.PrimaryBody != "" && ep.PrimaryBody != "L8Query" {
			return ep.PrimaryBody
		}
		for body := range ep.Body2Response {
			if body != "L8Query" && body != "L8Empty" {
				return body
			}
		}
	}
	return ""
}

func (this *descriptorListener) DeActivate() error                             { return nil }
func (this *descriptorListener) Put(ifs.IElements, ifs.IVNic) ifs.IElements    { return nil }
func (this *descriptorListener) Patch(ifs.IElements, ifs.IVNic) ifs.IElements  { return nil }
func (this *descriptorListener) Delete(ifs.IElements, ifs.IVNic) ifs.IElements { return nil }
func (this *descriptorListener) Get(ifs.IElements, ifs.IVNic) ifs.IElements    { return nil }
func (this *descriptorListener) TransactionConfig() ifs.ITransactionConfig     { return nil }
func (this *descriptorListener) WebService() ifs.IWebService                   { return nil }
func (this *descriptorListener) Failed(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return nil
}
