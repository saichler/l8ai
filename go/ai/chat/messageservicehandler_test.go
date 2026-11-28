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
	"testing"

	"github.com/saichler/l8ai/go/types/l8ai"
)

// The first page is the latest one.
func TestTimePage(t *testing.T) {
	cases := []struct {
		count, page, limit int
		start, end         int
	}{
		{25, 0, 10, 15, 25},  // latest 10
		{25, 1, 10, 5, 15},   // the 10 before them
		{25, 2, 10, 0, 5},    // the oldest, a short page
		{25, 3, 10, 0, 0},    // past the oldest
		{25, 0, 0, 0, 25},    // no limit: everything
		{25, 5, 0, 0, 25},    // no limit ignores the page
		{3, 0, 10, 0, 3},     // fewer than a page
		{0, 0, 10, 0, 0},     // empty conversation
		{25, -1, 10, 15, 25}, // a negative page is the first
	}
	for _, c := range cases {
		start, end := timePage(c.count, c.page, c.limit)
		if start != c.start || end != c.end {
			t.Errorf("timePage(%d, %d, %d) = [%d, %d), expected [%d, %d)",
				c.count, c.page, c.limit, start, end, c.start, c.end)
		}
	}
}

// seqs returns messages with seq 1..n, in seq order.
func seqs(n int) []*l8ai.ChatMessage {
	list := make([]*l8ai.ChatMessage, n)
	for i := range list {
		list[i] = &l8ai.ChatMessage{Seq: int64(i + 1)}
	}
	return list
}

// seq>N and seq<N narrow the messages; the bounds are exclusive.
func TestSeqWindow(t *testing.T) {
	msgs := seqs(25)
	cases := []struct {
		name   string
		bounds seqBounds
		lo, hi int
	}{
		{"none", seqBounds{}, 0, 25},
		{"after 10", seqBounds{after: 10, hasAfter: true}, 10, 25},   // seq 11..25
		{"before 11", seqBounds{before: 11, hasBefore: true}, 0, 10}, // seq 1..10
		{"after 5 before 9", seqBounds{after: 5, hasAfter: true, before: 9, hasBefore: true}, 5, 8},
		{"after 0", seqBounds{after: 0, hasAfter: true}, 0, 25},
		{"after the newest", seqBounds{after: 25, hasAfter: true}, 25, 25},
		{"before the oldest", seqBounds{before: 1, hasBefore: true}, 0, 0},
		{"empty range", seqBounds{after: 10, hasAfter: true, before: 5, hasBefore: true}, 10, 10},
	}
	for _, c := range cases {
		lo, hi := c.bounds.window(msgs)
		if lo != c.lo || hi != c.hi {
			t.Errorf("%s: [%d, %d), expected [%d, %d)", c.name, lo, hi, c.lo, c.hi)
		}
	}
	if lo, hi := (seqBounds{after: 3, hasAfter: true}).window(nil); lo != 0 || hi != 0 {
		t.Errorf("empty conversation: [%d, %d)", lo, hi)
	}
}

// The UI's "load older": seq<oldest limit 10 page 0 is the 10 just before
// the oldest message shown, oldest first.
func TestSeqWindowThenPage(t *testing.T) {
	msgs := seqs(25)
	lo, hi := (seqBounds{before: 16, hasBefore: true}).window(msgs)
	start, end := timePage(hi-lo, 0, 10)
	start, end = lo+start, lo+end
	if msgs[start].Seq != 6 || msgs[end-1].Seq != 15 {
		t.Fatalf("seq<16 limit 10: seq %d..%d, expected 6..15", msgs[start].Seq, msgs[end-1].Seq)
	}
	// Tanto: seq>20 limit 50 is everything after the summary point.
	lo, hi = (seqBounds{after: 20, hasAfter: true}).window(msgs)
	start, end = timePage(hi-lo, 0, 50)
	start, end = lo+start, lo+end
	if msgs[start].Seq != 21 || msgs[end-1].Seq != 25 {
		t.Fatalf("seq>20 limit 50: seq %d..%d, expected 21..25", msgs[start].Seq, msgs[end-1].Seq)
	}
}

