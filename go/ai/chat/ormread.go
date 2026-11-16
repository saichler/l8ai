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
	"sort"
	"strconv"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
)

// ormGet sends a read to a chat-orm service and returns the non-nil elements.
func (this *Instance) ormGet(serviceName string, query interface{}) ([]interface{}, error) {
	return aicommon.LeaderGet(this.vnic, serviceName, query, this.cfg.ReadTimeoutSeconds)
}

// ensureRing loads the latest RingSize messages of the conversation from
// chat-orm into the message cache, unless the ring is already loaded.
// The caller holds c.mtx.
func (this *Instance) ensureRing(c *conversation) error {
	c.lastUsed = time.Now()
	if c.ringLoaded {
		return nil
	}
	if c.header.LastSeq == 0 {
		c.ringLoaded, c.ringComplete = true, true
		return nil
	}
	query := "select * from ChatMessage where conversationId=" + c.header.ConversationId +
		" sort-by seq descending limit " + strconv.Itoa(this.cfg.RingSize) + " page 0"
	elems, err := this.ormGet(aicommon.ChatMsgDbService, query)
	if err != nil {
		return err
	}
	msgs := make([]*l8ai.ChatMessage, 0, len(elems))
	for _, e := range elems {
		if m, ok := e.(*l8ai.ChatMessage); ok {
			msgs = append(msgs, m)
		}
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Seq < msgs[j].Seq })
	c.ringIds = make([]string, 0, len(msgs))
	for _, m := range msgs {
		if _, _, err := this.msgCache.Post(m, false); err == nil {
			c.ringIds = append(c.ringIds, m.MessageId)
		}
	}
	c.ringLoaded = true
	c.ringComplete = len(msgs) < this.cfg.RingSize || (len(msgs) > 0 && msgs[0].Seq <= 1)
	return nil
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

// messagesAfter returns up to limit of the newest messages with seq > after,
// in ascending seq order: from the ring when it covers them, else from chat-orm.
// The caller holds c.mtx and has loaded the ring.
func (this *Instance) messagesAfter(c *conversation, after int64, limit int) []*l8ai.ChatMessage {
	from := c.header.LastSeq - int64(limit)
	if from < after {
		from = after
	}
	result := make([]*l8ai.ChatMessage, 0)
	for _, id := range c.ringIds {
		v, err := this.msgCache.Get(&l8ai.ChatMessage{MessageId: id})
		if err != nil {
			continue
		}
		if m, ok := v.(*l8ai.ChatMessage); ok && m.Seq > from {
			result = append(result, m)
		}
	}
	if int64(len(result)) >= c.header.LastSeq-from || c.ringComplete {
		return result
	}
	query := "select * from ChatMessage where conversationId=" + c.header.ConversationId +
		" and seq>" + strconv.FormatInt(from, 10) + " sort-by seq descending limit " + strconv.Itoa(limit) + " page 0"
	elems, err := this.ormGet(aicommon.ChatMsgDbService, query)
	if err != nil {
		this.vnic.Resources().Logger().Error("chat: snapshot of ", c.header.ConversationId, ": ", err.Error())
		return result
	}
	byId := make(map[string]*l8ai.ChatMessage)
	for _, m := range result {
		byId[m.MessageId] = m
	}
	for _, e := range elems {
		if m, ok := e.(*l8ai.ChatMessage); ok {
			if _, dup := byId[m.MessageId]; !dup {
				byId[m.MessageId] = m
			}
		}
	}
	result = result[:0]
	for _, m := range byId {
		result = append(result, m)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq < result[j].Seq })
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result
}
