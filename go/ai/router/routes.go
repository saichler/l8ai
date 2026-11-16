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
	"errors"
	"runtime/debug"
	"sync"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"github.com/saichler/l8types/go/types/l8web"
)

var errNoMessage = object.NewError("chat router services require the incoming message")

// routeBase holds what the public router services share. They are
// ifs.IServiceMessageHandler handlers: the service manager calls the
// *Message variants, which carry the requester's aaaId.
type routeBase struct {
	r  *Router
	ws ifs.IWebService
}

func (this *routeBase) activate(sla *ifs.ServiceLevelAgreement) error {
	this.ws = sla.WebService()
	for _, arg := range sla.Args() {
		if r, ok := arg.(*Router); ok {
			this.r = r
			return nil
		}
	}
	return errors.New("chat router service activated without its *Router argument")
}

func (this *routeBase) DeActivate() error                             { return nil }
func (this *routeBase) Post(ifs.IElements, ifs.IVNic) ifs.IElements   { return errNoMessage }
func (this *routeBase) Put(ifs.IElements, ifs.IVNic) ifs.IElements    { return errNoMessage }
func (this *routeBase) Patch(ifs.IElements, ifs.IVNic) ifs.IElements  { return errNoMessage }
func (this *routeBase) Delete(ifs.IElements, ifs.IVNic) ifs.IElements { return errNoMessage }
func (this *routeBase) Get(ifs.IElements, ifs.IVNic) ifs.IElements    { return errNoMessage }
func (this *routeBase) TransactionConfig() ifs.ITransactionConfig     { return nil }
func (this *routeBase) WebService() ifs.IWebService                   { return this.ws }
func (this *routeBase) Failed(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	vnic.Resources().Logger().Error("router: failed to deliver message to ", msg.ServiceName())
	return nil
}

func (this *routeBase) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: PUT is not supported")
}

func (this *routeBase) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("router: DELETE is not supported")
}

// guard turns a panic into an error response.
func guard(vnic ifs.IVNic, resp *ifs.IElements) {
	if r := recover(); r != nil {
		vnic.Resources().Logger().Error("router: recovered from panic: ", r, "\n", string(debug.Stack()))
		*resp = object.NewError("router: internal error")
	}
}

var accepted = func() ifs.IElements { return object.New(nil, &l8web.L8Empty{}) }

// routeWrite sends a write to the conversation's owner and returns at once
// (plan §4.1). An unknown conversation is assigned by the leader when create
// is true; a non-leader replica hands such a request to the leader.
func (this *Router) routeWrite(publicService, instanceService string, action ifs.Action, conversationId string,
	elem interface{}, aaaId string, create bool) ifs.IElements {
	if conversationId == "" {
		return object.NewError("router: ConversationId is required")
	}
	owner := this.ownerOf(conversationId)
	if owner == "" {
		if !this.isLeader() {
			return this.vnic.LeaderRequest(publicService, aicommon.ServiceArea, action, elem, this.cfg.ReadTimeoutSeconds, aaaId)
		}
		if !create {
			return object.NewError("router: unknown conversation " + conversationId)
		}
		if owner = this.assignNew(conversationId); owner == "" {
			return object.NewError("router: no chat instance is available")
		}
	}
	this.unicast(owner, instanceService, action, elem, aaaId)
	return accepted()
}

// routeRead sends a read to the conversation's owner and waits, bounded by
// ReadTimeoutSeconds (plan §4.2).
func (this *Router) routeRead(publicService, instanceService, conversationId string, pq *l8api.L8Query, aaaId string) ifs.IElements {
	owner := this.ownerOf(conversationId)
	if owner == "" {
		if !this.isLeader() {
			return this.vnic.LeaderRequest(publicService, aicommon.ServiceArea, ifs.GET, pq, this.cfg.ReadTimeoutSeconds, aaaId)
		}
		return object.NewError("router: unknown conversation " + conversationId)
	}
	return this.vnic.Request(owner, instanceService, aicommon.ServiceArea, ifs.GET, pq, this.cfg.ReadTimeoutSeconds, aaaId)
}

// fanOut sends a read to the given instances in parallel and returns the
// elements of every answer. An instance that does not answer in time is skipped.
func (this *Router) fanOut(targets []string, instanceService string, pq *l8api.L8Query, aaaId string) []interface{} {
	results := make([][]interface{}, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			resp := this.vnic.Request(target, instanceService, aicommon.ServiceArea, ifs.GET, pq, this.cfg.ReadTimeoutSeconds, aaaId)
			if resp == nil || resp.Error() != nil {
				this.vnic.Resources().Logger().Warning("router: instance ", target, " skipped in ", instanceService, " fan-out")
				return
			}
			results[i] = resp.Elements()
		}(i, target)
	}
	wg.Wait()
	merged := make([]interface{}, 0)
	for _, list := range results {
		for _, e := range list {
			if e != nil {
				merged = append(merged, e)
			}
		}
	}
	return merged
}

// parseRead returns the structured query and its parsed form.
func parseRead(pb ifs.IElements, vnic ifs.IVNic) (*l8api.L8Query, ifs.IQuery, error) {
	pq := aicommon.PQueryOf(pb)
	if pq == nil {
		return nil, nil, errors.New("router: a query is required")
	}
	q, err := pb.Query(vnic.Resources())
	if err != nil || q == nil {
		return nil, nil, errors.New("router: invalid query")
	}
	return pq, q, nil
}