// record gives each message its conversation's next seq and notification
// seq, continuing from the header (e.g. a conversation reloaded with 40
// messages), keeps each conversation in seq order and moves the header.
func TestRecordAssignsSeq(t *testing.T) {
	h := &messageStore{}
	h.init(50)
	a := newConversation(&l8ai.ChatConversation{ConversationId: "a",
		Members: []*l8ai.ChatMember{{UserId: "u1"}}})
	b := newConversation(&l8ai.ChatConversation{ConversationId: "b", LastSeq: 40, NotifSeq: 90})
	post := func(c *conversation, id string, seq int64) {
		h.record(c, &l8ai.ChatMessage{MessageId: id, ConversationId: c.header.ConversationId,
			SenderId: "u1", Body: id, Seq: seq}, 10)
	}
	post(a, "a1", 0)
	post(b, "b1", 0)
	post(a, "a2", 99) // the sender's seq is ignored
	post(a, "a3", 0)
	post(b, "b2", 7)
	expect := map[string][]int64{"a": {1, 2, 3}, "b": {41, 42}}
	for conv, want := range expect {
		got := h.conversations[conv].messages
		if len(got) != len(want) {
			t.Fatalf("conversation %s has %d messages, expected %d", conv, len(got), len(want))
		}
		for i, m := range got {
			if m.Seq != want[i] {
				t.Errorf("conversation %s message %s: seq %d, expected %d", conv, m.MessageId, m.Seq, want[i])
			}
		}
	}
	if a.header.LastSeq != 3 || a.header.LastPreview == "" || b.header.LastSeq != 42 {
		t.Errorf("headers: a LastSeq %d preview %q, b LastSeq %d", a.header.LastSeq, a.header.LastPreview, b.header.LastSeq)
	}
	if b.header.NotifSeq != 92 || h.conversations["b"].messages[1].NotifSeq != 92 {
		t.Errorf("b NotifSeq: header %d, message %d, expected 92", b.header.NotifSeq, h.conversations["b"].messages[1].NotifSeq)
	}
	if a.member("u1").LastReadSeq != 3 {
		t.Errorf("sender's read position %d, expected 3", a.member("u1").LastReadSeq)
	}
	if len(a.notifs) != 3 {
		t.Errorf("a retained %d pushes, expected 3", len(a.notifs))
	}
	if !h.hasMessage("a", "a2") || h.hasMessage("a", "b1") {
		t.Error("hasMessage")
	}
}

// A client component has one subscription: a new registration moves it.
func TestMessageSubscriptions(t *testing.T) {
	s := newMessageSubscriptions()
	s.register("c1", "s1", "thread", true)
	s.register("c1", "s2", "thread", true)
	s.register("c2", "s1", "other", true)
	if ids := s.latestAaaIds("c1"); len(ids) != 2 || !ids["s1"] || !ids["s2"] {
		t.Fatalf("c1 subscribers %v", ids)
	}
	s.register("c3", "s2", "thread", true) // s2's thread moves to c3
	if ids := s.latestAaaIds("c1"); len(ids) != 1 || !ids["s1"] {
		t.Fatalf("c1 subscribers after the move %v", ids)
	}
	s.unregister("s1", "thread")
	if ids := s.latestAaaIds("c1"); ids != nil {
		t.Fatalf("c1 subscribers after unregister %v", ids)
	}
	if ids := s.latestAaaIds("c2"); len(ids) != 1 || !ids["s1"] {
		t.Fatalf("s1's other component was removed: %v", ids)
	}
	s.register("c4", "s3", "older", false) // registered on an older page: no push
	if ids := s.latestAaaIds("c4"); ids != nil {
		t.Fatalf("an older-page subscription gets new messages: %v", ids)
	}
	s.dropConversation("c3")
	if s.latestAaaIds("c3") != nil || len(s.conversationOf) != 2 {
		t.Fatalf("drop: %v %v", s.latestAaaIds("c3"), s.conversationOf)
	}
}

