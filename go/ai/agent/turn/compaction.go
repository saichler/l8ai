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
	"context"
	"strings"
	"time"

	"github.com/saichler/l8ai/go/ai/agent/convctx"
	"github.com/saichler/l8ai/go/ai/agent/llm"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

const compactionTimeout = 2 * time.Minute

// compactIfNeeded runs after the reply is posted, so the user never waits
// for it: when the summary and tail exceed the threshold share of the budget,
// or the tail is too long (assessment §4.1).
func (this *Agent) compactIfNeeded(r *turnRun) {
	cfg := this.settings.Chat
	used := convctx.Tokens(r.header.Summary) + convctx.TailTokens(r.tail)
	if used*100 <= this.budget()*cfg.CompactionThresholdPc && len(r.tail) <= cfg.CompactionMaxTail {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), compactionTimeout)
	defer cancel()
	this.compact(ctx, r)
}

// compact folds the oldest part of the tail into the summary (assessment
// §4.2). The summary is stored raw: it is masked again on every use. The
// owner applies it only if its summary position is still the one this
// compaction started from (compare-and-set), so racing compactions cannot
// clobber each other. On success the turn continues with the new summary.
func (this *Agent) compact(ctx context.Context, r *turnRun) {
	conversationId := r.turn.ConversationId
	if !this.startCompaction(conversationId) {
		return
	}
	defer this.endCompaction(conversationId)
	chunk := convctx.CompactionChunk(r.tail, this.settings.Chat.VerbatimTail)
	if len(chunk) == 0 {
		return
	}
	user := "Current summary:\n" + r.masker.MaskText(r.header.Summary) +
		"\n\nNext part of the conversation:\n" + convctx.Transcript(chunk, r.masker)
	resp, err := r.client.Send(ctx, &llm.Request{System: compactionPrompt,
		Messages: []llm.Message{{Role: "user", Content: user}}, MaxTokens: this.settings.Chat.MaxOutputTokens})
	if err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: compaction of ", conversationId, ": ", err.Error())
		return
	}
	summary := strings.TrimSpace(r.masker.Unmask(resp.Text()))
	if summary == "" {
		return
	}
	upTo := chunk[len(chunk)-1].Seq
	patch := &l8ai.ChatConversation{ConversationId: conversationId, Summary: summary, SummaryUpToSeq: upTo,
		ExpectedSummaryUpToSeq: r.header.SummaryUpToSeq}
	if err = this.vnic.Unicast(r.source, aicommon.ChatShCnvService, aicommon.ServiceArea, ifs.PATCH, patch); err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: compaction of ", conversationId, ": ", err.Error())
		return
	}
	this.vnic.Resources().Logger().Info("ChatAgent: l8ai_compaction_ratio ", conversationId, " ",
		convctx.Tokens(r.header.Summary)+convctx.TailTokens(chunk), "->", convctx.Tokens(summary))
	r.header.Summary, r.header.SummaryUpToSeq = summary, upTo
	r.tail = r.tail[len(chunk):]
}
