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

package common

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
	"google.golang.org/protobuf/proto"
)

// CriteriaValue returns the right-hand value of the first "<field> <op> <value>"
// comparator in the query's WHERE clause. The parser lower-cases the left
// operand and strips quotes from the right one.
func CriteriaValue(q ifs.IQuery, field, op string) (string, bool) {
	return expressionValue(q.Criteria(), strings.ToLower(field), op)
}

func expressionValue(expr ifs.IExpression, field, op string) (string, bool) {
	for !isNil(expr) {
		for cond := expr.Condition(); !isNil(cond); cond = cond.Next() {
			comp := cond.Comparator()
			if isNil(comp) {
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

// isNil is true for a nil interface and for an interface holding a nil
// pointer: the query interpreter returns typed nils (e.g. a nil *Condition).
func isNil(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}

// CriteriaList returns the values of a "<field> in [a,b,c]" comparator.
func CriteriaList(q ifs.IQuery, field string) []string {
	v, ok := CriteriaValue(q, field, "in")
	if !ok {
		return nil
	}
	v = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "["), "]")
	result := make([]string, 0)
	for _, item := range strings.Split(v, ",") {
		item = strings.Trim(strings.TrimSpace(item), "'\"")
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

// CriteriaInt64 is CriteriaValue parsed as an int64.
func CriteriaInt64(q ifs.IQuery, field, op string) (int64, bool) {
	v, ok := CriteriaValue(q, field, op)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// PQueryOf returns the structured L8Query of a query-mode request, or nil.
func PQueryOf(pb ifs.IElements) *l8api.L8Query {
	if pq, ok := pb.(interface{ PQuery() *l8api.L8Query }); ok {
		return pq.PQuery()
	}
	return nil
}

// PlainQuery returns a copy of the query without its live registration, so a
// read can be passed on (e.g. to chat-orm) without registering it there. The
// text is kept: the receiver parses it (an empty text is not read as a query),
// and clients send the registration as fields, not in the text.
func PlainQuery(q *l8api.L8Query) *l8api.L8Query {
	c := proto.Clone(q).(*l8api.L8Query)
	c.Register = l8api.L8Register_L8_REGISTER_INVALID
	c.RegisterId = ""
	c.AaaId = ""
	return c
}
