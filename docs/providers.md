# Providers

GoCode talks to 20 built-in providers plus any number of custom
OpenAI-compatible endpoints. All of them expose the same three operations to the
agent — list models, stream a chat completion, report usage when available — so
switching providers never changes how tools, approvals, or sessions behave.

- [How a provider is selected](#how-a-provider-is-selected)
- [The 20 built-in providers](#the-20-built-in-providers)
- [Choosing and switching models](#choosing-and-switching-models)
- [Usage reporting and `stream_usage`](#usage-reporting)
- [Custom OpenAI-compatible endpoints](#custom-openai-compatible-endpoints)
- [Authentication flows](#authentication-flows)
- [Setup guides](#setup-guides)
- [Retries and timeouts](#retries-and-timeouts)
- [Troubleshooting](#troubleshooting)

## How a provider is selected

At startup GoCode registers every provider, then activates one:

1. `--provider <name>` wins.
2. Otherwise a resumed session's stored provider wins.
3. Otherwise `[provider] default` is used (built-in default: `ollama`).

An unknown name is a startup error that lists every registered provider.
`/provider <name>` performs the same switch in-session.

There are three implementation dialects behind the names:

| Dialect | Used by | What it speaks |
| :-- | :-- | :-- |
| OpenAI-compatible gateway proxy | 19 built-ins + every custom endpoint | `POST {base_url}/chat/completions`, `GET {base_url}/models`, SSE |
| Native Anthropic | `anthropic` | `POST {base_url}/messages`, SSE, `x-api-key` + `anthropic-version` |
| GitHub Copilot | `copilot` | OpenAI-compatible chat at the API base returned by a GitHub token exchange |
| Ollama | `ollama` | `POST /api/chat` (NDJSON), `GET /api/tags` |

The distinction matters for configuration: Anthropic still uses the four gateway
keys under `[provider.anthropic]`, Copilot has its own OAuth/editor settings, and
Ollama has a host plus an optional default model. `[provider] stream_usage`
applies only to the gateway proxy.

## The 22 built-in providers

### OpenAI-compatible gateways (19)

| Provider | `--provider` | Default base URL | Default model | API key variable |
| :-- | :-- | :-- | :-- | :-- |
| OmniRoute (local) | `omniroute` | `http://127.0.0.1:20128/v1` | `auto` | — |
| OpenAI | `openai` | `https://api.openai.com/v1` | `gpt-4o` | `OPENAI_API_KEY` |
| Google Gemini | `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | `gemini-2.5-flash` | `GEMINI_API_KEY` |
| Groq | `groq` | `https://api.groq.com/openai/v1` | `llama-3.3-70b-versatile` | `GROQ_API_KEY` |
| OpenRouter | `openrouter` | `https://openrouter.ai/api/v1` | `anthropic/claude-sonnet-4.5` | `OPENROUTER_API_KEY` |
| Qwen (DashScope) | `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen-max` | `DASHSCOPE_API_KEY` |
| Kimi (Moonshot) | `kimi` | `https://api.moonshot.cn/v1` | `moonshot-v1-8k` | `MOONSHOT_API_KEY` |
| Hermes Agent (local) | `hermes` | `http://127.0.0.1:8642/v1` | `hermes-agent` | `HERMES_API_SERVER_KEY` |
| xAI | `xai` | `https://api.x.ai/v1` | `grok-code-fast-1` | `XAI_API_KEY` |
| Mistral | `mistral` | `https://api.mistral.ai/v1` | `mistral-large-latest` | `MISTRAL_API_KEY` |
| MiniMax | `minimax` | `https://api.minimax.io/v1` | `MiniMax-M2.5` | `MINIMAX_API_KEY` |
| DeepSeek | `deepseek` | `https://api.deepseek.com/v1` | `deepseek-chat` | `DEEPSEEK_API_KEY` |
| Together | `together` | `https://api.together.xyz/v1` | `Qwen/Qwen3-Coder-480B-A35B-Instruct` | `TOGETHER_API_KEY` |
| Fireworks | `fireworks` | `https://api.fireworks.ai/inference/v1` | `accounts/fireworks/models/kimi-k2-thinking` | `FIREWORKS_API_KEY` |
| Cerebras | `cerebras` | `https://api.cerebras.ai/v1` | `qwen-3-coder-480b` | `CEREBRAS_API_KEY` |
| Zhipu (GLM) | `zhipu` | `https://open.bigmodel.cn/api/paas/v4` | `glm-4.6` | `ZHIPU_API_KEY` |
| NVIDIA NIM | `nvidia` | `https://integrate.api.nvidia.com/v1` | `qwen/qwen3-coder-480b-a35b-instruct` | `NVIDIA_API_KEY` |
| Mistral Vibe | `mistral-vibe` | `https://api.mistral.ai/v1` | `mistral-large-4-0` | `MISTRAL_API_KEY` |
| OpenCode Go | `opencode-go` | `https://opencode.ai/zen/go/v1` | `deepseek-v4-flash` | `OPENCODE_API_KEY` |

Notes:

- **OmniRoute and Hermes are local.** Their defaults point at loopback ports, and
  OmniRoute ships with no key variable at all. Plain HTTP is accepted only for
  loopback, which is why they work without TLS.
- **OpenRouter** additionally sends `HTTP-Referer` and `X-Title` headers.
- **OpenCode Go** is the subscription tier of OpenCode's gateway
  (`opencode.ai/zen/go`); it speaks the same OpenAI-compatible surface as the
  pay-as-you-go Zen tier and serves a curated coding subset. Use
  `OPENCODE_API_KEY`; override `base_url` in `[provider.opencode_go]` if Zen's
  path changes.
- **Mistral Vibe** is Mistral's coding model on la Plateforme (Vibe defaults to
  Mistral Large 4); it shares `MISTRAL_API_KEY` with `mistral`. Set
  `default_model` to `devstral-medium-latest` or `mistral-medium-latest` for the
  coding-tuned variants.
- Every key variable can be renamed per provider with `api_key_env`, and an
  inline `api_key` is accepted but warned about.

### Native dialects (3)

| Provider | `--provider` | Endpoint | Default model | Credential |
| :-- | :-- | :-- | :-- | :-- |
| Anthropic Claude | `anthropic` | `https://api.anthropic.com/v1/messages` | `claude-sonnet-4-20250514` | `ANTHROPIC_API_KEY` |
| GitHub Copilot | `copilot` | API base from token exchange (normally `https://api.githubcopilot.com`) | `gpt-4.1` | GitHub OAuth token (`GITHUB_COPILOT_TOKEN` or `gocode auth copilot`) |
| Ollama (local) | `ollama` | `http://127.0.0.1:11434` | first installed model, else `codellama` | none |

- **Anthropic** speaks the Messages API directly: the system prompt is sent as the
  top-level `system` field, tool results are `tool_result` blocks, and
  `max_tokens` is 8192 (4096 for `claude-3-opus*`). Its model list is built in
  (five Claude ids) rather than fetched, so `/model` validation works offline.
  Pointing `base_url` at a proxy is supported as long as it speaks Messages.
- **Copilot** always exchanges the OAuth token for a short-lived JWT and an API
  base; `[provider.copilot]` has no `base_url` and no `api_key`. Requests carry
  `Editor-Version`, `Editor-Plugin-Version`, `User-Agent` and
  `Copilot-Integration-Id` headers, without which GitHub rejects them.
- **Ollama** is a stdlib-only client with no upstream module. When
  `default_model` is empty at startup, GoCode asks `/api/tags` (3-second bound)
  and takes the first model.

## Choosing and switching models

| How | Scope | Behavior |
| :-- | :-- | :-- |
| `--provider <name>` / `--model <id>` | one process | Highest precedence at startup. `--model` is **not** validated, so it can name a model the list endpoint does not advertise. |
| `/providers` | session | Lists every registered provider and the active provider's models. |
| `/provider <name>` | session | Switches provider (errors with the available list if unknown). The model is not changed; follow with `/model`. |
| `/model` | session | Shows the active model and the provider's models. |
| `/model <id>` | session | **Fail-closed**: it asks the provider for its model list and refuses if the list cannot be fetched, is empty, or does not contain the id. |
| `Ctrl+L` (TUI) | session | Fuzzy model picker across all providers. Refreshes on open (2 s per provider, 3 s overall) while keeping the previous list on screen; a selection sends `/provider` then `/model`. |

Because `/model` validates against the live list, a gateway that does not
implement `GET /models` cannot be switched to in-session; set `default_model` in
config and start with `--model` instead. The same applies to the picker, which
silently omits providers whose model list fails.

Provider changes are persisted with the session, so a resumed session reopens on
the provider and model it was using.

## Usage reporting

Usage parsing is always on: when a provider reports prompt/completion tokens,
they are recorded for the turn and used for context accounting (the larger of the
provider count and the local estimate wins). What is optional is *asking* for it.

Most OpenAI-compatible gateways omit usage from a stream unless the request
includes:

```json
{"stream_options": {"include_usage": true}}
```

Set `[provider] stream_usage = true` to send that field on every gateway request
(built-in and custom). It is off by default because strict gateways reject
unknown body fields and would fail the whole request. Native Anthropic (cumulative
`message_start` / `message_delta` usage), Ollama (`prompt_eval_count` /
`eval_count` on the done chunk) and Copilot (usage on the final chunk when the
gateway sends it) are unaffected by this switch.

When a provider never reports usage, the TUI, plain summary and headless
`turn_done` fall back to local estimates and mark the counts as not
provider-reported.

## Custom OpenAI-compatible endpoints

```bash
gocode provider add my-gateway \
  --base-url https://gateway.internal/v1 \
  --api-key-env MY_GATEWAY_KEY \
  --model my-model

export MY_GATEWAY_KEY="your-key"
gocode --provider my-gateway

gocode provider list
gocode provider remove my-gateway
```

`provider add` is an upsert: re-running it with the same name replaces the entry.
Only the environment-variable **name** is stored — the key itself never is.
`--base-url` and `--model` are required.

Validation rules:

| Field | Rule |
| :-- | :-- |
| Name | Lowercased automatically; must match `[a-z][a-z0-9_-]{0,31}`. Built-in names are refused. |
| `base_url` | Absolute `http`/`https` URL with a host; no embedded credentials, query or fragment; HTTPS unless `localhost`, `*.localhost` or a loopback IP; trailing slash trimmed. |
| `default_model` | Required, non-empty. |
| `api_key_env` | Optional; if set, must match `[A-Za-z_][A-Za-z0-9_]*`. |

`provider remove` refuses to delete the provider named by `[provider] default` —
change the default first. A hand-written `[provider.custom.<name>]` section must
use a lowercase name or the config fails to load; a custom entry that shares a
built-in name takes precedence over the built-in (validated endpoints only).

## Authentication flows

### GitHub Copilot: OAuth device flow

`gocode auth copilot` requires an OAuth App client id:

```bash
gocode auth copilot --client-id <your-oauth-app-client-id>
```

It requests a device code (scope `read:user`), prints the verification URL and
user code, and polls at the interval GitHub returns (5 s if unspecified).
`authorization_pending` keeps waiting; `slow_down` adds 5 s; `expired_token` and
`access_denied` fail with an explanatory error. `--timeout` bounds the wait
(default 10 minutes). On success the OAuth token is written to
`<config dir>/copilot_token` with `0600` permissions.

The provider prefers `$GITHUB_COPILOT_TOKEN` (or the configured
`oauth_token_env`) and falls back to that file. It then exchanges the OAuth token
at `https://api.github.com/copilot_internal/v2/token` for a short-lived JWT plus
the API base URL, and caches both until 60 seconds before expiry. A rotated OAuth
token triggers a fresh exchange. On `401`/`403` the cached JWT is invalidated and
the streaming request retries **once** with a new token, so a mid-session expiry
recovers instead of failing every remaining turn. The exchange's API base is
untrusted input: it must be HTTPS, port-free, without credentials/query/fragment,
on `api.githubcopilot.com`, `*.githubcopilot.com`, `api.github.com` or
`*.github.com`, otherwise GoCode refuses to send the token there.

`gocode doctor` reports whether a token exists and whether the exchange succeeds.

### Ollama: optional ed25519 signing

A local Ollama server needs no credentials. Two variables change that:

- `OLLAMA_HOST` is resolved when `[provider.ollama] host` is empty. A bare host
  means HTTP, except `ollama.com` which means HTTPS on 443; an explicit scheme
  selects ports 80/443; anything after the first `/` becomes the URL path; an
  unparsable value falls back to `127.0.0.1:11434`.
- `OLLAMA_AUTH` enables request signing. Unset or empty means off; an unparsable
  non-empty value counts as on. Requests to `ollama.com` are always signed.

Signing reads `~/.ollama/id_ed25519`, signs `METHOD,PATH?ts=<unix>` and sends
`Authorization: <base64 public key>:<base64 signature>` with the same `ts` as a
query parameter — the scheme Ollama's own client uses. Hosted Ollama therefore
works without pasting a key, as long as that key file exists.

## Setup guides

### GitHub Copilot

```bash
# 1. Authorize once (client id from your GitHub OAuth App)
gocode auth copilot --client-id <your-oauth-app-client-id>

# 2. Run
gocode --provider copilot
```

```toml
[provider]
default = "copilot"

[provider.copilot]
oauth_token_env = "GITHUB_COPILOT_TOKEN"   # optional override; the saved file is used otherwise
default_model = "gpt-4.1"
editor_version = "vscode/1.111.0"
editor_plugin_version = "copilot-chat/0.40.0"
```

```bash
gocode doctor          # ✓ copilot reachable (default: gpt-4.1, N models)
```

The account needs an active Copilot subscription; the exchange returns an empty
token or `403` otherwise. If GitHub changes its editor requirements, update
`editor_version` / `editor_plugin_version` rather than patching code.

### Hermes Agent

Hermes Agent exposes an OpenAI-compatible API on loopback. GoCode's `hermes`
provider defaults to it:

```toml
[provider]
default = "hermes"

[provider.hermes]
base_url = "http://127.0.0.1:8642/v1"
api_key_env = "HERMES_API_SERVER_KEY"
default_model = "hermes-agent"
```

```bash
export HERMES_API_SERVER_KEY="your-server-key"
gocode --provider hermes
```

Loopback HTTP is allowed by the URL validator. If your server listens on a
different port or path, change `base_url` — it must still end at the API root
(GoCode appends `/chat/completions` and `/models`). Use `/providers` or `/model`
to see which model ids the server advertises.

### llama.cpp (`llama-server`)

`llama-server` serves an OpenAI-compatible API under `/v1` (default port 8080).

```bash
# Terminal 1
llama-server -m model.gguf --host 127.0.0.1 --port 8080

# Terminal 2
gocode provider add llamacpp \
  --base-url http://127.0.0.1:8080/v1 \
  --model local-model

gocode --provider llamacpp
```

No API key is needed. If you start the server with `--api-key`, add
`--api-key-env LLAMACPP_KEY` and export it; GoCode sends it as a bearer token.

Two practical notes:

- `--model` at launch is not validated, which is useful when the server reports a
  path-like id. `/model <id>` does validate against `GET /v1/models`.
- An unknown model name gets the registry's conservative 64 k context window. If
  the server runs a smaller context (`-c 8192`), set `[tools] max_context_tokens`
  to match so truncation and auto-compaction trigger at the right point.

### OpenCode Zen

Zen exposes an OpenAI-compatible Chat Completions surface under
`https://opencode.ai/zen/v1`:

```bash
gocode provider add opencode-zen \
  --base-url https://opencode.ai/zen/v1 \
  --api-key-env OPENCODE_API_KEY \
  --model deepseek-v4-flash

export OPENCODE_API_KEY="your-key"
gocode --provider opencode-zen
```

Only models Zen serves on the **Chat Completions** endpoint work through a custom
gateway, because GoCode's proxy always posts to `{base_url}/chat/completions`.
Zen also exposes `/responses` and `/messages` endpoints for some models; those
are not reachable through the gateway proxy. Check the endpoint column in Zen's
model catalog and pick a chat-completions model, then use `/model` to switch among
the ones `GET /v1/models` returns.

## Retries and timeouts

Every HTTP provider uses one retry transport:

| Behavior | Value |
| :-- | :-- |
| Retried methods | `GET`, `HEAD`, `PUT`, `DELETE`, `OPTIONS`, `TRACE` only |
| Retried statuses | `429`, `500`, `502`, `503`, `504` |
| Retries | 3 |
| Backoff | 500 ms doubling, with 10–20% jitter, capped at 30 s |
| `Retry-After` | Honored for either seconds or an HTTP date, clamped to 30 s |
| POST | **Never retried.** A failed chat request may already have been accepted and billed upstream. |

Per-client ceilings: 10 minutes for a whole streamed turn, 30 s dial timeout,
15 s TLS handshake, and 30 s for model listing (`GET /models`, `/api/tags`).
Ollama uses the same policy, with an 8 MiB NDJSON line cap so long tool
arguments are not truncated. Copilot adds its one-shot token re-exchange, which is
not a transport retry.

A non-SSE `200` response (a proxy returning HTML, or a gateway ignoring
`stream: true`) is rejected explicitly instead of silently producing an empty
answer.

## Troubleshooting

| Symptom | Likely cause | Fix |
| :-- | :-- | :-- |
| `unknown provider "x", available: [...]` | Typo, or a custom provider that failed to load | `gocode provider list` |
| `no response received from provider` notice | Missing/invalid key, or a model that returned nothing | Check the key variable, run `gocode doctor`, try `/provider` |
| `model "x" is not available on y` | `/model` validation against the live list | Use an advertised id, or set `default_model` and launch with `--model` |
| `provider returned a non-SSE 200 response` | Endpoint is not a streaming chat API, or a proxy rewrote the response | Point `base_url` at the API root, not a web UI |
| `base URL must use HTTPS unless it targets localhost` | Plain HTTP to a non-loopback host | Use HTTPS, or a loopback tunnel |
| `copilot rejected the GitHub token` | Expired token or no Copilot subscription | Re-run `gocode auth copilot` |
| `token exchange returned an untrusted API base URL` | Exchange response pointed outside GitHub-owned hosts | Refused by design; check the network/proxy |
| `[Context overflow …]` notices | Prompt exceeded the model window | Let auto-compaction run, lower `keep_recent_tokens`, or raise `max_context_tokens` only if the model really supports it |
| Gateway rejects requests after enabling `stream_usage` | Strict gateway refusing `stream_options` | Set `stream_usage = false` |
