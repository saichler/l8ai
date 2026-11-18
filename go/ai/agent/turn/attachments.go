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
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// Files attached to the users' messages are read through the FileStore
// service, as the user, and their text is put into the message the model
// sees. These caps keep a turn's context bounded.
const (
	fileStoreService   = "FileStore"
	fileStoreArea      = byte(0)
	maxAttachmentBytes = 64 * 1024  // one file
	maxAttachmentTotal = 256 * 1024 // all the files of a turn's messages
)

// downloadFunc returns a stored file's bytes.
type downloadFunc func(storagePath, aaaId string) ([]byte, error)

// download reads a file from the FileStore service, as the user.
func (this *Agent) download(storagePath, aaaId string) ([]byte, error) {
	data, err := this.readFile(storagePath, aaaId)
	if err != nil {
		this.vnic.Resources().Logger().Warning("ChatAgent: attachment ", storagePath, " not read: ", err.Error())
	}
	return data, err
}

func (this *Agent) readFile(storagePath, aaaId string) ([]byte, error) {
	resp := this.vnic.ProximityRequest(fileStoreService, fileStoreArea, ifs.PUT,
		&l8api.L8FileDownloadRequest{StoragePath: storagePath}, this.settings.Chat.ReadTimeoutSeconds, aaaId)
	if resp == nil {
		return nil, errors.New("no response from the file store")
	}
	if resp.Error() != nil {
		return nil, resp.Error()
	}
	file, ok := resp.Element().(*l8api.L8FileDownloadResponse)
	if !ok || file == nil {
		return nil, errors.New("unexpected file store response")
	}
	return file.FileData, nil
}

// inlineAttachments puts the text of the files attached to the users'
// messages of tail into those messages, after their text, between
// [Attached file "name"] and [End of attached file "name"]. A file that is
// not text, cannot be read or does not fit is named with the reason instead.
// The newest messages are read first, so their files fit the cap.
func inlineAttachments(tail []*l8ai.ChatMessage, aaaId string, download downloadFunc) {
	budget := maxAttachmentTotal
	for i := len(tail) - 1; i >= 0; i-- {
		m := tail[i]
		if m == nil || m.SenderId == aicommon.TantoUserId || len(m.Attachments) == 0 {
			continue
		}
		parts := make([]string, 0, len(m.Attachments)+1)
		if strings.TrimSpace(m.Body) != "" {
			parts = append(parts, m.Body)
		}
		for _, a := range m.Attachments {
			if a != nil {
				parts = append(parts, attachmentText(a, aaaId, download, &budget))
			}
		}
		m.Body = strings.Join(parts, "\n\n")
	}
}

func attachmentText(a *l8ai.ChatAttachment, aaaId string, download downloadFunc, budget *int) string {
	name := a.FileName
	if name == "" {
		name = filepath.Base(a.StoragePath)
	}
	if *budget <= 0 {
		return fmt.Sprintf("[Attached file %q: not read, the attached files of this conversation are too large]", name)
	}
	if a.StoragePath == "" {
		return fmt.Sprintf("[Attached file %q could not be read: it has no storage path]", name)
	}
	data, err := download(a.StoragePath, aaaId)
	if err != nil {
		return fmt.Sprintf("[Attached file %q could not be read: %s]", name, err.Error())
	}
	if !isText(data) {
		kind := a.MimeType
		if kind == "" {
			kind = "binary"
		}
		return fmt.Sprintf("[Attached file %q (%s, %d bytes) is not a text file; its content cannot be read]",
			name, kind, len(data))
	}
	limit := maxAttachmentBytes
	if *budget < limit {
		limit = *budget
	}
	text, note := string(data), ""
	if len(text) > limit {
		text = cutUTF8(text, limit)
		note = fmt.Sprintf(" (only the first %d of %d bytes)", len(text), len(data))
	}
	*budget -= len(text)
	return fmt.Sprintf("[Attached file %q%s]\n%s\n[End of attached file %q]", name, note, text, name)
}

// isText reports whether data is UTF-8 text (no NUL bytes).
func isText(data []byte) bool {
	return utf8.Valid(data) && !bytes.Contains(data, []byte{0})
}

// cutUTF8 shortens text to at most n bytes without splitting a character.
func cutUTF8(text string, n int) string {
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}
