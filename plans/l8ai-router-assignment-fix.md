# Fix: router assignments rejected by chat instances, and router restart with live instances

Status: **DRAFT. Not started.** Found during Phase E of `plans/l8ai-followup-l8erp-l8alarms-migration.md` (l8erp on KIND, 2026-11-17). Waiting for approval.

## 1. How it was found

The full l8erp e2e suite on KIND passed 730 of 731 tests. `tests/mobile/05-chat.spec.ts:33` ("a message sent from mobile is delivered") was flaky: on the first try the page threw `conversation <id> is not owned by this instance` for two conversations, `8068ffb5…` and `9477fe99…`.

Both conversations exist in chat-orm (`chatdb.chatconversation`), and the router has an owner for them. The owning instances (`erp-chat-2` and `erp-chat-0`) do not hold them, so every read of them fails, and it still fails now. It is not a race.

**Timeline (UTC, from `/data/logs/erp/chat*` in the pods):**

| Time | Event |
|---|---|
| 00:14 | Chat instances and routers start. Cold start assigns 0 conversations |
| 00:16:49–00:17:09 | The e2e run creates 6 conversations, which instances hold in memory |
| 00:28:02 | Both routers restart with the new image (`3bdb936`). The instances keep running. The routers' in-memory index is lost |
| 00:28:08 | Router cold start: "assigned 6 conversations". At the same second all 3 instances log `Unknown session` |
| afterwards | 4 of the 6 still work, because the cold start happened to pick the instance that already held them. The other 2 are routed to instances that never received them |

## 2. Bugs

### B1: Every router assignment is rejected in production (critical)
- The router assigns conversations by POSTing to the instance's `ChatShCnv` **without a session**. It does this at cold start (`assignHeaders`) and when an instance is lost (`reassign` / `reassignLost`).
- The instance tells an assignment apart from a user creating a conversation with `assignment := msg.AAAId() == ""` (`go/ai/chat/conversations.go:51`).
- On the real bus that check is never true. `l8secure` `_securityProvider.Message()` (`go/secure/provider/SecurityProvider.go:159-171`) fills an empty session with the node's `LocalAlias`.
- So the instance runs `createConversation`, then `requester`, then `UserInfo(<alias>)`, which returns `Unknown session`. The handler returns on the first error, so the whole batch is dropped. The router uses fire-and-forget `Unicast` and never finds out.
- **Impact:**
  - **Cold start** never loads anything into the instances.
  - **Failover (`reassignLost`) is broken:** when a chat instance dies, its conversations can't be read or written until the whole chat tier restarts.
- **Why tests missed it:** the l8utils `MockVNic` leaves the session empty, so the in-process tests take the assignment path.

### B2: Router restart with live instances reassigns conversations that are already held (design gap)
- The ownership index (`ChatConvIndex`) lives only in the routers' memory. When every router replica restarts while the instances keep running, `coldStart` assigns every conversation again with `leastLoaded`, ignoring who already holds it.
- Even with B1 fixed, a conversation can move to a new owner while the old owner keeps a stale copy. That copy has its own live registrations, notification sequence and agent turn.
- This happens on every rolling update of the router.

### B3: A failed gap fill becomes an uncaught exception in the browser (l8ui `l8chat`)
- A 400 from `ChatSync` gap fill (`select * from ChatSync where conversationId=… and notifSeq>…`) is thrown as an uncaught page error. The e2e page-error check then fails the test.
- It should be handled like the other failed reads: log it once, and fall back to a snapshot on the next poll or mark the conversation as unavailable.

### B4 (framework, for your approval only): `send on closed channel` panic in l8utils
- Both router logs from before the 00:28 restart contain:
  `panic: send on closed channel` at `l8utils/go/utils/requests/Requests.go:165` (`(*Request).SetResponse`), called from `l8bus vnic/RX.go:132` (`notifyRawDataListener`).
