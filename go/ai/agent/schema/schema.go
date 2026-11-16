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

// Package schema produces the model descriptions given to the LLM: the
// compact Tier-1 catalog in the system prompt, and Tier-2 describe_model,
// read from the introspector one model at a time.
package schema

import (
	"bytes"
	"sort"
	"strconv"
	"strings"

	"github.com/saichler/l8ai/go/ai/agent/catalog"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8reflect"
)

// maxNestedDepth limits how deep nested struct fields are expanded.
const maxNestedDepth = 2

// Tier1 lists the models the requester may access, one line each.
func Tier1(entries []catalog.Entry) string {
	buff := bytes.Buffer{}
	buff.WriteString("Available models (model, service, area, primary key):\n")
	for _, e := range entries {
		buff.WriteString("- model: ")
		buff.WriteString(e.Model)
		buff.WriteString(", service: ")
		buff.WriteString(e.Service)
		buff.WriteString(", area: ")
		buff.WriteString(strconv.Itoa(int(e.Area)))
		if e.PrimaryKey != "" {
			buff.WriteString(", pk: ")
			buff.WriteString(e.PrimaryKey)
		}
		buff.WriteString("\n")
	}
	return buff.String()
}

// DescribeModel lists a model's fields, with nested struct fields as dotted
// paths (e.g. auditInfo.createdAt) so they can be queried and sorted on.
func DescribeModel(res ifs.IResources, model string) string {
	node, ok := res.Introspector().NodeByTypeName(model)
	if !ok {
		return "Model '" + model + "' not found."
	}
	buff := bytes.Buffer{}
	buff.WriteString("Model: ")
	buff.WriteString(node.TypeName)
	buff.WriteString("\nFields:\n")
	writeFields(&buff, node, "", 0)
	return buff.String()
}

func writeFields(buff *bytes.Buffer, node *l8reflect.L8Node, prefix string, depth int) {
	names := make([]string, 0, len(node.Attributes))
	for name := range node.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		attr := node.Attributes[name]
		path := prefix + name
		buff.WriteString("  - ")
		buff.WriteString(path)
		buff.WriteString(" (")
		if attr.IsSlice {
			buff.WriteString("list of ")
		} else if attr.IsMap {
			buff.WriteString("map of ")
		}
		buff.WriteString(attr.TypeName)
		buff.WriteString(")")
		if isMoney(attr.TypeName) {
			buff.WriteString(" — nested: .amount (int64, cents), .currencyCode (string). For sums use ")
			buff.WriteString(path)
			buff.WriteString(".amount")
		}
		buff.WriteString("\n")
		if attr.IsStruct && !attr.IsSlice && !attr.IsMap && depth < maxNestedDepth && len(attr.Attributes) > 0 {
			writeFields(buff, attr, path+".", depth+1)
		}
	}
}

func isMoney(typeName string) bool {
	return strings.HasSuffix(strings.TrimPrefix(typeName, "*"), ".Money") || strings.TrimPrefix(typeName, "*") == "Money"
}
