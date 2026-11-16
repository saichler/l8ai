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
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

const (
	assignWorkers      = 8
	assignQueueSize    = 200000
	assignAttempts     = 3
	maintenanceEvery   = time.Minute
	watchdogEvery      = 5 * time.Second
	memoryGuardPercent = 80 // drop idle rings above this % of the container memory limit
	memoryGuardDropPc  = 25 // share of the loaded rings dropped per check while above it
)

// assignById queues a conversation whose header must be loaded from chat-orm
// (the router reassigned it after an instance was lost, plan §4.3).
func (this *Instance) assignById(conversationId string) {
	if conversationId == "" {
		return
	}
	select {
	case this.assignQueue <- conversationId:
	default:
		this.vnic.Resources().Logger().Error("chat: assignment queue is full, dropped ", conversationId)
	}
}

func (this *Instance) assignWorker() {
	for this.running.Load() {
		conversationId, ok := <-this.assignQueue
		if !ok {
			return
		}
		if this.conversation(conversationId) != nil {
			continue
		}
		var header *l8ai.ChatConversation
		var err error
		for attempt := 1; attempt <= assignAttempts; attempt++ {
			if header, err = this.loadHeader(conversationId); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if err != nil {
			this.vnic.Resources().Logger().Error("chat: cannot take ownership of ", conversationId, ": ", err.Error())
			continue
		}
		this.takeOwnership(header)
	}
}

// watchdog runs the turn watchdog until the instance stops.
func (this *Instance) watchdog() {
	ticker := time.NewTicker(watchdogEvery)
	defer ticker.Stop()
	for this.running.Load() {
		<-ticker.C
		this.checkTurns()
	}
}

// maintenance evicts idle rings, applies the memory guard and purges the
// identity cache (plan §4.3, §5.4).
func (this *Instance) maintenance() {
	ticker := time.NewTicker(maintenanceEvery)
	defer ticker.Stop()
	for this.running.Load() {
		<-ticker.C
		this.users.Purge()
		this.evictIdleRings()
		if overMemoryGuard() {
			this.evictOldestRings()
		}
	}
}

func (this *Instance) evictIdleRings() {
	for _, c := range this.owned() {
		c.mtx.Lock()
		if c.ringLoaded && c.turn == nil && time.Since(c.lastUsed) > this.cfg.RingEviction {
			this.dropRing(c)
		}
		c.mtx.Unlock()
	}
}

// evictOldestRings drops the least recently used share of the loaded rings.
func (this *Instance) evictOldestRings() {
	loaded := make([]*conversation, 0)
	for _, c := range this.owned() {
		c.mtx.Lock()
		if c.ringLoaded && c.turn == nil {
			loaded = append(loaded, c)
		}
		c.mtx.Unlock()
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].lastUsedAt().Before(loaded[j].lastUsedAt()) })
	drop := len(loaded) * memoryGuardDropPc / 100
	if drop == 0 && len(loaded) > 0 {
		drop = 1
	}
	for _, c := range loaded[:drop] {
		c.mtx.Lock()
		if c.turn == nil {
			this.dropRing(c)
		}
		c.mtx.Unlock()
	}
	this.vnic.Resources().Logger().Info("chat: memory guard dropped ", drop, " rings")
}

func (this *conversation) lastUsedAt() time.Time {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	return this.lastUsed
}

// overMemoryGuard is true when the heap is above memoryGuardPercent of the
// container's memory limit. Without a cgroup limit the guard is off.
func overMemoryGuard() bool {
	limit := memoryLimit()
	if limit == 0 {
		return false
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapInuse > limit/100*memoryGuardPercent
}

func memoryLimit() uint64 {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err == nil && v > 0 && v < 1<<62 {
			return v
		}
	}
	return 0
}

// Shutdown is the graceful shutdown for SIGTERM (plan §4.3): stop accepting
// writes, flush the write-behind to chat-orm, then release the owned
// conversations to the router, which reassigns them. A write that arrives
// after this point is rejected and the browser resends it to the new owner.
func (this *Instance) Shutdown() {
	if !this.accepting.CompareAndSwap(true, false) {
		return
	}
	this.writer.drain()
	this.release()
	this.running.Store(false)
}

// release hands the owned conversations back to the router leader. The router
// treats a DELETE of index rows by their owner as a release (Phase 7).
func (this *Instance) release() {
	local := this.vnic.Resources().SysConfig().LocalUuid
	rows := &l8ai.ChatConvIndexList{}
	for _, c := range this.owned() {
		c.mtx.Lock()
		rows.List = append(rows.List, &l8ai.ChatConvIndex{ConversationId: c.header.ConversationId, OwnerUuid: local})
		c.mtx.Unlock()
	}
	if len(rows.List) == 0 {
		return
	}
	resp := this.vnic.LeaderRequest(aicommon.ChatIdxService, aicommon.ServiceArea, ifs.DELETE, rows.List,
		this.cfg.ReadTimeoutSeconds)
	if resp != nil && resp.Error() != nil {
		this.vnic.Resources().Logger().Error("chat: release: ", resp.Error().Error())
	}
}
