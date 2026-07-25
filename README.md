<div align="center">

# GoCode

**A Go-native, local-first, provider-agnostic AI terminal coding agent.**

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=flat-square)](CONTRIBUTING.md)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue?style=flat-square)](https://github.com/gocode-cli/gocode)

[Features](#features) •
[Quick Start](#quick-start) •
[Configuration](#configuration) •
[Tools & Security](#tools--security) •
[Architecture](#architecture) •
[Contributing](#contributing)

---

</div>

## Overview

**GoCode** is a lightweight, high-performance terminal AI coding agent written in Go. It operates directly inside your terminal, assisting you with code generation, debugging, refactoring, file inspection, and command execution.

Unlike cloud-locked AI tools, GoCode puts privacy, security, and developer control first. It connects seamlessly to any model hosted on your local Ollama instance or external provider APIs.

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

- **Local-First & Private** — Runs with any local LLM via Ollama out of the box. Zero data leaves your machine unless explicitly configured.
- **Model Agnostic** — Automatically detects and works with any model pulled in Ollama (`codellama`, `llama3`, `mistral`, `deepseek-coder`, `qwen2.5-coder`, `gemma`, etc.).
- **Provider Agnostic** — Seamlessly switch between Ollama, OpenAI-compatible APIs, and cloud providers through a unified interface.
- **Human-in-the-Loop Security** — All file writes and shell execution commands require your explicit approval before execution.
- **Single Binary, Zero Overhead** — Native Go executable with no Node.js runtime, no `npm install`, and minimal startup latency.
- **Extensible Tool Engine** — Easily add custom commands, file processors, or provider implementations.
- **Zero Telemetry** — No analytics, tracking scripts, or external phone-home calls.

---

## Quick Start

### Prerequisites

- **Go 1.22+** installed on your system.
- **Ollama** (Recommended for local offline execution with any pulled model):
  ```bash
  # Pull any Ollama model of your choice
  ollama pull codellama
  # Or: ollama pull llama3.1
  # Or: ollama pull mistral
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

## Usage

Start GoCode with default settings (automatically detects any model available in local Ollama):

```bash
gocode
```

### Command Flags

Specify provider, model, or configuration file via CLI flags:

```bash
# Launch with a specific model installed in Ollama
gocode --model llama3.1:8b

# Override provider and config file path
gocode --provider ollama --model deepseek-coder:6.7b --config path/to/config.toml
```

### Interactive Commands

During an active session, use built-in slash commands:

| Command | Action |
| :--- | :--- |
| `exit` / `quit` | Exit the GoCode agent session |
| `/clear` | Reset conversation history while keeping system instructions intact |

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
default = "ollama"

[provider.ollama]
host = "http://localhost:11434"
default_model = ""  # Leave empty to auto-detect any pulled Ollama model

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
│   └── gocode/           # CLI entry point, flag parsing & dependency wiring
├── internal/
│   ├── agent/            # Core agent loop, session memory & context state
│   ├── config/           # XDG directory management & TOML parser
│   ├── provider/         # Provider registry & LLM API streaming adapters
│   └── tools/            # Tool registry, execution safety & approval gates
├── CONTRIBUTING.md       # Developer contribution guidelines
├── go.mod                # Module dependencies
└── README.md             # Documentation
```

---

## Contributing

Contributions are welcome. Whether you are interested in fixing bugs, adding new tools, expanding provider support, or improving docs:

1. Read our [**Contributing Guide**](CONTRIBUTING.md) for setup and development standards.
2. Fork the repository and create your feature branch (`git checkout -b feat/awesome-feature`).
3. Ensure all tests pass (`go test ./...` and `go vet ./...`).
4. Submit a Pull Request.

---

## License

This project is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.
