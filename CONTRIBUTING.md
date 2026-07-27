# Contributing to GoCode

Thank you for your interest in contributing to **GoCode**! We welcome contributions from developers of all skill levels. Whether you are fixing a bug, adding a new provider, introducing a tool, or polishing documentation, your help makes GoCode better for everyone.

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [How Can I Contribute?](#how-can-i-contribute)
  - [Reporting Bugs](#reporting-bugs)
  - [Suggesting Enhancements](#suggesting-enhancements)
  - [Adding a Provider or Tool](#adding-a-provider-or-tool)
  - [Submitting Pull Requests](#submitting-pull-requests)
- [Development Setup](#development-setup)
- [Project Architecture](#project-architecture)
- [Extending GoCode](#extending-gocode)
  - [Adding a New Tool](#adding-a-new-tool)
  - [Adding a New Provider](#adding-a-new-provider)
- [Coding Guidelines](#coding-guidelines)
- [Testing](#testing)
- [Security Policy](#security-policy)

---

## Code of Conduct

GoCode is an open, welcoming, and inclusive community. Please be respectful and constructive in all issues, pull requests, and discussions.

---

## How Can I Contribute?

### Reporting Bugs

Before creating a bug report, please check existing issues to avoid duplicates. When filing a bug report, include:

1. **GoCode Version**: Output of `gocode --version` or git commit hash.
2. **OS & Environment**: (e.g., Windows 11, macOS Sequoia, Ubuntu 24.04).
3. **LLM Provider & Model**: (e.g., Ollama `codellama`).
4. **Reproduction Steps**: Clear, step-by-step instructions.
5. **Expected vs. Actual Behavior**: Include console logs or error stack traces if applicable.

### Suggesting Enhancements

Feature requests are tracked as GitHub Issues. Please include:

- A clear, descriptive title.
- The **use case** or problem you are trying to solve.
- Proposed solution or API design.

### Adding a Provider or Tool

GoCode is designed around modular Go interfaces (`provider.Provider` and `tools.Tool`). Adding a new LLM backend or tool integration is a great first contribution! See [Extending GoCode](#extending-gocode) below.

### Submitting Pull Requests

1. **Fork the repository** on GitHub.
2. **Clone your fork** locally:
   ```bash
   git clone https://github.com/YOUR_USERNAME/gocode.git
   cd gocode
   ```
3. **Create a topic branch**:
   ```bash
   git checkout -b feat/my-new-feature
   ```
4. **Make your changes** following the [Coding Guidelines](#coding-guidelines).
5. **Verify your changes**:
   ```bash
   go test ./...
   go vet ./...
   ```
6. **Commit your changes** with a clear, concise commit message:
   ```bash
   git commit -m "feat(tools): add web search tool"
   ```
7. **Push to your fork** and open a **Pull Request** against `main`.

---

## Development Setup

### Prerequisites

- **Go**: 1.22 or higher installed ([golang.org](https://golang.org/dl/)).
- **Git**: Installed and configured.
- **Ollama**: (Optional but recommended for local testing) Running locally on port `11434` ([ollama.com](https://ollama.com)).

### Building & Running Locally

```bash
# Clone the repository
git clone https://github.com/mevarx/GoCode.git
cd gocode

# Build binary
go build -o gocode ./cmd/gocode/

# Run interactive binary
./gocode

# Or run directly with flags
go run ./cmd/gocode/ --provider ollama --model codellama
```

---

## Project Architecture

GoCode follows standard Go layout patterns:

```
gocode/
├── cmd/
│   └── gocode/           # Main entry point (Cobra CLI)
├── internal/
│   ├── agent/            # Core agent loop, conversation history, context management
│   ├── config/           # Configuration management (TOML, XDG paths)
│   ├── provider/         # LLM Provider interfaces and implementations (Ollama, etc.)
│   └── tools/            # Tool interfaces, approval gate, and tool implementations
├── go.mod                # Module dependencies
├── go.sum
├── CONTRIBUTING.md       # Contribution guide
└── README.md             # Project overview & quickstart
```

---

## Extending GoCode

### Adding a New Tool

All tools must implement the `tools.Tool` interface defined in [`internal/tools/tool.go`](file:///d:/CODING/GoCode/internal/tools/tool.go):

```go
type Tool interface {
    Spec() ToolSpec
    Execute(ctx context.Context, args json.RawMessage) (Result, error)
    RequiresApproval() bool
}
```

1. Create a new file in `internal/tools/your_tool.go`.
2. Implement `Spec()` (returns name, description, JSON schema parameters), `Execute()`, and `RequiresApproval()`.
3. Register your tool in [`cmd/gocode/main.go`](cmd/gocode/main.go):
   ```go
   toolRegistry.Register(&tools.YourTool{})
   ```
4. Add tests for your tool in `internal/tools/your_tool_test.go`.

### Adding a New Provider

Providers must implement the `provider.Provider` interface in [`internal/provider/provider.go`](internal/provider/provider.go):

```go
type Provider interface {
    Name() string
    Models(ctx context.Context) ([]string, error)
    Stream(ctx context.Context, model string, history []Message, tools []ToolSpec) (<-chan StreamChunk, error)
}
```

1. Create a new file in `internal/provider/your_provider.go`.
2. Implement `Name()`, `Models()`, and `Stream()` with streaming communication to your backend API.
3. Register the provider in [`cmd/gocode/main.go`](cmd/gocode/main.go).

---

## Coding Guidelines

- **Format**: Run `gofmt -s -w .` before committing.
- **Linting**: Ensure `go vet ./...` reports zero issues.
- **Error Handling**: Always handle returned errors. Wrap errors with contextual information using `fmt.Errorf("failed to ...: %w", err)`.
- **Security**: Any tool performing file modifications or executing system commands MUST set `RequiresApproval() bool { return true }`.
- **Dependencies**: Keep external dependencies minimal. Prefer the Go standard library where possible.

---

## Testing

Run unit tests across all packages:

```bash
go test -v ./...
```

For package coverage:

```bash
go test -cover ./...
```

---

## Security Policy

If you discover a security vulnerability, please **do not open a public issue**. Instead, report it privately to the maintainers via email or security advisory.

---

Thank you for helping build a better, open-source terminal coding experience!
