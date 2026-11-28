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
	"strings"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"github.com/saichler/l8types/go/types/l8web"
)

// ormReadPage is the page size of a chat-orm read of older messages (l8ql
// accepts a limit below 1000).
const ormReadPage = 500

// GetMessage handles a read from the router (plan §5.3): a page of the
// messages of the conversation named by the query's conversationId criteria,
// for a member of the conversation, in time order. seq>N (Tanto: after the
// summary) and seq<N (the UI: older than the oldest shown) narrow the
// messages before paging. The page comes from the conversation's window when
// the window covers it, otherwise the older part comes from chat-orm.
// A read with Register subscribes the client component to the conversation's
// new messages; an unregister (e.g. sent when the browser's websocket
// closes) carries no conversation, removes the subscription and reads nothing.
func (this *ChatMessageServiceHandler) GetMessage(pb ifs.IElements, vnic ifs.IVNic, msg *ifs.Message) (resp ifs.IElements) {
	defer guard(vnic, &resp)
	if !this.inst.fromRouter(msg) {
		return object.NewError("chat: reads are accepted only from the router")
	}
	q, err := pb.Query(vnic.Resources())
	if err != nil || q == nil {
		return object.NewError("chat: invalid message query")
	}
	if q.Register() == l8api.L8Register_L8_REGISTER_UNREGISTER {
		this.subs.unregister(q.AAAId(), q.RegisterId())
		return object.New(nil, &l8web.L8Empty{})
	}
	userId, err := this.inst.requester(msg)
	if err != nil {
		return object.NewError(err.Error())
	}
	conversationId, ok := aicommon.CriteriaValue(q, "conversationId", "=")
	if !ok || conversationId == "" {
		return object.NewError("chat: a message query must select a conversationId")
	}
	bounds, err := seqBoundsOf(q)
	if err != nil {
		return object.NewError(err.Error())
	}
	c := this.inst.conversation(conversationId)
	if c == nil {
		return object.NewError("chat: conversation " + conversationId + " is not owned by this instance")
	}
	c.mtx.Lock()
	if !c.isMember(userId) {
		c.mtx.Unlock()
		return object.NewError("chat: not a member of the conversation")
	}
	c.lastUsed = time.Now()
	err = this.reload(conversationId)
	c.mtx.Unlock()
	if err != nil {
		return object.NewError(err.Error())
	}
	if q.AAAId() != "" && q.Register() == l8api.L8Register_L8_REGISTER_REGISTER {
		latest := q.Page() == 0 && !bounds.hasBefore
		this.subs.register(conversationId, q.AAAId(), q.RegisterId(), latest)
	}
	messages, err := this.page(conversationId, bounds, int(q.Page()), int(q.Limit()))
	if err != nil {
		return object.NewError(err.Error())
	}
	result := make([]interface{}, len(messages))
	for i, m := range messages {
		result[i] = m
	}
	return object.NewQueryResult(result, nil)
}

// messagesAfter returns up to limit of the newest messages with seq > after,
// in seq order: the gap fill's snapshot (plan §5.7). The caller holds c.mtx.
func (this *ChatMessageServiceHandler) messagesAfter(conversationId string, after int64, limit int) ([]*l8ai.ChatMessage, error) {
	if err := this.reload(conversationId); err != nil {
		return nil, err
	}
	messages, err := this.page(conversationId, seqBounds{after: after, hasAfter: true}, 0, limit)
	if err != nil {
		return nil, err
	}
	result := make([]*l8ai.ChatMessage, len(messages))
	for i, m := range messages {
		result[i] = cloneMessage(m)
	}
	return result, nil
}

