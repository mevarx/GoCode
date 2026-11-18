# GoCode — As-Is Baseline Audit (for an omp-inspired upgrade)

**Repo:** `D:\CODING\GoCode` · branch `main` · audit base HEAD `fc93321` · clean working tree
**HEAD note:** during the audit a **concurrent docs-only commit** moved HEAD to `d645368` (`docs(research): add omp reverse-engineering report…`, +3,181 lines of docs, 4 lines in CHANGES.md, **no `.go` changes**). All source citations and build/test results below remain valid for the unchanged Go tree.
**Module:** `github.com/mevarx/GoCode` · `go 1.26.11` (pinned; go.mod comment cites stdlib advisories)
**Audit date:** 2026-11-19 · **Method:** static reading of source + `go build`/`go vet`/`go test ./...` on Windows 11 (git-bash)
**Convention:** `[V]` = source-verified (file:line cited). `[I]` = inference/interpretation, not directly stated by code.

---

## 0. Package map

| Package | Files (non-test / total) | LOC | Role |
|---|---|---|---|
| `cmd/gocode` | 5 / 17 | 3,593 | cobra CLI: root/agent launcher, doctor, logs, mcp, provider, session, auth, headless |
| `internal/agent` | 8 / 27 | 6,975 | the single agent engine: loop, prompt, context/compaction, guard, slash commands, metrics, plain REPL |
| `internal/config` | 2 / 4 | 1,123 | TOML config, defaults, platform dirs, secret-env list |
| `internal/eval` | 1 / 2 | 436 | in-repo eval harness (test-only; `doc.go` + `eval_test.go`) |
| `internal/ignore` | 2 / 4 | 545 | `.gitignore`/`.gocodeignore` matcher + sensitive-file matcher |
| `internal/mcp` | 3 / 6 | 1,652 | MCP stdio client, manager, tool adapter |
| `internal/provider` | 7 / 14 | 4,426 | Provider interface + 20 built-ins (17 gateways, ollama, anthropic, copilot) + retry |
| `internal/safetext` | 1 / 2 | 185 | ANSI/control-char sanitizer for untrusted text |
| `internal/session` | 2 / 8 | 2,352 | SQLite session store + markdown/JSON export |
| `internal/tools` | 15 / 35 | 7,529 | tool registry, 7 built-ins, approval gate, path confinement, redaction, plan/scratchpad |
| `internal/tui` | 10 / 25 | 4,336 | Bubble Tea v2 model, mascot animation, picker, approval bridge, theme/styles |
| `internal/version` | 1 / 1 | 8 | version string |
| **total** | **57 / 145** | **33,160** | |

[V] `find . -name '*.go' | wc -l` = 145; `wc -l` sum = 33160.

---

## 1. Agent loop — exact turn lifecycle

### 1.1 ASCII flow (one turn)

```
main.runAgent (cmd/gocode/main.go:212)
  └─ tui.Run(...)  or  loop.Run(ctx)          <- one engine, two front-ends
       │
       ├─ AgentLoop.RunTurn(ctx, input)                       internal/agent/loop.go:177
       │    ├─ Approval.BeginTurn()                           (clears per-turn read-only flag)
       │    ├─ newTurnMetrics(a)                              (prompt/completion/tool counters)
       │    ├─ handleSlashCommand → HandleCommand             commands.go:35   [returns if "/..." or exit]
       │    └─ Session.AddMessage({role:user}) ; streamResponse
       │
       └─ streamResponseGuarded  (loop.go:265)  ┌── for {  ──────────────────────────────┐
            Registry.Active()                  │  p := active provider                   │
            Session.Model()                    │  ContextManager.ResolveBudget(reg,model)│  80% of window, clamp [4096, window]
            ContextManager.Truncate(history)   │  → whole-turn drop + EnsureToolPairing  │
            p.Stream(ctx,model,history,tools)  │  → <-chan StreamChunk                   │
              for chunk := range ch:           │    Delta → emitDelta (EventDelta)       │
                 Reasoning → accumulated only  │    ToolCalls → append (already complete)│
                 FinishReason → recorded       │    ctx.Err() checked per chunk          │
            Session.AddMessage({assistant, content, tool_calls, reasoning})              │
            if len(toolCalls)==0 → guard.Summary + truncation warning → return           │
            guard.CheckCall(toolCalls)  ── over limit ─→ synthetic results + return      │
            handleToolCallsWithVerify                                                    │
              ├─ observeMutations (via Approval.OnDecision)                              │
              ├─ handleToolCalls (loop.go:557)                                           │
              │    Phase A (sequential): emit EventToolStart → Registry.Get             │
              │        → Approval.Prepare(...) → execute closure or final error         │
              │    Phase B: read-only calls concurrent (sem=4); mutating serialized     │
              │    Phase C: finishToolCalls → redact → AddMessage(tool) → EventToolResult │
              └─ if any mutating tool ran && VerifyCommand != "" → runAutoVerify          │
          └───────────────────────────────────────────────────────────────────────────┘
```

### 1.2 Types and functions (with lines)

| Thing | Where | Notes |
|---|---|---|
| `AgentLoop` struct | loop.go:89-119 | Registry, Session, ToolRegistry, Approval, ContextManager, WorkspaceRoot, Plan, Scratchpad, VerifyCommand/Timeout, GuardConfig, `Observe func(LoopEvent)`, `AskApproval func(string) bool` |
| `NewAgentLoop` | loop.go:121 | sets `ContextManager: NewContextManager(0)` |
| `LoopEventKind` | loop.go:20-38 | `EventDelta, EventToolStart, EventToolResult, EventNotice, EventError, EventExit, EventTurnEnd` |
| `LoopEvent` | loop.go:42-58 | `Kind, Text, Tool, IsError, ToolCallID, Args, Diff, Err`. **`EventTurnEnd` is declared but never emitted** [V] (grep: only declared at loop.go:37) |
| `RunTurn` | loop.go:177-212 | the only public turn entry; shared by TUI + plain |
| `handleSlashCommand` | loop.go:222-235 | builds `CommandContext` and calls `HandleCommand` |
| `streamResponse` / `streamResponseGuarded` | loop.go:239 / 265 | guard construction + metrics defer + observeApprovals |
| `EnsureSystemPrompt` | loop.go:166-173 | rebuilds prompt every call: `DefaultPromptOptions(workspace, provider, model)` + `Plan.Render()` + `Session.Scratchpad()` |
| `handleToolCalls` (3 phases) | loop.go:557-622 | deterministic approval order, then bounded parallel execution |
| `preparedToolCall` | loop.go:627-640 | call/tool/execute closure/readOnly/timing/result |
| `executeToolCalls` | loop.go:670-718 | semaphore `toolExecutionLimit = 4` (loop.go:82) for read-only; mutating waits for pending reads then runs alone |
| `finishToolCalls` | loop.go:748-827 | single transcript boundary; redacts; emits; re-renders system prompt after plan/scratchpad writes (loop.go:803-809) |
| `LoopGuard` | loop_guard.go:31-47 | `MaxIterations`, `MaxRepeatedCalls`, `ToolCalls`, consecutive-repeat tracking |
| `DefaultMaxToolIterations` = **50** | loop_guard.go:13 | provider round-trips per turn |
| `DefaultMaxRepeatedToolCalls` = **3** | loop_guard.go:17 | *consecutive* identical batches |
| `canonicalCallKey` | loop_guard.go:125-134 | JSON re-marshal to canonicalize args, so formatting can't hide repeats |
| `runAutoVerify` | loop.go:437-498 | opt-in verify command, `DefaultVerifyTimeout = 120s` (loop.go:69), output capped 8 KiB (loop.go:66), redacted |
| `writeSyntheticToolResults` | loop.go:547-555 | synthesizes `tool` messages so no dangling call ids (OpenAI/Anthropic wire requirement) |
| `cappedBuffer` | loop.go:513-543 | concurrency-safe byte cap for verify output |

### 1.3 Prompt assembly (system prompt composition)

`baseSystemPrompt` (prompt.go:13-28) is a fixed 16-line string: role, untrusted-data warning, workspace boundary, sensitive files, `code_search` first, `file_read` offset/limit, prefer `file_patch`, plan tool for 3+ steps, scratchpad, verify with shell_exec, avoid loops.

`BuildSystemPromptWithOptions` (prompt.go:128-180) composes, in order:

1. `baseSystemPrompt`
2. `## Current Plan` + plan text (owned by assistant, rendered *before* untrusted blocks) — prompt.go:133-137
3. `## Task Scratchpad` + scratchpad (agent state, explicitly "not policy") — prompt.go:139-143
4. `## Project Context (untrusted repository data)` — delimited `<<<BEGIN UNTRUSTED PROJECT CONTEXT>>>` / `END`, plus a notice that it cannot grant permissions — prompt.go:145-148, 104-108
5. `## Global Context (untrusted user data)` — same treatment — prompt.go:150-153, 110-113
6. `## Environment` — `OS`, `Workspace`, `Git Branch`, `Provider`, `Model` — prompt.go:155-177

