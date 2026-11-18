# oh-my-pi (omp) — Reverse-Engineering Report

**What we understood from the source, and what GoCode can learn from it.**

- **Subject:** [`can1357/oh-my-pi`](https://github.com/can1357/oh-my-pi) — "a coding agent with the IDE wired in", a fork of Mario Zechner's [pi-mono](https://github.com/badlogic/pi-mono), built by Stencil Labs. Bun/TypeScript monorepo (~74% TS) with an ~80k-line Rust native core (~22%).
- **Analyzed at:** commit `840cc4b6bc94581509215d8db30fb04ad0ab7fea` (main, ~v18.22.2, 2026-11-18).
- **Method:** read-only static source analysis (shallow+sparse clone; no omp code executed). Three parallel scoped audits (harness, TUI/animations, commands) plus orchestrator spot-verification of key constants. Every mechanism claim below is source-cited in the detailed evidence reports; docs-only claims are labeled.
- **Evidence reports (full detail, file:line citations):**
  - [`research/omp-harness.md`](research/omp-harness.md) — agent loop, streaming, context, tools, subagents, advisor, sessions, providers (Q1–Q11).
  - [`research/omp-tui.md`](research/omp-tui.md) — renderer, input, layout, 25-effect animation inventory, graphics, themes (Q1–Q9).
  - [`research/omp-commands.md`](research/omp-commands.md) — 56 CLI subcommands, 89 slash commands, 79 keybinding actions, 5 magic keywords (Q1–Q7).

---

## 1. The shape of the system

Five layers, bottom-up:

```
packages/coding-agent   CLI app: AgentSession, tools, prompts, MCP, extensions/hooks,
                        advisor, plan-mode, IRC, interactive TUI application
packages/agent          provider-agnostic loop: agentLoop/runLoopBody, compaction,
                        append-only context, speculation, live steering, output budget
packages/ai             multi-provider client: streamSimple + 11 dialects, providers/*,
                        registry, usage, error classification, auth
packages/tui            product-agnostic terminal engine: component tree, differential
                        row renderer, input decoding, overlays, widgets
crates/* (Rust, N-API)  bash 5.3 engine + 67 in-process utilities, grep/glob, edit engine,
                        ast-grep/tree-sitter, workspace isolation, PTY, desktop control
```

Four entry points share one engine: interactive TUI (`omp`), one-shot (`omp -p`),
NDJSON RPC over stdio (`omp --mode rpc`, plus `rpc-ui`), and ACP for editors (`omp acp`).
A Node SDK (`createAgentSession`, `SessionManager`, `ModelRegistry`) embeds the same session.

**The single most important design idea:** boundaries are contracts, not conventions.
Provider events are normalized into one vocabulary; the renderer never guesses whether
content is final; model/provider policy lives in a compiled KDL rule tree, never in
TypeScript `if`-chains; prompts live in static Handlebars `.md` files, never in code strings.

---

## 2. Harness

### 2.1 Turn lifecycle

`AgentSession.prompt → Agent.#runLoop → agentLoop() → runLoopBody()`
(`packages/agent/src/agent-loop.ts:655,1253`). Two nested loops: an outer `while(true)`
wrapping an inner `while(hasMoreToolCalls || pendingMessages.length)`. Each inner step:

1. Drain steering / follow-up messages into context.
2. `prepareProviderCall`: transformContext → convertToLlm → normalize →
   append-only context build → normalizeTools → transformProviderContext → sentToolDefinitions.
3. `beforeModelCall` gate (hooks may stop the run).
4. `streamAssistantResponse` via `streamSimple` → provider adapter.
5. `prepareToolCallDispatch`: id repair, intent, schema validation, `beforeToolCall`.
6. `executeToolCalls` under an **exclusive/shared concurrency scheduler**, ordered result emission.
7. `emitTurnEnd` → `onTurnEnd` hook (advisor, compaction checks, title generation…).
8. Loop decision: `hasMoreToolCalls = runnableStop && toolCalls.length > 0`.

Two details worth stealing:

- **`runnableStop` includes `stop`, not just `toolUse`** — Anthropic models emit tool calls
  under `end_turn`; only `length` (truncated args) excludes tool execution
  (`agent-loop.ts:1677-1701`).
- **System prompt + tool list are re-synced before every model call**
  (`syncContextBeforeModelCall`, `agent.ts:2054-2062`) — late-bound content (todo reminders,
  plan mode, MCP instructions, dynamic tools) reaches the wire without rebuilding the loop.

Termination: no runnable tool calls **and** the stop-boundary drain finds no late steering,
asides, or follow-ups. Early exits: deadline, external abort, gate stop, terminal tool-result
abort reason, thrown error.

### 2.2 Three-layer event vocabulary

| Layer | Contract | Events |
|---|---|---|
| Provider → agent | `AssistantMessageEvent` (`ai/src/types.ts`) | `start`, `text_start/delta/end`, `thinking_start/delta/end`, `toolcall_start/delta/end`, `image_end`, `done{stop\|length\|toolUse}`, `error{aborted\|error}` |
| Agent → host | `AgentEvent` (`agent/src/types.ts:1327-1356`) | `agent_start/end`, `turn_start/end`, `message_start/update/end`, `tool_execution_start/update/end`, `tool_stream_update` |
| App → UI | `AgentSessionEvent` | mirrors the above + queue changes, compaction, retry, advisor notes |

`message_update` carries **read-only full snapshots** of the streaming assistant message —
the UI never mutates them, and transcript rebuilds replay the same path as live streaming.

### 2.3 Streaming, dialects, steering

- **Dialect layer** (`packages/ai/src/dialect`): 11 in-band tool-calling protocols
  (glm, hermes, kimi, xml, anthropic, deepseek, minimax, harmony, qwen3, gemini, gemma)
  with a streaming `InbandScanner`. When a model "owns" a dialect, the harness sends **no
  native tools**, appends the protocol prompt, re-encodes history, and
  `wrapInbandToolStream` re-materializes text into native `toolCall` blocks — the loop
  above is unchanged. This is how one harness drives models with broken/absent native
  tool calling.
- **Partial tool-argument JSON**: parsed args lag the raw stream by a throttled parse
  window; previews render from the raw `partialJson` buffer (notably bash env assignments)
  and every render path (live + rebuilt transcript) must go through the same decode
  (`ToolArgsRevealController`).
- **Abort layering**: three signals, one listener (user abort, deadline, stream-rule abort)
  with synthesized aborted tool results so history stays pair-consistent.
- **Live steering**: provider-side injection of user messages mid-turn
  (`openLiveSteering`), drained at loop boundaries.
- **TTSR (time-traveling stream rules)**: synchronous incremental regex matching on text
  deltas; a match aborts the stream mid-token with a tool-scoped reason, then a 50 ms-deferred
  retry discards/keeps the partial output, injects a hidden rule message, and continues from
  the same point. Injections survive compaction. Judged `question` rules run post-hoc at
  threshold 0.7 and land as asides. Rules cost zero context until they fire.

### 2.4 Context management (the deepest part of the harness)

**Compaction** — six trigger paths: manual `/compact`; context-overflow recovery;
`stopReason=length` recovery; post-turn threshold maintenance; **mid-turn** maintenance
(pinned on for subagents); idle maintenance. Automatic paths try **context promotion**
(switch to a larger-window model) before compacting.

Threshold math (`compaction/compaction.ts:338-436`):

```
effectiveReserve = max(floor(window * 0.15), reserveTokens ?? 16384)
promptBudget     = clamp(window - reserve, 0, window - 1)
shouldCompact    = enabled && strategy != off && contextTokens > threshold
contextTokens    = max(providerReported, localEstimate)   // compression can't hide growth
```

Defaults: `keepRecentTokens = 20000`, `idleThresholdTokens = 200000`.
Method order: `["remote", "snapcompact", "handoff", "shake", "soft"]` — provider-native
server compaction first, bitmap-frame compression second, summary handoff third.

Cut-point rule: walk backwards over valid cut points accumulating tokens; **a cut never
separates a tool result from its call** (hard rule). Split turns produce two summaries
(history + turn-prefix). Files touched are tracked cumulatively and appended as a `<files>`
tree capped at 20.

**Pre-compaction pruning** (`pruning.ts`): protect newest 40k tool-output tokens; only act
if savings ≥ 20k; never blank results < 50 tokens. Uneventful results (zero-match search,
empty wait) are elided only when doing so won't itself cost a prompt-cache rebuild.

**Append-only context / prefix-cache stabilization** (`append-only-context.ts`):
`StablePrefix` freezes system prompt + tool specs by wire-identity fingerprint;
`AppendOnlyContextManager` handles exactly three cases — append, compaction (shrink →
clear+replay), in-place rewrite (truncate to longest stable prefix, re-append diverged tail).
The system prompt puts **all cwd-bound content in one trailing `<project-context>` block**
so the static prefix is shared across directories. Intent: only the new user message is a
cache miss each turn.

**Output budget** (`output-budget.ts:81`): shrink `max_tokens` per request so
`prompt + max_tokens ≤ window` on Chat-Completions hosts; anchor on the newest trustworthy
provider usage report and locally count only the tail (+10% margin, +16 framing tokens/message).

**Checkpoint/rewind**: a tool pair, not a loop primitive — `checkpoint` records a message
count + entry id; `rewind` truncates to it and writes a findings report; the entry id lets
rewind **branch the session tree** instead of only truncating.

**Cache warming** (`session/cache-warmer.ts`): a ski-rental model — refresh at
`min(90% of TTL, TTL−10s)` only when expected avoided miss-cost clears a savings floor;
streaming phase "rents" until the cheaper buy-out (rewrite prefix once, or compact+rewrite);
idle phase uses a fixed continuation probability over a 30-minute window.

**Speculative execution** (`speculative-execution.ts`): tools declare an effect class —
`{kind:"pure"}` or `{kind:"local_read", resources:[…]}`. **Nothing that writes is eligible**,
so rollback is a plain `discard()` with no journal. Admission at `toolcall_start` (stream
sessions) and `toolcall_end` (finalized), fingerprint re-checked at `claim()`; host
validate/commit/fallback hooks; undispached candidates discarded wholesale on abort/error.

**Replay policy** (`replay-policy.ts`): provider refusals are **dropped** on replay
(replaying refusal text feeds the classifier its own verdict); synthetic error placeholders
keep the "not executed" fact with neutral text.

### 2.5 Tools

29 built-ins + 3 hidden (README says 31; inventory in evidence report Q4.2): files/search
(`read`, `write`, `edit` with **hashline content-hash anchors**, `ast_edit`, `ast_grep`,
`grep`, `glob`), runtime (`bash` — in-process bash 5.3 engine, `eval` — persistent
Python/Bun kernels that can call back into agent tools over a loopback bridge), code
intelligence (`lsp` 14 ops, `debug` 28 DAP ops, `security_scan`), coordination (`task`,
`wait`, `todo`, `ask` HTML forms), desktop/web (`browser`, `computer`, `web_search` with 25
backends, `github`, `generate_image`, `tts`), memory/skills (`checkpoint`, `rewind`,
`retain`, `recall`, `reflect`, `memory_edit`, `learn`, `manage_skill`).

- **One path interface**: 16 internal URL schemes (`pr://`, `issue://`, `agent://`,
  `skill://`, `ssh://`, `conflict://`, `xd://`, `local://`, …) resolve inside every
  FS-shaped tool. `read pr://1428` has the same shape as `read src/foo.ts`; merge conflicts
  resolve by writing `@theirs` to `conflict://N`; subagent fields are pulled by path
  (`agent://<id>/findings.0.path`).
- **Result shaping**: oversized tool output spills to `local://N.log` files (per-session
  counter, exclusively claimed) instead of bloating context; line-page reads get a budget.
- **Approval**: permission flow with session grants; `ast_edit` stages a *(proposed)* card
  and applies atomically only after an accept write to `xd://resolve`.

### 2.6 Subagents, advisor, extensibility

- **`task`** fans out isolated workers (optionally in copy-on-write worktrees via `omp-iso`:
  APFS clones / btrfs-zfs reflinks / overlayfs / projfs / rcopy). Each worker has its own
  tool surface; the final yield is a **schema-validated typed object**, not prose.
  **IRC** gives peer-to-peer messaging between workers; `wait` blocks on background
  results/peer messages/steering. Lifecycle: park / revive / kill without aborting the
  parent; Alt+A opens the **Agent Hub** (roster, live transcripts, steering input).
- **Advisor**: a second model on its own context reviews every turn; one pure function
  (`resolveAdvisorDeliveryChannel`) decides steer/aside/preserve from severity,
  terminal-idle state, and an immune-turn window (default 3). An emission guard dedupes by
  severity rank, drops content-free phrases, caps non-blocker notes at 4 per update.
- **Hooks/extensions**: TypeScript modules with the same tool API, slash registry, hotkey
  table and TUI primitives as built-ins; hook lifecycle points around model calls, tool
  calls, turn end, session events. MCP clients integrate at the tool catalog; first-run
  discovery inherits `.claude`, `.cursor`, `.windsurf`, `.gemini`, `.codex`, `.cline`,
  copilot and vscode configs without migration.

### 2.7 Sessions, system prompt, providers

- **Sessions**: JSONL — one header + a **tree** of entries (branching is first-class).
  Resume/fork/export operate on the tree; persistence is append-with-fsync guarantees.
- **System prompt**: composed from static `.md` files with Handlebars (repo rule: *never
  build prompts in code*); context files (AGENTS.md and 8 other formats) injected as
  delimited untrusted data; a rulebook matching pipeline activates rules by glob/condition;
  per-model prompt adjustments come from the catalog, not string-matching model ids.
- **Providers**: 60+ providers, nine **roles** (`default`, `smol`, `slow`, `plan`, `commit`,
  `vision`, `task`, `advisor`, `tiny`) route work by intent; `Ctrl+P` cycles the active
  role's models. Model/provider policy (effort ladders, pricing, quirks, API routing) lives
  in a KDL rule tree compiled to `rules.json` — TS may branch only on structured
  `classifyModel()` facts. Fallback chains per role/model take over on 429/quota and restore
  on cooldown; round-robin credentials with session affinity and per-credential backoff.

---

## 3. TUI

### 3.1 Architecture — the finality inversion

Three layers own different questions:

1. **Engine (`packages/tui`)** — product-agnostic. Component tree where
   `Component.render(width) → readonly string[]`; **reference equality is the change
   signal**; `Container` memoizes concatenation on child-array identity (`tui.ts:537-667`).
2. **Composer (`prompt/composer.ts:437-603`)** — the layout owner. Implements
   `TerminalFrameProvider`; every frame it hands the engine an ordered `HistoryBatch`
   (append-only, already scrolled) **plus the complete mutable viewport**.
3. **Application (`modes/interactive-mode.ts`, controllers, tray, HUD, job widgets)** —
   decides what is final.

**Finality is an application decision, never inferred by the renderer.** Each accepted
history batch is written exactly once and acked by monotonic id, so retries and coalesced
frames are safe. The engine lives in the **normal screen buffer**: retired rows go to
native scrollback and are never re-audited; only the live viewport is diffed.

### 3.2 Differential writer

`#emitPlanFrame` (`tui.ts:3802-4086`): prepare rows (strip markers, classify, width-fit,
SGR coalesce; memoized by content+width+width-epoch+image-protocol) → diff by **string
identity** → emit either a row-move sequence (line feeds + **at most 3 DL/IL moves**, the
fallback when DEC 2026 synchronized output is unavailable) or absolute row rewrites inside
a synchronized-output frame → park cursor → ack batch after a single write.

**Frame scheduling** takes the max of three terms: `tui.maxFps` ceiling (default **30**,
`frame-rate.ts:8` — verified), a fixed 30 fps input cadence, and adaptive backpressure of
2× last frame cost capped at 200 ms; plus a 256 KiB output-backlog gate.

### 3.3 Input pipeline

Single-keystroke-to-action keybinding sets (no chords; canonical set lookup). Dispatch
order: TSP events → CPR replies → input latch → startup hold → overlay repair →
`capturesInput` → listeners → focused component. Vim is a **pure state machine**
(hardcoded, outside the rebindable registry). Multi-key gestures (double Ctrl+C within
500 ms, push-to-talk space-hold) are app-level state machines. Emacs kill-ring editing,
bracketed paste, mouse, fuzzy autocomplete, Windows AltGr/conhost handling all live in the
engine layer.

### 3.4 Screen layout

```
native scrollback (terminal-owned, immutable)   ← history batches, written once
──────────────────────────────────────────────  ← viewportTop anchor
HEADER (welcome/gradient logo)                  ┐
TranscriptContainer: settled blocks + live tail │
  user msgs, assistant markdown, thinking,      │ mutable viewport
  tool cards, todo, notices                     │ (diffed every frame)
chrome: queued msgs → HUD rows → working row    │
  (spinner · elapsed · shimmering intent · Stop)│
editor (box|band|pi|rail|rule|borderless)       │
  + autocomplete popup + attachment chips       │
status line (model, context %, git, cost…)      ┘
```

Overlays are screen-coordinate content composited over the viewport, never history;
fullscreen overlays borrow the alternate screen without touching normal-screen accounting.

### 3.5 Animation inventory (25 effects)

Three substrates: **(1)** self-rescheduling JS timers + `requestComponentRender`;
**(2)** wall-clock phase reads with no per-widget timer (shared spinner clock, shimmer);
**(3)** terminal-clocked effects on TSP terminals (JS timers explicitly skipped).
A global `motionEffectsEnabled()` switch gates *decorative* motion only — spinners and
streaming reveal are functional and gated separately.

| # | Effect | Driver | Key constants |
|---|---|---|---|
| 1 | Working-row spinner | shared clock | braille `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`, period **80 ms** (min 50, 0=static) |
| 2 | Tool-card spinners | one shared ticker for all cards | same glyphs, `spinnerTickDelay` |
| 3 | Status-line spinner | repaint on frames | 8 braille / 12 nerd / 4 ascii; active 250 ms, static 1000 ms |
| 4 | Thinking starburst | per-frame dwell | `✻ ✼ ❉ ❊ ✺ ✹ ✸ ✶`, dwell **70–230 ms** raised-cosine `(1−cos(2πf/8))/2`, mean ≈150 ms |
| 5 | Shimmer — classic band | wall-clock function | **30 cells/s**, 12-cell cosine band, tiers 0.22/0.65 |
| 6 | Shimmer — KITT scanner | wall-clock function | head 0.6, trail 7 cells, quadratic decay `(1−t)²`, ping-pong |
| 7 | Streaming text reveal | 30 fps interval | `nextStep = max(3, ceil(backlog/8))` graphemes/frame |
| 8 | Tool-argument reveal | 30 fps interval | same law + incremental JSON-string decode for write/edit/eval |
| 9 | Status-line brand fade | 40 ms frames | **450 ms** cubic ease-in-out, retargets from on-screen color |
| 10 | Speculation blink | setInterval | 600 ms half-period, motion-gated |
| 11 | Editor keyword sweep | 70 ms chain | period 1800 ms, phase `(now%1800)/1800` |
| 12 | Startup splash | 33 ms tick | **2600 ms**; 3-term sine water, starfield twinkle every 8 frames, diagonal-shared gradient escapes |
| 13 | Splash→scene dissolve | setup tick | 420 ms cross-dissolve |
| 14 | Setup outro | 33 ms tick | 1200 ms settle+dim |
| 15 | Welcome intro | 33 ms tick | 3000 ms, 2.5 sweeps, ease `1−(1−p)³` |
| 16 | Rewind camera slide | 16 ms tick | 160 ms ease-out cubic |
| 17 | Codex-reset fireworks | 85 ms tick | 34 frames ≈2.9 s, 6 bursts at fixed frames |
| 18 | Todo completion strike | 65 ms tick | 2 hold + 12 reveal frames ≈ 910 ms left→right strikethrough |
| 19 | Tool elapsed ticker | 1000 ms | only while partial/unsealed |
| 20 | Countdown timers | 1000 ms + expiry timeout | skipped on TSP (terminal counts) |
| 21 | HUD progress rows | event + timers | reveal 150 ms, done retain 1500 ms, failed retain 10000 ms, bar width 18 |
| 22 | Agent-hub clocks | setInterval | age tick 5000 ms, data coalesce 100 ms |
| 23 | Jobs sheet/widgets | setInterval | sheet 250 ms, widget 1000 ms, min ingest 250 ms |
| 24 | Native TSP fx | terminal clock | `fx:"pulse"`, shimmer/spinner/elapsed/meter nodes |
| 25 | Indeterminate progress | none (static glyph) | terminal animates on TSP |

**The shared spinner clock** (the most reusable idea):

```
tick(now)      = floor(now / intervalMs)
frame(n, now)  = tick(now) % n
tickDelay(p)   = max(1, ceil((floor(now/p)+1)*p − now))   // lands on the next boundary
```

All spinners read the same wall clock → frame-locked with zero coordination; timers arm to
the next *boundary*, so N spinners cost one repaint per period. Animation has its own CPU
budget: `nextDelay = max(tickDelay, min(200ms, lastFrameCost) * 9)` — animation stays
≤ ~10% CPU regardless of renderer load.

**Not animated** (deliberately): no message fade-in, no typewriter for tool results, no
animated borders, no software cursor blink, no scroll animation. Data-driven surfaces move
only when data changes.

### 3.6 Graphics, themes, robustness

- Images: kitty graphics (transmit-once/place-many), sixel, glyph protocol/deccara;
  charts, LaTeX→Unicode, HTML forms (`ask`), markdown pipeline with syntax highlighting.
- Themes: JSON theme files, dark/light + OS appearance tracking (mode 2031 + native macOS
  FFI), symbol sets (braille/nerd/ascii) chosen by capability.
- Capability detection drives every fallback; tmux/screen/multiplexer handling; Windows
  conhost vs VT input modes.
- Robustness: loop watchdog + loop sampler, output-stall watchdog, frame budget
  backpressure, a debug server. All rendering text is sanitized (tabs→spaces, width
  truncation, `~` path shortening, preview limits) on **every** path including errors.

---

## 4. Commands

Four registries drive everything (all source-verified in `research/omp-commands.md`):

| Registry | Size | Where |
|---|---|---|
| CLI subcommands | **56** (55 public + `__complete`), 6 aliases | `cli-commands.ts:27-319` |
| Builtin slash commands | **89** (48 with text handles, **41 TUI-only**), 9 aliases | `slash-commands/builtin-registry.ts` (8 per-area arrays) |
| Keybinding actions | **79** (32 `tui.*` + 47 `app.*`, declaration merging) | `packages/tui` keybinding tables |
| Magic keywords | **5** | `modes/magic-keywords.ts:93-137` |

Mechanics worth noting:

- **CLI dispatch is not argv[0]-strict**: `resolveCliArgv` hoists a subcommand past leading
  global flags and strips launch-only flags for non-launch commands; unknown bare plugin
  words are hard errors with hints; anything else becomes `launch`. 16 hidden
  `__omp_worker_*` selectors re-enter the same CLI entry as workers.
- **Slash dispatch**: parse name/args at earliest whitespace or colon → builtin →
  extension command → custom TS command → markdown file command; skills reachable as
  `/skill:<name>`. `handleTui` preferred in TUI; a text-mode `handle` is required for
  ACP/print dispatch. **Print mode dispatches only `/compact`, `/handoff`, `/retry`,
  `/mcp reauth`** — everything else goes to the model verbatim.
- **Completions are generated from live command/flag metadata** (bash/zsh/fish), so they
  cannot drift; model names complete from the bundled catalog, `--resume` from disk
  sessions. (The audit found 6 flags the parser accepts that `--help` metadata omits —
  drift exists only in help text, not completions.)
- **Vim mode sits outside the rebindable registry** (hardcoded state machine); defaults
  intentionally collide across contexts (Ctrl+P/R/S); Ctrl+D is dual-role.
- **Magic keywords**: `ultrathink`, `chillax`, `orchestrate`, `workflowz`, **`jevify`**
  (the fifth — bulk classification via the eval kernel's `judge()` — is absent from the
  README). Matching is prose-only: a length-preserving mask hides fenced/inline code and
  HTML sections; only standalone lowercase words trigger; each appends a hidden notice
  and/or changes thinking effort. `workflowz` maps to settings key `magicKeywords.workflow`.
- Session controls: `/vibe` (director mode over persistent fast/good workers), `/fresh`
  (reset provider stream state without touching the transcript), `/model`, `/login`,
  `/collab` (E2E-encrypted relay sharing with QR), `/review`, `/annotate`, `/debug`.

---

## 5. What this means for GoCode

GoCode already has the bones: approval-gated tools, SQLite sessions, `/compact`,
plan/scratchpad, MCP, 20 providers, Bubble Tea v2 TUI, harmonica springs. The omp designs
that transfer best, in rough priority order (full mapping in [`PLAN.md`](../PLAN.md)):

1. **Event contract + streaming normalization** — one normalized event vocabulary between
   provider layer and agent loop, and between loop and TUI; read-only snapshots; partial
   tool-arg previews from the raw buffer.
2. **Context engineering** — compaction cut-point rule (never split tool call/result),
   reserve/threshold math, pruning with protected recent output, output-budget fitting,
   stable-prefix prompt ordering for cache hits.
3. **TUI finality split** — settled history vs mutable viewport; differential updates only
   on the live tail; overlays composited, never history.
4. **Animation system** — shared wall-clock spinner phase (one tick source, N widgets),
   the `max(3, ceil(backlog/8))` reveal law for smooth streaming, motion gate for decorative
   effects, explicit CPU backpressure. GoCode already depends on harmonica for springs.
5. **Command surface** — registry-driven slash commands with TUI/text handle split,
   generated completions, magic-keyword masking, keybinding tables as data.
6. **Later/optional** — dialect layer for weak tool-calling models, speculation for
   read-only tools, advisor loop, subagent fan-out with typed yield.

## 6. Limits of this analysis

- Read-only static analysis at one commit; **no runtime observation** of omp.
- `packages/catalog/src/models.json` (generated megafile) deliberately unread; catalog rule
  semantics are docs-level. `agent-session.ts` (16.9k lines) and `interactive-mode.ts`
  (10.2k lines) traced at the seams, not exhaustively. Remote compaction lanes described
  from docs, not line-by-line. Rust crates characterized by role/layout, not internals.
- Provider-credential env vars (~250) catalogued by doc only; 9 CLI/TUI vars spot-verified.
- Docs-vs-code drift found and recorded: 5th magic keyword `jevify` missing from README;
  6 launch flags missing from `--help` metadata; `docs/stream.md` is terminal broadcasting,
  not provider streaming (name trap).
- Orchestrator spot-verification: `DEFAULT_MAX_FPS = 30` (`frame-rate.ts:8`), speculation
  effect classes `pure`/`local_read` (`speculative-execution.ts:177-180`), 5 magic keywords
  (`magic-keywords.ts:93-137`) — all confirmed directly in source.