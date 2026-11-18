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
	"encoding/json"
	"runtime/debug"

	"github.com/saichler/l8ai/go/ai/agent/convctx"
	"github.com/saichler/l8ai/go/ai/agent/executor"
	"github.com/saichler/l8ai/go/ai/agent/llm"
	"github.com/saichler/l8ai/go/ai/agent/masking"
	"github.com/saichler/l8ai/go/ai/agent/schema"
	"github.com/saichler/l8ai/go/ai/agent/tools"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
)

// turnRun is the state of one turn on this agent instance.
type turnRun struct {
	turn    *l8ai.ChatAgentTurn
	source  string // the dispatching chat instance; the reply goes back to it
	aaaId   string // the requester's session; every tool call runs with it
	profile *llm.Profile
	client  llm.Client
	header  *l8ai.ChatConversation
	tail    []*l8ai.ChatMessage
	masker  *masking.Masker
	tokens  int
	call    *executor.Call // the data tools' calls; carries the files made for the answer
}

// run is one turn (plan §7.4–7.5). A failure that the user can act on ends
// the turn with a SYSTEM message; anything else is logged and left to the
// owner's watchdog, which re-dispatches or fails the turn.
func (this *Agent) run(t *l8ai.ChatAgentTurn, source, aaaId string) {
	if !this.begin(t.TurnId) {
		return
	}
	defer this.end(t.TurnId)
	this.slots <- struct{}{}
	defer func() { <-this.slots }()
	r := &turnRun{turn: t, source: source, aaaId: aaaId}
	defer func() {
		// A turn that cannot finish ends now with a SYSTEM message, instead of
		// waiting for the owner's watchdog.
		if p := recover(); p != nil {
			this.vnic.Resources().Logger().Error("ChatAgent: turn ", t.TurnId, " panicked: ", p, "\n", string(debug.Stack()))
			this.reply(r, l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM, failedText)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), this.settings.Chat.AgentTurnTimeout)
	defer cancel()

	if _, _, err := this.vnic.Resources().Security().UserInfo(aaaId, this.vnic); err != nil {
		this.reply(r, l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM, sessionExpiredText)
		return
	}
	kind, baseURL, apiKey, model, err := this.vnic.Resources().Security().LlmProfile(t.LlmProfileId, this.vnic.Resources())
	if err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: LLM profile ", t.LlmProfileId, ": ", err.Error())
		this.reply(r, l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM, notConfiguredText)
		return
	}
	r.profile = &llm.Profile{Kind: kind, BaseURL: baseURL, APIKey: apiKey, Model: model}
	if r.client, err = this.factory.Client(r.profile); err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: ", err.Error())
		this.reply(r, l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM, notConfiguredText)
		return
	}
	if r.header, err = this.fetchHeader(t.ConversationId, aaaId); err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: turn ", t.TurnId, ": ", err.Error())
		return
	}
	messages, err := this.fetchTail(t.ConversationId, r.header.SummaryUpToSeq, aaaId)
	if err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: turn ", t.TurnId, ": ", err.Error())
		return
	}
	r.tail = convctx.Tail(messages)
	inlineAttachments(r.tail, aaaId, this.download)
	r.masker = masking.NewMasker(this.masks, this.secret, t.ConversationId)

	text := ""
	mode, question := parsePrefix(lastUserText(r.tail))
	switch mode {
	case modeHelp:
		text, err = this.answerHelp(ctx, r, question)
	case modeWeb:
		text, err = this.answerWeb(ctx, r, question)
	default:
		text, err = this.answerData(ctx, r)
	}
	if err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: turn ", t.TurnId, ": ", err.Error())
		this.reply(r, l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM, failedText)
		return
	}
	this.reply(r, l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_AGENT, r.masker.Unmask(text))
	go this.compactIfNeeded(r)
}

// answerData is the default mode: the data tools in a budgeted tool loop.
func (this *Agent) answerData(ctx context.Context, r *turnRun) (string, error) {
	allowed := this.allowedModels(r.aaaId)
	entries := this.catalog.Entries(allowed)
	models := make([]string, 0, len(entries))
	for _, e := range entries {
		models = append(models, e.Model)
	}
	system := DefaultSystemPrompt
	if this.settings.SystemPrompt != "" {
		system += "\n\n" + this.settings.SystemPrompt
	}
	system += "\n\n" + schema.Tier1(entries)

	// A context that does not fit even before the tool loop is compacted first.
	if convctx.Tokens(system)+convctx.Tokens(r.header.Summary)+convctx.TailTokens(r.tail) > this.budget() {
		this.compact(ctx, r)
	}
	req := &llm.Request{System: system, Context: convctx.SummaryContext(r.header.Summary, r.masker),
		Messages: convctx.Messages(r.tail, r.masker, stripPrefix), Tools: tools.Data(models),
		MaxTokens: this.settings.Chat.MaxOutputTokens}
	call := &executor.Call{AaaId: r.aaaId, TurnId: r.turn.TurnId, ConversationId: r.turn.ConversationId, Allowed: allowed}
	r.call = call
	return this.toolLoop(ctx, r, req, func(name, input string) (string, string, error) {
		return this.exec.Execute(name, input, call)
	})
}

