# GoCode Changelog

### Unreleased

- **OpenCode Go and Mistral Vibe providers** — two new built-in gateway presets. `opencode-go` targets OpenCode's subscription gateway (`https://opencode.ai/zen/go/v1`, `OPENCODE_API_KEY`, curated coding models); `mistral-vibe` targets Mistral's Vibe coding model on la Plateforme (`MISTRAL_API_KEY`, default `mistral-large-4-0`). Both are overridable like any gateway via `[provider.opencode_go]` / `[provider.mistral_vibe]` (or a `custom` entry). Also fixes a docs typo that mangled the default MiniMax model id.

### v0.8.0 — 2026-11-21

A major upgrade to the terminal UI and agent harness. Full notes: `docs/releases/v0.8.0.md`.

- **Theme System** — the TUI gains a full semantic colour system: 56 semantic color tokens across six built-in themes (gocode-dark default, gruvbox-dark, keanu, rosé pine/moon/dawn) with exact palette values, CIELAB-derived diff tints, a WCAG badge-ink contrast rule, JSON user themes with base inheritance in the config `themes/` dir, `[tui] active_theme` and `transparent` settings, full style rebuild with render-cache invalidation on switch, and the terminal background painted from the theme. Light mode is a theme (rose-pine-dawn), not an adaptive-color guess. 
- **Screen Layout** — new screen geometry: one-cell outer frame; a 32-column right sidebar with a gradient GoCode wordmark over a diagonal field, model/provider, workspace and live turn metrics; the universal 2-cell chat gutter (primary rule for user messages, thick rule on the focused item) with a 120-column content cap and blank-row rhythm; a borderless editor with a fixed 4-cell prompt column (badge or `:::` dots — text never shifts); a merged single status/help row; and compact mode below 120×30 that collapses the sidebar into a one-row header. Focus state joined the render-cache identity, so focus moves re-render only the affected blocks; the streaming benchmark is unchanged (0.165 block-renders/chunk at 200 and 2000 settled messages). Session/MCP/plan sidebar sections land with the data wiring.
- **Markdown, Tool Cards & Diffs** — assistant messages render as full markdown through glamour with a per-theme chroma configuration (one syntax role table shared by code blocks, file views and diffs), NBSP+U+FE0E inline-code sentinels, and stable-prefix content caching keyed by theme generation. Tool cards use a plain-head anatomy: a plain head (spinner, state word, elapsed) over an indented body tinted on the less-visible surface with faint params. Unified diffs render as two-band tinted rows — darker sign gutter, lighter code band — in both the transcript and approval previews. Added `charm.land/glamour/v2`; goldmark raised to v1.7.17 to clear GO-2026-5320 (`govulncheck` clean). Block-renders/chunk stay at 0.165; thinking display awaits the harness emitting reasoning events.
- **Dialogs & Badges** — Dialog chrome: rounded primary frame with a gradient ╱ title rule over an undimmed backdrop, borderless mnemonic buttons, WCAG-contrast badges, and destructive/warning frame variants. The permission dialog gains input grace (425ms quiet / 1500ms hard cap / 500ms reopen-skip — it only delays keys, never approves), danger-flagged requests that default to Deny with a negative approve button, and `t`/`f` split/fullscreen diff toggles. Idle Ctrl+C now opens a quit confirmation (safe Nope default; a second Ctrl+C quits immediately). New theme picker (Ctrl+T) with live preview, esc-revert and enter-to-persist, plus a palette editor (ctrl+e) that writes the theme file before applying colours. The session switcher gains deleting (ctrl+x) and renaming (ctrl+r) states backed by store mutations (`UpdateTitle` added). The sidebar shows session identity, configured MCP servers and live plan progress; a READONLY badge marks per-turn read-only grants.
- **Keymap, Focus, Completions & History** — a focus enum (editor/main/sidebar) with the dispatch contract: ctrl+c before dialogs (approval keeps deny+quit), dialogs see keys first, esc cancels a busy turn, then focus routing. Full chat keyset: j/k line scroll, J/K item selection with scroll-into-view, u/d half-page, b/f paging, g/G top/bottom, global ctrl+end follow — and fixes for several keybinding defects: space expands only (never pages), c/y/C/Y copy centrally via OSC 52 with a toast, esc clears the highlight, H/L scroll any item horizontally. Command palette moves to ctrl+p (ctrl+k kept as alias). New @file completion popup: gitignore-aware bounded async walk, four-tier ranking, upward-growing reversed geometry, circular navigation, quoted insertion. Prompt history persists per workspace (200 entries, owner-only file) with draft save/restore. Status toasts replace the status row for 5s with sequence-stamped expiry and OKAY!/HEY!/WARNING/ERROR badge anatomy — one-shot timers, no idle tick.
- **Shared Clock & Scramble Spinner** — one generation-guarded 50ms clock (20fps) now drives everything animated: flush coalescing, spinner scramble, elapsed badges, the opt-in reveal pacer and the mascot springs. Exactly one outstanding tick; stale generations and superseded sequences are dropped; a tick lost for 2s is re-armed; an idle session arms nothing. The braille spinner is replaced by a scramble primitive: deterministic per-column birth steps in [0,20), 10 pre-rendered frames, initialized at 20 frames, a 4-step ellipsis every 8 frames (suppressed when an elapsed badge shows), and a per-rune theme gradient — 15 cycling cells in transcript cards, a compact 3-cell row in the status bar. Streamed text now renders on arrival by default (`smooth_streaming` flipped to false, opt-in pacing preserved); the motion gate still degrades decoration to static while elapsed data keeps refreshing at 1Hz.
- **Pubsub Event Bus** — new `internal/pubsub` generic broker: 4096-slot per-subscriber ring, non-blocking lossy publish with counted drops, must-deliver bounded at 50ms per subscriber, in-place coalescing by entity key that preserves queue position, an overflow resync signal, and idempotent close. The agent loop publishes every LoopEvent (deltas lossy; tool results, errors, exits and turn-end must-deliver, turn-end mapped to `run_complete`), and the TUI + headless boundaries now consume one fan-in pump each instead of private Observe closures — a turn-end gate keeps done messages from overtaking bus events, and the headless JSONL schema stays byte-identical. Permission request/notification payloads are on the bus ready for the permission service. Note: GoCode deltas are stream frames, not snapshots, so they ride the lossy lane unkeyed — coalescing them deterministically drops text; the coalescing lane is contract-tested for snapshot-style publishers.
- **Permission Service** — the approval gate becomes a request/grant service: `normal`/`yolo` modes (`[permissions] mode` + `--yolo` flag; yolo auto-grants safe calls but REFUSES dangerous ones with a message naming the way out; sysadmin deliberately omitted — the deny-list and redaction stay unconditional). A pure conservative danger classifier (12 shell rules + outside-workspace paths, case/word-boundary aware) feeds the modal's Deny-default and the bus payloads. Session grants are keyed `{session, tool, action, path, grant_key}` — bash grants scope to the exact command, file paths normalize to directories, exact-args grants preserved as a class. Resolution is atomic first-answer-wins on SERVER-stored params (a responder cannot widen a grant), prompts serialize per scope (default global), cancellation denies without hanging, and the request/notification lifecycle rides the bus must-deliver with an async observer tap ready for hooks. Headless session auto-approval is opt-in via policy; deny-by-default preserved.
- **Hook Engine** — new `internal/hooks` package: 13 events (SessionStart/End, TurnStart/End, StopFailure, Interrupt, PreToolUse, PostToolUse, PostToolUseFailure, PreCompact, PostCompact, PermissionRequest/Result) with normalized names + aliases, regex matcher, parallel execution aggregated in config order, 30s timeout with 1s abandon grace and an injectable executor. Claude Code-compatible wire: stdin JSON + env, stdout JSON decisions, exit codes 0/2/49/other. Aggregation: deny>allow>none, halt OR, shallow-merge `updated_input`, last-wins `updated_output`. Wiring: PreToolUse block/rewrite/per-call approve before the permission gate, PostToolUse(+Failure) feedback and output merge, exactly-one TurnEnd/StopFailure/Interrupt funnel, SessionStart/End at process boundaries, PreCompact deny blocks compaction, Permission hooks via the async PromptObserver tap (never delay the tool), strict `[hooks]` TOML, headless parity (NonInteractive skips turn hooks only). The per-call hook-approval slot is implemented: context stamp + gate registration, consumed exactly once, checked after auto_approve before keyed grants, deny-list still outranks it, Granted audit emitted.
- **Transcript Repair, Anchored Compaction & Stop Conditions** — `EnsureToolPairing` now dedupes per call preferring a real result over an error/interrupted one and skips empty assistant messages (idempotent, untrusted-context delimiters byte-intact). Compaction persists a flagged assistant summary row plus the retained tail and anchors replay at it (`messages.is_summary_message`, `sessions.summary_message_id` via additive migration); a resumed transcript reads from the anchor onward sliced by row id with the summary relabelled to a user turn, and pre-anchor rows are never deleted (a full `SaveSession` rewrite inlines the transcript and clears the anchor). The summary prompt restates the live plan/todo list. Stop conditions gain a remaining-context backstop (≤20k tokens for >200k windows, ≤20% otherwise) and a signature-window guard (same tool+args in >5 of the last 10 steps stops the turn, catching interleaved loops the consecutive guard misses). Session persistence is debounced 33ms and coalesced, with synchronous flush on terminal transitions (tool results, turn end, abort) so a cut-short turn loses nothing. v0.7.5 auto-compaction contracts (reserve, pair-safe cut, pruning, overflow recovery) preserved. Note: GoCode compacts at round boundaries and protects in-flight tool blocks, so an interrupted-call re-queue is unnecessary; the remaining-context rule is a backstop because GoCode's 80%-budget threshold already fires earlier.
- **Queued-Turn Dispatch & Cancel-on-Entry** — new `internal/agent/dispatch.go` per-session state machine: a monotonic accept-sequence and cancel high-water mark, three states decided atomically (cancel-on-entry / queued / active), a FIFO queue, and compare-and-delete active registration so a finishing older run never wipes a newer one. `CancelTurns` raises the mark, cooperatively cancels the active stream (the partial assistant message is kept and synthetic interrupted tool results preserve pairing), and settles EVERY dropped prompt with a persisted cancelled row plus a terminal `Cancelled` event so no caller hangs. `RunTurn` self-drains the queue for serial callers; `RunTurnAccepted` is the front-end executor entry (no double-queueing). The TUI `acceptPromptLoop` admits prompts mid-turn and the queued-pills chip reflects pending turns; headless routes its pending queue through the dispatcher with an additive `cancel` input and a settlement-aware end handshake (JSONL schema byte-identical). Security: the `BeginTurn` read-only-grant reset now fires only for EXECUTING turns, so a queued or cancel-on-entry call can never clear a running turn's grants. `LoopEvent.Cancelled` (additive) distinguishes settlements from mid-stream cancels.
- **Documentation & Verification** — new `docs/themes.md` (six built-in themes, the 56-key token model, CIELAB derivation rules, user-theme JSON `{base, overrides}`, the Ctrl+T picker and Ctrl+E palette editor) and `docs/hooks.md` (13 events + aliases, the Claude Code-compatible stdin/env/stdout wire, exit codes 0/2/49/other, aggregation rules, `[hooks]` TOML examples, and the unconditional deny-list/redaction invariant). `docs/architecture.md` refreshed for every the latest changes subsystem tied to real package/file names; `docs/configuration.md` documents all 69 config keys and the CLI flags re-derived from source with an added-in column; README feature refresh (no fabricated screenshots). Verification: build/vet/test gates green, `make eval` green, **1007** top-level tests, streaming benchmark held at **0.165** block-renders/chunk, `govulncheck` reports **0 vulnerabilities affecting code**, and **0 broken doc links**. `staticcheck` reports 12 pre-existing `internal/tui` findings (unused styles/funcs + minor style suggestions) unchanged.

