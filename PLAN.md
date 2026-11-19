# GoCode ← omp — Implementation Plan

**Port the reverse-engineered oh-my-pi harness and TUI mechanisms into GoCode, phase by phase.**

- Design reference: [`docs/omp-reverse-engineering.md`](docs/omp-reverse-engineering.md) (synthesis) and [`docs/research/`](docs/research/) (full evidence audits of omp at `840cc4b`, plus the GoCode baseline audit [`gocode-audit.md`](docs/research/gocode-audit.md) at `fc93321`).
- Baseline (measured): `go build ./...`, `go vet ./...`, `go test ./...` all clean — 11 packages ok, 579 top-level tests, 816 pass lines incl. subtests, 23 environment-driven skips (Windows), 0 failures. 145 `.go` files, ~33k LOC. Toolchain: go1.26.11 (auto-downloaded; host go1.26.5 satisfies `GOTOOLCHAIN`).
- Priority per request: **TUI and harness first**. Everything below attaches to existing seams — no rewrites of the engine, the approval model, or the security boundaries.

## Status

| Phase | State | Commit |
|---|---|---|
| P1 — turn lifecycle + usage events | ✓ done | `59e8ba8` |
| P2 — automatic compaction | ✓ done | `8380959` |
| P3 — tool-arg streaming + steering | ✓ done | `555561c` |
| P4 — render cache + coalescing | ✓ done | `3d0c9e3` |
| P5 — animation system | ✓ done | `ebad4e7` |
| P6 — command registry + palette | ✓ done | `716e7b5` |
| P7 — live cards + pickers | ✓ done | `4dcec3f` |
| P8 — docs + final verification | ✓ done | (docs commit) |

## 0. Mechanism mapping (omp → GoCode)

| omp mechanism | GoCode today | Target phase |
|---|---|---|
| 3-layer event contract, `turn_start/turn_end`, read-only snapshots | `LoopEvent` w/ 7 kinds; `EventTurnEnd` declared, **never emitted** (loop.go:37); metrics only in `slog` | P1 |
| Provider usage reported, `max(provider, estimate)` accounting | local CJK-weighted estimates only; no usage parsed | P1 |
| Auto-compaction: threshold math, pair-safe cut points, pruning, overflow recovery | manual `/compact` only; overflow **silently drops whole turns** (context.go:404) | P2 |
| Stable prompt prefix (dynamic content trailing) | plan/scratchpad (dynamic) sit mid-prompt, before context blocks (prompt.go:128-180) | P2 |
| Tool-argument streaming (`toolcall_delta`, raw-buffer previews) | tool calls arrive complete; SSE buffers args until flush (copilot.go:574-603) | P3 |
| Live steering drained at loop boundaries | `inputCh` cap 1; Enter ignored while streaming (model.go:256) | P3 |
| Finality split: settled history vs mutable viewport; diff only the tail | **whole transcript re-rendered per token** (model.go:297,804) | P4 |
| Frame cadence governance; no perpetual idle tick | idle 5 fps tick forever (model.go:146); no redraw coalescing | P4 |
| Shared wall-clock spinner clock (one tick source, N widgets) | no spinner at all; static `running…` text | P5 |
| Reveal law `max(3, ceil(backlog/8))` @30fps | deltas appended raw per chunk | P5 |
| Shimmer band (30 cells/s, cosine, 3 tiers) | `shimmerDim/Bright` palette roles **defined, unused** (theme.go:59) | P5 |
| Motion gate (`motionEffectsEnabled`) | `ReducedMotion` **detected, never read** (capability.go:35) | P5 |
| Command registry + generated help + completions | hand-edited switch (commands.go:56); `/help` literal duplicates it | P6 |
| Keybinding tables as data; overlay stack | 15 hardcoded bindings; two overlay booleans over one compositor | P6 |
| Tool cards: streaming preview, elapsed ticker, status transitions | cards mutate in place, args truncated 200 chars, no elapsed | P7 |
| Pickers refresh on open | model picker = startup snapshot, never refreshes (run.go:89-125) | P7 |

**Deliberately not ported** (stretch, need their own plans): dialect layer for weak tool-calling models, speculative execution, advisor/watchdog, subagent fan-out + typed yield, cache warming, TTSR stream rules, magic keywords, session-tree branching, internal URL schemes. Rationale: each is a subsystem, not a seam attachment; GoCode's 20-provider surface is OpenAI-compat + 2 native dialects, and its security model (approval-first) must not be weakened by speculation or auto-execution.

## 1. Execution protocol

