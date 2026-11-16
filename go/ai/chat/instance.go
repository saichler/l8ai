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

// Package chat implements the chat instances (plan §4.3): each process owns a
// set of conversations and is their single writer. It hosts ChatShMsg
// (messages), ChatShCnv (conversation headers and the sidebar view) and
// ChatShSync (notification-sequence sync), all reached through the router.
package chat

import (
	"sync"
	"sync/atomic"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8services/go/services/base"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8notify"
	"github.com/saichler/l8utils/go/utils/cache"
)

// Instance is one chat instance process. The three services share it.
type Instance struct {
	cfg  *aicommon.ChatConfig
	vnic ifs.IVNic

	mtx   sync.RWMutex
	convs map[string]*conversation // owned conversations by ID

	// msgCache holds the message ring of every owned conversation whose ring is
	// loaded; cnvCache holds every owned header. Both carry the browsers' live
	// registrations and produce their push notifications.
	msgCache *cache.Cache
	cnvCache *cache.Cache

	users  *userCache
	writer *writeBehind

	assignQueue chan string // conversations to load from chat-orm and own

	accepting atomic.Bool // false once a graceful shutdown starts
	running   atomic.Bool
}

func newInstance(cfg *aicommon.ChatConfig, vnic ifs.IVNic) *Instance {
	r := vnic.Resources()
	this := &Instance{cfg: cfg, vnic: vnic, convs: make(map[string]*conversation)}
	this.msgCache = cache.NewCache(&l8ai.ChatMessage{}, nil, nil, r)
	this.msgCache.SetNotificationsFor(aicommon.ChatMsgService, aicommon.ServiceArea)
	this.cnvCache = cache.NewCache(&l8ai.ChatConversation{}, nil, nil, r)
	this.cnvCache.SetNotificationsFor(aicommon.ChatConvService, aicommon.ServiceArea)
	this.users = newUserCache(cfg.UserInfoCacheTTL)
	this.writer = newWriteBehind(this)
	this.assignQueue = make(chan string, assignQueueSize)
	this.accepting.Store(true)
	this.running.Store(true)
	return this
}

// conversation returns an owned conversation, or nil.
func (this *Instance) conversation(conversationId string) *conversation {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	return this.convs[conversationId]
}

// owned returns a snapshot of the owned conversations.
func (this *Instance) owned() []*conversation {
	this.mtx.RLock()
	defer this.mtx.RUnlock()
	result := make([]*conversation, 0, len(this.convs))
	for _, c := range this.convs {
		result = append(result, c)
	}
	return result
}

// takeOwnership makes this instance the owner of the header's conversation.
// The notification sequence starts at 1 (plan §5.7) and the header is pushed,
// so a browser that followed the conversation from its previous owner resets.
// Returns false if the conversation is already owned here.
func (this *Instance) takeOwnership(header *l8ai.ChatConversation) bool {
	this.mtx.Lock()
	if _, ok := this.convs[header.ConversationId]; ok {
		this.mtx.Unlock()
		return false
	}
	c := newConversation(header)
	this.convs[header.ConversationId] = c
	this.mtx.Unlock()

	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.header.NotifSeq = 0
	if c.header.AgentState == l8ai.ChatAgentState_CHAT_AGENT_STATE_RUNNING {
		// The previous owner's turn is not known here; the watchdog ends it.
		c.turn = &agentTurn{turnId: c.header.AgentTurnId, orphan: true}
	}
	this.publishHeader(c, true)
	return true
}

// publishHeader advances the notification sequence, stores the header in the
// header cache and pushes it to the registered browsers. Unless the header is
// new, the change is also queued for chat-orm. The caller holds c.mtx.
func (this *Instance) publishHeader(c *conversation, isNew bool) {
	c.header.NotifSeq++
	h := cloneConversation(c.header)
	var cn *l8notify.L8NotificationSet
	var err error
	if isNew {
		_, cn, err = this.cnvCache.Post(h, true)
	} else {
		_, cn, err = this.cnvCache.Put(h, true)
		this.writer.header(c.header.ConversationId)
	}
	if err != nil {
		this.vnic.Resources().Logger().Error("chat: header ", c.header.ConversationId, ": ", err.Error())
		return
	}
	c.retain(&notification{notifSeq: h.NotifSeq, header: h}, this.cfg.NotifRetain)
	this.push(cn)
}

// publishMessage stores a new message in the ring, pushes it and queues it for
// chat-orm. The caller holds c.mtx, has loaded the ring and has set Seq.
func (this *Instance) publishMessage(c *conversation, m *l8ai.ChatMessage) {
	c.header.NotifSeq++
	m.NotifSeq = c.header.NotifSeq
	_, cn, err := this.msgCache.Post(m, true)
	if err != nil {
		this.vnic.Resources().Logger().Error("chat: message ", m.MessageId, ": ", err.Error())
		return
	}
	c.ringIds = append(c.ringIds, m.MessageId)
	this.trimRing(c)
	c.retain(&notification{notifSeq: m.NotifSeq, message: cloneMessage(m)}, this.cfg.NotifRetain)
	this.push(cn)
	this.writer.message(m)
}

// trimRing keeps at most RingSize messages in the conversation's ring. Dropped
// messages leave the cache silently: they still exist in chat-orm.
func (this *Instance) trimRing(c *conversation) {
	for len(c.ringIds) > this.cfg.RingSize {
		this.msgCache.Delete(&l8ai.ChatMessage{MessageId: c.ringIds[0]}, false)
		c.ringIds = c.ringIds[1:]
		c.ringComplete = false
	}
}

// dropRing removes the conversation's ring from memory. The caller holds c.mtx.
func (this *Instance) dropRing(c *conversation) {
	for _, id := range c.ringIds {
		this.msgCache.Delete(&l8ai.ChatMessage{MessageId: id}, false)
	}
	c.ringIds = nil
	c.ringLoaded = false
	c.ringComplete = false
}

// push sends a browser notification to the web servers, as BaseService does.
func (this *Instance) push(cn *l8notify.L8NotificationSet) {
	if cn != nil {
		this.vnic.Multicast(base.WsServiceName, base.WsServiceArea, ifs.Action(cn.Type), cn)
	}
}

// isFrom reports whether the node is a participant of the given l8ai service.
func (this *Instance) isFrom(source, serviceName string) bool {
	if source == "" {
		return false
	}
	_, ok := this.vnic.Resources().Services().GetParticipants(serviceName, aicommon.ServiceArea)[source]
	return ok
}

// fromRouter is true for requests sent by a chat-router replica.
func (this *Instance) fromRouter(msg *ifs.Message) bool {
	return this.isFrom(msg.Source(), aicommon.ChatIdxService)
}

// fromAgent is true for requests sent by a ChatAgent instance (plan §7.1).
func (this *Instance) fromAgent(msg *ifs.Message) bool {
	return this.isFrom(msg.Source(), aicommon.ChatAgentService)
}