Prompt-injection defences:
- `neutralizeUntrustedMarkers` rewrites `<<<` → `<< <` in any untrusted content (prompt.go:197-199) so a hostile file cannot close the block early.
- `getGitBranch` reads `.git/HEAD` directly instead of shelling out (prompt.go:202-216).

Context discovery:
- **AGENTS.md**: `FindProjectContext` walks from `startDir` upward, checking `AGENTS.md`, `.gocode/AGENTS.md`, `docs/AGENTS.md` at each level; first hit wins (prompt.go:47-84).
- **CONTEXT.md**: `LoadGlobalContext` reads `config.ConfigDir()/CONTEXT.md` (prompt.go:86-100).
- No `CLAUDE.md`, no `GEMINI.md`, no nested/multi-file AGENTS.md merge, no size cap on injected context [V by absence].

### 1.4 Streaming handling

Provider-agnostic: the loop only reads `provider.StreamChunk` (`Delta`, `Reasoning`, `ToolCalls`, `Done`, `Err`, `FinishReason`; provider.go:23-32). It accumulates text in a `strings.Builder`, forwards deltas **immediately** as `EventDelta`, accumulates `Reasoning` into `ReasoningContent` for replay, and appends tool calls as emitted. Cancellation is polled per chunk (loop.go:318-321) *and* after the stream (loop.go:323-325).

There is **no tool-argument streaming to the UI**: `ToolCalls` arrive already-complete from the provider layer (SSE assembly happens inside `streamOpenAISSE`, copilot.go:477-634), so the UI sees `EventToolStart` only after the whole call is parsed. [V]

Truncation handling: `isTruncationFinish` ("length"/"max_tokens") is duplicated in `agent` (loop.go:138) and `provider` (copilot.go:637). On truncation, no tool calls are executed and a user-facing warning is emitted (loop.go:352-358); pending tool calls are dropped in the SSE reader (copilot.go:536-546).

Empty-response notice: if a round returns no text and no tool calls, the loop emits a "Verify provider API keys/credentials" notice (loop.go:334-339).

### 1.5 Approval-gate integration

`Approval.Prepare(ctx, tool, args)` returns a closure (`func(context.Context) (tools.Result, error)`), so *all* decisions for a batch complete before *any* execution (loop.go:601, approval.go:390-392). Decision order inside the gate: denied-by-config → auto-approve → `!RequiresApproval()` → exact-args session grant → per-turn read-only flag → preview → prompt (approval.go:439-511).

Metrics/verification hooks are chained onto a single mutable callback `ApprovalGate.OnDecision` (loop.go:251-263, 400-414) with save/restore — a string-based side-channel, not an event bus [V, and a structural seam noted in Q11].

### 1.6 Limits, abort, compaction

- **Iteration limit:** `LoopGuard.MaxIterations`, default 50, wired from `cfg.Tools.MaxToolIterations` (main.go:439-442). Checked *before* execution (`CheckCall`, loop_guard.go:87-94) so a failing tool retried identically still hits the cap.
- **Repeat limit:** 3 *consecutive* identical batches (`consecutiveRep`), keyed on `name + canonical(args)`; interleaved calls reset the streak (loop_guard.go:96-118). On trip: synthetic results for all calls + `Stopped: ...` assistant message + `EventError` (loop.go:363-373).
- **Abort:** `ctx` cancellation. TUI: Ctrl+C/Esc → `cancelCh` → `watchCancelSignal` → `turnCancel()` (run.go:223-230); second Ctrl+C quits (model.go:230-249). Plain: SIGINT/SIGTERM cancels the root context (main.go:409-414). In-flight calls get `interruptedToolResult` ("Error: tool call interrupted before it could execute.", loop.go:85) so history stays valid.
- **Compaction:** only explicit `/compact` (commands.go:88-123): `CompactWithSummary` (model-written summary, context.go:537-601) with fallback to `Compact` (count-only placeholder, context.go:460-504). `keepLastTurns = 2` in both paths. Summary request is capped at 20 000 runes and 60 s (context.go:509-514).
- **Automatic context control:** there is **no auto-compaction**. Two safety nets instead: `ContextManager.Truncate` drops whole oldest turns until the estimate fits (context.go:404-455), and `Session.AddMessage` trims at >500 messages to the last 40 turns (session.go:84-95).
- **Plan/scratchpad role:** `PlanTool` writes a checklist (`tools.PlanStore`) rendered under `## Current Plan`; `ScratchpadTool` writes working state that is mirrored into `Session.SetScratchpad` and survives compaction because it lives in the system prompt. Both rebuild the system prompt after a successful write (loop.go:803-809). Both are in `readOnlyToolNames` (loop.go:73-78) so they never trigger auto-verify or a concurrency exclusion.

---

## 2. Provider layer

### 2.1 Structure

`Provider` interface is three methods (provider.go:40-44):

```go
Name() string
Models(ctx) ([]string, error)
Stream(ctx, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error)
```

Wire types: `Message{Role, Content, ToolCalls, ToolCallID, ReasoningContent}` (provider.go:8-15), `ToolCall{ID, Name, Args json.RawMessage}` (17-21), `StreamChunk{Delta, Reasoning, ToolCalls, Done, Err, FinishReason}` (23-32), `ToolSpec{Name, Description, Parameters}` (34-38).

`Registry` (registry.go:30-36) is a plain `map[string]Provider` + `active` + `contextWindows` (no plugin discovery, no config-driven registration hook besides `Register`). `Register` sets `active` to the first registrant (registry.go:45-51) — in practice **ollama**, because `main.go:244` registers it first, then the gateways, then anthropic + copilot, then custom (main.go:238-255). The configured default is applied later via `Switch` (main.go:297).

### 2.2 The 20 built-in providers

17 gateways served by one OpenAI-compatible proxy (`gatewayProviderNames`, main.go:129-133):

`omniroute, openai, gemini, groq, openrouter, qwen, kimi, hermes, xai, mistral, minimax, deepseek, together, fireworks, cerebras, zhipu, nvidia`

plus `ollama`, `anthropic`, `copilot` = **20** [V].

Per-provider config resolution: `gatewayConfigFor` (main.go:136-180) — a custom entry **wins over** the built-in, so a user can shadow a built-in name. `config.ProviderConfig` holds one `GatewayConfig` field per gateway (`config.go:20-40`).

`GatewayConfig` has `BaseURL`, `APIKey` (inline), `APIKeyEnv`, `DefaultModel` [V, used at config.go:160-200]; inline keys warn on startup (`InlineAPIKeyWarnings`, main.go:234-236).

### 2.3 Dialect normalization

**OpenAI-compatible (17 gateways + copilot)** — one shared path:
- Request body: `{model, messages, stream:true, tools?}` (gateway_proxy.go:170-178); `buildOpenAIMessages`/`buildOpenAITools` live in copilot.go:408-444 and are shared so both send byte-identical history.
- `openAIMessage` carries `reasoning_content` so MiniMax/DeepSeek keep the chain across tool round-trips (gateway_proxy.go:118-119).
- Streaming: `streamOpenAISSE` (copilot.go:477-634) — scanner with 8 MiB line cap, skips non-`data:` lines, handles in-band `{"error":{...}}`, accumulates tool calls **by index** into a `map[int]*pendingToolCall` with a `replaced` list for id collisions, synthesizes ids `call_<name>_<idx>`, flushes on `finish_reason == tool_calls|stop` or `[DONE]`.
- Content-type guard: a non-SSE 200 is an error rather than a silent empty stream (copilot.go:456-464).
- OpenRouter gets `HTTP-Referer` + `X-Title` headers (gateway_proxy.go:64-67).
- HTTP client timeout 10 min, dial 30 s, TLS 15 s, wrapped in `RetryTransport` (gateway_proxy.go:28-42).

**Anthropic** (`anthropic.go`) — separate provider with its own request shape; not normalized through the OpenAI path [V, distinct file/type; details not exhaustively read in this pass — flagged as a partial].

**Gemini** — note: `gemini` is *registered as an OpenAI-compatible gateway*, i.e. it targets Google's OpenAI-compat endpoint, not the native `generateContent` API [V from main.go:130 + gateway_proxy.go].

**Ollama** (`ollama.go`) — native `/api/chat` NDJSON, not SSE: line scan → `ollamaChatResponse`, `DoneReason` mapped to `FinishReason` (ollama.go:426-434), tool-call ids synthesized as `call_<name>_<i>` (ollama.go:443). Supports `OLLAMA_HOST` resolution quirks and `OLLAMA_AUTH` ed25519 signing (`~/.ollama/id_ed25519`, ollama.go:144-173), plus implicit auth for `ollama.com`.

### 2.4 Events emitted to the agent

Only `StreamChunk` on a channel. There is no provider-level event type for usage/tokens/cost — **token counts are estimated locally** (`estimateTextTokens`, context.go:82-107), not read from provider usage blocks [V].

