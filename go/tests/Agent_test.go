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
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/saichler/l8ai/go/types/l8ai"
)

const tantoTimeout = 60 * time.Second

// newTantoConversation creates a conversation with Tanto for the operator.
func newTantoConversation(t *testing.T) string {
	conv, resp := env.createAgentConversation(sessionOp, "operator")
	if failed(resp) {
		t.Fatalf("create the Tanto conversation: %v", errOf(resp))
	}
	return conv
}

// Tanto answers a message: the user's text reaches the LLM and the LLM's
// answer comes back as Tanto's message.
func TestAgentReplies(t *testing.T) {
	llm.use(func(req stubRequest) stubAnswer { return stubAnswer{Text: "pong from the stub"} })
	conv := newTantoConversation(t)
	if resp := env.ask(sessionOp, conv, "ping"); failed(resp) {
		t.Fatalf("ask: %v", errOf(resp))
	}
	reply := env.waitForTanto(sessionOp, conv, 1, tantoTimeout)
	if reply == nil || reply.Kind != l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_AGENT || !strings.Contains(reply.Body, "pong from the stub") {
		t.Fatalf("Tanto's reply: %+v", reply)
	}
	reqs := llm.recorded()
	if len(reqs) == 0 || !strings.Contains(reqs[len(reqs)-1].lastUserText(), "ping") {
		t.Fatalf("the LLM did not get the user's text (%d requests)", len(reqs))
	}
}

// A file Tanto writes (create_file) is attached to its answer.
func TestAgentCreatesFile(t *testing.T) {
	llm.use(func(req stubRequest) stubAnswer {
		if req.hasToolResult() {
			return stubAnswer{Text: "Here is your report."}
		}
		return stubAnswer{ToolCalls: []stubToolCall{toolCall("create_file",
			map[string]interface{}{"file_name": "report.md", "content": "# Report\nAll good."})}}
	})
	conv := newTantoConversation(t)
	if resp := env.ask(sessionOp, conv, "write me a report"); failed(resp) {
		t.Fatalf("ask: %v", errOf(resp))
	}
	reply := env.waitForTanto(sessionOp, conv, 1, tantoTimeout)
	if reply == nil || len(reply.Attachments) != 1 || reply.Attachments[0].FileName != "report.md" ||
		reply.Attachments[0].StoragePath == "" {
		t.Fatalf("Tanto's reply with the file: %+v", reply)
	}
}

// askAndWait asks Tanto and returns the LLM request that answered it and
// Tanto's reply.
func askAndWait(t *testing.T, conv, body string, after int64, attachments ...*l8ai.ChatAttachment) (stubRequest, *l8ai.ChatMessage) {
	t.Helper()
	if resp := env.ask(sessionOp, conv, body, attachments...); failed(resp) {
		t.Fatalf("ask: %v", errOf(resp))
	}
	reply := env.waitForTanto(sessionOp, conv, after, tantoTimeout)
	if reply == nil {
		t.Fatal("Tanto did not answer")
	}
	reqs := llm.recorded()
	if len(reqs) == 0 {
		t.Fatal("the LLM got no request")
	}
	return reqs[len(reqs)-1], reply
}

func mustUpload(t *testing.T, conv, name, mimeType string, data []byte) *l8ai.ChatAttachment {
	t.Helper()
	a, err := env.upload(sessionOp, conv, name, mimeType, data)
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	return a
}

func answer(text string) func(stubRequest) stubAnswer {
	return func(stubRequest) stubAnswer { return stubAnswer{Text: text} }
}

