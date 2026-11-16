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

// Package common holds the l8ai service area, service names, the Tanto
// identity, deterministic conversation IDs and the tunable defaults.
package common

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

// ServiceArea is shared by every l8ai service.
const ServiceArea = byte(81)

// Chat router (public).
const (
	ChatMsgService  = "ChatMsg"
	ChatConvService = "ChatConv"
	ChatPeerService = "ChatPeer"
	ChatSyncService = "ChatSync"
)

// Service groups: each process elects one leader for its public services.
const (
	ChatRouterGroup = "ChatRtr"
	ChatUsersGroup  = "ChatUsr"
)

// Chat router (internal).
const (
	ChatIdxService = "ChatIdx"
	ChatRegService = "ChatReg"
)

// Chat instances (internal).
const (
	ChatShMsgService  = "ChatShMsg"
	ChatShCnvService  = "ChatShCnv"
	ChatShSyncService = "ChatShSync"
)

// Chat users (public).
const (
	ChatStatService  = "ChatStat"
	ChatGroupService = "ChatGroup"
)

// Chat ORM.
const (
	ChatMsgDbService  = "ChatMsgDb"
	ChatCnvDbService  = "ChatCnvDb"
	ChatGrpDbService  = "ChatGrpDb"
	ChatStatDbService = "ChatStatDb"
)

// Chat agent.
const ChatAgentService = "ChatAgent"

// Tanto is a virtual chat participant, not a security user.
const (
	TantoUserId      = "tanto"
	TantoDisplayName = "Tanto (Layer8)"
)

// idNamespace is the UUIDv5 namespace for all deterministic l8ai IDs.
var idNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/saichler/l8ai"))

// DeterministicID returns the UUIDv5 of name in the l8ai namespace.
func DeterministicID(name string) string {
	return uuid.NewSHA1(idNamespace, []byte(name)).String()
}

// DirectConversationID is the "General" conversation of two users.
// The order of the two user IDs does not matter.
func DirectConversationID(userA, userB string) string {
	return DeterministicID("dm|" + DirectPeerKey(userA, userB))
}

// DirectPeerKey is the peer key of a direct conversation: "<minUserId>|<maxUserId>".
func DirectPeerKey(userA, userB string) string {
	if userB < userA {
		userA, userB = userB, userA
	}
	return userA + "|" + userB
}

// GroupConversationID is the "General" conversation of a group.
func GroupConversationID(groupId string) string {
	return DeterministicID("grp|" + groupId)
}

// AgentPeerKey is the peer key of a user's default Tanto conversation.
func AgentPeerKey(userId string) string {
	return "agent|" + userId
}

// AgentConversationID is a user's default Tanto conversation.
func AgentConversationID(userId string) string {
	return DeterministicID(AgentPeerKey(userId))
}

// AgentReplyMessageID is the deterministic ID of the agent's reply to a turn,
// so a re-dispatched turn cannot produce a duplicate reply.
func AgentReplyMessageID(turnId string) string {
	return DeterministicID("reply|" + turnId)
}

// PeerRowID is the ID of a computed ChatPeer row.
func PeerRowID(ownerId string, peerKind int32, peerId string) string {
	return DeterministicID(ownerId + "|" + strconv.Itoa(int(peerKind)) + "|" + peerId)
}

// Defaults for the tunables. Every value can be overridden through ChatConfig.
const (
	DefaultRingSize              = 50               // latest messages kept per conversation on its owner
	DefaultPageSize              = 10               // messages per page ("load 10 older")
	DefaultRingEviction          = 30 * time.Minute // idle time before a ring is evicted
	DefaultFlushInterval         = time.Second      // write-behind flush to chat-orm
	DefaultFlushMaxItems         = 50               // or flush as soon as this many writes are queued
	DefaultReadTimeoutSeconds    = 3                // bounded Request for fan-out reads
	DefaultUserInfoCacheTTL      = 30 * time.Second // per-aaaId UserInfo cache on chat instances
	DefaultNotifRetain           = 10               // notifications retained per conversation for gap fill
	DefaultHeartbeatInterval     = 60 * time.Second // client presence heartbeat
	DefaultOfflineAfter          = 150 * time.Second
	DefaultAwayAfter             = 5 * time.Minute
	DefaultClientResendAfter     = 10 * time.Second
	DefaultSyncPollInterval      = 60 * time.Second
	DefaultGapWait               = time.Second
	DefaultPeerListSize          = 30                // peers shown in the sidebar
	DefaultAgentTurnTimeout      = 300 * time.Second // turn deadline enforced by the owner's watchdog
	DefaultAgentMaxAttempts      = 2
	DefaultContextWindowTokens   = 200000
	DefaultMaxOutputTokens       = 4096
	DefaultMaxToolResultTokens   = 4096
	DefaultCompactionThresholdPc = 60 // compact when summary + tail exceed this % of the budget
	DefaultCompactionMaxTail     = 40 // or when the tail is longer than this many messages
	DefaultVerbatimTail          = 10 // newest messages never compacted
)

// ChatConfig carries the tunables. Start from NewChatConfig and override fields as needed.
type ChatConfig struct {
	RingSize              int
	PageSize              int
	RingEviction          time.Duration
	FlushInterval         time.Duration
	FlushMaxItems         int
	ReadTimeoutSeconds    int
	UserInfoCacheTTL      time.Duration
	NotifRetain           int
	HeartbeatInterval     time.Duration
	OfflineAfter          time.Duration
	PeerListSize          int
	AgentTurnTimeout      time.Duration
	AgentMaxAttempts      int
	ContextWindowTokens   int
	MaxOutputTokens       int
	MaxToolResultTokens   int
	CompactionThresholdPc int
	CompactionMaxTail     int
	VerbatimTail          int
}

// NewChatConfig returns a ChatConfig holding every default.
func NewChatConfig() *ChatConfig {
	return &ChatConfig{
		RingSize:              DefaultRingSize,
		PageSize:              DefaultPageSize,
		RingEviction:          DefaultRingEviction,
		FlushInterval:         DefaultFlushInterval,
		FlushMaxItems:         DefaultFlushMaxItems,
		ReadTimeoutSeconds:    DefaultReadTimeoutSeconds,
		UserInfoCacheTTL:      DefaultUserInfoCacheTTL,
		NotifRetain:           DefaultNotifRetain,
		HeartbeatInterval:     DefaultHeartbeatInterval,
		OfflineAfter:          DefaultOfflineAfter,
		PeerListSize:          DefaultPeerListSize,
		AgentTurnTimeout:      DefaultAgentTurnTimeout,
		AgentMaxAttempts:      DefaultAgentMaxAttempts,
		ContextWindowTokens:   DefaultContextWindowTokens,
		MaxOutputTokens:       DefaultMaxOutputTokens,
		MaxToolResultTokens:   DefaultMaxToolResultTokens,
		CompactionThresholdPc: DefaultCompactionThresholdPc,
		CompactionMaxTail:     DefaultCompactionMaxTail,
		VerbatimTail:          DefaultVerbatimTail,
	}
}