### 2.5 Error / retry handling

`RetryTransport` (retry.go:19-117):
- retries **idempotent methods only** — `GET/HEAD/PUT/DELETE/OPTIONS/TRACE`; **POST is never replayed** (retry.go:39-52, 119-126), so a failed chat completion is not retried at transport level.
- retryable statuses: 429, 500, 502, 503, 504 (retry.go:131-142).
- backoff: `InitialDelay(500ms) * 2^attempt` + 10–20 % jitter, clamped to `MaxDelay` 30 s; honors `Retry-After` (seconds or HTTP date), clamped to MaxDelay (retry.go:76-107, 144-157).
- `RetryCount atomic.Int64` is a **process-global counter**, read as a delta per turn by metrics (retry.go:16, metrics.go:60/115).
- Copilot adds its own 401/403 → invalidate token → one re-exchange retry (copilot.go:373-382).

Non-200 bodies are capped at 16 KiB (gateway_proxy.go:20) / 8 KiB (copilot) and surfaced verbatim in the error string.

### 2.6 Auth

- Gateways: `APIKey` inline, else `os.Getenv(APIKeyEnv)`, else no `Authorization` header (gateway_proxy.go:48-63).
- Anthropic: same `APIKey`/`APIKeyEnv` pattern (main.go:822-834 shows `doctor` reading both).
- Copilot: GitHub OAuth token from env (`OAuthTokenEnv`, default `GITHUB_COPILOT_TOKEN`) or a token file; exchanged for a short-lived JWT with a cached API base URL, invalidated on 401 (copilot.go:355-384, `p.token(ctx)`). `gocode auth copilot` implements the device/CLI flow (`cmd/gocode/auth_cmd.go`, 227 lines).
- Ollama: optional ed25519 challenge signing (see above).

### 2.7 Custom provider add/remove

`gocode provider add <name> --base-url ... --model ... [--api-key-env ...]`:
- name pattern `^[a-z][a-z0-9_-]{0,31}$` (provider_cmd.go:18), must not collide with a built-in (provider_cmd.go:100-102);
- base URL validated by `config.ValidateProviderBaseURL` (provider_cmd.go:125);
- persisted under `[provider.custom.<name>]` (provider_cmd.go:107-111);
- registered as `provider.NewGatewayProxyProvider(name, endpoint)` (provider_cmd.go:175), **after** built-ins so it shadows them;
- `provider remove` refuses to remove the configured default (provider_cmd.go:242-244).

Custom providers are therefore **OpenAI-compatible only** — there is no way to plug a non-OpenAI dialect without code. [V]

---

## 3. Tools and approval

### 3.1 Registry

`tools.Registry` (tool.go:61-98) is a `map[string]Tool` with:
- `Register` **rejects duplicate names** (`tool name collision`, tool.go:71-78);
- `Get`, `Specs` (unordered — map iteration), `List` (unordered).

`Tool` interface (tool.go:15-19): `Spec() ToolSpec`, `Execute(ctx, json.RawMessage) (Result, error)`, `RequiresApproval() bool`.
Optional interfaces:
- `Previewer { Preview(ctx, args) (Preview, error) }` (tool.go:22-24) — approval preview;
- `ReadOnlyTool { ReadOnly() bool }` (approval.go:47-49) — per-turn read-only grant + concurrent execution.

`Result{Output, Error, Diff}` (tool.go:48-52); `String()` renders `Error: …` or the output (tool.go:54-59).

There is **no** tool middleware/hook chain, no per-tool timeout field, no tool-schema validation step, and no risk metadata beyond `RequiresApproval` + `ReadOnly`. [V]

### 3.2 The 7 built-ins

| Tool | File | Approval | Notes |
|---|---|---|---|
| `file_read` | file_read.go | **no** (:57) | directory listing or file content; 1-based `offset`/`limit`; whole-file cap **10 MiB** (`defaultMaxFileBytes`, :146); ranged reads stream instead of rejecting |
| `code_search` | code_search.go | **no** (:75) | regex (falls back to literal on compile error), `glob`, `case_sensitive`, `max_results` (default 50), `context_lines`; skips files > **4 MiB** and scans at most **200 000 lines/file** (:282-284); symlink/hardlink guarded |
| `file_write` | file_write.go | **yes** | create/overwrite; Myers unified diff via `gotextdiff` (:184-263); snapshot + hash check before writing |
| `file_patch` | file_patch.go | **yes** | `find`/`replace` exact string, **first occurrence only**, reports how many were left (:120-122); Preview records a snapshot and Execute refuses if the file changed since approval (:90-99) |
| `shell_exec` | shell.go | **yes** | see 3.4 |
| `plan` | plan.go | **no** (:144) | replaces the whole checklist; statuses `pending` / `in_progress` / `done`; `PlanStore.Render` emits `- [x] (id) text` |
| `scratchpad` | scratchpad.go | **no** (:108) | `mode` = `replace` or `append`; hard cap **8192 bytes** (`MaxScratchpadBytes`, :13) |

MCP tools are added at runtime as `mcp_<server>_<tool>` (Q8). Runtime tool count = 7 + N(MCP).

### 3.3 Approval flow and the y/a/r/e/n grants

`ApprovalChoice` (approval.go:19-34): `Deny`, `Once`, `GrantExact`, `ReadOnlyTurn`, `Edit`.

Terminal prompt (approval.go:345):

    Approve? [y]es / [a]llow exact args this session / [r]ead-only tools this turn / [e]dit / [n]o:

`parseApprovalResponse` (approval.go:356-369) maps `y|yes`, `a|all|allow`, `r|read|readonly|read-only`, `e|edit`; **anything else = deny**.

Grant semantics:
- **`a` (exact args)**: key = `sha256(toolName \0 canonicalArgs)` where `canonicalArgs` re-marshals JSON with `UseNumber` so key order/whitespace cannot split a grant (approval.go:128-154). In-memory only, never persisted, dies with the process; different args re-prompt. Result is prefixed `(session grant)` so the transcript records why no prompt appeared (approval.go:538-546).
- **`r` (read-only this turn)**: sets a flag cleared by `BeginTurn()` at the top of every `RunTurn` (approval.go:119-123; loop.go:179-181). Only tools implementing `ReadOnly()` run; the pending call is denied if it is not read-only (approval.go:494-501).
- **`e` (edit)**: plain mode only — `EditArgs` reads one JSON object from the loop's shared stdin reader (loop_plain.go:26, 93-106). The gate validates JSON, regenerates the preview, and re-prompts **exactly once** (approval.go:258-296). **The TUI has no argument editor** (tui/approval.go:44-46; model.go:464-466) — an explicit parity gap.

Decision order: config `deny` wins over everything; `auto_approve` and `!RequiresApproval()` skip the prompt; auto decisions reach `OnDecision(..., auto=true)`.

Panic containment: `executeToolRecovered` / `previewToolRecovered` turn a tool panic into an `Error` result instead of crashing (approval.go:549-568).

### 3.4 shell_exec specifics

