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

// Package tools defines the tools offered to the model. The data tools name
// a model, whose parameter carries an enum of the models the requester may
// access; the executor resolves the service from the catalog.
package tools

import (
	"github.com/saichler/l8ai/go/ai/agent/llm"
)

// Tool names.
const (
	Query         = "l8query"
	CreateRecord  = "create_record"
	UpdateRecord  = "update_record"
	DeleteRecord  = "delete_record"
	ListModules   = "list_modules"
	DescribeModel = "describe_model"
)

const queryDescription = `Run an L8Query and return the matching records or aggregates.

Syntax: select <columns|aggregates> from <Model> [where <conditions>] [group-by <fields>] [sort-by <field> [descending]] [limit <n>]

- The model in the from clause decides the service; use the exact model and field names (describe_model).
- For totals, counts and averages use aggregates: count(*), sum(field), avg(field), min(field), max(field).
- A 'select *' needs an id filter or 'limit <n>' (n <= 100).
- Money fields are objects with 'amount' (cents) and 'currencyCode'; use sum(totalAmount.amount) for sums.

Examples:
  select * from Employee where departmentId=D001 limit 10
  select count(*) from Employee
  select count(*),sum(totalAmount.amount) from SalesOrder group-by status`

// Data returns the data tools for the models in the enum.
func Data(models []string) []llm.Tool {
	modelParam := func(description string) map[string]interface{} {
		p := map[string]interface{}{"type": "string", "description": description}
		if len(models) > 0 {
			p["enum"] = models
		}
		return p
	}
	object := func(properties map[string]interface{}, required ...string) map[string]interface{} {
		schema := map[string]interface{}{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	data := map[string]interface{}{"type": "object",
		"description": "The record's fields as key-value pairs, using the model's field names"}
	return []llm.Tool{
		{Name: Query, Description: queryDescription, InputSchema: object(map[string]interface{}{
			"query": map[string]interface{}{"type": "string", "description": "The L8Query"},
		}, "query")},
		{Name: CreateRecord, Description: "Create a record of a model.", InputSchema: object(map[string]interface{}{
			"model": modelParam("The model of the record"), "data": data,
		}, "model", "data")},
		{Name: UpdateRecord, Description: "Update a record of a model. The data must include the primary key.",
			InputSchema: object(map[string]interface{}{
				"model": modelParam("The model of the record"), "data": data,
			}, "model", "data")},
		{Name: DeleteRecord, Description: "Delete records of a model. Only after the user explicitly confirmed it.",
			InputSchema: object(map[string]interface{}{
				"model": modelParam("The model of the records"),
				"query": map[string]interface{}{"type": "string", "description": "L8Query selecting the records to delete"},
			}, "model", "query")},
		{Name: ListModules, Description: "List the models you can use, with their services and primary keys.",
			InputSchema: object(map[string]interface{}{})},
		{Name: DescribeModel, Description: "List the fields of a model. Use it before writing a query.",
			InputSchema: object(map[string]interface{}{"model": modelParam("The model")}, "model")},
	}
}
