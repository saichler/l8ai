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

package executor

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Files Tanto makes are stored through the FileStore service as the user,
// like a file the user attaches, and attached to Tanto's answer.
const (
	fileStoreService = "FileStore"
	fileStoreArea    = byte(0)
	maxFileBytes     = 5 * 1024 * 1024 // the file store's limit
	maxExportRows    = 10000
	exportPageSize   = 500
)

var (
	limitClauseRe = regexp.MustCompile(`(?i)\s+limit\s+(\d+)`)
	pageClauseRe  = regexp.MustCompile(`(?i)\s+page\s+\d+`)
	fileNameRe    = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)
)

// RegisterFileTypes registers the FileStore messages the agent sends and
// decodes.
func RegisterFileTypes(registry ifs.IRegistry) {
	registry.Register(&l8api.L8FileUploadRequest{})
	registry.Register(&l8api.L8FileUploadResponse{})
	registry.Register(&l8api.L8FileDownloadRequest{})
	registry.Register(&l8api.L8FileDownloadResponse{})
}

// exportCSV runs the query as the user over every page of its results and
// attaches them as a CSV file. The rows never go through the model: it gets
// the file's name, row count and columns.
func (this *Executor) exportCSV(args map[string]interface{}, call *Call) (string, string, error) {
	query, _ := args["query"].(string)
	m := fromRe.FindStringSubmatch(query)
	if m == nil {
		return "", "", errors.New("export_csv needs a query with a 'from <Model>' clause")
	}
	if aggRe.MatchString(query) {
		return "", "", errors.New("export_csv exports records; use 'select * from <Model> ...' (no aggregates)")
	}
	entry, err := this.resolve(m[1], call)
	if err != nil {
		return "", "", err
	}
	md := this.descriptor(entry.Model)
	if md == nil {
		return "", "", fmt.Errorf("model %s cannot be exported", entry.Model)
	}
	columns := stringList(args["columns"])
	if len(columns) == 0 {
		columns = defaultColumns(md)
	}

	wanted := maxExportRows
	if lm := limitClauseRe.FindStringSubmatch(query); lm != nil {
		if n, err := strconv.Atoi(lm[1]); err == nil && n > 0 && n < wanted {
			wanted = n
		}
	}
	base := strings.TrimSpace(pageClauseRe.ReplaceAllString(limitClauseRe.ReplaceAllString(query, ""), ""))

	records := make([]map[string]interface{}, 0)
	for page := 0; len(records) < wanted; page++ {
		q := fmt.Sprintf("%s limit %d page %d", base, exportPageSize, page)
		resp := this.vnic.LeaderRequest(entry.Service, byte(entry.Area), ifs.GET, q, this.timeout, call.AaaId)
		if resp == nil {
			return "", "", errors.New("no response from the service")
		}
		if resp.Error() != nil {
			return "", "", resp.Error()
		}
		items := resp.Elements()
		for _, item := range items {
			msg, ok := item.(proto.Message)
			if !ok || msg == nil {
				continue
			}
			if len(records) == wanted {
				break
			}
			rec, err := recordJSON(msg)
			if err != nil {
				return "", "", err
			}
			records = append(records, rec)
		}
		if len(items) < exportPageSize {
			break
		}
	}

	data, err := writeCSV(columns, records)
	if err != nil {
		return "", "", err
	}
	name := fileName(stringArg(args, "file_name"), entry.Model, ".csv")
	file, err := this.store(name, "text/csv", data, call)
	if err != nil {
		return "", "", err
	}
	out, _ := json.Marshal(map[string]interface{}{"file": file.FileName, "rows": len(records), "columns": columns,
		"truncated_at_max_rows": len(records) == maxExportRows, "attached": true})
	return string(out), "", nil
}

// createFile attaches text Tanto wrote (a report, a summary) as a file.
func (this *Executor) createFile(args map[string]interface{}, call *Call) (string, string, error) {
	content := stringArg(args, "content")
	if content == "" {
		return "", "", errors.New("create_file needs 'content'")
	}
	name := fileName(stringArg(args, "file_name"), "file", ".txt")
	file, err := this.store(name, mimeOf(name), []byte(content), call)
	if err != nil {
		return "", "", err
	}
	out, _ := json.Marshal(map[string]interface{}{"file": file.FileName, "bytes": file.FileSize, "attached": true})
	return string(out), "", nil
}

