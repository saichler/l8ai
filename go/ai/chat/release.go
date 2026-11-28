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
	"sort"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
)

// release gives up to count idle conversations away for rebalancing (the
// router leader asked, ChatRebalance). A conversation qualifies when no agent
// turn is running or pending and it has not been used for idle. For each one:
//
//  1. it leaves the owned set, so a write that reaches this instance now is
//     rejected (the browser resends it, and the router sends it to the new
//     owner once the index moved);
//  2. after one write-behind flush, so every queued message and header write
//     has reached chat-orm, its header is written with ownerPod="" and the
//     write is awaited: the new owner loads exactly this header, and this pod
//     does not reclaim the conversation if it restarts;
//  3. its header and ring leave memory.
//
// If chat-orm does not take the headers, the conversations are owned again
// and nothing is released. Returns the released conversations as index rows.
func (this *Instance) release(count int, idle time.Duration) ifs.IElements {
	local := this.vnic.Resources().SysConfig().LocalUuid
	if count <= 0 || !this.accepting.Load() {
		return object.New(nil, []*l8ai.ChatConvIndex{})
	}
	candidates := make([]*conversation, 0)
	for _, c := range this.owned() {
		c.mtx.Lock()
		if c.turn == nil && c.pending == nil && time.Since(c.lastUsed) >= idle {
			candidates = append(candidates, c)
		}
		c.mtx.Unlock()
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].lastUsedAt().Before(candidates[j].lastUsedAt()) })
	if len(candidates) > count {
		candidates = candidates[:count]
	}

	// 1. Leave the owned set.
	taken := make([]*conversation, 0, len(candidates))
	this.mtx.Lock()
	for _, c := range candidates {
		id := c.header.ConversationId // immutable
		if this.convs[id] == c {
			delete(this.convs, id)
			taken = append(taken, c)
		}
	}
	this.mtx.Unlock()
	if len(taken) == 0 {
		return object.New(nil, []*l8ai.ChatConvIndex{})
	}

	// 2. Let queued writes reach chat-orm, then write the released headers.
	time.Sleep(this.writer.interval + 500*time.Millisecond)
	headers := make([]*l8ai.ChatConversation, 0, len(taken))
	for _, c := range taken {
		c.mtx.Lock()
		h := cloneConversation(c.header)
		c.mtx.Unlock()
		h.OwnerPod = ""
		headers = append(headers, h)
	}
	resp := this.vnic.LeaderRequest(aicommon.ChatCnvDbService, aicommon.ServiceArea, ifs.PUT, headers,
		this.cfg.ReadTimeoutSeconds)
	if resp == nil || resp.Error() != nil {
		reason := "no response"
		if resp != nil {
			reason = resp.Error().Error()
		}
		this.mtx.Lock()
		for _, c := range taken {
			if _, ok := this.convs[c.header.ConversationId]; !ok {
				this.convs[c.header.ConversationId] = c
			}
		}
		this.mtx.Unlock()
		this.vnic.Resources().Logger().Error("chat: rebalance: chat-orm did not take the released headers (",
			reason, "); ", len(taken), " conversations stay here")
		return object.New(nil, []*l8ai.ChatConvIndex{})
	}

	// 3. Leave memory.
	rows := make([]*l8ai.ChatConvIndex, 0, len(taken))
	for _, c := range taken {
		c.mtx.Lock()
		this.messages.dropConversation(c.header.ConversationId)
		this.cnvCache.Delete(cloneConversation(c.header), false)
		rows = append(rows, &l8ai.ChatConvIndex{ConversationId: c.header.ConversationId, OwnerUuid: local})
		c.mtx.Unlock()
	}
	this.vnic.Resources().Logger().Info("chat: rebalance: released ", len(rows), " idle conversations (asked for ",
		count, ")")
	return object.New(nil, rows)
}
