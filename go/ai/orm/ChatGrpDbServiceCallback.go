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

package orm

import (
	"errors"

	"github.com/saichler/l8ai/go/types/l8ai"
	l8c "github.com/saichler/l8common/go/common"
	"github.com/saichler/l8types/go/ifs"
)

func newChatGrpDbCallback() ifs.IServiceCallback {
	return l8c.NewServiceCallback("ChatGroup",
		func(v interface{}) bool { _, ok := v.(*l8ai.ChatGroup); return ok },
		func(v interface{}) { l8c.GenerateID(&v.(*l8ai.ChatGroup).GroupId) },
		nil,
		func(v interface{}, action ifs.Action, vnic ifs.IVNic) error {
			return validateChatGroup(v.(*l8ai.ChatGroup), action)
		})
}

func validateChatGroup(group *l8ai.ChatGroup, action ifs.Action) error {
	if action != ifs.POST && action != ifs.PUT {
		return nil
	}
	if err := l8c.ValidateRequired(group.GroupId, "GroupId"); err != nil {
		return err
	}
	if err := l8c.ValidateRequired(group.Name, "Name"); err != nil {
		return err
	}
	if err := l8c.ValidateRequired(group.CreatedBy, "CreatedBy"); err != nil {
		return err
	}
	if len(group.MemberIds) == 0 {
		return errors.New("ChatGroup must have at least one member")
	}
	return nil
}
