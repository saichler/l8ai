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
	"strconv"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

const (
	releaseQueueSize = 200000
	releaseDebounce  = 200 * time.Millisecond
	coldStartPage    = 500
	monitorEvery     = 5 * time.Second
	replayDelay      = 5 * time.Second // lets a new owner load the moved headers first
)

// indexCallback turns a DELETE of index rows into a release (plan §4.3): the
// rows are not deleted, their conversations are reassigned to other instances.
type indexCallback struct {
	router *Router
}

func (this *indexCallback) Before(elem interface{}, action ifs.Action, isNotification bool, vnic ifs.IVNic) (interface{}, bool, error) {
	if action != ifs.DELETE || isNotification {
		return nil, true, nil
	}
	if row, ok := elem.(*l8ai.ChatConvIndex); ok && row != nil {
		select {
		case this.router.releases <- row:
		default:
			vnic.Resources().Logger().Error("router: release queue is full, dropped ", row.ConversationId)
		}
	}
	return nil, false, nil
}

func (this *indexCallback) After(elem interface{}, action ifs.Action, notify bool, vnic ifs.IVNic) (interface{}, bool, error) {
	return nil, true, nil
}

// releaseWorker collects released rows for a short while and reassigns them
// in one batch, away from the releasing instance.
func (this *Router) releaseWorker() {
	for {
		row, ok := <-this.releases
		if !ok {
			return
		}
		batch := []*l8ai.ChatConvIndex{row}
		timer := time.After(releaseDebounce)
	collect:
		for {
			select {
			case r := <-this.releases:
				batch = append(batch, r)
			case <-timer:
				break collect
			}
		}
		if !this.isLeader() {
			continue
		}
		rows := make([]*l8ai.ChatConvIndex, 0, len(batch))
		for _, r := range batch {
			// Only the current owner can release a conversation.
			if r.OwnerUuid != "" && this.ownerOf(r.ConversationId) == r.OwnerUuid {
				this.mtx.Lock()
				this.releasing[r.OwnerUuid] = true
				this.mtx.Unlock()
				rows = append(rows, &l8ai.ChatConvIndex{ConversationId: r.ConversationId, OwnerUuid: r.OwnerUuid})
			}
		}
		this.reassign(rows)
	}
}

// reassign moves the rows' conversations to the least-loaded instances: the
// index is updated, each new owner gets its rows (it loads the headers from
// chat-orm) and, a little later, the conversations' live registrations.
func (this *Router) reassign(rows []*l8ai.ChatConvIndex) {
	if len(rows) == 0 {
		return
	}
	candidates := this.instances()
	byOwner := make(map[string][]*l8ai.ChatConvIndex)
	updated := make([]*l8ai.ChatConvIndex, 0, len(rows))
	now := time.Now().Unix()
	this.mtx.Lock()
	for _, row := range rows {
		owner := this.leastLoaded(candidates)
		if owner == "" {
			this.vnic.Resources().Logger().Error("router: no chat instance to take ", row.ConversationId)
			continue
		}
		if this.counts[row.OwnerUuid] > 0 {
			this.counts[row.OwnerUuid]--
		}
		this.counts[owner]++
		moved := &l8ai.ChatConvIndex{ConversationId: row.ConversationId, OwnerUuid: owner, AssignedAt: now}
		updated = append(updated, moved)
		byOwner[owner] = append(byOwner[owner], moved)
	}
	this.mtx.Unlock()
	if len(updated) == 0 {
		return
	}
	this.idx.Post(object.New(nil, updated), this.vnic)
	moved := make(map[string]string, len(updated))
	for owner, list := range byOwner {
		this.unicast(owner, aicommon.ChatShCnvService, ifs.POST, list)
		for _, row := range list {
			moved[row.ConversationId] = owner
		}
	}
	go func() {
		time.Sleep(replayDelay)
		this.replayMoved(moved)
	}()
}