// store saves a file through the FileStore service as the user and adds it to
// the turn's attachments.
func (this *Executor) store(name, mimeType string, data []byte, call *Call) (*l8ai.ChatAttachment, error) {
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("the file is %d bytes, over the %d bytes limit; select fewer records or columns",
			len(data), maxFileBytes)
	}
	req := &l8api.L8FileUploadRequest{FileName: name, MimeType: mimeType, FileData: data,
		DocumentId: call.ConversationId, Version: 1}
	resp := this.vnic.ProximityRequest(fileStoreService, fileStoreArea, ifs.POST, req, this.timeout, call.AaaId)
	if resp == nil {
		return nil, errors.New("no response from the file store")
	}
	if resp.Error() != nil {
		return nil, resp.Error()
	}
	up, ok := resp.Element().(*l8api.L8FileUploadResponse)
	if !ok || up == nil || up.StoragePath == "" {
		return nil, errors.New("unexpected file store response")
	}
	call.files++
	a := &l8ai.ChatAttachment{
		AttachmentId: aicommon.DeterministicID("file|" + call.TurnId + "|" + strconv.Itoa(call.files)),
		StoragePath:  up.StoragePath, FileName: name, FileSize: int64(len(data)), MimeType: mimeType,
		Checksum: up.Checksum}
	call.Files = append(call.Files, a)
	return a, nil
}

func (this *Executor) descriptor(model string) protoreflect.MessageDescriptor {
	info, err := this.vnic.Resources().Registry().Info(model)
	if err != nil || info == nil {
		return nil
	}
	instance, err := info.NewInstance()
	if err != nil {
		return nil
	}
	msg, ok := instance.(proto.Message)
	if !ok {
		return nil
	}
	return msg.ProtoReflect().Descriptor()
}

// defaultColumns are a record's own scalar fields and the scalar fields of
// its (non-repeated) sub-messages, one level deep, in declaration order.
// Lists and maps are left out.
func defaultColumns(md protoreflect.MessageDescriptor) []string {
	columns := make([]string, 0)
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.IsList() || fd.IsMap() {
			continue
		}
		if fd.Message() == nil {
			columns = append(columns, fd.JSONName())
			continue
		}
		sub := fd.Message().Fields()
		for j := 0; j < sub.Len(); j++ {
			sf := sub.Get(j)
			if !sf.IsList() && !sf.IsMap() && sf.Message() == nil {
				columns = append(columns, fd.JSONName()+"."+sf.JSONName())
			}
		}
	}
	return columns
}

func recordJSON(msg proto.Message) (map[string]interface{}, error) {
	raw, err := protojson.Marshal(msg)
	if err != nil {
		return nil, err
	}
	rec := map[string]interface{}{}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// writeCSV writes a header row and one row per record; a column is a field
// path ("deviceInfo.vendor"), a missing value is empty, a list or object is
// written as JSON.
func writeCSV(columns []string, records []map[string]interface{}) ([]byte, error) {
	buff := &bytes.Buffer{}
	w := csv.NewWriter(buff)
	if err := w.Write(columns); err != nil {
		return nil, err
	}
	row := make([]string, len(columns))
	for _, rec := range records {
		for i, col := range columns {
			row[i] = cell(valueAt(rec, col))
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buff.Bytes(), w.Error()
}

func valueAt(rec map[string]interface{}, path string) interface{} {
	var v interface{} = rec
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		if v, ok = m[part]; !ok {
			return nil
		}
	}
	return v
}

func cell(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// fileName makes a safe file name with an extension.
func fileName(requested, fallback, ext string) string {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(requested, "\\", "/")))
	name = strings.Trim(fileNameRe.ReplaceAllString(name, "_"), "._ ")
	if name == "" {
		name = fallback
	}
	if utf8.RuneCountInString(name) > 100 {
		name = string([]rune(name)[:100])
	}
	if filepath.Ext(name) == "" {
		name += ext
	}
	return name
}

func mimeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".csv":
		return "text/csv"
	case ".md":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	}
	return "text/plain"
}

func stringArg(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return s
}

func stringList(v interface{}) []string {
	list, _ := v.([]interface{})
	result := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			result = append(result, strings.TrimSpace(s))
		}
	}
	return result
}
