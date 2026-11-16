# Assessment: Scaling the Tanto AI Agent (concurrency, context, compaction)

Companion to `plans/l8ai-chat-and-agent.md` (§7). Every number marked *(assumption)* is a planning estimate to be confirmed by the Phase 12 load test.

---

## 1. Load model *(assumptions)*

| Quantity | Value | Basis |
|---|---|---|
| Registered users | 20,000 | requirement |
| Peak concurrently online | 6,000 (30%) | typical enterprise day peak |
| Human message rate | ~100 msg/s | 1 msg/min per online user |
| Conversations talking to Tanto at peak | 300 (5% of online) | |
| Tanto turns started | ~2.5 turns/s | 1 user message per 2 min per Tanto conversation |
| Turn wall time | 10–60 s, median ~20 s | 1–10 LLM calls + tool round-trips |
| Concurrent in-flight turns | ~50 (p99 ~150) | Little's law: 2.5/s × 20 s |
| LLM calls/s | ~10 (p99 ~30) | ~4 calls per turn |

**What this means:** agent CPU and memory are small. Each turn is a goroutine waiting on HTTP. The real limits are **provider rate limits** (requests and tokens per minute) and **cost**. So scaling is about fair queueing and backpressure across agent instances, not raw compute.

---

## 2. Concurrency

### 2.1 Problems to solve
1. **Two turns on the same conversation at once.** A user sends a second message while Tanto is still answering. Two agent instances would build contexts from different snapshots and post interleaved, contradictory replies.
2. **Duplicate replies when an agent instance dies or times out mid-turn.**
3. **Many agent instances sharing one provider quota.**
4. **Ordering of the reply relative to user messages.**

### 2.2 Design (recommended)

| Concern | Mechanism |
|---|---|
| One turn per conversation | The **owner chat instance is the single writer** and owns `agent_state`. It dispatches a `ChatAgentTurn` only when the state is IDLE. Messages arriving while RUNNING set `pending=true`. When the reply lands, a pending conversation immediately gets a new turn covering every message after `trigger_seq`. No distributed lock is needed because the owner instance already serialises the conversation. |
| Agent instance selection | **Rendezvous hash** of `conversationId` over `GetParticipants("ChatAgent",81)`. The turn is sent by `Unicast` with the requester's aaaId in the header. The same conversation tends to reach the same agent instance, which keeps the provider prompt cache and the local context LRU warm. When instances are added, only about 1/N of conversations move. |
| Agent instance failure / slowness | A watchdog on the owner chat instance enforces the turn deadline (default 300 s, aligned with the web `SetServiceTimeout`). On expiry it re-dispatches with the **same `turn_id`** to the next agent instance in hash order (`attempt+1`). After 2 attempts it posts a SYSTEM message ("Tanto couldn't answer, try again") and sets the state to IDLE. |
| Duplicate replies | Reply `message_id = UUIDv5(turn_id)`. The owner instance already dedupes by `message_id`, so a late reply from the "dead" agent instance is dropped. It also rejects replies whose `turn_id` is not the current one. |
| Ordering | The reply gets the next `seq` at the owner instance like any message. A reply to a coalesced turn lands after all the messages it answers. |
| Provider quota across instances | Each agent instance keeps a token bucket per provider (requests/min and tokens/min) sized to `quota / live agent instances`, recomputed as participants change. When a bucket is empty, the turn waits up to 30 s and then fails with a SYSTEM message, which applies backpressure. 429 responses use `Retry-After` and exponential backoff with jitter. |
| Per-instance admission | `maxConcurrentTurns` per agent instance (default 64). Over the limit, the agent instance Unicasts `busy` back and the owner chat instance tries the next agent instance in hash order. That spreads load without a central queue. |
| Tool execution | Runs as the **requesting user's AaaId** (from the turn's message header), so ScopeView/deny rules apply. At turn start the agent calls `Security().UserInfo(aaaId)`; an expired session ends the turn with a SYSTEM message. Tools are read-mostly. Write tools (`create/update/delete_record`) are idempotent per turn through a `turn_id`-derived key, so a retried turn cannot double-create. |