- One phase = one implementation pass + one commit (Conventional Commits, matching history: `feat(agent): …`, `perf(tui): …`, `docs: …`).
- Implementation agents work in `D:\CODING\GoCode` directly, **do not commit**, and do not touch `CHANGES.md`, `README.md`, or `docs/` — the orchestrator reviews the diff, re-runs the gates, updates the changelog, and commits.
- Gates per phase (all must pass before commit): `gofmt -s -w .` → `go build ./...` → `go vet ./...` → `go test ./...`. `make eval` at P8. Race detector when CGO/gcc available (Makefile falls back gracefully).
- Security invariants (never regress): approval before mutation; redaction at the single transcript boundary (`redactToolResult`, approval.go:407); synthetic results keep tool-call pairing on abort; untrusted context stays delimited + marker-neutralized; no new dependencies without justification; Windows-safe tests (no symlink/file-mode assumptions — the 23 existing skips are environmental, don't add more).
- Strict config: unknown `config.toml` keys are errors — every new key is registered with a default and a test.
- Each phase adds contract tests for its new behavior (repo rule: tests defend observable contracts, not implementation echoes).

## 2. Phases

### P1 — Harness: structured turn lifecycle + usage events
**Seams:** `LoopEventKind`/`LoopEvent` (loop.go:20-58), `RunTurn` (loop.go:177), `streamResponseGuarded` (loop.go:265), metrics.go, `StreamChunk` (provider.go:23-32), `toTeaMsg` (run.go:153-170), headless.go, loop_plain.go, status bar (model.go:552-587).
**Design:**
- Add `EventTurnStart`; emit `EventTurnEnd` (the free slot) exactly once on **every** exit path (normal, abort, guard-stop, error), carrying `TurnMetrics{PromptTokens, CompletionTokens, ToolCalls, Elapsed, Retries, ProviderReported}` reusing `newTurnMetrics`.
- `StreamChunk.Usage *Usage` — parse when present: OpenAI-compat final-chunk `usage` (copilot.go SSE), Anthropic `message_start`/`message_delta` usage, Ollama `prompt_eval_count`/`eval_count`. Opt-in `[provider] stream_usage` (default false) sends `stream_options:{include_usage:true}` on the OpenAI-compat path only; parsing is always on.
- Context accounting takes `max(localEstimate, providerReported)` (omp's rule: compression can't hide growth).
- Map new events in all three front-ends: TUI status line gains context-% + last-turn tokens + streaming elapsed; headless `turn_done` gains a metrics object; plain REPL prints a one-line turn summary.
**Acceptance:** event-order contract tests (start → deltas → tools → end, end-once on abort); per-dialect usage fixture tests; headless schema test; all gates green.
**Commit:** `feat(agent): emit structured turn lifecycle and usage events`

### P2 — Harness: automatic context management
**Seams:** `ContextManager.Truncate/Compact/CompactWithSummary` (context.go:404/460/537), `ResolveBudget`, `streamResponseGuarded` round boundary, `EnsureSystemPrompt` (loop.go:166), prompt.go:128-180, config.
**Design:**
- Threshold math (omp port): `reserve = max(15% of window, 16384)`; auto-compact when `contextTokens > budget − reserve` equivalent, expressed against GoCode's existing 80% budget; `keep_recent_tokens` default 20000 replaces blind `keepLastTurns=2` for automatic paths (manual `/compact` behavior unchanged).
- **Pair-safe cut points**: walk backwards to a user-message boundary that never separates a tool result from its call (`EnsureToolPairing` already exists — reuse); summarize the dropped span via the existing bounded+redacted `CompactWithSummary`, placeholder fallback on provider failure.
- Triggers: post-turn maintenance, pre-request mid-round check, and **overflow recovery** (provider context-overflow error → compact → retry once) replacing silent whole-turn drops; `Truncate` remains last resort but emits an explicit `EventNotice`.
- Pruning-lite before summarizing: elide old oversized tool outputs to `[output truncated — N tokens]`, protecting the newest ~40k tokens of tool output; plan/scratchpad untouched (they live in the prompt).
- Prompt prefix stability: move dynamic plan/scratchpad rendering into one **trailing** block after the static base + delimited context blocks (omp's trailing `<project-context>` idea), keeping untrusted delimiters and marker neutralization intact; update any order-asserting tests deliberately.
- Config: `[context] auto_compact = true`, `keep_recent_tokens = 20000` (registered, defaulted, tested).
**Acceptance:** cut-point invariant tests (never split pairs, incl. split-turn case); threshold tests with a fake estimator; overflow-recovery test (no silent drop); compaction-survival test for plan/scratchpad; gates green.
**Commit:** `feat(agent): automatic compaction with pair-safe cut points`

### P3 — Harness: tool-argument streaming + mid-turn steering
**Seams:** `StreamChunk` (provider.go:23-32), `streamOpenAISSE` (copilot.go:477-634), anthropic.go SSE, ollama.go NDJSON, loop.go:312-314, `RunTurn` vicinity (steering API), run.go:55/182-220 (`inputCh`), model.go:256-258, headless.go.
**Design:**
- `StreamChunk.ToolCallDelta *ToolCallDelta{Index, ID, Name, ArgsDelta}` emitted as args arrive (OpenAI-compat per-index accumulation already exists — tap it; Anthropic `input_json_delta`; Ollama emits one complete delta). Complete `ToolCalls` flush unchanged for compatibility.
- New `EventToolDelta` → UI updates the pending tool card's arg preview live. **Approval still happens on complete args only** — never on partial JSON (security invariant).
- `AgentLoop.Steer(text)`: queued messages drained at the round boundary (top of the provider round, omp's `pendingMessages` pattern), injected as user messages with a steering marker; TUI Enter while streaming **queues** instead of ignoring (queued chip in the input area); Esc/Ctrl+C cancel semantics unchanged; headless gains `{"type":"steer"}`; plain REPL unchanged (synchronous).
**Acceptance:** delta-ordering fixtures per dialect; steering lands between rounds, never mid-stream; abort still yields pair-consistent synthetic results; approval-on-complete-args test; gates green.
**Commit:** `feat(agent,provider): stream tool arguments and accept mid-turn steering`

### P4 — TUI: render cache + finality split
**Seams:** `renderMessages`/`setStreamingMessage` (model.go:293-301,794-805), `viewport.SetContent` call sites, `Model.render` (model.go:524-547), anim.go tick chain, `Init` idle tick (model.go:143-150).
**Design:**
- Rendered-block cache keyed by (role, label, content-hash, diff-hash, width-epoch): settled messages render once; only the live assistant tail and mutating tool cards re-render; `viewport.SetContent` joins cached strings (omp's settled-history vs mutable-viewport split, inside Bubble Tea's viewport).
- Coalesce streaming redraws to ≤30 fps: chunks append to the buffer immediately, viewport flush rides a frame tick; autoscroll only when `AtBottom()` (existing behavior preserved).
- Kill the perpetual idle tick: ticks arm only while something animates; idle mascot renders a static pose (blink on demand). `ReducedMotion` honored here (full gate in P5).
**Acceptance:** cache-invariant tests (same content+width → no re-render; width change invalidates); a `testing.B` benchmark showing per-chunk cost independent of transcript length; visual behavior unchanged (existing TUI tests green); gates green.
**Commit:** `perf(tui): cache settled transcript blocks and coalesce streaming redraws`

### P5 — TUI: animation system (omp port)
**Seams:** anim.go (`tickUntil`/`frameMsg`/`animEpoch`), mascot.go cadence, theme.go:59-60 (unused shimmer roles), capability.go:35 (`ReducedMotion`), status bar, tool cards, config `[tui]`.
**Design:**
- **Shared spinner clock** (omp's most reusable idea): `tick(now)=floor(now/80ms)`, `frame(n,now)=tick%n`, boundary-aligned next-tick delay; one tick chain drives the working-row spinner **and** all live tool-card spinners (braille `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`), frame-locked with zero coordination. Static mode = frame 0 + 1 s label refresh.
- **Smooth streaming reveal**: display buffer paced by `nextStep = max(3, ceil(backlog/8))` graphemes per 33 ms tick (assistant text + tool-arg previews); snap-to-full when disabled or reduced-motion.
- **Shimmer**: classic band sweep at 30 cells/s, 12-cell cosine band, 3 tiers mapped to the existing `shimmerDim/Bright` roles, on the working-row intent text; one ANSI open/close per same-tier run.
- Tool-card elapsed ticker (1 s); status-line turn elapsed + context-% (P1 data).
- **Motion gate**: `[tui] motion = "auto"|"reduced"|"off"` + `capabilities.ReducedMotion` gate decorative motion (shimmer, mascot springs); functional indicators degrade to static glyphs, never disappear.
- Mouse wheel scrolls the viewport (fixes enabled-but-unhandled `MouseModeCellMotion`).
**Acceptance:** clock-math tests (phase alignment, boundary delay, static mode); reveal-law tests; motion-gate tests; no perpetual idle tick; gates green.
**Commit:** `feat(tui): shared-clock spinners, smooth streaming reveal, shimmer and motion gate`

### P6 — Commands: registry, completions, keymap data, palette
**Seams:** `HandleCommand` switch (commands.go:35-274), `/help` literal (commands.go:57-76), `Model.Update` (model.go:152-421), `placeModal` (model.go:763-784), picker.go, `renderHelpLine` (model.go:599-615).
**Design:**
- `CommandSpec{Name, Aliases, Description, Usage, Handle}` registry replaces the switch; all 13 commands behavior-identical; `/help` **generated** from the registry (drift eliminated); unknown-command error unchanged.
- Keybindings as data (`keymap.go`: key/context/action/help); the 15 existing bindings preserved exactly; help line and `/help` key section generated from the table.
- Tab completion: slash names + `/model`/`/provider` arguments from the live registry/catalog; inline hints via the existing help-line seam.
- Prompt history (in-memory, per session) on non-conflicting keys (Alt+P/N; Up when input empty at start) — no change to viewport scroll bindings.
- Overlay state generalized from two booleans to a small stack with approval priority; **command palette** (Ctrl+K) as a fuzzy `bubbles/list` overlay driving the registry, reusing `placeModal`.
**Acceptance:** dispatch-equivalence tests for all 13 commands; generated-help test (no duplicated literal); completion + palette navigation tests; every existing keybinding unchanged (table-driven test); gates green.
**Commit:** `feat(agent,tui): command registry, tab completion, keymap table and command palette`

### P7 — TUI: live cards, pickers, overlay polish
**Seams:** tool-card rendering (model.go:332-378), `pendingTools`, picker.go + run.go:89-125, overlay stack (P6), steering queue (P3).
**Design:**
- Tool cards: streaming arg preview (P3 deltas) with the reveal law, status transitions (pending → running → done/error) with spinner/elapsed from P5, diff rendering preserved.
- Session picker overlay (`/sessions` and palette entry) replacing the plain text list; model picker **refreshes on open** (bounded, concurrent, with the existing 2 s/3 s deadlines) instead of the startup snapshot.
- Queued-steering chip visible above the input; compaction notices render as existing GoCode cards.
**Acceptance:** card-lifecycle tests; picker-refresh test (fake registry); overlay-priority test (approval always wins); gates green.
**Commit:** `feat(tui): live tool cards, session picker and overlay polish`

### P8 — Docs, README, verification
**Design:** write the three missing docs the README already links — `docs/architecture.md` (from the baseline audit + current source), `docs/configuration.md` (full TOML surface incl. new keys), `docs/providers.md` (20 gateways, custom providers, auth flows); README refresh (features, keys, commands tables — generated from the registries where possible); fix CONTRIBUTING's stale Go-version claim (1.22 → go.mod's 1.26.11) and package tree; finalize `CHANGES.md` Unreleased narrative.
**Acceptance:** every README/doc relative link resolves; documented flags/keys re-derived from the binary/source; full gate suite + `make eval`; final report of measured test counts.
**Commit:** `docs: architecture, configuration and provider references; README refresh`

## 3. Dependencies & ordering

```
P1 ──► P2 ──► P3 ──────────────► P7 ──► P8
 │              │                 ▲
 └──► P4 ──► P5 ┴──► P6 ──────────┘
```

P1 first (event vocabulary everything else consumes). P4 may overlap P2 only if file sets stay disjoint (agent vs tui) — default is **sequential** to keep one clean commit per phase and avoid same-file conflicts. P5 consumes P1 (status data), P3 (reveal targets), P4 (tick substrate). P6's overlay stack enables P7.

## 4. Risks

- **Anthropic is a parallel SSE path** — every provider-level feature (P1 usage, P3 deltas) must be implemented twice or normalized; plan doubles the test fixtures accordingly. Full dialect normalization is stretch.
- **Prompt-order change (P2)** may break order-asserting tests and prefix-cache assumptions elsewhere; update deliberately, keep untrusted-block defenses byte-intact.
- **Strict config** rejects unknown keys — new keys need registration + defaults + tests in the same phase.
- **Windows test environment**: 23 skips are environmental; new tests must not add platform-dependent skips. Race detector unavailable without gcc/CGO — Makefile fallback is the norm here.
- **Bubble Tea v2 redraw semantics**: P4's coalescing must preserve `AltScreen`/`MouseMode` re-declaration on every `View()` return path (model.go:511-522) and the `AtBottom()` autoscroll contract.