- **Timeout**: `[tools.shell] timeout_seconds`, default 30 s (shell.go:238-243); enforced by `context.WithTimeout` **and** whole-process-group kill (`configureShellProcess` + `interruptShellProcess`, 2 s grace, then `killShellProcess`; shell.go:174-206).
- **Output caps**: separate **1 MiB** cap per stream (shell.go:245-249); excess is discarded but flagged, then `\n... (output truncated)` appended (shell.go:119-126). `WaitDelay = 2s` bounds `Wait` against an inherited pipe (shell.go:107-108).
- **Env scrub**: `cmd.Env = ScrubbedEnv(s.EnvScrub)` (shell.go:101). `essentialEnvVars` allowlist (PATH/HOME/SYSTEMROOT/…, env.go:11-32) always survives; `LC_*` survives; anything matching `*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `*_PASSWD`, `*_KEY`, `*_ACCESS_KEY`, `*_PRIVATE_KEY`, `*_CREDENTIALS` is dropped (env.go:37-47), plus configured `api_key_env` values and `[tools.shell] env_scrub` (`config.SecretEnvNames`, config.go:358-382).
- **Redaction**: 18 regex families (shell_guard.go:24-48) — OpenAI/Anthropic/GitHub/Google/Slack/AWS/Stripe/SendGrid/npm/PyPI/DigitalOcean/JWT/PEM blocks plus a generic `assigned-secret` rule for `api_key|secret|password|token = …`. Applied to stdout/stderr when `redact_secrets` is on (shell_guard.go:81-86) and **unconditionally** at the transcript boundary (`redactToolResult`, approval.go:407-423).
- **Confinement**: the shell is **explicitly not confined** to the workspace — the tool description says so (shell.go:31) and `ShellGuard` is advisory only (shell_guard.go:12-13). `FlagSensitivePaths` adds a warning listing sensitive paths the command mentions (shell_guard.go:89-149).
- **Platforms**: `cmd /C` on Windows, `sh -c` elsewhere (shell.go:89-94); process-group helpers are per-OS (`shell_process_windows.go` / `shell_process_unix.go`).

### 3.5 Diff preview generation

`generateUnifiedDiff(filename, old, new)` (file_write.go:184-263) uses `gotextdiff/myers` + `span`, then **renders hunks by hand** because gotextdiff's own `Format` omits zero counts (file_write.go:213-215). New files get a `/dev/null`-style header (:194). `file_patch` reuses it (file_patch.go:65, 101). The diff flows `Result.Diff` → `LoopEvent.Diff` (loop.go:823) → TUI `agentToolMsg.diff` → coloured `renderDiff` (model.go:740-761).

### 3.6 Path confinement

`ValidatePath(workspaceRoot, requested)` (workspace.go:230-277):
1. backslashes normalized; relative paths joined to root;
2. lexical containment (`checkPathWithinRoot`);
3. `rejectNormalisationForms` refuses NTFS ADS (`::`, stray colons), trailing space, trailing dot (workspace.go:62-84);
4. resolves the **nearest existing ancestor** with `EvalSymlinks` and re-checks containment, so symlinked workspaces work while symlink escapes are rejected (:262-274).

Sensitive files: `ignore.SensitiveMatcher` name patterns **plus hardlink identity** — `SensitiveIdentitySet` walks the workspace and stats `~/.ssh/id_rsa`, `~/.aws/credentials`, `~/.kube/config`, `~/.netrc`, `~/.npmrc`, cached 30 s per (root, matcher) and invalidated on write (workspace.go:15-227).

### 3.7 safetext

`safetext.Sanitize` (safetext.go:19-49) strips C0 (except `\n`,`\t`), DEL, C1, and every ANSI form (CSI/OSC/DCS/SOS/PM/APC/two-byte escapes, lone ESC). It is applied at every render boundary: approval prompt (approval.go:302-303), plain renderer (loop_plain.go:110-111), TUI stream ingestion (model.go:296), tool text/labels/diff (model.go:336-362), modal args/preview (model.go:688-706), error text (model.go:326).

---

## 4. TUI (Bubble Tea v2)

### 4.1 Complete file inventory

| File | Lines | Purpose |
|---|---|---|
| `model.go` | 827 | Bubble Tea model: `Model` struct, `Init/Update/View`, all message handling, status bar, help line, message + diff rendering, approval modal, `placeModal` compositor |
| `run.go` | 230 | `Run(...)`: builds the engine, wires `Observe`→`tea.Msg`, engine goroutine, cancel plumbing, startup model-picker collection |
| `anim.go` | 90 | animation primitives: `frameMsg`, `tickUntil`, `spring` wrapper over `harmonica.Spring`, `blink`, `stripANSI` |
| `mascot.go` | 245 | mascot sprite: states, faces, spring bob/sway, `inline()` (status bar) and `hero()` (banner) |
| `approval.go` | 56 | `ApprovalBridge` — channel handshake between `ApprovalGate.OnPresentChoice` and the modal |
| `picker.go` | 147 | `ModelItem` + `bubbles/list` model picker construction/styling |
| `banner.go` | 82 | empty-transcript banner (hero mascot + version/provider/model chips) and compact fallback |
| `styles.go` | 224 | every Lip Gloss style token (chrome, bubbles, modal buttons, mascot, banner chips); no hex |
| `theme.go` | 134 | `palette` struct, light/dark resolution, `GOCODE_THEME` override; single source of colour |
| `capability.go` | 69 | one-shot terminal capability detection: `NoColor`, `ForceColor`, `IsTTY`, `Dumb`, `ReducedMotion` |
| 15 test files | ~2.4k | anim, anim_ticker, fixes, grants, mascot (antenna/blink/motion/portrait/turn/wave), model, picker, run, sanitize, statusbar |

### 4.2 Model structure

`Model` (model.go:62-110): `width/height/ready`, `viewport viewport.Model`, `messages []ChatMessage`, `streamBuf *strings.Builder`, `textarea textarea.Model`, `focused`, `providerName/modelName/version/workspaceRoot/streaming`, `mascot mascot`, `animating`, `animEpoch`, `modelPickerActive`, `modelPicker list.Model`, `approvalActive/approvalReq/approvalFocus/sessionGrants`, `bridge *ApprovalBridge`, `inputCh chan string`, `outputCh chan tea.Msg`, `cancelCh chan struct{}`, `agentSession *agent.Session`, `registry *provider.Registry`, `pendingTools map[string]int`, `cancelRequested`.

`ChatRole`: `RoleUser, RoleAssistant, RoleTool, RoleSystem, RoleError` (model.go:24-32); `ChatMessage{Role, Label, Content, Diff}`.

`View()` returns `tea.View` and **must** re-declare `AltScreen = true` and `MouseMode = tea.MouseModeCellMotion` on every return path (model.go:511-522).

Layout: status bar + viewport + input area, then the help line; modal/picker composited on top by `placeModal` (model.go:524-547, 763-784) — clamp with MaxWidth/MaxHeight, centre via `lipgloss.Place`, overlay via `NewCompositor`/`NewLayer` at Z=1.

### 4.3 Agent → UI message types

| UI msg | Declared | Produced by | Meaning |
|---|---|---|---|
| `agentChunkMsg{delta}` | model.go:41 | `EventDelta` (run.go:155-156) | streamed assistant text (sanitized at ingestion) |
| `agentDoneMsg{err}` | model.go:42 | engine goroutine after `RunTurn` (run.go:214) | turn finished |
| `agentToolMsg{name,result,isError,toolCallID,diff}` | model.go:43-49 | `EventToolResult`, and `EventError`/`EventNotice` renamed "GoCode" (run.go:159-164) | tool result / notice / error |
| `agentToolStartMsg{name,args,toolCallID}` | model.go:52-56 | `EventToolStart` | pending tool card |
| `agentExitMsg{notice}` | model.go:59 | `EventExit` | render then quit |
| `approvalRequestMsg{req}` | model.go:60 | `pollApproval` on `bridge.RequestCh` (model.go:823-827) | approval modal request |
| `frameMsg{at,epoch}` | anim.go:20-23 | `tea.Tick` via `tickUntil` | animation frame |

`EventTurnEnd` has no UI mapping (run.go:153-170) and is never emitted.

### 4.4 Streaming render

`agentChunkMsg` → sanitize → append to `streamBuf` → `setStreamingMessage` rewrites the **last** message when it is `RoleAssistant`, else appends one (model.go:293-301, 794-805). Every chunk calls `viewport.SetContent(renderMessages())`, i.e. the **whole transcript is re-rendered per token** [V; performance-relevant for long sessions]. Autoscroll only when `viewport.AtBottom()` (model.go:298-300).

Tool cards: `agentToolStartMsg` appends a `RoleTool` card with `running…` + args truncated to 200 chars and records `pendingTools[toolCallID] = index` (model.go:332-354); `agentToolMsg` **replaces that card in place** (model.go:369-378), so cards mutate instead of duplicating.

### 4.5 Keybindings (full table from source)

| Key | Context | Action | Source |
|---|---|---|---|
| `ctrl+l` | normal | open model picker | model.go:224-228 |
| `ctrl+l` / `esc` | picker | close picker | model.go:194-196 |
| `enter` | picker | select → sends `/provider X` then `/model Y` on `inputCh` | model.go:197-212 |
| `ctrl+c` | streaming | 1st cancels the turn, 2nd quits | model.go:230-239 |
| `ctrl+c` | idle | quit | model.go:239 |
| `esc` | streaming | same as ctrl+c (cancel, 2nd quits) | model.go:241-249 |
| `esc` | idle | clear textarea | model.go:250-252 |
| `enter` | idle | submit (ignored while streaming) | model.go:255-277 |
| `shift+enter` | textarea | newline (`KeyMap.InsertNewline.SetKeys`, :120) | model.go:120 |
| `up` / `down` | textarea unfocused | scroll viewport ±3 | model.go:279-286 |
| `pgup` / `pgdown` | any | scroll half a page | model.go:287-290 |
| `left`/`right`/`tab` | approval modal | toggle Approve/Deny focus | model.go:425-430 |
| `enter` | approval modal | confirm focused button | model.go:431-435 |
| `y` / `n` | approval modal | approve once / deny | model.go:436-439 |
| `a` | approval modal | grant exact args for the session | model.go:440-449 |
| `r` | approval modal | read-only tools this turn | model.go:450-451 |
| `esc` | approval modal | deny | model.go:452-453 |
| `ctrl+c` | approval modal | deny + quit | model.go:454-459 |

Typing `exit`/`quit`/`/exit`/`/quit` also quits (model.go:263-266).

**Absent:** no command palette (Ctrl+P/K), no Tab completion, no prompt history (Up is viewport scroll), no Ctrl+J, no user-configurable keymap. [V by absence in model.go]

### 4.6 Animations that exist today

One animated element: the **mascot**, plus the textarea's blink cursor supplied by bubbles.

- `harmonica` springs: `bob` (fps 20, freq 5.2, damping 0.55) and `sway` (fps 20, freq 3.1, damping 0.7) (mascot.go:67-76); `spring.step` → `oscillator.Update` (anim.go:46-56).
- Cadence: working **20 fps**, thinking **10 fps**, everything else **5 fps** (mascot.go:79-88).
- Amplitude ramp `m.amp += (target - m.amp) * 0.12` so state changes ease (mascot.go:128, 141).
- Phase: fixed `rate() = 0.03` per frame, triangle wave, springs smooth it (mascot.go:143-158).
- Blink: every 4 s, 110 ms closed, idle/thinking only (mascot.go:74, 164-176).
- Ticker lifecycle: `animEpoch` increments per turn; a `frameMsg` re-arms only when `m.animating && msg.epoch == m.animEpoch` (model.go:402-407; anim.go:35-37). `animating` set on submit (model.go:273) and cleared on `agentDoneMsg` (model.go:316).
- Idle cost: `Init` starts a tick at `cadence(mascotIdle)` = **5 fps even when idle** (model.go:143-150) — the process never fully idles while the TUI is open.
- `capabilities.ReducedMotion` is detected (capability.go:35) but **never read** anywhere else [V].
- `palette.shimmerDim` / `shimmerBright` are defined (theme.go:59-60, 106-107) and **unused** [V].

No spinner widget, no shimmer text, no modal transition, no fade, no gradient, no progress bar.

### 4.7 Overlays, picker, status line, scrolling, plain fallback

- **Model picker**: `bubbles/list` with fuzzy filtering, title "Select Model", rows show model + `Provider: x`; `enter` selects (picker.go:90-146). Items are gathered **once at startup**, concurrently for every provider with a 2 s per-provider timeout and a 3 s overall deadline (run.go:89-125) — the list never refreshes.
- **Approval modal**: title, tool, decoded arg pairs (values >120 chars elided), preview body, Approve/Deny buttons, `a`/`r` hints, granted-tool list (model.go:681-738).
- **Status line**: inline mascot + `│` + provider + `│` + model (truncated) + `│` + state while streaming; workspace basename right-aligned when it fits. Narrow-width drop order: workspace first, then model below `minModelWidth = 12` (model.go:549-587).
- **Scrolling**: viewport only (PgUp/PgDn/Up/Down). `MouseModeCellMotion` is enabled but **no `tea.MouseMsg` case exists**, so wheel events do nothing [V].
- **Plain fallback** (`--tui=false`): `AgentLoop.Run` — a `bufio` REPL with `> `, deltas printed as they arrive, `[tool result]` blocks, `[GoCode]` errors, no colour and no history (loop_plain.go:18-131).

---

## 5. Sessions

### 5.1 SQLite schema (store.go:118-151)

```sql
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  workspace TEXT NOT NULL DEFAULT '',
  scratchpad TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_calls TEXT,            -- JSON array of provider.ToolCall
  tool_call_id TEXT,
  reasoning_content TEXT,
  created_at DATETIME NOT NULL,
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX idx_messages_session ON messages(session_id);
CREATE INDEX idx_sessions_updated ON sessions(updated_at DESC);
```

Pragmas `busy_timeout=5000`, `foreign_keys=ON`, `journal_mode=WAL`; pool pinned to **one connection** so pragmas cannot be bypassed (store.go:65-78). DB + `-wal` + `-shm` forced to `0600` on every open (store.go:84-96).

Schema evolution is additive only — `addMissingColumns` probes `PRAGMA table_info` and `ALTER TABLE … ADD COLUMN` (store.go:154-187). **There is no schema-version table** [V].

### 5.2 Resume logic

- Startup: `selectSessionForResume` (main.go:471-479) — explicit `--session <id>` wins; otherwise `GetLastSessionForWorkspace(root)` so another repo's transcript is never replayed (store.go:462-484; workspace lowercased on Windows).
- Resumed session: provider/model from the record unless `--provider/--model` override (main.go:290-327), messages loaded, scratchpad restored (main.go:329-334).
- **The system prompt is always rebuilt for the current workspace** — the stored prompt is replaced on the first `RunTurn` via `EnsureSystemPrompt` (loop.go:166-173) and explicitly on `/resume`/`/sessions <id>` (commands.go:293-297).

### 5.3 Export

`session.Export` → `ExportMarkdown` | `ExportJSON` (export.go:59-77):
- **System messages and reasoning content are always excluded** (export.go:100-103, 183-186).
- JSON is versioned (`Version: 1`), pretty-printed, `SetEscapeHTML(false)` (export.go:30-39, 143-151).
- `IncludeTools=false` drops `tool` messages, tool-call blocks, and assistant messages that were only tool calls (export.go:136-138).
- Tool-call args pass through `RedactToolArgs`, with a JSON-shaped fallback that redacts string values individually if a pattern spanned structure (export.go:276-330).
- Markdown fences widen when content contains 3+ backticks (`wrapCodeFence`, export.go:364-395).

CLI: `gocode session export [id] [-f markdown|json] [-o file] [--force] [--no-tools]` (session_cmd.go:32-73); refuses overwrite without `--force` and unlinks a symlink rather than writing through it (session_cmd.go:137-190).

### 5.4 `/sessions`, `/new`, `/clear`

| Command | Behaviour | Source |
|---|---|---|
| `/sessions` | last **20** sessions: id, provider, model, msg count, local time; `*` marks current | commands.go:159-189 |
| `/sessions <id>` | resume | commands.go:160-161 |
| `/new` | creates a fresh DB row, clears history + scratchpad, re-adds a fresh system prompt | commands.go:137-157 |
| `/clear` | clears in-memory history, re-adds the system prompt, and **writes an empty transcript over the session row** (destructive) | commands.go:78-86; session.go:215-235 |

---

## 6. Slash commands and CLI subcommands

### 6.1 Slash commands (complete)

Dispatch is one `switch` on the lowercased first field: `RunTurn` → `handleSlashCommand` → `agent.HandleCommand` (loop.go:222-235; commands.go:35-274). Unknown `/foo` → `unknown command %q — type /help` (commands.go:271-272). There is no command registry: adding a command means editing that switch.

| Command | Defined at | Behaviour |
|---|---|---|
| `/help` | commands.go:57-76 | hand-maintained help string duplicating the switch (all 13 commands listed) |
| `/clear` | commands.go:78-86 | clear history + rebuild prompt + persist empty |
| `/compact` | commands.go:88-123 | model-summarized compaction, fallback to placeholder |
| `/stats` | commands.go:125-135 | `Context: N / M tokens (x%) \| k messages` |
| `/new` | commands.go:137-157 | new session row |
| `/sessions` | commands.go:159-189 | list (max 20) |
| `/resume <id>` | commands.go:191-195 | resume |
| `/commit [msg]` | commands.go:197-198 → 317-384 | `git add -- <file>` per tracked file, then `git commit -m "<msg>\n\nAssisted-by: GoCode:<model>" -- <paths>`; approval prompt unless unanswered |
| `/providers` | commands.go:200-210 | active + all names + active provider's models |
| `/provider <name>` | commands.go:212-226 | switch + list models |
| `/model [name]` | commands.go:228-269 | show; with arg validates against the live model list and **fails closed** |
| `/exit`, `/quit` (and bare `exit`/`quit`) | commands.go:44-46 | exit via `EventExit` |

Absent: `/undo`, `/diff`, `/init`, `/review`, `/mcp`, `/theme`, `/keymap`, `/export`, `/cost`, `/tokens`, `!` shell passthrough. [V]

### 6.2 Cobra CLI

Root `gocode` (main.go:54-72), flags: `--provider`, `--model`, `--config` (persistent), `--workdir`, `--tui` (default **true**), `--new`, `--session`, `--headless`, `--policy`, `-v/--verbose`, `--version` (main.go:61).

| Subcommand | Flags | Source |
|---|---|---|
| `gocode doctor` | — | main.go:74-79, 767-864 |
| `gocode logs` | `-n/--tail` (50), `-f/--follow` | main.go:114-121, 656-713 |
| `gocode mcp add <name> <command> [args...]` | `DisableFlagParsing`; manual `--config`/`-v` extraction | main.go:86-93, 495-576 |
| `gocode mcp list` | — | main.go:95-99, 614-633 |
| `gocode mcp remove <name>` | — | main.go:101-106, 635-654 |
| `gocode session export [id]` | `-f/--format`, `-o/--output`, `--force`, `--no-tools` | session_cmd.go:24-73 |
| `gocode provider add <name>` | `--base-url`*, `--model`*, `--api-key-env` | provider_cmd.go:29-61 |
| `gocode provider list` | — | provider_cmd.go:63-68, 180-221 |
| `gocode provider remove <name>` | — | provider_cmd.go:69-74, 233-251 |
| `gocode auth copilot` | `--client-id`*, `--timeout` (10m) | auth_cmd.go:46-61 |

`--headless` wins over `--tui` (main.go:453-457). Protocol: stdin `{"type":"user","text":…}` / `{"type":"exit"}`; stdout `delta`, `notice`, `tool_start` (raw args), `tool_result` (`is_error` always present), `error`, `turn_done`; 1 MiB line cap (headless.go:19, 102-203). Policy JSON `{"auto_approve":[…],"deny":[…]}`; everything not allowlisted is denied and the present hook is a hard deny (headless.go:53-59, 117-120).

---

## 7. Config

### 7.1 Full `config.toml` surface

```toml
[provider]
default = "ollama"

