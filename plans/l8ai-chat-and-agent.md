# l8ai — Scalable Chat with Integrated AI Agent "Tanto (Layer8)"

Status: **DRAFT, revision 3: router redesign, framework-change policy and the `UserInfo` security method applied; `ChatDir`/`Users` dropped (see §14). Waiting for approval to implement.** Nothing will be implemented until you approve it (PlanRequirements).
Companion document: `plans/l8ai-agent-scaling-assessment.md`. It covers agent concurrency, context management, compaction and sizing.

Sources reviewed: `l8book/ai/layer-8-guide-lines.md`, `layer-8-arch.md`, `prime-object.md`, l8agent (both the local checkout and origin/main `8532d4d`, which probler vendors), l8types `ifs/Security.go` and `ifs/VNic.go`, l8bus VNic (`SendUnicast.go`, `SendForward.go`), l8services (SLA, BaseService, transactions/replication, DCache, FileStore), l8orm, l8common, l8web (ServiceHandler, Registrations, WebSocketManager), l8utils cache subscriptions, l8ui (websocket client, popup, file upload, theme tokens, l8agent bubble/chat), l8erp documents module (`DocDoc`), and probler (models.go, agent/, k8s/, newui web). l8secure was read only to understand behaviour behind `ISecurityProvider`; no l8ai or probler code calls l8secure services.

---

## 1. Goals and scope

| # | Requirement (from `request`) | Where it is addressed |
|---|---|---|
| R1 | Chat for 20K users. One of the users is the AI agent "Tanto (Layer8)" | §3, §4, §7.1 (Tanto is a virtual chat participant, not a security user), Phases 6–8 |
| R2 | The UI lives in l8ui, is generic and reusable, and appears as a bottom-right bubble add-on | §6, Phase 9 |
| R3 | Sidebar: current user plus status (online/offline/away/custom dot), a divider, then 30 peers (Tanto pinned on top, the rest sorted by latest message) with their status | §6.2 |
| R4 | Clicking a peer opens a conversation list, sorted by latest message, with a purple dot for unread and a grey dot for read | §6.3 |
| R5 | The thread shows the last 10 messages, with a "load 10 older" control at top-left | §6.4 |
| R6 | A "+" button on the sidebar opens a group dialog: group name, a searchable user list on the right, selected users on the left, and Create | §6.5 |
| R7 | A "+" button on the conversation list opens a popup to name a new conversation and create it | §6.6 |
| R8 | Upload a file into a conversation. The other side can quick-view or download it. Reuse the l8erp documents logic | §5.6, §6.7 |
| R9 | Conversations, groups and statuses update live over websockets | §5.5 |
| R10 | The chat service is in-memory only, runs as several instances, and has an indexer/router that forwards each message to the instance owning the conversation | §4 |
| R11 | The chat ORM is transactional with replication count 1, and chat forwards every message to it | §4.5, §5.4 |
| R12 | LLM Providers and LLM Profiles live in l8secure and are exposed through ISecurityProvider | Phases 1–2 |
| R13 | The AI agent is a separate service. Chat forwards messages addressed to it. It does what the l8agent backend does, with new code | §7, Phase 8 |
| R14 | Solve the model→service mapping the way l8web does: register all models on load, like probler `models.Register` | §7.3 |
| R15 | Multiple agent instances: an assessment of concurrency, context and compaction | assessment doc, §7.5 |
| R16 | Probler is the first adopter. Remove l8agent from it | Phase 11 |

Out of scope here: migrating l8erp and l8alarms off l8agent. That is covered by the follow-up plan `plans/l8ai-followup-l8erp-l8alarms-migration.md` (Q4). `l8ui/l8agent/` and the l8agent repo stay until that follow-up is done.

---

## 2. Classification, canonical references and framework-change policy

- **l8ai is a library project, like l8agent.** It ships proto types, `Activate*` functions and tests. The adopter (probler) owns the binaries, images and K8s manifests, the same way probler owns `go/prob/agent/` today.
- **Data model:** ERP-style (CRUD entities, ORM). Canonical reference: **l8erp** for service and callback structure. **probler** is canonical for binary layout, Dockerfiles and K8s, because it is the adopter.
- **UI:** l8ui components (`Layer8DPopup`, `Layer8FileUpload`, `Layer8DWebSocket`, `Layer8Markdown`, theme tokens), plus a new l8ui folder `l8chat/` with desktop and mobile parity.

### 2.1 Framework-change policy
**This plan changes no Layer 8 framework component unless you have explicitly authorised that change.** Services reach security **only** through `ISecurityProvider` (`vnic.Resources().Security()`). No l8ai or probler code calls l8secure services (tokens, users, roles, credentials) directly.

**Authorised framework changes (the complete list):**

| # | Component | Change | Who | Authorised by |
|---|---|---|---|---|
| F1 | l8types `ifs.IVNic` + l8bus VNic | `Unicast(dest, svc, area, action, payload, aaaId ...string) error`: optional aaaId, placed in the message header like `Request` (also the l8utils `MockVNic` test double) | done by me at your request: l8types `3e9cd77`, l8bus `9f9c2eb`, l8utils `387e332` | you (round 3) |
| F2 | l8types | `ISecurityProvider.LlmProfile`, plain return values like `Credential` (the LLM protos live in l8secure, F4) | plan, Phase 1 | R12 + D2 |
| F3 | l8types | `ISecurityProvider.UserInfo` (§8.1) | plan, Phase 1 | you (round 3) |
| F4 | l8secure | LLM provider/profile services, the implementations of F2–F3, and the probler security config seed | plan, Phase 2 | R12 + you (round 3) |
| F5 | l8ui | **new** folder `l8ui/l8chat/` only. No existing l8ui file is edited | plan, Phase 9 | request ("UI built in l8ui") |
| F6 | l8types `ifs` + l8services manager | `IServiceMessageHandler` extends `IServiceHandler` with `PostMessage`/`PutMessage`/`PatchMessage`/`DeleteMessage`/`GetMessage(IElements, IVNic, *Message)`. `ServiceManager.handle()` passes the message to them when the handler implements the interface (covers `Handle` and `TransactionHandle`); other handlers are unchanged | done by me at your request: l8types `0ea0698` + `e899129`, l8services `1d24f81` + `301d82d` | you (Phase 6) |
| F7 | l8utils `aggregator` | `NewAggregator(vnic, intervalInSeconds, maxSize, timeoutInSeconds)`: flushes on whichever cap is reached first, time or size (`maxSize <= 0` = time only). Flushing stays in the loop goroutine, so batches never overlap | done by me at your request: l8utils `00da90a` | you (Phase 6) |
| F8 | l8parser, l8collector, l8inventory, l8stocks | the callers of `NewAggregator` pass the new size cap `0` (time-only flushing, as before); F7 had missed them | done by me at your request: l8parser `d964c34`, l8collector `ac41a30`, l8inventory `a840c77`, l8stocks `db3b7d9` | you (Phase 10) |
| F9 | l8secure `AllowedTypes` | lists the root types with `Introspector().Nodes(false, true)`; `Nodes(true, true)` now panics in l8reflect ("no node can be both a leaf and a root"), so every caller of `AllowedTypes` panicked | done by me at your request: l8secure `a6c4cd2` | you (Phase 10) |

**Not authorised: framework gaps that stay as they are unless you approve them.** Each one has a default that the plan uses instead:

| # | Gap | Default in this plan (no framework change) | Consequence of the default |
|---|---|---|---|
| N1 | l8services `T02_Get` runs query GETs locally only on replicated transactional services | one chat-orm instance | the chat ORM cannot scale out |
| N2 | SYS > Security tabs for LLM Providers/Profiles (`l8sys-config.js`, a new CRUD file in `l8ui/sys/security/`) | providers and profiles are seeded and maintained through the security config JSON (SecurityRules) | no admin UI for LLM settings |
| N3 | l8ui bugs found while exploring: `l8sys-init.js` `SYS` fallback; `l8security-credentials-crud.js:292-295` id mismatch | not fixed | only matters if N2 is approved |
| N4 | `layer8d-websocket.js` `MAX_LIVE=5` LRU cap | l8chat uses only 3 registrations (§5.5) | a host page with more than 2 live tables of its own can evict a chat registration; l8chat re-registers when the panel opens |
| N5 | l8services FileStore has no per-path authorisation (Risk A2) | not changed | any authenticated user who learns an attachment path can download it |
| N6 | Fallback for the agent descriptor listener, should Spike S1 fail | `AgentConfig.ServiceMap` supplied by the adopter (§7.3) | the adopter maintains a model→service map |
| N7 | Docs: `l8book` guide lines (PrdL8uiIncludesAudit list, project table), `l8erp/rules/*-script-loading-order.md` | not edited | the l8chat block is documented in this plan only |
| N8 | `Layer8FileUpload` has only `download()` (saves the file); no method returns the file data for an inline quick view | l8chat offers Download only | no in-chat quick view of images, PDFs or text (§6.7) |
| N9 | `Layer8FileUpload` resolves through `Layer8DConfig` and the host's `getAuthHeaders()`, which mobile does not define | l8chat mobile uses it as mobile forms do | mobile uploads and downloads are sent without the bearer token |

Not a framework change: re-vendoring. Go builds use only the main module's vendor directory, so the l8utils subscription fix (`3e5a83f`) reaches the binaries when **probler and l8ai** bump l8utils and re-vendor. You run those commands (VendorAndGit).

---

## 3. Service areas and names

Free areas were verified by grepping all ecosystem Go and JS. 72–78 are taken (l8secure, l8events, l8notify) and 120 is l8agent/l8erp AIA.

**Terminology.** An **instance** is one process with its own VNic (one UUID) on the VNet, hosting one or more Layer 8 services. A service instance is identified by `(ServiceName, Area, VNic UUID)`, and `GetParticipants(name, area)` returns the VNic UUIDs that host it. Kubernetes only packages and restarts these processes (one pod = one instance). Under KIND or locally they are plain processes.

| Area | Owner | Process (instance) | Services (ServiceName ≤ 10 chars) |
|---|---|---|---|
| **79** | l8secure | security (existing) | `LlmProv` (L8LlmProvider), `LlmProf` (L8LlmProfile) |
| **81** | l8ai | chat-router | public: `ChatMsg`, `ChatConv`, `ChatPeer`, `ChatSync` · internal: `ChatIdx` (the index), `ChatReg` (routed live registrations) |
| **81** | l8ai | chat | internal: `ChatShMsg`, `ChatShCnv`, `ChatShSync` |
| **81** | l8ai | chat-users | public: `ChatStat` (presence), `ChatGroup` |
| **81** | l8ai | chat-orm | `ChatMsgDb`, `ChatCnvDb`, `ChatGrpDb`, `ChatStatDb` |
| **81** | l8ai | chat-agent | `ChatAgent` |

All l8ai services share area 81 (Maintainability: one ServiceArea per module). l8secure gives each entity its own area, but here both LLM entities share 79 so they occupy only one byte.

---

## 4. Backend architecture

```
                       writes (POST/PATCH): Unicast with aaaId, no wait
 Browser ──REST /81/ChatMsg|ChatConv──► web (l8web) ──LeaderRequest──► chat-router (leader)
    ▲                                                                   │ ChatIdx: conversationId → chat instance UUID
    │                                                                   │ vnic.Unicast(ownerUuid, "ChatShMsg", 81, action, msg, aaaId)
    │                                                                   ▼
    │  websocket push                                          chat instance (owner of the conversation)
    └──── web ◄── multicast websock/0 ◄──────────────────────── │ Security().UserInfo(aaaId) → sender      
                                                                │ single writer: dedupe, seq, ring buffer
                                                                │ write-behind (aggregator) ──► chat-orm ──► Postgres
                                                                └ Unicast ChatAgentTurn (requester aaaId) ──► chat-agent ──► LLM
                                                                                                               │ tools as requester
                                  reply as Tanto: Unicast directly to the dispatching chat instance ◄──┘

 reads (GET, register): router → owner (Request, 3 s) or fan-out to all chat instances and merge
 presence / groups: web ──► chat-users (separate services, not on the router)
 user names and user search: the browser queries the users endpoint directly (ScopeView applies)
```

### 4.1 Chat router: an index and nothing else
- **The index (`ChatIdx`)** holds one row per conversation: `ChatConvIndex{conversationId, ownerUuid}`. It is a DCache, so every router replica keeps a full copy and a new leader has the index immediately.
- **Writes:** the router receives the request from the web, looks up `conversationId`, and calls `vnic.Unicast(ownerUuid, "ChatShMsg"|"ChatShCnv", 81, action, msg, aaaId)` with the aaaId it received. The router implements `IServiceMessageHandler` (F6), so the aaaId arrives as an argument of `PostMessage`/`PatchMessage`/…; it passes it on with Unicast (F1). It returns `accepted` to the web straight away and **never waits for the chat instance**, so a slow or dead instance cannot block the router.
- **The router does not resolve identity or inspect content.** The chat instance also implements `IServiceMessageHandler` (F6), gets the message (`msg.AAAId()`, `msg.Source()`) and does its own security checks.
- **A conversation ID is a UUID, so it is routable.** Default conversations get deterministic IDs. That means opening one needs no lookup, and two users opening the same DM at once cannot create duplicates:
  - direct message "General": `UUIDv5("dm|" + minUserId + "|" + maxUserId)`
  - group "General": `UUIDv5("grp|" + groupId)`
  - Tanto default: `UUIDv5("agent|" + userId)`
  - conversations created with "+": a client-generated random UUID.
