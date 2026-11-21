# Architecture

GoCode is a single Go binary that runs one agent engine behind three front-ends: a
Bubble Tea TUI, a plain terminal REPL, and a JSONL headless protocol. This page
describes how the pieces fit together as the binary behaves today. For the user-facing knobs referenced
here, see [configuration.md](configuration.md); for providers see
[providers.md](providers.md); for themes see [themes.md](themes.md); for the hook
contract see [hooks.md](hooks.md); for the threat model see
[SECURITY.md](SECURITY.md).

- [Package map](#package-map)
- [One turn, end to end](#one-turn-end-to-end)
- [Queued-turn dispatch](#queued-turn-dispatch)
- [Event vocabulary and front-ends](#event-vocabulary-and-front-ends)
- [Harness subsystems](#harness-subsystems)
- [Context management](#context-management)
- [TUI architecture](#tui-architecture)
- [Session persistence](#session-persistence)
- [Security boundaries](#security-boundaries)

## Package map

| Package | Responsibility |
| :-- | :-- |
| `cmd/gocode` | Cobra CLI: flags, subcommands (`doctor`, `logs`, `mcp`, `provider`, `auth`, `session`), wiring of config → providers → tools → hooks → loop → front-end; headless JSONL protocol; session-lifecycle hook firing. |
| `internal/agent` | The engine: turn lifecycle, queued-turn dispatch, provider rounds, steering, tool batches, loop guard (iteration/repeat/signature stops), context budget/truncation/repair/anchored compaction, hook wiring, prompt assembly, slash-command registry, turn metrics, debounced persistence. |
| `internal/config` | Platform directories, TOML loading with strict key checking, defaults, base-URL validation, theme-name and permission-mode validation, hook-config normalization, secret-env enumeration. |
| `internal/eval` | Deterministic, network-free evaluation harness that scripts a provider to exercise tool dispatch, approvals, truncation, compaction and workspace-scoped resume (`make eval`). |
| `internal/hooks` | hook engine: 13 events, Claude Code-compatible stdin/stdout JSON, exit-code semantics (0/2/49/other), parallel execution with per-hook timeouts, config-order aggregation. See [hooks.md](hooks.md). |
| `internal/ignore` | `.gocodeignore` / `.gitignore` matching (gitignore semantics) and the sensitive-file pattern set. |
| `internal/mcp` | MCP client and manager: stdio JSON-RPC 2.0, tool discovery/caps, on-demand restart, scrubbed child environment. |
| `internal/provider` | Provider implementations and registry: OpenAI-compatible gateway proxy, native Anthropic, GitHub Copilot, local Ollama, retry transport, context-window table. |
| `internal/pubsub` | generic event bus: `Broker[T]` with lossy, must-deliver and coalescing delivery classes; typed wire vocabulary for process boundaries. |
| `internal/safetext` | Strips ANSI escapes and control characters from untrusted model/tool text before it reaches a terminal. |
| `internal/session` | SQLite session store (WAL, owner-only files), workspace-scoped resume queries, summary-anchor replay, transcript export and tool-argument redaction. |
| `internal/tools` | Tool registry and the seven built-ins, path confinement and sensitive-file/hardlink checks, shell execution, diff previews, the permission service (modes, danger classification, keyed grants, first-answer-wins), per-call hook approvals, secret redaction. |
| `internal/tui` | Bubble Tea v2 interface: theme system, sidebar/gutter layout, glamour markdown and banded diffs, tool cards, dialogs/badges/toasts, focus model and keymap table, completions and persistent history, shared generation clock and scramble spinner, render cache and coalesced flushing. |
| `internal/version` | Build version placeholder, injected at link time by releases. |

**`cmd/gocode`** owns process startup and nothing else. It creates the data
directories, opens the log, loads config (or `--config <path>`), builds the
provider registry (Ollama plus 17 OpenAI-compatible gateways plus native
Anthropic, Copilot, and any custom endpoints), opens the session store unless
`[session] persist = false`, resolves the workspace, resumes a session, builds
the shared hook runner (`session_hooks.go`) and fires `SessionStart` now /
`SessionEnd` on every exit path, registers the seven built-in tools and every
MCP tool, constructs one `AgentLoop`, and hands it to the selected front-end.
`--headless` wins over `--tui` so a machine-driven process never opens a TUI.
`--yolo` overrides `[permissions] mode` for the run. It also implements the
maintenance subcommands, which operate on config or the session database
directly rather than through the loop.

**`internal/agent`** is the only place turn behavior lives. `RunTurn` accepts one
line of input, decides whether it is a slash command or a model turn, and runs
provider round-trips until the model stops requesting tools. The same type serves
the TUI (`RunTurn`/`RunTurnAccepted`) and the plain REPL (`Run`), so termination
conditions, approval sequencing and context accounting cannot drift between
front-ends. It also owns prompt construction: base instructions, delimited
untrusted context, and one trailing dynamic state block.

**`internal/config`** resolves platform paths, decodes TOML over a defaulted
struct, and rejects unknown keys so a typo cannot silently disable a setting. It
validates every configured base URL at load time (absolute http(s), no
credentials/query/fragment, HTTPS unless loopback), the theme name, the
permission mode and the hook configuration, and exposes the set of
environment-variable names that must be scrubbed from child processes.

**`internal/eval`** is a test package, not a runtime dependency. It scripts a
provider with canned streams so approval gating, tool dispatch, truncation,
compaction and workspace-scoped resume can be asserted without credentials or a
network; `make eval` runs it.

**`internal/ignore`** loads the first `.gocodeignore` (or `.gitignore`) found
walking up from the workspace root and matches paths with gitignore semantics.
Case is folded on Windows and macOS. It also defines the built-in sensitive
patterns (`.env*`, SSH keys, certificates, cloud credential files, token files)
and combines them with `[permissions] sensitive_patterns`.

**`internal/mcp`** spawns configured servers as child processes, performs the
initialize handshake with a 10-second bound, lists tools, and registers each one
as `mcp_<server>_<tool>` so an MCP server can never shadow a built-in name. Tool
metadata is capped (200 tools per server; 128-byte names; 8 KiB descriptions;
32 KiB schemas). A dead server's tools stay registered and the next call restarts
it once. Child environments are scrubbed exactly like `shell_exec`'s.

**`internal/provider`** defines the `Provider` interface (`Name`, `Models`,
`Stream`) and the normalized stream chunk (text, reasoning, complete tool calls,
tool-call deltas, finish reason, optional usage). Four dialects exist: the
OpenAI-compatible gateway proxy used by 17 built-ins and every custom endpoint,
native Anthropic Messages, Copilot's token-exchanged OpenAI-compatible endpoint,
and a stdlib-only Ollama NDJSON client. The package also owns the retry transport
and the model-family context-window table.

**`internal/safetext`** removes C0/C1 controls, DEL and every ANSI escape form
(CSI, OSC, DCS/SOS/PM/APC, two-byte escapes, lone ESC) while preserving printable
Unicode, newlines and tabs. Model output, tool output, tool names, previews and
hook diagnostics are sanitized before display; sanitization is a display boundary
only, and the transcript stores the redacted result rather than the sanitized
rendering.

**`internal/session`** is the persistence layer: one SQLite connection with WAL,
foreign keys and a 5-second busy timeout, a `sessions` table with workspace and
`summary_message_id` columns, and a `messages` table holding role, content, tool
calls, tool-call id and reasoning. Auto-resume queries by normalized workspace;
replay starts at the summary anchor when one is set. Export renders Markdown or
JSON and always omits system prompts and reasoning; tool-call arguments are
redacted before they are written to disk.

**`internal/tools`** holds the tool contract (`Spec`, `Execute`,
`RequiresApproval`), the seven built-ins (`file_read`, `code_search`, `plan`,
`scratchpad`, `file_write`, `file_patch`, `shell_exec`), and the permission
service. File tools resolve every path through the workspace root, reject
traversal and normalization tricks (ADS, trailing space/dot), re-check after
symlink resolution, block sensitive/ignored paths, and refuse hardlink aliases by
inode identity (cached for 30 seconds, invalidated on write). The service
implements permission modes, the danger classifier, keyed session grants,
first-answer-wins resolution, per-call hook approvals, and the unconditional
transcript redaction of results — see [Harness subsystems](#harness-subsystems).

**`internal/tui`** is a pure view over engine events. It never calls a provider or
a tool: it sends input on a channel, receives events fanned in from the bus, and
owns rendering, input editing, overlays, and animation. See
[TUI architecture](#tui-architecture).

**`internal/version`** carries the semantic version; release builds inject it with
`-ldflags -X`.

## One turn, end to end

```text
gocode [--provider P] [--model M] [--yolo] [--tui=false | --headless]
 │
 ├── TUI (Bubble Tea) ─────┐
 ├── plain REPL ───────────┤ all three enter through the dispatch gate
 └── headless JSONL ───────┘
 │
 AcceptTurn / RunTurn (dispatcher: active | queued | cancel-on-entry)
 ├─ Approval.BeginTurn() per-turn read-only grant cleared; hook stamps cleared
 ├─ beginTurn() steering window opens; old queue dropped
 ├─ emit EventTurnStart · fire TurnStart hook
 ├─ HandleCommand(input) ────► handled: Notice/Error/Exit, one EventTurnEnd (no turn hooks)
 ├─ Session.AddMessage(user)
 └─ streamResponse
 │
 └─ every provider round:
 ├─ ctx cancelled? ─────────────────────────────► return
 ├─ drainSteering() queued text becomes user messages
 ├─ ResolveBudget(model) 80% of window, or configured override
 ├─ autoCompactNow(false) threshold/backstop → prune → summarize (PreCompact/PostCompact hooks)
 ├─ TruncateReport(history) last resort; whole turns only; pairing repaired
 ├─ openStream() ── overflow ─► compact once, retry; truncate on 2nd
 │ └─ deltas · reasoning · tool-call deltas · usage
 ├─ append assistant message (content + tool calls + reasoning)
 ├─ no tool calls ─► loop summary, post-turn compaction, return
 ├─ guard.CheckCall() ── iteration/repeat/signature limit ─► synthetic results, stop turn
 └─ handleToolCalls
 ├─ approval pass (sequential, call order):
 │ EventToolStart → PreToolUse hook (deny/halt/rewrite/allow)
 │ → Approval.Prepare for each call
 ├─ execution pass:
 │ read-only calls in parallel (max 4); mutating calls serial
 ├─ result pass (call order):
 │ PostToolUse / PostToolUseFailure hooks → redact →
 │ Session.AddMessage(tool) → EventToolResult
 └─ mutated workspace && verify_command → runAutoVerify
 └─ next round
 exit funnel: exactly one of TurnEnd / StopFailure / Interrupt hooks,
 one EventTurnEnd with metrics, debounced persistence flushed
```

`RunTurn` has one exit funnel: normal completion, cancellation, a loop-guard stop,
a provider failure and a slash command all pass through the same deferred
function, which logs the turn record, fires exactly one terminal turn hook and
emits exactly one `EventTurnEnd` with the turn's metrics. `/exit` is the only case
where `EventExit` precedes it. A hook halt is a normal termination: the turn ends
cleanly and `TurnEnd` fires.

**Input classification.** `HandleCommand` lowercases and matches the first token
against the command registry (names and aliases). A bare alias such as `exit` is
only a command when it is the whole line; `exit now` goes to the model. Anything
starting with `/` that is not registered returns an "unknown command" error
notice rather than being sent as a prompt.

**Steering.** `Steer(text)` is accepted only while a turn is in flight. A queue
survives only until the next round boundary: `drainSteering` runs before budget
resolution, compaction and truncation, so steered text is counted by all three.
Drained messages are added as ordinary user messages prefixed with `[steering]`,
and one notice (`[Queued steering applied to the next request.]`) is emitted per
batch. Steering is never injected into a stream already in progress, and a steer
that races the last boundary is discarded with the turn. Steering is the mid-turn
guidance lane; whole prompts submitted while a turn runs are **queued turns**
(below), not steering.

**One provider round.** The loop resolves the active provider and model, converts
the tool registry to provider tool specs, resolves the token budget for the
current model, runs automatic context maintenance, truncates if still necessary,
and opens the stream. Chunks are consumed until the channel closes: text deltas
are emitted and accumulated, reasoning is accumulated without display, complete
tool calls are collected, streamed tool-argument fragments are sanitized and
emitted as `EventToolDelta` for display only, and the last usage report wins.
Cancellation is checked after every chunk. Usage is recorded only for a request
that ran to completion, because a cancelled stream reports partial counts. A
cooperative cancel keeps the partial assistant output so the transcript
reflects what the user saw.

The assistant message is appended to the session whether it contains text, tool
calls, or both. If there are no tool calls, the turn ends after a loop-guard
summary (when tools ran), a truncation warning if `finish_reason` was
`length`/`max_tokens`, and one post-turn compaction pass so the next user message
starts inside the threshold. An empty response with no tool calls and no
truncation emits a notice suggesting the credentials or `/provider` be checked.

**Tool batches.** Before anything executes, `LoopGuard.CheckCall` bounds the
batch: iteration count is capped (`[tools] max_tool_iterations`, default 50),
consecutive identical batches are capped (`max_repeated_tool_calls`, default 3,
comparing canonicalized JSON), and a tool+args signature appearing in more
than 5 of the last 10 batches stops the turn even when interleaved with other
calls. A guard stop writes a synthetic result for every declared call — both
OpenAI-shaped and Anthropic APIs reject a call without a result — appends a stop
message, emits an error, and ends the turn.

Tool calls then run in three passes:

1. **Approval pass**, sequential and in call order. Each call is announced with
 `EventToolStart` (before any blocking prompt). The `PreToolUse` hook fires
 first: a deny produces a final error result for that call; a halt closes out
 the batch and ends the turn; an allow stamps a per-call approval; an
 `updated_input` patch rewrites the arguments. The call is then resolved
 against the registry, classified read-only or mutating, and passed to
 `Approval.Prepare`, which applies `deny`, the permission mode, `auto_approve`,
 the per-call hook stamp, keyed session grants, the per-turn read-only flag,
 then renders a preview and asks the user. `Prepare` returns a closure, so
 every decision for the batch is made before any tool runs. An unknown tool
 produces an error result without approval.
2. **Execution pass.** Read-only calls are dispatched to a worker set bounded at
 four; a mutating call waits for every read previously dispatched, then runs
 alone. Call-order effects and cancellation stay predictable without
 serializing independent reads. A cancelled turn interrupts every call it never
 reached.
3. **Result pass**, in call order regardless of completion order.
 `PostToolUse`/`PostToolUseFailure` hooks fire (output rewrite, context,
 feedback, halt); each result then crosses the transcript redaction boundary
 exactly once — hook-added text included — is appended as a tool message, and
 is emitted as `EventToolResult` with a diff kept separate from prose. A
 successful `plan` or `scratchpad` write rebuilds the system prompt so the next
 round sees the state the model just wrote. Displayed tool output is capped at
 2000 bytes; the model and transcript receive the full result. A post-hook halt
 ends the turn once every result is in the transcript.

**Auto-verify.** When `[tools] verify_command` is set and any approved call in the
round can mutate the workspace, the command runs once after the batch, in the
workspace, under `verify_timeout_seconds` (default 120). It deliberately skips the
approval gate — configuring it is the opt-in. Output is capped at 8 KiB and
redacted. Success emits a notice; failure emits a notice and appends a user
message so the next provider round can react to it.

**Abort paths.** Cancellation returns the context error; the front-end renders a
"stopped" line. A provider error is returned as `stream error: …` and ends the
turn. Every prepared-but-unreached call gets
`Error: tool call interrupted before it could execute.` (or the hook-halt
equivalent) so history never holds a dangling call id.

## Queued-turn dispatch

`internal/agent/dispatch.go` is the per-session authority for turn entry — one
`AgentLoop` is one session. Acceptance is split from execution:

- `AcceptTurn(text)` atomically assigns an accept-sequence and enqueues the
 prompt; `BeginAccepted()` reserves a sequence for a prompt accepted elsewhere
 (headless). Acceptance happens the moment input arrives, even mid-turn.
- `RunTurnAccepted(ctx, text, seq)` re-tests entry under the dispatcher mutex and
 decides one of three states:
 1. **cancel-on-entry** — the accept-sequence is ≤ the session's cancel
 high-water mark: the prompt never enters the stream. A cancelled turn row
 is persisted and a terminal cancelled event is emitted.
 2. **queued** — a turn is already active: the prompt waits in the FIFO queue
 and the entry returns immediately.
 3. **active** — the call registers as the running turn (with its own cancel
 func) and executes the full turn machinery.
- `CancelTurns()` raises the cancel mark (covering prompts accepted but not yet
 entered), cancels the active turn's context, and settles every queued prompt
 with a terminal event, returning the dropped `DispatchCall`s so front-ends can
 close their per-prompt accounting.
- `RunTurn` is the self-draining convenience entry (it runs the dispatcher loop
 itself); the TUI and headless front-ends drive the queue explicitly with
 `NextQueuedTurn()` + `RunTurnAccepted`.

The TUI (`internal/tui/run.go`) runs an accept goroutine that admits every
`inputCh` prompt into the dispatcher as it arrives, and one serial executor
goroutine that drains the FIFO — one full turn machinery per prompt, re-draining
stale cancels and orphaned steering between turns. The queued-pills chip above
the prompt shows pending steering; a cancel-on-entry settlement renders a
"queued turn cancelled before it started" system line. Headless
(`cmd/gocode/headless.go`) queues `user` records the same way and adds a `cancel`
input record; a cancelled parent context still exits non-zero.

Steering semantics are unchanged: `steerCh` remains the foldable mid-turn lane
applied at round boundaries, while queued prompts run as their own turns.

## Event vocabulary and front-ends

`LoopEvent` is the engine's event vocabulary. The loop reports; each front-end
renders. The TUI and headless front-ends receive events through the
pubsub bus (`AgentLoop.Bus`); the plain REPL observes directly. The bus never
changes what `Observe` sees, and the exactly-once turn-event contract is
preserved.

| Kind | Meaning | Payload | Bus class |
| :-- | :-- | :-- | :-- |
| `EventTurnStart` | A turn began; precedes every other event of that turn. | — | lossy |
| `EventDelta` | Fragment of assistant text. | `Text` | lossy, unkeyed |
| `EventToolStart` | A tool call was announced, before approval/execution. | `Tool`, `ToolCallID`, `Args` (raw JSON) | lossy |
| `EventToolDelta` | Streamed tool-argument fragment. Display only; approval and execution wait for the complete call. | `Tool`, `ToolCallID`, `Args` (accumulated, sanitized preview, max 4 KiB) | lossy, unkeyed |
| `EventToolResult` | Outcome of one call. | `Tool`, `ToolCallID`, `Text`, `Diff`, `IsError` | must-deliver |
| `EventNotice` | Informational output that is not an error. | `Text` | lossy |
| `EventError` | Failure the user should see. | `Text` | must-deliver |
| `EventExit` | A slash command asked the UI to shut down. | `Text` (optional farewell) | must-deliver |
| `EventTurnEnd` | Turn finished, successfully or not; exactly once per turn. | `Err`, `Metrics` | must-deliver |

Deltas are stream **frames**, not whole-message snapshots, so they publish
lossy-UNKEYED: keyed coalescing would deterministically drop text and break the
headless frame contract (a recorded deviation). The coalescing lane is
implemented and contract-tested for snapshot-style publishers.

`TurnMetrics` carries prompt/completion tokens, tool-call count, elapsed time,
retry count, whether the token counts came from the provider
(`ProviderReported`), and the effective context size plus its budget. It contains
counts and timings only — no message content, arguments, command text or paths.

| Front-end | Started by | Consumes events as | Notes |
| :-- | :-- | :-- | :-- |
| TUI | `tui.Run` | Bus fan-in: one pump goroutine forwards bus events into the coalescing `tea.Msg` channel, gated so a turn's last event lands before its done message | Adds render cache, reveal pacing, overlays; approval via bridge. |
| Plain REPL | `AgentLoop.Run` | Direct `Observe` printing: deltas stream, tool results and notices print, one summary line per turn | Approval prompts read the same stdin reader as input. |
| Headless | `runHeadless` | Bus subscription → JSONL records on stdout | No human present: approval prompts always deny unless the policy allows. |

**Plain REPL.** One `bufio.Reader` serves both the prompt and the approval gate so
piped answers stay in order. The `e` (edit arguments) choice is available here: it
reads one JSON object from the same reader. After each turn it prints a
single-line summary (elapsed, tokens, tool calls, context percent) when the turn
measured anything.

**Headless.** Requests arrive on stdin as JSON objects:

```json
{"type":"user","text":"..."}
{"type":"steer","text":"..."}
{"type":"cancel"}
{"type":"exit"}
```

A `user` record is accepted into the dispatcher immediately; records that arrive
during a running turn are queued and run in order. `cancel` raises the
cancel mark and settles queued prompts. Output records are `delta`, `notice`,
`tool_start` (with raw `args`), `tool_delta` (`args_so_far`, never executable
partial JSON), `tool_result` (always `text` and `is_error`), `error`, and
`turn_done` (with the metrics schema). There is no record for turn start or exit:
a turn opens implicitly with its first record. `--policy <file>` takes
`{"auto_approve":[…],"deny":[…]}` (plus the additive fields
`auto_approve_session` and `dangerous_tools`); anything else that requires
approval is denied, and no code path can prompt. Invalid input is an error record
and a non-zero exit.

## Harness subsystems

### Pubsub event bus

`internal/pubsub/broker.go` implements a generic `Broker[T]` with three delivery
classes:

- **Lossy `Publish`** — per-subscriber queue of 4096; a full queue drops the
 event and counts the drop (never silent).
- **`PublishMustDeliver`** — blocking send bounded at 50 ms **per subscriber**,
 for terminal events (tool results, errors, exit, turn end, permission
 requests, run completions). A stalled subscriber's loss is counted.
- **Coalescing by entity key** — at most one pending event per key per
 subscriber, for snapshot-style state updates.

`events.go` defines the stable wire vocabulary for process boundaries
(`{type, payload}`, additive only): `message`, `permission_request`,
`permission_notification`, `permission_mode_changed`, `run_complete`, `session`,
`agent_event`. `internal/agent/loop_bus.go` maps `LoopEvent` kinds onto delivery
classes; `tui/run.go` and `cmd/gocode/headless.go` subscribe and fan in. Shutdown
closes the broker without goroutine leaks.

### Permission service

`internal/tools/permission.go` upgrades the approval gate into a request/grant
service:

- **Modes.** `normal` prompts for approval-gated calls; `yolo` skips prompts for
 non-dangerous calls and **refuses dangerous calls without asking**. Set via
 `[permissions] mode` or `--yolo`; validated at config load. A nothing-blocked
 `sysadmin` mode is deliberately omitted — it would contradict the
 unconditional deny-list invariant.
- **Danger classification.** `danger.go`'s `ClassifyDanger` is a pure function
 of (tool name, raw args, workspace root): a documented, bounded, deliberately
 conservative heuristic (false positives acceptable; false negatives are the
 bug direction). `[permissions] dangerous_tools` extends it. Normal mode shows
 the danger warning in the prompt and biases the default focus to Deny; yolo
 refuses.
- **Keyed session grants.** `PermissionKey{SessionID, ToolName, Action, Path,
 GrantKey}` — path normalized to a directory; grant key is the command string
 for shell tools (approving `ls` does not approve `sudo`) and the canonical
 exact arguments for everything else, preserving the historical exact-args
 grant class. In-memory only; cleared on process exit.
- **First-answer-wins.** Exactly one answer resolves each request, using the
 server-stored parameters — a responder echoes only the request id and a
 choice, so it cannot widen a grant. Prompts serialize per scope.
- **Bus payloads.** `permission_request` / `permission_notification` /
 `permission_mode_changed` publish on the bus; an async `PromptObserver`
 tap runs off the requesting goroutine (the informational permission hooks
 attach here).
- **Per-call hook approvals.** `hook_approval.go`: a `PreToolUse` allow
 stamps the context for one `toolCallID`; the gate consumes it after the config
 allowlist and before keyed grants, exactly once, cleared at every turn
 boundary. The deny-list, yolo refusal and redaction outrank it.

The existing `y`/`a`/`r`/`e`/`n` flows are unchanged; approval-before-mutation
remains the execution contract.

### Hook engine

`internal/hooks` runs shell hooks on 13 lifecycle events with Claude
Code-compatible payloads, exit-code semantics (0 / 2 / 49 / other), parallel
execution with per-hook timeouts (30 s default, 1 s abandon grace), and
config-order aggregation (deny > allow > none, halt OR, input shallow-merge,
output last-wins). `internal/agent/loop_hooks.go` wires tool/turn/permission/
compaction events into the loop's existing seams; `cmd/gocode/session_hooks.go`
fires `SessionStart`/`SessionEnd`. The full contract — events, wire format,
aggregation, security invariant and TOML examples — is [hooks.md](hooks.md).

## Context management

Everything is measured in estimated tokens; nothing depends on a model
tokenizer.

**Budget.** `ResolveBudget` runs before every request, so a `/model` switch takes
effect on the next round. An explicit `[tools] max_context_tokens` always wins.
Otherwise the budget is 80% of the model's context window, floored at 4096 and
capped at the window, leaving headroom for the response. Windows come from the
provider registry's longest-prefix, case-insensitive table:

| Prefix | Window | Prefix | Window |
| :-- | --: | :-- | --: |
| `gpt-4o`, `gpt-4.1` | 128 000 | `deepseek`, `qwen`, `kimi` | 128 000 |
| `o1`, `o3` | 200 000 | `llama` | 131 072 |
| `claude` | 200 000 | unknown model | 65 536 |
| `gemini` | 1 000 000 | | |

**Estimation.** ASCII letters and digits count 1/4 token, whitespace 1/6, other
ASCII and non-ASCII symbols 1/3, and Han/Hangul/kana 1/1.5. Each message adds 4
tokens, and reasoning content plus tool names and arguments are counted because
both occupy request budget.

**Provider report vs estimate.** When a provider reports usage, the loop stores
prompt+completion as `ProviderReportedContext`. Accounting always uses the larger
of that and the local estimate: a provider that compresses a window must not be
able to hide growth. A provider that reports nothing simply leaves the estimate
in charge.

**Transcript repair.** `EnsureToolPairing` (`context.go`) repairs history so
it satisfies the provider wire invariant: at most one system message at index 0;
every tool message directly follows an assistant `tool_calls` message that
declares its id; every declared call has exactly one result in call order;
undeclared results are dropped and missing results get
`Error: result missing (history truncated)`. Duplicate results
per call are deduped **preferring a real result over an error/interrupted
placeholder**, and an in-flight tool block at the history tail is protected —
`keepDeclaredResults` keeps the live block's real results and lets its remaining
ones land, rather than synthesizing placeholders under a running call.
`trimHistory` applies the session trim policy (last N logical turns, system
prompt kept, turns never split) and repairs the settled prefix so trimming
cannot strand a result. Assistant text is never dropped.

**Truncation (last resort).** `TruncateReport` keeps the system prompt and drops
whole turns from the oldest end until the history fits, always retaining the
newest turn. If the provider's count exceeded the estimate, the trimming target is
scaled by the observed ratio (with a floor of a quarter budget) so the drop is
proportional. Pairing is repaired even when the budget already fits, because it
is a wire requirement. Dropped turns are reported once per turn.

**Automatic compaction.** With `[context] auto_compact = true` (default), a pass
runs before each request, after a turn with no tool calls, and forcibly on a
provider overflow. It is due when effective tokens exceed:

```text
threshold = budget − reserve
reserve = max(15% of window, 16384), capped at half the budget
```

A **remaining-context backstop** (`autocompact.go`) compacts when the
space left in the real model window falls to 20 000 tokens (windows >200 000) or
20% (smaller windows). The budget threshold usually fires first; the backstop
guards the edge where the provider's own count approaches the window.

One pass does two things:

1. **Prune old tool output.** The newest 40 000 tokens of tool output are
 protected, results under 50 tokens are never touched, and nothing is rewritten
 unless the saving reaches 8 192 tokens. Replaced content becomes
 `[output truncated — ~N tokens]`. Only tool message content changes, so pairing
 remains valid; plan and scratchpad live in the system prompt and cannot be
 pruned.
2. **Summarize the older turns.** The cut point is the earliest whole-turn
 boundary whose retained tail still fits `[context] keep_recent_tokens`
 (default 20 000), always keeping at least the newest turn, and it must start on
 a user message. A turn is never split, so a tool result cannot be separated
 from its call. The omitted span is sent to the active provider with a bounded
 (20 000-rune) transcript and a 60-second timeout; the live plan/todo list is
 restated in the summary prompt. The returned summary
 replaces the span as a user/assistant pair headed
 `[Summary of earlier conversation]`. If the summary request fails or returns
 nothing, a count-only placeholder is used instead — unless a `PreCompact`
 hook denied, which blocks compaction entirely. The rewritten history is
 loaded back into the session and the system prompt is rebuilt from current
 workspace/plan/scratchpad state.

Manual `/compact` uses the same summary path but always keeps the last two turns
and is unaffected by `keep_recent_tokens`.

**Anchored persistence.** A compaction is persisted as a flagged summary
row plus the retained tail, appended **after** the existing transcript
(`Session.RecordSummaryAnchor`); the row id becomes the session's
`summary_message_id` anchor. Replay from the anchor reproduces the live
compacted window — the summary is relabelled to a user turn on replay, and the
tail is re-appended so the resumed window matches the live one. Pre-anchor rows
are retained (the tail is duplicated in the DB by design); an empty anchor
replays the full transcript.

**Overflow recovery.** If a provider rejects a request as too large
(`context_length_exceeded`, `prompt is too long`, `exceeds the context length`,
etc.), the loop forces one compaction pass and retries the same round. If that
second attempt still overflows, it truncates once and retries; if no older turns
remain to drop, it reports that and fails rather than looping. A notice explains
each step. Recovery is bounded at one forced compaction per round.

**Stop conditions.** Beyond the iteration and consecutive-repeat caps,
`LoopGuard` keeps the canonical keys of the last 10 batches; one tool+args
signature appearing in more than 5 of them ends the turn even when interleaved
with other calls (which reset the consecutive streak).

**Session safety net.** Independently of the budget, `Session.AddMessage` trims
history once it exceeds 500 messages to the last 40 logical turns (system prompt
kept, turns never split, pairing repaired). This bounds memory for a very long
session before the request path runs.

**System prompt.** The prompt has a stable prefix and one dynamic tail:

1. Base instructions (role, workspace boundary, sensitive files, tool usage,
 planning, scratchpad, verification, untrusted-data rule).
2. `## Project Context (untrusted repository data)` — the first `AGENTS.md`,
 `.gocode/AGENTS.md` or `docs/AGENTS.md` found walking up from the workspace.
3. `## Global Context (untrusted user data)` — `CONTEXT.md` in the config
 directory.
4. `## Session State (dynamic)` — environment (OS/arch, workspace, git branch read
 from `.git/HEAD`, provider, model), the plan checklist, and the scratchpad.

Untrusted blocks are delimited, labelled as data that cannot grant permissions,
and have `<<<` neutralized so a hostile file cannot forge a block boundary. The
dynamic block is rebuilt per turn (and after plan/scratchpad writes), keeping the
prefix stable.

## TUI architecture

The TUI is a Bubble Tea v2 program that owns no agent logic. Its visual language
was rebuilt while preserving GoCode's render cache,
coalescing, motion gate, approval-priority overlays and steering.

### Themes

Every colour derives from the active theme's semantic tokens — 40 roles plus a
16-slot ANSI remap. Six built-ins ship (`gocode-dark` default,
`gruvbox-dark`, `keanu`, `rose-pine`, `rose-pine-moon`, `rose-pine-dawn` light);
user themes are JSON override documents in the config `themes/` dir.
`styles.go`'s `rebuildStyles()` is the one place styles are constructed; a
switch bumps `themeGeneration`, invalidating every cached markdown render.
Light mode is a theme — the UI never queries the terminal's background. Full
reference: [themes.md](themes.md). Files: `theme.go`, `palette.go`,
`themes_builtin.go`, `derive.go`, `user_theme.go`, `styles.go`, `capability.go`.

### Layout

`layout.go` computes the geometry: a one-cell outer margin; a **32-column right
sidebar** (8-row gradient logo plus session/model/workspace/MCP/context
sections, scrolling when content overflows); 2 blank columns and 1 blank row as
separators; chat with a universal **2-cell gutter** (user primary rule, focused
thick rule, no card borders), one blank row between items, markdown capped at
120 columns; a borderless textarea with a **4-cell prompt column contract**
(badge+margin vs ::: dots — text never shifts); a pills strip (todos / queued
steering); and a 1-row help/status bar. Below **120×30** the layout switches to
compact mode: a 1-row header (wordmark + diagonal field + dot-separated
path/context/hints) and full-width chat. Width changes are render-cache epochs.
Files: `layout.go`, `model.go`, `banner.go`, `mascot.go`.

### Chat rendering

`markdown.go` renders assistant messages with glamour: one renderer per
(width, theme generation), one shared chroma role table (the theme's `Syntax`
overrides) so fenced blocks, numbered file views and diffs can never disagree,
and a stable-prefix content cache so settled messages render once and only the
live tail re-renders as it grows. Tool calls render as live cards
(`model.go`'s `toolCard`): status glyph + info-coloured name + faint params
header, indented body on `bg_less_visible`, spinner/elapsed badges, and clear
pending/running/done/error states. `diffview.go` renders unified diffs as
two-band tinted rows — a darker one-cell sign gutter and a lighter code band,
diff ink on top, syntax highlighting over the bands — using the theme's derived
CIELAB tints. Reasoning and error displays have dedicated styles; hook
diagnostics render as sanitized notice lines.

### Dialogs, badges and toasts

`dialog.go` draws the rounded primary frame with a gradient ╱ title rule over
an undimmed backdrop (`placeModal` composites over the live chat), borderless
buttons with mnemonic underlines, and badges whose ink obeys the contrast
rule. The permission dialog (`approval.go`, `model.go`) offers Approve/Deny
buttons with a **danger-biased default Deny**, `esc` = Deny, `t`/`f` diff
split/fullscreen toggles, and keeps GoCode's `y`/`a`/`r` grant keys. **Input
grace** (`grace.go`) absorbs keystrokes until 425 ms of quiet passes, capped at
1500 ms from open; each absorbed key restarts the quiet window but never
extends past the cap; reopening the same request within 500 ms skips grace.
Grace only delays key handling — it can never approve anything. Toasts
(`toast.go`) replace the status/help row for a default 5 s TTL (OKAY!/HEY!/
WARNING/ERROR badges), sequence-stamped so a stale expiry never clears a newer
message, expiring on a one-shot tick. The session switcher
(`session_dialog.go`) has normal/deleting/renaming states; `quit.go` confirms
exit with a banner. The overlay stack (`overlay.go`) keeps **approval the
highest priority**: pushing it drops any open picker/palette/theme surface.

### Focus, keymap, completions, history

`focus.go` defines the focus enum — none (pre-ready) / editor / main / sidebar.
`keymap.go` is a **data table**: one row per binding with explicit context
(global, idle, streaming, textarea, transcript, approval, picker, palette,
chat, sidebar), driving dispatch, the help line and the audit tests together.
Notable bindings: `Tab` cycles focus (chat/sidebar → editor) and completes at
the idle prompt; `h`/`l` move between chat and sidebar; in chat, `j`/`k` scroll
lines, `J`/`K` jump items, `u`/`d` half-page, `b`/`f` page, `g`/`G` top/bottom,
`space` expands the focused item **only** (paging is `f`/`pgdown` — fixing
a double-binding), `c`/`y`/`C`/`Y` copy centrally, `esc`
clears the highlight, `H`/`L` scroll the focused item horizontally.
`Ctrl+End` re-arms follow-bottom from any focus.

The command palette opens on **`Ctrl+P`** (`Ctrl+K` stays as a silent alias),
projected from the slash-command registry: argument-free commands run
immediately, commands with arguments stage at the prompt, `/sessions` opens the
session picker. `complete.go` cycles `Tab` completions for command names and
`/model`/`/provider` arguments; `atfile.go` adds **@file mentions** — files
listed off the update loop (max 500 files, depth 6), ranked into four stable
tiers, popup growing upward, paths quoted when needed. `prompt_history.go`
persists recall **per workspace** (`<config dir>/history/<sha8>.json`, 200
entries, `0600`); `Alt+P`/`Alt+N` walk it, `Up` recalls only from an empty,
focused prompt.

### Shared clock and scramble spinner

`animclock.go` is the **one clock**: a generation-guarded 20 fps tick (50 ms)
drives every animated surface — transcript flush coalescing, spinner scramble,
elapsed badges, the opt-in reveal pacer and the mascot's springs (`anim.go`,
harmonica; idle 5 fps / active 20 fps stepping). One
outstanding tick at a time; stale generations and superseded sequences are
dropped whole; a tick silent for 2 s while armed is recovered; idle freezes the
chain (no perpetual idle tick). This replaces the earlier 80 ms spinner chain
and 30 fps flush tick.

`scramble.go` is the spinner primitive: cycling glyphs from a fixed charset
enter column-by-column on deterministic birth steps (`(7·col) mod 20`, so the
entrance completes within one second), then loop through ten pre-rendered
frames (500 ms). The loop phase is global — `floor(now/50ms)` — so every
spinner agrees on the frame with zero per-widget state; birth and the ellipsis
cycle (one step every 8 frames: 400 ms per step, 1.6 s per cycle) are
per-spinner. The status bar uses a compact 3-cell scramble row (keeping the
narrow-width squeeze contract); transcript cards use 15 cells. `spinner.go`
holds the shared clock math; the working-state label carries the shimmer band
(`reveal.go`/`spinner.go`), decorative only — motion off renders the same cells
in the plain streaming colour with identical width.

**Reveal law.** With smooth streaming active, each tick reveals
`max(3, ceil(backlog/8))` clusters of the buffered text (one base rune plus its
combining marks). Pacing is opt-in (`smooth_streaming = true`); by default text
renders on arrival. When the stream ends, pacing is off, or motion is reduced,
the whole target is shown at once. Tool-argument previews are paced by the same
law.

**Motion gate.** `[tui] motion` is `auto`, `reduced` or `off`; precedence
`off` > `reduced` > `auto`; under `auto`, `GOCODE_REDUCED_MOTION` or
`REDUCED_MOTION` set truthy resolves to reduced. Decorative motion (mascot
springs, shimmer, scramble cycling) runs only at full motion; functional
indicators (spinner glyph, elapsed labels) degrade to static, never disappear.

### Render cache and coalescing

Each `ChatMessage` is a comparable value, and the cache (`rendercache.go`)
stores `{message, width, rendered}` per index. A settled message compares equal
on later frames and its rendered string is reused verbatim; only the live
assistant tail or a mutating tool card misses. A width change is a new epoch and
resets the cache; a theme switch invalidates rendered strings via
`themeGeneration` without dropping cached heights. Streamed chunks only mark
the transcript dirty; the shared 50 ms tick applies one viewport update, so a
token burst costs one render per frame rather than one per token. Discrete
events (tool start, tool result, notice, turn end) flush immediately.
Autoscroll follows only when the viewport was already at the bottom, so a user
reading scrollback stays put.

### Engine boundary

`tui.Run` builds the engine, installs the approval bridge and the event bus, then
starts the accept/executor goroutines (see
[Queued-turn dispatch](#queued-turn-dispatch)) and the channels: `inputCh`
(1), `steerCh` (8), `outputCh` (256), `cancelCh` (1). One pump goroutine fans
bus events into `outputCh`, gated so a turn's last event lands before its done
message. Stale cancels are drained between turns so an interrupt can never stop
the next turn. `Esc`/`Ctrl+C` cancel through the dispatcher (raising the cancel
mark over queued prompts); a second press quits. While a turn runs, `Enter`
queues steering; the queued-pills chip shows the count until the engine's
steering notice clears it. The mouse wheel scrolls the transcript three lines
per notch; the viewport's own wheel handling is disabled so a notch never
scrolls twice.

**Status bar.** Mascot, provider, model, workspace basename, the shared
scramble glyph plus state word (shimmering at full motion), and — while a turn
runs and only when it fits — turn metrics (elapsed, tokens, context percent).
When space is tight the workspace path is dropped first, then the model id.

## Session persistence

Sessions are stored in SQLite (pure-Go `modernc.org/sqlite`), at
`<data dir>/sessions/sessions.db` (paths per platform in
[configuration.md](configuration.md#platform-paths)).

**Connection.** One pooled connection, so pragmas apply everywhere:
`busy_timeout=5000`, `foreign_keys=ON`, `journal_mode=WAL`. The database, its
`-wal` and `-shm` sidecars are `0600`; the directory is `0700`. Permissions are
re-tightened on every open, because WAL mirrors the database's mode onto sidecars.

**Schema.**

| Table | Columns |
| :-- | :-- |
| `sessions` | `id` TEXT PK, `title`, `provider`, `model`, `workspace`, `scratchpad`, `summary_message_id`, `created_at`, `updated_at` |
| `messages` | `id` INTEGER PK AUTOINCREMENT, `session_id` FK → `sessions(id)` ON DELETE CASCADE, `role`, `content`, `tool_calls` (JSON), `tool_call_id`, `reasoning_content`, `created_at` |
| Indexes | `idx_messages_session(session_id)`, `idx_sessions_updated(updated_at DESC)` |

Missing columns are added idempotently on open, so older databases keep working.

**Writes (debounce).** `Session.AddMessage` queues the row and arms a
**33 ms debounce timer** (`persistDebounce`); a burst of appends coalesces into
one write batch. Terminal transitions — turn end, cancel, abort, compaction
anchor, session reset — call `FlushMessages` synchronously, so no message is
lost on an interrupt or exit. The first user message also becomes the title
(capped at 40 runes). Tool-call arguments are redacted in the copy that is
persisted; the in-memory call keeps its raw arguments so the tool still receives
them during the turn. `/clear` and `/new` reset the stored transcript (an upsert
that replaces the message rows and clears the anchor). A session record that
carries an empty workspace keeps its existing binding.

**Resume.** Without `--new`, startup loads the most recently updated session whose
`workspace` equals the normalized workspace (absolute, cleaned, case-folded on
Windows). `--session <id>` wins over workspace scoping, and `/sessions`,
`/resume <id>`, and the session picker load by id. Replay starts at the
`summary_message_id` anchor when set: the summary row is relabelled to a
user turn and the retained tail follows, reproducing the live compacted window.
A resumed transcript keeps its messages and scratchpad but its system prompt is
rebuilt: the stored prompt describes the workspace it was recorded in.

**Export.** `gocode session export [id]` renders Markdown or JSON to stdout or a
file (`--format`, `--output`, `--force`, `--no-tools`). System prompts and
reasoning are always excluded; destinations are `0600` on POSIX and never
overwritten without `--force`.

## Security boundaries

The full threat model is [SECURITY.md](SECURITY.md); this is the short version.

| Boundary | Behavior |
| :-- | :-- |
| File tools | Confined to the workspace; traversal and normalization tricks rejected; symlinks re-checked; ignore rules and sensitive patterns block; hardlink aliases refused by inode identity. |
| Shell | **Not confined.** `shell_exec` uses the workspace as cwd, not a sandbox. Approval is the real protection. |
| Permission modes | `normal` prompts; `yolo` skips prompts for safe calls but **refuses dangerous calls without asking**. The deny-list and redaction are unconditional in every mode; `sysadmin` is deliberately not offered. |
| Approval | `deny` wins over everything — modes, grants and hook allows; `auto_approve` and read-only tools skip the prompt; keyed session grants match the command string (shell) or exact canonical arguments (other tools) only and die with the process; first-answer-wins uses server-stored parameters. |
| Hooks | Trusted user configuration, no sandbox — but a hook allow is per-call, consumed once, cleared at turn boundaries, and can never bypass the deny-list, yolo refusal or redaction. Permission hooks are informational. Hook children get the scrubbed environment. See [hooks.md](hooks.md#security-invariant). |
| Child environment | Credential-shaped variables and every configured `api_key_env` are removed from shell, MCP and hook child processes; a small allowlist (`PATH`, `HOME`, `SystemRoot`, …) always survives. |
| Redaction | Tool results, hook-added text, persisted tool-call arguments, compaction transcripts and session exports are redacted unconditionally. `[tools.shell] redact_secrets=false` only disables masking of shell output before it becomes a result. |
| Terminal safety | All model/tool text is sanitized (`safetext`) before display, so escape sequences cannot repaint the UI or spoof a prompt. |
| Secrets at rest | `config.toml`, `gocode.log`, `sessions.db`, prompt-history files and `copilot_token` are owner-only; directories are `0700`; base URLs must be HTTPS unless loopback. |

MCP servers are untrusted child processes: their tools always require approval,
their metadata is capped, and their environment is scrubbed — but they run with
the user's privileges.