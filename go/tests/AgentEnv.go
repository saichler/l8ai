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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/saichler/l8ai/go/ai/agent"
	"github.com/saichler/l8ai/go/ai/agent/executor"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8services/go/services/filestore"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// The "local" LLM profile of the probler security configuration points at
// 127.0.0.1:19080; the stub LLM serves it, and 19081 is its fallback origin.
const (
	stubMain     = "127.0.0.1:19080"
	stubFallback = "127.0.0.1:19081"
	localProfile = "local"
)

// stubLLM is an OpenAI-compatible chat completions server the tests script:
// each request is recorded and answered by the current script.
type stubLLM struct {
	mtx      sync.Mutex
	requests []stubRequest
	script   func(req stubRequest) stubAnswer
	// down holds the origins that drop every connection (a connection-level
	// failure, the kind that makes the agent fall back).
	down map[string]bool
}

type stubRequest struct {
	Origin   string
	Model    string        `json:"model"`
	Messages []stubMessage `json:"messages"`
	Tools    []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

type stubMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []stubToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type stubToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// stubAnswer is what the stub replies: text, or tool calls, or a failure
// (Status) or a delay before answering.
type stubAnswer struct {
	Text      string
	ToolCalls []stubToolCall
	Status    int
	Delay     time.Duration
}

var llm = &stubLLM{script: func(stubRequest) stubAnswer { return stubAnswer{Text: "ok"} }, down: map[string]bool{}}

// setDown makes an origin drop (or stop dropping) every connection.
func (this *stubLLM) setDown(addr string, down bool) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.down[addr] = down
}

// drop closes the connection without an HTTP answer when the origin is down.
func (this *stubLLM) drop(addr string, w http.ResponseWriter) bool {
	this.mtx.Lock()
	down := this.down[addr]
	this.mtx.Unlock()
	if !down {
		return false
	}
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
		}
	}
	return true
}

func (this *stubLLM) serve(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if this.drop(addr, w) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		req := stubRequest{}
		_ = json.Unmarshal(body, &req)
		req.Origin = addr
		this.mtx.Lock()
		this.requests = append(this.requests, req)
		script := this.script
		this.mtx.Unlock()
		answer := script(req)
		if answer.Delay > 0 {
			time.Sleep(answer.Delay)
		}
		if answer.Status != 0 {
			w.WriteHeader(answer.Status)
			return
		}
		msg := stubMessage{Role: "assistant", ToolCalls: answer.ToolCalls}
		finish := "tool_calls"
		if len(answer.ToolCalls) == 0 {
			text := answer.Text
			msg.Content = &text
			finish = "stop"
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "stub", "choices": []interface{}{map[string]interface{}{"message": msg, "finish_reason": finish}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1}})
	})
	// Any other path answers too (the endpoint probe).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !this.drop(addr, w) {
			w.WriteHeader(http.StatusOK)
		}
	})
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			panic("stub LLM on " + addr + ": " + err.Error())
		}
	}()
}

// use sets the script and clears the recorded requests.
func (this *stubLLM) use(script func(req stubRequest) stubAnswer) {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	this.script = script
	this.requests = nil
}

// recorded returns the requests the stub got since the last use.
func (this *stubLLM) recorded() []stubRequest {
	this.mtx.Lock()
	defer this.mtx.Unlock()
	return append([]stubRequest(nil), this.requests...)
}

// lastUserText returns the content of the request's last user message.
func (this stubRequest) lastUserText() string {
	for i := len(this.Messages) - 1; i >= 0; i-- {
		if this.Messages[i].Role == "user" && this.Messages[i].Content != nil {
			return *this.Messages[i].Content
		}
	}
	return ""
}

