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
	"testing"
	"time"

	aicommon "github.com/saichler/l8ai/go/ai/common"
)

var env *chatEnv

func TestMain(m *testing.M) {
	setup()
	env = startChatEnv(aicommon.NewChatConfig())
	code := m.Run()
	tear()
	os.Exit(code)
}

// A direct conversation: messages get seq 1..n, a read pages from the newest
// and returns the page oldest first, and seq< reads the page before it.
func TestChatSeqAndPaging(t *testing.T) {
	op := env.login("operator", "Oper123!")
	op2 := env.login("operator2", "Oper123!")
	conv, resp := env.createDM(op, "operator", "operator2")
	if resp == nil || resp.Error() != nil {
		t.Fatalf("create the conversation: %v", resp)
	}
	time.Sleep(time.Second)
	for i := 1; i <= 5; i++ {
		if _, resp := env.send(op, conv, "message "+strconv.Itoa(i)); resp == nil || resp.Error() != nil {
			t.Fatalf("send %d: %v", i, resp)
		}
	}
	page := messages(env.read(aicommon.ChatMsgService, "select * from ChatMessage where conversationId="+conv+
		" sort-by seq descending limit 2 page 0", op2))
	if len(page) != 2 || page[0].Seq != 4 || page[1].Seq != 5 {
		t.Fatalf("newest page of 2: %v", seqsOf(page))
	}
	older := messages(env.read(aicommon.ChatMsgService, "select * from ChatMessage where conversationId="+conv+
		" and seq<4 sort-by seq descending limit 2 page 0", op2))
	if len(older) != 2 || older[0].Seq != 2 || older[1].Seq != 3 {
		t.Fatalf("seq<4 limit 2: %v", seqsOf(older))
	}
}
