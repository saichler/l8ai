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

// Package executor runs the model's data tool calls against the services, as
// the requesting user: every request carries the requester's aaaId, so the
// security provider's action checks and ScopeView apply (plan §7.4).
package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/saichler/l8ai/go/ai/agent/catalog"
	"github.com/saichler/l8ai/go/ai/agent/schema"
	"github.com/saichler/l8ai/go/ai/agent/tools"
	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxListLimit is the largest limit allowed on a 'select *' query.
const maxListLimit = 100

// Call is the per-turn context of tool calls.
type Call struct {
	AaaId          string
	TurnId         string
	ConversationId string
	Allowed        map[string]bool // models the requester may access; nil allows all
	// Files are the files made by export_csv and create_file, attached to
	// the turn's answer.
	Files   []*l8ai.ChatAttachment
	creates int
	files   int
}

// Executor runs data tools.
type Executor struct {
	vnic    ifs.IVNic
	catalog *catalog.Catalog
	timeout int
}

func New(vnic ifs.IVNic, c *catalog.Catalog, timeoutSeconds int) *Executor {
	return &Executor{vnic: vnic, catalog: c, timeout: timeoutSeconds}
}

// Execute runs one tool call and returns the result and the model it is about
// (used to classify the result's fields for masking).
func (this *Executor) Execute(name, input string, call *Call) (string, string, error) {
	args := map[string]interface{}{}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", "", errors.New("invalid tool input: " + err.Error())
	}
	model, _ := args["model"].(string)
	switch name {
	case tools.ListModules:
		return schema.Tier1(this.catalog.Entries(call.Allowed)), "", nil
	case tools.DescribeModel:
		entry, err := this.resolve(model, call)
		if err != nil {
			return "", "", err
		}
		return schema.DescribeModel(this.vnic.Resources(), entry.Model), entry.Model, nil
	case tools.Query:
		query, _ := args["query"].(string)
		return this.query(query, call)
	case tools.CreateRecord, tools.UpdateRecord:
		data, _ := args["data"].(map[string]interface{})
		return this.mutate(name, model, data, call)
	case tools.DeleteRecord:
		query, _ := args["query"].(string)
		return this.delete(model, query, call)
	case tools.ExportCSV:
		return this.exportCSV(args, call)
	case tools.CreateFile:
		return this.createFile(args, call)
	}
	return "", "", errors.New("unknown tool: " + name)
}

func (this *Executor) resolve(model string, call *Call) (*catalog.Entry, error) {
	entry, ok := this.catalog.Resolve(model)
	if !ok {
		return nil, fmt.Errorf("unknown model %q; use list_modules for the models you can use", model)
	}
	if call.Allowed != nil && !call.Allowed[entry.Model] {
		return nil, fmt.Errorf("you are not permitted to access %s", entry.Model)
	}
	return entry, nil
}

var (
	fromRe  = regexp.MustCompile(`(?i)\bfrom\s+([A-Za-z0-9_]+)`)
	limitRe = regexp.MustCompile(`(?i)\blimit\s+(\d+)`)
	aggRe   = regexp.MustCompile(`(?i)\b(sum|count|avg|min|max)\s*\(`)
)

// checkQueryPolicy rejects an unbounded 'select *' (no id filter, no small
// limit): totals and summaries must use aggregates.
func checkQueryPolicy(query string) error {
	if aggRe.MatchString(query) {
		return nil
	}
	lower := strings.ToLower(query)
	if !strings.Contains(lower, "select *") || strings.Contains(lower, "id=") {
		return nil
	}
	if m := limitRe.FindStringSubmatch(query); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n <= maxListLimit {
			return nil
		}
	}
	return fmt.Errorf("rejected: 'select *' needs an id filter or 'limit <n>' (n <= %d); use aggregates "+
		"(sum, count, avg, min, max) for totals and summaries", maxListLimit)
}