[provider.ollama]        host = "http://127.0.0.1:11434"; default_model = ""
[provider.omniroute]     base_url = "http://127.0.0.1:20128/v1"; default_model = "auto"
[provider.openai]        base_url; api_key; api_key_env="OPENAI_API_KEY"; default_model="gpt-4o"
[provider.gemini]        ... "GEMINI_API_KEY" / "gemini-2.5-flash"   # OpenAI-compat endpoint
[provider.groq]          ... "GROQ_API_KEY" / "llama-3.3-70b-versatile"
[provider.openrouter]    ... "OPENROUTER_API_KEY" / "anthropic/claude-sonnet-4.5"
[provider.anthropic]     ... "ANTHROPIC_API_KEY" / "claude-sonnet-4-20250514"
[provider.qwen]          ... "DASHSCOPE_API_KEY" / "qwen-max"
[provider.kimi]          ... "MOONSHOT_API_KEY" / "moonshot-v1-8k"
[provider.hermes]        ... "HERMES_API_SERVER_KEY" / "hermes-agent"
[provider.xai]           ... "XAI_API_KEY" / "grok-code-fast-1"
[provider.mistral]       ... "MISTRAL_API_KEY" / "mistral-large-latest"
[provider.minimax]       ... "MINIMAX_API_KEY" / "MiniMax-M2.5"
[provider.deepseek]      ... "DEEPSEEK_API_KEY" / "deepseek-chat"
[provider.together]      ... "TOGETHER_API_KEY" / "Qwen/Qwen3-Coder-480B-A35B-Instruct"
[provider.fireworks]     ... "FIREWORKS_API_KEY" / "accounts/fireworks/models/kimi-k2-thinking"
[provider.cerebras]      ... "CEREBRAS_API_KEY" / "qwen-3-coder-480b"
[provider.zhipu]         ... "ZHIPU_API_KEY" / "glm-4.6"
[provider.nvidia]        ... "NVIDIA_API_KEY" / "qwen/qwen3-coder-480b-a35b-instruct"
[provider.copilot]       oauth_token_env="GITHUB_COPILOT_TOKEN"; default_model="gpt-4.1";
                         editor_version="vscode/1.111.0"; editor_plugin_version="copilot-chat/0.40.0"
