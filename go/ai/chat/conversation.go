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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/saichler/l8ai/go/types/l8ai"
	"google.golang.org/protobuf/proto"
)

// previewLength is the number of characters of the last message kept on the header.
const previewLength = 100

// conversation is the owner's state of one conversation. Every field is
// guarded by mtx; the owner is the conversation's single writer.
type conversation struct {
	mtx    sync.Mutex
	header *l8ai.ChatConversation // authoritative header, including NotifSeq

	ringIds      []string // message IDs of the ring in the message cache, ascending seq
	ringLoaded   bool
	ringComplete bool // the ring holds every message of the conversation

	notifs   []*notification // the last NotifRetain pushes, ascending notifSeq
	lastUsed time.Time

	turn    *agentTurn // the in-flight agent turn, or nil
	pending *turnRequest
}

// notification is one retained push: a header or a message.
type notification struct {
	notifSeq int64
	header   *l8ai.ChatConversation
	message  *l8ai.ChatMessage
}

func newConversation(header *l8ai.ChatConversation) *conversation {
	return &conversation{header: cloneConversation(header), lastUsed: time.Now()}
}

// retain keeps the notification for gap fill, dropping the oldest beyond max.
func (this *conversation) retain(n *notification, max int) {
	this.notifs = append(this.notifs, n)
	if len(this.notifs) > max {
		this.notifs = this.notifs[len(this.notifs)-max:]
	}
}

// isMember reports whether the user is a member of the conversation.
func (this *conversation) isMember(userId string) bool {
	return this.member(userId) != nil
}

func (this *conversation) member(userId string) *l8ai.ChatMember {
	for _, m := range this.header.Members {
		if m != nil && m.UserId == userId {
			return m
		}
	}
	return nil
}

// isAgentConversation is true for a conversation with Tanto.
func (this *conversation) isAgentConversation() bool {
	return this.header.PeerKind == l8ai.ChatPeerKind_CHAT_PEER_KIND_AGENT
}

// recordMessage moves the header forward for a message that was just assigned
// its seq. The sender's read position moves to the message.
func (this *conversation) recordMessage(m *l8ai.ChatMessage) {
	this.header.LastSeq = m.Seq
	this.header.LastMessageAt = m.SentAt
	this.header.LastPreview = preview(m)
	if sender := this.member(m.SenderId); sender != nil {
		sender.LastReadSeq = m.Seq
	}
}

func preview(m *l8ai.ChatMessage) string {
	text := m.Body
	if text == "" && len(m.Attachments) > 0 && m.Attachments[0] != nil {
		text = m.Attachments[0].FileName
	}
	if utf8.RuneCountInString(text) <= previewLength {
		return text
	}
	runes := []rune(text)
	return string(runes[:previewLength]) + "…"
}

func cloneConversation(c *l8ai.ChatConversation) *l8ai.ChatConversation {
	return proto.Clone(c).(*l8ai.ChatConversation)
}

func cloneMessage(m *l8ai.ChatMessage) *l8ai.ChatMessage {
	return proto.Clone(m).(*l8ai.ChatMessage)
}