// The gap fill's snapshot is the newest limit messages after lastMsgSeq.
func TestMessagesAfter(t *testing.T) {
	h := &messageStore{inst: &Instance{}}
	h.init(50)
	h.conversations["a"] = &messageWindow{messages: seqs(25), complete: true, loaded: true}
	got, err := h.messagesAfter("a", 10, 5)
	if err != nil || len(got) != 5 || got[0].Seq != 21 || got[4].Seq != 25 {
		t.Fatalf("after 10 limit 5: %d messages, err %v", len(got), err)
	}
	got, _ = h.messagesAfter("a", 22, 50)
	if len(got) != 3 || got[0].Seq != 23 {
		t.Fatalf("after 22 limit 50: %d messages", len(got))
	}
	got[0].Seq = 0 // a clone: the held message is unchanged
	if h.conversations["a"].messages[22].Seq != 23 {
		t.Fatal("messagesAfter returned the held message, not a clone")
	}
}

// A conversation taken over without messages is held (so it is not loaded
// from chat-orm on its first read); a released one leaves with its
// subscriptions; removed members lose theirs.
func TestTakeOverAndDrop(t *testing.T) {
	h := &messageStore{inst: &Instance{}}
	h.init(50)
	if err := h.takeOver(&l8ai.ChatConversation{ConversationId: "new"}); err != nil {
		t.Fatal(err)
	}
	if w, ok := h.conversations["new"]; !ok || !w.loaded || !w.complete || len(w.messages) != 0 {
		t.Fatalf("new conversation held as %+v (%v)", w, ok)
	}
	h.conversations["a"] = &messageWindow{messages: seqs(3), complete: true, loaded: true}
	h.subs.register("a", "s1", "thread", true)
	h.subs.register("a", "s2", "thread", true)
	h.dropConversation("a")
	if _, ok := h.conversations["a"]; ok || h.subs.latestAaaIds("a") != nil {
		t.Fatal("released conversation still held")
	}
	h.subs.register("b", "s1", "thread", true)
	h.subs.register("b", "s2", "thread", true)
	h.subs.dropMatching("b", func(aaaId string) bool { return aaaId == "s1" })
	if ids := h.subs.latestAaaIds("b"); len(ids) != 1 || !ids["s2"] {
		t.Fatalf("after dropping s1: %v", ids)
	}
}

// The window keeps the latest size messages and is then no longer complete;
// merging a chat-orm read into an evicted window keeps what the window gained
// since the eviction and the latest size in seq order.
func TestMessageWindow(t *testing.T) {
	w := &messageWindow{complete: true, loaded: true}
	for _, m := range seqs(5) {
		w.add(m, 3)
	}
	if len(w.messages) != 3 || w.messages[0].Seq != 3 || w.messages[2].Seq != 5 || w.complete {
		t.Fatalf("after 5 adds to a window of 3: %d messages from seq %d, complete %v", len(w.messages), w.messages[0].Seq, w.complete)
	}
	evicted := &messageWindow{}
	evicted.add(&l8ai.ChatMessage{MessageId: "m6", Seq: 6}, 3) // a message to the evicted window
	read := seqs(5)
	for i, m := range read {
		m.MessageId = "m" + string(rune('1'+i))
	}
	evicted.merge(read, true, 3)
	if !evicted.loaded || evicted.complete || len(evicted.messages) != 3 ||
		evicted.messages[0].Seq != 4 || evicted.messages[2].Seq != 6 {
		t.Fatalf("merged window: loaded %v complete %v, %d messages from seq %d", evicted.loaded, evicted.complete,
			len(evicted.messages), evicted.messages[0].Seq)
	}
}
