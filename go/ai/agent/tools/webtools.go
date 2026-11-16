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

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/saichler/l8ai/go/ai/agent/llm"
)

// Web tool names (the '@web' mode, AgentConfig.WebToolsURL).
const (
	WebSearch = "web_search"
	FetchURL  = "fetch_url"
)

const (
	webMaxResultChars = 12000
	webRequestTimeout = 45 * time.Second
	webDialTimeout    = 6 * time.Second
	webAttempts       = 4
)

// Web calls a web tools service (POST /v1/search and /v1/fetch, bearer-key
// authenticated), as l8agent's '@web' mode does.
type Web struct {
	baseURL string
	http    *http.Client
}

// NewWeb returns a client for the service at baseURL, or nil if it is empty.
func NewWeb(baseURL string) *Web {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: webDialTimeout}).DialContext,
		TLSHandshakeTimeout: webDialTimeout,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Web{baseURL: baseURL, http: &http.Client{Timeout: webRequestTimeout, Transport: transport}}
}

// WebDefinitions returns the web tools offered to the model.
func WebDefinitions() []llm.Tool {
	return []llm.Tool{
		{Name: WebSearch, Description: "Search the web. Returns titles, URLs and snippets. Use it for current or real-world information.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"query":       map[string]interface{}{"type": "string", "description": "The search query"},
				"max_results": map[string]interface{}{"type": "integer", "description": "Number of results (1-10)"},
			}, "required": []string{"query"}}},
		{Name: FetchURL, Description: "Fetch a web page and return its text. Use it after web_search to read a result.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"url":       map[string]interface{}{"type": "string", "description": "The http(s) URL to read"},
				"max_chars": map[string]interface{}{"type": "integer", "description": "Maximum characters to return"},
			}, "required": []string{"url"}}},
	}
}

// Execute runs one web tool call. The API key is the turn's LLM profile key.
func (this *Web) Execute(ctx context.Context, name, input, apiKey string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid tool arguments: %w", err)
	}
	path := ""
	switch name {
	case WebSearch:
		path = "/v1/search"
		if _, ok := args["max_results"]; !ok {
			args["max_results"] = 5
		}
	case FetchURL:
		path = "/v1/fetch"
		if _, ok := args["max_chars"]; !ok {
			args["max_chars"] = 6000
		}
	default:
		return "", fmt.Errorf("unknown web tool %q", name)
	}
	body, _ := json.Marshal(args)
	resp, err := this.post(ctx, path, body, apiKey)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web tools returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	out := string(data)
	if len(out) > webMaxResultChars {
		out = out[:webMaxResultChars] + "...[truncated]"
	}
	return out, nil
}

// post sends the request, retrying when the connection cannot be opened.
func (this *Web) post(ctx context.Context, path string, body []byte, apiKey string) (*http.Response, error) {
	var lastErr error
	for i := 0; i < webAttempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, this.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := this.http.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var nerr net.Error
		if ctx.Err() != nil || !errors.As(err, &nerr) {
			return nil, err
		}
	}
	return nil, lastErr
}
