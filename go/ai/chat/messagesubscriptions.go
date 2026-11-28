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
	"sync/atomic"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8utils/go/utils/notify"
)

// messageSubscriptions are the browsers' live registrations on conversation
// threads: a read with Register subscribes its client component (aaaId +
// registerId) to the conversation it read. A component has at most one
// subscription: a new registration (another conversation, or a re-read)
// replaces it.
//
// Page scoping (LiveUpdates): a browser is notified only of changes to the
// page it displays. Messages are append-only and a new one always lands on a
// thread's latest page, so the page scope of a subscription is one fact:
// whether the registering read was the latest page (page 0 and no seq<
// bound). Only those subscriptions get new messages.
type messageSubscriptions struct {
	mtx sync.Mutex
	// byConversation holds each conversation's subscribers by component key.
	byConversation map[string]map[string]subscriber
	// conversationOf is the conversation each component is subscribed to.
	conversationOf map[string]string
}

// subscriber is one client component's subscription to a conversation.
type subscriber struct {
	aaaId string
	// latest is true when the component displays the conversation's latest
	// page, the only page a new message lands on.
	latest bool
}

func newMessageSubscriptions() *messageSubscriptions {
	return &messageSubscriptions{
		byConversation: make(map[string]map[string]subscriber),
		conversationOf: make(map[string]string),
	}
}

// register subscribes the client component to the conversation, replacing
// its previous subscription. latest tells whether the read it registered with
// was the conversation's latest page.
func (this *messageSubscriptions) register(conversationId, aaaId, registerId string, latest bool) {
	key := subscriptionKey(aaaId, registerId)
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.remove(key)
	subs := this.byConversation[conversationId]
	if subs == nil {
		subs = make(map[string]subscriber)
		this.byConversation[conversationId] = subs
	}
	subs[key] = subscriber{aaaId: aaaId, latest: latest}
	this.conversationOf[key] = conversationId
}

// unregister removes the client component's subscription.
func (this *messageSubscriptions) unregister(aaaId, registerId string) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.remove(subscriptionKey(aaaId, registerId))
}

// dropConversation removes every subscription to the conversation, e.g. when
// the conversation leaves this instance.
func (this *messageSubscriptions) dropConversation(conversationId string) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	for key := range this.byConversation[conversationId] {
		delete(this.conversationOf, key)
	}
	delete(this.byConversation, conversationId)
}

// dropMatching removes the conversation's subscriptions whose session
// matches.
func (this *messageSubscriptions) dropMatching(conversationId string, match func(aaaId string) bool) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	for key, sub := range this.byConversation[conversationId] {
		if match(sub.aaaId) {
			this.remove(key)
		}
	}
}

// latestAaaIds returns the sessions displaying the conversation's latest
// page, the ones a new message is pushed to, or nil when there are none.
func (this *messageSubscriptions) latestAaaIds(conversationId string) map[string]bool {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	var result map[string]bool
	for _, sub := range this.byConversation[conversationId] {
		if !sub.latest {
			continue
		}
		if result == nil {
			result = make(map[string]bool)
		}
		result[sub.aaaId] = true
	}
	return result
}

// remove deletes a component's subscription. The caller holds the lock.
func (this *messageSubscriptions) remove(key string) {
	conversationId, ok := this.conversationOf[key]
	if !ok {
		return
	}
	delete(this.conversationOf, key)
	subs := this.byConversation[conversationId]
	delete(subs, key)
	if len(subs) == 0 {
		delete(this.byConversation, conversationId)
	}
}

func subscriptionKey(aaaId, registerId string) string {
	return aaaId + "/" + registerId
}

// push sends a new message to the browsers displaying its conversation's
// latest page: the notification carries the full record and the AaaIds of
// those subscribers, and nothing is sent when there are none (an empty AaaIds
// is dropped by l8web, never broadcast).
//
// Building this client notification is the message store's job. LiveUpdates
// asks for the cache-built cn; the store is explicit in l8ai, so it
// builds it. That is the accepted deviation of plan
// l8ai-chat-message-handler-compliance.md (D2 (a)), and it moves into TCache
// (l8utils) with the store, which will build and return the cn like Cache.
func (this *messageStore) push(m *l8ai.ChatMessage) {
	aaaIds := this.subs.latestAaaIds(m.ConversationId)
	if len(aaaIds) == 0 {
		return
	}
	seq := atomic.AddUint32(&this.notifySequence, 1)
	cn, err := notify.CreateAddNotification(cloneMessage(m), aicommon.ChatMsgService, m.MessageId,
		aicommon.ServiceArea, "ChatMessage", this.inst.vnic.Resources().SysConfig().LocalUuid, 1, seq)
	if err != nil {
		this.inst.vnic.Resources().Logger().Error("chat: push of message ", m.MessageId, ": ", err.Error())
		return
	}
	cn.AaaIds = aaaIds
	this.inst.push(cn)
}