// A text file attached to a message is part of the message the LLM reads,
// read as the user; a message with only an attachment reaches the LLM too.
func TestAgentReadsAttachedFiles(t *testing.T) {
	llm.use(answer("read it"))
	conv := newTantoConversation(t)
	a := mustUpload(t, conv, "test2.txt", "text/plain", []byte("how many network devices are there\n"))
	req, _ := askAndWait(t, conv, "can you read the attached file?", 1, a)
	want := "can you read the attached file?\n\n[Attached file \"test2.txt\"]\nhow many network devices are there\n\n" +
		"[End of attached file \"test2.txt\"]"
	if !strings.Contains(req.userTexts(), want) {
		t.Fatalf("the LLM got:\n%s\nwant it to contain\n%s", req.userTexts(), want)
	}
	only := mustUpload(t, conv, "q.txt", "text/plain", []byte("how many GPUs?"))
	req, _ = askAndWait(t, conv, "", 3, only)
	if !strings.Contains(req.userTexts(), "[Attached file \"q.txt\"]\nhow many GPUs?") {
		t.Fatalf("an attachment-only message did not reach the LLM:\n%s", req.userTexts())
	}
}

// Files that cannot be read are named with the reason.
func TestAgentNamesUnreadableFiles(t *testing.T) {
	llm.use(answer("noted"))
	conv := newTantoConversation(t)
	png := mustUpload(t, conv, "img.png", "image/png", []byte{0x89, 'P', 'N', 'G', 0, 0, 1})
	gone := &l8ai.ChatAttachment{AttachmentId: "gone", StoragePath: "/no/such/gone.txt", FileName: "gone.txt", MimeType: "text/plain"}
	nopath := &l8ai.ChatAttachment{AttachmentId: "nopath", FileName: "nopath"}
	req, _ := askAndWait(t, conv, "see files", 1, png, gone, nopath)
	for _, want := range []string{
		`[Attached file "img.png" (image/png, 7 bytes) is not a text file; its content cannot be read]`,
		`[Attached file "gone.txt" could not be read: `,
		`[Attached file "nopath" could not be read: it has no storage path]`,
	} {
		if !strings.Contains(req.userTexts(), want) {
			t.Fatalf("missing %q in\n%s", want, req.userTexts())
		}
	}
}

// Large files are cut to the per-file limit, the total is capped, and the
// newest message's files are read first; Tanto's own files are not inlined.
func TestAgentCutsLargeFilesNewestFirst(t *testing.T) {
	big := []byte(strings.Repeat("é", 64*1024)) // 128KB: twice the per-file limit
	conv := newTantoConversation(t)
	llm.use(func(req stubRequest) stubAnswer {
		if req.hasToolResult() {
			return stubAnswer{Text: "tanto wrote a file"}
		}
		if strings.Contains(req.lastUserText(), "first") {
			return stubAnswer{ToolCalls: []stubToolCall{toolCall("create_file",
				map[string]interface{}{"file_name": "t.txt", "content": "TANTO-FILE-CONTENT"})}}
		}
		return stubAnswer{Text: "read"}
	})
	old := mustUpload(t, conv, "old.txt", "text/plain", big)
	askAndWait(t, conv, "first", 1, old)
	files := make([]*l8ai.ChatAttachment, 0)
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		files = append(files, mustUpload(t, conv, n, "text/plain", big))
	}
	req, _ := askAndWait(t, conv, "second", 3, files...)
	texts := req.userTexts()
	if n := strings.Count(texts, "(only the first 65536 of 131072 bytes)"); n != 4 {
		t.Fatalf("%d files were cut, want the 4 that fit the total", n)
	}
	for _, skipped := range []string{"5", "old.txt"} {
		if !strings.Contains(texts, `[Attached file "`+skipped+`": not read, the attached files of this conversation are too large]`) {
			t.Fatalf("%s was not skipped for the total cap", skipped)
		}
	}
	if strings.Contains(req.assistantTexts(), "TANTO-FILE-CONTENT") || strings.Contains(texts, "TANTO-FILE-CONTENT") {
		t.Fatal("Tanto's own file was inlined")
	}
}