[provider.custom.<name>] base_url; api_key; api_key_env; default_model

[permissions]
auto_approve = ["file_read"]
deny = []
sensitive_patterns = []

[session]
persist = true

[tools]
max_tool_iterations = 0        # 0 -> 50
max_repeated_tool_calls = 0    # 0 -> 3
max_context_tokens = 0         # 0 -> 80% of the active model's window
verify_command = ""            # empty disables auto-verify
verify_timeout_seconds = 120

[tools.shell]
timeout_seconds = 30
max_output_bytes = 1048576
redact_secrets = true
env_scrub = []

[mcp.servers.<name>]
command = "…"; args = […]; env = { K = "V" }; request_timeout_seconds = 0   # 0 -> 60
```
[V] struct tags config.go:16-133; defaults config.go:136-263.

Validation: **unknown keys are a hard error** (`md.Undecoded()`, config.go:444-457); base URLs must be absolute http(s), without userinfo/query/fragment, and **https unless loopback** (config.go:310-341); inline `api_key` warns (config.go:345-353); file chmod `0600` on load and save (config.go:396, 487-489).

### 7.2 Platform dirs (paths.go)

| Purpose | Windows | Unix |
|---|---|---|
| Config | `%APPDATA%\gocode` | `$XDG_CONFIG_HOME/gocode` or `~/.config/gocode` |
| Data | `%LOCALAPPDATA%\gocode` | `$XDG_DATA_HOME/gocode` or `~/.local/share/gocode` |
| Sessions | `<DataDir>/sessions/sessions.db` | same |
| Logs | `<DataDir>/logs/gocode.log` | same |
| Config file | `<ConfigDir>/config.toml` | same |
| Copilot token | `<ConfigDir>/copilot_token` | same |

All dirs `0700`, re-chmodded on every `EnsureDirs` (paths.go:67-84).

### 7.3 Environment variables

`GOCODE_THEME`, `NO_COLOR`, `CLICOLOR_FORCE`, `TERM`, `GOCODE_REDUCED_MOTION`, `REDUCED_MOTION`; `OLLAMA_HOST`, `OLLAMA_AUTH`; every configured `api_key_env` (`OPENAI_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY`, `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `DASHSCOPE_API_KEY`, `MOONSHOT_API_KEY`, `HERMES_API_SERVER_KEY`, `XAI_API_KEY`, `MISTRAL_API_KEY`, `MINIMAX_API_KEY`, `DEEPSEEK_API_KEY`, `TOGETHER_API_KEY`, `FIREWORKS_API_KEY`, `CEREBRAS_API_KEY`, `ZHIPU_API_KEY`, `NVIDIA_API_KEY`), `GITHUB_COPILOT_TOKEN`, plus `APPDATA`/`LOCALAPPDATA`/`USERPROFILE`/`XDG_*`.

No `GOCODE_CONFIG`, `GOCODE_MODEL`, or `GOCODE_HOME`. [V]

---

## 8. MCP

### 8.1 Lifecycle

1. **Config → manager**: `mcp.NewManager(cfg.MCP.Servers)` with `EnvScrub = config.SecretEnvNames(cfg)` (main.go:416-417).
2. **Start all** (`StartAll`, manager.go:94-161), per server: spawn → `Initialize` handshake bounded by **10 s** (`initTimeout`, manager.go:27) → `ListTools` (also 10 s) → register each tool.
3. **Failure policy**: a failed server is logged to stderr and skipped; other servers still start; all failures are joined and returned (manager.go:160) and main prints one warning (main.go:418-421). Startup never blocks the CLI.
4. **Call**: `MCPTool.Execute` → `client.CallTool`; if the client is closed and the manager exists, it restarts the server once and retries the call **exactly once** (tool.go:102-117).
5. **Shutdown**: `defer mcpManager.CloseAll()` sets `closed=true`; after that `EnsureClient` refuses to restart (manager.go:167-207).

Transport: stdio JSON-RPC 2.0, one goroutine reading stdout, `pending map[uint64]chan *JSONRPCResponse` for correlation, `writeMu` for serialized writes, `doneCh` closed on exit (`client.go:73-141`). Per-request timeout configurable per server (`request_timeout_seconds`, default **60 s**) and retunable at runtime (`SetRequestTimeout`, client.go:96-109).

### 8.2 Tool registration and naming

`MCPTool.Spec()` (tool.go:50-68) builds the name as:
- prefix `mcp_` is **reserved** and always applied — an MCP server cannot shadow a built-in;
- server name is prepended unless the tool already starts with `<server>_` (avoids `srv_srv_foo`).

Result: `mcp_<server>_<tool>` (matches CHANGES v0.7.0). Description is prefixed `[MCP: <server>]`.

### 8.3 Limits

| Limit | Value | Source |
|---|---|---|
| Tools per server | **200** — server over the cap registers **none** | manager.go:20, 125-132 |
| Tool name length | 128 bytes | manager.go:21 |
| Tool description | 8 KiB | manager.go:22 |
| Tool input schema | 32 KiB | manager.go:23 |
| Handshake / ListTools timeout | 10 s | manager.go:27 |
| Per-request timeout | 60 s default, per-server override | client.go:98 |
| Protocol frame | 16 MiB `Content-Length` ceiling (`maxMessageSize`); oversized frames drop the connection | client.go:178-220 |

Oversized individual tools are skipped (not fatal) and counted into a joined warning (manager.go:136-157).

### 8.4 Approval and read-only

MCP tools always `RequiresApproval() == true` (tool.go:70-72). `ReadOnly()` reflects the server's `annotations.readOnlyHint` (tool.go:74-79) and is used **only** to honour an explicit "read-only tools this turn" choice or to run concurrently — a lying hint still faces the per-call prompt. Preview shows server/tool plus indented args (tool.go:81-100).

Env: MCP children get the same credential scrub as shell children (`NewClientWithEnvScrub`, client.go:129-141).

---

## 9. Tests and quality — measured baseline

### 9.1 Commands run (this audit)

| Command | Result |
|---|---|
| `go version` | `go version go1.26.11 windows/amd64` |
| `go build ./...` | **exit 0**, no output |
| `go vet ./...` | **exit 0**, no output |
| `go test ./...` (first pass) | **exit 0**; 11 packages `ok`, 1 `[no test files]` |
| `go test ./... -count=1 -v` | **816** `--- PASS` lines (incl. subtests), **0** FAIL, **23** SKIP, **11** packages `ok` |

