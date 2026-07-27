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
──────────────────────────────────────────────────

> Refactor main.go to use context timeouts and add unit tests
```

---

## Features

- **Single Native Go Binary** — Fast startup, low resource usage, zero Python or Node.js dependencies.
- **Local-First & Privacy-Focused** — Runs 100% offline with local models via Ollama (`codellama`, `llama3.3`, `deepseek-coder`, `qwen2.5-coder`, etc.).
- **9 Multi-Provider Gateways** — Connect to **Ollama**, **OpenAI**, **Google Gemini**, **Anthropic Claude**, **Groq**, **OpenRouter**, **Qwen**, **Kimi**, and **OmniRoute**.
- **Human-in-the-Loop Approval Gate** — Safety-first architecture requiring explicit confirmation before executing terminal commands or modifying files.
- **On-the-Fly Switching** — Switch providers or models dynamically inside an active terminal session using `/provider` and `/model` commands.
- **Built-in Diagnostic Doctor** — Instantly check API key configurations, network reachability, and model availability with `gocode doctor`.
- **Autonomous Tool Execution** — Equipped with `file_read`, `file_write`, `file_patch`, and `shell_exec` tools for full-lifecycle coding assistance.

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

# Run GoCode
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

### Usage Examples

```bash
# 1. Run local LLM with Ollama (Default)
gocode --provider ollama --model deepseek-coder

# 2. Run with Google Gemini API
export GEMINI_API_KEY="your-gemini-api-key"
gocode --provider gemini --model gemini-2.5-flash

# 3. Run with Anthropic Claude API
export ANTHROPIC_API_KEY="sk-ant-api..."
gocode --provider anthropic --model claude-3-5-sonnet-20241022

# 4. Run with OpenAI API
export OPENAI_API_KEY="sk-..."
gocode --provider openai --model gpt-4o

# 5. Run with Groq ultra-fast inference
export GROQ_API_KEY="gsk_..."
gocode --provider groq --model llama-3.3-70b-versatile

# 6. Run with OpenRouter gateway
export OPENROUTER_API_KEY="sk-or-..."
gocode --provider openrouter

# 7. Run with Qwen (Aliyun DashScope)
export DASHSCOPE_API_KEY="sk-..."
gocode --provider qwen

# 8. Run with Kimi (Moonshot AI)
export MOONSHOT_API_KEY="sk-..."
gocode --provider kimi
```

### In-Session Terminal Slash Commands

Control GoCode dynamically without restarting your session:

- `/providers` — View active provider and list all available model endpoints.
- `/provider <name>` — Switch active provider (e.g. `/provider gemini` or `/provider ollama`).
- `/model <name>` — Change model on the fly (e.g. `/model gpt-4o-mini`).
- `/clear` — Clear conversation history while retaining system instructions.
- `exit` or `quit` — Exit the agent session.

### Provider Diagnostic Check (`gocode doctor`)

Run `gocode doctor` to inspect network reachability, API key validation, and model discovery for all 9 providers:

```bash
gocode doctor
```

---

## GoCode vs. Other AI Coding Agents

| Feature | GoCode | Cursor | Aider | GitHub Copilot CLI |
| :--- | :---: | :---: | :---: | :---: |
| **Open Source** | MIT (Open Source) | Proprietary | Apache-2.0 | Proprietary |
| **Language & Runtime** | Native Go Binary | Electron / TS | Python Runtime | Node.js / CLI |
| **Local LLM Support (Ollama)** | Built-in | Limited | Yes | Cloud-only |
| **Cloud Providers Supported** | 9 Gateways | Proprietary | Various APIs | GitHub / OpenAI |
| **Human Approval Control** | Explicit Gate | Semi-auto | Auto/Prompt | Auto |
| **Memory Footprint** | Extremely Low (<20MB) | High (Electron) | Moderate (Python) | Moderate (Node) |

---

## Tools & Security Architecture

GoCode operates under a strict **Human-in-the-Loop Security Architecture**. The agent cannot mutate your workspace or run commands without explicit terminal authorization.

| Tool Name | Purpose & Function | Approval Gate |
| :--- | :--- | :---: |
| `file_read` | Inspect file contents and workspace context | Automatic |
| `file_write` | Create new files or overwrite existing files | **Requires Confirmation (y/n)** |
| `file_patch` | Perform target string replacements & targeted code edits | **Requires Confirmation (y/n)** |
| `shell_exec` | Run terminal commands (builds, tests, git operations) | **Requires Confirmation (y/n)** |

---

## Configuration Guide

GoCode reads configuration options from standard platform paths:

- **Linux / macOS**: `~/.config/gocode/config.toml`
- **Windows**: `%APPDATA%\gocode\config.toml`

### Comprehensive `config.toml` Example

```toml
[provider]
default = "gemini"  # Default provider: ollama | omniroute | openai | gemini | groq | openrouter | anthropic | qwen | kimi

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

[approval]
auto_approve_reads = true
auto_approve_writes = false
auto_approve_shell = false
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
