# Configuration

GoCode reads one TOML file and a small set of environment variables. Every key
below is the complete surface: anything else is rejected at load time. For how
these settings affect behavior, see [architecture.md](architecture.md); for
provider-specific setup, see [providers.md](providers.md).

- [File locations](#file-locations)
- [How the file is loaded](#how-the-file-is-loaded)
- [`[provider]`](#provider)
- [Gateway sections](#gateway-sections)
- [`[provider.custom.<name>]`](#providercustomname)
- [`[permissions]`](#permissions)
- [`[session]`](#session)
- [`[tools]`](#tools)
- [`[tools.shell]`](#toolsshell)
- [`[context]`](#context)
- [`[tui]`](#tui)
- [`[mcp.servers.<name>]`](#mcpserversname)
- [Environment variables](#environment-variables)
- [Platform paths](#platform-paths)
- [Complete annotated example](#complete-annotated-example)

## File locations

| Purpose | Linux / macOS | Windows |
| :-- | :-- | :-- |
| Config file | `~/.config/gocode/config.toml` | `%APPDATA%\gocode\config.toml` |
| Project/global context | `~/.config/gocode/CONTEXT.md` | `%APPDATA%\gocode\CONTEXT.md` |
| Copilot token | `~/.config/gocode/copilot_token` | `%APPDATA%\gocode\copilot_token` |
| Sessions | `~/.local/share/gocode/sessions/sessions.db` | `%LOCALAPPDATA%\gocode\sessions\sessions.db` |
| Log | `~/.local/share/gocode/logs/gocode.log` | `%LOCALAPPDATA%\gocode\logs\gocode.log` |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` override the Unix bases. All directories
are created `0700`, and the config file, log file, session database (plus its WAL
sidecars) and Copilot token are `0600`. Config, log and session files are
re-tightened whenever they are opened.

`--config <path>` selects a different file. An agent run fails if that path does
not exist; `doctor`, `provider`, `mcp` and `session` treat a missing file as empty
defaults and create it when they save.

## How the file is loaded

1. Start from the built-in defaults (below).
2. Decode the TOML file over them, so omitted keys keep their defaults.
3. **Reject unknown keys.** A misspelled key is a hard error listing the
   offending keys, not a silently ignored setting. Keys are matched exactly, so
   only the spelling shown in this document works.
4. Validate `[tui] motion` (`auto`, `reduced` or `off`; empty means `auto`;
   anything else is an error).
5. Validate every configured base URL: absolute `http`/`https`, with a host, and
   no embedded credentials, query or fragment. Plain HTTP is allowed only for
   `localhost`, `*.localhost`, or a loopback IP. A trailing slash is trimmed.
6. Print a warning for every provider whose key is stored inline as `api_key`
   (the value itself is never printed).

Only the sections you change need to appear.

## `[provider]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `default` | string | `"ollama"` | Provider used when `--provider` is absent and no resumed session names one. |
| `stream_usage` | bool | `false` | Ask OpenAI-compatible gateways to report token usage on the final stream chunk (`stream_options.include_usage`). Parsing usage is always on; this only makes providers that hide it send it. Leave off for gateways that reject unknown request fields. Ignored by the native Anthropic, Copilot and Ollama dialects, which report usage on their own. |

Every provider has a section under `[provider.<name>]`. The model used at startup
is resolved in this order: `--model`, then the resumed session's model, then the
provider's `default_model` (for Ollama with no default, the first model the server
reports, falling back to `codellama`).

## Gateway sections

`[provider.<name>]` accepts four keys for every OpenAI-compatible gateway and for
Anthropic:

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `base_url` | string | per provider | Endpoint root. The proxy appends `/chat/completions` (or `/messages` for Anthropic) and `/models`. Validated as described above. |
| `api_key` | string | `""` | Inline key. Stored in plaintext; `gocode doctor` warns. Prefer `api_key_env`. |
| `api_key_env` | string | per provider | Name of the environment variable holding the key. Only the name is stored. |
| `default_model` | string | per provider | Model id sent when neither `--model` nor a resumed session selects one. |

Built-in defaults:

| Section | `base_url` | `api_key_env` | `default_model` |
| :-- | :-- | :-- | :-- |
| `omniroute` | `http://127.0.0.1:20128/v1` | — | `auto` |
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `gpt-4o` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | `GEMINI_API_KEY` | `gemini-2.5-flash` |
| `groq` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `llama-3.3-70b-versatile` |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `anthropic/claude-sonnet-4.5` |
| `anthropic` | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` | `claude-sonnet-4-20250514` |
| `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` | `qwen-max` |
| `kimi` | `https://api.moonshot.cn/v1` | `MOONSHOT_API_KEY` | `moonshot-v1-8k` |
| `hermes` | `http://127.0.0.1:8642/v1` | `HERMES_API_SERVER_KEY` | `hermes-agent` |
| `xai` | `https://api.x.ai/v1` | `XAI_API_KEY` | `grok-code-fast-1` |
| `mistral` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | `mistral-large-latest` |
| `minimax` | `https://api.minimax.io/v1` | `MINIMAX_API_KEY` | `MiniMax-M2.5` |
| `deepseek` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` | `deepseek-chat` |
| `together` | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | `Qwen/Qwen3-Coder-480B-A35B-Instruct` |
| `fireworks` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `accounts/fireworks/models/kimi-k2-thinking` |
| `cerebras` | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` | `qwen-3-coder-480b` |
| `zhipu` | `https://open.bigmodel.cn/api/paas/v4` | `ZHIPU_API_KEY` | `glm-4.6` |
| `nvidia` | `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` | `qwen/qwen3-coder-480b-a35b-instruct` |

`[provider.ollama]` is different:

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `host` | string | `"http://127.0.0.1:11434"` | Ollama base URL. A bare `host:port` is treated as `http://host:port`. When empty, `OLLAMA_HOST` is resolved instead. |
| `default_model` | string | `""` | Model to use; empty means "ask the server and take the first model". |

`[provider.copilot]` is different too:

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `oauth_token_env` | string | `"GITHUB_COPILOT_TOKEN"` | Environment variable holding the GitHub OAuth token. The saved token file is used when the variable is empty. |
| `default_model` | string | `"gpt-4.1"` | Model id. |
| `editor_version` | string | `"vscode/1.111.0"` | Sent as `Editor-Version`; Copilot rejects requests without editor headers. |
| `editor_plugin_version` | string | `"copilot-chat/0.40.0"` | Sent as `Editor-Plugin-Version` (and used to build the `User-Agent`). |

## `[provider.custom.<name>]`

Custom endpoints use the same four gateway keys (`base_url`, `api_key`,
`api_key_env`, `default_model`) and are normally written by
`gocode provider add`. Rules enforced when the config is loaded:

- `<name>` must be **lowercase** and match `[a-z][a-z0-9_-]{0,31}`.
- `base_url` is required and validated (HTTPS unless loopback).
- `default_model` is required and non-empty.
- `api_key_env`, if present, must be a valid environment-variable name
  (`[A-Za-z_][A-Za-z0-9_]*`).

A custom entry whose name matches a built-in provider shadows the built-in,
because custom configuration is checked first.

## `[permissions]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `auto_approve` | list of strings | `["file_read"]` | Tools that run without a prompt. `deny` always wins over this list. |
| `deny` | list of strings | `[]` | Tools that are permanently blocked. A denied call returns an error result and never executes. |
| `sensitive_patterns` | list of strings | `[]` | Extra file patterns hidden from the file tools and MCP, on top of the built-in sensitive set (`.env*`, SSH keys, `*.pem`, `*.key`, cloud credential files, token files, kubeconfigs). Matched by basename, glob and path suffix. |

Tool names are the registered names: `file_read`, `code_search`, `plan`,
`scratchpad`, `file_write`, `file_patch`, `shell_exec`, and `mcp_<server>_<tool>`
for MCP tools. Approving `shell_exec` is the same as handing the agent your
terminal; see [SECURITY.md](SECURITY.md).

## `[session]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `persist` | bool | `true` | Write conversations to SQLite and resume the last workspace session. `false` keeps everything in memory: the store is never opened, and `session export` refuses to run. |

## `[tools]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `max_tool_iterations` | int | `0` → 50 | Provider round-trips allowed per user turn. Zero or negative uses 50. |
| `max_repeated_tool_calls` | int | `0` → 3 | Maximum consecutive batches repeating identical tool calls (same name, same canonical arguments). Zero or negative uses 3. |
| `max_context_tokens` | int | `0` | Overrides the per-model token budget. Zero or negative derives it as 80% of the active model's window. |
| `verify_command` | string | `""` | Command run once after any provider round that executed a tool able to modify the workspace. Empty disables auto-verify. Runs in the workspace **without** an approval prompt. |
| `verify_timeout_seconds` | int | `120` | Bound on one auto-verify run. Zero or negative uses 120. Only consulted when `verify_command` is set. |

## `[tools.shell]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `timeout_seconds` | int | `30` | Per-command timeout. Zero or negative falls back to the built-in default (30 s). |
| `max_output_bytes` | int | `1048576` | Cap on captured stdout and on captured stderr, each. Excess is discarded and the result notes truncation. |
| `redact_secrets` | bool | `true` | Mask credential-shaped values in `shell_exec` output before it becomes a tool result. Setting it `false` does **not** disable transcript redaction, which always applies to tool results, persisted arguments and exports. |
| `env_scrub` | list of strings | `[]` | Extra environment-variable names or glob patterns to remove from child processes (shell and MCP), on top of the built-in credential patterns and every configured `api_key_env`. Patterns may use `*`; matching is case-insensitive. |

Built-in scrub patterns: `*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`,
`*_PASSWD`, `*_KEY`, `*_ACCESS_KEY`, `*_PRIVATE_KEY`, `*_CREDENTIALS`. Essential
variables (`PATH`, `HOME`, `USERPROFILE`, `SystemRoot`, `TEMP`, `COMSPEC`,
`PATHEXT`, `LC_*`, …) always survive.

## `[context]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `auto_compact` | bool | `true` | Summarize older turns before the provider window overflows. Truncation remains the last resort. |
| `keep_recent_tokens` | int | `20000` | Approximate verbatim tail automatic compaction preserves. Zero or negative uses 20000. Does not affect manual `/compact`, which always keeps two turns. |

## `[tui]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `motion` | string | `"auto"` | `auto` follows the terminal's reduced-motion signal (`GOCODE_REDUCED_MOTION` / `REDUCED_MOTION`); `reduced` stops decorative motion; `off` is the strictest. Functional indicators (spinner glyph, elapsed time) degrade to static instead of disappearing. |
| `smooth_streaming` | bool | `true` | Pace streamed text and tool-argument previews into the transcript instead of appending raw provider bursts. Pacing also requires full motion. |

## `[mcp.servers.<name>]`

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `command` | string | — | Executable to spawn. A server without a command is skipped. |
| `args` | list of strings | `[]` | Arguments passed verbatim. |
| `env` | table of strings | `{}` | Extra environment variables for the server, applied **after** scrubbing. |
| `request_timeout_seconds` | int | `0` → 60 | Bound on a single JSON-RPC request to this server. Zero or negative uses 60. |

```toml
[mcp.servers.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/repo"]
request_timeout_seconds = 60

[mcp.servers.filesystem.env]
LOG_LEVEL = "warn"
```

`gocode mcp add <name> <command> [args...]` writes `command` and `args` only; set
`env` and the timeout by editing the file.

## Environment variables

| Variable | Effect |
| :-- | :-- |
| `GOCODE_THEME` | `light` or `dark` forces the palette; otherwise it is detected once on a TTY (pipes default to dark). |
| `GOCODE_REDUCED_MOTION`, `REDUCED_MOTION` | Any value except empty/`0`/`false`/`no`/`off` asks `motion = "auto"` to resolve to reduced. |
| `NO_COLOR` | Any non-empty value disables colour; it wins over `CLICOLOR_FORCE`. |
| `CLICOLOR_FORCE` | Any value except empty/`0` forces colour even on a pipe. |
| `TERM` | `dumb` disables colour and escape-sequence rendering. |
| `OLLAMA_HOST` | Ollama endpoint when `[provider.ollama] host` is empty. Bare hosts mean HTTP (except `ollama.com`, which means HTTPS); `http`/`https` select ports 80/443; a path after the first `/` becomes the URL path; unparsable values fall back to `127.0.0.1:11434`. |
| `OLLAMA_AUTH` | Non-empty enables ed25519 request signing; unparsable values count as enabled. Requests to `ollama.com` are always signed. |
| `GITHUB_COPILOT_TOKEN` | GitHub OAuth token for Copilot (unless `oauth_token_env` renames it). The saved token file is used when this is empty. |
| `OPENAI_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY`, `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `DASHSCOPE_API_KEY`, `MOONSHOT_API_KEY`, `HERMES_API_SERVER_KEY`, `XAI_API_KEY`, `MISTRAL_API_KEY`, `MINIMAX_API_KEY`, `DEEPSEEK_API_KEY`, `TOGETHER_API_KEY`, `FIREWORKS_API_KEY`, `CEREBRAS_API_KEY`, `ZHIPU_API_KEY`, `NVIDIA_API_KEY` | Default key variables for the built-in providers. Changing `api_key_env` changes which name is read. |
| `APPDATA`, `LOCALAPPDATA`, `USERPROFILE`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME` | Platform directory bases (see below). |

There is no `GOCODE_CONFIG`, `GOCODE_MODEL` or `GOCODE_HOME`; use `--config`,
`--model` and the platform directories.

## Platform paths

| | Windows | Linux / macOS |
| :-- | :-- | :-- |
| Config dir | `%APPDATA%\gocode` (fallback `%USERPROFILE%\AppData\Roaming\gocode`) | `$XDG_CONFIG_HOME/gocode` or `~/.config/gocode` |
| Data dir | `%LOCALAPPDATA%\gocode` (fallback `%USERPROFILE%\AppData\Local\gocode`) | `$XDG_DATA_HOME/gocode` or `~/.local/share/gocode` |
| Sessions | `<data>\sessions\sessions.db` (+ `-wal`, `-shm`) | `<data>/sessions/sessions.db` (+ `-wal`, `-shm`) |
| Logs | `<data>\logs\gocode.log` | `<data>/logs/gocode.log` |

`--verbose` also mirrors logs to stderr at debug level. `gocode logs -n 200 -f`
tails the file without loading it whole.

## Complete annotated example

```toml
# ~/.config/gocode/config.toml          (Linux / macOS)
# %APPDATA%\gocode\config.toml          (Windows)

[provider]
default = "gemini"        # provider used when --provider is absent
stream_usage = false      # opt-in: stream_options.include_usage for gateways

[provider.ollama]
host = "http://127.0.0.1:11434"
default_model = ""        # empty: first model the server reports

[provider.gemini]
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
api_key_env = "GEMINI_API_KEY"   # preferred: the key stays in the environment
default_model = "gemini-2.5-flash"
# api_key = ""            # inline key: plaintext on disk, doctor warns

[provider.anthropic]
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-20250514"

[provider.copilot]
oauth_token_env = "GITHUB_COPILOT_TOKEN"
default_model = "gpt-4.1"
editor_version = "vscode/1.111.0"
editor_plugin_version = "copilot-chat/0.40.0"

[provider.custom.my-gateway]
base_url = "https://gateway.internal/v1"
api_key_env = "MY_GATEWAY_KEY"
default_model = "my-model"

[permissions]
auto_approve = ["file_read", "code_search"]
deny = []                            # e.g. ["shell_exec"] for a hard boundary
sensitive_patterns = ["*.vault", "config/prod-*"]

[session]
persist = true

[tools]
max_tool_iterations = 50             # provider round-trips per turn
max_repeated_tool_calls = 3          # identical consecutive batches per turn
max_context_tokens = 0               # 0: 80% of the active model's window
verify_command = "go test ./..."     # runs after mutating rounds; empty disables
verify_timeout_seconds = 120

[tools.shell]
timeout_seconds = 30
max_output_bytes = 1048576
redact_secrets = true                # shell output only; transcript redaction stays on
env_scrub = ["MY_GATEWAY_KEY", "TEAM_*_CREDENTIALS"]

[context]
auto_compact = true
keep_recent_tokens = 20000           # verbatim tail kept by auto-compaction

[tui]
motion = "auto"                      # auto | reduced | off
smooth_streaming = true

[mcp.servers.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/repo"]
request_timeout_seconds = 60

[mcp.servers.filesystem.env]
LOG_LEVEL = "warn"
```
