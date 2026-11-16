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

// Package convctx builds the token-budgeted context of a turn (assessment §3):
// the conversation summary, then the tail of messages after it as user and
// assistant text, every text masked on every call. Earlier tool calls and
// results are never replayed; only the final assistant text is kept.
package convctx

import (
	"sort"
	"strings"

	"github.com/saichler/l8ai/go/ai/agent/llm"
	"github.com/saichler/l8ai/go/ai/agent/masking"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
)

// Tokens estimates the tokens of a text (about four characters per token).
func Tokens(text string) int {
	return len(text)/4 + 1
}

// Budget is the context budget: the context window, less the output and a 5% margin.
func Budget(contextWindow, maxOutput int) int {
	return (contextWindow - maxOutput) * 95 / 100
}

// Tail returns the conversation messages a turn sees: text from users and
// Tanto, ascending by seq. SYSTEM messages are left out.
func Tail(messages []*l8ai.ChatMessage) []*l8ai.ChatMessage {
	result := make([]*l8ai.ChatMessage, 0, len(messages))
	for _, m := range messages {
		if m != nil && m.Kind != l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM && strings.TrimSpace(m.Body) != "" {
			result = append(result, m)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq < result[j].Seq })
	return result
}

// Messages renders the tail as masked user/assistant messages. Consecutive
// user messages (a coalesced turn) are joined into one. strip removes a mode
// prefix (e.g. "@web") from user text.
func Messages(tail []*l8ai.ChatMessage, masker *masking.Masker, strip func(string) string) []llm.Message {
	result := make([]llm.Message, 0, len(tail))
	for _, m := range tail {
		role, text := "user", m.Body
		if m.SenderId == aicommon.TantoUserId {
			role = "assistant"
		} else if strip != nil {
			text = strip(text)
		}
		text = masker.MaskText(text)
		if n := len(result); n > 0 && result[n-1].Role == role {
			result[n-1].Content = result[n-1].Content.(string) + "\n\n" + text
			continue
		}
		result = append(result, llm.Message{Role: role, Content: text})
	}
	// The model needs the conversation to start with the user.
	for len(result) > 0 && result[0].Role != "user" {
		result = result[1:]
	}
	return result
}

// SummaryContext renders the masked summary for the request's Context part.
func SummaryContext(summary string, masker *masking.Masker) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	return "Summary of the earlier part of this conversation:\n" + masker.MaskText(summary)
}

// TailTokens estimates the tokens of the tail.
func TailTokens(tail []*l8ai.ChatMessage) int {
	total := 0
	for _, m := range tail {
		total += Tokens(m.Body)
	}
	return total
}

// CompactionChunk returns the oldest messages to fold into the summary: up to
// about half of the tail's tokens, never the newest verbatim messages.
func CompactionChunk(tail []*l8ai.ChatMessage, verbatim int) []*l8ai.ChatMessage {
	if len(tail) <= verbatim {
		return nil
	}
	candidates := tail[:len(tail)-verbatim]
	half := TailTokens(tail) / 2
	total := 0
	for i, m := range candidates {
		total += Tokens(m.Body)
		if total >= half {
			return candidates[:i+1]
		}
	}
	return candidates
}

// Transcript renders messages as plain text for the compaction call.
func Transcript(messages []*l8ai.ChatMessage, masker *masking.Masker) string {
	buff := strings.Builder{}
	for _, m := range messages {
		who := "User"
		if m.SenderId == aicommon.TantoUserId {
			who = "Assistant"
		}
		buff.WriteString(who)
		buff.WriteString(": ")
		buff.WriteString(masker.MaskText(m.Body))
		buff.WriteString("\n\n")
	}
	return buff.String()
}
