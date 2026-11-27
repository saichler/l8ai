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
	"strconv"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
)

const (
	reclaimPage       = 500
	reclaimRetryEvery = 2 * time.Second
)

// podName is this instance's stable identity: the pod name, which survives a
// restart (StatefulSet), unlike the vnic UUID. POD_NAME (downward API) wins;
// the host name is the fallback (it is the pod name in Kubernetes).
func podName() string {
	if name := os.Getenv("POD_NAME"); name != "" {
		return name
	}
	name, _ := os.Hostname()
	return name
}

// reclaim loads, before the instance's services are activated, every
// conversation chat-orm records as owned by this pod (ownerPod): a restarted
// chat pod takes its own conversations back without the router moving
// anything, and it only shows up as a participant once it holds them.
// Pages are read by key ("conversationId > last"). chat-orm is retried until
// it answers: an instance cannot serve without its conversations.
func (this *Instance) reclaim() {
	if this.pod == "" {
		this.vnic.Resources().Logger().Error("chat: no pod name, conversations are not reclaimed on restart")
		return
	}
	last := ""
	total := 0
	for {
		query := "select * from ChatConversation where ownerPod=" + this.pod
		if last != "" {
			query += " and conversationId>" + last
		}
		query += " sort-by conversationId limit " + strconv.Itoa(reclaimPage) + " page 0"
		elems, err := this.ormGet(aicommon.ChatCnvDbService, query)
		if err != nil {
			this.vnic.Resources().Logger().Warning("chat: reclaim from chat-orm: ", err.Error(), ", retrying")
			time.Sleep(reclaimRetryEvery)
			continue
		}
		for _, e := range elems {
			h, ok := e.(*l8ai.ChatConversation)
			if !ok || h.ConversationId == "" {
				continue
			}
			if h.ConversationId > last {
				last = h.ConversationId
			}
			if h.OwnerPod == this.pod && this.takeOwnership(h) {
				total++
			}
		}
		if len(elems) < reclaimPage {
			break
		}
	}
	this.vnic.Resources().Logger().Info("chat: pod ", this.pod, " reclaimed ", total, " conversations")
}