### 2.3 Alternatives considered
- **Central work queue (leader-owned).** It gives fair scheduling, but it is a single hot spot and adds a hop. Without affinity it also loses cache locality. Rejected for v1. It can be revisited if fairness becomes a problem.
- **Distributed lock per conversation (DCache entry).** Redundant, because the owner chat instance is already the single writer. It would also add a failure mode (stale locks).
- **Transactional `ChatAgent` service with replication.** This is the framework 2PC path. It is too heavy for long-running (tens of seconds) LLM calls, and transactions time out at 15 s by default.

---

## 3. Context management

### 3.1 Context layout per LLM call (in order, so the provider prompt cache hits on the prefix)
1. **System prompt** (static): Tanto persona, L8Query rules, money in cents, mask token rules, safety and permissions statement. ~1.5K tokens. *Cached.*
2. **Tier-1 catalog** (changes only when services change): `- model: X, service: S, area: A, pk: P` lines from the catalog (plan §7.3), filtered to models the requester's roles may access (`Security().AllowedTypes`). ~2–6K tokens. *Cached.*
3. **Tool definitions** (static per catalog version). *Cached.*
4. **Conversation summary** (`ChatConversation.summary`), if any. ≤ 1.5K tokens.
5. **Tail:** messages with `seq > summary_up_to_seq`, as user/assistant text only. Tool calls and results from earlier turns are **not** replayed (as in l8agent). Only the final assistant text is kept, which bounds growth.
6. **Current turn's user message(s)**: all messages since the last agent reply, when turns were coalesced.
7. **In-turn tool loop:** tool results produced during this turn, with each result truncated to `maxToolResultTokens` (default 4K). Rows beyond a limit are summarised as "N more rows; refine the query".

### 3.2 Budget
- `budget = context_window − max_output_tokens − safety margin (5%)`. `LlmProfile` returns only kind, URL, API key and model, so the context window and output limit come from the agent's configuration (`AgentConfig`), not from the profile.
- If 1–6 exceed the budget before the tool loop even starts, a **synchronous compaction** runs first (rare; see §4).
- During the tool loop, if the running total reaches 90% of budget, the agent stops calling tools and asks for a final answer (`tool_choice: none`), as l8agent's `SendFinalMessage` does.

### 3.3 Where context comes from (multiple agent instances)
- The agent fetches the summary and tail from the **owner chat instance** through the router (`ChatMsg` GET with `seq > summary_up_to_seq`, a bounded `Request`). The owner is the source of truth, so any agent instance can serve any turn.
- A per-instance LRU of `(conversationId → prepared tail, last_seq)` avoids re-fetching on affinity hits. It only fetches the delta.

### 3.4 Privacy in context
- Masking is applied to **every** user/assistant text in 4–7 on each call, not just the newest message (fixes l8agent, which replayed history unmasked).
- **Deterministic tokens:** `[NAME_<HMAC(secret, conversationId|value)[:6]>]` and `[MONEY_…]`.
  - The same value maps to the same token on every turn and on every agent instance, so the LLM sees a stable pseudonym across the whole conversation.
  - No token map has to be shared or persisted. Each call builds its own unmask map from the values it masked.
- Classification uses the real model name from the catalog.

---

## 4. Compaction

### 4.1 When
- **Asynchronous (normal path):** after the reply is posted, if `tokens(summary) + tokens(tail) > 60% of budget`, or the tail is over 40 messages.
- **Synchronous (fallback):** before a turn, if 1–6 do not fit the budget. This happens only when async compaction is behind, for example right after a long burst.

### 4.2 How
1. Split the tail. The **oldest** messages, up to about 50% of the tail tokens, are compacted. The newest ~10 messages always stay verbatim so the immediate thread keeps its exact wording.
2. Call the LLM with the masked old summary and the masked chunk, using a fixed compaction prompt:
   - Keep facts, decisions, entities and IDs, the user's preferences and open questions.
   - Keep any L8Query that produced a result the user cares about.
   - Drop pleasantries and superseded answers.
   - Target ≤ 1,500 tokens.
