# Follow-up: Migrate l8erp and l8alarms from l8agent to l8ai, then retire l8agent

Status: **DRAFT follow-up (Q4).** Runs only after `plans/l8ai-chat-and-agent.md` Phases 0–12 are complete and verified in probler. Waiting for approval.

## 1. Current l8agent touch points (verified by grep, vendor/ and l8ui/ copies excluded)

### l8erp
- **Agent services run inside the main ERP binary**, not a separate pod. `go/erp/services/activate_aia.go` calls `aia.Activate(creds, dbname, nic)` and `aia.ActivateChat(...)`.
  - Config: `go/erp/aia/activate.go`, area 120. It has `LLMCreds "TestLLM"`, the openai provider, `HelpDocs` (`help.md`), `WebToolsURL`, and 4 default prompts.
- **Types:** `go/erp/ui/shared_aia.go` registers 4 l8agent types. `go/erp/ui/main/main.go:25` sets the AgntChat timeout. `go/go.mod` carries the l8agent dependency.
- **Mocks:** `go/tests/mocks/gen_aia.go`, `aia_phases.go`.
- **Desktop web:**
  - `app.html` (l8agent CSS/JS at about lines 83 and 657; `aia/*` scripts 658–664; nav link `data-section="aia"` at 226)
  - `js/app.js:230-231`: `L8AgentBubble.init`, behind a `typeof` guard, which is also a FailFast violation
  - `js/sections.js:32,103-105`
  - `sections/aia.html`
  - `aia/` directory
- **Mobile web:** `m/app.html`, `m/js/app-core.js`, `m/js/aia/agent-chat-init.js`, and `erp-ui/m/nav-configs/layer8m-nav-config-prj-other.js` (aia entries).

### l8alarms
- `go/alm/ui/web/app.html:54-55` (l8agent CSS) and `328-332` (l8agent JS). These are **loaded but never initialised**: no `L8AgentBubble.init` call exists in l8alarms JS. No Go dependency.

## 2. Phases

### Phase A: l8erp backend
1. Add the l8ai binaries the same way as probler. Per DeploymentArtifacts:
   - `go/erp/chat-router/`, `go/erp/chat/`, `go/erp/chat-orm/` (base `erp-postgres`), `go/erp/chat-agent/` (base `erp-security`)
   - Each gets `build.sh` and `Dockerfile`. Images: `saichler/erp-chat-*`.
   - `chat-agent` is configured with `RegisterTypes`: the ERP UI type registration. The `@help` docs come from the existing `aia/help.md` and the `@web` tools from `WebToolsURL`.
   - The 4 default prompts become role-scoped guidance in the agent system prompt (l8ai has no prompt service). Confirm when this phase starts.
2. Remove `services/activate_aia.go`, the `go/erp/aia/` package and `ui/shared_aia.go`. The UI type registration calls `l8aicommon.RegisterTypes(res)`; the LLM types are registered by the security plugin.
3. Remove the l8agent entry from `go.mod`. **You re-vendor** (VendorAndGit).
4. Security: `l8secure/go/secure/plugin/erp/erp.json` gets an LLM provider and default profile (seeded from `TestLLM`). Tanto is not a security user and there is no dedicated chat role (main plan DR13, DR26). Then rebuild `erp-security`.
5. K8s: the 4 workloads in `k8s/l8erp-{local,baremetal,gke,kind}.yaml` (same kinds as the probler table), a shared `/data/l8files` volume for the web pods, `deploy.sh`/`undeploy.sh` order, `kind-start.sh` image loads.
6. Mocks: replace `gen_aia.go`/`aia_phases.go` with chat seeding (groups, conversations, messages through `/81/ChatMsg`) placed after HCM. Users come from the Security API.

### Phase B: l8erp UI (desktop + mobile)
1. Remove the l8agent CSS/JS, the `aia/*` scripts, the AIA nav link, the `sections.js` entries, `sections/aia.html`, the `aia/` directory, and the mobile aia files and nav-config entries.
2. Add the l8chat CSS/JS blocks (canonical loading order updated in the main plan, Phase 9). Call `L8ChatBubble.init({})` in `app.js` **without** a `typeof` guard, and `L8ChatBubbleMobile.init({})` in `m/js/app-core.js`. Add the `L8Chat*` globals to `erp-deps-check.js` in both shells.
3. SYS > Security > LLM Providers/Profiles tabs appear automatically (the l8ui sys config from the main plan, Phase 3). Add `l8security-llm-provider-crud.js` to `app.html`.

### Phase C: l8alarms UI
- Remove the dead l8agent CSS/JS includes. Decide whether l8alarms mounts the chat bubble. **Recommendation:** yes, for consistency. That requires l8alarms' backend to reach a chat deployment. If l8alarms runs only embedded in probler (`ui.RegisterAlmTypes`), keep it bubble-free in its standalone shell.

### Phase D: Retire l8agent
Precondition: `grep -rn "l8agent" --include=*.go --include=*.js --include=*.html --include=go.mod` across the ecosystem (excluding vendor/) returns nothing outside l8agent itself.
1. Delete `l8ui/l8agent/` (desktop + `m/`).
2. Remove the l8agent entries from `l8erp/rules/desktop-script-loading-order.md` and `mobile-script-loading-order.md`, from the guide lines (L8AgentChat section, PrdL8uiIncludesAudit lists, dependency graphs, project table), and from `layer-8-arch.md` (replace the "l8agent" section with "l8ai").
3. Archive the l8agent repository (README pointer to l8ai). **You do this.** No git operations by me.

### Phase E: Verification
- l8erp KIND: `kind-start.sh` → seed → `e2e` desktop and mobile chat specs (ported from probler's specs) plus a smoke test of every ERP section. Confirm the AIA section is gone and the SYS LLM tabs work.
- l8alarms: open the standalone shell with no console errors.
- Final grep (Phase D precondition) is clean.

## 3. Traceability

| # | Section | Gap / Action Item | Platform | Phase |
|---|---|---|---|---|
| 1 | l8erp | Chat binaries, images, k8s 4 modes, deploy order | Backend/K8s | A |
| 2 | l8erp | Remove aia package/activation/types; go.mod | Backend | A |
| 3 | l8erp | erp.json LLM provider/profile, Tanto, roles | Backend | A |
| 4 | l8erp | Mock seeding for chat | Backend | A |
| 5 | l8erp | Remove AIA UI, add l8chat bubble, deps-check | Desktop | B |
| 6 | l8erp | Same | Mobile | B |
| 7 | l8alarms | Remove dead l8agent includes; bubble decision | Desktop | C |
| 8 | Ecosystem | Delete `l8ui/l8agent`, update rules/docs, archive repo | All | D |
| 9 | Verify | KIND e2e + smoke + clean grep | All | E |
