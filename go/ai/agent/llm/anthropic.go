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
	"strings"
)

const anthropicVersion = "2023-06-01"

// anthropic speaks the Anthropic Messages API. The base URL and model come
// from the LLM profile; the system prompt and the tools are marked for prompt
// caching, so the static prefix is billed once per cache window.
type anthropic struct {
	url    string
	apiKey string
	model  string
	poster *poster
}

func newAnthropic(p *Profile, l *limiter) *anthropic {
	url := strings.TrimRight(p.BaseURL, "/")
	if !strings.HasSuffix(url, "/messages") {
		url += "/messages"
	}
	return &anthropic{url: url, apiKey: p.APIKey, model: p.Model, poster: newPoster(l)}
}

type cacheControl struct {
	Type string `json:"type"`
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	InputSchema  interface{}   `json:"input_schema"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type anthropicRequest struct {
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens"`
	System     []systemBlock   `json:"system,omitempty"`
	Messages   []Message       `json:"messages"`
	Tools      []anthropicTool `json:"tools,omitempty"`
	ToolChoice interface{}     `json:"tool_choice,omitempty"`
}

func (this *anthropic) Send(ctx context.Context, req *Request) (*Response, error) {
	ephemeral := &cacheControl{Type: "ephemeral"}
	body := &anthropicRequest{Model: this.model, MaxTokens: req.MaxTokens, Messages: req.Messages}
	if req.System != "" {
		body.System = []systemBlock{{Type: "text", Text: req.System, CacheControl: ephemeral}}
	}
	if req.Context != "" {
		body.System = append(body.System, systemBlock{Type: "text", Text: req.Context})
	}
	for i, t := range req.Tools {
		tool := anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
		if i == len(req.Tools)-1 {
			tool.CacheControl = ephemeral // caches every tool definition before it too
		}
		body.Tools = append(body.Tools, tool)
	}
	if req.NoTools && len(req.Tools) > 0 {
		body.ToolChoice = map[string]string{"type": "none"}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	out, err := this.poster.post(ctx, this.url, map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         this.apiKey,
		"anthropic-version": anthropicVersion,
	}, data)
	if err != nil {
		return nil, err
	}
	resp := &Response{}
	if err = json.Unmarshal(out, resp); err != nil {
		return nil, err
	}
	return resp, nil
}