- A late response arrives after its request was already closed (timed out). This is framework code. Per policy it is listed here and **not** changed without your authorization.

## 3. Suggested fixes (l8ai unless noted)

### F1: The payload type, not the session, says whether it is an assignment (fixes B1)
- **Router:** `assignHeaders` (`go/ai/router/index.go`) sends `ChatConvIndex` rows to each owner, as `reassign` already does, instead of `ChatConversation` headers.
- **Instance** (`go/ai/chat/conversations.go` `PostMessage`):
  - `*l8ai.ChatConvIndex` → `assignById` (loads the header from chat-orm). Always an assignment, and accepted only from the router (`fromRouter`).
  - `*l8ai.ChatConversation` → `createConversation`. Always a user creation, which needs a session.
  - Remove `assignment := msg.AAAId() == ""`.
- Handle each element's error separately, so one bad element no longer drops the rest of the batch. Log what is skipped.
- **Cost:** at cold start and failover, instances read their headers from chat-orm (`assignById`) instead of receiving them from the router. This happens only at startup and failover.

### F2: Rebuild the index from the instances before cold start (fixes B2)
- New instance read (GET on `ChatShCnv`, router only): returns the `ConversationId`s this instance owns, as `ChatConvIndex` rows with `OwnerUuid` set to itself.
- `coldStart` (leader):
  1. Fan out that read to every instance (`fanOut`). Post the answers to the index and rebuild `counts`.
  2. Then page through chat-orm as today. `assignHeaders` already skips conversations that have an owner (`ownerOf(...) != ""`), so only unheld conversations are assigned.
- If two instances both claim one conversation (possible after an earlier B2), keep the one with the higher `NotifSeq` and tell the other to release it (existing release path).

### F3: Handle a failed gap fill in l8ui `l8chat` (fixes B3)
- In `l8chat-core-live.js`, gap fill catches request errors and does not rethrow. On "not owned" or "unknown conversation", it drops the gap state for that conversation and lets the 60 s `ChatSync` poll recover it.
- It is in the l8ui repo, so it can go in with F1/F2 or on its own.

### F4: Framework fix for B4 (needs your approval)
- In l8utils `Requests.go`, `SetResponse` must not send on a closed channel. Either close and send under one lock, or drop a response whose request was already removed.

## 4. Tests
- An in-process test where the instance sees the router's **alias as the session** (configure the `MockVNic`/security double the same way as `l8secure` `Message()`). It covers:
  - Cold start loads every conversation into its owner.
  - `reassignLost` moves the conversations of a removed instance, and they can be read and written afterwards.
  - Router restart with live instances: no conversation changes owner, and an unheld one is assigned.
  - A batch with one bad element still applies the others.
- KIND:
  - Restart both routers (`kubectl rollout restart statefulset/erp-chat-router`) during a chat session. Every existing conversation is still readable, and no instance logs `Unknown session`.
  - Delete one chat instance pod. Its conversations stay readable after reassignment.
- l8erp and probler e2e: rerun the desktop and mobile chat specs and the full suite.

## 5. Rollout
1. F1 + F2 + tests in l8ai. Then re-vendor in l8erp and probler (you), and rebuild the chat images.
2. F3 in l8ui. Then update the l8ui submodule in l8erp and probler.
3. F4 only if you approve it.
4. Recreate the l8erp KIND cluster (`kind-stop.sh`, then `kind-start.sh`) so the two broken conversations are cleared, then rerun Phase E.

## 6. Related notes from Phase E
- The follow-up plan (Phase B.3 and Phase E) still expects "SYS LLM Providers/Profiles tabs". The main plan removed them (Phase 3: "SYS LLM tabs are N2 and not in scope"). The follow-up plan should be corrected.
- The 164 `POST rejected` lines in `12-crud-all-models.spec.ts` are skips of the generic CRUD record builder, not failures. They have not been compared with a run from before the port.
