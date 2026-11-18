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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/saichler/l8ai/go/ai/agent/convctx"
	"github.com/saichler/l8ai/go/ai/agent/masking"
	"github.com/saichler/l8ai/go/ai/agent/tools"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
)

func files(contents map[string][]byte) (downloadFunc, *[]string) {
	calls := []string{}
	return func(path, aaaId string) ([]byte, error) {
		calls = append(calls, path+"@"+aaaId)
		data, ok := contents[path]
		if !ok {
			return nil, errors.New("file not found: " + path)
		}
		return data, nil
	}, &calls
}

func attach(path, name, mime string) *l8ai.ChatAttachment {
	return &l8ai.ChatAttachment{StoragePath: path, FileName: name, MimeType: mime}
}

func TestAttachedTextFileIsPartOfTheMessage(t *testing.T) {
	download, calls := files(map[string][]byte{"/store/a/test2": []byte("can you tell me how many network devices are there\n")})
	tail := []*l8ai.ChatMessage{{Seq: 1, SenderId: "operator", Body: "can you read the attached file and do the request inside of it?",
		Attachments: []*l8ai.ChatAttachment{attach("/store/a/test2", "test2", "text/plain")}}}
	inlineAttachments(tail, "aaa-1", download)

	want := "can you read the attached file and do the request inside of it?\n\n" +
		"[Attached file \"test2\"]\ncan you tell me how many network devices are there\n\n[End of attached file \"test2\"]"
	if tail[0].Body != want {
		t.Fatalf("Body =\n%s\nwant\n%s", tail[0].Body, want)
	}
	if len(*calls) != 1 || (*calls)[0] != "/store/a/test2@aaa-1" {
		t.Fatalf("the file was not read as the user: %v", *calls)
	}
}

func TestAttachmentOnlyMessageReachesTheModel(t *testing.T) {
	download, _ := files(map[string][]byte{"/s/q.txt": []byte("how many GPUs?")})
	messages := []*l8ai.ChatMessage{
		{Seq: 2, SenderId: "operator", Attachments: []*l8ai.ChatAttachment{attach("/s/q.txt", "q.txt", "text/plain")}},
		{Seq: 1, SenderId: "operator", Kind: l8ai.ChatMessageKind_CHAT_MESSAGE_KIND_SYSTEM, Body: "system notice"},
	}
	tail := convctx.Tail(messages)
	if len(tail) != 1 || tail[0].Seq != 2 {
		t.Fatalf("Tail dropped the message with only an attachment: %v", tail)
	}
	inlineAttachments(tail, "aaa", download)
	if !strings.HasPrefix(tail[0].Body, "[Attached file \"q.txt\"]\nhow many GPUs?") {
		t.Fatalf("Body = %q", tail[0].Body)
	}
}

func TestUnreadableFilesAreNamedWithTheReason(t *testing.T) {
	download, _ := files(map[string][]byte{"/s/img.png": {0x89, 'P', 'N', 'G', 0, 0, 1}})
	tail := []*l8ai.ChatMessage{{SenderId: "operator", Body: "see files", Attachments: []*l8ai.ChatAttachment{
		attach("/s/img.png", "img.png", "image/png"), attach("/s/gone.txt", "gone.txt", "text/plain"), attach("", "nopath", "")}}}
	inlineAttachments(tail, "aaa", download)
	body := tail[0].Body
	for _, want := range []string{
		`[Attached file "img.png" (image/png, 7 bytes) is not a text file; its content cannot be read]`,
		`[Attached file "gone.txt" could not be read: file not found: /s/gone.txt]`,
		`[Attached file "nopath" could not be read: it has no storage path]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
}

func TestLargeFilesAreCutAndTheTotalIsCapped(t *testing.T) {
	big := strings.Repeat("é", maxAttachmentBytes) // 2 bytes each: twice the per-file cap
	contents := map[string][]byte{}
	atts := []*l8ai.ChatAttachment{}
	for _, p := range []string{"/s/1", "/s/2", "/s/3", "/s/4", "/s/5"} {
		contents[p] = []byte(big)
		atts = append(atts, attach(p, p[3:], "text/plain"))
	}
	download, calls := files(contents)
	tail := []*l8ai.ChatMessage{{SenderId: "operator", Body: "files", Attachments: atts}}
	inlineAttachments(tail, "aaa", download)
	body := tail[0].Body
	if n := strings.Count(body, "(only the first "); n != 4 {
		t.Fatalf("%d files were cut, want the 4 that fit the total", n)
	}
	if !strings.Contains(body, `[Attached file "5": not read, the attached files of this conversation are too large]`) {
		t.Fatalf("the fifth file was not skipped for the total cap")
	}
	if len(*calls) != 4 {
		t.Fatalf("read %d files, want 4", len(*calls))
	}
	if !strings.Contains(body, "(only the first 65536 of 131072 bytes)") {
		t.Fatalf("the cut is not reported in bytes on a character boundary")
	}
}

func TestTantoMessagesAndNewestFirst(t *testing.T) {
	download, calls := files(map[string][]byte{"/s/old": []byte("old"), "/s/new": []byte("new"), "/s/t": []byte("tanto")})
	tail := []*l8ai.ChatMessage{
		{Seq: 1, SenderId: "operator", Body: "a", Attachments: []*l8ai.ChatAttachment{attach("/s/old", "old", "")}},
		{Seq: 2, SenderId: aicommon.TantoUserId, Body: "b", Attachments: []*l8ai.ChatAttachment{attach("/s/t", "t", "")}},
		{Seq: 3, SenderId: "operator", Body: "c", Attachments: []*l8ai.ChatAttachment{attach("/s/new", "new", "")}},
	}
	inlineAttachments(tail, "aaa", download)
	if tail[1].Body != "b" {
		t.Fatalf("Tanto's message was changed: %q", tail[1].Body)
	}
	if len(*calls) != 2 || !strings.HasPrefix((*calls)[0], "/s/new") {
		t.Fatalf("the newest message's file is not read first: %v", *calls)
	}
}

func TestCreateFileContentGetsTheValuesTheUserReads(t *testing.T) {
	m := masking.NewMasker(nil, []byte("s"), "c")
	tokened := m.MaskText("pay $1,200.00 to a@x.com")
	input, _ := json.Marshal(map[string]interface{}{"file_name": "r.md", "content": "Report: " + tokened})
	out := map[string]interface{}{}
	if err := json.Unmarshal([]byte(unmaskInput(m, tools.CreateFile, string(input))), &out); err != nil {
		t.Fatal(err)
	}
	if out["content"] != "Report: pay $1,200.00 to a@x.com" || out["file_name"] != "r.md" {
		t.Fatalf("create_file input = %v", out)
	}
	// Other tools keep the raw restoration.
	raw := unmaskInput(m, tools.Query, `{"query":"select * from X where email=`+strings.Split(tokened, " to ")[1]+`"}`)
	if !strings.Contains(raw, "email=a@x.com") {
		t.Fatalf("query input = %s", raw)
	}
}
