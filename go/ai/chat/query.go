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

package chat

import (
	"strconv"
	"strings"

	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"google.golang.org/protobuf/proto"
)

// criteriaValue returns the right-hand value of the first "<field> <op> <value>"
// comparator in the query's WHERE clause. The parser lower-cases the left
// operand and strips quotes from the right one.
func criteriaValue(q ifs.IQuery, field, op string) (string, bool) {
	return expressionValue(q.Criteria(), strings.ToLower(field), op)
}

func expressionValue(expr ifs.IExpression, field, op string) (string, bool) {
	for expr != nil {
		for cond := expr.Condition(); cond != nil; cond = cond.Next() {
			comp := cond.Comparator()
			if comp == nil {
				continue
			}
			if strings.ToLower(comp.Left()) == field && strings.TrimSpace(comp.Operator()) == op {
				return comp.Right(), true
			}
		}
		if v, ok := expressionValue(expr.Child(), field, op); ok {
			return v, true
		}
		expr = expr.Next()
	}
	return "", false
}

// criteriaInt64 is criteriaValue parsed as an int64.
func criteriaInt64(q ifs.IQuery, field, op string) (int64, bool) {
	v, ok := criteriaValue(q, field, op)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// pqueryOf returns the structured L8Query of a query-mode request, or nil.
func pqueryOf(pb ifs.IElements) *l8api.L8Query {
	if pq, ok := pb.(interface{ PQuery() *l8api.L8Query }); ok {
		return pq.PQuery()
	}
	return nil
}

// plainQuery returns a copy of the structured query without its live
// registration, so a read can be passed on (e.g. to chat-orm) without
// registering it there. The text is dropped as well: it may carry the
// "register" keyword, and the receiver uses the structured form when the
// text is empty.
func plainQuery(q *l8api.L8Query) *l8api.L8Query {
	c := proto.Clone(q).(*l8api.L8Query)
	c.Text = ""
	c.Register = l8api.L8Register_L8_REGISTER_INVALID
	c.RegisterId = ""
	c.AaaId = ""
	return c
}
