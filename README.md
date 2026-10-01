<div align="center">

# GoCode

### Open-Source AI Terminal Coding Agent

**A high-performance, Go-native, local-first AI coding agent for your command line.**

[![Go Report Card](https://goreportcard.com/badge/github.com/mevarx/GoCode)](https://goreportcard.com/report/github.com/mevarx/GoCode)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go 1.22+](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Release](https://img.shields.io/github/v/release/mevarx/GoCode)](https://github.com/mevarx/GoCode/releases)

[Overview](#overview) •
[Features](#features) •
[Quick Start](#quick-start) •
[Configuration](#configuration) •
[FAQ](#faq)

</div>

---

## Overview

**GoCode** is a lightweight, open-source AI terminal coding agent written in Go. It turns your command line into an interactive pair programming environment capable of reading project files, editing code via structured patches, executing shell commands, and debugging errors in real time—all with strict human-in-the-loop approval.

Unlike heavy Python-based or Node.js-based AI CLI tools, GoCode compiles into a **single, zero-dependency binary** with instant startup latency and minimal memory footprint.

GoCode is **provider-agnostic** and **local-first**: run completely offline with local LLMs via **Ollama** or **llama.cpp**, or connect seamlessly to leading cloud AI models from **Google Gemini**, **Anthropic Claude**, **OpenAI**, **Groq**, **OpenRouter**, **Qwen**, **Kimi**, **OpenCode Zen**, and more.

---

## Features

### Core Capabilities

- **Single Native Go Binary** — Fast startup, low resource usage, zero Python or Node.js dependencies
- **Local-First & Privacy-Focused** — Runs 100% offline with local models via Ollama or llama.cpp
- **20 Built-in Provider Gateways** — Ollama, OpenAI, Gemini, Claude, GitHub Copilot, xAI (SpaceXAI), Mistral, MiniMax, DeepSeek, Groq, OpenRouter, Together, Fireworks, Cerebras, Zhipu, NVIDIA NIM, Qwen, Kimi, Hermes Agent, and OmniRoute
- **Custom API Endpoints** — add any OpenAI Chat Completions-compatible service, including local llama.cpp, with its own base URL and API-key environment variable
- **Human-in-the-Loop Approval Gate** — Explicit confirmation before executing commands or modifying files
- **Enforced Workspace Boundary (file tools)** — `file_read`/`file_write`/`file_patch`/`code_search` are confined to the workspace, traversal- and symlink-proof, and blocked from sensitive files. `shell_exec` is **not** confined — see [What is actually enforced](#tools--security-architecture)
- **On-the-Fly Switching** — Switch providers or models dynamically with `/provider` and `/model` commands

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

## Quick Start

### Prerequisites

- **Go 1.22+** installed (if building from source)
- An active AI provider: **Ollama** for local execution, or an API key for cloud providers

### Installation

#### Option 1: Install via `go install` (Recommended)

```bash
go install github.com/mevarx/GoCode/cmd/gocode@latest
```

#### Option 2: Build from Source

```bash
git clone https://github.com/mevarx/GoCode.git
cd GoCode
go build -o gocode ./cmd/gocode/
./gocode
```

#### Option 3: Download Binary

Download pre-built binaries from the [Releases page](https://github.com/mevarx/GoCode/releases).

### Basic Usage

```bash
# Auto-resume last session (default behavior)
gocode

# Start a fresh session
gocode --new

# Resume a specific session
gocode --session sess_20260815190405_a1b2c3d4

# Use a specific provider and current model
gocode --provider openai --model gpt-6-astra

# Plain terminal mode (no TUI)
gocode --tui=false

# Enable debug logging
gocode -v
```

---

## Session Management

GoCode persists conversations to SQLite and auto-resumes the last session by default. Set `persist = false` under `[session]` to keep a conversation entirely in memory — nothing is written to disk and nothing is resumed on the next run.

```bash
# List saved sessions
/sessions

# Resume a specific session
/sessions <id>
/resume <id>

# Start a fresh session
/new
```

Sessions are stored in `~/.local/share/gocode/sessions/sessions.db` (Linux/macOS) or `%LOCALAPPDATA%\gocode\sessions\sessions.db` (Windows).

---

## Model Context Protocol (MCP)

Extend GoCode with any external MCP server over `stdio` JSON-RPC 2.0:

```bash
# Add an MCP server
gocode mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /path/to/repo

# List configured MCP servers
gocode mcp list

# Remove an MCP server
gocode mcp remove filesystem
```

MCP tools are automatically registered into the agent's tool catalog upon startup.

---

## Project & Global Context

### Project Context (`AGENTS.md`)

When launching in any workspace, GoCode automatically traverses the current directory and all parent folders searching for:

- `AGENTS.md`
- `.gocode/AGENTS.md`
- `docs/AGENTS.md`

If found, its instructions are injected under `## Project Context` in the system prompt.

### Global Context (`CONTEXT.md`)

Create `~/.config/gocode/CONTEXT.md` (or `%APPDATA%\gocode\CONTEXT.md` on Windows) for global rules across all repositories (e.g., coding preferences, language versions).

---

## `.gocodeignore` File Filtering

Create a `.gocodeignore` file in your repository root to prevent GoCode tools from reading, writing, or listing sensitive files:

```gitignore
# .gocodeignore
.env*
secrets/
*.pem
*.key
credentials.json
```

If `.gocodeignore` is absent, GoCode automatically falls back to `.gitignore`.

---

## Supported AI Providers

| Provider | `--provider` | Environment Variable | Current model example | Base URL |
| :-- | :-- | :-- | :-- | :-- |
| **Google Gemini** | `gemini` | `GEMINI_API_KEY` | `gemini-3.8-flash` | `https://generativelanguage.googleapis.com/v1beta/openai` |
| **Anthropic Claude** | `anthropic` | `ANTHROPIC_API_KEY` | `claude-opus-5-5` | `https://api.anthropic.com/v1` |
| **OpenAI** | `openai` | `OPENAI_API_KEY` | `gpt-6-astra` | `https://api.openai.com/v1` |
| **GitHub Copilot** | `copilot` | `GITHUB_COPILOT_TOKEN` | `gpt-4.1` | _dynamic, see below_ |
| **xAI (SpaceXAI)** | `xai` | `XAI_API_KEY` | `grok-code-fast-1` | `https://api.x.ai/v1` |
| **Mistral** | `mistral` | `MISTRAL_API_KEY` | `mistral-large-latest` | `https://api.mistral.ai/v1` |
| **MiniMax** | `minimax` | `MINIMAX_API_KEY` | `MiniMax-M2.5` | `https://api.minimax.io/v1` |
| **DeepSeek** | `deepseek` | `DEEPSEEK_API_KEY` | `deepseek-chat` | `https://api.deepseek.com/v1` |
| **Groq** | `groq` | `GROQ_API_KEY` | _List with `/models`_ | `https://api.groq.com/openai/v1` |
| **OpenRouter** | `openrouter` | `OPENROUTER_API_KEY` | _List with `/models`_ | `https://openrouter.ai/api/v1` |
| **Together** | `together` | `TOGETHER_API_KEY` | `Qwen/Qwen3-Coder-480B-A35B-Instruct` | `https://api.together.xyz/v1` |
| **Fireworks** | `fireworks` | `FIREWORKS_API_KEY` | _List with `/models`_ | `https://api.fireworks.ai/inference/v1` |
| **Cerebras** | `cerebras` | `CEREBRAS_API_KEY` | `qwen-3-coder-480b` | `https://api.cerebras.ai/v1` |
| **Zhipu (GLM)** | `zhipu` | `ZHIPU_API_KEY` | `glm-4.6` | `https://open.bigmodel.cn/api/paas/v4` |
| **NVIDIA NIM** | `nvidia` | `NVIDIA_API_KEY` | `qwen/qwen3-coder-480b-a35b-instruct` | `https://integrate.api.nvidia.com/v1` |
| **Qwen (DashScope)** | `qwen` | `DASHSCOPE_API_KEY` | _List with `/models`_ | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| **Kimi (Moonshot)** | `kimi` | `MOONSHOT_API_KEY` | _List with `/models`_ | `https://api.moonshot.cn/v1` |
| **Hermes Agent** | `hermes` | `HERMES_API_SERVER_KEY` | `hermes-agent` | `http://127.0.0.1:8642/v1` |
| **OmniRoute Proxy** | `omniroute` | `OMNIROUTE_API_KEY` | `auto` | `http://localhost:20128/v1` |
| **Ollama (Local)** | `ollama` | _None_ | _Auto-detected_ | `http://localhost:11434` |

Model catalogs change frequently. Use `/providers` to list the models currently exposed by a provider and `/model <id>` to select one. The examples above are current recommended identifiers, not guarantees of account access; provider quotas, regions, and plan availability still apply.

### GitHub Copilot

Copilot is not a plain bearer-token API, so it has its own provider. It uses two layers: a long-lived GitHub OAuth token is exchanged for a short-lived Copilot JWT, and that exchange also returns the API address to use. The JWT is cached and refreshed shortly before it expires.

Log in once with GitHub's device flow, using a client id from an OAuth App you register at [github.com/settings/developers](https://github.com/settings/developers):

```bash
gocode auth copilot --client-id <your-oauth-app-client-id>
gocode --provider copilot
```

The token is written to `<config-dir>/copilot_token` with owner-only permissions and read automatically; `GITHUB_COPILOT_TOKEN` takes precedence when set. A device flow is required because Copilot has no copy-paste API key — you cannot use a plain PAT here. An active Copilot subscription is required; the exchange returns a clear error when the account has none.

The token exchange endpoint and the `Editor-Version` headers are reverse-engineered from GitHub's own VS Code extension and are **not documented by GitHub**. They can change or be removed without notice, so treat Copilot support as best-effort.

### Connect to a Hermes Agent instance

If you run Hermes Agent, its API server speaks the OpenAI Chat Completions API on `127.0.0.1:8642`, so it can serve as GoCode's backend — and GoCode as Hermes'. Enable it in Hermes first:

```bash
# in ~/.hermes/.env
API_SERVER_ENABLED=true
API_SERVER_KEY=change-me-local-dev
```

Then point GoCode at it:

```bash
export HERMES_API_SERVER_KEY="change-me-local-dev"
gocode --provider hermes
```

The model is `hermes-agent` by default (a Hermes profile advertises its profile name instead). Use `--model` to override.

### Add another API endpoint

Use a named custom provider for a company gateway or any other service that implements the OpenAI Chat Completions API but is not built in:

```bash
gocode provider add my-gateway \
  --base-url https://gateway.internal/v1 \
  --api-key-env MY_GATEWAY_KEY \
  --model my-model

export MY_GATEWAY_KEY="your-key"
gocode --provider my-gateway
gocode provider list
```

Provider configuration stores the environment-variable name, not the key. Custom endpoints can also be managed with `gocode provider remove <name>` and live in `[provider.custom.<name>]` in `config.toml`. To make one the default, set `default = "my-gateway"` in `[provider]`; otherwise select it with `gocode --provider my-gateway`. The endpoint must support OpenAI Chat Completions; use the built-in `anthropic` provider for Anthropic's native API.

A custom provider you added earlier keeps working even if its name has since become a built-in — your saved endpoint wins over the built-in default rather than being silently replaced.

### Connect a llama.cpp server

`llama-server` exposes an OpenAI-compatible API, so connect it as a custom provider. Start the server with the model you want to use:

```bash
llama-server -m /path/to/model.gguf --host 127.0.0.1 --port 8080
```

Register the endpoint with GoCode (no API key is required for a local server):

```bash
gocode provider add llama-cpp \
  --base-url http://127.0.0.1:8080/v1 \
  --model <model-id-from-v1-models>

gocode --provider llama-cpp
```

The model ID must match the identifier returned by `GET http://127.0.0.1:8080/v1/models`; use that endpoint to discover the exact ID. For a server exposed on another machine, use HTTPS or a trusted reverse proxy rather than exposing an unauthenticated HTTP port to the internet.

### Connect OpenCode Zen free models directly

GoCode can call the OpenCode Zen gateway directly as an OpenAI-compatible provider. This avoids an unofficial third-party proxy. Create an API key in the [OpenCode console](https://opencode.ai/auth), then configure the official Zen endpoint:

```bash
gocode provider add opencode-zen \
  --base-url https://opencode.ai/zen/v1 \
  --api-key-env OPENCODE_API_KEY \
  --model deepseek-v4-flash-free

export OPENCODE_API_KEY="your-opencode-api-key"
gocode --provider opencode-zen
gocode --provider opencode-zen --model mimo-v2.5-free
```

Zen currently lists free models including `deepseek-v4-flash-free`, `mimo-v2.5-free`, `nemotron-3-ultra-free`, `north-mini-code-free`, and `big-pickle`. Free-model availability, limits, and IDs can change, so inspect the live catalog before selecting a model:

```bash
curl https://opencode.ai/zen/v1/models \
  -H "Authorization: Bearer $OPENCODE_API_KEY"
```

Use `/providers` in GoCode to list what the configured gateway currently returns. OpenCode Zen is a hosted service, not a local or guaranteed unlimited free tier; use the current [Zen model catalog](https://opencode.ai/docs/zen) for pricing and availability.


---

## In-Session Slash Commands

| Command | Description |
| :-- | :-- |
| `/sessions` | List saved sessions with timestamps and message counts |
| `/sessions <id>` | Switch to and resume an existing session |
| `/new` | Start a fresh session and clear current context |
| `/commit [message]` | Stage and commit modified files with `Assisted-by:` trailer |
| `/providers` | View active provider and list all available models |
| `/provider <name>` | Switch active provider |
| `/model` | Show current active model |
| `/model <name>` | Change model on the fly (validated against the active provider's model list) |
| `/clear` | Clear conversation history while retaining system instructions |
| `/help` | Display help and available commands |
| `Ctrl+L` _(TUI)_ | Open interactive fuzzy model search picker |
| `Ctrl+C` _(TUI)_ | Stop a running turn; press again (or when idle) to quit |
| `Esc` _(TUI)_ | Stop a running turn, or clear the current input |
| _(TUI status bar)_ | Shows the active provider, model, and workspace |
| `exit`, `quit`, `/exit`, `/quit` | Exit the agent session |

---

## Configuration

Configuration is stored at:

- **Linux / macOS**: `~/.config/gocode/config.toml`
- **Windows**: `%APPDATA%\gocode\config.toml`

### Comprehensive `config.toml` Example

```toml
[provider]
default = "gemini"

[provider.ollama]
host = "http://localhost:11434"
default_model = ""

[provider.gemini]
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
api_key_env = "GEMINI_API_KEY"
default_model = "gemini-3.8-flash"

[provider.anthropic]
base_url = "https://api.anthropic.com/v1"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-opus-5-5"

[provider.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-6-astra"

# GitHub Copilot uses two-layer auth: the OAuth token below is exchanged for a
# short-lived JWT at request time. Set it with `gocode auth copilot`, which
# writes to <config-dir>/copilot_token, or export it yourself.
[provider.copilot]
oauth_token_env = "GITHUB_COPILOT_TOKEN"
default_model = "gpt-4.1"
editor_version = "vscode/1.111.0"
editor_plugin_version = "copilot-chat/0.40.0"

# Every other built-in gateway is configured the same way. Only the section you
# want to change needs to appear in your file; the rest come from defaults.
# [provider.xai]
# base_url = "https://api.x.ai/v1"
# api_key_env = "XAI_API_KEY"
# default_model = "grok-code-fast-1"

# [provider.hermes]
# base_url = "http://127.0.0.1:8642/v1"
# api_key_env = "HERMES_API_SERVER_KEY"
# default_model = "hermes-agent"

[permissions]
auto_approve = ["file_read", "code_search"]  # Tools that execute without confirmation
deny = []                                    # Tools that are permanently blocked
sensitive_patterns = ["*.vault", "custom.env"] # Additional patterns to block from AI access

[session]
persist = true                # Set false to keep the conversation out of the SQLite store

[tools]
max_tool_iterations = 50      # Cap provider round-trips per turn
max_repeated_tool_calls = 3   # Cap identical tool calls (same name + arguments) per turn

[tools.shell]
timeout_seconds = 30      # Maximum seconds a shell command may run
max_output_bytes = 1048576 # Cap captured stdout+stderr per command
redact_secrets = true      # Mask credential-shaped values in command output

[mcp.servers.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "."]
```

---

## Logs & Diagnostics

```bash
# Diagnostic health check of all configured providers
gocode doctor

# View the last 50 log lines
gocode logs --tail 50

# Continuously follow live log output
gocode logs --follow
```

---

## Tools & Security Architecture

GoCode operates under a strict **Human-in-the-Loop Security Architecture**. The agent cannot mutate your workspace or run commands without explicit terminal authorization.

| Tool Name | Purpose | Approval Gate |
| :-- | :-- | :-: |
| `file_read` | Inspect file contents and workspace context (with line offset/limit) | Automatic |
| `code_search` | Search files via regex/plain text with context lines & glob filtering | Automatic |
| `file_write` | Create new files or overwrite existing files (with unified diff preview) | **Requires Confirmation** |
| `file_patch` | Perform target string replacements & targeted code edits (with unified diff preview) | **Requires Confirmation** |
| `shell_exec` | Run terminal commands in the workspace working directory (**not confined** — see below) | **Requires Confirmation** |

Tools can be auto-approved or denied via the `[permissions]` section in `config.toml`.

### What is actually enforced

Being precise about the boundary matters more than claiming the widest one, so here is exactly where each control applies.

**The file tools are confined.** `file_read`, `file_write`, `file_patch` and `code_search` resolve every path through the workspace root, reject traversal, and re-verify after resolving symlinks. Protected patterns (`.env*`, SSH keys, certificates, cloud credentials) are unconditionally blocked from inspection by these tools.

**The shell is not confined.** `shell_exec` runs a command string through your shell with the workspace as its *working directory*. A working directory is not a sandbox: a command can read any file your user account can read, anywhere on the machine. GoCode cannot reliably prevent this — command substitution, pipes and interpreters defeat any static inspection of a shell string.

What GoCode does instead, for `shell_exec`:

- **Secret redaction** — credential-shaped values in command output (API keys, tokens, private key blocks, AWS keys, JWTs, `KEY=value` secrets) are masked before the output reaches the model or is written to your session transcript. Disable with `redact_secrets = false`.
- **Advisory warnings** — a command that references a known-sensitive path is flagged in the approval prompt so you can decline it.
- **The approval gate** — the real protection. Keep `shell_exec` out of `auto_approve` and read the command before approving.

If you need a hard boundary, either deny `shell_exec` outright:

```toml
[permissions]
deny = ["shell_exec"]
```

…or treat the approval prompt as the trust boundary it actually is. Do not put `shell_exec` in `auto_approve` on a machine holding credentials.

---

## Architecture

```
gocode/
├── cmd/gocode/           # CLI entry point, flag parsing & subcommands
├── internal/
│   ├── agent/            # Core agent loop, session memory & slash commands
│   ├── config/           # Platform directory management & TOML parser
│   ├── provider/         # Unified provider registry (Ollama, OpenAI-compatible, Anthropic)
│   ├── tools/            # Tool registry, shell execution, patch engine & approval gates
│   ├── session/          # SQLite-backed session persistence
│   ├── mcp/              # Model Context Protocol stdio client
│   ├── ignore/           # .gocodeignore/.gitignore pattern matching
│   └── tui/              # Interactive TUI (Bubble Tea model, styles & approval prompts)
├── .github/workflows/    # GitHub Actions CI/CD
├── .goreleaser.yaml      # GoReleaser release configuration
├── Makefile              # Build, test, and release targets
└── go.mod                # Module definition
```

---

## GoCode vs. Other AI Coding Agents

| Feature | GoCode | Cursor | Aider | GitHub Copilot CLI |
| :-- | :-: | :-: | :-: | :-: |
| **Open Source** | MIT | Proprietary | Apache-2.0 | Proprietary |
| **Language & Runtime** | Native Go Binary | Electron / TS | Python Runtime | Node.js / CLI |
| **Local LLM Support** | Built-in | Limited | Yes | Cloud-only |
| **Cloud Providers** | 9 Gateways | Proprietary | Various APIs | GitHub / OpenAI |
| **Human Approval Control** | Explicit Gate | Semi-auto | Auto/Prompt | Auto |
| **Session Persistence** | SQLite | Yes | No | No |
| **MCP Support** | stdio | Yes | Yes | No |
| **Memory Footprint** | Extremely Low (<20MB) | High | Moderate | Moderate |

---

## FAQ

### What is GoCode used for?

GoCode is an open-source terminal AI coding assistant used for automated code generation, code refactoring, bug fixing, test writing, project directory inspection, and command-line automation.

### Can GoCode run completely offline?

Yes. GoCode connects natively to **Ollama** (`http://localhost:11434`) or a **llama.cpp** server (`http://localhost:8080/v1`). You can run open-weights models such as `llama3.3`, `deepseek-coder`, or `qwen-coder` with zero internet access and complete data privacy.

### How does GoCode compare to Cursor or Aider?

Unlike Cursor (which is an Electron IDE extension) or Aider (which runs on Python), GoCode is a compiled **Go binary** that runs directly in any terminal (Linux, macOS, Windows). It offers sub-millisecond startup, minimal memory consumption, and a human-in-the-loop approval gate for safe command execution.

### Which LLM API providers does GoCode support?

GoCode supports 9 built-in provider gateways plus custom OpenAI-compatible endpoints: Google Gemini, Anthropic Claude, OpenAI, Groq, OpenRouter, Qwen (Aliyun DashScope), Kimi (Moonshot AI), local Ollama, OmniRoute proxies, llama.cpp, and OpenCode Zen.

### Is GoCode free to use?

Yes, GoCode is 100% free and open-source software licensed under the MIT License. When paired with local Ollama models, it is completely free to operate with no subscription or API costs.

### How do I extend GoCode with new tools?

GoCode supports the **Model Context Protocol (MCP)**. You can add any MCP server via `gocode mcp add <name> <command> [args...]` and its tools will be automatically available to the agent.

### Where are sessions stored?

Sessions are stored in a SQLite database at `~/.local/share/gocode/sessions.db` (Linux/macOS) or `%LOCALAPPDATA%\gocode\sessions.db` (Windows). Use `gocode --new` to start fresh or `/sessions` to list and resume past sessions.

---

## Contributing

Contributions are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) for details on our code of conduct and the process for submitting pull requests.

---

## License

GoCode is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.

---

<div align="center">

**Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) • Powered by Go**

Made with ❤️ by [mevarx](https://github.com/mevarx)

</div>