Per-package `ok` timings (first pass): cmd/gocode 1.746s, agent 1.718s, config 0.696s, eval 0.874s, ignore 1.092s, mcp 2.301s, provider 7.635s, safetext 1.092s, session 1.802s, tools 12.516s, tui 4.354s.

No failures to quote verbatim — the run is green.

### 9.2 Test inventory (files / top-level `Test` functions)

| Package | Test files | Test funcs |
|---|---|---|
| `cmd/gocode` | 12 | 60 |
| `internal/agent` | 19 | 132 |
| `internal/config` | 2 | 23 |
| `internal/eval` | 1 | 5 |
| `internal/ignore` | 2 | 9 |
| `internal/mcp` | 3 | 23 |
| `internal/provider` | 7 | 72 |
| `internal/safetext` | 1 | 2 |
| `internal/session` | 6 | 30 |
| `internal/tools` | 20 | 142 |
| `internal/tui` | 15 | 81 |
| `internal/version` | 0 | 0 |
| **total** | **88** | **579** |

**0 benchmarks, 0 fuzz targets** in the tree [V].

### 9.3 What is covered (by file name + content sampling)

- **agent**: loop turn behaviour, parallel read-only execution, interrupt, truncation, plain-mode grants/sanitization, history invariants (645 lines — the largest single test file), context summary/compaction, prompt injection markers, scratchpad, session prompt, verify, commands, workspace commands, loop guard, metrics.
- **tools**: approval decisions/grants/parallel/sanitize, TOCTOU write+patch, read bounds, redaction policy, identity cache, shell guard/process, code search, security, workspace, plan, scratchpad, diff, env, regression.
- **tui**: animation determinism (`anim_test.go` 721 lines, `anim_ticker_test.go`), mascot antenna/blink/motion/portrait/turn/wave, model, picker, run, sanitize, statusbar, grants, fixes.
- **provider**: registry, retry, gateway proxy (+bounds), anthropic, ollama, copilot (675 lines).
- **session**: store (+perms/reasoning/scratchpad/workspace), export (510 lines).
- **mcp**: client, client_env, robustness (450 lines — timeouts, restart, caps).
- **eval**: 5 deterministic end-to-end scenarios (see 9.4).

### 9.4 `internal/eval` purpose

A **network-free, deterministic** harness: `scriptedProvider` returns queued chunk slices per `Stream` call and records the history it was given, so assertions can check exactly what the model saw (eval_test.go:20-75). Real `AgentLoop` over a fake tool (eval_test.go:109-153). Five scenarios:

1. tool selection executes with exact args and the result is in history before the next provider call (:157);
2. truncation persists partial content and emits one notice (:234);
3. a denying gate prevents a mutating tool and the denial lands in history (:266);
4. compaction keeps summary + retained turns + tool pairing (:301);
5. resume is workspace-scoped (:367).

Run with `make eval` → `go test ./internal/eval -count=1` (Makefile).

### 9.5 Gaps in coverage / quality

- **23 skips on Windows**, all environment-driven: file-mode assertions (`Go mode bits are ignored on Windows`), symlink creation (`A required privilege is not held by the client`), hardlinks, `cmd.exe` timeout kill semantics (`verify_test.go:300`), Linux-only case sensitivity. These paths are therefore **unverified on this host**.
- No tests for `auth_cmd.go` device flow beyond unit seams, no provider live-contract tests (by design), no UI golden/screenshot tests.
- No benchmarks for the per-token full-transcript re-render (`setStreamingMessage` → `renderMessages`), no fuzz tests for the SSE parser or the approval-response parser.
- CI: `.github/workflows/ci.yml` exists (plus `release.yml`), SHA-pinned per CHANGES v0.7.4; `Makefile:test` falls back to non-race when `gcc`/`CGO_ENABLED` are absent — on this Windows host that fallback is what ran.

---

## 10. Conventions and docs

### 10.1 CHANGES.md — the actual scheme (premise correction)

**There is no phase-ID scheme (A1/B1/C1) anywhere in this repo.** [V: `grep -rInE "\b(Phase [A-Z][0-9]|[A-Z][0-9]{1,2}\b.*phase)" --include="*.md" .` returns nothing; `.hoplite/` contains only `settings.json`.]

The real scheme is **version-sectioned, newest-first**, with prose summaries plus bold-lead bullets grouped by theme:

```
### Unreleased                  (only entry as of d645368: omp research docs)
### v0.7.4 — 2026-11-13        (one-paragraph release narrative, then bold-lead bullets)
### v0.7.1 … v0.7.0 … v0.6.2 Fixes … v0.6.0 Additions … v0.5.2 … v0.2.0
```
- `### Unreleased` was added **during this audit** by the concurrent docs commit (CHANGES.md:3-5); it follows the same version-section shape.
- Sections are either "X Additions" or "X Fixes" for older versions; v0.7.x dropped the suffix.
- Each bullet is `**Theme** — sentence(s)` describing behaviour, not files.
- Detailed notes live in `docs/releases/v<version>.md` (only `v0.7.4.md` exists), which uses `## Security`, `## Harness`, `## Security remediation` headings.

Latest entries (v0.7.4 themes): Approval Integrity, Secret Containment, Bounded & Scoped Execution, Harness Upgrades, Headless & Evals, CI & Supply Chain, Security Remediation, Real Context Compaction, Model-Aware Token Budgets, Better Token Estimation, Persistent Scratchpad Tool (CHANGES.md:5-17).

### 10.2 CONTRIBUTING.md

- Commit style: **Conventional Commits** — `feat(tools): add web search tool`, topic branches `feat/my-new-feature` (CONTRIBUTING.md:64-77). No mention of scope enforcement, no changelog-update requirement, no DCO/sign-off.
- Gates before PR: `go test ./...` and `go vet ./...` (:69-73); coding guidelines add `gofmt -s -w .`, `go vet` zero issues, wrapped errors, and a hard rule that **any tool that modifies files or runs commands must return `RequiresApproval() == true`** (:180).
- Documented extension recipe: implement `tools.Tool`, register in `cmd/gocode/main.go` (:137-155); implement `provider.Provider`, register in `main.go` (:157-171).
- Stale bits: prerequisites say **Go 1.22+** (:86) while `go.mod` pins 1.26.11; architecture tree omits `session`, `mcp`, `ignore`, `safetext`, `eval`, `version` (:113-131); links point at a local `file:///d:/…` path (:139).

### 10.3 Makefile

| Target | Command |
|---|---|
| `build` | `go build -o bin/gocode ./cmd/gocode/` |
| `test` | race+coverage if `gcc` present and `CGO_ENABLED=1`, else plain `go test ./... -coverprofile=coverage.out` with an explanatory note |
| `lint` | `go vet ./...` then `gofmt -l .` (lists, does not fail on unformatted — `gofmt -l` exits 0 either way) |
| `eval` | `go test ./internal/eval -count=1` |
| `install` | `go install ./cmd/gocode/` |
| `clean` | `rm -rf bin/ dist/ coverage.out` |

### 10.4 `docs/` contents and README link check

Actual `docs/` at audit time: `SECURITY.md` (9.5 KB) and `releases/v0.7.4.md`. The concurrent commit `d645368` then added `docs/omp-reverse-engineering.md` and `docs/research/omp-{harness,tui,commands}.md` — none of which the README links to.

README links verified individually:

