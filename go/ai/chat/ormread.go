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
	"errors"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
)

// ormGet sends a read to a chat-orm service and returns the non-nil elements.
func (this *Instance) ormGet(serviceName string, query interface{}) ([]interface{}, error) {
	return aicommon.LeaderGet(this.vnic, serviceName, query, this.cfg.ReadTimeoutSeconds)
}

// loadHeader reads one conversation header from chat-orm.
func (this *Instance) loadHeader(conversationId string) (*l8ai.ChatConversation, error) {
	elems, err := this.ormGet(aicommon.ChatCnvDbService,
		"select * from ChatConversation where conversationId="+conversationId)
	if err != nil {
		return nil, err
	}
	for _, e := range elems {
		if h, ok := e.(*l8ai.ChatConversation); ok && h.ConversationId == conversationId {
			return h, nil
		}
	}
	return nil, errors.New("chat: conversation " + conversationId + " not found in chat-orm")
}
