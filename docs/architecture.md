# Architecture

GoCode is a single Go binary that runs one agent engine behind three front-ends: a
Bubble Tea TUI, a plain terminal REPL, and a JSONL headless protocol. This page
describes how the pieces fit together as the binary behaves today. For the
user-facing knobs referenced here, see [configuration.md](configuration.md); for
providers see [providers.md](providers.md); for the threat model see
[SECURITY.md](SECURITY.md).

- [Package map](#package-map)
- [One turn, end to end](#one-turn-end-to-end)
- [Event vocabulary and front-ends](#event-vocabulary-and-front-ends)
- [Context management](#context-management)
- [TUI architecture](#tui-architecture)
- [Session persistence](#session-persistence)
- [Security boundaries](#security-boundaries)

## Package map

| Package | Responsibility |
| :-- | :-- |
| `cmd/gocode` | Cobra CLI: flags, subcommands (`doctor`, `logs`, `mcp`, `provider`, `auth`, `session`), wiring of config → providers → tools → loop → front-end. |
| `internal/agent` | The engine: turn lifecycle, provider rounds, steering, tool batches, loop guard, context budget/truncation/compaction, prompt assembly, slash-command registry, turn metrics. |
| `internal/config` | Platform directories, TOML loading with strict key checking, defaults, base-URL validation, secret-env enumeration. |
| `internal/eval` | Deterministic, network-free evaluation harness that scripts a provider to exercise tool dispatch, approvals, truncation, compaction and workspace-scoped resume. |
| `internal/ignore` | `.gocodeignore` / `.gitignore` matching (gitignore semantics) and the sensitive-file pattern set. |
| `internal/mcp` | MCP client and manager: stdio JSON-RPC 2.0, tool discovery/caps, on-demand restart, scrubbed child environment. |
| `internal/provider` | Provider implementations and registry: OpenAI-compatible gateway proxy, native Anthropic, GitHub Copilot, local Ollama, retry transport, context-window table. |
| `internal/safetext` | Strips ANSI escapes and control characters from untrusted model/tool text before it reaches a terminal. |
| `internal/session` | SQLite session store (WAL, owner-only files), workspace-scoped resume queries, transcript export and tool-argument redaction. |
| `internal/tools` | Tool registry and the seven built-ins, path confinement and sensitive-file/hardlink checks, shell execution, diff previews, approval gate, secret redaction. |
| `internal/tui` | Bubble Tea v2 interface: render cache, coalesced flushing, reveal pacer, shared spinner clock, overlays/pickers/palette, keymap table, theme and motion gate. |
| `internal/version` | Build version placeholder, injected at link time by releases. |

**`cmd/gocode`** owns process startup and nothing else. It creates the data
directories, opens the log, loads config (or `--config <path>`), builds the
provider registry (Ollama plus 17 OpenAI-compatible gateways plus native
Anthropic, Copilot, and any custom endpoints), opens the session store unless
`[session] persist = false`, resolves the workspace, resumes a session, registers
the seven built-in tools and every MCP tool, constructs one `AgentLoop`, and hands
it to the selected front-end. `--headless` wins over `--tui` so a machine-driven
process never opens a TUI. It also implements the maintenance subcommands, which
operate on config or the session database directly rather than through the loop.

**`internal/agent`** is the only place turn behavior lives. `RunTurn` accepts one
line of input, decides whether it is a slash command or a model turn, and runs
provider round-trips until the model stops requesting tools. The same type serves
the TUI (`RunTurn`) and the plain REPL (`Run`), so termination conditions,
approval sequencing and context accounting cannot drift between front-ends. It
also owns prompt construction: base instructions, delimited untrusted context,
and one trailing dynamic state block.

**`internal/config`** resolves platform paths, decodes TOML over a defaulted
struct, and rejects unknown keys so a typo cannot silently disable a setting. It
also validates every configured base URL at load time (absolute http(s), no
credentials/query/fragment, HTTPS unless loopback) and exposes the set of
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
Unicode, newlines and tabs. Model output, tool output, tool names and previews are
sanitized before display; sanitization is a display boundary only, and the
transcript stores the redacted result rather than the sanitized rendering.

**`internal/session`** is the persistence layer: one SQLite connection with WAL,
foreign keys and a 5-second busy timeout, a `sessions` table with a workspace
column, and a `messages` table holding role, content, tool calls, tool-call id and
reasoning. Auto-resume queries by normalized workspace. Export renders Markdown
or JSON and always omits system prompts and reasoning; tool-call arguments are
redacted before they are written to disk.

**`internal/tools`** holds the tool contract (`Spec`, `Execute`,
`RequiresApproval`), the seven built-ins (`file_read`, `code_search`, `plan`,
`scratchpad`, `file_write`, `file_patch`, `shell_exec`), and the approval gate.
File tools resolve every path through the workspace root, reject traversal and
normalization tricks (ADS, trailing space/dot), re-check after symlink
resolution, block sensitive/ignored paths, and refuse hardlink aliases by inode
identity (cached for 30 seconds, invalidated on write). The gate implements the
permission lists, exact-argument session grants, the per-turn read-only flag, and
the unconditional transcript redaction of results.

**`internal/tui`** is a pure view over engine events. It never calls a provider or
a tool: it sends input on a channel, receives `LoopEvent`s on another, and owns
rendering, input editing, overlays, and animation. See
[TUI architecture](#tui-architecture).

**`internal/version`** carries the semantic version; release builds inject it with
`-ldflags -X`.

## One turn, end to end

```text
gocode [--provider P] [--model M] [--tui=false | --headless]
   │
   ├── TUI (Bubble Tea) ─────┐
   ├── plain REPL ───────────┤  all three call AgentLoop.RunTurn(ctx, input)
   └── headless JSONL ───────┘
             │
        RunTurn
        ├─ Approval.BeginTurn()          per-turn read-only grant is cleared
        ├─ beginTurn()                   steering window opens; old queue dropped
        ├─ emit EventTurnStart
        ├─ HandleCommand(input) ────────► handled: Notice/Error/Exit, one EventTurnEnd
        ├─ Session.AddMessage(user)
        └─ streamResponse
              │
              └─ every provider round:
                 ├─ ctx cancelled? ─────────────────────────────► return
                 ├─ drainSteering()          queued text becomes user messages
                 ├─ ResolveBudget(model)     80% of window, or configured override
                 ├─ autoCompactNow(false)    threshold → prune → summarize
                 ├─ TruncateReport(history)  last resort; whole turns only
                 ├─ openStream() ── overflow ─► compact once, retry; truncate on 2nd
                 │     └─ deltas · reasoning · tool-call deltas · usage
                 ├─ append assistant message (content + tool calls + reasoning)
                 ├─ no tool calls ─► loop summary, post-turn compaction, return
                 ├─ guard.CheckCall() ── limit ─► synthetic results, stop turn
                 └─ handleToolCalls
                      ├─ approval pass (sequential, call order):
                      │     EventToolStart → Approval.Prepare for each call
                      ├─ execution pass:
                      │     read-only calls in parallel (max 4); mutating calls serial
                      ├─ result pass (call order):
                      │     redact → Session.AddMessage(tool) → EventToolResult
                      └─ mutated workspace && verify_command → runAutoVerify
                 └─ next round
```

`RunTurn` has one exit funnel: normal completion, cancellation, a loop-guard stop,
a provider failure and a slash command all pass through the same deferred
function, which logs the turn record and emits exactly one `EventTurnEnd` with the
turn's metrics. `/exit` is the only case where `EventExit` precedes it.

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
that races the last boundary is discarded with the turn.

**One provider round.** The loop resolves the active provider and model, converts
the tool registry to provider tool specs, resolves the token budget for the
current model, runs automatic context maintenance, truncates if still necessary,
and opens the stream. Chunks are consumed until the channel closes: text deltas
are emitted and accumulated, reasoning is accumulated without display, complete
tool calls are collected, streamed tool-argument fragments are sanitized and
emitted as `EventToolDelta` for display only, and the last usage report wins.
Cancellation is checked after every chunk. Usage is recorded only for a request
that ran to completion, because a cancelled stream reports partial counts.

The assistant message is appended to the session whether it contains text, tool
calls, or both. If there are no tool calls, the turn ends after a loop-guard
summary (when tools ran), a truncation warning if `finish_reason` was
`length`/`max_tokens`, and one post-turn compaction pass so the next user message
starts inside the threshold. An empty response with no tool calls and no
truncation emits a notice suggesting the credentials or `/provider` be checked.

**Tool batches.** Before anything executes, `LoopGuard.CheckCall` bounds the
batch: iteration count is capped (`[tools] max_tool_iterations`, default 50) and
consecutive identical batches are capped (`max_repeated_tool_calls`, default 3,
comparing canonicalized JSON). A guard stop writes a synthetic result for every
declared call — both OpenAI-shaped and Anthropic APIs reject a call without a
result — appends a stop message, emits an error, and ends the turn.

Tool calls then run in three passes:

1. **Approval pass**, sequential and in call order. Each call is announced with
   `EventToolStart` (before any blocking prompt), resolved against the registry,
   classified read-only or mutating, and passed to `Approval.Prepare`, which
   applies `deny`, `auto_approve`, the "no approval required" contract,
   exact-argument session grants, the per-turn read-only flag, then renders a
   preview and asks the user. `Prepare` returns a closure, so every decision for
   the batch is made before any tool runs. An unknown tool produces an error
   result without approval.
2. **Execution pass.** Read-only calls are dispatched to a worker set bounded at
   four; a mutating call waits for every read previously dispatched, then runs
   alone. Call-order effects and cancellation stay predictable without
   serializing independent reads. A cancelled turn interrupts every call it never
   reached.
3. **Result pass**, in call order regardless of completion order. Each result
   crosses the transcript redaction boundary exactly once, is appended as a tool
   message, and is emitted as `EventToolResult` with a diff kept separate from
   prose. A successful `plan` or `scratchpad` write rebuilds the system prompt so
   the next round sees the state the model just wrote. Displayed tool output is
   capped at 2000 bytes; the model and transcript receive the full result.

**Auto-verify.** When `[tools] verify_command` is set and any approved call in the
round can mutate the workspace, the command runs once after the batch, in the
workspace, under `verify_timeout_seconds` (default 120). It deliberately skips the
approval gate — configuring it is the opt-in. Output is capped at 8 KiB and
redacted. Success emits a notice; failure emits a notice and appends a user
message so the next provider round can react to it.

**Abort paths.** Cancellation returns the context error; the front-end renders a
"stopped" line. A provider error is returned as `stream error: …` and ends the
turn. Every prepared-but-unreached call gets
`Error: tool call interrupted before it could execute.` so history never holds a
dangling call id.

## Event vocabulary and front-ends

`LoopEvent` is the only channel between engine and UI. The loop reports; each
front-end renders.

| Kind | Meaning | Payload |
| :-- | :-- | :-- |
| `EventTurnStart` | A turn began; precedes every other event of that turn. | — |
| `EventDelta` | Fragment of assistant text. | `Text` |
| `EventToolStart` | A tool call was announced, before approval/execution. | `Tool`, `ToolCallID`, `Args` (raw JSON) |
| `EventToolDelta` | Streamed tool-argument fragment. Display only; approval and execution wait for the complete call. | `Tool`, `ToolCallID`, `Args` (accumulated, sanitized preview, max 4 KiB) |
| `EventToolResult` | Outcome of one call. | `Tool`, `ToolCallID`, `Text`, `Diff`, `IsError` |
| `EventNotice` | Informational output that is not an error. | `Text` |
| `EventError` | Failure the user should see. | `Text` |
| `EventExit` | A slash command asked the UI to shut down. | `Text` (optional farewell) |
| `EventTurnEnd` | Turn finished, successfully or not; exactly once per turn. | `Err`, `Metrics` |

`TurnMetrics` carries prompt/completion tokens, tool-call count, elapsed time,
retry count, whether the token counts came from the provider
(`ProviderReported`), and the effective context size plus its budget. It contains
counts and timings only — no message content, arguments, command text or paths.

| Front-end | Started by | Consumes events as | Notes |
| :-- | :-- | :-- | :-- |
| TUI | `tui.Run` | Bubble Tea messages: turn-start, chunk, tool-start, tool-delta, tool-result/notice/error, exit, turn-metrics | Adds render cache, reveal pacing, overlays; approval via bridge. |
| Plain REPL | `AgentLoop.Run` | Direct printing: deltas stream, tool results and notices print, one summary line per turn | Approval prompts read the same stdin reader as input. |
| Headless | `runHeadless` | JSONL records on stdout | No human present: approval prompts always deny. |

**Plain REPL.** One `bufio.Reader` serves both the prompt and the approval gate so
piped answers stay in order. The `e` (edit arguments) choice is available here: it
reads one JSON object from the same reader. After each turn it prints a
single-line summary (elapsed, tokens, tool calls, context percent) when the turn
measured anything.

**Headless.** Requests arrive on stdin as JSON objects:

```json
{"type":"user","text":"..."}
{"type":"steer","text":"..."}
{"type":"exit"}
```

A `user` record starts a turn on a goroutine so the reader stays free for `steer`
records mid-stream; records that arrive during a running turn are queued and run
in order. Output records are `delta`, `notice`, `tool_start` (with raw `args`),
`tool_delta` (`args_so_far`, never executable partial JSON), `tool_result`
(always `text` and `is_error`), `error`, and `turn_done` (with the metrics
schema). There is no record for turn start or exit: a turn opens implicitly with
its first record. `--policy <file>` takes `{"auto_approve":[…],"deny":[…]}`;
anything else that requires approval is denied, and no code path can prompt.
Invalid input is an error record and a non-zero exit.

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

**Truncation (last resort).** `TruncateReport` keeps the system prompt and drops
whole turns from the oldest end until the history fits, always retaining the
newest turn. If the provider's count exceeded the estimate, the trimming target is
scaled by the observed ratio (with a floor of a quarter budget) so the drop is
proportional. It then repairs the wire invariant via `EnsureToolPairing`: at most
one system message at index 0; every tool message directly follows an assistant
`tool_calls` message that declares its id; every declared call has exactly one
result in call order; undeclared results are dropped and missing results get
`Error: result missing (history truncated)`. Assistant text is never dropped.
Dropped turns are reported once per turn.

**Automatic compaction.** With `[context] auto_compact = true` (default), a pass
runs before each request, after a turn with no tool calls, and forcibly on a
provider overflow. It is due when effective tokens exceed:

```text
threshold = budget − reserve
reserve   = max(15% of window, 16384), capped at half the budget
```

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
   (20 000-rune) transcript and a 60-second timeout; the returned summary replaces
   it as a user/assistant pair headed `[Summary of earlier conversation]`. If the
   summary request fails or returns nothing, a count-only placeholder is used
   instead. The rewritten history is loaded back into the session and the system
   prompt is rebuilt from current workspace/plan/scratchpad state.

Manual `/compact` uses the same summary path but always keeps the last two turns
and is unaffected by `keep_recent_tokens`.

**Overflow recovery.** If a provider rejects a request as too large
(`context_length_exceeded`, `prompt is too long`, `exceeds the context length`,
etc.), the loop forces one compaction pass and retries the same round. If that
second attempt still overflows, it truncates once and retries; if no older turns
remain to drop, it reports that and fails rather than looping. A notice explains
each step. Recovery is bounded at one forced compaction per round.

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

The TUI is a Bubble Tea v2 program that owns no agent logic.

**Threading.** `tui.Run` builds the engine, installs an approval bridge, then
starts one engine goroutine and four channels: `inputCh` (1), `steerCh` (8),
`outputCh` (256), `cancelCh` (1). The engine goroutine turns each input into
`RunTurn` and pumps steering messages into `AgentLoop.Steer` while the turn runs.
Stale cancels are drained between turns so an interrupt can never stop the next
turn. `Esc`/`Ctrl+C` cancel the turn context; a second press quits.

**Approval.** `ApprovalGate.OnPresentChoice` is wired to a bridge channel. The
modal offers `y` (once), `a` (exact arguments for the session), `r` (read-only
tools this turn) and `n`; the TUI deliberately has no argument editor, so `e` is
plain-mode only. Approval is the highest-priority overlay: pushing it drops any
open picker, palette or session list so its keys can never go nowhere.

**Render cache and coalescing.** Each `ChatMessage` is a comparable value, and the
cache stores `{message, width, rendered}` per index. A settled message compares
equal on later frames and its rendered string is reused verbatim; only the live
assistant tail or a mutating tool card misses. A width change is a new epoch and
resets the cache. Streamed chunks only mark the transcript dirty; a 30 fps tick
applies one viewport update, so a token burst costs one render per frame rather
than one per token. Discrete events (tool start, tool result, notice, turn end)
flush immediately. Autoscroll follows only when the viewport was already at the
bottom, so a user reading scrollback stays put.

**Reveal law.** With smooth streaming active, each 30 fps tick reveals
`max(3, ceil(backlog/8))` clusters of the buffered text, where a cluster is one
base rune plus its combining marks. A slow stream still moves at least three
units per frame; a large backlog catches up in under a second without a budget
that depends on message length. When the stream ends, `smooth_streaming` is off,
or motion is reduced, the whole target is shown at once. Tool-argument previews
are paced by the same law.

**Shared spinner clock.** Every live spinner derives its frame from the wall
clock at render time: `floor((now + 1µs) / 80ms) % 10` over a ten-frame braille
set. The status bar and every pending tool card therefore agree on the frame with
no per-widget state. One tick chain re-arms on the next period boundary, is
deduped against the flush tick, and only runs while a turn is streaming. Under
reduced motion the glyph is pinned to frame zero and the chain drops to a
one-second refresh so elapsed labels keep moving — a functional indicator
degrades, it never disappears.

**Shimmer.** The working-state label carries a band sweeping at a constant 30
cells/second with a 6-cell half-width (a 12-cell band) and 10 cells of off-text
padding, mapped to three intensity tiers. A run of same-tier cells is one ANSI
span, which keeps the band cheap. Decorative only: motion off renders the same
cells in the plain streaming colour with identical width, so toggling motion never
reflows the bar.

**Motion gate.** `[tui] motion` is `auto`, `reduced` or `off`. Precedence is
`off` > `reduced` > `auto`; under `auto`, a terminal that asks for less motion
(`GOCODE_REDUCED_MOTION` or `REDUCED_MOTION` set truthy) resolves to reduced.
Decorative motion (mascot springs, shimmer) runs only at full motion. Pacing
additionally requires `[tui] smooth_streaming = true` and a live turn.

**Overlays.** One stack holds the approval modal and the open model picker,
command palette or session picker. Key routing reads the top of the stack;
`pgup`/`pgdn` fall through to the transcript. Pushing approval clears every other
surface, and no other surface can open over approval. `Ctrl+K` opens a fuzzy
palette projected from the slash-command registry — argument-free commands run
immediately, commands with arguments are staged at the prompt, and `/sessions`
opens the session picker. `Ctrl+L` opens the model picker, which re-collects
models on open (2 s per provider, 3 s overall) while keeping the startup snapshot
on screen; a failed refresh never empties the list. Selecting a row sends
`/provider <p>` then `/model <m>` so the engine still owns validation.

**Input.** `Tab` completes command names, and the first argument of `/model` and
`/provider` from live data, extending to the longest common prefix and cycling
candidates. `Alt+P`/`Alt+N` walk the in-memory prompt history (100 entries,
consecutive duplicates collapsed); `Up` recalls only from an empty, focused
prompt. While a turn runs, `Enter` queues steering rather than submitting a new
turn, and a chip above the input shows the queued count until the engine's
steering notice clears it. The mouse wheel scrolls the transcript three lines per
notch; the viewport's own wheel handling is disabled so a notch never scrolls
twice.

**Status bar.** Mascot, provider, model, workspace basename, the shared spinner
glyph plus state word (shimmering at full motion), and — while a turn runs and
only when it fits — turn metrics (elapsed, tokens, context percent). When space
is tight the workspace path is dropped first, then the model id.

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
| `sessions` | `id` TEXT PK, `title`, `provider`, `model`, `workspace`, `scratchpad`, `created_at`, `updated_at` |
| `messages` | `id` INTEGER PK AUTOINCREMENT, `session_id` FK → `sessions(id)` ON DELETE CASCADE, `role`, `content`, `tool_calls` (JSON), `tool_call_id`, `reasoning_content`, `created_at` |
| Indexes | `idx_messages_session(session_id)`, `idx_sessions_updated(updated_at DESC)` |

Missing columns are added idempotently on open, so older databases keep working.

**Writes.** Every `Session.AddMessage` appends one row and bumps the session
timestamp in a transaction; the first user message also becomes the title (capped
at 40 runes). Tool-call arguments are redacted in the copy that is persisted; the
in-memory call keeps its raw arguments so the tool still receives them during the
turn. `/clear` and `/new` reset the stored transcript (an upsert that replaces the
message rows); compaction rewrites only the in-memory history, and the messages
added afterwards are appended to the stored transcript. A session record that
carries an empty workspace keeps its existing binding.

**Resume.** Without `--new`, startup loads the most recently updated session whose
`workspace` equals the normalized workspace (absolute, cleaned, case-folded on
Windows). `--session <id>` wins over workspace scoping, and `/sessions`,
`/resume <id>`, and the session picker load by id. A resumed transcript keeps its
messages and scratchpad but its system prompt is rebuilt: the stored prompt
describes the workspace it was recorded in.

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
| Approval | `deny` wins over everything; `auto_approve` and read-only tools skip the prompt; session grants match exact canonical arguments only and die with the process. |
| Child environment | Credential-shaped variables and every configured `api_key_env` are removed from shell and MCP child processes; a small allowlist (`PATH`, `HOME`, `SystemRoot`, …) always survives. |
| Redaction | Tool results, persisted tool-call arguments, compaction transcripts and session exports are redacted unconditionally. `[tools.shell] redact_secrets=false` only disables masking of shell output before it becomes a result. |
| Terminal safety | All model/tool text is sanitized (`safetext`) before display, so escape sequences cannot repaint the UI or spoof a prompt. |
| Secrets at rest | `config.toml`, `gocode.log`, `sessions.db` and `copilot_token` are owner-only; directories are `0700`; base URLs must be HTTPS unless loopback. |

MCP servers are untrusted child processes: their tools always require approval,
their metadata is capped, and their environment is scrubbed — but they run with
the user's privileges.