// page returns page p of limit messages inside the bounds, the first page
// being the newest, in seq order (oldest first, as the chat window shows it;
// descending selects the newest page, it does not reverse it). A limit of 0
// or less means every message inside the bounds.
//
// The window holds every message from its oldest to the newest, so its
// messages inside the bounds are the newest ones inside the bounds. It answers
// alone when it is complete, when the bounds lie inside it, or when it holds
// the whole requested page. Otherwise chat-orm supplies only what is older
// than the window: chat-orm gets new messages through the write-behind, so
// its newest messages may be only in the window.
func (this *ChatMessageServiceHandler) page(conversationId string, bounds seqBounds, page, limit int) ([]*l8ai.ChatMessage, error) {
	if page < 0 {
		page = 0
	}
	this.mtx.RLock()
	w := this.conversations[conversationId]
	var inWindow []*l8ai.ChatMessage
	complete := false
	oldest := int64(0)
	if w != nil {
		lo, hi := bounds.window(w.messages)
		inWindow = append([]*l8ai.ChatMessage(nil), w.messages[lo:hi]...)
		complete = w.complete
		if len(w.messages) > 0 {
			oldest = w.messages[0].Seq
		}
	}
	this.mtx.RUnlock()

	covered := complete ||
		(oldest > 0 && bounds.hasAfter && bounds.after >= oldest-1) ||
		(limit > 0 && len(inWindow) >= (page+1)*limit)
	if !covered {
		older, err := this.readOlder(conversationId, bounds, oldest, (page+1)*limit-len(inWindow), limit > 0)
		if err != nil {
			return nil, err
		}
		inWindow = append(older, inWindow...)
	}
	start, end := timePage(len(inWindow), page, limit)
	return inWindow[start:end], nil
}

// readOlder reads from chat-orm the messages inside the bounds that are older
// than the window (seq < oldest, when the window holds any): the newest want
// of them, or all of them when the read is not limited. Returned in seq order.
func (this *ChatMessageServiceHandler) readOlder(conversationId string, bounds seqBounds, oldest int64, want int, limited bool) ([]*l8ai.ChatMessage, error) {
	clauses := ""
	if bounds.hasAfter {
		clauses += " and seq>" + strconv.FormatInt(bounds.after, 10)
	}
	before, hasBefore := bounds.before, bounds.hasBefore
	if oldest > 0 && (!hasBefore || oldest < before) {
		before, hasBefore = oldest, true
	}
	if hasBefore {
		clauses += " and seq<" + strconv.FormatInt(before, 10)
	}
	pageSize := ormReadPage
	if limited && want < pageSize {
		pageSize = want
	}
	result := make([]*l8ai.ChatMessage, 0)
	for pg := 0; ; pg++ {
		query := "select * from ChatMessage where conversationId=" + conversationId + clauses +
			" sort-by seq descending limit " + strconv.Itoa(pageSize) + " page " + strconv.Itoa(pg)
		elems, err := aicommon.LeaderGet(this.inst.vnic, aicommon.ChatMsgDbService, query, this.inst.cfg.ReadTimeoutSeconds)
		if err != nil {
			return nil, errors.New("chat: reading older messages of " + conversationId + " from chat-orm: " + err.Error())
		}
		for _, e := range elems {
			if m, ok := e.(*l8ai.ChatMessage); ok && m != nil && m.ConversationId == conversationId {
				result = append(result, m)
			}
		}
		if len(elems) < pageSize || (limited && len(result) >= want) {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq < result[j].Seq })
	if limited && len(result) > want {
		result = result[len(result)-want:]
	}
	return result, nil
}

// seqBounds are a message query's seq criteria: seq>after and seq<before.
type seqBounds struct {
	after, before       int64
	hasAfter, hasBefore bool
}

// seqBoundsOf reads the seq>N and seq<N criteria of the query, if any.
func seqBoundsOf(q ifs.IQuery) (seqBounds, error) {
	b := seqBounds{}
	var err error
	if v, ok := aicommon.CriteriaValue(q, "seq", ">"); ok {
		if b.after, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
			return b, errors.New("chat: invalid seq>" + v)
		}
		b.hasAfter = true
	}
	if v, ok := aicommon.CriteriaValue(q, "seq", "<"); ok {
		if b.before, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
			return b, errors.New("chat: invalid seq<" + v)
		}
		b.hasBefore = true
	}
	return b, nil
}

// window returns the [lo, hi) range of the messages, kept in seq order, that
// fall within the bounds. Each bound is a binary search, not a pass.
func (this seqBounds) window(messages []*l8ai.ChatMessage) (int, int) {
	lo, hi := 0, len(messages)
	if this.hasAfter {
		lo = sort.Search(len(messages), func(i int) bool { return messages[i].Seq > this.after })
	}
	if this.hasBefore {
		hi = sort.Search(len(messages), func(i int) bool { return messages[i].Seq >= this.before })
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// timePage returns the [start, end) range of a page over count time series
// elements kept oldest first. The first page is the latest one: page 0 is the
// last limit elements, page 1 the limit before them, and so on. A limit of 0
// or less means every element.
func timePage(count, page, limit int) (int, int) {
	if limit <= 0 {
		return 0, count
	}
	if page < 0 {
		page = 0
	}
	end := count - page*limit
	if end <= 0 {
		return 0, 0
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	return start, end
}
