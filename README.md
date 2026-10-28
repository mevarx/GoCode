<div align="center">

<img src="assets/gocode-banner.png" alt="GoCode: Write, Build, Grow. An open-source AI coding agent for your terminal." width="100%">

# GoCode

**A native Go AI coding agent for your terminal.**

[![CI](https://github.com/mevarx/GoCode/actions/workflows/ci.yml/badge.svg)](https://github.com/mevarx/GoCode/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mevarx/GoCode)](https://goreportcard.com/report/github.com/mevarx/GoCode)
[![Release](https://img.shields.io/github/v/release/mevarx/GoCode)](https://github.com/mevarx/GoCode/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[Quick Start](#quick-start) •
[Features](#features) •
[Security](#security) •
[Configuration](#configuration) •
[Contributing](#contributing)

</div>

<!--
  TODO: add a real terminal demo here (GIF or video, ~20-30 seconds).
  Show: ask GoCode to inspect a project -> it proposes a patch -> approval prompt -> task runs.
  Example: <p align="center"><img src="assets/demo.gif" alt="GoCode demo" width="800"></p>
-->

---

GoCode helps you explore, write, refactor, and debug code from your command line. Connect your preferred AI provider, use a local model, and keep control over file changes and shell commands through explicit approval.

- **Native Go CLI**: a self-contained executable with no separate Python or Node.js runtime required.
- **Your choice of models**: 20 built-in provider gateways, any OpenAI-compatible endpoint, or a local backend such as Ollama or llama.cpp.
- **Controlled execution**: review diffs and approve commands before they run.
- **Persistent sessions**: SQLite-backed history that resumes where you left off.
- **Extensible**: add external tools through the Model Context Protocol (MCP).

---

## Quick Start

### 1. Install

```bash
npm install -g gocode-cli
```

<details>
<summary>Other install options</summary>

```bash
# One-shot, no install
npx gocode-cli
bunx gocode-cli

# Go toolchain
go install github.com/mevarx/GoCode/cmd/gocode@latest

# From source
git clone https://github.com/mevarx/GoCode.git
cd GoCode
go build -o gocode ./cmd/gocode/
```

Pre-built binaries are available on the [Releases page](https://github.com/mevarx/GoCode/releases).

Building from source requires Go 1.26.8 or newer (see `go.mod`).

</details>

### 2. Connect a provider

**Cloud provider** (example: Gemini):

```bash
export GEMINI_API_KEY="your-key"
gocode --provider gemini
```

**Local model** (example: Ollama):

```bash
ollama serve                 # in another terminal, with a model already pulled
gocode --provider ollama
```

### 3. Start working

```bash
gocode
```

Then ask for something concrete, such as *"Find where config is loaded and explain the precedence order."* GoCode asks for confirmation before it writes files or runs commands.

### Common commands

| Command | What it does |
| :-- | :-- |
| `gocode` | Launch and resume your last session |
| `gocode --new` | Start a fresh session |
| `gocode --provider <name> --model <id>` | Pick a provider and model for this run |
| `gocode --tui=false` | Plain terminal mode, no TUI |
| `gocode doctor` | Health-check configured providers |
| `gocode logs --follow` | Follow live logs |
| `/provider`, `/model` | Switch provider or model in-session |
| `/sessions`, `/new`, `/clear` | List, start, or reset sessions |
| `/commit [message]` | Commit modified files with an `Assisted-by:` trailer |
| `/help` | Show all in-session commands |

TUI keys: `Ctrl+L` opens the model picker, `Esc` stops a running turn or clears input, and `Ctrl+C` stops a turn (press again to quit).

---

## Features

### File and shell tools

| Tool | Purpose | Approval |
| :-- | :-- | :-: |
| `file_read` | Read files, with line offset and limit | Automatic |
| `code_search` | Regex or plain-text search with context and glob filters | Automatic |
| `file_write` | Create or overwrite files, with a unified diff preview | **Required** |
| `file_patch` | Targeted edits, with a unified diff preview | **Required** |
| `shell_exec` | Run a terminal command in the workspace directory | **Required** |

Change which tools need approval with the `[permissions]` section of `config.toml`. See [Security](#security) before auto-approving anything.

### Providers

Switch providers and models at any time with `/provider` and `/model`. GoCode ships with gateways for:

Ollama, OpenAI, Anthropic Claude, Google Gemini, GitHub Copilot, xAI, Mistral, MiniMax, DeepSeek, Groq, OpenRouter, Together, Fireworks, Cerebras, Zhipu, NVIDIA NIM, Qwen, Kimi, Hermes Agent, and OmniRoute.

| Provider | `--provider` | API key variable |
| :-- | :-- | :-- |
| Google Gemini | `gemini` | `GEMINI_API_KEY` |
| Anthropic Claude | `anthropic` | `ANTHROPIC_API_KEY` |
| OpenAI | `openai` | `OPENAI_API_KEY` |
| GitHub Copilot | `copilot` | `GITHUB_COPILOT_TOKEN` (or `gocode auth copilot`) |
| xAI | `xai` | `XAI_API_KEY` |
| Mistral | `mistral` | `MISTRAL_API_KEY` |
| MiniMax | `minimax` | `MINIMAX_API_KEY` |
| DeepSeek | `deepseek` | `DEEPSEEK_API_KEY` |
| Groq | `groq` | `GROQ_API_KEY` |
| OpenRouter | `openrouter` | `OPENROUTER_API_KEY` |
| Together | `together` | `TOGETHER_API_KEY` |
| Fireworks | `fireworks` | `FIREWORKS_API_KEY` |
| Cerebras | `cerebras` | `CEREBRAS_API_KEY` |
| Zhipu (GLM) | `zhipu` | `ZHIPU_API_KEY` |
| NVIDIA NIM | `nvidia` | `NVIDIA_API_KEY` |
| Qwen (DashScope) | `qwen` | `DASHSCOPE_API_KEY` |
| Kimi (Moonshot) | `kimi` | `MOONSHOT_API_KEY` |
| Hermes Agent | `hermes` | `HERMES_API_SERVER_KEY` |
| OmniRoute | `omniroute` | `OMNIROUTE_API_KEY` |
| Ollama (local) | `ollama` | none |

Model catalogs change often. Use `/providers` to list the models a provider currently exposes and `/model <id>` to select one. Availability still depends on your account, region, and plan.

**Add any OpenAI-compatible endpoint** (company gateways, llama.cpp, OpenCode Zen, and others):

```bash
gocode provider add my-gateway \
  --base-url https://gateway.internal/v1 \
  --api-key-env MY_GATEWAY_KEY \
  --model my-model

export MY_GATEWAY_KEY="your-key"
gocode --provider my-gateway
```

Only the environment-variable *name* is stored, never the key. Manage endpoints with `gocode provider list` and `gocode provider remove <name>`. Setup guides for Copilot, Hermes, llama.cpp, and OpenCode Zen are in [docs/providers.md](docs/providers.md).

### Local models

GoCode supports offline workflows when a local model and its backend (Ollama or a llama.cpp `llama-server`) are installed, running, and configured. Nothing is sent to a cloud provider unless you select one.

### Sessions

Conversations are saved to SQLite and the last session resumes automatically.

```bash
/sessions            # list saved sessions
/sessions <id>       # switch to a session (alias: /resume <id>)
/new                 # start fresh
```

Sessions live at `~/.local/share/gocode/sessions/sessions.db` on Linux and macOS, and `%LOCALAPPDATA%\gocode\sessions\sessions.db` on Windows. Set `persist = false` under `[session]` to keep a conversation in memory only, with nothing written to disk.

### Project and global context

GoCode searches the current directory and its parents for `AGENTS.md`, `.gocode/AGENTS.md`, or `docs/AGENTS.md` and injects it into the system prompt under `## Project Context`.

For rules that apply to every repository, create `~/.config/gocode/CONTEXT.md` (`%APPDATA%\gocode\CONTEXT.md` on Windows).

### Ignore rules: `.gocodeignore`

Keep sensitive files away from GoCode's file tools by adding a `.gocodeignore` to your repository root:

```gitignore
.env*
secrets/
*.pem
*.key
credentials.json
```

If `.gocodeignore` is missing, GoCode falls back to `.gitignore`. On case-insensitive filesystems (Windows and macOS), patterns match regardless of letter case, so `.ENV` is treated the same as `.env`. On Linux, matching stays case-sensitive.

### MCP servers

Extend GoCode with any MCP server over `stdio`:

```bash
gocode mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /path/to/repo
gocode mcp list
gocode mcp remove filesystem
```

MCP tools are registered into the agent's tool catalog on startup. Everything after the server name belongs to the server, so put GoCode's own flags first:

```bash
gocode --config /path/to/config.toml mcp add filesystem npx -y @scope/server .
```

To pass `--config` to the server instead, separate the sections with `--`:

```bash
gocode mcp add filesystem npx -y @scope/server . -- --config /path/to/config.toml
```

---

## Security

GoCode is built around human-in-the-loop approval: the agent cannot write files or run commands without your confirmation unless you explicitly auto-approve a tool.

**File tools are confined.** `file_read`, `file_write`, `file_patch`, and `code_search` resolve every path through the workspace root, reject traversal, and re-check after resolving symlinks. Sensitive files (`.env*`, SSH keys, certificates, cloud credentials) and anything matched by your ignore rules are blocked.

**The shell is not confined.** `shell_exec` uses the workspace as its *working directory*, which is not a sandbox. A command can read anything your user account can read. Static inspection cannot reliably prevent this, since command substitution, pipes, and interpreters defeat it. GoCode instead provides:

- **Secret redaction**: credential-shaped values in command output are masked before reaching the model or your session transcript (`redact_secrets = false` disables it).
- **Advisory warnings**: commands referencing known-sensitive paths are flagged in the approval prompt.
- **The approval gate**: your real protection. Read each command before approving it.

**Recommendations**

- Keep `shell_exec` out of `auto_approve`, especially on machines that hold credentials.
- For a hard boundary, deny the shell entirely:

  ```toml
  [permissions]
  deny = ["shell_exec"]
  ```

- Run GoCode in a container or VM if you need real isolation.

Details and the full threat model: [docs/SECURITY.md](docs/SECURITY.md).

---

## Configuration

Config file location:

- **Linux / macOS**: `~/.config/gocode/config.toml`
- **Windows**: `%APPDATA%\gocode\config.toml`

A minimal example:

```toml
[provider]
default = "gemini"

[provider.gemini]
api_key_env = "GEMINI_API_KEY"
default_model = "gemini-3.8-flash"

[permissions]
auto_approve = ["file_read", "code_search"]  # run without confirmation
deny = []                                    # permanently blocked
sensitive_patterns = ["*.vault"]             # extra patterns hidden from the AI

[session]
persist = true

[tools]
max_tool_iterations = 50      # provider round-trips per turn
max_repeated_tool_calls = 3   # identical tool calls per turn

[tools.shell]
timeout_seconds = 30
max_output_bytes = 1048576
redact_secrets = true
```

Only the sections you change need to appear; everything else comes from defaults. The full reference, including MCP servers, custom providers, and Copilot settings, is in [docs/configuration.md](docs/configuration.md).

---

## Platform support

Release builds are configured with GoReleaser for Linux, macOS, and Windows on AMD64 and ARM64. See the [CI workflow](https://github.com/mevarx/GoCode/actions) for current build and test status.

---

## Project layout

```
cmd/gocode/        CLI entry point, flags, subcommands
internal/agent/    agent loop, session memory, slash commands
internal/config/   platform directories, TOML parsing
internal/provider/ provider registry (Ollama, OpenAI-compatible, Anthropic)
internal/tools/    tool registry, shell execution, patch engine, approval gates
internal/session/  SQLite session persistence
internal/mcp/      MCP stdio client
internal/ignore/   .gocodeignore / .gitignore matching
internal/tui/      Bubble Tea interface
```

A deeper walkthrough is in [docs/architecture.md](docs/architecture.md).

---

## FAQ

**Can I use GoCode without a cloud provider?**
Yes. Configure Ollama or a llama.cpp server and select it with `--provider`. See [Local models](#local-models).

**Is it free?**
GoCode is MIT-licensed and free. Cloud providers bill you under their own terms; local models have no API cost.

**How do I add tools?**
Through MCP: `gocode mcp add <name> <command> [args...]`. See [MCP servers](#mcp-servers).

**Where are my sessions?**
See [Sessions](#sessions). Use `gocode --new` or `/new` to start fresh.

---

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) for the code of conduct and pull request process. Release history is in [CHANGES.md](CHANGES.md).

```bash
go build ./...
go vet ./...
go test ./...
```

## License

MIT. See [LICENSE](LICENSE).

---

<div align="center">

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) v2 • Made by [mevarx](https://github.com/mevarx)

</div>