3. **Unmask** the result with the call's map and store it **raw**. It is re-masked deterministically on every future use.
4. `PATCH ChatConv {summary, summary_up_to_seq = last seq of chunk, expected_prev_up_to_seq}`. The owner chat instance applies it **only if** `summary_up_to_seq` still equals `expected_prev_up_to_seq` (compare-and-set). That means two agent instances racing to compact cannot clobber each other, and the loser simply discards its result.
5. Messages are never deleted. Compaction only changes what goes into the LLM context. The UI still pages the full history from `ChatMsgDb`.

### 4.3 Cost and quality controls
- Compaction calls use the conversation's profile.
- At most one compaction per conversation is in flight. This is tracked on the owner chat instance with the same IDLE/RUNNING gate as turns, using a separate flag.
- Metric `l8ai_compaction_ratio` (tokens before/after) is logged for tuning.

### 4.4 Why not alternatives
- **Plain truncation** (l8agent's "last 20"): loses facts the user established early ("only show devices in site X"). Rejected.
- **Embeddings/RAG over history:** more machinery (a vector store) for little gain in chat-length histories. It can be added later as a tool (`search_history`) without changing this design.

---

## 5. Sizing and instances *(assumptions, to be verified in Phase 12)*

| Component | Start | Per-instance estimate | Scale trigger |
|---|---|---|---|
| chat-router | 2 (only the leader is active; the other is standby with a full index copy) | index lookup + Unicast; `ChatIdx` ~200K rows + `ChatReg` (<100 MB) | p99 forward latency > 20 ms |
| chat (instances) | 3 | all owned headers (~70K × ~1.5 KB ≈ 100 MB) + ~7K active rings × 50 msgs × ~1 KB ≈ 350 MB | RSS > 1.5 GB or p99 send→push > 150 ms |
| chat-users | 2 | `ChatStat` 20K rows + groups (<50 MB) | — |
| chat-orm | 1 | ~100 inserts/s batched | write lag > 2 s (scale-out requires the N1 framework change, not authorised) |
| chat-agent | 2 | 64 concurrent turns, <300 MB | queue wait p95 > 5 s *and* provider quota not exhausted |

Targets for the load test: send→push p50 < 50 ms and p99 < 250 ms (human messages); time-to-first-Tanto-reply bounded by the LLM; no lost or duplicate messages in the failover tests; agent duplicate-reply rate 0.

---

## 6. Failure modes summary

| Failure | Effect | Recovery |
|---|---|---|
| Chat instance restarted on purpose (upgrade, drain, scale-down) | None visible | SIGTERM: stop accepting, flush to ORM, send `release`. The router leader reassigns and replays registrations; new owners hydrate. **No loss.** |
| Chat instance hard crash (OOM, node failure, eviction, panic) | Its conversations are briefly unavailable | The router leader reassigns (least loaded) and replays registrations; new owners hydrate from ORM. Un-flushed messages (≤ ~1 s) are lost (accepted, Q2). Browsers resend messages still *pending* after 10 s by `message_id`. Mitigations: memory guard, `recover()` in handlers. |
| Agent instance dies mid-turn | Reply delayed until the deadline | Watchdog re-dispatches the same `turn_id`; dedupe guarantees one reply |
| Provider 429/outage | Turns queue, then fail visibly | Backoff and token bucket; SYSTEM message after 30 s wait; profile switch available in the UI |
| Router leader dies | Requests fail until a new leader is elected | The web routes to the service leader (`M_Leader`); the new leader already holds the full `ChatIdx`/`ChatReg` copy (DCache), so there is no state loss. Pending browser messages are resent |
| chat-orm down | Writes buffer in the chat instances' aggregators (bounded) | Flush on recovery. Hydration of cold conversations fails visibly until it is back. |
| Websocket drop | Client misses pushes | `Layer8DWebSocket` reconnects and re-registers (`onReconnect`). The client re-fetches the open lists from page 0. |

---

## 7. Recommendation
Build the single-writer chat instance + affinity-hashed stateless agent design described above. It needs no framework changes beyond the authorised F1–F4 (plan §2.1). It gives one-turn-per-conversation correctness and idempotent failover, and the cache-friendly context layout keeps per-turn cost low. Compaction is asynchronous, CAS-guarded, never deletes history, and stores summaries raw with deterministic masking at send time.
