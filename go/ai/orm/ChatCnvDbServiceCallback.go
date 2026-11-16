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

func newChatCnvDbCallback() ifs.IServiceCallback {
	return l8c.NewServiceCallback("ChatConversation",
		func(v interface{}) bool { _, ok := v.(*l8ai.ChatConversation); return ok },
		func(v interface{}) { l8c.GenerateID(&v.(*l8ai.ChatConversation).ConversationId) },
		nil,
		func(v interface{}, action ifs.Action, vnic ifs.IVNic) error {
			return validateChatConversation(v.(*l8ai.ChatConversation), action)
		})
}

func validateChatConversation(conv *l8ai.ChatConversation, action ifs.Action) error {
	// notif_seq is an in-memory notification sequence and is never persisted.
	conv.NotifSeq = 0
	if action != ifs.POST && action != ifs.PUT {
		return nil
	}
	if err := l8c.ValidateRequired(conv.ConversationId, "ConversationId"); err != nil {
		return err
	}
	if err := l8c.ValidateEnum(conv.PeerKind, l8ai.ChatPeerKind_name, "PeerKind"); err != nil {
		return err
	}
	if err := l8c.ValidateRequired(conv.PeerKey, "PeerKey"); err != nil {
		return err
	}
	if err := l8c.ValidateRequired(conv.CreatedBy, "CreatedBy"); err != nil {
		return err
	}
	if len(conv.Members) == 0 {
		return errors.New("ChatConversation must have at least one member")
	}
	for _, member := range conv.Members {
		if member == nil || member.UserId == "" {
			return errors.New("ChatConversation member UserId is required")
		}
	}
	if conv.PeerKind == l8ai.ChatPeerKind_CHAT_PEER_KIND_GROUP {
		if err := l8c.ValidateRequired(conv.GroupId, "GroupId"); err != nil {
			return err
		}
	}
	return nil
}