// hasToolResult reports whether the request carries a tool result.
func (this stubRequest) hasToolResult() bool {
	for _, m := range this.Messages {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}

// toolCall builds a tool call the stub returns.
func toolCall(name string, args map[string]interface{}) stubToolCall {
	data, _ := json.Marshal(args)
	call := stubToolCall{ID: "call-" + uuid.NewString()[:8], Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = string(data)
	return call
}

// startAgent starts the stub LLM, a FileStore and a ChatAgent on the topology.
func (this *chatEnv) startAgent(cfg *aicommon.ChatConfig) {
	llm.serve(stubMain)
	llm.serve(stubFallback)
	dir, err := os.MkdirTemp("", "l8ai-filestore-")
	if err != nil {
		panic(err)
	}
	executor.RegisterFileTypes(this.client.Resources().Registry())
	filestore.SetStorageRoot(dir)
	filestore.Activate(topo.VnicByVnetNum(3, 3))
	activateTestEmployees(topo.VnicByVnetNum(2, 3))
	registerTestTypes(this.client.Resources())
	agent.ActivateAgent(agent.AgentConfig{
		RegisterTypes: registerTestTypes,
		ServiceMap:    testServiceMap,
		Chat:          cfg,
		SystemPrompt:  "You are Tanto, the test agent.",
		MaskingSecret: "l8ai-test-masking-secret-0123456789",
		Fallbacks:     map[string]string{"http://" + stubMain: "http://" + stubFallback},
	}, topo.VnicByVnetNum(3, 2))
}

// createAgentConversation creates a conversation with Tanto for the user.
func (this *chatEnv) createAgentConversation(aaaId, user string) (string, ifs.IElements) {
	id := uuid.NewString()
	resp := this.call(aicommon.ChatConvService, ifs.POST, &l8ai.ChatConversation{ConversationId: id,
		PeerKind: l8ai.ChatPeerKind_CHAT_PEER_KIND_AGENT, PeerKey: "agent|" + user, Title: "tanto " + id[:8]}, aaaId)
	return id, resp
}

// ask sends a message to Tanto on the local profile.
func (this *chatEnv) ask(aaaId, conversationId, body string, attachments ...*l8ai.ChatAttachment) ifs.IElements {
	return this.call(aicommon.ChatMsgService, ifs.POST, &l8ai.ChatMessage{MessageId: uuid.NewString(),
		ConversationId: conversationId, Body: body, LlmProfileId: localProfile, Attachments: attachments}, aaaId)
}

// waitForTanto waits for Tanto's message after seq "after" and returns it.
func (this *chatEnv) waitForTanto(aaaId, conversationId string, after int64, timeout time.Duration) *l8ai.ChatMessage {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range messages(this.read(aicommon.ChatMsgService, "select * from ChatMessage where conversationId="+
			conversationId+" sort-by seq descending limit 10 page 0", aaaId)) {
			if m.Seq > after && m.SenderId == aicommon.TantoUserId {
				return m
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// upload stores a file through the FileStore service as the user and returns
// the attachment a message carries for it.
func (this *chatEnv) upload(aaaId, conversationId, name, mimeType string, data []byte) (*l8ai.ChatAttachment, error) {
	resp := this.client.ProximityRequest(filestore.ServiceName, filestore.ServiceArea, ifs.POST,
		&l8api.L8FileUploadRequest{FileName: name, MimeType: mimeType, FileData: data, DocumentId: conversationId,
			Version: 1}, requestTimeout, aaaId)
	if failed(resp) {
		return nil, errors.New(errOf(resp))
	}
	up, ok := resp.Element().(*l8api.L8FileUploadResponse)
	if !ok || up == nil || up.StoragePath == "" {
		return nil, errors.New("unexpected file store response")
	}
	return &l8ai.ChatAttachment{AttachmentId: uuid.NewString(), StoragePath: up.StoragePath, FileName: name,
		FileSize: int64(len(data)), MimeType: mimeType}, nil
}

// download reads a file through the FileStore service as the user.
func (this *chatEnv) download(aaaId, storagePath string) ([]byte, error) {
	resp := this.client.ProximityRequest(filestore.ServiceName, filestore.ServiceArea, ifs.PUT,
		&l8api.L8FileDownloadRequest{StoragePath: storagePath}, requestTimeout, aaaId)
	if failed(resp) {
		return nil, errors.New(errOf(resp))
	}
	file, ok := resp.Element().(*l8api.L8FileDownloadResponse)
	if !ok || file == nil {
		return nil, errors.New("unexpected file store response")
	}
	return file.FileData, nil
}

// userTexts returns the content of every user message of the request.
func (this stubRequest) userTexts() string {
	result := ""
	for _, m := range this.Messages {
		if m.Role == "user" && m.Content != nil {
			result += *m.Content + "\n"
		}
	}
	return result
}

// assistantTexts returns the content of every assistant message of the request.
func (this stubRequest) assistantTexts() string {
	result := ""
	for _, m := range this.Messages {
		if m.Role == "assistant" && m.Content != nil {
			result += *m.Content + "\n"
		}
	}
	return result
}
