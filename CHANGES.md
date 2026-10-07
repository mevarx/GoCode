# GoCode Changelog

### v0.7.0

A security-and-robustness release: the entire CODEBASE-ANALYSIS audit applied (34 items), the TUI migrated to Bubble Tea v2, and every tool call now renders as a live card.

- **Bubble Tea v2 Migration** — the TUI is rebuilt on `charm.land/bubbletea/v2`, `charm.land/bubbles/v2` and `charm.land/lipgloss/v2`. Key messages arrive as `tea.KeyPressMsg`, the alternate screen and mouse mode are declared by the model's `View`, and layout geometry is measured from rendered output instead of hardcoded rows, so the frame no longer drifts a row taller than the terminal.
- **Adaptive Palette Without `AdaptiveColor`** — v2 removed that type, so the palette is now resolved once at startup in `internal/tui/theme.go` via `lipgloss.LightDark`, with `GOCODE_THEME=light|dark` as an override. `internal/tui/capability.go` additionally detects NO_COLOR, CLICOLOR_FORCE, dumb terminals, non-TTY output and reduced-motion requests so the UI can degrade rather than spray escape codes into a pipe.
- **Live Tool Cards** — the agent loop now emits `EventToolStart` (with the raw arguments) the moment a call is announced, so the TUI opens a `running…` card before approval or execution, and the matching result replaces that card in place. Diffs produced by `file_write`/`file_patch` travel as their own field and render with green/red line fills instead of being flattened into the text summary.
- **Security: Sensitive-File Protection Closed** — the sensitive-file check now canonicalizes before comparing: case-folded on Windows/macOS, and trailing-space, trailing-dot and NTFS alternate-stream (`::$DATA`) forms are rejected outright in `ValidatePath`. `code_search` now resolves each candidate through `EvalSymlinks`, re-confines it to the workspace, runs the same canonical sensitive check, and skips hardlink aliases by `os.SameFile` identity — closing the hole where the alias read through a name `file_read` already blocked.
- **Security: Tool Namespace and MCP Registry** — `Registry.Register` refuses duplicate names instead of silently overwriting, and MCP tools are unconditionally namespaced as `mcp_<server>_<tool>`, so a server can never shadow a built-in or inherit its auto-approval entry.
- **Session Integrity on Interrupt** — guard-stops and mid-batch cancellations now write synthetic `tool_result` messages for every unanswered call id before the turn returns, so the session no longer wedges with "400 on every later turn" after a single Esc. A parked interrupt is drained between turns so it cannot cancel the next message.
- **Provider Robustness** — exhausted retries now return a readable body (the provider's actual error, not an empty 429), `Retry-After` is clamped, only idempotent methods are retried, in-band `{"error": …}` payloads and non-SSE 200s surface as errors, Anthropic scanner cap raised to 8 MiB, truncation stop-reasons become explicit errors instead of silently stored "complete" answers, and the Copilot JWT is re-exchanged on 401/403.
- **Terminal-Size Safety** — the approval modal clamps to the terminal width and height and is composited over the live frame instead of replacing it; at minimum viable widths the transcript and input box stay visible.
- **`/commit` Works in the TUI** — `engine.AskApproval` is backed by the real approval modal, and the git invocation uses `--` and a pathspec so a crafted path cannot inject flags and the commit cannot sweep unrelated staged changes.
- **`/model` Fails Closed** — validation errors from `Models()` now abort the switch instead of silently accepting the unvalidated name, and the status bar re-syncs from the engine's actual provider/model after every turn.
- **MCP Client Hardening** — a 16 MiB message ceiling, a 60s per-request deadline, stdin writes moved out of the call mutex, and startup failures are printed and returned rather than silently swallowed.
- **Session Store Hardening** — `busy_timeout` is applied before `journal_mode`, the pool is pinned to one connection, the database file is created `0600`, open failures surface a warning, and all row scans check `rows.Err()`.
- **Loop Guard** — repeated-call detection now keys on canonicalized arguments (JSON key order and whitespace no longer reset it) and counts consecutive repeats only, using the previously dead counter.
- **Config Strictness** — unknown keys in `config.toml` are now an error naming the typo, instead of being silently ignored and disabling the control they meant to set.
- **`make test` Degrades Gracefully** — `-race` runs when gcc/cgo is available and is skipped with a printed note where it cannot run, instead of failing outright on Windows.

### v0.6.2 Fixes

The mascot had shipped looking finished and moving like a still image. Both defects were only visible by driving a real turn and rendering the screen, because every test up to that point checked the mascot in isolation.

- **The Status Bar Mascot Never Moved** — the bob spring feeds only the hero portrait, which the startup banner shows once and never again, and the sway spring was driven at half amplitude. It peaked at 0.73 cells, so `quantize` rounded it to zero on every frame. In the one place the mascot is visible for a whole session it was frozen: 400 frames of working produced exactly one distinct sprite. Sway now runs at full amplitude, and the sprite has one cell of travel inside a field one cell wider, so the pad moves from the right of the mascot to its left while the field stays exactly 8 cells in every state.
- **The Antenna Sits One Cell Left of the Lid** — `padTo` centres a glyph within the whole portrait width, which includes the two-space indent, so the stalk did not meet the `┴` it sprouts from. It is now placed on the joint's column.
- **`frameMsg.at` Was Dead** — every frame carried a timestamp that nothing read; the handler stepped the mascot against the wall clock instead. A frame now advances by when it was scheduled rather than when it was processed, which is more correct when frames batch and is what makes the animation deterministic under test.

Two tests now walk a whole turn and assert the rendered status bar and the sprite both change across 400 frames of working. The mascot does not blink while working, so blinking cannot make them pass; both fail on the previous code with `1 distinct`.

### v0.6.1 Fixes

Corrections to v0.6.0, found by independent review and by measuring rendered output rather than reading it.

- **`gocode mcp add` Rejected Every Server Command That Takes a Flag** — `gocode mcp add filesystem npx -y @modelcontextprotocol/server-filesystem .`, the example in this README, failed outright with `unknown shorthand flag: 'y'`. Cobra parsed the *server's* flag as GoCode's and exited before the server was configured, so no MCP server whose command takes a flag could be added at all. Cobra's `--` escape hatch does not help here: with `Args` consuming positionals, `--` only ends flag parsing *before* the first positional, which for this command is the server name, so it never reaches `-y`. The rule is now positional — GoCode's flags come before the first non-flag token, everything after belongs to the server, and `--` anywhere ends GoCode's section. See [MCP](#model-context-protocol-mcp).
- **The Mascot Did Not Actually Move** — `rate()` drove the animation oscillator at 0.22 cycles/frame, which at 20fps is 4.4Hz — faster than the 5.2 rad/s spring could follow. The spring attenuated the drive to about 0.06 cells, which rounded to zero on every frame, and the hero's row-shift branch was unreachable: the antenna could never lift. Every test still passed, because nothing had measured whether the mascot moved at all. The rate is now 0.03 cycles/frame, where the spring tracks at 89% of the requested amplitude.
- **A Comment Described Behaviour That Did Not Exist** — `waveAt`'s doc claimed the triangle was "smoothed at the turning points" and "holds briefly at each end". Measured over a full cycle there is no dwell and the slope is flat: it is a hard triangle. The code was right and the comment wrong — the corner is deliberate, since a stiff signal into an under-damped spring is what produces the ease-out.
- **Frame Ticker Chains Compounded Across Turns** — the animation ticker was gated only by a boolean, so a tick in flight when a turn ended re-armed once the next turn started, and every later turn added another. Each frame now carries the turn it belongs to.
- **Portrait Row Width Was Only Checked at Rest** — `hero()` padded a negative bob with a hardcoded 7-space row while every other row was 9 cells. The row is fixed, and the test now sweeps the whole animation instead of only the pinned pose. The old assertion passed while the defect it was meant to prevent was live.
- **Dead Fields Removed** — `mascot.since` was written but never read, and its comment described a relax-to-idle that no code performed.

### v0.6.0 Additions

- **A Mascot** — GoCode now has a character. It sits in the status bar as a fixed-width sprite and appears in the startup banner as a full portrait, with an antenna, a face, and five expressions tied to what the agent is actually doing: `idle`, `thinking` (prompt sent, no tokens yet), `working` (streaming), `done`, and `error`. A cancelled turn relaxes to idle rather than showing a failure face, because interrupting is a user action, not a failure.
- **Spring Physics, Not Keyframes** — Motion is driven by `charmbracelet/harmonica` damped springs chasing a shaped oscillator. A spring accelerates and decelerates on its own, so retargeting mid-flight produces natural movement with no easing curve to tune. The bob spring is deliberately under-damped (0.55) so it overshoots once and settles — at critical damping it reads as a machine.
- **The Drive Rate Is Measured, Not Guessed** — A spring cannot follow a drive faster than its own natural frequency, and the mascot shipped completely motionless because the oscillator ran at 4.4Hz against a spring rated for ~0.8Hz: the signal attenuated to 0.06 cells and rounded to zero every frame. Every test still passed — nothing had checked whether the mascot moved at all. The rate is now 0.03 cycles/frame, where the spring tracks at 89% of the requested amplitude, and states differ by amplitude rather than by rate. `TestBobActuallyMovesEnoughToQuantize` measures peak displacement per state so this cannot regress silently.
- **Animations That Cost Nothing When Idle** — The frame ticker runs at 20fps while streaming and 10fps while the model is thinking, and stops entirely when no turn is running. A parked session redraws zero frames rather than burning a core animating a mascot nobody is watching.
- **Blinks** — While a turn is running, the mascot blinks on a 4s cycle. A parked session freezes its pose rather than running a ticker nobody is watching.
- **Design Tokens in One Place** — `internal/tui/styles.go` and `internal/tui/theme.go` are the single source of truth for the visual language: no call site writes a hex value. The palette adapts to the resolved terminal background (`GOCODE_THEME` overrides detection), so light terminals get darkened light values rather than merely lightened ones. The direction is **cool chrome, warm content** — surfaces and interactive elements are azure/slate, anything the agent *did* is amber/red, so a glance at colour alone tells you whether you are reading UI or reading output.
- **Context-Aware Help Line** — The hint under the input box now lists the keys that work *right now*. Mid-turn it says interrupting is the only useful action instead of listing keys that currently do nothing, and it collapses progressively on narrow terminals.
- **Startup Banner Reworked** — The `GOCODE` wordmark is replaced by the mascot, with the session's provider and model shown as chips. Below roughly 40 columns it falls back to a text-only layout, because a creature squeezed into 30 columns is noise rather than character.

### Bug Fixes Found During the Rework

- **MCP tool names were double-prefixed** — `NewMCPTool` computed a correctly guarded `server_tool` name and then threw it away; `Spec()` re-derived the name with no prefix guard, so a server that already namespaces its tools produced `srv_srv_foo`. The dead computation is gone and the guard now lives in the one place that uses it.
- **Dead assignments in three file tools** — `file_read`, `file_write` and `file_patch` each assigned `path := a.Path` before immediately overwriting it in both branches of the following conditional.

### v0.5.2 Additions

- **Eleven New Provider Endpoints** — GitHub Copilot, xAI (SpaceXAI), Mistral, MiniMax, DeepSeek, Together, Fireworks, Cerebras, Zhipu (GLM), NVIDIA NIM, and a Hermes Agent bridge (`127.0.0.1:8642`). Every base URL was taken from the vendor's own documentation rather than guessed.
- **GitHub Copilot Support** — Copilot is not a bearer-token API, so it gets a real provider rather than a config row: a GitHub OAuth token is exchanged for a short-lived Copilot JWT, and the exchange also returns the API address to use. The JWT is cached and refreshed 60s before expiry, and re-exchanged when the underlying OAuth token rotates. `gocode auth copilot --client-id <id>` runs GitHub's device flow and stores the token with owner-only permissions. **The token exchange endpoint and editor headers are reverse-engineered from GitHub's VS Code extension and are not documented by GitHub — treat this as best-effort.**
- **SSRF Guard on the Copilot Base URL** — because that base URL arrives in a network response, it is validated before the JWT is sent to it: HTTPS only, GitHub-owned hosts only, no credentials in the URL, no port. An untrusted host is a hard error, never a silent downgrade. Covered by tests including `api.githubcopilot.com.evil.com` and `user:pass@api.githubcopilot.com`.
- **Reasoning Preserved Across Turns** — MiniMax and DeepSeek document that the complete assistant message, including its `reasoning_content`, must be replayed into history to keep the reasoning chain intact across a tool-call turn. That field was previously dropped at the stream boundary, silently degrading multi-turn tool calling on those providers. It now flows stream → session → SQLite → next request.
- **Existing Databases Migrated In Place** — `CREATE TABLE IF NOT EXISTS` does not add columns to an existing table, so the new `reasoning_content` column is added by an explicit, idempotent migration. Verified by opening a hand-built pre-v0.5.2 database: old data survives and new writes work.
- **Custom Providers No Longer Break on Name Collisions** — making `deepseek` built-in would have made GoCode **refuse to start** for anyone who had already added it via `provider add`. A saved custom endpoint now takes precedence over the same-named built-in, covered by a regression test.
- **Four Standard-Library CVEs Closed** — `govulncheck` reported 12 reachable vulnerabilities at v0.5.1. Four were Go stdlib CVEs (GO-2026-6218, GO-2026-6090, GO-2026-5972, GO-2026-5026) reachable from this module's HTTP client; pinning `go 1.26.8` in `go.mod` clears them, taking the count to 8. The remaining 8 are Ollama advisories against the `github.com/ollama/ollama` module with **no upstream fix** (`Fixed in: N/A`); upgrading to v0.35.0 was tested and does not clear them, so the dependency was left alone. They are server-side issues in Ollama itself — keep Ollama bound to loopback.
- **Duplicated SSE Parser Removed** — the gateway proxy and Copilot each carried their own ~120-line copy of the OpenAI streaming parser. They now share one `streamOpenAISSE`, so tool-call assembly and `[DONE]` handling cannot drift apart. Net −120 lines even after adding Copilot and reasoning support.
- **Dead Code Removed** — `Registry.AllModels`, `GatewayProxyProvider.DefaultModel` and `BaseURL` had zero callers. Removed rather than shipped.
- **`.gitattributes` Added** — with `core.autocrlf=true` on Windows, a stash/pop round-trip silently rewrote working-tree files to CRLF and failed CI's `gofmt` gate on files whose committed content was correct. Line endings are now pinned per file type.

### v0.5.1 Additions

- **One Agent Engine, Two Views** — `internal/agent.AgentLoop` is now the only implementation of an agent turn. The TUI is a view over it rather than a second loop. Previously two copies of the same logic had to be kept in sync by hand, which is how the TUI came to omit context truncation while the plain loop did it. Verified by grep: one `Stream` call site, one `Truncate` call site, one tool-dispatch site.
- **`/exit` and `/quit` Now Work** — both were documented in `/help` and the README, but only the bare words `exit` and `quit` were recognized; the documented slash form fell through to the unknown-command path.
- **Test Coverage in the Wiring Layer** — `cmd/gocode` 12.4% → 16.0%, `config` 19.2% → 21.9%, `agent` 57.2% → 66.6%. The config keys added in v0.5.0 are now asserted to round-trip, so a renamed key fails a test rather than silently disabling a user's limits.
- **Secret-Scanner Noise Removed** — test fixtures for the shell redaction suite are built at runtime rather than written as literals, so routine pushes no longer raise credential alerts. No real credential was ever committed; the fixtures were placeholders that detectors could not distinguish from live keys.

### v0.5.0 Additions

Correctness and safety fixes, each verified against the real binary:

- **Bounded Agent Turns** — a turn now stops at an iteration cap (default 50) or after repeated identical tool calls (default 3). Previously a model that kept requesting tools looped without limit; a reproduced runaway made 68,077 provider requests in one turn before being killed, and now stops after 4 with an explanation.
- **Context Truncation in the TUI** — the default UI now truncates history before every provider request, matching the plain loop. History previously grew unbounded in the mode most users run until the provider rejected the request.
- **Approval Gate Works on Piped Input** — the gate and the prompt loop now share one stdin reader. Previously a piped `y` was swallowed by a second buffered scanner and every approval failed with `failed to read input`, so nothing could be approved non-interactively.
- **`session.persist` Honored** — `persist = false` now actually disables the session store. It was parsed and ignored, so every message was written regardless.
- **Dead Config Keys Removed** — `approval.auto_approve_reads` / `_writes` / `_shell` and `session.history_dir` were parsed but never read. Use `permissions.auto_approve` and `permissions.deny`.
- **Secret Redaction in `shell_exec` Output** — credential-shaped values are masked before output reaches the model or the session transcript, and commands referencing sensitive paths are flagged in the approval preview.
- **Anthropic Stream Parser Tested** — first coverage for the native Anthropic path, including tool-argument reassembly across fragmented events.
- **New `[tools]` Limits** — `max_tool_iterations` and `max_repeated_tool_calls` tune the turn guard; zero or negative uses the defaults.

### v0.4.0 Additions

- **Hardened Security** — workspace path confinement, sensitive file protection, bounded shell execution, and approval-before-execution diff previews
- **`code_search` Tool** — search the workspace with per-file offsets and result metadata
- **Custom Provider Commands** — manage OpenAI-compatible endpoints via `gocode provider add` / `list` / `remove`
- **Turn-Aware Context Compaction** — keeps long sessions reliable without losing context
- **Provider Retry Transport** — automatic retries with backoff on transient provider failures
- **Symlink-Aware Workspace Resolution** — resolves workspace paths through symlinks
- **Multi-Platform CI Quality Matrix** — race-detected testing and trimmed release binaries with ldflags versioning

### v0.3.0 Additions

- **Interruptible Generation** — Cancel turns in progress with `Ctrl+C` or `Esc` without terminating the session
- **Safer Key Handling** — `Esc` clears textarea when idle; double `Ctrl+C` cleanly exits; `/exit` and `/quit` aliases
- **Model Validation** — `/model <name>` validates model availability against the active provider before switching
- **Command Guardrails** — Unrecognized slash commands provide helpful `/help` suggestions instead of querying the model
- **Smart Viewport Scrolling** — Avoids auto-scroll jumping when reading previous conversation history during streaming

### v0.2.0 Additions

- **SQLite Session Persistence** — Automatically saves and resumes sessions across terminal restarts
- **Model Context Protocol (MCP)** — Extend with any MCP server (`stdio` transport) via `gocode mcp add`
- **Project Context (`AGENTS.md`)** — Auto-loads project-specific instructions from CWD hierarchy
- **Global Context (`CONTEXT.md`)** — User-wide conventions from `~/.config/gocode/CONTEXT.md`
- **`.gocodeignore` Filtering** — Protect sensitive files with gitignore-style patterns
- **Granular Tool Permissions** — Configure `auto_approve` and `deny` lists in `config.toml`
- **Interactive Model Picker** — Fuzzy-search models with `Ctrl+L` in TUI mode
- **Git Attribution** — Auto-format commit trailers (`Assisted-by: GoCode:<model>`)
- **Structured Logging** — `gocode logs --tail N --follow` for debugging
- **Diagnostics** — `gocode doctor` to check provider health

---