// reassignLost moves every conversation of an instance that left.
func (this *Router) reassignLost(lost string) {
	rows := make([]*l8ai.ChatConvIndex, 0)
	for _, v := range this.idx.Collect(func(v interface{}) (bool, interface{}) {
		row, ok := v.(*l8ai.ChatConvIndex)
		return ok && row.OwnerUuid == lost, v
	}) {
		rows = append(rows, v.(*l8ai.ChatConvIndex))
	}
	this.mtx.Lock()
	delete(this.counts, lost)
	delete(this.releasing, lost)
	this.mtx.Unlock()
	this.vnic.Resources().Logger().Info("router: instance ", lost, " left, reassigning ", len(rows), " conversations")
	for _, row := range rows {
		row.OwnerUuid = "" // its count was dropped with the instance
	}
	this.reassign(rows)
}

// coldStart assigns every conversation in chat-orm to the instances when the
// index is empty, sending each instance its headers in batches (plan §4.3).
// Pages are read by key ("conversationId > last"), so no offset paging is used.
// Returns false when chat-orm could not be read; the monitor retries.
func (this *Router) coldStart() bool {
	last := ""
	total := 0
	for {
		query := "select * from ChatConversation"
		if last != "" {
			query += " where conversationId>" + last
		}
		query += " sort-by conversationId limit " + strconv.Itoa(coldStartPage) + " page 0"
		elems, err := aicommon.LeaderGet(this.vnic, aicommon.ChatCnvDbService, query, this.cfg.ReadTimeoutSeconds)
		if err != nil {
			this.vnic.Resources().Logger().Error("router: cold start: ", err.Error())
			return false
		}
		this.assignHeaders(elems)
		for _, e := range elems {
			if h, ok := e.(*l8ai.ChatConversation); ok && h.ConversationId > last {
				last = h.ConversationId
			}
		}
		total += len(elems)
		if len(elems) < coldStartPage {
			break
		}
	}
	this.vnic.Resources().Logger().Info("router: cold start assigned ", total, " conversations")
	return true
}

// assignHeaders assigns one page of headers and sends each owner its share.
func (this *Router) assignHeaders(elems []interface{}) {
	candidates := this.instances()
	byOwner := make(map[string][]*l8ai.ChatConversation)
	rows := make([]*l8ai.ChatConvIndex, 0, len(elems))
	now := time.Now().Unix()
	this.mtx.Lock()
	for _, e := range elems {
		h, ok := e.(*l8ai.ChatConversation)
		if !ok || this.ownerOf(h.ConversationId) != "" {
			continue
		}
		owner := this.leastLoaded(candidates)
		if owner == "" {
			break
		}
		this.counts[owner]++
		rows = append(rows, &l8ai.ChatConvIndex{ConversationId: h.ConversationId, OwnerUuid: owner, AssignedAt: now})
		byOwner[owner] = append(byOwner[owner], h)
	}
	this.mtx.Unlock()
	if len(rows) > 0 {
		this.idx.Post(object.New(nil, rows), this.vnic)
	}
	for owner, headers := range byOwner {
		this.unicast(owner, aicommon.ChatShCnvService, ifs.POST, headers)
	}
}

// monitor runs on every replica. The leader rebuilds its counts when it takes
// over, runs the cold start once, reassigns the conversations of instances
// that left and replays fan-out registrations to instances that joined.
func (this *Router) monitor() {
	ticker := time.NewTicker(monitorEvery)
	defer ticker.Stop()
	for {
		<-ticker.C
		this.monitorTick()
	}
}

func (this *Router) monitorTick() {
	current := make(map[string]bool)
	for _, id := range this.allInstances() {
		current[id] = true
	}
	leader := this.isLeader()
	becameLeader := leader && !this.wasLeader
	this.wasLeader = leader
	if !leader {
		this.known = current
		return
	}
	if becameLeader {
		this.rebuildCounts()
	}
	// The cold start skips conversations already in the index, so it is
	// safe to run while new conversations are being created.
	if !this.coldStarted && len(current) > 0 {
		this.coldStarted = this.coldStart()
	}
	this.mtx.Lock()
	lost := make([]string, 0)
	for owner := range this.counts {
		if !current[owner] {
			lost = append(lost, owner)
		}
	}
	this.mtx.Unlock()
	for _, owner := range lost {
		this.reassignLost(owner)
	}
	for id := range current {
		if !this.known[id] {
			this.replayFanOut(id)
		}
	}
	this.known = current
}