// Free text the LLM must not see in clear (an amount, an email) reaches it
// masked; a file it writes with those tokens is stored with the values the
// user reads.
func TestAgentMasksFreeTextAndRestoresFiles(t *testing.T) {
	var seen string
	llm.use(func(req stubRequest) stubAnswer {
		if req.hasToolResult() {
			return stubAnswer{Text: "Report attached."}
		}
		seen = req.lastUserText()
		return stubAnswer{ToolCalls: []stubToolCall{toolCall("create_file",
			map[string]interface{}{"file_name": "r.md", "content": "Report: " + seen})}}
	})
	conv := newTantoConversation(t)
	_, reply := askAndWait(t, conv, "pay $1,200.00 to a@x.com", 1)
	if strings.Contains(seen, "a@x.com") || strings.Contains(seen, "1,200.00") {
		t.Fatalf("the LLM saw the values in clear: %q", seen)
	}
	if len(reply.Attachments) != 1 {
		t.Fatalf("no file in Tanto's reply: %+v", reply)
	}
	data, err := env.download(sessionOp, reply.Attachments[0].StoragePath)
	if err != nil {
		t.Fatalf("download the file: %v", err)
	}
	if !strings.Contains(string(data), "pay $1,200.00 to a@x.com") {
		t.Fatalf("the file does not carry the values the user reads: %q", string(data))
	}
}

// The main LLM origin answers, so it is used; when it fails, the agent
// re-probes it, falls back to the fallback origin with the same path; when
// the fallback fails in turn, it re-probes the main one and uses it again.
func TestAgentLlmEndpointFallback(t *testing.T) {
	llm.use(answer("from the main"))
	conv := newTantoConversation(t)
	req, _ := askAndWait(t, conv, "one", 1)
	if req.Origin != stubMain {
		t.Fatalf("answered by %s, expected the main origin", req.Origin)
	}
	llm.setDown(stubMain, true)
	llm.use(answer("from the fallback"))
	req, reply := askAndWait(t, conv, "two", 3)
	if req.Origin != stubFallback || !strings.Contains(reply.Body, "from the fallback") {
		t.Fatalf("with the main origin down: answered by %s: %q", req.Origin, reply.Body)
	}
	llm.setDown(stubMain, false)
	llm.setDown(stubFallback, true)
	defer llm.setDown(stubFallback, false)
	llm.use(answer("from the main again"))
	req, reply = askAndWait(t, conv, "three", 5)
	if req.Origin != stubMain || !strings.Contains(reply.Body, "from the main again") {
		t.Fatalf("with the fallback down: answered by %s: %q", req.Origin, reply.Body)
	}
}

// The @web gate passes a question only when the classifier clearly answers
// NO (not about the application's data) and fails closed on anything else,
// including an empty answer; an empty @web question gets the usage hint.
func TestAgentWebGate(t *testing.T) {
	var classifier string
	llm.use(func(req stubRequest) stubAnswer {
		if strings.Contains(req.lastUserText(), "Reply YES or NO") {
			return stubAnswer{Text: classifier}
		}
		return stubAnswer{Text: "WEB ANSWER"}
	})
	conv := newTantoConversation(t)
	cases := []struct {
		answer  string
		allowed bool
	}{
		{"NO", true}, {"No.", true}, {"no", true}, {"**NO**", true}, {"  NO\n", true},
		{"YES", false}, {"**YES**", false}, {"", false}, {"   ", false}, {"Not sure", false},
		{"NOPE", false}, {"Maybe, no.", false},
	}
	seq := int64(1)
	for _, c := range cases {
		classifier = c.answer
		_, reply := askAndWait(t, conv, "@web what is the capital of France?", seq)
		seq = reply.Seq + 1
		if got := strings.Contains(reply.Body, "WEB ANSWER"); got != c.allowed {
			t.Fatalf("classifier answer %q: the question was allowed=%v (reply %q), want %v", c.answer, got, reply.Body, c.allowed)
		}
		if !c.allowed && !strings.Contains(reply.Body, "cannot be asked with @web") {
			t.Fatalf("classifier answer %q: no rejection: %q", c.answer, reply.Body)
		}
	}
	_, reply := askAndWait(t, conv, "@web", seq)
	if !strings.Contains(reply.Body, "Please add your question after @web") {
		t.Fatalf("empty @web question: %q", reply.Body)
	}
}

