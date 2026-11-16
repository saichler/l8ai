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
	"sort"
	"strings"

	aicommon "github.com/saichler/l8ai/go/ai/common"
	"github.com/saichler/l8ai/go/types/l8ai"
	"github.com/saichler/l8srlz/go/serialize/object"
	"github.com/saichler/l8types/go/ifs"
	"github.com/saichler/l8types/go/types/l8api"
)

// peers computes this instance's part of the requester's sidebar (plan §4.2):
// one ChatPeer per peer across the owned conversations the requester belongs
// to. The router merges the parts of every instance, then sorts and limits.
// Display names are filled for Tanto and groups only; the UI resolves users.
func (this *Instance) peers(userId string, limit int) ifs.IElements {
	rows := make(map[string]*l8ai.ChatPeer)
	for _, c := range this.owned() {
		c.mtx.Lock()
		mine := c.member(userId)
		if mine == nil {
			c.mtx.Unlock()
			continue
		}
		kind, peerId, name := peerOf(c.header, userId)
		rowId := aicommon.PeerRowID(userId, int32(kind), peerId)
		row, ok := rows[rowId]
		if !ok {
			row = &l8ai.ChatPeer{PeerRowId: rowId, OwnerId: userId, PeerKind: kind, PeerId: peerId, DisplayName: name}
			rows[rowId] = row
		}
		if c.header.LastMessageAt > row.LastMessageAt {
			row.LastMessageAt = c.header.LastMessageAt
			row.LastPreview = c.header.LastPreview
		}
		if row.DisplayName == "" || c.header.ConversationId == aicommon.GroupConversationID(c.header.GroupId) {
			if name != "" {
				row.DisplayName = name
			}
		}
		if c.header.LastSeq > mine.LastReadSeq {
			row.UnreadConversationIds = append(row.UnreadConversationIds, c.header.ConversationId)
		}
		c.mtx.Unlock()
	}

	list := make([]*l8ai.ChatPeer, 0, len(rows))
	for _, row := range rows {
		list = append(list, row)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastMessageAt > list[j].LastMessageAt })
	if limit <= 0 || limit > this.cfg.PeerListSize {
		limit = this.cfg.PeerListSize
	}
	if len(list) > limit {
		list = list[:limit]
	}
	return object.NewQueryResult(list, &l8api.L8MetaData{})
}

// peerOf returns the peer of a conversation as seen by userId.
func peerOf(h *l8ai.ChatConversation, userId string) (l8ai.ChatPeerKind, string, string) {
	switch h.PeerKind {
	case l8ai.ChatPeerKind_CHAT_PEER_KIND_AGENT:
		return h.PeerKind, aicommon.TantoUserId, aicommon.TantoDisplayName
	case l8ai.ChatPeerKind_CHAT_PEER_KIND_GROUP:
		return h.PeerKind, h.GroupId, h.Title
	}
	parts := strings.Split(h.PeerKey, "|")
	if len(parts) == 2 && parts[0] == userId {
		return h.PeerKind, parts[1], ""
	}
	if len(parts) == 2 {
		return h.PeerKind, parts[0], ""
	}
	return h.PeerKind, h.PeerKey, ""
}