### v0.7.5 — 2026-11-19

A harness and TUI upgrade. The agent gains structured turn metrics with provider-reported usage, automatic pair-safe compaction with overflow recovery, streamed tool arguments and mid-turn steering. The terminal UI gains a render cache (per-token cost no longer scales with transcript length), a shared-clock animation system with smooth streaming reveal and a configurable motion gate, a command registry with generated help, Tab completion, prompt history, a Ctrl+K command palette, and live tool cards with session/model pickers.

- **Release Hygiene** — dead-code sweep (reachability analysis + staticcheck) found no removable code: all nine unreachable-from-main helpers are exercised by active tests, including the approval security contracts. `.gitignore` now excludes local SQLite/WAL and test-binary artifacts; `go mod tidy`, `staticcheck` and `govulncheck` are clean. Full notes: `docs/releases/v0.7.5.md`.
- **Structured Turn Lifecycle & Usage** — every turn now emits turn-start and exactly one turn-end event carrying metrics (tokens, tool calls, elapsed, retries) on all exit paths including abort and guard stops; provider-reported usage is parsed from OpenAI-compatible, Anthropic and Ollama streams, with opt-in `[provider] stream_usage` requesting it from gateways; context accounting takes the larger of local estimate and provider report; the TUI status line shows context-%, last-turn tokens and elapsed time, headless `turn_done` carries a metrics object, and the plain REPL prints a turn summary.
- **Automatic Context Management** — history is now pruned and summarized automatically before it hits the limit: pair-safe cut points that never separate a tool call from its result, reserve-based threshold math that stays sane on small windows, pre-round and post-turn maintenance, and overflow recovery that compacts and retries once instead of silently dropping turns (last-resort truncation now announces what it dropped). Old tool output is elided to token-count placeholders with the newest ~40k tokens protected. Per-turn state (environment, plan, scratchpad) moved into one trailing prompt block so the static prefix stays byte-stable for provider caches; untrusted-context delimiters and notices are unchanged. New `[context] auto_compact` (default on) and `keep_recent_tokens` (default 20000) settings; manual `/compact` is untouched.
- **Tool-Argument Streaming & Mid-Turn Steering** — tool-call arguments now stream to the UI as they arrive on all three dialect paths (OpenAI-compatible, Anthropic, Ollama): pending tool cards fill in live through display-only deltas while approval and execution still wait for complete arguments, and headless gains an additive `tool_delta` frame. Enter while streaming queues a steering message instead of being ignored — applied at the next provider-round boundary with a notice and a queued chip; headless accepts `{"type":"steer"}` mid-turn (turns now run asynchronously with piped user input kept in order), and the plain REPL is unchanged.
- **Render Cache & Smooth Redraws** — the transcript no longer re-renders per token: settled messages render once and are cached (message value equality is the change signal, terminal width the invalidation epoch), while only the live tail and mutating tool cards re-render; streamed updates coalesce to at most 30 redraws per second with tool starts/results and turn ends still immediate. Autoscroll now correctly follows long sessions (bottom is sampled before content growth). The perpetual idle tick is gone — an idle session costs no CPU — and reduced-motion stops decorative animation chains at the source. Measured: block-renders per chunk stay constant at 0.165 for 200 vs 2000 settled messages, roughly 25–55× faster than the old per-token full re-render.
- **Animation System** — frame-locked motion: every live spinner (status bar and pending tool cards) is frame-locked to one shared wall-clock clock (80ms braille frames, boundary-aligned ticks, a single chain that stops when nothing spins); streamed assistant text and tool-argument previews reveal smoothly at max(3, ceil(backlog/8)) clusters per 30fps tick without ever splitting combining marks; a classic shimmer band sweeps the working state word at 30 cells/s using the previously unused shimmer palette roles; pending cards show elapsed badges. New `[tui] motion` (auto/reduced/off) and `smooth_streaming` settings gate decorative motion — functional indicators degrade to a static glyph and 1s data refreshes instead of disappearing — and terminal reduced-motion is honored end-to-end. The mouse wheel now scrolls the transcript.
- **Command Registry & Palette** — slash commands moved from a hand-edited switch into a single registry: dispatch, generated `/help`, Tab completion and the new Ctrl+K fuzzy command palette all read the same table, so help can no longer drift from behavior. All 13 commands act identically (including `/commit` approval and `/model` fail-closed validation); commands that take arguments are staged at the prompt from the palette instead of running with defaults. Keybindings are now a data table preserving every existing binding; Tab completes command names and `/model`/`/provider` arguments; Alt+P/Alt+N (and Up on an empty prompt) recall prompt history. Overlays are managed as a stack where approval always wins focus — a tool request can never hide behind the palette or picker.
- **Live Cards & Pickers** — tool cards show explicit lifecycle states: pending with streaming arguments, running, ✓ done in the success color, ✗ error. `/sessions` in the TUI opens a fuzzy session picker (short id, excerpt, updated-at, message count) that resumes through the engine's validated path; plain terminal and headless keep the text list. The model picker refreshes on open through the same bounded concurrent collection as startup (2s per provider, 3s overall), preserving the current selection and falling back to the snapshot on failure. The steering chip shows a count when several messages are queued.

