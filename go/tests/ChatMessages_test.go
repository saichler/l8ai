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

package tests

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

var env *chatEnv

// The sessions of the probler users the tests act as, logged in once, as a
// browser keeps its session.
var sessionOp, sessionOp2, sessionViewer string

// Test configuration: a small window and gap-fill retention, so the tests
// cross them with a few messages, and a short idle eviction.
const (
	testRingSize    = 10
	testNotifRetain = 5
	testRingEvict   = 3 * time.Second
)

func TestMain(m *testing.M) {
	setup()
	cfg := aicommon.NewChatConfig()
	cfg.RingSize = testRingSize
	cfg.NotifRetain = testNotifRetain
	cfg.RingEviction = testRingEvict
	env = startChatEnv(cfg)
	sessionOp = env.login("operator", "Oper123!")
	sessionOp2 = env.login("operator2", "Oper123!")
	sessionViewer = env.login("viewer", "Viewer123!")
	code := m.Run()
	tear()
	os.Exit(code)
}

// newDM creates a direct conversation between the two operators.
func newDM(t *testing.T) (op, op2, conv string) {
	op, op2 = sessionOp, sessionOp2
	conv, resp := env.createDM(op, "operator", "operator2")
	if failed(resp) {
		t.Fatalf("create the conversation: %v", errOf(resp))
	}
	return op, op2, conv
}

// sendN sends n messages "<prefix> 1".."<prefix> n" and returns their IDs.
func sendN(t *testing.T, aaaId, conv, prefix string, n int) []string {
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id, resp := env.send(aaaId, conv, prefix+" "+strconv.Itoa(i))
		if failed(resp) {
			t.Fatalf("send %s %d: %v", prefix, i, resp)
		}
		ids = append(ids, id)
	}
	return ids
}

func msgQuery(conv, criteria, paging string) string {
	return "select * from ChatMessage where conversationId=" + conv + criteria + " sort-by seq descending " + paging
}

func expectSeqs(t *testing.T, what string, got []*l8ai.ChatMessage, from, to int64) {
	t.Helper()
	if int64(len(got)) != to-from+1 {
		t.Fatalf("%s: seqs %v, expected %d..%d", what, seqsOf(got), from, to)
	}
	for i, m := range got {
		if m.Seq != from+int64(i) {
			t.Fatalf("%s: seqs %v, expected %d..%d", what, seqsOf(got), from, to)
		}
	}
}

// Seq 1..n from the header; a read pages from the newest and returns the page
// oldest first; seq< reads the page before it and seq> the messages after a
// point; the header records the last message.
func TestChatSeqAndPaging(t *testing.T) {
	op, op2, conv := newDM(t)
	sendN(t, op, conv, "message", 5)
	expectSeqs(t, "newest page of 2", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 2 page 0"), op2)), 4, 5)
	expectSeqs(t, "page 1 of 2", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 2 page 1"), op2)), 2, 3)
	expectSeqs(t, "seq<4 limit 2", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, " and seq<4", "limit 2 page 0"), op2)), 2, 3)
	expectSeqs(t, "seq>2 limit 10", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, " and seq>2", "limit 10 page 0"), op2)), 3, 5)
	h := env.header(op2, conv)
	if h == nil || h.LastSeq != 5 || !strings.Contains(h.LastPreview, "message 5") {
		t.Fatalf("header after 5 messages: %+v", h)
	}
}

// A resend of an accepted message (same MessageId) is a no-op.
func TestChatResendIsNoOp(t *testing.T) {
	op, op2, conv := newDM(t)
	id := sendN(t, op, conv, "once", 1)[0]
	resp := env.call(aicommon.ChatMsgService, ifs.POST, &l8ai.ChatMessage{MessageId: id, ConversationId: conv, Body: "once 1"}, op)
	if failed(resp) {
		t.Fatalf("resend: %v", resp)
	}
	expectSeqs(t, "after a resend", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 10 page 0"), op2)), 1, 1)
}

// Messages are append-only, and the chat instance takes messages only from
// the router (or a ChatAgent) and reads only from the router.
func TestChatAppendOnlyAndRouterOnly(t *testing.T) {
	op, _, conv := newDM(t)
	id := sendN(t, op, conv, "keep", 1)[0]
	m := &l8ai.ChatMessage{MessageId: id, ConversationId: conv, Body: "changed"}
	for _, action := range []ifs.Action{ifs.PUT, ifs.PATCH, ifs.DELETE} {
		if resp := env.direct(action, m, op); !failed(resp) {
			t.Fatalf("action %v on a message was accepted", action)
		}
		if resp := env.call(aicommon.ChatMsgService, action, m, op); !failed(resp) {
			t.Fatalf("action %v through the router was accepted", action)
		}
	}
	if resp := env.direct(ifs.POST, &l8ai.ChatMessage{MessageId: "x-" + id, ConversationId: conv, Body: "sneak"}, op); !failed(resp) ||
		!strings.Contains(resp.Error().Error(), "only from the router") {
		t.Fatalf("a message posted straight to the instance: %v", resp)
	}
}

// A user who is not a member can neither read nor post.
func TestChatMembership(t *testing.T) {
	op, _, conv := newDM(t)
	sendN(t, op, conv, "private", 1)
	viewer := sessionViewer
	if resp := env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 10 page 0"), viewer); !failed(resp) && len(messages(resp)) > 0 {
		t.Fatalf("a non-member read the conversation: %v", seqsOf(messages(resp)))
	}
	if _, resp := env.send(viewer, conv, "intruder"); !failed(resp) {
		t.Fatal("a non-member posted to the conversation")
	}
}

