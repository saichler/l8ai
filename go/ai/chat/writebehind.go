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
	"sync"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8utils/go/utils/aggregator"
)

// writeBehind persists the owner's writes to chat-orm (plan §5.4). Messages
// and headers use separate aggregators, so each flush sends one batch per
// service. Header writes are coalesced: a conversation is marked dirty and its
// latest header is queued once per flush interval, however often it changed.
type writeBehind struct {
	inst     *Instance
	interval time.Duration // the aggregators' flush interval
	messages *aggregator.Aggregator
	headers  *aggregator.Aggregator

	mtx   sync.Mutex
	dirty map[string]bool
}

func newWriteBehind(inst *Instance) *writeBehind {
	cfg := inst.cfg
	interval := int64(cfg.FlushInterval / time.Second)
	if interval < 1 {
		interval = 1
	}
	timeout := int64(cfg.ReadTimeoutSeconds)
	return &writeBehind{
		inst:     inst,
		interval: time.Duration(interval) * time.Second,
		messages: aggregator.NewAggregator(inst.vnic, interval, cfg.FlushMaxItems, timeout),
		headers:  aggregator.NewAggregator(inst.vnic, interval, cfg.FlushMaxItems, timeout),
		dirty:    make(map[string]bool),
	}
}

// message queues a new message for chat-orm.
func (this *writeBehind) message(m *l8ai.ChatMessage) {
	this.messages.AddElement(cloneMessage(m), ifs.Leader, "", aicommon.ChatMsgDbService, aicommon.ServiceArea, ifs.POST)
}

// header marks a conversation's header as changed.
func (this *writeBehind) header(conversationId string) {
	this.mtx.Lock()
	this.dirty[conversationId] = true
	this.mtx.Unlock()
}

// queueDirtyHeaders moves the latest header of every dirty conversation to the
// header aggregator. PUT is an upsert in chat-orm.
func (this *writeBehind) queueDirtyHeaders() {
	this.mtx.Lock()
	ids := this.dirty
	this.dirty = make(map[string]bool)
	this.mtx.Unlock()
	for id := range ids {
		c := this.inst.conversation(id)
		if c == nil {
			continue
		}
		c.mtx.Lock()
		h := cloneConversation(c.header)
		c.mtx.Unlock()
		this.headers.AddElement(h, ifs.Leader, "", aicommon.ChatCnvDbService, aicommon.ServiceArea, ifs.PUT)
	}
}

// run queues the dirty headers every flush interval until the instance stops.
func (this *writeBehind) run() {
	ticker := time.NewTicker(this.inst.cfg.FlushInterval)
	defer ticker.Stop()
	for this.inst.running.Load() {
		<-ticker.C
		this.queueDirtyHeaders()
	}
}

// drain queues the remaining headers and waits for both aggregators to send
// what they hold. The aggregator flushes on its own timer, so waiting one
// flush interval (plus a margin) lets that flush happen before shutdown.
func (this *writeBehind) drain() {
	this.queueDirtyHeaders()
	time.Sleep(this.interval + 500*time.Millisecond)
	this.messages.Shutdown()
	this.headers.Shutdown()
}