### v0.7.4 — 2026-11-13

A security- and harness-focused release: approval previews now use a real diff engine, model/tool text is sanitized before rendering, secrets are scrubbed from child process environments and transcripts, session storage is owner-only and workspace-scoped, and the agent gains context compaction, parallel read-only tools, session-scoped approval grants, a plan tool, auto-verification, per-turn metrics, headless mode, and an eval suite. Full notes: `docs/releases/v0.7.4.md`.

- **Approval Integrity** — file previews now use a real Myers unified diff, `/commit` shows the file list and message it will commit, model/tool text is sanitized before terminal rendering, and approved writes are refused if the file changed after preview.
- **Secret Containment** — child processes no longer inherit credential-shaped env vars, transcript/export redaction covers tool-call arguments, and session WAL/SHM files plus config/logs are owner-only.
- **Bounded & Scoped Execution** — file/search reads are capped, provider error bodies are limited, tool timeouts kill process groups, hardlink identity checks are cached, and session auto-resume is scoped to the current workspace.
- **Harness Upgrades** — per-model context budgets, model-summarized `/compact`, persistent scratchpad, parallel read-only tool calls, session-scoped approval grants, plan tool + optional auto-verify, per-turn metrics, and hardened MCP restart/timeout/caps.
- **Headless & Evals** — `--headless` JSONL mode with an explicit approval policy, and a deterministic eval suite runnable with `make eval`.
- **CI & Supply Chain** — release toolchain from `go.mod`, SHA-pinned actions, npm checksum verification, and govulncheck/staticcheck gates.
- **Security Remediation** — removed the unpatched `github.com/ollama/ollama` dependency (every published advisory for it has no fixed version) in favor of a local stdlib HTTP/NDJSON client, and raised the floors to Go 1.26.11 and `golang.org/x/net v0.60.0`; `govulncheck ./...` is clean again.
- **Real Context Compaction** — `/compact` now asks the active model to summarize earlier turns (goal, decisions, open files, next steps) instead of inserting a placeholder count, and falls back to the placeholder when the provider cannot answer.
- **Model-Aware Token Budgets** — histories are truncated against each model family's context window (80% of the window by default), and an explicitly configured limit still wins.
- **Better Token Estimation** — estimates now weight CJK text and punctuation-heavy code higher than Latin prose, so source and non-English transcripts no longer undercount.
- **Persistent Scratchpad Tool** — a new `scratchpad` tool records agent-maintained working state in the session and system prompt so it survives compaction; the scratchpad column is added to existing databases in place.

