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
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// syncService is ChatShSync: the per-conversation notification sequence (plan §5.7).
//   - gap fill: "where conversationId=X and notifSeq>N and lastMsgSeq=M"
//   - latest only: "where conversationId in [...]" returns {conversationId, notifSeq}
type syncService struct {
	handlerBase
}

func (this *syncService) Activate(sla *ifs.ServiceLevelAgreement, vnic ifs.IVNic) error {
	return this.activate(sla)
}

func (this *syncService) PostMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: ChatSync is read-only")
}

func (this *syncService) PutMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: ChatSync is read-only")
}

func (this *syncService) PatchMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: ChatSync is read-only")
}

func (this *syncService) DeleteMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) ifs.IElements {
	return object.NewError("chat: ChatSync is read-only")
}

func (this *syncService) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	if !this.inst.fromRouter(msg) {
		return object.NewError("chat: reads are accepted only from the router")
	}
	userId, err := this.inst.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	q, err := pb.Query(vnic.Resources())
	if err != nil || q == nil {
		return object.NewError("chat: invalid sync query")
	}
	if after, ok := criteriaInt64(q, "notifSeq", ">"); ok {
		conversationId, _ := criteriaValue(q, "conversationId", "=")
		lastMsgSeq, _ := criteriaInt64(q, "lastMsgSeq", "=")
		return this.inst.gapFill(conversationId, userId, after, lastMsgSeq)
	}
	return this.inst.latestOnly(q, userId)
}

// gapFill returns what a browser missed after notifSeq "after": the retained
// notifications when they cover the gap, otherwise a snapshot of the header
// and the newest page of messages after lastMsgSeq.
func (this *Instance) gapFill(conversationId, userId string, after, lastMsgSeq int64) ifs.IElements {
	c := this.conversation(conversationId)
	if c == nil {
		return object.NewError("chat: conversation " + conversationId + " is not owned by this instance")
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if !c.isMember(userId) {
		return object.NewError("chat: not a member of the conversation")
	}
	reply := &l8ai.ChatSync{ConversationId: conversationId, NotifSeq: c.header.NotifSeq,
		Conversation: cloneConversation(c.header)}
	if after < c.header.NotifSeq && len(c.notifs) > 0 && c.notifs[0].notifSeq <= after+1 {
		for _, n := range c.notifs {
			if n.notifSeq > after && n.message != nil {
				reply.Messages = append(reply.Messages, cloneMessage(n.message))
			}
		}
		return object.New(nil, reply)
	}
	if after >= c.header.NotifSeq {
		return object.New(nil, reply) // nothing was missed
	}
	reply.Snapshot = true
	if err := this.ensureRing(c); err != nil {
		return object.NewError(err.Error())
	}
	reply.Messages = this.messagesAfter(c, lastMsgSeq, this.cfg.PageSize)
	return object.New(nil, reply)
}

// latestOnly returns the current notifSeq of every owned conversation the
// query selects and the requester belongs to, without data.
func (this *Instance) latestOnly(q ifs.IQuery, userId string) ifs.IElements {
	result := make([]*l8ai.ChatSync, 0)
	for _, c := range this.owned() {
		c.mtx.Lock()
		if c.isMember(userId) {
			row := &l8ai.ChatSync{ConversationId: c.header.ConversationId, NotifSeq: c.header.NotifSeq}
			if q.Match(row) {
				result = append(result, row)
			}
		}
		c.mtx.Unlock()
	}
	return object.NewQueryResult(result, &l8api.L8MetaData{})
}
