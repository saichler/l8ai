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
	"time"

	"github.com/saichler/l8ai/go/types/l8ai"
)

const (
	assignWorkers    = 8
	assignQueueSize  = 200000
	assignAttempts   = 3
	maintenanceEvery = time.Minute
	watchdogEvery    = 5 * time.Second
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
		// The conversation moves to this pod: record it in chat-orm, so the
		// pod it came from does not reclaim it when it comes back.
		header.OwnerPod = this.pod
		taken, err := this.takeOwnership(header)
		if err != nil {
			this.vnic.Resources().Logger().Error("chat: cannot take ownership of ", conversationId, ": ", err.Error())
			continue
		}
		if taken {
			this.writer.header(conversationId)
		}
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

// maintenance evicts the message windows of idle conversations and purges the
// identity cache (plan §4.3, §5.4).
func (this *Instance) maintenance() {
	ticker := time.NewTicker(maintenanceEvery)
	defer ticker.Stop()
	for this.running.Load() {
		<-ticker.C
		this.users.Purge()
		if evicted := this.messages.evictIdle(this.cfg.RingEviction); evicted > 0 {
			this.vnic.Resources().Logger().Info("chat: evicted the message windows of ", evicted, " idle conversations")
		}
	}
}

func (this *conversation) lastUsedAt() time.Time {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	return this.lastUsed
}

// Shutdown is the graceful shutdown for SIGTERM (plan §4.3): stop accepting
// writes and flush the write-behind to chat-orm. The instance keeps its
// conversations: a pod that is restarted reclaims them by its pod name
// (ownerPod) when it comes back, and the router only moves them if the pod
// stays missing longer than ChatConfig.LostGrace (e.g. its node is gone).
// A write that arrives after this point is rejected and the browser resends it.
func (this *Instance) Shutdown() {
	if !this.accepting.CompareAndSwap(true, false) {
		return
	}
	this.writer.drain()
	this.running.Store(false)
}