- **New conversations:** when a conversation ID has no index row, the router picks the chat instance with the fewest conversations from `GetParticipants("ChatShCnv", 81)`, records the row, then Unicasts.
- **Only the router leader creates index rows.** The web routes with `ifs.M_Leader` by default (`l8web/.../ServiceHandler.go:106`), so in normal operation every request reaches the leader. A non-leader replica that sees an unknown conversation sends it to the leader (`LeaderRequest`). This means two routers can never assign different owners. A chat instance accepts writes only for conversations it was assigned, and drops anything else.
- **Registration replay (`ChatReg`):** the router records every registered query it routes (`aaaId`, `registerId`, conversation or fan-out, query text) in a DCache. The router replays them with the original aaaId in two cases:
  - When a conversation moves to a new owner, it replays that conversation's registrations to the new owner.
  - When a chat instance joins, it replays the fan-out registrations to it.
  The browser's live updates therefore survive a move without the browser knowing. Unregisters (including the ones l8web sends when a socket closes, `Registrations.go`) remove the record.

### 4.2 Reads
Reads need an answer, so they are the one place where the router waits. The wait is bounded:

| Read | Path | Timeout |
|---|---|---|
| Thread messages, conversation header, `ChatSync` gap fill (one conversation) | router → owner instance (`Request` with aaaId) | 3 s. On expiry the client gets an error and retries |
| `ChatSync` latest only (the 60 s poll, §5.7) | router groups the conversation IDs by owner → one `Request` per owner | 3 s per owner |
| Sidebar (`ChatPeer`) and conversation list for a peer (`ChatConversation`) | router → **every** chat instance in parallel (`Request` with aaaId), then merge, sort and limit | 3 s per instance. An instance that does not answer is skipped and a warning is logged; the response is not marked (no field exists, DR30 #5) |
| Older messages not in an instance's ring | the owner instance reads `ChatMsgDb` itself | — |

Each chat instance scopes fan-out reads to the requester: it calls `Security().UserInfo(aaaId)` with `msg.AAAId()` from `GetMessage` (F6) and returns only rows where that user is a member.

### 4.3 Chat instances
- In-memory only (no `SetStore`). Each process hosts `ChatShMsg` (messages) and `ChatShCnv` (conversation headers), and each service has its own `Cache`, which the websocket registrations need.
- **Identity:** the services implement `IServiceMessageHandler` (F6). `Security().UserInfo(aaaId)` returns `(userId, fullName)`. The result is cached per aaaId for 30 s. It is used to stamp `senderId`, to check membership, and to scope reads. An error (unknown or expired aaaId) rejects the request.
- **Each conversation has exactly one owner instance, which is its single writer.** The owner dedupes by `messageId`, assigns a monotonically increasing `seq` and keeps a ring buffer of the latest K messages (default 50). No cross-instance locks are needed.
- **Conversation headers are always resident; message rings are not.** A header is about 1–2 KB, so even 200K conversations spread over 3 instances take only a few hundred MB. Message rings are loaded on first use and dropped after 30 idle minutes. Keeping every header in memory makes the sidebar fan-out complete without querying the ORM.
- **Cold start:** the router leader pages through `ChatCnvDb`, assigns each conversation to the least-loaded chat instance and Unicasts batches of headers to it. Chat instances never read headers themselves at startup.
- **Instance lost:** when an instance leaves `GetParticipants` (or a Unicast to it fails), the router leader reassigns its conversations to the remaining instances. Each new owner loads those headers by ID from `ChatCnvDb` and lazily loads their rings. l8orm cannot translate `in [...]` to SQL, so each header is one GET, run by 8 workers (DR29 #5). Up to about 1 s of unflushed writes can be lost (Q2).
- **Instance joins:** new conversations go to it because it is the least loaded. Existing conversations stay where they are (sticky). Rebalancing is out of scope for v1.
- **Graceful shutdown (SIGTERM):** stop accepting writes, queue the dirty headers, wait one aggregator flush interval (the aggregator has no flush call, DR29 #6), and send `release` to the router, which reassigns the instance's conversations. Planned restarts lose nothing.
- **Router ↔ instance protocol (DR29 #2):**
  - **assign:** the router POSTs to `ChatShCnv` **without an aaaId**: `ChatConversation` headers (cold start), or `ChatConvIndex` rows whose headers the instance loads from `ChatCnvDb` (instance lost). A POST with an aaaId is a user creating a conversation.
  - **release:** the instance sends `DELETE ChatIdx` (`ChatConvIndex` rows with `ownerUuid` = itself) to the router leader. The router treats a DELETE of index rows by their owner as a release and reassigns them.
  - Instances accept user requests only when `msg.Source()` is a router replica (`GetParticipants("ChatIdx", 81)`).
- **Deterministic IDs (DR29 #7):** a UUIDv5 conversation ID is accepted on create only if it equals the ID derived from that conversation's own peers or group; client-created conversations use random UUIDs.

### 4.4 Chat users (presence, groups)
- A separate process, `chat-users`, with 2 replicas. The web reaches these services directly. They are not on the router path.
  - `ChatStat`: presence for 20K users. A DCache, so every replica keeps a full copy.
  - `ChatGroup`: groups. A DCache persisted through `ChatGrpDb`. Creating a group also POSTs its "General" conversation (deterministic ID) through `ChatConv`.
  - **Group membership propagation (concern #4):** every group conversation keeps its own `members` list on its owner instance, so a membership change must reach all of them.
    - After a `ChatGroup` PUT/PATCH changes `member_ids`, chat-users sends `PATCH ChatConv {groupId, members}` to the router with the requester's aaaId.
    - chat-users does not need to know the group's conversation IDs. The router fans the update out by Unicast (with the aaaId) to **every** chat instance.
    - Each instance updates every conversation it owns with that `groupId`: it adds new members with `last_read_seq = 0` and removes departed ones.
    - The header change is pushed as usual:
      - new members start seeing the group's conversations, because they now match their own `members.userId` registration;
      - removed members stop matching, and the instance rejects their later reads and posts.
    - Only the group's creator (`created_by`, checked with `UserInfo`) may change membership. On the instance, a membership change applies only to conversations the requester created, so "+" group conversations started by other members are not updated (DR29 #4).
    - R6 asks only for group creation, so there is no edit-group UI. This covers changes made through the API.
- **User names and user search are resolved by the UI**, not by any chat service. The browser queries the users endpoint with the user's own token and selects only `userId` and `fullName`. There is no dedicated chat role: the existing roles decide what each user can read (§8.2, DR26). The chat backend works with user IDs only and never needs names.

### 4.5 ChatOrm
- Four ORM services built with `common.NewOrmSLA` + `ActivateService`. The replication count is overridden to **1** (`NewOrmSLA` defaults to 3). They stay transactional, which satisfies R11.
- **One chat-orm instance** (N1). `T02_Get` for replicated transactional services runs query GETs locally only, so with replication 1 spread over more than one instance, `select ... where conversationId=X` would miss rows. One instance with one Postgres is exact.
- Writes arrive through the l8utils aggregator (batched, `ifs.Leader`). The expected rate is about 100 messages/s at peak, which is comfortable for one Postgres.

### 4.6 ChatAgent instances
These are described in §7.

---

## 5. Data model (`l8ai/proto/l8ai.proto`, package `l8ai`, go `go/types/l8ai`)

All primary keys are UUID strings. Every enum has `_UNSPECIFIED = 0`. Every Prime Object has `XList{repeated X list = 1; l8api.L8MetaData metadata = 2;}`.

```protobuf
enum ChatPeerKind      { CHAT_PEER_KIND_UNSPECIFIED=0; CHAT_PEER_KIND_USER=1; CHAT_PEER_KIND_GROUP=2; CHAT_PEER_KIND_AGENT=3; }
enum ChatPresence      { CHAT_PRESENCE_UNSPECIFIED=0; CHAT_PRESENCE_ONLINE=1; CHAT_PRESENCE_AWAY=2; CHAT_PRESENCE_OFFLINE=3; CHAT_PRESENCE_CUSTOM=4; }
enum ChatMessageKind   { CHAT_MESSAGE_KIND_UNSPECIFIED=0; CHAT_MESSAGE_KIND_TEXT=1; CHAT_MESSAGE_KIND_SYSTEM=2; CHAT_MESSAGE_KIND_AGENT=3; }
enum ChatAgentState    { CHAT_AGENT_STATE_UNSPECIFIED=0; CHAT_AGENT_STATE_IDLE=1; CHAT_AGENT_STATE_RUNNING=2; CHAT_AGENT_STATE_FAILED=3; }

message ChatMember     { string user_id=1; int64 last_read_seq=2; int64 joined_at=3; }            // child of ChatConversation
message ChatAttachment { string attachment_id=1; string storage_path=2; string file_name=3;      // child of ChatMessage
                         int64 file_size=4; string mime_type=5; string checksum=6; }

message ChatConversation {           // Prime. Owner: one chat instance; persisted in ChatCnvDb
  string conversation_id=1; ChatPeerKind peer_kind=2;
  string peer_key=3;                 // USER: "<minUserId>|<maxUserId>", GROUP: group_id, AGENT: "agent|<userId>"
  string group_id=4; string title=5; string created_by=6; int64 created_at=7;
  int64 last_seq=8; int64 last_message_at=9; string last_preview=10;
  repeated ChatMember members=11;    // membership and per-member read position live here (no inbox rows)
  string llm_profile_id=12;          // AGENT conversations: default profile for this conversation
  string summary=13; int64 summary_up_to_seq=14;   // agent compaction state (see assessment)
  ChatAgentState agent_state=15; string agent_turn_id=16;
  l8api.AuditInfo audit_info=17;     // field type per prime-object-types.proto
  int64 notif_seq=18;                // §5.7: per-conversation notification sequence, in memory only (not persisted)
}
message ChatMessage {                // see Rule-deviation D1
  string message_id=1;               // client-generated UUID → idempotent retries
  string conversation_id=2; int64 seq=3; string sender_id=4; ChatMessageKind kind=5;
  string body=6; repeated ChatAttachment attachments=7; int64 sent_at=8;
  string llm_profile_id=9; int32 token_count=10; string agent_turn_id=11;
  int64 notif_seq=12;                // §5.7: set on push; not persisted
}
message ChatGroup        { string group_id=1; string name=2; repeated string member_ids=3; string created_by=4; int64 created_at=5; }
message ChatPeer {                   // computed view (not persisted): one row per peer of owner, merged by the router
  string peer_row_id=1;              // UUIDv5(owner_id + peer_kind + peer_id)
  string owner_id=2; ChatPeerKind peer_kind=3; string peer_id=4; string display_name=5;
  int64 last_message_at=6; string last_preview=7;
  repeated string unread_conversation_ids=8;   // union across instances; the client keeps it current from pushes
}
message ChatUserStatus { string user_id=1; ChatPresence presence=2; string custom_text=3; string custom_color=4; int64 last_seen_at=5; }
message ChatConvIndex  { string conversation_id=1; string owner_uuid=2; int64 assigned_at=3; }   // router index row
message ChatRegistration { string registration_id=1; string aaa_id=2; string register_id=3;       // router: routed live queries,
                           string conversation_id=4; bool fan_out=5; string query_text=6; }       // replayed on owner change
message ChatSync {                   // §5.7: sync reply (query/response type only, not persisted)
  string conversation_id=1; int64 notif_seq=2; bool snapshot=3;
  ChatConversation conversation=4; repeated ChatMessage messages=5;
  int64 last_msg_seq=6;              // gap-fill request: highest message seq the client holds (DR29 #1)
}
message ChatAgentTurn  { string turn_id=1; string conversation_id=2; string requester_id=3;
                         string llm_profile_id=4; int64 trigger_seq=5; int32 attempt=6; }
                         // internal Unicast payload; the requester's aaaId travels in the message header (F1)
```

### 5.1 Rule deviations that need your approval
- **D1 (APPROVED). `ChatMessage` is a separate service even though it has a parent (`conversation_id`).** Prime-Object Rule 1 test 4 says it should be an embedded child.
  - Why embedding does not work for messages: conversations grow without bound. Every append would rewrite the whole conversation row in ORM, which is O(n). And R5's "load 10 older" needs `seq`-ranged L8Query paging, which is impossible on an embedded repeated field.
  - l8agent made the same choice (`L8AgentChatMessage`), but keyed it by ConversationId, which was a bug. Here each row has its own UUID.
  - **Proposal:** treat it as an "unbounded child collection". It gets its own service but **no** UI nav/forms (it is not a CRUD screen), and it references the parent by string ID. `ChatMember` and `ChatAttachment` stay embedded.
  - Revision 2's `ChatInboxItem` is gone. Membership and read position live in `ChatConversation.members`, so this deviation is now smaller.
- **D2 (APPROVED by the framework owner). New methods on `ISecurityProvider` (l8types `ifs`):** `LlmProfile` (R12) and `UserInfo` (round 3). See §8.1.

- **D3 (APPROVED). Chat Prime Objects get no module CRUD screens.** PrimeObjectReferences Rule 3 gives every Prime Object a config entry, columns, forms, a nav entry and a reference registry entry. `ChatConversation` and `ChatGroup` are Prime Objects, but they are created, viewed and edited only through the l8chat bubble (§6): the conversation list, thread, group dialog and new-conversation popup. They get type registration (`RegisterTypes`) and mock generators, but no nav, columns, forms or reference registry entries. `ChatMessage` follows D1.

### 5.2 Message send flow
1. The UI shows the message as *pending*, then POSTs `/81/ChatMsg` with `{messageId(uuid), conversationId, body, attachments}`.
2. The web validates the token (aaaId) and calls the router (`LeaderRequest`).
3. The router looks up the owner in `ChatIdx` (assigning one if this is a new conversation, §4.1), calls `Unicast(ownerUuid, "ChatShMsg", 81, POST, msg, aaaId)`, and returns `accepted`.
4. The owner instance:
   1. Calls `Security().UserInfo(msg.AAAId())`. It stamps `senderId`, checks that the sender is a member, then dedupes by `messageId`. A `kind=AGENT` message from this path (anything arriving through the router) is rejected. Tanto's replies arrive only by the agent path in §7.1.
   2. Assigns `seq = last_seq+1` and appends to the ring.
   3. Updates the header's `last_*` fields and the sender's `last_read_seq`.
   4. Its caches emit notifications for the message and the header. These are multicast to `websock`/0 and pushed to every registered browser: the thread viewers, and every member's sidebar and conversation list.
   5. Enqueues the ORM writes (message and header).
   6. If the conversation is an AGENT conversation, it triggers an agent turn (§7.5).
5. **Acknowledgement:** the sender's browser sees its own message come back on the websocket carrying its `seq`, and marks it *sent*. If no push arrives within 10 s, the browser resends with the **same** `messageId`. The owner dedupes, so this is safe. That is how a message lost to a dying instance gets delivered.

### 5.3 Paging (R5)
- **First load:** `select * from ChatMessage where conversationId=X sort-by seq descending limit 10 page 0 register`.
- **Older:** `... where conversationId=X and seq<{oldestLoaded} sort-by seq descending limit 10 page 0` (always page 0, framework gotcha).
- The router sends both to the owner. The owner serves from the ring if it covers the range. Otherwise it falls through to `ChatMsgDb`.

### 5.4 Persistence and failure window
- **Write-behind (accepted, Q2):** the aggregator flushes every 1 s or every 50 items. Header writes for the same conversation are coalesced, so a burst of read-position updates becomes one write.
- **Planned restarts lose nothing:** graceful shutdown (§4.3) flushes before release. `terminationGracePeriodSeconds: 30`.
- **Hard crashes** are the only case that can lose up to about one second of unflushed messages. Causes: OOM kill when the memory limit is exceeded, node or VM failure, kernel/kubelet eviction under node pressure, or an unrecovered Go panic.
  - A message the sender still sees as *pending* is resent by the browser (§5.2 step 5).
  - Only a message that was already pushed but not yet flushed can be lost.
- **Mitigations against hard crashes:**
  - Memory is bounded: ring size K, idle ring eviction, and a guard that drops the oldest idle rings at 80% of the container limit.
  - Request handlers wrap `recover()` so that one bad message cannot kill the instance.
  - K8s memory limits are set from the load-test results.

### 5.5 Live updates (R9): uses the generic mechanism only
The UI opens 3 registered queries:

| Registration | Lands on | Drives |
|---|---|---|
| `select * from ChatConversation where members.userId=<me> register` | **every** chat instance (router fan-out, replayed to instances that join later) | sidebar order, unread dots, conversation list |
| `select * from ChatMessage where conversationId=<c> ... register` | the owner instance (replayed to a new owner on a move) | thread |
| `select * from ChatUserStatus where userId in [<30 visible peers>] register` | chat-users | status dots |

- The initial sidebar comes from the merged `ChatPeer` GET (§4.2). After that, the client updates it from conversation-header pushes:
  - the peer's `lastMessageAt` becomes the larger of the current value and the pushed one;
  - the conversation ID is added to `unreadConversationIds` when `lastSeq > my lastReadSeq`, and removed otherwise.
- The client subscribes by modelType (`ChatConversation`, `ChatMessage`, `ChatUserStatus`) through `Layer8DWebSocket.listen` and filters by primary key.
- No new push channel and no per-model notification code is needed. This follows the "Live Updates" section of the architecture doc.
- Dependencies:
  - The l8utils subscription fix, re-vendored in probler and l8ai (§2.1).
  - N4: the `MAX_LIVE=5` cap stays. 3 registrations keep chat within it on typical pages.
  - Spike S2: a registered query must match on a repeated child field (`members.userId`). Fallback, inside l8ai: add a flat `repeated string member_ids` to `ChatConversation`.

**Presence (served by chat-users):**
- **online:** the bubble PATCHes `ChatStat` as a heartbeat every 60s. A sweeper on the chat-users leader marks a user offline after 150s without one.
- **Notify only on transitions (concern #13):** the `ChatStat` handler on the leader compares the incoming presence with the cached row.
  - **Unchanged (a plain heartbeat):** only the leader's local last-heartbeat map is updated. Nothing is written to the DCache, so nothing replicates and no notification goes out.
  - **Changed** (offline→online, online→away, away→online, sweeper→offline, custom text/color): the row is written to the DCache, including `last_seen_at`. That write is the only thing that notifies registered UIs.
  - About 330 heartbeats per second become a few transitions per second.
  - **Leader switchover:** the new leader's heartbeat map starts empty, so its sweeper waits one full 150s window before marking anyone offline. Users who are still active send a heartbeat within that window.
- **away:** set by client idle detection (5 minutes without input or focus).
- **custom (Q3):** user-chosen text and color. It is persisted in `ChatStatDb` (ORM, written on change) and reloaded into the `ChatStat` DCache when chat-users starts, so it survives a full restart. Online/away/offline are always derived live and never persisted.
- Tanto is pinned ONLINE.

### 5.6 Attachments (R8): reusing the l8erp documents logic
- l8erp's `DocDoc` does **not** store files server-side. Its UI uploads through `Layer8FileUpload` → `/0/FileStore` (l8services) and keeps only metadata. l8ai reuses exactly that pattern. `ChatAttachment` is the embedded metadata child, mirroring the `DocDocument` fields. l8ai does not depend on l8erp's ERP-specific `DocDoc` service.
- **Upload:** `Layer8FileUpload.upload(file, conversationId, 1)` stores under `/data/l8files/<conversationId>/1/<name>`. The 5MB limit and SHA-256 checksum come from FileStore.
- **Quick view:** `Layer8FileUpload` download (PUT) produces a Blob URL. Images, PDFs and text render inline in `Layer8DPopup`. Other types offer download only.
- ⚠ **Risk A1:** FileStore writes to the **local disk of the web node**, and probler's web is a hostNetwork DaemonSet, so there is one web instance per node. Files uploaded on node A cannot be read on node B. Phase 11 mounts a shared volume (`/data/l8files`) for the web instances: a GKE shared PVC, and a hostPath/NFS note for baremetal.
- ⚠ **Risk A2 (open, N5):** FileStore PUT accepts any path under the root, so any authenticated user who learns a path can download the file. This plan does not change FileStore. Paths contain the conversation UUID and are shown only to members, which reduces but does not remove the risk.

### 5.7 UI consistency: per-conversation notification sequence
Pushes are fast but can be lost: a websocket drop, an owner crash, or a lost Unicast. This mechanism keeps the UI within about a minute of the true state, using only l8ai types and ordinary queries (no framework change).

**Server (owner chat instance):**
- Each conversation has a `notif_seq` counter, **in memory only and never persisted**. It starts at 1 whenever an instance takes ownership: on first assignment, or on reassignment after an instance fails or releases its conversations.
- Every change pushed for the conversation (`ChatConversation` header or `ChatMessage`) increments it, and the pushed object carries it (`notif_seq` field).
- The owner keeps the **last 10 notifications** per conversation.
- `ChatSync` requests (routed by the router to the owner, bounded `Request`):
  - **gap fill:** `select * from ChatSync where conversationId=X and notifSeq>N and lastMsgSeq=M`.
    - If everything after N is among the retained 10, the reply carries those messages and the current header (`snapshot=false`).
    - Otherwise the reply is a **snapshot** (`snapshot=true`): the current header plus the messages with `seq > M` (from the ring, falling back to `ChatMsgDb`), capped at one page.
  - **latest only:** `select * from ChatSync where conversationId in [...]` returns `{conversationId, notifSeq}` for each conversation, with no data. The router groups the IDs by owner and asks each owner once.

**UI, per conversation it holds (`last` = last applied `notif_seq`):**

| Received | Action |
|---|---|
| `seq = last+1` | apply |
| `seq` is 1 or 0 | **reset**: ownership moved. Set `last = seq`, then request a **snapshot**, which removes any message the UI shows that the server lost in a crash |
| `seq > last+1` | buffer and wait 1 s. If the gap was only reordering (e.g. 1,2,3,5,4), sort and apply. If it is still missing, send a gap-fill request for `notifSeq > last` |
| `seq ≤ last` | duplicate, drop |

**Detecting a lost final notification and other silent drift:**
- **Poll every 60 s:** the UI sends one "latest only" `ChatSync` query for the conversations it shows (the sidebar's conversations, the open list and the open thread).
  - If the server's `notifSeq` is higher than `last`, it runs the gap fill.
  - If it is **lower** than `last`, it treats that as a reset: ownership moved and the new owner's notification 1 was lost. It sets `last` to the server value and requests a snapshot.
- **Websocket reconnect:** the same gap fill runs for every conversation the UI shows.
- **Presence** is not covered by the sequence: status dots come from chat-users. The 60 s poll also re-reads `ChatUserStatus` for the visible peers, so dots are at most a minute stale.

Cost: one "latest only" query per online browser per minute, about 100 requests/s at 6K online users. These are served from memory.

---

## 6. UI: `l8ui/l8chat/` (generic, no project names or prefixes; new files only)

### 6.1 Files (each under 500 lines; shared core used by desktop **and** mobile, per the Second Instance Rule)

| File | Global | Purpose |
|---|---|---|
| `l8chat-core-api.js` | `L8ChatApi` | REST over `Layer8DConfig.resolveEndpoint(<endpoint from init config>)`, never a hardcoded path or area (§6.8); L8Query builders; headers via `getAuthHeaders()`/`Layer8MAuth` adapter; user lookup against the users endpoint (`L8User`), selecting only `userId` and `fullName` (`select userId,fullName from L8User ...`): name search, and batched `userId in [...]` name resolution for peer IDs, cached per session |
| `l8chat-core-store.js` | `L8ChatStore` | state (me, peers, conversations, open thread, messages, statuses, pending sends), pure reducers for pushed changes (uses `Layer8DWebSocket.applyChange`); per-conversation `last notif_seq`, reorder buffer, reset handling (§5.7) |
| `l8chat-core-live.js` | `L8ChatLive` | 3 registrations + `listen` by modelType, re-register on reconnect and on panel open, heartbeat + idle → away, resend of unacknowledged messages after 10 s; §5.7 gap detection (1 s wait → gap fill), snapshot on reset, 60 s `ChatSync` latest-only poll + status re-read, gap fill on reconnect |
| `l8chat-core-files.js` | `L8ChatFiles` | upload/download wrappers over `Layer8FileUpload` (no quick view, N8) |
| `l8chat-core-actions.js` | `L8ChatSession` | one signed-in chat: api + store + live, and the user's actions (open peer/conversation, send, read marks, groups, status, LLM profile) (DR32 #3) |
| `l8chat-bubble.js` | `L8ChatBubble` | `init({endpoints, tantoUserId?})`: validates that every required endpoint is present and throws naming the missing ones (FailFast); mounts button + panel on `document.body`, toggle/open/close |
| `l8chat-sidebar.js` | `L8ChatSidebar` | me + status picker, divider, 30-row peer list (Tanto pinned), user search, "+" group |
| `l8chat-status.js` | `L8ChatStatus` | status dot renderer + picker (online/away/offline/custom text+color) |
| `l8chat-convlist.js` | `L8ChatConvList` | slide-out conversation list, purple/grey dots, "+" new conversation |
| `l8chat-thread.js` | `L8ChatThread` | last 10, "load older" (top-left), markdown for Tanto, attachments, pending/sent state, Tanto profile selector + typing indicator |
| `l8chat-composer.js` | `L8ChatComposer` | textarea, send, attach |
| `l8chat-group-dialog.js` | `L8ChatGroupDialog` | `Layer8DPopup` dual list: name field, left = selected, right = users from the users endpoint + search |
| `l8chat-conv-dialog.js` | `L8ChatConvDialog` | name + create |
| `l8chat.css` | — | existing `--layer8d-*` tokens only, `l8chat-` prefix, `layer8d-btn*` buttons, `var(--layer8d-on-primary, white)` on primary backgrounds. Status colors: `--layer8d-success` (online), `--layer8d-text-medium` (offline), `--layer8d-warning` (away). The unread dot uses the existing `--layer8d-indigo-text` (purple; light `#3730a3`, dark `#a5b4fc`), so **no theme token is added** |
| `m/l8chat-m.js` | `L8ChatMobile` | full-screen drill-down: peers → conversations → thread (same core) |
| `m/l8chat-bubble-m.js` | `L8ChatBubbleMobile` | mobile bubble, same `init({endpoints, tantoUserId?})` contract |
| `m/l8chat-m.css` | — | mobile layout |

### 6.2 Sidebar (R3)
- **Header:** the current user's name with a status dot. Clicking the dot opens the picker.
- **Peer list:**
  - Tanto is always row 1, even before any conversation exists. It is a synthetic row from `tantoUserId`.
  - Next come the merged `ChatPeer` rows sorted by `lastMessageAt` descending.
  - Peer names come from the UI's user lookup (`L8ChatApi`), resolved by ID.
  - If there are fewer than 30, the list is padded alphabetically from the user lookup, so a new user can still start a chat.
  - **User search (Q3):** a search box under the divider queries the users endpoint by name (debounced, limit 20). The results replace the list while text is present.
    - Clicking a user opens the direct "General" conversation. The client computes its deterministic ID (§4.1) and POSTs `ChatConv`, which is idempotent.
    - Clearing the box restores the peer list.
  - Each row shows its status dot. Groups show a member count instead of a dot.

### 6.3 Conversation list (R4)
- The list comes from `ChatConversation` rows for the selected peer (a router fan-out GET), sorted by `lastMessageAt`.
- The dot is **purple** when `lastSeq > my lastReadSeq` and **grey** otherwise.
- Opening a conversation PATCHes `ChatConv` with my `lastReadSeq`, routed to the owner. The push then turns the dot grey.

### 6.4 Thread (R5)
- The initial page is 10 messages. A load-older button (top-left) fetches the next 10 by `seq<oldest`.
- Messages arriving through push append at the bottom. The view auto-scrolls only if it was already at the bottom.
- The sender's own messages show *pending* until their push arrives (§5.2 step 5).

### 6.5 Group dialog (R6)
- `Layer8DPopup.show({size:'large'})`. All DOM lookups are scoped to `Layer8DPopup.getBody()` (StackedPopupDomScoping).
- Layout: the name field on top; on the right, users from the user lookup with a search box; on the left, the selected users. Users can be clicked to highlight, then moved with →/← (or by double-click).
- **Create** POSTs `ChatGroup` to chat-users. That service creates the group's "General" conversation through `ChatConv` (§4.4), so the group shows up in the list right away.

### 6.6 New conversation (R7)
- The "+" button opens a popup with a name field. **Create** POSTs `ChatConv` with `{conversationId (new UUID), peerKind, peerKey/groupId, title}`.

### 6.7 Files (R8)
- The paperclip opens a picker, then uploads, then sends a message with `attachments[]`.
- Each attachment renders as a chip with Download. Quick view is not offered (N8).

### 6.8 Fail-fast and wiring
- No `typeof` guards around required globals. All new globals go into each adopter's `<project>-deps-check.js`.
- The bubble is mounted from the host app's `app.js` (desktop) and `app-core.js` (mobile), the same way `L8AgentBubble` is mounted today.
- **Endpoints come from the adopter (L8UINoProjectSpecificCode).** l8chat contains no service names, areas or prefixes, following the `L8AgentChat.init({chatEndpoint})` precedent. The adopter passes them all:
  ```js
  L8ChatBubble.init({ endpoints: {
      msg: '/81/ChatMsg', conv: '/81/ChatConv', peer: '/81/ChatPeer', sync: '/81/ChatSync',
      stat: '/81/ChatStat', group: '/81/ChatGroup', users: '/73/users', llmProf: '/79/LlmProf' } });
  ```
  A missing key throws at init. The paths are resolved through `Layer8DConfig.resolveEndpoint`, so the adopter's prefix is applied there.

---

## 7. AI Agent (`l8ai/go/ai/agent/`): new code, same capabilities as l8agent, with its gaps fixed

| Package | Responsibility | l8agent gap fixed |
|---|---|---|
| `llm/` | `Client` interface; `anthropic.go` (configurable URL+model, prompt caching on system+tools, retry/backoff on 429/5xx, per-provider token bucket), `openai.go` (OpenAI-compatible); factory from the `LlmProfile` values (kind → Anthropic or OpenAI-compatible client, base URL, API key, model); client cache keyed by those four values, so a changed profile builds a new client | model/URL hard-coded for Anthropic; no retry/caching |
| `catalog/` | model registry + service catalog (§7.3) | catalog was local-node only (probler workaround in `prob/agent/agent.go`) |
| `schema/` | Tier-1 compact catalog text, Tier-2 `describe_model` (introspector, never `Nodes(true,true)`) | — |
| `tools/` | `l8query`, `create_record`, `update_record`, `delete_record`, `list_modules`, `describe_model`; the `model` parameter carries an `enum` generated from the catalog | static list |
| `executor/` | resolves model→(service, area) via the catalog, runs `vnic.Request/LeaderRequest(..., requesterAaaId)`; query policy (no unbounded `select *`) | ran as the agent's own identity → no ScopeView; now **as the requesting user** |
| `masking/` | field classification with the **real model name** (from the catalog), deterministic tokens (§7.6) | received the tool name as model; history replayed unmasked |
| `convctx/` (not `context/`, which clashes with Go's `context`) | token-budgeted context builder + compaction chunking (assessment §3–4) | "last 20 messages" truncation only |
| `turn/` | `ChatAgent` service handler: receives `ChatAgentTurn`, runs the loop, posts the reply as Tanto, posts the compaction | — |
| `l8ai/go/ai/agent/activate.go` | `agent.ActivateAgent(cfg AgentConfig, vnic)` with `cfg.RegisterTypes func(ifs.IResources)`, `cfg.SystemPrompt`, `cfg.HelpDocs`, `cfg.WebToolsURL`, optional `cfg.ServiceMap` (N6), `cfg.MaskingOverrides`, `cfg.MaskingSecret` (same on every agent instance), `cfg.LlmRequestsPerMinute`, limits | — |

The l8agent origin/main prefixes `@help` (answer from HelpDocs) and `@web` (webtools) are carried over as optional `AgentConfig` features. Probler uses both today.

### 7.1 Tanto: a virtual chat participant, not a security user
- **Tanto is not a user on the system.** There is no Tanto user, password, credential or role in the security config, and the agent never calls `Authenticate`. No credentials exist that anyone could steal.
- **Identity is a constant in l8ai:** `l8ai.TantoUserId` with the display name `l8ai.TantoDisplayName = "Tanto (Layer8)"`. AGENT conversations list `TantoUserId` as a member.
- **UI:** the pinned row, the sender name and the online status come from these constants. Tanto is never looked up in the users endpoint.
- **How Tanto's replies are trusted:**
  1. The owner chat instance dispatches a `ChatAgentTurn` by Unicast. On the agent side, the dispatching instance's UUID is the message source.
  2. The agent sends its reply, a `ChatMessage` with `kind=AGENT`, `agent_turn_id` and `message_id = UUIDv5(turnId)`, by Unicast **directly to that chat instance's `ChatShMsg`**, not through the router. Compaction results (`PATCH ChatConv`, compare-and-set) go the same way to `ChatShCnv`.
  3. The chat instance accepts an AGENT message or compaction only if both checks pass:
     - the message source is a `ChatAgent` instance (`GetParticipants("ChatAgent", 81)`);
     - its `agent_turn_id` matches the conversation's current turn.
     It then stamps `senderId = TantoUserId`.
  4. Browser requests always reach the chat instance from the router, never from a `ChatAgent` instance. No user token can therefore produce a Tanto message.
- **Conversation moved mid-turn:** the old owner drops the reply (it no longer owns the conversation). The new owner hydrates `agent_state = RUNNING`, but it cannot re-dispatch the turn: the requester's aaaId was in memory on the old owner only. Its watchdog ends the turn with a SYSTEM message asking the user to send the message again (DR29 #3). The deterministic reply ID prevents duplicates.
- **Tools never run as Tanto.** They run under the requesting user's aaaId (§7.4).

### 7.2 LLM profile on every message
- The thread header (AGENT conversations only) shows a profile selector filled from a GET on the `llmProf` endpoint the adopter passes in (`/79/LlmProf` in probler). The selected profile is stored on `ChatConversation.llmProfileId` and stamped on each user message.
- The agent resolves it on every turn with `kind, baseURL, apiKey, model, err := Security().LlmProfile(id, res)`, the same way services read `Credential(...)`. An empty id falls back to the default profile. No l8secure type is used outside l8secure.

### 7.3 Model→service mapping (R14): the l8web way
1. **Models:** at startup `agent.CallRegisterModels(res, register)` runs the adopter's registration function. For probler that is the same `models.Register` the web server uses (passed in by the adopter, so l8ai imports no adopter code). This fills the Registry and Introspector exactly as l8web's does.
2. **Services → models:** l8web learns each service's endpoint types from the `ifs.WebService` multicast that every service sends on Activate (`WebService.Post` → `ws.DeSerialize`). The agent activates a small **descriptor listener** on `ifs.WebService`/area 0 that only indexes those descriptors (it does not serve HTTP). On start it multicasts `ifs.EndPoints` so peers re-send them, the same way l8web's `Activate` does. That gives an exact map `modelType → (serviceName, area, actions)`.
3. **Merge order:** web descriptors first, then any model the health records or local services name, then the adopter's optional `ServiceMap` over all. Only models registered on the agent node are offered. The chat services (area 81) are excluded; area 0 is not (probler serves `NetworkDevice` from `NCache`/0). The catalog refreshes every 30s and on every descriptor.

**Implemented and verified in Phase 10 (DR33):** the listener (`go/ai/agent/catalog/listener.go`) learned 77 probler models on KIND, e.g. `NetworkDevice → NCache/0`, `GpuDevice → GCache/2`, `L8PTarget → Targets/91`. The full probler e2e suite passed with it running next to the web server, so spike S1 holds. The health records alone cannot serve as the catalog: they carry each node's services and areas but no models (`HealthStats.go` publishes the service manager's list, which has no models).

### 7.4 Agent service
- `ChatAgent` (area 81) is stateless per turn, apart from a short-lived LRU of prepared contexts.
- It scales horizontally: K8s replicas plus `GetParticipants("ChatAgent",81)`.
- **Turn identity:** a `ChatAgentTurn` arrives by Unicast with the requester's aaaId in the header (F1). The agent service implements `IServiceMessageHandler` (F6), so the message reaches it as an argument.
  - At turn start the agent calls `Security().UserInfo(aaaId)`. If the session is gone, it ends the turn with a SYSTEM message ("your session expired").
  - Every tool call runs with that aaaId, so ScopeView and deny rules apply to the user.
  - If a tool call fails authorisation mid-turn, the turn ends with the same SYSTEM message rather than retrying.

### 7.5 Concurrency, context and compaction
Summarised here. The detail is in the assessment doc.
- **Concurrency:**
  - The owner chat instance is the single writer and allows at most one in-flight turn per conversation. Messages that arrive mid-turn set `pending`, and are answered together in the next turn.
  - Turns go to an agent instance chosen by a hash of `conversationId` over `GetParticipants("ChatAgent",81)`. That gives cache affinity for the warm prompt cache and context LRU.
  - Each turn has a deadline (default 5 min). On timeout it is re-dispatched to the next agent instance in hash order with the same `turnId`. The reply `messageId = UUIDv5(turnId)`, so duplicates are dropped.
  - After 2 failed attempts the chat instance posts a system message instead.
- **Context per turn:** system prompt + Tier-1 catalog (cached prefix) → `summary` (if any) → messages with `seq > summary_up_to_seq` → current user message. The whole thing is budgeted against the profile's context window.
- **Compaction:**
  - Triggered when (summary + tail) is over 60% of the budget, or the tail is over 40 messages.
  - It runs **after** the reply is posted, so the user never waits for it. The oldest chunk of the tail is summarised (stored raw, unmasked after the LLM call) and written by `PATCH ChatConv {summary, summaryUpToSeq, expectedPrevUpToSeq}`, which is compare-and-set on the owner instance.
  - Tool results are never replayed verbatim across turns. Only the assistant's final text is kept.

### 7.6 Masking
- Tokens are deterministic: `[NAME_<6 base32 of HMAC(secret, conversationId|value)>]`. The same value always gets the same token in a conversation, across turns and agent instances.
- Each turn re-masks the full context from raw text and builds the unmask map for that turn.
- Classification uses the real model name resolved by the catalog. Consumer overrides (`MaskingOverrides`) are kept.
- The money formatter uses cents and the currency field (fixes l8agent's TODO).

---

## 8. l8types and l8secure changes (F2–F4)

LLM providers and profiles follow the credentials pattern exactly:
- **Credentials today:** the `Creds` service lives in l8secure, is seeded from the security config JSON, and is maintained by the l8ui SYS screens over REST with plain JSON. Services read it only through `Security().Credential(name, key, res)`, which returns plain strings (l8agent: `_, baseURL, apiKey, model, err`; l8notify; l8collector; every ORM main). No project imports an l8secure type.
- **LLM:** the `LlmProv` and `LlmProf` services and **all their protos live in l8secure**. Services read a profile only through `Security().LlmProfile(id, res)`, which returns plain strings. l8types gets the interface method only, with no LLM types.

### 8.1 l8types
- `ifs.ISecurityProvider` gets two methods, both returning plain values like `Credential(...)`, so l8types gets no new proto:
  ```go
  // LlmProfile resolves an LLM profile. An empty id means the default profile.
  // Returns: kind, baseURL, apiKey, model, error
  LlmProfile(string, IResources) (string, string, string, string, error)

  // UserInfo returns the user behind a validated aaaId.
  // Returns: userId, fullName, error (unknown or expired aaaId)
  UserInfo(string, IVNic) (string, string, error)
  ```
  - `kind` is the provider kind name (`L8_LLM_PROVIDER_KIND_ANTHROPIC` or `L8_LLM_PROVIDER_KIND_OPENAI_COMPATIBLE`), which tells the agent which client to build.
- Every implementer gets both, found with `grep -rn "func (.*) NewSystemConfig()"` across the ecosystem. Any stub or no-op provider returns an error.
- **Rollout:** these two methods (l8types `c53a24a`) and the `Unicast` change (F1, l8types `3e9cd77`) are released. The ecosystem then re-vendors and rebuilds its security plugins once (Go plugin ABI rule). Projects that do not use chat need only that rebuild, with no code change.

### 8.2 l8secure
- New messages in l8secure's `proto/secure.proto` (package `secure`), generated by l8secure's own `make-bindings.sh`. A profile is one combination of URL + API key + model, as the request defines it:
  ```protobuf
  enum L8LlmProviderKind { L8_LLM_PROVIDER_KIND_UNSPECIFIED=0; L8_LLM_PROVIDER_KIND_ANTHROPIC=1; L8_LLM_PROVIDER_KIND_OPENAI_COMPATIBLE=2; }
  message L8LlmModel    { string model_id=1; string display_name=2; }                       // child of L8LlmProvider
  message L8LlmProvider { string provider_id=1; string name=2; L8LlmProviderKind kind=3; string base_url=4;
                          string api_key=5; repeated L8LlmModel models=6; string description=7; }
  message L8LlmProfile  { string profile_id=1; string name=2; string provider_id=3; string model_id=4;
                          bool is_default=5; string description=6; }
  message L8LlmProviderList { repeated L8LlmProvider list=1; l8api.L8MetaData metadata=2; }
  message L8LlmProfileList  { repeated L8LlmProfile list=1;  l8api.L8MetaData metadata=2; }
  ```
  - `is_default` marks the profile used when a message names none.
- `L8SecureConfig` gets `map<string, L8LlmProvider> llm_providers` and `map<string, L8LlmProfile> llm_profiles`, seeded from the config JSON the same way `credentials` is.
- `LlmProvService.go` and `LlmProfService.go` are copied from `PortalService.go`: BaseService, `SystemServiceGroup`, voter, non-transactional, `CacheStore` namespaces `"lp"`/`"lf"`, init items from config, and non-vnet nodes pull through a deferred GET (as Portals do). The service types are registered by the security plugin itself when the services activate, as for users, roles and portals, so adopters register nothing.
- The API key is an ordinary field of the provider record: stored and returned like any other field. Who can read or change providers and profiles is decided by the roles in the security config, the same as the existing `Creds` service.
- Validation:
  - A profile must reference an existing provider, and its model must be listed by that provider.
  - At most one profile has `is_default`.
  - A provider cannot be deleted while profiles reference it.
- `SecurityProvider.LlmProfile()` looks up the profile (or the default one) and its provider in the local services, and returns `kind, baseURL, apiKey, model`.
- `SecurityProvider.UserInfo()` is implemented inside l8secure on top of its own token and user stores. Only the user ID and full name leave.
- `plugin/probler/probler.json` gets:
  - one provider seeded from the existing `TestLLM` credential (aside = URL, yside = model, zside = key, provider kind OpenAI-compatible), and one default profile;
  - **no dedicated chat role, and the existing roles are unchanged** (DR26). The UI's user lookup selects only `userId` and `fullName`. How chat access and user lookup are scoped per role is deferred.

  The `probler-security` plugin is then rebuilt.
- The **Go plugin ABI rule** applies: rebuild the plugin and all host images with the same toolchain.

### 8.3 l8ui SYS tabs
Not in scope (N2). LLM providers and profiles are maintained through the security config JSON. The chat thread's profile selector reads the `llmProf` endpoint (`/79/LlmProf`), which needs no SYS change.

---

## 9. Phase plan

### Phase 0: Prerequisites, spikes and duplication audit
- **0.1 F1 (done):** `Unicast` with an optional aaaId in l8types (`3e9cd77`) + l8bus (`9f9c2eb`) + the l8utils test double (`387e332`), and l8ai re-vendored with it. Chat instances take identity from the aaaId of the Unicast, which reaches the handler through `IServiceMessageHandler` (F6, DR27) (DR18). l8bus builds against it; its runtime tests wait for the security plugin rebuild (`/var/loader.so` is stale since Phase 1).
- **0.2 Re-vendor (you):** bump l8utils (subscription fix `3e5a83f`) in probler and l8ai and re-vendor. No framework repo changes.
- **0.3 Spikes. Each is a small test in `l8ai/go/tests/` and changes no framework code.** They run before Phase 4 because any of them could change the design:
  - **S1** agent descriptor listener on `ifs.WebService` (§7.3). Fallback: `ServiceMap` (N6).
  - **S2** a registered query matches on a repeated child field (`members.userId`). Fallback: a flat `member_ids` field.
  - **S3** after F1, a Unicast with an aaaId arrives as `msg.AAAId()`, and `Security().CanDoAction`/`ScopeView` work with it on the receiving service.
  - **S4** `UserInfo` latency per call, which sets the cache TTL in §4.3.
- **0.4 Duplication audit result.**
  - `l8ui/l8agent` bubble and chat are **not** extended or copied. l8chat is new, and its behavioral code is shared between desktop and mobile through `l8chat-core-*`.
  - l8agent's Go code is ported by concept, not copied. No existing duplicated pattern in the touched code passes the `behavioral_lines × instances > 100` threshold, so no extraction refactor is required.
  - Backport note: once l8erp and l8alarms migrate, `l8ui/l8agent` is deleted (Q4, with your authorisation in the follow-up).

### Phase 1: l8types (F2, F3)
The two interface methods (plain return values, no new proto), then stubs in every implementer, then `go build ./...`. Released together with F1.

### Phase 2: l8secure (F4)
§8.2: the LLM messages in `secure.proto` + `make-bindings.sh`, `L8SecureConfig` maps, the LLM services, `LlmProfile`/`UserInfo`, the probler.json seed (provider, default profile), and the plugin rebuild (yours).

### Phase 3: (removed)
SYS LLM tabs are N2 and not in scope. The number is kept so later phase references stay stable.

### Phase 4: l8ai scaffold
- `.gitignore` gets an uncommented `go/vendor/` line.
- `proto/l8ai.proto` + `proto/make-bindings.sh` (docker `-i`, not `-it`). Bindings go to `go/types/l8ai`.
- Module `github.com/saichler/l8ai/go`.
- `go/ai/common/defaults.go` holds `ServiceArea=81`, the service names, `TantoUserId`, the deterministic-ID helpers (§4.1), ring/eviction/flush/timeout defaults and the config struct.
- `go/ai/common/types.go` holds `RegisterTypes(res)`, which every adopter calls from both its web and agent `models.Register`.

### Phase 5: Chat ORM
- `go/ai/orm/activate.go`: `ActivateOrm(creds, dbName, vnic)` builds 4 `NewOrmSLA` instances (`ChatMsgDb`, `ChatCnvDb`, `ChatGrpDb`, `ChatStatDb`) with `SetReplicationCount(1)`.
- Each gets a callback using `common.NewServiceCallback`: `GenerateID` on POST guarded by `if action == ifs.POST`, then validation.
  - `ChatMsgDb` is append-only: PUT is rejected, and PATCH is limited to `token_count`.

### Phase 6: Chat instances (implemented; not yet run, tests are Phase 10)
- **Prerequisite:** the l8types release with F1 (0.1), F2/F3 (Phase 1) and F6 (l8types + l8services) is vendored into l8ai (done). Spike S3 is skipped for now (your call); it is covered by the KIND tests.
- `go/ai/chat/` contains:
  - `ChatShMsg` and `ChatShCnv` as `IServiceMessageHandler` handlers (F6), with `UserInfo` identity and its cache;
  - ring, seq and dedupe;
  - read marks;
  - the agent turn state machine and watchdog;
  - header residency, lazy ring load and eviction;
  - batch header load from the router;
  - the write-behind aggregator;
  - graceful shutdown and release;
  - `notif_seq` per conversation (starts at 1 on ownership), retention of the last 10 notifications, and `ChatShSync` (gap fill, snapshot, latest only).
- `ActivateChat(cfg, vnic)` returns the `*Instance`; the adopter's `main` calls `Shutdown()` on SIGTERM. No business logic in `main`.
- `ChatConfig` gained `FlushMaxItems` (50) and `PeerListSize` (30).

### Phase 7: Chat router and chat-users (implemented; not yet run, tests are Phase 10)
- `go/ai/router/` contains:
  - the `ChatIdx` index (DCache), leader-only assignment and least-loaded selection;
  - cold-start assignment from `ChatCnvDb`;
  - reassignment on instance loss or release, using the protocol in §4.3 (assign = POST without aaaId; release = DELETE of the owner's index rows);
  - Unicast writes with aaaId;
  - bounded `Request` reads and the fan-out merge for `ChatPeer`/`ChatConversation`;
  - `ChatSync` routing: gap fill to the owner, and latest-only grouped by owner;
  - `ChatReg` registration record and replay;
  - fan-out of group member updates (`PATCH ChatConv` with `groupId`) by Unicast to every chat instance.
- `ActivateRouter(cfg, vnic)`. Public services expose web endpoints (`web.New(name, 81, 0)`, POST/PATCH/GET as in §5). All router services are in the `ChatRtr` service group, so the replicas elect one leader (DR30 #2).
- `go/ai/users/` contains:
  - `ChatStat` (DCache + `ChatStatDb` for custom status + leader-local heartbeat map and sweeper; a DCache write, and therefore a notification, happens only on a presence transition, §5.5);
  - `ChatGroup` (DCache + `ChatGrpDb`, creates the General conversation, propagates membership changes, creator-only edits);
- `ActivateUsers(cfg, vnic)`. Both services are in the `ChatUsr` service group and embed `base.BaseService`, adding the `IServiceMessageHandler` methods for identity (DR30 #2).
- Shared helpers (query criteria, the per-aaaId `UserInfo` cache, leader reads) live in `go/ai/common/` and are used by chat, router and chat-users.

### Phase 8: Agent (implemented; not yet run, tests are Phase 10)
- The §7 packages plus `ActivateAgent(cfg, vnic)`. `ChatAgent` is activated as stateful (DR30 #1).
- Chat instances accept a compaction `PATCH ChatConv` from a `ChatAgent` instance and apply it by compare-and-set on `expected_summary_up_to_seq` (DR31 #1–2).
- Default system prompt: L8Query rules, money in cents, mask tokens, and "you are Tanto (Layer8)".

### Phase 9: l8ui l8chat (desktop + mobile, new files only) (implemented; not yet run in a browser)
- The deterministic conversation IDs computed in the browser were checked against the Go ones; the §5.7 ordering rules were checked in node.
- §6.
- **Script loading** in adopter shells: add a block `<!-- JS: Chat (l8chat) -->` **after** the websocket, popup, file-upload and markdown files.
  - Desktop: placed where the AI Agent block was (the end of the JS list).
  - Mobile: after "AI agent mobile" and before `app-core.js`.
- The l8erp loading-order rules and the l8book guide lines are not edited (N7).

### Phase 10: Probler integration tests on KIND (done; replaces the in-process topology below)
**Your direction:** test through probler as the early adopter: update `probler.json` in l8secure, rebuild the probler security images with l8secure `build-images.sh`, integrate l8ai into probler (most of Phase 11), add the e2e tests to probler's KIND e2e suite and test with probler's KIND setup.
- **l8secure `probler.json`:** `chatdb` postgres credential (copy of `agentdb`) and a second writing user `operator2` ("Second Operator", role `operator`) for the two-user chat specs (`viewer` is read-only).
- **probler e2e:**
  - `pages/Chat.ts`, `tests/desktop/28-chat.spec.ts` (8 tests: bubble on every screen with Tanto pinned, two-user direct message pending → sent and live unread → read, 10 + load older, "+" dialog, group dual list, presence seen by the other user, Tanto answers with the real device count, non-member refused) and `tests/mobile/07-chat.spec.ts` (2 tests).
  - Seeded sessions also set `sessionStorage.currentUser`, as the real login does; the inventory was regenerated without AIA.
- **Result:** the full probler e2e suite passes on KIND, 271 tests, Tanto included (it answered "19", the `NCache` count, through the test LLM).
- **Bugs found and fixed** (DR33): F8, F9, service discovery through the health records, typed-nil query nodes, `PlainQuery` dropping the text, the shared ORM service group, the router cold-start retry, a panicking turn ending with a SYSTEM message, LAN/public LLM names on KIND, and the model catalog (descriptor listener + `CallRegisterModels`).
- The in-process cases below were not built; the KIND specs cover the user-visible ones. The remaining cases (failover, ordering under load, gap fill/snapshot, membership propagation, presence sweeper, agent timeouts and compaction CAS, masking determinism) are still open.

#### Original Phase 10 (in-process topology, not built)
- In-process multi-node topology: 1 vnet, 2 routers, 3 chat instances, 1 chat-users, 1 orm (test Postgres), 2 agents with a stub LLM `httptest` server, and a test `ISecurityProvider` that implements `UserInfo`.
- Cases:
  - Ordering under 50 concurrent senders to one conversation (contiguous seqs).
  - Idempotent resend by `messageId`, including a resend after the owner was killed.
  - Leader-only assignment: two routers, one unknown conversation, exactly one owner.
  - Paging 10 + older (ring and ORM fallback).
  - Instance kill → reassignment and hydration with no duplicate seq, plus thread and fan-out registrations replayed to the new owner (a push still arrives).
  - Sidebar fan-out merge, `partial` flag when an instance does not answer, unread set maintenance.
  - Notification sequence (§5.7):
    - gap fill from the retained 10;
    - snapshot when the gap is older than the retained 10;
    - after an owner kill, the new owner starts at 1 and a client holding a higher `last` resets and snapshots;
    - a latest-only query returning a value lower than the client's `last` is treated as a reset;
    - a message lost in the crash window disappears from the client after the snapshot.
  - Membership enforcement: a non-member cannot post or read; `senderId` cannot be spoofed.
  - Group membership propagation: add and remove a member of a group that has conversations on different chat instances. The added member sees all of them via push. The removed member stops receiving pushes and is rejected on read/post. A non-creator cannot change membership.
  - Presence sweeper and custom status reload; repeated heartbeats with unchanged presence produce no DCache write and no notification, a transition produces exactly one; after a chat-users leader switch nobody is marked offline before one full 150s window.
  - Agent:
    - one in-flight turn with pending coalescing;
    - timeout re-dispatch with no duplicate reply;
    - compaction CAS;
    - tool execution scoped by requester (a user denied a row cannot read it through Tanto);
    - Tanto trust: an AGENT message arriving through the router (from a browser) is rejected; a reply from a `ChatAgent` instance with a stale `agent_turn_id` is dropped; a reply sent to an instance that lost ownership is dropped, and the new owner's watchdog re-dispatches;
    - expired requester session ends the turn with a SYSTEM message;
    - masking determinism across two agent instances;
    - the model→service catalog contains a model served only on a remote node.
  - LLM: profile resolution (explicit and default profile), validation (unknown provider/model, second default, deleting a referenced provider).

### Phase 11: Probler adoption (R16) (done for KIND and all manifests in Phase 10, except the items marked open)
**Status:**
- **Done:** l8agent and AIA removed; the 5 chat binaries with Dockerfiles and `build.sh`; `go/build-all-images.sh`, `k8s/build-images.sh`, `kind-start.sh` (image load, readiness wait, LAN/public `hostAliases` detection); the chat workloads in all 5 manifests (kind and sharon-k8s/baremetal StatefulSets, local and gke Deployments + a chat-orm StatefulSet; the fused `agentdb)apiVersion` lines in local and gke are gone); web shells, `probler-chat-config.js` with the 8 endpoints, deps-check, `models.Register` → `l8aicommon.RegisterTypes`; the e2e specs.
- **Open:** mock seeding of chat users, groups and conversations (`seed-mocks.sh`), the shared `/data/l8files` volume for the web DaemonSet (Risk A1), the remaining e2e specs (attachments, gap fill with a dropped push, websocket reconnect). `k8s/deploy.sh` and `un-deploy.sh` apply per-service files that no longer exist and were left as they are.


**Remove l8agent from probler** (touch points from the exploration):
- `go/go.mod:6`, `go.sum` entries, `vendor/` (you re-vendor).
- `go/prob/agent/` (whole directory).
- `common/models/models.go`: the l8agent import + 4 `RegisterType` calls. Replace them with `l8aicommon.RegisterTypes(res)`.
- `newui/main.go` `SetServiceTimeout("AgntChat",…)`. Replace it with `ChatMsg`.
- **Desktop web:** `app.html` (l8agent CSS/JS, `aia/*`, nav link `data-section="aia"`), `js/app.js` (`L8AgentBubble.init`, `nsMap 'aia'`), `js/sections.js` (aia mapping + `initializeAia`), `sections/aia.html`, `aia/` directory.
- **Mobile web:** `m/app.html` (l8agent CSS/JS, aia scripts), `m/js/app-core.js` (bubble init), `m/js/aia/*`, and the aia entries in `m/nav-configs/probler-nav-config{-base,,-system,-icons}.js`.
- **K8s:** the `probler-agent` StatefulSet in all 5 yamls. In **gke and local** its comment line is fused with `apiVersion` (a broken manifest), and removing it fixes that too.
- Plans/docs that mention l8agent: `plans/l8agent-ai-bubble.md` is marked superseded.

**Add:**
- **Binaries.** Each has a `main.go` that only creates resources and the vnic and calls `Activate`, plus `build.sh` and a `Dockerfile` copied from probler's:
  - `go/prob/chat-router/`, base `probler-security`
  - `go/prob/chat/` (chat instances), base `probler-security`
  - `go/prob/chat-users/`, base `probler-security`
  - `go/prob/chat-orm/`, base `probler-postgres`, own DB `chatdb`
  - `go/prob/chat-agent/`, base `probler-security`, `RegisterTypes: models.Register`
- `go/build-all-images.sh` gets 5 entries. `k8s/build-images.sh` as well.
- **K8s, 4 modes + sharon-k8s:**

  | Workload | local | baremetal/kind | gke |
  |---|---|---|---|
  | chat-router | Deployment ×2 | StatefulSet + anti-affinity + volumeClaimTemplates | Deployment, shared PVC |
  | chat (instances) | Deployment ×3 | StatefulSet + anti-affinity + volumeClaimTemplates | Deployment, shared PVC |
  | chat-users | Deployment ×2 | StatefulSet + anti-affinity + volumeClaimTemplates | Deployment, shared PVC |
  | chat-agent | Deployment ×2, hostAliases carried over from probler-agent | StatefulSet + anti-affinity + volumeClaimTemplates | Deployment, shared PVC |
  | chat-orm | StatefulSet ×1 (`hdata` volume) | StatefulSet (`hdata` volume) | StatefulSet, shared PVC |

  Every workload carries `NODE_IP` env and `labels: {app: …}`, and `terminationGracePeriodSeconds: 30` on chat instances. A shared `/data/l8files` volume is added for the web DaemonSet (Risk A1).
- `k8s/deploy.sh` / `un-deploy.sh` order: vnet → orm(s) → chat-orm → chat → chat-router → chat-users → chat-agent → web. `kind-start.sh` loads the new images.
- `models.Register` calls `l8aicommon.RegisterTypes(res)`. The LLM types are not registered by probler: the security plugin registers them, and probler never imports l8secure.
- **Web:**
  - `app.html`: `l8ui/l8chat/l8chat.css` + the l8chat JS block.
  - `js/app.js`: `L8ChatBubble.init({ endpoints: {...} })` with the 8 endpoints listed in §6.8.
  - `m/app.html`: `m/l8chat-m.css` + the JS block.
  - `m/js/app-core.js`: `L8ChatBubbleMobile.init({ endpoints: {...} })`, the same 8 endpoints.
  - `probler-deps-check.js` (desktop + mobile) gets all `L8Chat*` globals.
  - `newui/main.go`: `SetServiceTimeout("ChatMsg", …)` (adopter configuration).
- **Mock seeding** (`go/tests/mocks` / `seed-mocks.sh`). The mocks are an external client using the web REST API as admin, the same way probler mocks create other data today:
  - About 50 users. The test target can scale to 20K via a flag.
  - 5 groups.
  - Conversations with messages through `/81/ChatMsg`, including Tanto conversations against the stub or test LLM.
- **e2e specs** (desktop + mobile, no fixed sleeps): bubble open/close; Tanto pinned; peer ordering after a new message; unread purple → grey; load older; group create through the dual list; new conversation; attachment upload, quick-view and download; status change propagating to a second browser context; Tanto reply rendered as markdown; a message stays *pending* then turns *sent*; a push dropped on purpose (test hook in the stub) is recovered by gap fill within about 1 s; with the websocket blocked and then restored, the thread converges by gap fill on reconnect.

### Phase 12: Final verification
- KIND (`kind-start.sh` → `seed-mocks.sh` → `cd e2e && npm test`, desktop + mobile).
- **Smoke-test every affected section** end-to-end: the chat bubble on both shells; the thread profile selector; the removed AIA section is gone with no console errors; other probler sections are unaffected.
- **Failover test on KIND:** delete a chat instance pod while two browsers chat. Expect no duplicate seq, pending messages delivered by resend, and live updates continuing.
- **Load test:** `go/tests/loadsim` (l8ai). It simulates 20K logged-in clients (6K online, 100 msg/s, 300 concurrent Tanto conversations against the stub LLM). It records p50/p99 send→push latency, chat instance memory, ORM lag and agent queue depth against the targets in the assessment.
- Walk this plan section by section (VerifyPrdCompletenessBeforeDone).

---

## 10. Traceability matrix

| # | Section | Gap / Action Item | Platform | Phase |
|---|---|---|---|---|
| 1 | §2.1 F1 | `Unicast` with optional aaaId (l8types + l8bus + l8utils test double), done | Backend | 0.1 |
| 1a | §2.1 F6 | `IServiceMessageHandler` (l8types) + dispatch in `ServiceManager.handle()` (l8services), done | Backend | 6 |
| 2 | §2.1 | l8utils subscription fix re-vendored in probler and l8ai, by you | Backend | 0.2 |
| 3 | Spikes | S1 descriptor listener, S2 child-field registration, S3 Unicast aaaId security, S4 UserInfo latency | Backend | 0.3 |
| 4 | R12 / F2 | `ISecurityProvider.LlmProfile` (plain values, no proto) | Backend | 1 |
| 5 | F3 | `ISecurityProvider.UserInfo` | Backend | 1 |
| 6 | R12 / F4 | LlmProv/LlmProf services, validation | Backend | 2 |
| 7 | F4 | `UserInfo` implementation; no dedicated chat role (DR26) | Backend | 2 |
| 8 | R1 | Tanto is an l8ai constant (no security user), replies trusted by `ChatAgent` source + turn ID | Backend | 6 / 8 |
| 9 | — | l8ai scaffold, protos, RegisterTypes, deterministic IDs | Backend | 4 |
| 10 | R11 | Chat ORM (4 services), replication 1, append-only messages, one instance (N1) | Backend | 5 |
| 11 | R10 | Chat instances: identity via UserInfo, single writer, seq, ring, header residency, write-behind, graceful release | Backend | 6 |
| 12 | R10 | Router: index only, leader-only assignment, Unicast writes, bounded reads, fan-out merge, registration replay | Backend | 7 |
| 13 | R6/R9 | chat-users: presence (heartbeat/away/custom), groups | Backend | 7 |
| 13a | #4 | Group membership changes propagated to every group conversation (router fan-out by Unicast), creator-only | Backend | 7 |
| 13b | R9 | Notification sequence: `notif_seq`, retain last 10, `ChatSync` gap fill / snapshot / latest only | Backend | 6 / 7 |
| 13c | R9 | UI: gap detection, 1 s reorder wait, reset on 1/0 or lower poll value with snapshot, 60 s poll + status re-read, gap fill on reconnect | Desktop+Mobile | 9 |
| 14 | R13–R15 | Agent: Tanto as an l8ai constant with replies trusted by `ChatAgent` source + turn ID, llm, catalog, tools, executor as requester, session check, masking, context, compaction, turn | Backend | 8 |
| 15 | R2–R9 | l8chat bubble, sidebar, convlist, thread, composer, dialogs, files, pending/resend | Desktop | 9 |
| 16 | R2–R9 | l8chat mobile drill-down + bubble | Mobile | 9 |
| 17 | Tests | l8ai `go/tests` incl. concurrency, failover, replay, membership, agent | Backend | 10 |
| 18 | R16 | Remove l8agent from probler (Go, web, mobile, k8s, nav) | All | 11 |
| 19 | R16 | 5 binaries, Dockerfiles, build scripts, 4-mode k8s, deploy order | K8s | 11 |
| 20 | R16 | probler app.html / m/app.html / deps-check / bubble init | Desktop+Mobile | 11 |
| 21 | R16 | Mock seeding + e2e specs | Desktop+Mobile | 11 |
| 22 | Files | Shared `/data/l8files` for web instances (A1) | K8s | 11 |
| 23 | Verify | KIND + Playwright + smoke + failover + 20K load test | All | 12 |
| 24 | Q2 | Graceful-shutdown flush + release; memory guard; `recover()` in handlers; client resend | Backend+UI | 6 / 9 |
| 25 | Q3 | `ChatStatDb` persistence of custom status + reload on chat-users start | Backend | 5 / 7 |
| 26 | Q3 | Sidebar user search (UI user lookup) + open DM by deterministic ID | Desktop | 9 |
| 27 | Q3 | User search on the mobile peer list | Mobile | 9 |
| 28 | Q4 | Follow-up plan: migrate l8erp + l8alarms, then retire l8agent | All | follow-up |
| 29 | §2.1 | N1–N7 not changed; each default implemented as stated | All | — |

---

## 11. L8UI Includes Audit (probler adopter shells)

Method: every `l8ui/*.css|js` path listed in `l8erp/rules/desktop-script-loading-order.md` (122 files) and `mobile-script-loading-order.md` (58 files) was diffed against `probler/go/prob/newui/web/app.html` (162 l8ui refs) and `m/app.html` (133).
- **Result: every canonical file is already included in both shells.** None are missing, so nothing is N/A.
- Probler additionally includes l8events/l8notify UI, websocket, router, theme-switcher and export files. That is correct and they stay.

Changes from this plan:

| File | Desktop | Mobile | Action |
|---|---|---|---|
| `l8agent/l8agent-chat.css`, `l8agent-bubble.css` | included | — | **remove** (Phase 11) |
| `l8agent/l8agent-{enums,columns,forms,chat,bubble}.js` | included | enums/columns/forms included | **remove** |
| `l8agent/m/l8agent-{chat,bubble}-m.{css,js}` | — | included | **remove** |
| `shared/layer8-markdown.{css,js}` | included | included | **keep** (Tanto replies) |
| `shared/layer8d-websocket.js`, `popup/*`, `shared/layer8-file-upload.*` | included | included (mobile equivalents) | keep (required by l8chat) |
| `l8chat/l8chat.css` + 12 `l8chat/*.js` | **add** | — | Phase 11 |
| `l8chat/m/l8chat-m.css` + `l8chat-core-*.js` + `m/l8chat-m.js`, `m/l8chat-bubble-m.js` | — | **add** | Phase 11 |

---

## 12. Local Development Setup (LocalRunViaKind)
1. Build the images with `go/build-all-images.sh`, which includes the 5 chat images.
2. `k8s/kind-start.sh` creates the cluster, loads the images, deploys in phases and runs `k8s/seed-mocks.sh` (users, groups, conversations).
3. `cd e2e && npm test` runs the desktop and mobile specs. During iteration you can use `-g "chat /"`.
4. Fix loop: rebuild the image → `kind load docker-image` → `kubectl delete pod` → `kubectl wait`.
5. `k8s/kind-stop.sh`.

---

## 13. Compliance checklist

Every section of `l8book/ai/layer-8-guide-lines.md` is listed, as PlanRequirements requires ("check ALL applicable rules, not cherry-pick"). Rules that do not apply say why.

**Plan process and structure**

| Rule | How this plan complies |
|---|---|
| PlanRequirements | Written to `./plans/`; implementation only after your explicit approval. Phase 0.4 duplication audit. Traceability matrix with a Platform column (§10). Final verification phase (Phase 12). This checklist covers every rule |
| NeverActOnQuestions | Questions are answered without editing; the plan changes only on your explicit instruction |
| PrdCompliance | Compliance checklist (this section). Layout follows l8erp (`go/ai/<sub>/`, `go/types/l8ai`, `proto/`). Mock data, deployment, KIND and e2e are covered in Phase 11. login.json and ModConfig are unchanged because probler's existing web shell is reused |
| CanonicalProjectSelection | §2: l8erp for service/callback structure (ERP-style CRUD); probler for binaries, Dockerfiles and K8s because it is the adopter |
| Layer8ProjectLocations | Sibling projects referenced as `../<project>` |
| FrameworkInterfaceBoundaries | The rule forbids adding to `l8types/go/ifs` for feature work and says to flag it to the framework owner. F1 (`Unicast` aaaId), F2/F3 (`LlmProfile`, `UserInfo`) and F6 (`IServiceMessageHandler`, with its dispatch in l8services) do add to `ifs`; **each was explicitly authorised by you, the framework owner** (§2.1, D2, DR8, DR9, DR27). Nothing else in the framework changes; other gaps are N1–N7 with no-change defaults |
| ReportInfraBugs | Framework gaps found during planning are reported as N1–N7 (§2.1), not worked around |
| FollowInstructionsVerifyUserIssue | Applies to bug reports during implementation; no bug report is in scope |
| VerifyPrdCompletenessBeforeDone | Phase 12 walks the plan section by section and traces data end to end |

**Data model and services**

| Rule | How this plan complies |
|---|---|
| PrimeObjectReferences Rules 1–2 | Cross-references by string ID only; `ChatMember`/`ChatAttachment` embedded. **D1 (approved):** `ChatMessage` is an unbounded child collection with its own service |
| PrimeObjectReferences Rule 3 | **D3 (approved):** `ChatConversation` and `ChatGroup` are served by the l8chat bubble, with no module nav/columns/forms/reference registry entries |
| PrimeObjectReferences Rule 4 | Every PK is a string UUID; deterministic conversation IDs are UUIDv5 strings, kept by `GenerateID` because they are preset. No ID is ever converted to a number |
| ProtobufRules | `_UNSPECIFIED=0` on every enum, `XList{list=1, metadata=2}`, one `make-bindings.sh` with `docker -i`, protobuf type names (not ServiceNames) in L8Query and JS |
| Maintainability | ServiceName ≤10 chars; one area per module (81 for l8ai, 79 for the two l8secure LLM services); `GenerateID` on POST guarded by `action == ifs.POST`; files <500 lines; types registered via `RegisterTypes` from the adopter's web `main.go`; Second Instance Rule via shared `l8chat-core-*` |
| NoGoGenerics | None |
| MainPackageMinimal | Every `main.go` only creates resources/vnic and calls `Activate*` |
| SingleOwnerDatabaseTable | The 4 chat ORM services are activated only in chat-orm; everything else uses vnic |
| SystemServicesRequired | New binaries never activate Events/Notify; probler's existing `system.Activate` call stays the only one |
| IntrospectorNodesParams | The agent's model catalog uses `Nodes(false, true)` for root types, never `Nodes(true, true)` |
| ImmutabilityUiAlignment | `ChatMsgDb` rejects PUT (append-only); l8chat offers no edit or delete control for messages |

**Security**

| Rule | How this plan complies |
|---|---|
| SecurityRules | All service-side identity and LLM access goes through `ISecurityProvider` (`UserInfo`, `LlmProfile`). No l8ai/probler code imports l8secure or calls its services. Only the browser looks up users, with its own token. Roles come from the config JSON. Tanto is not a security user. Access to LLM providers/profiles is controlled by roles in the config JSON |
| SecurityConfigStructure / AssociateIdsScopeView | `probler.json` gets the LLM provider and default profile in the config JSON. Roles are unchanged and there is no dedicated chat role (DR26); per-role scoping of chat access and user lookup is deferred |
| LoginableEntityUserProvisioning | N/A: no new loginable entity; chat users are existing security users |

**UI (l8chat and the probler shells)**

| Rule | How this plan complies |
|---|---|
| L8UINoProjectSpecificCode | l8chat has no project names, prefixes, service names or areas. Every endpoint is passed by the adopter in `init({endpoints})` (§6.8), following the `L8AgentChat.init({chatEndpoint})` precedent |
| L8UIThemeCompliance | Existing `--layer8d-*` tokens only (unread = `--layer8d-indigo-text`); no dark-mode blocks; `layer8d-btn*`; `var(--layer8d-on-primary, white)` on primary backgrounds |
| FailFastNoSilentFallback | No `typeof` guards; missing init endpoints throw; failed required fetches throw; `probler-deps-check.js` (both shells) lists every `L8Chat*` global; partial fan-out results are flagged, not hidden |
| ReportInfraBugs (UI) | Unresolvable IDs log `console.error`, never a silent empty default |
| L8QueryRules | Every GET carries `?body=` with an L8Query; page 0 for counts and paging |
| Layer8DTablePaginationMetadata | Counts are read only from page 0 responses |
| JsProtobufFieldNames | Every JS field name is taken from the `.pb.go` `json=` tag |
| DateFieldRenderingPipeline | int64 fields (`seq`, `notif_seq`, `last_read_seq`, timestamps) arrive as JSON strings. l8chat coerces them with `Number()` before comparing or formatting, so §5.7 gap detection never compares strings (`"10" < "9"`). These are counters and times, not IDs, so PrimeObjectReferences Rule 4 is not affected |
| StackedPopupDomScoping | Dialogs scope DOM lookups to `Layer8DPopup.getBody()` (desktop) or the passed `popup.body` (mobile) |
| FileUploadPattern | `FileStore` + `Layer8FileUpload`; `ChatAttachment` stores `storage_path` only; no file I/O in callbacks |
| MobileRules / Platform completeness | Every l8chat feature has a mobile counterpart sharing `l8chat-core-*` (matrix rows 15–16, 20–21, 26–27) |
| PrdL8uiIncludesAudit | §11 audits every canonical desktop and mobile file against probler's shells |
| ScriptLoadingOrder / VerifyAppHtmlScriptsAgainstLoadingOrder | Phase 9 places the l8chat block after websocket, popup, file-upload and markdown in both shells; Phase 12 re-verifies both `app.html` files against the loading order |
| L8AgentChat | Removed from probler (Phase 11): scripts, CSS, init and the agent binary. `l8ui/l8agent` itself stays until the follow-up plan |
| L8UICopyToNewProject | N/A: probler already has the l8ui submodule |
| AppHtmlBodyFromL8erp / IndexHtmlRedirect / LoginJsonAdaptation / SetupConfiguration / ModconfigFailureNoLogout / PortalsSameWebServer / RegistrationPage | N/A: probler's existing web shell, login and single web server are reused unchanged apart from the l8chat block and init |
| AddingModule / ModuleInitSectionSelector / ReferenceRegistryCompleteness / ReuseExistingModuleForms | N/A by D3: l8chat is a bubble, not a module, and defines no forms with `lookupModel` |
| SingleDisplayFormatterRule / CompoundFormFieldDataCollection / MoneyFieldTypeMapping / InlinePopupRenderingParity / CascadingHideZeroChildren / TemplateLiteralTernaryEdits / DataCompletenessPipeline Stage 1 | N/A: l8chat uses no l8ui form fields, money fields, inline tables or nav filtering. Template literals in l8chat are checked with `node -c` |
| Layer8DApiReference / Layer8MApiReference / SharedComponentsReference | l8chat uses only the documented `Layer8D*`/`Layer8M*` APIs (`Layer8DConfig`, `Layer8DPopup`, `Layer8DWebSocket`, `Layer8FileUpload`, `Layer8MAuth`, `Layer8DNotification`) |
| Layer8CsvExport / DataImportSystem / L8Logs | N/A: not used by chat |
| PlatformConversionDataFlow | N/A: no platform conversion; desktop and mobile are built together |

**Deployment, testing and repository**

| Rule | How this plan complies |
|---|---|
| DeploymentArtifacts | 5 new binaries, each with `build.sh` + multi-stage `Dockerfile` on probler's own `probler-security` base; `build-all-images.sh`, `deploy.sh`/`un-deploy.sh` updated |
| K8sRules | All 4 modes (local, baremetal, gke, kind) + KIND scripts, `hdata` volume, `NODE_IP` env, labels |
| L8PollarisBinaryDeployment | N/A: chat is outside the collection/parsing/orm chain |
| LogServicesRequired | probler already has `log-vnet` and `log-agent` |
| MockDataRules / DataCompletenessPipeline Stage 3 | Mock generators in probler's `go/tests/mocks/` for groups, conversations and messages, phased after users; endpoints use the exact ServiceName constants |
| TestDataFieldVerification | Every mock and test map key is checked against the `.pb.go` JSON name |
| TestLocationAndApproach | All tests and spikes in `go/tests/`, system API end to end |
| CleanupTestBinaries | Builds use `go build ./...` or `-o /dev/null`, never a bare main-package build |
| LocalRunViaKind | §12: `kind-start.sh` → `seed-mocks.sh` → `npm test` → `kind-stop.sh` |
| PostImplementationE2ETesting | Phase 11 e2e (desktop + mobile, no fixed sleeps, `lint:sleep`) and Phase 12 KIND verification |
| VendorAndGit | No vendor, `go mod` or git commands by me; `go/vendor/` uncommented in `.gitignore` |

---

## 14. Decisions log
| # | Question | Decision | Plan impact |
|---|---|---|---|
| Q1 | D1 separate `ChatMessage` service; D2 `ISecurityProvider.LlmProfile` | **Approved** | §5.1, §8.1 |
| Q2 | Write-behind with a ~1 s loss window on hard crash | **Accepted** | §5.4: graceful-shutdown flush so planned restarts lose nothing; client resend for pending messages |
| Q3 | Persist custom status; directory search for starting DMs | **Yes to both** | `ChatStatDb` (§4.4, §5.5, Phases 5/7); sidebar search (§6.2, Phase 9) |
| Q4 | Follow-up migration of l8erp/l8alarms | **Yes** | `plans/l8ai-followup-l8erp-l8alarms-migration.md` |
| Q5 | Areas 79 (LLM, l8secure) and 81 (l8ai) | **Agreed** | §3 |
| DR1 | The router only indexes conversationId → chat instance and Unicasts | **Yes** | §4.1. Revision 2's user shards, inbox rows, router-hosted presence/groups/directory and router identity checks are removed |
| DR2 | New conversations: the router picks a chat instance and records it | **Yes** (least loaded, leader only) | §4.1 |
| DR3 | Sidebar from a fan-out to all chat instances, merged | **Agreed** | §4.2, §5.5 |
| DR4 | Presence and groups as separate services | **Yes** | §4.4 (chat-users) |
| DR5 | Router writes use Unicast, not Request (Request can block and get stuck) | **Yes** | §4.1, §5.2: acknowledgement by websocket push, client resend by `messageId` |
| DR6 | No framework change unless explicitly authorised | **Rule** | §2.1: F1–F5 authorised, N1–N7 left unchanged with defaults |
| DR7 | Services access security only via `ISecurityProvider`, never l8secure services directly | **Rule** | §4.3, §4.4, §7.1, §8 |
| DR8 | `Unicast` carries an optional aaaId | **F1, implemented by me at your request** (l8types `3e9cd77`, l8bus `9f9c2eb`, l8utils `387e332`) | §4.1, §5.2, §7.4; the payload envelope is dropped |
| DR9 | `ISecurityProvider.UserInfo`, plain return values, no new proto | **Authorised (simpler version)** | §8.1, F3 |
| DR10 | User names and search: the UI looks up users itself | **Yes**: `ChatDir` and `ISecurityProvider.Users` dropped | §4.4, §6.1–6.5, §8.2 (no dedicated chat role, DR26) |
| DR11 | Concern #4: group membership changes must reach every group conversation | **Agreed** | §4.4, Phase 7, Phase 10, matrix row 13a |
| DR12 | UI consistency: per-conversation notification sequence; gap → wait 1 s → request missing; server keeps the last 10; seq 1/0 resets the UI; 60 s poll of the latest seq; gap fill on reconnect; presence re-read by the poll. Accepted suggestions: a poll value lower than the UI's is also a reset, and every reset fetches a snapshot | **Agreed** | §5.7, §6.1, Phases 6/7/9/10/11, matrix rows 13b–13c |
| DR13 | Concern #6: Tanto credential exposure | **Not applicable: Tanto is not a system user** | §7.1 rewritten: Tanto is an l8ai constant; replies trusted by `ChatAgent` source + current turn ID; Tanto user, `TantoCreds` and `chat-agent` role removed from the seed |
| DR14 | Concern #7: prompt injection driving write tools | **Deferred** to the future AI agent / LLM security work; out of scope for this plan | §7 tools unchanged |
| DR15 | Concern #8: live-update matching cost | **Dropped**: the mechanism already works at scale in another project | Spike S2 only checks child-field matching |
| DR16 | Concern #3b: every router replica restarts at once, the `ChatIdx` index comes back empty, and cold start reassigns conversations that running chat instances still own | **Won't happen**: routers restart rolling-only, so at least one replica always keeps the `ChatIdx` copy and cold start never meets running owners | §4.1, §4.3 unchanged |
| DR17 | Concern #13: presence pushed on every heartbeat (~330/s) | **Agreed**: notify only on transitions; heartbeats update a leader-local map only | §5.5, Phase 7, Phase 10 |
| DR18 | Concern #11: Phase 6 relies on `msg.AAAId()` arriving through Unicast | **Added**: F1 ships in the Phase 1 l8types release; Phase 6 is blocked until it is vendored | §9 Phase 0.1, Phase 6 |
| DR19 | Compliance #1: l8chat hardcoded `/81/...` and `/79/LlmProf`, which L8UINoProjectSpecificCode forbids in l8ui | **Yes**: the adopter passes every endpoint in `init({endpoints})`; missing keys throw | §6.1, §6.8, §7.2, §8.3, Phase 11, §13 |
| DR20 | Compliance #2: §13 cherry-picked rules | **Yes**: §13 now lists every guideline section, with N/A reasons | §13 |
| DR21 | Compliance #3: chat Prime Objects have no module CRUD screens | **D3 approved** | §5.1, §13 |
| DR22 | Compliance #5: F1–F3 add to `l8types/go/ifs`, which FrameworkInterfaceBoundaries reserves for the framework owner | **Yes**: §13 states each was authorised by you as framework owner | §13 |
| DR23 | Concern #12: limiting the user lookup to the requester's "customer". This was my assumption, not your requirement | **Removed**: `UserInfo` returns `(userId, fullName, error)` with no customer; the `chat-user` role sees only `userId` and `fullName` of every user, with no row restriction. Concern #12 closed | §4.3, §8.1, §8.2, §10 row 7, §13 |
| DR24 | "API key write-only, blanked on every GET". This was my assumption, not your requirement (the request only says providers hold URL, API keys and models) | **Removed**: the key is an ordinary field; access is by roles in the security config | §8.1, §8.2, §10 row 6, Phase 10, §13 |
| DR25 | LLM protos were placed in l8types (`l8llm.proto`) so `LlmProfile` could return `L8LlmResolvedProfile`. That contradicted your requirement that providers and profiles are part of l8secure | **Changed**: all LLM protos live in l8secure `secure.proto`; `LlmProfile` returns `kind, baseURL, apiKey, model, error` like `Credential`; `L8LlmResolvedProfile` is deleted. Profile fields I had added beyond URL + API key + model (`max_output_tokens`, `temperature`, `version`, model `context_window`/`max_output_tokens`, provider `is_active`) are dropped; `is_default` stays | §2.1 F2, §7, §7.2, §8, Phase 1, Phase 2, Phase 11, §10 row 4; assessment §3.2, §4.3 |
| DR26 | `chat-user` role. In `aaa.go` a deny in any role wins over another role's allow, and field-level denies are pooled across a user's roles, so a separate role either does nothing next to `viewer` or blanks the SYS user screens for admins and operators | **Deferred**: no dedicated chat role; the existing probler.json roles stay as they are; the UI selects only `userId` and `fullName` | §4.4, §6.1, §8.2, §10 rows 7–8, §13 |
| DR27 | Phase 6 blocker: `IServiceHandler.Post/Put/Patch/Delete/Get(IElements, IVNic)` never see the message, so a handler cannot read the requester's aaaId on writes (l8web stamps `L8Query.AaaId` on GETs only). The router could not pass the aaaId on, and the instances could not identify the sender | **F6, your design, implemented at your request**: `IServiceMessageHandler` with per-action methods taking the message (first proposed with the aaaId only; changed so instances can also check `msg.Source()` for Tanto replies, §7.1); `ServiceManager.handle()` dispatches to them. The router, the chat instances, chat-users and the agent implement it. Their plain CRUD methods return an error | §2.1 F6, §4.1–4.3, §7.4, Phase 0.1, Phase 6, §10 row 1a, §13 |
| DR28 | Phase 6: the l8utils aggregator flushed on a timer only, while §5.4 needs 1 s **or** 50 items | **F7, your design, implemented at your request**: a size cap added to `NewAggregator` (constructor parameter, not a setter) | §2.1 F7, §5.4 |
| DR29 | Phase 6 implementation choices inside l8ai: (1) `ChatSync.last_msg_seq` added, the §5.7 gap-fill query needs it; (2) router ↔ instance protocol: assign = POST to `ChatShCnv` without aaaId (headers or `ChatConvIndex` rows), release = DELETE of the owner's `ChatIdx` rows; (3) a turn inherited on an ownership move is ended with a SYSTEM message instead of re-dispatched (the aaaId is not persisted); (4) group membership changes apply only to conversations the requester created; (5) reassigned headers load one GET each, 8 workers (l8orm cannot translate `in [...]`); (6) graceful shutdown waits one aggregator interval instead of flushing; (7) UUIDv5 conversation IDs must match the conversation's own derivation | **Accepted** | §4.3, §4.4, §5, §7.1, Phase 6, Phase 7 |
| DR30 | Phase 7 implementation choices: (1) chat instance services (and Phase 8 `ChatAgent`) are activated as stateful, because the framework registers participants only for stateful services, so `GetParticipants` finds them; their elected leader is unused; (2) service groups `ChatRtr` (router) and `ChatUsr` (chat-users), one leader each; (3) release: the `ChatIdx` callback turns an owner's DELETE of its rows into a reassignment (rows are never deleted), batched over 200 ms; (4) the leader polls `GetParticipants` every 5 s to find lost and joined instances, and replays a moved conversation's registrations 5 s after the move so the new owner has loaded its header; (5) fan-out responses are not marked `partial`, a skipped instance is only logged; (6) a client cannot set OFFLINE (treated as a heartbeat; the sweeper sets it); presence is readable by everyone; (7) `ChatGroup` GET returns only the requester's groups, internal reads without a session (replica recovery) get all; (8) group and custom-status persistence goes straight to the ORM leader, not through an aggregator | **Accepted** | §4.2, §4.3, §4.4, §5.5, Phase 6, Phase 7, Phase 8 |
| DR31 | Phase 8 implementation choices: (1) `ChatConversation.expected_summary_up_to_seq = 19` added for the compaction compare-and-set, never stored; (2) chat instances accept the compaction PATCH from a `ChatAgent` instance; (3) no descriptor listener (spikes skipped): the catalog is `health.ServiceCatalog` + local services + `ServiceMap`, so the model does not see per-service actions; (4) no `busy` reply: above `MaxConcurrentTurns` a turn waits for a slot and the owner's watchdog covers long waits; (5) no per-instance context LRU: each turn reads the header and up to one ring (50 messages) after the summary through the router, about 5 extra router requests/s at the assessment's load (2–3% of the router's traffic); reading straight from the owner is a later option if the load test asks for it; (6) the compaction in-flight gate is agent-local, the owner's compare-and-set settles races; (7) a tool call refused mid-turn returns the error to the model; only an expired session at turn start ends the turn with a SYSTEM message; (8) `AgentConfig.MaskingSecret` keys the tokens (random with a warning when empty); (9) l8agent's LAN-first routing and prompt-template service are not carried over; (10) `@web` tools authenticate with the turn's LLM API key, tokens are estimated at ~4 characters each, the context package is `convctx` | **Accepted** | §5, §7, §7.4, §7.5, Phase 8; assessment §2.2, §3.3, §4.3 |
| DR32 | Phase 9 implementation choices: (1) N8: no quick view, Download only; (2) N9: mobile file transfer uses `Layer8FileUpload` as is; (3) extra file `l8chat-core-actions.js` (`L8ChatSession`); (4) the status picker has Online, Away and Custom, no Offline (DR30 #6); (5) the "my conversations" registration fetches one row (it only registers); the sidebar comes from `ChatPeer` and a peer's list from a `peerKey` query; (6) the group and new-conversation dialogs are shared by desktop and mobile through the platform's popup adapter; (7) deterministic IDs use `crypto.subtle`, so the page must be https or localhost; (8) the bubble takes the agent bubble's position (Phase 11 replaces it) | **Accepted** | §6.1, §6.2, §6.7, Phase 9, §2.1 N8–N9 |
| DR33 | Phase 10 (probler on KIND) findings: (1) F8: F7 broke the `NewAggregator` callers in l8parser, l8collector, l8inventory and l8stocks; fixed with a `0` size cap; (2) F9: l8secure `AllowedTypes` called `Nodes(true, true)`, which panics; now `Nodes(false, true)`; (3) `GetParticipants` only knows the participants of services the asking node hosts, so the router never saw the chat instances and the instances and agent could not verify each other; replaced everywhere by `common.Participants`, which reads the health records; the chat services stay stateful (DR30 #1) but no longer need to be; (4) l8ql returns typed-nil query nodes; the criteria walker treats them as nil; (5) `PlainQuery` keeps the query text (an empty text is not read as a query); (6) `NewOrmSLA` puts every ORM service in the shared `L8SG` group, whose leader was the alarms node; the chat ORM services get their own group `ChatOrm`; (7) the router retries a failed cold start; (8) a panicking turn ends at once with a SYSTEM message; (9) `kind-start.sh` keeps the LLM `hostAliases` only when the machine is on their subnet, else the pods use the public names; (10) the model catalog comes from the web descriptors (§7.3), reversing DR31 #3, and the agent registers the adopter's models through `CallRegisterModels`; (11) second probler user `operator2` and `chatdb` credential | **Done at your request** | §2.1 F8–F9, §7.3, Phase 10, Phase 11 |
| AR1 | Concern #2: router leader dies after writing a new index row but before the DCache replicates it, so the new leader can assign the same conversation to a second instance (duplicate owners) | **Accepted risk, not fixed**: it needs a new or just-reassigned conversation to get its next message exactly during a leader switchover | §4.1 unchanged |
| AR2 | Concern #10: a message resent after 10 s (its push was lost) gets a later `seq` than messages the user sent after it, so it shows out of order | **Accepted risk, not fixed** | §5.2 unchanged |

Parked: concern #5 (client resend for every write plus periodic reconciliation). §5.7 covers lost pushes. Resend still covers only messages.

Open (offered earlier, not applied):
- reordering delivery to LLM profiles → single-instance chat → multi-instance with failover tests → agent.

Proposed design choice, open for your review: reads use `Request` bounded to 3 s, because a read needs an answer (§4.2).
