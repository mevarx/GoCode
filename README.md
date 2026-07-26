<div align="center">

# GoCode

**A Go-native, local-first, provider-agnostic AI terminal coding agent.**

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=flat-square)](https://github.com/mevarx/GoCode)

[Features](#features) •
[Quick Start](#quick-start) •
[Running Providers](#running-providers) •
[Configuration](#configuration) •
[Tools & Security](#tools--security) •
[Architecture](#architecture)

---

</div>

## Overview

**GoCode** is a lightweight, high-performance terminal AI coding agent written in Go. It operates directly inside your terminal, assisting you with code generation, debugging, refactoring, file inspection, and command execution.

Unlike cloud-locked AI tools, GoCode puts privacy, security, and developer control first. It gives you the flexibility to run **local Ollama models** directly, connect to **cloud API providers** (OpenAI, Gemini, Groq, Anthropic, OpenRouter, Qwen, Kimi), or route through **gateway proxies** like OmniRoute.

```
GoCode — Terminal Coding Agent
Provider: gemini | Model: gemini-2.5-flash
Tools: shell_exec, file_read, file_write, file_patch
Type your message (or 'exit' to quit)
──────────────────────────────────────────────────

> Explain main.go and list all open issues
```

---

## Features

- **9 Provider Support** — Seamlessly choose between **Ollama**, **OmniRoute**, **OpenAI**, **Gemini**, **Groq**, **OpenRouter**, **Anthropic**, **Qwen**, and **Kimi**.
- **Local-First & Private** — Runs with local LLMs out of the box (`codellama`, `llama3`, `mistral`, `deepseek-coder`, `gemma`, etc.).
- **Cloud-Ready** — Connect to any OpenAI-compatible API or native Anthropic Messages API with a single environment variable.
- **Model Agnostic** — Automatically detects and runs any model pulled in Ollama or exposed via gateway endpoints.
- **On-the-Fly Switching** — Switch providers or models instantly inside an active session using `/provider` and `/model`.
- **Human-in-the-Loop Security** — All file writes and shell execution commands require your explicit approval before execution.
- **Single Binary, Zero Overhead** — Native Go executable with no Node.js runtime and minimal startup latency.
- **Diagnostic Doctor Subcommand** — Run `gocode doctor` to instantly check provider health, API key status, and reachability across all configured providers.

---

## Quick Start

### Prerequisites

- **Go 1.22+** installed on your system.
- **At least one provider** configured (see [Running Providers](#running-providers)).

### Installation

#### Option 1: Install via `go install`

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

# Run GoCode
./gocode
```

---

## Running Providers

GoCode supports 9 providers out of the box. Set the `--provider` flag or update `config.toml` to choose your preferred backend.

### Local Providers

#### Ollama (Default)

GoCode connects to your local Ollama instance (`http://localhost:11434`) by default:

```bash
# Launch with Ollama (auto-detects installed models)
gocode

# Launch with a specific Ollama model
gocode --provider ollama --model llama3.1:8b
```

#### OmniRoute Gateway Proxy

GoCode connects to your local OmniRoute instance (`http://localhost:20128/v1`):

```bash
# Launch with OmniRoute provider
gocode --provider omniroute

# Specify OmniRoute model routing
gocode --provider omniroute --model auto/coding
```

### Cloud Providers

All cloud providers read API keys from environment variables. Set the appropriate variable before launching GoCode.

| Provider | Env Variable | Default Model | Base URL |
| :--- | :--- | :--- | :--- |
| **OpenAI** | `OPENAI_API_KEY` | `gpt-4o` | `https://api.openai.com/v1` |
| **Gemini** | `GEMINI_API_KEY` | `gemini-2.5-flash` | `https://generativelanguage.googleapis.com/v1beta/openai` |
| **Groq** | `GROQ_API_KEY` | `llama-3.3-70b-versatile` | `https://api.groq.com/openai/v1` |
| **OpenRouter** | `OPENROUTER_API_KEY` | `anthropic/claude-sonnet-4.5` | `https://openrouter.ai/api/v1` |
| **Anthropic** | `ANTHROPIC_API_KEY` | `claude-sonnet-4-20250514` | `https://api.anthropic.com/v1` |
| **Qwen** | `DASHSCOPE_API_KEY` | `qwen-max` | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| **Kimi** | `MOONSHOT_API_KEY` | `moonshot-v1-8k` | `https://api.moonshot.cn/v1` |

```bash
# Example: Launch with Gemini
export GEMINI_API_KEY="your-api-key-here"
gocode --provider gemini

# Example: Launch with OpenAI using a specific model
export OPENAI_API_KEY="sk-..."
gocode --provider openai --model gpt-4o-mini

# Example: Launch with Groq
export GROQ_API_KEY="gsk_..."
gocode --provider groq

# Example: Launch with Anthropic
export ANTHROPIC_API_KEY="sk-ant-..."
gocode --provider anthropic --model claude-sonnet-4-20250514

# Example: Launch with OpenRouter
export OPENROUTER_API_KEY="sk-or-..."
gocode --provider openrouter

# Example: Launch with Qwen
export DASHSCOPE_API_KEY="sk-..."
gocode --provider qwen

# Example: Launch with Kimi
export MOONSHOT_API_KEY="sk-..."
gocode --provider kimi
```

### Switching Mid-Session

You can switch between any provider at any time inside an active terminal session:

```text
> /provider gemini
[Provider switched to gemini]

> /provider openai
[Provider switched to openai]

> /provider anthropic
[Provider switched to anthropic]

> /model gpt-4o-mini
[Model set to gpt-4o-mini]
```

### Health Check (`gocode doctor`)

Verify connectivity, API key status, and reachability of all providers simultaneously:

```bash
gocode doctor
```

```
GoCode Doctor — Diagnostic Health Check
──────────────────────────────────────────
✓ ollama         reachable at http://localhost:11434 (3 models available)
✗ omniroute      unreachable at http://localhost:20128/v1
⚠ openai         no API key (set OPENAI_API_KEY env var or api_key in config)
✓ gemini         reachable at https://generativelanguage.googleapis.com/v1beta/openai (default: gemini-2.5-flash, 12 models)
✓ groq           reachable at https://api.groq.com/openai/v1 (default: llama-3.3-70b-versatile, 8 models)
⚠ openrouter     no API key (set OPENROUTER_API_KEY env var or api_key in config)
✓ anthropic      configured (default: claude-sonnet-4-20250514, 5 models known)
⚠ qwen           no API key (set DASHSCOPE_API_KEY env var or api_key in config)
⚠ kimi           no API key (set MOONSHOT_API_KEY env var or api_key in config)
──────────────────────────────────────────
```

---

## Interactive Slash Commands

During an active session, use built-in slash commands:

| Command | Action |
| :--- | :--- |
| `exit` / `quit` | Exit the GoCode agent session |
| `/clear` | Reset conversation history while keeping system instructions intact |
| `/providers` | List all registered providers, active provider, and available models |
| `/provider <name>` | Switch active provider (e.g. `/provider gemini`, `/provider anthropic`) |
| `/model <name>` | Switch active model (e.g. `/model gpt-4o-mini`, `/model gemini-2.5-flash`) |

---

## Tools & Security

GoCode includes built-in tools that enable the AI model to interact with your local environment safely.

| Tool Name | Capability | Requires Approval |
| :--- | :--- | :---: |
| `file_read` | Read contents of workspace files | Automatic |
| `file_write` | Create or overwrite files | **Yes** |
| `file_patch` | Perform target string replacements / code edits | **Yes** |
| `shell_exec` | Execute terminal commands (builds, tests, git) | **Yes** |

**Approval Gate**: Whenever GoCode attempts to modify a file or execute a shell command, you will be prompted in the terminal to inspect the proposed action and approve (`y`) or reject (`n`).

---

## Configuration

GoCode automatically loads its configuration from standard platform paths:

- **Linux / macOS**: `~/.config/gocode/config.toml`
- **Windows**: `%APPDATA%\gocode\config.toml`

### Example `config.toml`

```toml
[provider]
default = "gemini"  # ollama | omniroute | openai | gemini | groq | openrouter | anthropic | qwen | kimi

[provider.ollama]
host = "http://localhost:11434"
default_model = ""  # Auto-detects any pulled local Ollama model

[provider.omniroute]
base_url = "http://localhost:20128/v1"
default_model = "auto"

[provider.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
# api_key = "sk-..."  # Or set the key directly (not recommended)
default_model = "gpt-4o"

[provider.gemini]
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
api_key_env = "GEMINI_API_KEY"
default_model = "gemini-2.5-flash"

[provider.groq]
base_url = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
default_model = "llama-3.3-70b-versatile"

[provider.openrouter]
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
default_model = "anthropic/claude-sonnet-4.5"

[provider.anthropic]
base_url = "https://api.anthropic.com/v1"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-20250514"

[provider.qwen]
base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key_env = "DASHSCOPE_API_KEY"
default_model = "qwen-max"

[provider.kimi]
base_url = "https://api.moonshot.cn/v1"
api_key_env = "MOONSHOT_API_KEY"
default_model = "moonshot-v1-8k"

[approval]
auto_approve_reads = true
auto_approve_writes = false
auto_approve_shell = false
```

---

## Architecture

GoCode is built with clean architecture and low package coupling:

```
gocode/
├── cmd/
│   └── gocode/           # CLI entry point, flag parsing, doctor command & wiring
├── internal/
│   ├── agent/            # Core agent loop, session memory & slash command router
│   ├── config/           # XDG directory management & TOML configuration
│   ├── provider/         # Provider registry, Ollama, GatewayProxy (OpenAI-compatible), Anthropic (native)
│   └── tools/            # Tool registry, execution safety & approval gates
├── go.mod                # Module dependencies
└── README.md             # Documentation
```

---

## License

This project is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.