func (this *Executor) query(query string, call *Call) (string, string, error) {
	m := fromRe.FindStringSubmatch(query)
	if m == nil {
		return "", "", errors.New("the query needs a 'from <Model>' clause")
	}
	if err := checkQueryPolicy(query); err != nil {
		return "", "", err
	}
	entry, err := this.resolve(m[1], call)
	if err != nil {
		return "", "", err
	}
	resp := this.vnic.LeaderRequest(entry.Service, byte(entry.Area), ifs.GET, query, this.timeout, call.AaaId)
	return this.result(resp, entry.Model)
}

func (this *Executor) mutate(name, model string, data map[string]interface{}, call *Call) (string, string, error) {
	if data == nil {
		return "", "", errors.New(name + " needs 'model' and 'data'")
	}
	entry, err := this.resolve(model, call)
	if err != nil {
		return "", "", err
	}
	action := ifs.PUT
	if name == tools.CreateRecord {
		action = ifs.POST
		// A retried turn creates the same records again with the same keys, so
		// it cannot duplicate them (assessment §2.2).
		if key := jsonName(entry.PrimaryKey); key != "" {
			if v, _ := data[key].(string); v == "" {
				call.creates++
				data[key] = aicommon.DeterministicID("tool|" + call.TurnId + "|" + strconv.Itoa(call.creates))
			}
		}
	}
	record, err := this.newRecord(entry.Model, data)
	if err != nil {
		return "", "", err
	}
	resp := this.vnic.LeaderRequest(entry.Service, byte(entry.Area), action, record, this.timeout, call.AaaId)
	return this.result(resp, entry.Model)
}

func (this *Executor) delete(model, query string, call *Call) (string, string, error) {
	if query == "" {
		return "", "", errors.New("delete_record needs 'model' and 'query'")
	}
	entry, err := this.resolve(model, call)
	if err != nil {
		return "", "", err
	}
	resp := this.vnic.LeaderRequest(entry.Service, byte(entry.Area), ifs.DELETE, &l8api.L8Query{Text: query},
		this.timeout, call.AaaId)
	return this.result(resp, entry.Model)
}

func (this *Executor) newRecord(model string, data map[string]interface{}) (proto.Message, error) {
	info, err := this.vnic.Resources().Registry().Info(model)
	if err != nil {
		return nil, fmt.Errorf("unknown model type %s", model)
	}
	instance, err := info.NewInstance()
	if err != nil {
		return nil, err
	}
	record, ok := instance.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("type %s is not a protobuf message", model)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, record); err != nil {
		return nil, fmt.Errorf("invalid data for %s: %s", model, err.Error())
	}
	return record, nil
}

// result renders a response as JSON: the record list, or the aggregates.
func (this *Executor) result(resp ifs.IElements, model string) (string, string, error) {
	if resp == nil {
		return "", model, errors.New("no response from the service")
	}
	if resp.Error() != nil {
		return "", model, resp.Error()
	}
	opts := protojson.MarshalOptions{UseEnumNumbers: true}
	if resp.Element() == nil {
		if md := resp.Metadata(); md != nil && md.KeyCount != nil && len(md.KeyCount.Counts) > 0 {
			parts := make([]string, 0, len(md.KeyCount.Counts))
			for k, v := range md.KeyCount.Counts {
				parts = append(parts, fmt.Sprintf("%q:%.2f", k, v))
			}
			return "{" + strings.Join(parts, ",") + "}", model, nil
		}
		return "{}", model, nil
	}
	if list, err := resp.AsList(this.vnic.Resources().Registry()); err == nil && list != nil {
		if msg, ok := list.(proto.Message); ok {
			out, err := opts.Marshal(msg)
			return string(out), model, err
		}
	}
	if msg, ok := resp.Element().(proto.Message); ok {
		out, err := opts.Marshal(msg)
		return string(out), model, err
	}
	return "{}", model, nil
}

// jsonName turns a Go field name into its JSON name (DeviceId → deviceId).
func jsonName(field string) string {
	if field == "" {
		return ""
	}
	runes := []rune(field)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}