// toolResults returns the content of the tool results of a request.
func toolResults(req stubRequest) []string {
	result := make([]string, 0)
	for _, m := range req.Messages {
		if m.Role == "tool" && m.Content != nil {
			result = append(result, *m.Content)
		}
	}
	return result
}

// maskToken returns the first mask token after key in a tool result.
func maskToken(result, key string) string {
	i := strings.Index(result, `"`+key+`":"[MASK_`)
	if i < 0 {
		return ""
	}
	start := i + len(key) + 4
	end := strings.Index(result[start:], "]")
	if end < 0 {
		return ""
	}
	return result[start : start+end+1]
}

// Only (l8ai.AIMask) fields of a tool result reach the LLM masked (a field
// that only looks sensitive does not); the LLM's answer quoting a token
// reaches the user with the value; a token in a tool's input is restored to
// the value, so a query by a masked value finds the record.
func TestAgentMasksAnnotatedFields(t *testing.T) {
	var token string
	llm.use(func(req stubRequest) stubAnswer {
		results := toolResults(req)
		switch len(results) {
		case 0:
			return stubAnswer{ToolCalls: []stubToolCall{toolCall("l8query",
				map[string]interface{}{"query": "select * from TestEmployee where employeeId=e1"})}}
		case 1:
			token = maskToken(results[0], "nationalId")
			return stubAnswer{ToolCalls: []stubToolCall{toolCall("l8query",
				map[string]interface{}{"query": "select * from TestEmployee where nationalId=" + token})}}
		default:
			return stubAnswer{Text: "The national ID is " + token + "."}
		}
	})
	conv := newTantoConversation(t)
	req, reply := askAndWait(t, conv, "show e1", 1)
	results := toolResults(req)
	if len(results) != 2 {
		t.Fatalf("%d tool results, expected 2", len(results))
	}
	first := results[0]
	for _, secret := range []string{"NID-111-22", "120000", "jo@corp.example", "+1-555-0100", `"age":41`, `"age":"41"`} {
		if strings.Contains(first, secret) {
			t.Fatalf("a masked value reached the LLM: %s in %s", secret, first)
		}
	}
	for _, clear := range []string{"core-1", "078-05-1120", `"amount":"5000"`, `Smith, \"Jo\"`} {
		if !strings.Contains(first, clear) {
			t.Fatalf("an unannotated value was changed: %s missing in %s", clear, first)
		}
	}
	if token == "" || !strings.Contains(results[1], `"employeeId":"e1"`) {
		t.Fatalf("the query by the masked national ID (%s) did not find e1: %s", token, results[1])
	}
	if !strings.Contains(reply.Body, "NID-111-22") || strings.Contains(reply.Body, "[MASK_") {
		t.Fatalf("the user did not get the value: %q", reply.Body)
	}
}

// An aggregate of a masked field reaches the LLM masked, in the result names
// the query language gives aggregates (sum(age) → "sumAge"); aggregates of
// fields that are not masked stay as they are.
func TestAgentMasksAggregatesOfMaskedFields(t *testing.T) {
	llm.use(func(req stubRequest) stubAnswer {
		if req.hasToolResult() {
			return stubAnswer{Text: "done"}
		}
		return stubAnswer{ToolCalls: []stubToolCall{
			toolCall("l8query", map[string]interface{}{"query": "select sum(age) from TestEmployee"}),
			toolCall("l8query", map[string]interface{}{"query": "select sum(bonus.amount) from TestEmployee"}),
			toolCall("l8query", map[string]interface{}{"query": "select count(*) from TestEmployee"}),
		}}
	})
	conv := newTantoConversation(t)
	req, _ := askAndWait(t, conv, "totals", 1)
	all := strings.Join(toolResults(req), "\n")
	if strings.Contains(all, "105") || !strings.Contains(all, `"sumAge":"[MASK_`) {
		t.Fatalf("the sum of a masked field reached the LLM in clear:\n%s", all)
	}
	if !strings.Contains(all, "5000") || !strings.Contains(all, `"count":3`) {
		t.Fatalf("an aggregate of an unmasked field was masked:\n%s", all)
	}
}

