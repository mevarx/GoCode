<div align="center">

# GoCode

**A Go-native, local-first, provider-agnostic AI terminal coding agent.**

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=flat-square)](https://github.com/gocode-cli/gocode)

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

Unlike cloud-locked AI tools, GoCode puts privacy, security, and developer control first. It gives you the flexibility to run **local Ollama models** directly or connect to **OmniRoute** gateway proxies.

```
GoCode — Terminal Coding Agent
Provider: ollama | Model: codellama
Tools: shell_exec, file_read, file_write, file_patch
Type your message (or 'exit' to quit)
──────────────────────────────────────────────────

> Explain main.go and list all open issues
```

---

## Features

- **Dual Provider Support** — Seamlessly choose between **Ollama** (default) or **OmniRoute** gateway proxy.
- **Local-First & Private** — Runs with local LLMs out of the box (`codellama`, `llama3`, `mistral`, `deepseek-coder`, `gemma`, etc.).
- **Model Agnostic** — Automatically detects and runs any model pulled in Ollama or exposed via OmniRoute (`auto`, `auto/coding`).
- **On-the-Fly Switching** — Switch providers or models instantly inside an active session using `/provider` and `/model`.
- **Human-in-the-Loop Security** — All file writes and shell execution commands require your explicit approval before execution.
- **Single Binary, Zero Overhead** — Native Go executable with no Node.js runtime and minimal startup latency.
- **Diagnostic Doctor Subcommand** — Run `gocode doctor` to instantly check provider health and reachability for both Ollama and OmniRoute.

---

## Quick Start

### Prerequisites

- **Go 1.22+** installed on your system.
- **Ollama** (Default local provider):
  ```bash
  ollama pull codellama
  ```
- **OmniRoute** (Optional gateway proxy provider):
  ```bash
  npm install -g omniroute
  omniroute
  ```

### Installation

#### Option 1: Install via `go install`

```bash
go install github.com/gocode-cli/gocode/cmd/gocode@latest
```

#### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/gocode-cli/gocode.git
cd gocode

# Build executable
go build -o gocode ./cmd/gocode/

# Run GoCode
./gocode
```

---

## Running Providers

GoCode supports two primary provider options out of the box:

### Option 1: Running with Ollama (Default)

GoCode connects to your local Ollama instance (`http://localhost:11434`) by default:

```bash
# Launch with Ollama (auto-detects installed models)
gocode

# Launch with a specific Ollama model
gocode --provider ollama --model llama3.1:8b
```

### Option 2: Running with OmniRoute Gateway Proxy

GoCode connects to your OmniRoute instance (`http://localhost:20128/v1`):

```bash
# Launch with OmniRoute provider
gocode --provider omniroute

# Specify OmniRoute model routing
gocode --provider omniroute --model auto/coding
```

### Switching Mid-Session

You can switch between Ollama and OmniRoute at any time inside an active terminal session:

```text
> /provider ollama
[Provider switched to ollama]

> /provider omniroute
[Provider switched to omniroute]

> /model auto
[Model set to auto]
```

### Health Check (`gocode doctor`)

Verify connectivity and reachability of both providers simultaneously:

```bash
gocode doctor
```

---

## Interactive Slash Commands

During an active session, use built-in slash commands:

| Command | Action |
| :--- | :--- |
| `exit` / `quit` | Exit the GoCode agent session |
| `/clear` | Reset conversation history while keeping system instructions intact |
| `/providers` | List all registered providers, active provider, and available models |
| `/provider <name>` | Switch active provider on the fly (`/provider ollama` or `/provider omniroute`) |
| `/model <name>` | Switch active model on the fly (`/model llama3.1:8b` or `/model auto`) |

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
default = "ollama"  # Set to "ollama" or "omniroute"

[provider.ollama]
host = "http://localhost:11434"
default_model = ""  # Auto-detects any pulled local Ollama model

[provider.omniroute]
base_url = "http://localhost:20128/v1"
api_key = ""
default_model = "auto"

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
│   ├── provider/         # Provider registry, Ollama (default) & GatewayProxy (OmniRoute)
│   └── tools/            # Tool registry, execution safety & approval gates
├── go.mod                # Module dependencies
└── README.md             # Documentation
```

---

## License

This project is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.
