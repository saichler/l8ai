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

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// openAI speaks an OpenAI-compatible chat completions endpoint. The
// Anthropic-shaped Message, Tool and Response types are translated to and
// from the OpenAI wire format.
type openAI struct {
	url    string
	apiKey string
	model  string
	poster *poster
}

func newOpenAI(p *Profile, l *limiter, e *Endpoints) *openAI {
	return &openAI{url: strings.TrimRight(p.BaseURL, "/") + "/chat/completions", apiKey: p.APIKey,
		model: p.Model, poster: newPoster(l, e)}
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiRequest struct {
	Model      string       `json:"model"`
	MaxTokens  int          `json:"max_tokens"`
	Messages   []oaiMessage `json:"messages"`
	Tools      []oaiTool    `json:"tools,omitempty"`
	ToolChoice string       `json:"tool_choice,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message      oaiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (this *openAI) Send(ctx context.Context, req *Request) (*Response, error) {
	body := &oaiRequest{Model: this.model, MaxTokens: req.MaxTokens,
		Messages: toOpenAIMessages(joinSystem(req.System, req.Context), req.Messages), Tools: toOpenAITools(req.Tools)}
	if req.NoTools && len(req.Tools) > 0 {
		body.ToolChoice = "none"
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	out, err := this.poster.post(ctx, this.url, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + this.apiKey,
	}, data)
	if err != nil {
		return nil, err
	}
	resp := &oaiResponse{}
	if err = json.Unmarshal(out, resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("LLM API returned no choices")
	}
	return fromOpenAI(resp), nil
}

func strPtr(s string) *string { return &s }

func joinSystem(system, context string) string {
	if context == "" {
		return system
	}
	if system == "" {
		return context
	}
	return system + "\n\n" + context
}

func toOpenAIMessages(system string, messages []Message) []oaiMessage {
	out := make([]oaiMessage, 0, len(messages)+1)
	if system != "" {
		out = append(out, oaiMessage{Role: "system", Content: strPtr(system)})
	}
	for _, m := range messages {
		switch c := m.Content.(type) {
		case string:
			out = append(out, oaiMessage{Role: m.Role, Content: strPtr(c)})
		case []ContentBlock:
			msg := oaiMessage{Role: m.Role}
			text := ""
			for _, b := range c {
				switch b.Type {
				case "text":
					text += b.Text
				case "tool_use":
					tc := oaiToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					msg.ToolCalls = append(msg.ToolCalls, tc)
				}
			}
			if text != "" || len(msg.ToolCalls) == 0 {
				msg.Content = strPtr(text)
			}
			out = append(out, msg)
		case []ToolResultContent:
			for _, r := range c {
				out = append(out, oaiMessage{Role: "tool", ToolCallID: r.ToolUseID, Content: strPtr(r.Content)})
			}
		}
	}
	return out
}

func toOpenAITools(tools []Tool) []oaiTool {
	out := make([]oaiTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, oaiTool{Type: "function", Function: oaiFunction{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}
	return out
}

func fromOpenAI(oai *oaiResponse) *Response {
	choice := oai.Choices[0]
	resp := &Response{Usage: Usage{InputTokens: oai.Usage.PromptTokens, OutputTokens: oai.Usage.CompletionTokens}}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		resp.Content = append(resp.Content, ContentBlock{Type: "text", Text: *choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		resp.Content = append(resp.Content, ContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name,
			Input: json.RawMessage(args)})
	}
	switch {
	case len(choice.Message.ToolCalls) > 0:
		resp.StopReason = "tool_use"
	case choice.FinishReason == "length":
		resp.StopReason = "max_tokens"
	default:
		resp.StopReason = "end_turn"
	}
	return resp
}
