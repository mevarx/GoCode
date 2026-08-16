<div align="center">

# GoCode — Open-Source AI Terminal Coding Agent

**A high-performance, Go-native, local-first AI coding agent for your command line.**

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=flat-square)](https://github.com/mevarx/GoCode)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=flat-square)](CONTRIBUTING.md)

[Overview](#overview) •
[Features](#features) •
[Quick Start](#quick-start) •
[Supported Providers](#supported-ai-providers) •
[Comparison](#gocode-vs-other-ai-coding-agents) •
[Configuration](#configuration-guide) •
[FAQ](#frequently-asked-questions-faq)

---

</div>

## Overview

> **What is GoCode?**
> **GoCode** is a lightweight, open-source AI terminal coding agent written in Go. It turns your command line into an interactive pair programming environment capable of reading project files, editing code via structured patches, executing shell commands, and debugging errors in real time—all with strict human-in-the-loop approval.

Unlike heavy Python-based or Node.js-based AI CLI tools, GoCode compiles into a **single, zero-dependency binary** with instant startup latency and minimal memory footprint.

GoCode is **provider-agnostic** and **local-first**: run completely offline with local LLMs via **Ollama**, or connect seamlessly to leading cloud AI models from **Google Gemini**, **Anthropic Claude**, **OpenAI**, **Groq**, **OpenRouter**, **Qwen (DashScope)**, **Kimi (Moonshot)**, and **OmniRoute** gateway proxies.

```text
GoCode — Terminal Coding Agent
Provider: gemini | Model: gemini-2.5-flash
Tools: shell_exec, file_read, file_write, file_patch
Type your message (or 'exit' to quit)
───────────────────────────────────�## Features

- **Single Native Go Binary** — Fast startup, low resource usage, zero Python or Node.js dependencies.
- **Local-First & Privacy-Focused** — Runs 100% offline with local models via Ollama (`codellama`, `llama3.3`, `deepseek-coder`, `qwen2.5-coder`, etc.).
- **SQLite Session Persistence** — Automatically saves and resumes sessions across terminal restarts with zero configuration via pure Go SQLite (`modernc.org/sqlite`).
- **Project Context Auto-Loading (`AGENTS.md`)** — Scans CWD and parent hierarchies for `AGENTS.md`, `.gocode/AGENTS.md`, or `docs/AGENTS.md` and prepends context to the agent prompt.
- **Global Context Support (`CONTEXT.md`)** — Reads user-wide conventions from `~/.config/gocode/CONTEXT.md` (or `%APPDATA%\gocode\CONTEXT.md`).
- **`.gocodeignore` Security Filtering** — Uses `go-git` ignore engine to protect sensitive files and directories before any tool can read, write, or patch them.
- **Granular Tool Permissions** — Configure `auto_approve` and `deny` lists in `config.toml` for hands-free automation or locked-down security.
- **Model Context Protocol (MCP) Stdio Client** — Seamlessly connect standard MCP servers (`gocode mcp add/list/remove`) and merge their tools into the agent.
- **Interactive TUI Model Picker (`Ctrl+L`)** — Fuzzy-search and switch across all available models from all registered providers instantly.
- **Git Attribution & Session Tracking** — Tracks modified files during coding sessions and formats commit trailers (`Assisted-by: GoCode:<model>`).
- **Diagnostics & Streaming Logs** — Built-in `gocode doctor` health checks and `gocode logs --tail N --follow` file log streaming.
- **9 Multi-Provider Gateways** — Connect to **Ollama**, **OpenAI**, **Google Gemini**, **Anthropic Claude**, **Groq**, **OpenRouter**, **Qwen**, **Kimi**, and **OmniRoute**.

---

## Quick Start

### Prerequisites

- **Go 1.22+** installed (if building from source).
- An active AI provider: **Ollama** for local execution, or an API key for cloud providers (**OpenAI**, **Gemini**, **Claude**, **Groq**, **OpenRouter**, **Qwen**, **Kimi**).

### Installation

#### Option 1: Install via `go install` (Recommended)

```bash
go install github.com/mevarx/GoCode/cmd/gocode@latest
```

#### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/mevarx/GoCode.git
cd gocode

# Build executable
go build -o gocode ./cmd/gocode/

# Run GoCode (auto-resumes last session by default)
./gocode
```

---

## Supported AI Providers

GoCode supports **9 AI model providers and gateway proxies** out of the box. Select your provider using the `--provider` flag or by setting default preferences in `config.toml`.

### Cloud Providers Cheat Sheet

| Provider | `--provider` | Environment Variable | Default Model | Base URL |
| :--- | :--- | :--- | :--- | :--- |
| **Google Gemini** | `gemini` | `GEMINI_API_KEY` | `gemini-2.5-flash` | `https://generativelanguage.googleapis.com/v1beta/openai` |
| **Anthropic Claude** | `anthropic` | `ANTHROPIC_API_KEY` | `claude-sonnet-4-20250514` | `https://api.anthropic.com/v1` |
| **OpenAI** | `openai` | `OPENAI_API_KEY` | `gpt-4o` | `https://api.openai.com/v1` |
| **Groq** | `groq` | `GROQ_API_KEY` | `llama-3.3-70b-versatile` | `https://api.groq.com/openai/v1` |
| **OpenRouter** | `openrouter` | `OPENROUTER_API_KEY` | `anthropic/claude-sonnet-4.5` | `https://openrouter.ai/api/v1` |
| **Qwen (DashScope)** | `qwen` | `DASHSCOPE_API_KEY` | `qwen-max` | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| **Kimi (Moonshot)** | `kimi` | `MOONSHOT_API_KEY` | `moonshot-v1-8k` | `https://api.moonshot.cn/v1` |
| **OmniRoute Proxy** | `omniroute` | `OMNIROUTE_API_KEY` | `auto` | `http://localhost:20128/v1` |
| **Ollama (Local)** | `ollama` | *None (Local Server)* | *Auto-detected* | `http://localhost:11434` |

### CLI Flags & Session Management

```bash
# 1. Start a fresh session (ignoring previous session)
gocode --new

# 2. Resume a specific session by ID
gocode --session sess_20260815190405_a1b2c3d4

# 3. Launch plain non-TUI terminal mode
gocode --tui=false

# 4. Enable verbose debug logging
gocode -v
```

### In-Session Terminal Slash Commands

Control GoCode dynamically without restarting your session:

| Slash Command | Description |
| :--- | :--- |
| `/sessions` | List saved sessions with timestamps, message counts, and active markers |
| `/sessions <id>` or `/resume <id>` | Switch to and resume an existing session history |
| `/new` | Start a fresh session and clear current context |
| `/commit [message]` | Stage and commit session modified files with `Assisted-by: GoCode:<model>` trailer |
| `/providers` | View active provider and list all available model endpoints |
| `/provider <name>` | Switch active provider (e.g. `/provider gemini` or `/provider ollama`) |
| `/model` | Show current active model |
| `/model <name>` | Change model on the fly (e.g. `/model gpt-4o-mini`) |
| `Ctrl+L` *(TUI)* | Open interactive fuzzy model search picker |
| `/clear` | Clear conversation history while retaining system instructions |
| `/help` | Display help and available commands |
| `exit` or `quit` | Exit the agent session |

---

## Model Context Protocol (MCP) Support

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

## Project Context (`AGENTS.md`) & Global Context (`CONTEXT.md`)

- **Project Context**: When launching in any workspace, GoCode automatically traverses the current directory and all parent folders searching for `AGENTS.md`, `.gocode/AGENTS.md`, or `docs/AGENTS.md`. If found, its instructions are injected under `## Project Context`.
- **Global Context**: Create `~/.config/gocode/CONTEXT.md` (or `%APPDATA%\gocode\CONTEXT.md` on Windows) for global rules across all repositories (e.g., coding preferences, language versions).

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

## Configuration Guide

Configuration is stored at:
- **Linux / macOS**: `~/.config/gocode/config.toml`
- **Windows**: `%APPDATA%\gocode\config.toml`

### Comprehensive `config.toml` Example

```toml
[provider]
default = "gemini"  # ollama | omniroute | openai | gemini | groq | openrouter | anthropic | qwen | kimi

[provider.ollama]
host = "http://localhost:11434"
default_model = ""  # Auto-detects installed models

[provider.gemini]
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
api_key_env = "GEMINI_API_KEY"
default_model = "gemini-2.5-flash"

[provider.anthropic]
base_url = "https://api.anthropic.com/v1"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-20250514"

[provider.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-4o"

[provider.groq]
base_url = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
default_model = "llama-3.3-70b-versatile"

[provider.openrouter]
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
default_model = "anthropic/claude-sonnet-4.5"

[provider.qwen]
base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key_env = "DASHSCOPE_API_KEY"
default_model = "qwen-max"

[provider.kimi]
base_url = "https://api.moonshot.cn/v1"
api_key_env = "MOONSHOT_API_KEY"
default_model = "moonshot-v1-8k"

[provider.omniroute]
base_url = "http://localhost:20128/v1"
default_model = "auto"

[permissions]
auto_approve = ["file_read"]  # Tools that execute without interactive confirmation
deny = []                     # Tools that are permanently blocked

[tools.shell]
timeout_seconds = 30          # Shell execution timeout in seconds

[mcp.servers.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "."]
```ites = false
auto_approve_shell = false

[tools.shell]
timeout_seconds = 30  # Maximum seconds a shell command may run before being killed
```

---

## Frequently Asked Questions (FAQ)

### What is GoCode used for?
GoCode is an open-source terminal AI coding assistant used for automated code generation, code refactoring, bug fixing, test writing, project directory inspection, and command-line automation.

### Can GoCode run completely offline?
Yes. GoCode connects natively to **Ollama** running locally on your machine (`http://localhost:11434`). You can run open-weights models like `codellama`, `llama3.3`, `deepseek-coder`, or `qwen2.5-coder` with zero internet access and complete data privacy.

### How does GoCode compare to Cursor or Aider?
Unlike Cursor (which is an Electron IDE extension) or Aider (which runs on Python), GoCode is a compiled **Go binary** that runs directly in any terminal (Linux, macOS, Windows). It offers sub-millisecond startup, minimal memory consumption, and a human-in-the-loop approval gate for safe command execution.

### Which LLM API providers does GoCode support?
GoCode supports 9 major provider gateways: Google Gemini, Anthropic Claude, OpenAI, Groq, OpenRouter, Qwen (Aliyun DashScope), Kimi (Moonshot AI), local Ollama servers, and OmniRoute proxies.

### Is GoCode free to use?
Yes, GoCode is 100% free and open-source software licensed under the MIT License. When paired with local Ollama models, it is completely free to operate with no subscription or API costs.

---

## Architecture & Codebase Structure

```text
gocode/
├── .github/              # GitHub Actions workflows & PR/issue templates
├── cmd/
│   └── gocode/           # CLI entry point, flag parsing & doctor subcommand
├── internal/
│   ├── agent/            # Core agent loop, session memory & slash command router
│   ├── config/           # Platform directory management & TOML configuration parser
│   ├── provider/         # Unified provider registry (Ollama, OpenAI-compatible, Anthropic native)
│   ├── tools/            # Tool registry, shell execution, patch engine & approval gates
│   └── tui/              # Interactive TUI (Bubble Tea model, styles & approval prompts)
├── .goreleaser.yaml      # GoReleaser release configuration
├── CONTRIBUTING.md       # Contribution guide
├── LICENSE               # MIT License
├── Makefile              # Build, test, and release targets
├── README.md             # Documentation
├── go.mod                # Module definition
└── go.sum                # Module checksums
```

---

## License

GoCode is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.
