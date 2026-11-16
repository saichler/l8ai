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

// Package llm holds the LLM clients of the agent: Anthropic (with prompt
// caching on the system prompt and tools) and OpenAI-compatible, both with
// retry/backoff and a per-provider request limiter (plan §7).
package llm

import (
	"context"
	"encoding/json"
)

// Message is one chat message. Content is a string, a []ContentBlock (an
// assistant turn with tool calls) or a []ToolResultContent (tool results).
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// ContentBlock is a block of a model response.
type ContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ToolResultContent returns a tool's result to the model.
type ToolResultContent struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Usage is the token usage of one call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is a model response. StopReason is "end_turn", "tool_use" or "max_tokens".
type Response struct {
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// Text returns the text of the response.
func (this *Response) Text() string {
	text := ""
	for _, block := range this.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}

// Request is one call to the model.
type Request struct {
	System    string // static system prompt; cached by providers that support it
	Context   string // per-conversation context (summary) after the cached prefix
	Messages  []Message
	Tools     []Tool
	NoTools   bool // the tools stay defined but the model must answer with text
	MaxTokens int
}

// Client is an LLM client.
type Client interface {
	Send(ctx context.Context, req *Request) (*Response, error)
}

// Profile is the resolved LLM profile: the values ISecurityProvider.LlmProfile returns.
type Profile struct {
	Kind    string
	BaseURL string
	APIKey  string
	Model   string
}

// Provider kinds, as ISecurityProvider.LlmProfile returns them.
const (
	KindAnthropic        = "L8_LLM_PROVIDER_KIND_ANTHROPIC"
	KindOpenAICompatible = "L8_LLM_PROVIDER_KIND_OPENAI_COMPATIBLE"
)