// csvRows reads the CSV file attached to Tanto's reply.
func csvRows(t *testing.T, reply *l8ai.ChatMessage) (string, [][]string) {
	t.Helper()
	if len(reply.Attachments) != 1 {
		t.Fatalf("no file in Tanto's reply: %+v", reply)
	}
	data, err := env.download(sessionOp, reply.Attachments[0].StoragePath)
	if err != nil {
		t.Fatalf("download the CSV: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, data)
	}
	return reply.Attachments[0].FileName, rows
}

func exportScript(args map[string]interface{}) func(stubRequest) stubAnswer {
	return func(req stubRequest) stubAnswer {
		if req.hasToolResult() {
			return stubAnswer{Text: "Exported."}
		}
		return stubAnswer{ToolCalls: []stubToolCall{toolCall("export_csv", args)}}
	}
}

// An export without columns takes the model's fields, one nested level
// flattened, lists and maps skipped; it carries the values the user reads,
// escapes commas and quotes, leaves missing values empty, and gets a safe,
// typed file name.
func TestAgentExportsDefaultColumns(t *testing.T) {
	llm.use(exportScript(map[string]interface{}{"query": "select * from TestEmployee sort-by employeeId",
		"file_name": "../../tmp/employees.csv"}))
	conv := newTantoConversation(t)
	_, reply := askAndWait(t, conv, "export the employees", 1)
	name, rows := csvRows(t, reply)
	if name != "employees.csv" {
		t.Fatalf("file name %q, expected employees.csv", name)
	}
	header := strings.Join(rows[0], ",")
	want := "employeeId,name,nationalId,salary.amount,salary.currencyCode,deviceInfo.hostname,deviceInfo.gpuCount," +
		"deviceInfo.ssn,bonus.amount,bonus.currencyCode,age,status"
	if header != want {
		t.Fatalf("header\n%s\nwant\n%s", header, want)
	}
	if len(rows) != 4 || rows[1][0] != "e1" || rows[1][1] != `Smith, "Jo"` || rows[1][2] != "NID-111-22" ||
		rows[1][5] != "core-1" || rows[2][0] != "e2" || rows[2][5] != "" {
		t.Fatalf("rows: %q", rows)
	}
}

// Explicit columns are exported in order, a list as JSON; the query's limit
// caps the rows and its page clause is dropped.
func TestAgentExportsColumnsAndDropsPaging(t *testing.T) {
	llm.use(exportScript(map[string]interface{}{"query": "select * from TestEmployee sort-by employeeId limit 2 page 1",
		"columns": []string{"employeeId", "name", "tags"}, "file_name": "tags.csv"}))
	conv := newTantoConversation(t)
	_, reply := askAndWait(t, conv, "export tags", 1)
	_, rows := csvRows(t, reply)
	want := [][]string{{"employeeId", "name", "tags"}, {"e1", `Smith, "Jo"`, `["a","b"]`}, {"e2", "Lee", ""}}
	if len(rows) != len(want) {
		t.Fatalf("rows %q, want %q", rows, want)
	}
	for i := range want {
		if strings.Join(rows[i], "|") != strings.Join(want[i], "|") {
			t.Fatalf("rows %q, want %q", rows, want)
		}
	}
}
