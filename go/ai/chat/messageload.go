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

// messageWindow is a conversation's latest messages held in memory: at most
// the handler's window size (RingSize), in seq order. Older messages are read
// from chat-orm. Every field is guarded by the handler's mtx.
type messageWindow struct {
	messages []*l8ai.ChatMessage
	// complete is true when the window holds the whole conversation.
	complete bool
	// loaded is false after an idle eviction, until the next read reloads the
	// window. A new message to an unloaded window is still added: the window
	// then holds the messages since the eviction.
	loaded bool
}

// add appends a new message and drops the oldest past size. The slice is
// shifted in place: readers copy what they need under the handler's lock.
func (this *messageWindow) add(m *l8ai.ChatMessage, size int) {
	this.messages = append(this.messages, m)
	if drop := len(this.messages) - size; drop > 0 {
		copy(this.messages, this.messages[drop:])
		for i := size; i < len(this.messages); i++ {
			this.messages[i] = nil
		}
		this.messages = this.messages[:size]
		this.complete = false
	}
}

// merge takes the latest messages read from chat-orm into an unloaded window:
// the union by MessageId with what the window gained since its eviction (it
// may still be in the write-behind), in seq order, the latest size of them.
func (this *messageWindow) merge(read []*l8ai.ChatMessage, complete bool, size int) {
	byId := make(map[string]*l8ai.ChatMessage, len(read)+len(this.messages))
	for _, m := range read {
		byId[m.MessageId] = m
	}
	for _, m := range this.messages {
		byId[m.MessageId] = m
	}
	merged := make([]*l8ai.ChatMessage, 0, len(byId))
	for _, m := range byId {
		merged = append(merged, m)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Seq < merged[j].Seq })
	if len(merged) > size {
		merged = merged[len(merged)-size:]
		complete = false
	}
	this.messages = merged
	this.complete = complete
	this.loaded = true
}

// ensureLoaded loads the conversation's window from chat-orm unless it is
// loaded: on a take-over, at start-up, and on the first read after an idle
// eviction. The caller holds the conversation's lock (or the conversation is
// not owned yet), so no new message is added while the window loads.
func (this *ChatMessageServiceHandler) ensureLoaded(conversationId string) error {
	this.mtx.RLock()
	w := this.conversations[conversationId]
	loaded := w != nil && w.loaded
	this.mtx.RUnlock()
	if loaded {
		return nil
	}
	read, complete, err := this.readLatest(conversationId)
	if err != nil {
		return err
	}
	this.mtx.Lock()
	w = this.conversations[conversationId]
	if w == nil {
		w = &messageWindow{}
		this.conversations[conversationId] = w
	}
	w.merge(read, complete, this.size)
	this.mtx.Unlock()
	return nil
}

// reload makes a read's window ready: an owned conversation always has a
// window (loaded when it was taken over), so a missing one is an error, not
// something to load and serve quietly; a window evicted for idleness is
// reloaded (Phase 1.8 of the plan). The caller holds the conversation's lock.
func (this *ChatMessageServiceHandler) reload(conversationId string) error {
	this.mtx.RLock()
	w := this.conversations[conversationId]
	this.mtx.RUnlock()
	if w == nil {
		err := errors.New("chat: conversation " + conversationId + " is owned but its messages are not loaded")
		this.inst.vnic.Resources().Logger().Error(err.Error())
		return err
	}
	return this.ensureLoaded(conversationId)
}

// readLatest reads the conversation's latest window-size messages from
// chat-orm, in seq order. complete is true when that is the whole
// conversation (fewer than a window, or the oldest is seq 1).
func (this *ChatMessageServiceHandler) readLatest(conversationId string) ([]*l8ai.ChatMessage, bool, error) {
	query := "select * from ChatMessage where conversationId=" + conversationId +
		" sort-by seq descending limit " + strconv.Itoa(this.size) + " page 0"
	elems, err := aicommon.LeaderGet(this.inst.vnic, aicommon.ChatMsgDbService, query, this.inst.cfg.ReadTimeoutSeconds)
	if err != nil {
		return nil, false, errors.New("chat: loading the messages of " + conversationId + " from chat-orm: " + err.Error())
	}
	result := make([]*l8ai.ChatMessage, 0, len(elems))
	for _, e := range elems {
		if m, ok := e.(*l8ai.ChatMessage); ok && m != nil && m.ConversationId == conversationId {
			result = append(result, m)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq < result[j].Seq })
	complete := len(result) < this.size || (len(result) > 0 && result[0].Seq <= 1)
	return result, complete, nil
}

// takeOver loads the window of a conversation this instance is taking over
// (a router assignment or a rebalance), before the instance owns it: once it
// is owned, a new message could arrive and start the conversation without its
// history. A conversation without messages yet (LastSeq 0) is held as an
// empty, complete window without a read.
func (this *ChatMessageServiceHandler) takeOver(header *l8ai.ChatConversation) error {
	if header.LastSeq == 0 {
		this.mtx.Lock()
		if _, ok := this.conversations[header.ConversationId]; !ok {
			this.conversations[header.ConversationId] = &messageWindow{complete: true, loaded: true}
		}
		this.mtx.Unlock()
		return nil
	}
	return this.ensureLoaded(header.ConversationId)
}

// evictIdle drops the window of every owned conversation with no agent turn
// running and no read or write for longer than idle. The conversation stays
// owned (header, members, subscriptions); its next read reloads the window.
// Returns the number of windows evicted.
func (this *ChatMessageServiceHandler) evictIdle(idle time.Duration) int {
	evicted := 0
	for _, c := range this.inst.owned() {
		c.mtx.Lock()
		if c.turn == nil && time.Since(c.lastUsed) > idle {
			this.mtx.Lock()
			if w := this.conversations[c.header.ConversationId]; w != nil && (w.loaded || len(w.messages) > 0) {
				*w = messageWindow{}
				evicted++
			}
			this.mtx.Unlock()
		}
		c.mtx.Unlock()
	}
	return evicted
}

// dropConversation removes a conversation that left this instance (a
// rebalance): its window and its subscriptions.
func (this *ChatMessageServiceHandler) dropConversation(conversationId string) {
	this.mtx.Lock()
	delete(this.conversations, conversationId)
	this.mtx.Unlock()
	this.subs.dropConversation(conversationId)
}

// dropSubscribers removes the conversation's subscriptions whose session
// belongs to one of the users, e.g. members removed from the conversation.
func (this *ChatMessageServiceHandler) dropSubscribers(conversationId string, userIds []string) {
	this.subs.dropMatching(conversationId, func(aaaId string) bool {
		user, ok := this.inst.users.Cached(aaaId)
		return ok && contains(userIds, user)
	})
}
