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

// Package router implements the chat router (plan §4.1–4.2): an index from
// conversation to owner chat instance, and the public chat services that
// route writes (Unicast, no wait) and reads (bounded Request, or a fan-out to
// every instance). It never resolves identity or inspects content.
package router

import (
	"sort"
	"sync"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

// Router is one chat-router replica. Every replica holds the index and the
// registration records (DCaches); only the leader assigns owners.
type Router struct {
	cfg  *aicommon.ChatConfig
	vnic ifs.IVNic

	idx ifs.IServiceHandlerCache // ChatIdx: conversationId → owner instance
	reg ifs.IServiceHandlerCache // ChatReg: routed live registrations

	mtx       sync.Mutex
	counts    map[string]int  // leader: owned conversations per instance
	known     map[string]bool // instances seen at the last monitor tick
	releasing map[string]bool // instances that released their conversations
	wasLeader bool
	synced    bool // leader: the index was rebuilt from the instances

	missingSince map[string]time.Time // leader: when an owning instance went missing (guarded by mtx)
	pendingSync  map[string]bool      // leader: joined instances whose conversations are not indexed yet

	releases chan *l8ai.ChatConvIndex
}

func newRouter(cfg *aicommon.ChatConfig, vnic ifs.IVNic) *Router {
	return &Router{cfg: cfg, vnic: vnic, counts: make(map[string]int), known: make(map[string]bool),
		releasing: make(map[string]bool), releases: make(chan *l8ai.ChatConvIndex, releaseQueueSize),
		missingSince: make(map[string]time.Time), pendingSync: make(map[string]bool)}
}

func (this *Router) isLeader() bool {
	return aicommon.IsLeader(this.vnic, aicommon.ChatIdxService)
}

// ownerOf returns the owner instance of a conversation, or "".
func (this *Router) ownerOf(conversationId string) string {
	if conversationId == "" {
		return ""
	}
	resp := this.idx.Get(object.New(nil, &l8ai.ChatConvIndex{ConversationId: conversationId}), this.vnic)
	if resp == nil || resp.Error() != nil {
		return ""
	}
	if row, ok := resp.Element().(*l8ai.ChatConvIndex); ok && row != nil {
		return row.OwnerUuid
	}
	return ""
}

// instances returns the chat instances that can take conversations, sorted.
func (this *Router) instances() []string {
	participants := aicommon.Participants(this.vnic.Resources(), aicommon.ChatShCnvService)
	this.mtx.Lock()
	defer this.mtx.Unlock()
	result := make([]string, 0, len(participants))
	for id := range participants {
		if !this.releasing[id] {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

// allInstances returns every chat instance, including releasing ones (reads).
func (this *Router) allInstances() []string {
	participants := aicommon.Participants(this.vnic.Resources(), aicommon.ChatShCnvService)
	result := make([]string, 0, len(participants))
	for id := range participants {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// leastLoaded picks the instance with the fewest conversations. The caller
// holds this.mtx. Returns "" when no instance is available.
func (this *Router) leastLoaded(candidates []string) string {
	best := ""
	for _, id := range candidates {
		if this.releasing[id] {
			continue
		}
		if best == "" || this.counts[id] < this.counts[best] {
			best = id
		}
	}
	return best
}

// assignNew makes the least-loaded instance the owner of a new conversation.
// Leader only; returns the existing owner if another request assigned it first.
func (this *Router) assignNew(conversationId string) string {
	candidates := this.instances()
	this.mtx.Lock()
	defer this.mtx.Unlock()
	if owner := this.ownerOf(conversationId); owner != "" {
		return owner
	}
	owner := this.leastLoaded(candidates)
	if owner == "" {
		return ""
	}
	row := &l8ai.ChatConvIndex{ConversationId: conversationId, OwnerUuid: owner, AssignedAt: time.Now().Unix()}
	this.idx.Post(object.New(nil, row), this.vnic)
	this.counts[owner]++
	return owner
}

// rebuildCounts recounts the conversations per instance from the index.
// Called when this replica becomes the leader.
func (this *Router) rebuildCounts() {
	counts := make(map[string]int)
	for _, v := range this.idx.All() {
		if row, ok := v.(*l8ai.ChatConvIndex); ok && row.OwnerUuid != "" {
			counts[row.OwnerUuid]++
		}
	}
	this.mtx.Lock()
	this.counts = counts
	this.mtx.Unlock()
}

// unicast sends a write to an instance without waiting. A nil aaaId list is
// used for the router's own assignments.
func (this *Router) unicast(dest, serviceName string, action ifs.Action, payload interface{}, aaaId ...string) {
	if err := this.vnic.Unicast(dest, serviceName, aicommon.ServiceArea, action, payload, aaaId...); err != nil {
		this.vnic.Resources().Logger().Error("router: unicast to ", dest, " ", serviceName, ": ", err.Error())
	}
}
