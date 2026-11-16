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

// ChatMsgDb is append-only: PUT is rejected and PATCH may change token_count only.
func newChatMsgDbCallback() ifs.IServiceCallback {
	return l8c.NewServiceCallback("ChatMessage",
		func(v interface{}) bool { _, ok := v.(*l8ai.ChatMessage); return ok },
		func(v interface{}) { l8c.GenerateID(&v.(*l8ai.ChatMessage).MessageId) },
		nil,
		func(v interface{}, action ifs.Action, vnic ifs.IVNic) error {
			return validateChatMessage(v.(*l8ai.ChatMessage), action)
		})
}

func validateChatMessage(msg *l8ai.ChatMessage, action ifs.Action) error {
	// notif_seq is an in-memory notification sequence and is never persisted.
	msg.NotifSeq = 0
	switch action {
	case ifs.PUT:
		return errors.New("ChatMessage is append-only: PUT is not allowed")
	case ifs.PATCH:
		if msg.ConversationId != "" || msg.Seq != 0 || msg.SenderId != "" || msg.Kind != 0 || msg.Body != "" ||
			len(msg.Attachments) > 0 || msg.SentAt != 0 || msg.LlmProfileId != "" || msg.AgentTurnId != "" {
			return errors.New("ChatMessage PATCH may change TokenCount only")
		}
		return l8c.ValidateRequired(msg.MessageId, "MessageId")
	case ifs.POST:
		if err := l8c.ValidateRequired(msg.ConversationId, "ConversationId"); err != nil {
			return err
		}
		if err := l8c.ValidateRequiredInt64(msg.Seq, "Seq"); err != nil {
			return err
		}
		if err := l8c.ValidateRequired(msg.SenderId, "SenderId"); err != nil {
			return err
		}
		return l8c.ValidateEnum(msg.Kind, l8ai.ChatMessageKind_name, "Kind")
	}
	return nil
}
