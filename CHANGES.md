# GoCode Changelog

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