// The window holds the latest RingSize messages; reads past it come from
// chat-orm, including the whole history (limit 0) right after new messages
// that are still in the write-behind.
func TestChatWindowAndOlderReads(t *testing.T) {
	op, op2, conv := newDM(t)
	sendN(t, op, conv, "early", 20)
	time.Sleep(2 * time.Second) // the write-behind flushes every second
	sendN(t, op, conv, "late", 5)
	expectSeqs(t, "whole history right after the last sends", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 0 page 0"), op2)), 1, 25)
	expectSeqs(t, "newest page of 5", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 5 page 0"), op2)), 21, 25)
	expectSeqs(t, "seq<11 limit 5 (older than the window)", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, " and seq<11", "limit 5 page 0"), op2)), 6, 10)
	expectSeqs(t, "page 3 of 5 (crosses the window)", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 5 page 3"), op2)), 6, 10)
	expectSeqs(t, "page 1 of 8 (window + chat-orm)", messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 8 page 1"), op2)), 10, 17)
}

// The gap fill: a browser behind the retained notifications gets a snapshot
// of the messages after its last message, newest PageSize of them.
func TestChatGapFillSnapshot(t *testing.T) {
	op, op2, conv := newDM(t)
	sendN(t, op, conv, "gap", 12)
	resp := env.read(aicommon.ChatSyncService, "select * from ChatSync where conversationId="+conv+" and notifSeq>0 and lastMsgSeq=3", op2)
	if failed(resp) {
		t.Fatalf("gap fill: %v", resp)
	}
	sync, _ := resp.Element().(*l8ai.ChatSync)
	if sync == nil || !sync.Snapshot {
		t.Fatalf("expected a snapshot: %+v", sync)
	}
	expectSeqs(t, "snapshot after seq 3", sync.Messages, 4, 12)
}

// A new message is pushed only to the sessions displaying the conversation's
// latest page; an older-page registration and an unregistered component get
// nothing.
func TestChatLivePushLatestPageOnly(t *testing.T) {
	op, op2, conv := newDM(t)
	sendN(t, op, conv, "before", 3)
	if resp := env.register(msgQuery(conv, "", "limit 10 page 0"), op2, "thread"); failed(resp) {
		t.Fatalf("register the latest page: %v", resp)
	}
	if resp := env.register(msgQuery(conv, " and seq<3", "limit 10 page 0"), op, "older"); failed(resp) {
		t.Fatalf("register an older page: %v", resp)
	}
	id := sendN(t, op, conv, "live", 1)[0]
	var aaaIds map[string]bool
	for i := 0; i < 50 && aaaIds == nil; i++ {
		if sets := pushesFor(id); len(sets) > 0 {
			aaaIds = sets[0].AaaIds
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !aaaIds[op2] || aaaIds[op] {
		t.Fatalf("push addressed to %v, expected only the latest-page session (%d pushes captured)", aaaIds, capturedCount())
	}
	if resp := env.unregister(msgQuery(conv, "", "limit 10 page 0"), op2, "thread"); failed(resp) {
		t.Fatalf("unregister: %v", resp)
	}
	id = sendN(t, op, conv, "after", 1)[0]
	time.Sleep(time.Second)
	if sets := pushesFor(id); len(sets) != 0 {
		t.Fatalf("a push after the latest-page component unregistered: %v", sets[0].AaaIds)
	}
}

// An idle conversation's window is evicted; a message to it is accepted
// without a reload and keeps the seq sequence, and the next read reloads the
// window and returns the full latest page. Eviction runs with the
// once-a-minute maintenance.
func TestChatIdleEviction(t *testing.T) {
	op, op2, conv := newDM(t)
	sendN(t, op, conv, "old", 3)
	time.Sleep(testRingEvict + 65*time.Second)
	sendN(t, op, conv, "new", 1)
	page := messages(env.read(aicommon.ChatMsgService, msgQuery(conv, "", "limit 10 page 0"), op2))
	expectSeqs(t, "after the eviction", page, 1, 4)
	if !strings.Contains(page[3].Body, "new 1") || !strings.Contains(page[0].Body, "old 1") {
		t.Fatalf("bodies after the eviction: %q .. %q", page[0].Body, page[3].Body)
	}
}
