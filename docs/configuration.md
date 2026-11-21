# Configuration

GoCode reads one TOML file, a set of CLI flags, and a small set of environment
variables. Every key below is the complete surface: anything else is rejected
at load time. For how these settings affect behavior, see
[architecture.md](architecture.md); for provider-specific setup, see
[providers.md](providers.md); for themes, see [themes.md](themes.md); for the
hook contract, see [hooks.md](hooks.md).

The **Added** column names the release that introduced a key: `v0.7.5` or
earlier for existing keys, `Unreleased` for the latest additions.

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
- [`[hooks]`](#hooks)
- [`[mcp.servers.<name>]`](#mcpserversname)
- [CLI flags](#cli-flags)
- [Environment variables](#environment-variables)
- [Platform paths](#platform-paths)
- [Complete annotated example](#complete-annotated-example)

## File locations

| Purpose | Linux / macOS | Windows |
| :-- | :-- | :-- |
| Config file | `~/.config/gocode/config.toml` | `%APPDATA%\gocode\config.toml` |
| User themes | `~/.config/gocode/themes/*.json` | `%APPDATA%\gocode\themes\*.json` |
| Prompt history | `~/.config/gocode/history/<sha8>.json` | `%APPDATA%\gocode\history\<sha8>.json` |
| Project/global context | `~/.config/gocode/CONTEXT.md` | `%APPDATA%\gocode\CONTEXT.md` |
| Copilot token | `~/.config/gocode/copilot_token` | `%APPDATA%\gocode\copilot_token` |
| Sessions | `~/.local/share/gocode/sessions/sessions.db` | `%LOCALAPPDATA%\gocode\sessions\sessions.db` |
| Log | `~/.local/share/gocode/logs/gocode.log` | `%LOCALAPPDATA%\gocode\logs\gocode.log` |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` override the Unix bases. Data directories
are created `0700`, and the config file, log file, session database (plus its WAL
sidecars), Copilot token and prompt-history files are `0600`. Config, log and
session files are re-tightened whenever they are opened. The themes directory is
`0755` and user theme files are written `0644` (they contain only colour data).

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
5. Validate `[tui] active_theme` (letters, digits, `-`, `_`, `.`; at most 64
   characters; empty means the default `gocode-dark`). Whether the name
   actually resolves is checked at startup, where an unknown theme logs a
   warning and falls back to the default — see [themes.md](themes.md).
6. Validate `[permissions] mode` (`normal` or `yolo`, case-insensitive; empty
   means `normal`; anything else is an error — a misspelled mode must not
   silently fall back to prompting or to skipping prompts).
7. Normalize and validate `[hooks]`: event names resolve case-insensitively
   with underscores stripped, unknown events/keys and empty commands are
   rejected, matchers must compile, negative timeouts are rejected, unset
   timeouts become 30 s, and hooks dedup by command per event.
8. Validate every configured base URL: absolute `http`/`https`, with a host, and
   no embedded credentials, query or fragment. Plain HTTP is allowed only for
   `localhost`, `*.localhost`, or a loopback IP. A trailing slash is trimmed.
9. Print a warning for every provider whose key is stored inline as `api_key`
   (the value itself is never printed).

Only the sections you change need to appear.

## `[provider]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `default` | string | `"ollama"` | v0.7.5 | Provider used when `--provider` is absent and no resumed session names one. |
| `stream_usage` | bool | `false` | v0.7.5 | Ask OpenAI-compatible gateways to report token usage on the final stream chunk (`stream_options.include_usage`). Parsing usage is always on; this only makes providers that hide it send it. Leave off for gateways that reject unknown request fields. Ignored by the native Anthropic, Copilot and Ollama dialects, which report usage on their own. |

Every provider has a section under `[provider.<name>]`. The model used at startup
is resolved in this order: `--model`, then the resumed session's model, then the
provider's `default_model` (for Ollama with no default, the first model the server
reports, falling back to `codellama`).

## Gateway sections

`[provider.<name>]` accepts four keys for every OpenAI-compatible gateway and for
Anthropic (all v0.7.5):

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
| `minimax` | `https://api.minimax.io/v1` | `MINIMAX_API_KEY` | `MiniMax-Unreleased.5` |
| `deepseek` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` | `deepseek-chat` |
| `together` | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | `Qwen/Qwen3-Coder-480B-A35B-Instruct` |
| `fireworks` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `accounts/fireworks/models/kimi-k2-thinking` |
| `cerebras` | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` | `qwen-3-coder-480b` |
| `zhipu` | `https://open.bigmodel.cn/api/paas/v4` | `ZHIPU_API_KEY` | `glm-4.6` |
| `nvidia` | `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` | `qwen/qwen3-coder-480b-a35b-instruct` |

`[provider.ollama]` is different (v0.7.5):

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `host` | string | `"http://127.0.0.1:11434"` | Ollama base URL. A bare `host:port` is treated as `http://host:port`. When empty, `OLLAMA_HOST` is resolved instead. |
| `default_model` | string | `""` | Model to use; empty means "ask the server and take the first model". |

`[provider.copilot]` is different too (v0.7.5):

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `oauth_token_env` | string | `"GITHUB_COPILOT_TOKEN"` | Environment variable holding the GitHub OAuth token. The saved token file is used when the variable is empty. |
| `default_model` | string | `"gpt-4.1"` | Model id. |
| `editor_version` | string | `"vscode/1.111.0"` | Sent as `Editor-Version`; Copilot rejects requests without editor headers. |
| `editor_plugin_version` | string | `"copilot-chat/0.40.0"` | Sent as `Editor-Plugin-Version` (and used to build the `User-Agent`). |

## `[provider.custom.<name>]`

Custom endpoints use the same four gateway keys (`base_url`, `api_key`,
`api_key_env`, `default_model`) and are normally written by
`gocode provider add` (v0.7.5). Rules enforced when the config is loaded:

- `<name>` must be **lowercase** and match `[a-z][a-z0-9_-]{0,31}`.
- `base_url` is required and validated (HTTPS unless loopback).
- `default_model` is required and non-empty.
- `api_key_env`, if present, must be a valid environment-variable name
  (`[A-Za-z_][A-Za-z0-9_]*`).

A custom entry whose name matches a built-in provider shadows the built-in,
because custom configuration is checked first.

## `[permissions]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `auto_approve` | list of strings | `["file_read"]` | v0.7.5 | Tools that run without a prompt. `deny` always wins over this list. |
| `deny` | list of strings | `[]` | v0.7.5 | Tools that are permanently blocked. A denied call returns an error result and never executes. Unconditional in every mode, and it outranks hook allows and session grants. |
| `sensitive_patterns` | list of strings | `[]` | v0.7.5 | Extra file patterns hidden from the file tools and MCP, on top of the built-in sensitive set (`.env*`, SSH keys, `*.pem`, `*.key`, cloud credential files, token files, kubeconfigs). Matched by basename, glob and path suffix. |
| `mode` | string | `"normal"` | Unreleased | Prompt policy: `normal` prompts for approval-gated calls; `yolo` skips prompts for non-dangerous calls and **refuses dangerous calls without asking**. Validated at load (anything else is an error). `--yolo` overrides it for one run. The deny-list and redaction stay unconditional in every mode; a nothing-blocked `sysadmin` mode is deliberately not offered. |
| `dangerous_tools` | list of strings | `[]` | Unreleased | Tool names always classified dangerous, on top of the built-in classifier rules (`internal/tools/danger.go`). In `yolo` mode these calls are refused; in `normal` mode the prompt shows the danger warning. |

Tool names are the registered names: `file_read`, `code_search`, `plan`,
`scratchpad`, `file_write`, `file_patch`, `shell_exec`, and `mcp_<server>_<tool>`
for MCP tools. Approving `shell_exec` is the same as handing the agent your
terminal; see [SECURITY.md](SECURITY.md).

Session grants (`a` in the approval prompt) are remembered in memory keyed by
session, tool, action, directory and a grant key — the command string for shell
tools, the canonical exact arguments for everything else (Unreleased). They die with the
process and never widen to different arguments or commands.

## `[session]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `persist` | bool | `true` | v0.7.5 | Write conversations to SQLite and resume the last workspace session. `false` keeps everything in memory: the store is never opened, and `session export` refuses to run. |

## `[tools]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `max_tool_iterations` | int | `0` → 50 | v0.7.5 | Provider round-trips allowed per user turn. Zero or negative uses 50. |
| `max_repeated_tool_calls` | int | `0` → 3 | v0.7.5 | Maximum consecutive batches repeating identical tool calls (same name, same canonical arguments). Zero or negative uses 3. Independently, Unreleased's signature stop ends a turn when one tool+args signature appears in more than 5 of the last 10 batches, even interleaved. |
| `max_context_tokens` | int | `0` | v0.7.5 | Overrides the per-model token budget. Zero or negative derives it as 80% of the active model's window. (This key lives under `[tools]`, not `[context]`.) |
| `verify_command` | string | `""` | v0.7.5 | Command run once after any provider round that executed a tool able to modify the workspace. Empty disables auto-verify. Runs in the workspace **without** an approval prompt. |
| `verify_timeout_seconds` | int | `120` | v0.7.5 | Bound on one auto-verify run. Zero or negative uses 120. Only consulted when `verify_command` is set. |

## `[tools.shell]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `timeout_seconds` | int | `30` | v0.7.5 | Per-command timeout. Zero or negative falls back to the built-in default (30 s). |
| `max_output_bytes` | int | `1048576` | v0.7.5 | Cap on captured stdout and on captured stderr, each. Excess is discarded and the result notes truncation. |
| `redact_secrets` | bool | `true` | v0.7.5 | Mask credential-shaped values in `shell_exec` output before it becomes a tool result. Setting it `false` does **not** disable transcript redaction, which always applies to tool results, persisted arguments and exports. |
| `env_scrub` | list of strings | `[]` | v0.7.5 | Extra environment-variable names or glob patterns to remove from child processes (shell, MCP **and hook children**), on top of the built-in credential patterns and every configured `api_key_env`. Patterns may use `*`; matching is case-insensitive. |

Built-in scrub patterns: `*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`,
`*_PASSWD`, `*_KEY`, `*_ACCESS_KEY`, `*_PRIVATE_KEY`, `*_CREDENTIALS`. Essential
variables (`PATH`, `HOME`, `USERPROFILE`, `SystemRoot`, `TEMP`, `COMSPEC`,
`PATHEXT`, `LC_*`, …) always survive.

## `[context]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `auto_compact` | bool | `true` | v0.7.5 | Summarize older turns before the provider window overflows. Truncation remains the last resort. Unreleased adds a remaining-context backstop (compact when ≤20 000 tokens remain in windows >200 000, or ≤20% otherwise) and persists each compaction as a summary anchor so a resumed session replays the compacted window. |
| `keep_recent_tokens` | int | `20000` | v0.7.5 | Approximate verbatim tail automatic compaction preserves. Zero or negative uses 20000. Does not affect manual `/compact`, which always keeps two turns. |

The per-model budget override is `[tools] max_context_tokens` (above).

## `[tui]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `motion` | string | `"auto"` | v0.7.5 | `auto` follows the terminal's reduced-motion signal (`GOCODE_REDUCED_MOTION` / `REDUCED_MOTION`); `reduced` stops decorative motion; `off` is the strictest. Functional indicators (spinner glyph, elapsed time) degrade to static instead of disappearing. |
| `smooth_streaming` | bool | `false` | v0.7.5 (default flipped to `false`) | Opt-in pacing of streamed text and tool-argument previews into the transcript. The default renders on arrival; set `true` to pace bursts. Pacing also requires full motion. |
| `active_theme` | string | `"gocode-dark"` | Unreleased | Built-in or user theme name; case-insensitive. The deprecated alias `gocode` resolves to the default. Validated at load; an unknown name logs a warning at startup and falls back to the default. `GOCODE_THEME=light|dark` overrides it. See [themes.md](themes.md). |
| `transparent` | bool | `false` | Unreleased | Skip painting the app background so the terminal's own background shows through. |

## `[hooks]`

Added in Unreleased. Each of the 13 hook events maps to an ordered list of hook
commands; the full wire contract, exit codes and aggregation rules are in
[hooks.md](hooks.md).

```toml
[[hooks.PreToolUse]]
name    = "guard-shell"
matcher = "^shell_exec$"
command = "python3 guard.py"
timeout = 10
```

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `name` | string | `""` | Optional diagnostic label; the command is used when unset. |
| `matcher` | string | `""` | Regex on the tool name (tool events only; empty matches all; ignored on lifecycle events). Must compile. |
| `command` | string | — | Required. Run through the platform shell (`bash -c` / `cmd /C`) in the workspace, with the payload JSON on stdin. |
| `timeout` | int | `0` → 30 | Per-hook deadline in seconds; negative values rejected. `SessionEnd` additionally has a 2 s total budget. |

Event names normalize case-insensitively with underscores stripped
(`pre_tool_use` = `PreToolUse`); unknown events and keys are rejected at load,
and hooks dedup by command within each event (first occurrence wins). Valid
events: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`,
`PermissionResult`, `SessionStart`, `SessionEnd`, `TurnStart`, `TurnEnd`,
`StopFailure`, `Interrupt`, `PreCompact`, `PostCompact`.

## `[mcp.servers.<name>]`

| Key | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `command` | string | — | v0.7.5 | Executable to spawn. A server without a command is skipped. |
| `args` | list of strings | `[]` | v0.7.5 | Arguments passed verbatim. |
| `env` | table of strings | `{}` | v0.7.5 | Extra environment variables for the server, applied **after** scrubbing. |
| `request_timeout_seconds` | int | `0` → 60 | v0.7.5 | Bound on a single JSON-RPC request to this server. Zero or negative uses 60. |

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

## CLI flags

Re-derived from `cmd/gocode/main.go` and the subcommand files. There is **no
`--theme` flag**: theme selection is `[tui] active_theme`, `GOCODE_THEME`, or
the in-app picker (`Ctrl+T`).

### Root command (`gocode`)

| Flag | Type | Default | Added | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `--provider` | string | `""` | v0.7.5 | LLM provider to use (overrides config and the resumed session's provider). |
| `--model` | string | `""` | v0.7.5 | Model to use. |
| `--config` | string | `""` | v0.7.5 | Path to config file (persistent flag; works on subcommands too). |
| `--workdir` | string | `""` | v0.7.5 | Workspace root directory; defaults to the current directory. Auto-resume is scoped to it. |
| `--tui` | bool | `true` | v0.7.5 | Rich TUI; `--tui=false` selects the plain terminal REPL. `--headless` wins over it. |
| `--new` | bool | `false` | v0.7.5 | Start a new session instead of auto-resuming. |
| `--session` | string | `""` | v0.7.5 | Resume a specific session by ID (wins over workspace scoping; creates it if missing). |
| `--headless` | bool | `false` | v0.7.5 | Drive the agent over a JSONL protocol on stdin/stdout; no TUI, no prompts. |
| `--policy` | string | `""` | v0.7.5 | Path to an approval-policy JSON file for headless mode: `{"auto_approve":[…],"deny":[…]}`. Additive Unreleased fields: `"auto_approve_session": true` (never prompt; default off keeps deny-by-default) and `"dangerous_tools": […]`. Without a policy, every approval-gated call is denied. |
| `--yolo` | bool | `false` | Unreleased | Yolo permission mode for this run (TUI, plain and headless alike): skip prompts for non-dangerous calls; dangerous calls are refused. The deny-list still applies. Overrides `[permissions] mode`. |
| `--verbose`, `-v` | bool | `false` | v0.7.5 | Debug logging; also mirrors logs to stderr (persistent flag). |
| `--version` | — | — | v0.7.5 | Print version, commit and build date. |

### Subcommands

| Command | Flags | Meaning |
| :-- | :-- | :-- |
| `gocode doctor` | — | Health-check configured providers, inline-key warnings, session-storage permissions. |
| `gocode logs` | `--tail`, `-n` (int, default 50); `--follow`, `-f` (bool) | View or follow the application log without loading it whole. |
| `gocode mcp add <name> <command> [args...]` | GoCode's own `--config`/`--verbose` must come **before** the server name; `--` ends GoCode's flags | Add or update an MCP server. Flag parsing is disabled for the server command, so server flags pass through verbatim. |
| `gocode mcp list` / `gocode mcp remove <name>` | — | List or remove configured MCP servers. |
| `gocode session export [session_id]` | `--format`, `-f` (string, default `markdown`; or `json`); `--output`, `-o` (string, default stdout); `--force` (bool); `--no-tools` (bool) | Export a transcript. System prompts and reasoning are always excluded; destinations are `0600` and never overwritten without `--force`. |
| `gocode provider add <name>` | `--base-url`, `--api-key-env`, `--model` (strings) | Add a custom OpenAI-compatible endpoint (writes `[provider.custom.<name>]`). |
| `gocode provider list` / `gocode provider remove <name>` | — | List or remove custom endpoints. |
| `gocode auth copilot` | `--client-id` (string, required); `--timeout` (duration, default `10m`) | GitHub Copilot OAuth device flow; saves the token file. |

## Environment variables

| Variable | Effect |
| :-- | :-- |
| `GOCODE_THEME` | `light` selects the `rose-pine-dawn` theme, `dark` selects `gocode-dark`, overriding `[tui] active_theme`. Any other value is ignored. Light mode is a theme — the UI never queries the terminal's background. |
| `GOCODE_REDUCED_MOTION`, `REDUCED_MOTION` | Any value except empty/`0`/`false`/`no`/`off` asks `motion = "auto"` to resolve to reduced. |
| `NO_COLOR` | Any non-empty value disables colour; it wins over `CLICOLOR_FORCE`. |
| `CLICOLOR_FORCE` | Any value except empty/`0` forces colour even on a pipe. |
| `TERM` | `dumb` disables colour and escape-sequence rendering. |
| `GOCODE_EVENT`, `GOCODE_SESSION_ID`, `GOCODE_CWD`, `GOCODE_PROJECT_DIR`, `CLAUDE_PROJECT_DIR`, `GOCODE_TOOL_NAME`, `GOCODE_TOOL_INPUT_COMMAND`, `GOCODE_TOOL_INPUT_FILE_PATH` | Set by GoCode **for hook child processes only** (Unreleased) — see [hooks.md](hooks.md#what-a-hook-receives). |
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
| User themes | `<config>\themes\<name>.json` | `<config>/themes/<name>.json` |
| Prompt history | `<config>\history\<sha8-of-workspace>.json` | `<config>/history/<sha8-of-workspace>.json` |
| Sessions | `<data>\sessions\sessions.db` (+ `-wal`, `-shm`) | `<data>/sessions/sessions.db` (+ `-wal`, `-shm`) |
| Logs | `<data>\logs\gocode.log` | `<data>/logs/gocode.log` |

Prompt history (Unreleased) is per workspace: the file name is the first 8 hex chars of
the workspace path's SHA-256, bounded at 200 entries, written `0600`.

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
mode = "normal"                      # normal | yolo (Unreleased); --yolo overrides per run
auto_approve = ["file_read", "code_search"]
deny = []                            # e.g. ["shell_exec"] for a hard boundary
sensitive_patterns = ["*.vault", "config/prod-*"]
dangerous_tools = []                 # extra always-dangerous tool names (Unreleased)

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
smooth_streaming = false             # true opts into paced reveal
active_theme = "gocode-dark"   # built-in or user theme (Unreleased); see themes.md
transparent = false                  # true: terminal background shows through (Unreleased)

# Hook engine (Unreleased) — full contract in hooks.md
[[hooks.PreToolUse]]
name    = "no-shell"
matcher = "^shell_exec$"
command = "echo 'shell is disabled here' >&2; exit 2"

[[hooks.PostToolUse]]
matcher = "^file_(write|patch)$"
command = "echo '{\"context\":\"Run the tests before finishing.\"}'"
timeout = 10

[mcp.servers.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/repo"]
request_timeout_seconds = 60

[mcp.servers.filesystem.env]
LOG_LEVEL = "warn"
```