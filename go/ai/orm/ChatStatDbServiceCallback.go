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
	"github.com/saichler/l8ai/go/types/l8ai"
	l8c "github.com/saichler/l8common/go/common"
	"github.com/saichler/l8types/go/ifs"
)

func newChatStatDbCallback() ifs.IServiceCallback {
	return l8c.NewServiceCallback("ChatUserStatus",
		func(v interface{}) bool { _, ok := v.(*l8ai.ChatUserStatus); return ok },
		// The primary key is the user's own ID, so it is never generated.
		func(v interface{}) {},
		nil,
		func(v interface{}, action ifs.Action, vnic ifs.IVNic) error {
			return validateChatUserStatus(v.(*l8ai.ChatUserStatus), action)
		})
}

func validateChatUserStatus(status *l8ai.ChatUserStatus, action ifs.Action) error {
	if action != ifs.POST && action != ifs.PUT {
		return nil
	}
	if err := l8c.ValidateRequired(status.UserId, "UserId"); err != nil {
		return err
	}
	return l8c.ValidateEnum(status.Presence, l8ai.ChatPresence_name, "Presence")
}