### v0.7.1

A cross-platform distribution and security-fix release. GoCode gains an npm-based launcher for its native executable, while ignore-rule matching is hardened against filename-case variations on Windows and macOS.

- **npm / npx / bunx Distribution** — added the `gocode-cli` npm package as a lightweight launcher for GoCode's native executable. The launcher selects the appropriate Linux, macOS, or Windows binary for x64 and ARM64, then forwards command-line arguments and terminal input/output.
- **Release Binary Fetching** — added a PowerShell script to retrieve platform-specific binaries from GitHub Releases. The script supports Linux and macOS `.tar.gz` archives and Windows `.zip` archives, and synchronizes the npm package version with the selected release version.
- **Case-Insensitive Ignore Rules** — ignore patterns are normalized for case-insensitive filesystems on Windows and macOS. Differently cased file and directory names can no longer bypass matching rules in `.gocodeignore` or the `.gitignore` fallback.
- **Ignore-Rule Regression Tests** — expanded test coverage for exact, uppercase, and mixed-case filenames, directories, nested paths, and negated patterns on supported operating systems.
- **Workspace Tool Test Coverage** — added tests around workspace path handling and sensitive-file protections to help prevent regressions in file-tool access checks.
- **Release Workflow Improvements** — updated the release workflow to support publishing the npm launcher alongside versioned GitHub release assets.

