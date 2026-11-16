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

package turn

import (
	"errors"
	"strconv"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

// The agent reads a conversation through the router with the requester's
// session, like the requester's browser; the owner instance is the source of
// truth, so any agent instance can serve any turn (assessment §3.3).

func (this *Agent) routerGet(service, query, aaaId string) ([]interface{}, error) {
	resp := this.vnic.LeaderRequest(service, aicommon.ServiceArea, ifs.GET, query,
		this.settings.Chat.ReadTimeoutSeconds, aaaId)
	if resp == nil {
		return nil, errors.New("no response from " + service)
	}
	if resp.Error() != nil {
		return nil, resp.Error()
	}
	return resp.Elements(), nil
}

// fetchHeader reads the conversation header (summary, summary position).
func (this *Agent) fetchHeader(conversationId, aaaId string) (*l8ai.ChatConversation, error) {
	elems, err := this.routerGet(aicommon.ChatConvService,
		"select * from ChatConversation where conversationId="+conversationId, aaaId)
	if err != nil {
		return nil, err
	}
	for _, e := range elems {
		if h, ok := e.(*l8ai.ChatConversation); ok && h.ConversationId == conversationId {
			return h, nil
		}
	}
	return nil, errors.New("conversation " + conversationId + " not found")
}

// fetchTail reads the newest messages after the summary, at most one ring's
// worth, so the owner serves them from memory, including the newest ones
// that may not be in chat-orm yet.
func (this *Agent) fetchTail(conversationId string, after int64, aaaId string) ([]*l8ai.ChatMessage, error) {
	query := "select * from ChatMessage where conversationId=" + conversationId +
		" and seq>" + strconv.FormatInt(after, 10) +
		" sort-by seq descending limit " + strconv.Itoa(this.settings.Chat.RingSize) + " page 0"
	elems, err := this.routerGet(aicommon.ChatMsgService, query, aaaId)
	if err != nil {
		return nil, err
	}
	result := make([]*l8ai.ChatMessage, 0, len(elems))
	for _, e := range elems {
		if m, ok := e.(*l8ai.ChatMessage); ok && m != nil {
			result = append(result, m)
		}
	}
	return result, nil
}