// toolLoop calls the model and runs its tool calls until it answers, the tool
// budget is spent or the context reaches 90% of the budget; then it asks for
// a final answer without tools (assessment §3.2).
func (this *Agent) toolLoop(ctx context.Context, r *turnRun, req *llm.Request,
	execute func(name, input string) (string, string, error)) (string, error) {
	maxResult := this.settings.Chat.MaxToolResultTokens * 4
	for i := 0; i < this.settings.MaxToolCalls; i++ {
		resp, err := r.client.Send(ctx, req)
		if err != nil {
			return "", err
		}
		r.tokens += resp.Usage.InputTokens + resp.Usage.OutputTokens
		if resp.StopReason != "tool_use" {
			return resp.Text(), nil
		}
		results := make([]llm.ToolResultContent, 0)
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			result := llm.ToolResultContent{Type: "tool_result", ToolUseID: block.ID}
			out, model, err := execute(block.Name, unmaskInput(r.masker, block.Name, string(block.Input)))
			if err != nil {
				result.Content, result.IsError = "Error: "+err.Error(), true
			} else {
				result.Content = truncate(r.masker.MaskJSON(out, model), maxResult)
			}
			results = append(results, result)
		}
		req.Messages = append(req.Messages, llm.Message{Role: "assistant", Content: resp.Content},
			llm.Message{Role: "user", Content: results})
		if resp.Usage.InputTokens+this.settings.Chat.MaxOutputTokens > this.budget()*9/10 {
			break
		}
	}
	final := *req
	final.System += finalAnswerPrompt
	final.NoTools = true
	resp, err := r.client.Send(ctx, &final)
	if err != nil {
		return "", err
	}
	r.tokens += resp.Usage.InputTokens + resp.Usage.OutputTokens
	if text := resp.Text(); resp.StopReason != "tool_use" && text != "" {
		return text, nil
	}
	return toolBudgetReply, nil
}

// allowedModels returns the models the requester's roles may access, or nil
// when the security provider does not restrict them.
func (this *Agent) allowedModels(aaaId string) map[string]bool {
	types := this.vnic.Resources().Security().AllowedTypes(this.vnic, aaaId)
	if types == nil {
		return nil
	}
	allowed := make(map[string]bool, len(types))
	for _, t := range types {
		allowed[t] = true
	}
	return allowed
}

// reply posts Tanto's message straight to the dispatching chat instance. Its
// ID is derived from the turn, so a re-dispatched turn cannot reply twice.
func (this *Agent) reply(r *turnRun, kind l8ai.ChatMessageKind, body string) {
	m := &l8ai.ChatMessage{MessageId: aicommon.AgentReplyMessageID(r.turn.TurnId),
		ConversationId: r.turn.ConversationId, Kind: kind, Body: body, AgentTurnId: r.turn.TurnId,
		LlmProfileId: r.turn.LlmProfileId, TokenCount: int32(r.tokens)}
	// The files the turn made are attached to its answer.
	if kind == l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_AGENT && r.call != nil {
		m.Attachments = r.call.Files
	}
	if err := this.vnic.Unicast(r.source, aicommon.ChatShMsgService, aicommon.ServiceArea, ifs.POST, m); err != nil {
		this.vnic.Resources().Logger().Error("ChatAgent: reply to turn ", r.turn.TurnId, ": ", err.Error())
	}
}

// lastUserText is the newest user message of the tail.
func lastUserText(tail []*l8ai.ChatMessage) string {
	for i := len(tail) - 1; i >= 0; i-- {
		if tail[i].SenderId != aicommon.TantoUserId {
			return tail[i].Body
		}
	}
	return ""
}

func truncate(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "...[truncated: the result is too large; refine the query, add a filter or a smaller limit]"
}

// unmaskInput restores the masked values in a tool call's input. The text of
// a file Tanto writes gets the values as the user reads them (money formatted);
// any other input gets the raw values.
func unmaskInput(masker *masking.Masker, tool, input string) string {
	if tool != tools.CreateFile {
		return masker.UnmaskRaw(input)
	}
	args := map[string]interface{}{}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return masker.UnmaskRaw(input)
	}
	for k, v := range args {
		if s, ok := v.(string); ok {
			args[k] = masker.Unmask(s)
		}
	}
	out, err := json.Marshal(args)
	if err != nil {
		return masker.UnmaskRaw(input)
	}
	return string(out)
}