### v0.7.0

A security-and-robustness release: the CODEBASE-ANALYSIS audit was applied, the TUI migrated to Bubble Tea v2, and tool calls gained live status cards.

- **Bubble Tea v2 Migration** — migrated the terminal UI to `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, and `charm.land/lipgloss/v2`. Updated keyboard handling, terminal modes, and layout measurement.
- **Adaptive Terminal Theme** — introduced startup theme resolution with `GOCODE_THEME=light|dark` overrides and terminal capability detection for reduced-color and non-interactive environments.
- **Live Tool Cards** — tool calls now display a running state before approval or execution, then update with their result. File-write and patch diffs receive dedicated rendering.
- **Sensitive-File Protection** — strengthened canonical path checks, rejected problematic Windows path forms, and hardened `code_search` against symlink and hardlink aliases.
- **Tool Registry and MCP Isolation** — duplicate tool registrations are rejected, and MCP tools use the `mcp_<server>_<tool>` namespace to prevent collisions with built-in tools.
- **Session Integrity on Interrupt** — ensured unanswered tool calls receive synthetic results when a turn is interrupted or stopped by a loop guard.
- **Provider Error Handling** — improved retry behavior, error reporting, response validation, stream-truncation handling, and GitHub Copilot token refresh.
- **Terminal-Size Safety** — constrained approval modals to the available terminal dimensions while keeping the conversation and input visible.
- **Safer `/commit` and `/model` Commands** — hardened Git path handling and made model switching fail closed when validation fails.
- **MCP Client Hardening** — added message-size limits, request deadlines, and clearer startup failure reporting.
- **Session Store Hardening** — improved SQLite initialization, database permissions, connection handling, and row-scan error checks.
- **Loop Guard Improvements** — canonicalized tool-call arguments when detecting repeated calls and tracked consecutive repeats.
- **Strict Configuration** — unknown `config.toml` keys now produce errors instead of being silently ignored.
- **Cross-Platform Test Reliability** — `make test` skips race testing with an explanatory message when the required GCC/CGO environment is unavailable.

### v0.6.2 Fixes

- Fixed the status-bar mascot animation so its movement is visible during a real session.
- Corrected antenna alignment with the mascot's portrait.
- Made animation frames advance using their scheduled timestamps for more deterministic behavior.
- Added tests that verify the mascot and status bar actually change during a turn.

### v0.6.1 Fixes

- Fixed `gocode mcp add` parsing so flags intended for an MCP server command are not consumed as GoCode flags.
- Corrected mascot animation timing and frame-ticker lifecycle issues.
- Fixed portrait row-width inconsistencies during animation.
- Removed unused mascot state and corrected misleading animation comments.

### v0.6.0 Additions

- Added a terminal mascot with idle, thinking, working, done, and error states.
- Introduced spring-based animation and blinking during active turns.
- Centralized terminal UI design tokens and adaptive theme colors.
- Added context-aware keyboard hints and a responsive startup banner.

### v0.5.2 Additions

- Added provider endpoints for GitHub Copilot, xAI, Mistral, MiniMax, DeepSeek, Together, Fireworks, Cerebras, Zhipu, NVIDIA NIM, and Hermes Agent.
- Implemented GitHub Copilot device authentication and token exchange.
- Preserved provider reasoning content across tool-call turns and persisted it in SQLite sessions.
- Added an idempotent migration for existing session databases.
- Fixed custom-provider name collisions and consolidated duplicated SSE parsing logic.
- Updated the Go version to address reachable standard-library vulnerabilities and removed unused code.

### v0.5.1 Additions

- Unified terminal and non-TUI execution around a single agent loop.
- Fixed `/exit` and `/quit` handling.
- Expanded tests for command wiring and configuration.
- Removed secret-scanner noise from placeholder credential test fixtures.

### v0.5.0 Additions

- Added iteration limits and repeated-tool-call guards to bound agent turns.
- Enabled context truncation in the TUI before provider requests.
- Fixed approval handling for piped input.
- Made `session.persist` control whether sessions are stored.
- Removed unused configuration keys.
- Added secret redaction to `shell_exec` output.
- Added configurable tool iteration and repetition limits.

### v0.4.0 Additions

- Hardened workspace path handling, sensitive-file protection, and approval previews.
- Added the `code_search` tool.
- Added commands for managing custom provider endpoints.
- Introduced turn-aware context compaction and provider retry handling.
- Improved symlink-aware workspace resolution and multi-platform CI.

### v0.3.0 Additions

- Added interruption of active generation with `Ctrl+C` or `Esc`.
- Improved keyboard handling and exit aliases.
- Added active-provider model validation.
- Improved unknown slash-command suggestions and conversation scrolling.

### v0.2.0 Additions

- Added SQLite-backed session persistence.
- Added Model Context Protocol (MCP) support over stdio.
- Introduced project-level `AGENTS.md` and global `CONTEXT.md` instructions.
- Added `.gocodeignore` filtering and configurable tool permissions.
- Added the interactive model picker, Git attribution, structured logs, and provider diagnostics.
