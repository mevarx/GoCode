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

Unlike cloud-locked AI tools, GoCode puts privacy, security, and developer control first. It runs with **Ollama** locally by default, and also seamlessly supports gateway proxy providers (like **OmniRoute**) and cloud LLMs.

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

- **Local-First & Private (Ollama Default)** — Runs with local LLMs via Ollama out of the box (`codellama`, `llama3`, `mistral`, `deepseek-coder`, `gemma`, etc.).
- **OmniRoute Gateway Support** — Built-in OpenAI-compatible proxy adapter for OmniRoute (`http://localhost:20128/v1`).
- **Provider Agnostic** — Switch between Ollama (default), OmniRoute, and external providers on the fly using `/provider <name>`.
- **Model Agnostic** — Automatically detects and runs any model pulled in Ollama or exposed via OmniRoute.
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
  # Or pull any model: ollama pull llama3.1
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

## Usage

Start GoCode with default settings (launches with Ollama as the default provider):

```bash
gocode
```

### Health Check (`gocode doctor`)

Verify connectivity and reachability of Ollama and OmniRoute:

```bash
gocode doctor
```

### Command Flags

Specify provider, model, or configuration file via CLI flags:

```bash
# Launch with Ollama and a specific model
gocode --provider ollama --model llama3.1:8b

# Launch with OmniRoute gateway proxy
gocode --provider omniroute --model auto
```

### Interactive Slash Commands

During an active session, use built-in slash commands:

| Command | Action |
| :--- | :--- |
| `exit` / `quit` | Exit the GoCode agent session |
| `/clear` | Reset conversation history while keeping system instructions intact |
| `/providers` | List all registered providers, active provider, and available models |
| `/provider <name>` | Switch active provider on the fly (e.g. `/provider ollama` or `/provider omniroute`) |
| `/model <name>` | Switch active model on the fly (e.g. `/model llama3.1:8b` or `/model auto`) |

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