| Link in README | Line | Exists? |
|---|---|---|
| `docs/providers.md` | 176 | **MISSING** |
| `docs/SECURITY.md` | 287 | exists |
| `docs/configuration.md` | 329 | **MISSING** |
| `docs/architecture.md` | 353 | **MISSING** |
| `docs/AGENTS.md` | 218 | **MISSING** — but this one is *intended*: it describes the AGENTS.md **lookup path** (`docs/AGENTS.md` as a candidate file inside a user's repo), not a GoCode doc. Not a broken link, and my regex flagged it as a false positive. |

So: **three genuinely broken documentation links** — `docs/providers.md`, `docs/configuration.md`, `docs/architecture.md`.

---

## 11. Extension seams (for the omp-inspired upgrade)

Concrete places a new harness/TUI feature must attach to. Nothing here is a redesign — these are the existing joints.

### 11.1 Harness seams

| Seam | Location | Why it matters for the port |
|---|---|---|
| `AgentLoop.Observe func(LoopEvent)` | loop.go:115, emit at :131-135 | the **only** engine→UI channel. New events (steering acks, compaction notices, job/tray state, token usage) belong as new `LoopEventKind` values here. `EventTurnEnd` is already declared (loop.go:37) and unused — a free slot. |
| `LoopEvent` struct | loop.go:42-58 | currently `Kind/Text/Tool/IsError/ToolCallID/Args/Diff/Err`. Adding structured fields (token counts, cost, tool risk, elapsed) is a one-struct change that both UIs and headless see. |
| `RunTurn(ctx, input)` | loop.go:177 | one entry point for both front-ends; a steering API would sit beside it (e.g. `Steer(text)`), not inside the provider loop. |
| `runEngineGoroutine` + `inputCh chan string` (cap **1**) | run.go:55, 182-220 | the TUI can only submit one turn at a time and Enter is ignored while `streaming` (model.go:256-258). Mid-turn steering needs a queue plus a second channel or a message type. |
| `ContextManager.Truncate` | context.go:404-455 | drops whole oldest turns with **no summary** — the current answer to overflow. Better compaction replaces or precedes this. |
| `ContextManager.Compact` / `CompactWithSummary` | context.go:460 / 537 | `/compact` only; `CompactWithSummary` already has the provider call, timeout, transcript bounding and redaction hook needed for an automatic trigger. |
| `LoopGuard.CheckCall` / `LoopGuardConfig` | loop_guard.go:87 / 21-27 | per-turn iteration + repeat limits; a place to add cost/token-based stop conditions. |
| `ApprovalGate.OnPresentChoice` | approval.go:66 | the single interactive hook; a richer prompt/overlay system generalises this. `OnDecision` (approval.go:74) is a string-keyed observe hook the loop chains onto (loop.go:251-263) — a real event bus would replace that chaining. |
| `tools.Registry.Register` | tool.go:71 | the only registration path; new tools (subagent, todo, web) plug in here, and `main.go:360-395` is the wiring site. |
| `Tool` / `Previewer` / `ReadOnlyTool` | tool.go:15, 22; approval.go:47 | adding capabilities (streaming tool output, per-tool timeout, tool categories) means new optional interfaces, following the existing pattern. |
| `provider.Provider.Stream` + `StreamChunk` | provider.go:40-44, 23-32 | **tool-argument streaming** requires a new `StreamChunk` field (e.g. `ToolCallDelta`) plus emitting it from `streamOpenAISSE` (copilot.go:574-603), which currently buffers args until flush. Both the loop (loop.go:312-314) and both UIs must learn the new event. |
| `provider.Registry.Register` / `RegisterContextWindow` | registry.go:45 / 93 | provider-agnostic; a new dialect is a new `Provider` implementation registered at main.go:238-255. **Anthropic is a separate code path**, so any cross-dialect feature must be implemented twice or normalized first. |
| `agent.HandleCommand` switch | commands.go:56 | every slash command is a `case` here; there is no registry, so a command palette would either drive this switch or replace it with a table. |
| `redactToolResult` | approval.go:407-423 | unconditional transcript boundary; new tool output types must pass through it (and `RedactToolArgs` for persistence, session.go:112-131). |

### 11.2 TUI seams

| Seam | Location | Why it matters |
|---|---|---|
| `Model.Update` type switch | model.go:152-421 | the single dispatch point for every message; new keys, new overlays and steering all start here. |
| `Model.render` | model.go:524-547 | composes status bar + viewport + input + help, then overlays modal/picker. A tray/job-card region or a side panel attaches here. |
| `placeModal` | model.go:763-784 | the only overlay compositor (`lipgloss.NewCompositor/NewLayer`, Z-indexed). Stacked overlays / command palette can reuse it, but only one overlay is representable today (`approvalActive` / `modelPickerActive` are separate booleans). |
| `anim.go` `spring`/`tickUntil`/`frameMsg` + `animEpoch` | anim.go:35-56, model.go:85, 402-407 | the whole animation substrate: one tick chain, epoch-guarded. New animated widgets (spinners, progress, shimmer text) reuse `tickUntil` and need their own epoch or phase, since `animating` gates *all* frames. |
| `palette` (theme.go:14-61) | theme.go | colour is centralised; **new UI surfaces must add roles here** (and `shimmerDim/Bright` already exist unused). |
| `capabilities` | capability.go:10-47 | `ReducedMotion`, `NoColor`, `Dumb`, `IsTTY` are detected but mostly unused — the accessibility hooks exist and are unclaimed. |
| `ApprovalBridge` | tui/approval.go:20-28 | `RequestCh` is buffered **1**; a general prompt/overlay queue needs a wider channel or a dispatcher. |
| `toTeaMsg` | run.go:153-170 | the only LoopEvent→tea.Msg translation table; new event kinds must be added here or they are silently dropped. |
| `renderHelpLine` / `renderStatusBar` | model.go:599-615 / 552-587 | context-aware hint text and width budgeting already exist; a palette hint or job counter goes here. |
| model picker | picker.go, run.go:89-125 | the only picker, built from a startup snapshot; a command palette can mirror this structure but must refresh. |

---

## 12. Gap list (baseline, not the plan)

1. **No automatic compaction** — overflow silently drops the oldest whole turns (`Truncate`, context.go:404); `/compact` is manual and only summarises to `keepLastTurns=2`.
2. **No mid-turn steering** — `inputCh` cap 1 and Enter disabled while streaming (model.go:256-258); no way to inject guidance without cancelling.
3. **No tool-arg streaming** — approvals and UI appear only after a full call is parsed (copilot.go:507-518, 574-603).
4. **No structured turn lifecycle event** — `EventTurnEnd` declared, never emitted (loop.go:37); UI reconstructs completion from `agentDoneMsg`.
5. **No usage/cost in the UI** — metrics exist only as one `slog` line per turn (metrics.go:113-148); token counts are estimates, never provider-reported.
6. **TUI renders the whole transcript per token** (model.go:297, 804) — O(n) per chunk.
7. **One overlay at a time** — `approvalActive` and `modelPickerActive` are independent booleans over a single compositor (model.go:538-544).
8. **Accessibility flags unused** — `ReducedMotion` detected (capability.go:35), never read; idle tick runs at 5 fps forever (model.go:146).
9. **Mouse mode enabled, no handler** — `MouseModeCellMotion` (model.go:520) with no `tea.MouseMsg` case.
10. **No command registry** — slash commands are a hand-edited `switch` (commands.go:56); `/help` is a hand-maintained string literal (commands.go:60-75) that duplicates the switch and can drift from it.
11. **Anthropic is a parallel implementation** — cross-dialect features cost twice.
12. **Docs drift** — 3 referenced docs missing; CONTRIBUTING says Go 1.22 and lists an incomplete package tree.
13. **Windows-unverified security paths** — 23 skipped tests cover file modes, symlinks, hardlinks, and cmd.exe timeout semantics.

---

## 13. Question checklist

| Q | Status | Where | Notes |
|---|---|---|---|
| Q1 agent loop | **answered** | §1 | turn lifecycle, prompt assembly, streaming, dispatch, approval integration, both limits, abort, compaction, plan/scratchpad. One caveat: `EventTurnEnd` is declared but never emitted. |
| Q2 provider layer | **answered (one partial)** | §2 | registry, 20 built-ins, OpenAI/Ollama normalization, retry, auth, custom providers. **Partial:** Anthropic's dialect was confirmed to be a separate SSE implementation (`anthropicStreamEvent`, anthropic.go:97-127) but its field-by-field mapping was not read line-by-line. |
| Q3 tools & approval | **answered** | §3 | registry, 7 built-ins, y/a/r/e/n, diffs, shell limits/env/redaction, confinement, safetext |
| Q4 TUI | **answered** | §4 | full file inventory, model structure, message types, streaming render, complete keybinding table, animations, picker/modal/status/scrolling, plain fallback |
| Q5 sessions | **answered** | §5 | exact schema, resume, export, /sessions /new /clear |
| Q6 commands | **answered** | §6 | 13 slash commands with lines, 10 CLI subcommands with flags |
| Q7 config | **answered** | §7 | full TOML surface + defaults, dirs, env vars |
| Q8 MCP | **answered** | §8 | lifecycle, naming, 6 caps, approval/read-only, restart |
| Q9 tests | **answered** | §9 | build/vet/test exit codes, 816 pass / 0 fail / 23 skip, per-package inventory, eval purpose, coverage gaps. **Caveat:** pass counts include subtest lines; top-level `Test` functions = 579. |
| Q10 conventions | **answered (premise corrected)** | §10 | **No A1/B1/C1 phase scheme exists** — CHANGES.md is version-sectioned (an `### Unreleased` section was added mid-audit by a concurrent docs commit). Commit style = Conventional Commits. Makefile targets listed. **3 README doc links are broken** (`docs/providers.md`, `docs/configuration.md`, `docs/architecture.md`); `docs/AGENTS.md` is a false positive (it documents a lookup path in the user's repo). |
| Q11 extension points | **answered** | §11, §12 | named seams with file:line for harness and TUI, plus a 13-item gap list |

### Unresolved / deliberately out of scope

- Anthropic's exact SSE event mapping and `maxTokensForModel` table (anthropic.go:97-127) — read structurally, not field-by-field.
- `cmd/gocode/auth_cmd.go` (227 lines) device-flow internals — only its command shape and flags were read.
- No GoCode process was run against any provider (per the task constraints), so no runtime/provider verification was performed.
