# Hooks

Hooks are shell commands GoCode runs at 13 lifecycle events — before and after
tool calls, around turns, sessions and compaction, and at permission prompts.
They can block a call, rewrite its input or output, add context the model
sees, or halt a turn. The payload contract is Claude Code-compatible, so
existing hook scripts largely work unchanged.

The engine lives in `internal/hooks` (`events.go`, `config.go`, `input.go`,
`output.go`, `aggregate.go`, `runner.go`); the agent-loop wiring is
`internal/agent/loop_hooks.go` and the session-lifecycle wiring is
`cmd/gocode/session_hooks.go`. Configuration is the `[hooks]` section of
`config.toml` (see [configuration.md](configuration.md#hooks)).

- [Events](#events)
- [Configuring hooks](#configuring-hooks)
- [What a hook receives](#what-a-hook-receives)
- [What a hook may answer](#what-a-hook-may-answer)
- [Exit codes](#exit-codes)
- [Aggregation](#aggregation)
- [Timeouts and abandonment](#timeouts-and-abandonment)
- [How each event acts on the loop](#how-each-event-acts-on-the-loop)
- [Security invariant](#security-invariant)
- [Examples](#examples)

## Events

Exactly 13 events (`hooks.AllEvents`). Names normalize **case-insensitively
with underscores stripped**, so `pre_tool_use`, `PRETOOLUSE` and `PreToolUse`
all resolve to `PreToolUse`. Unknown event names are rejected at config load.

| Event | Kind | When it fires | Event-specific payload |
| :-- | :-- | :-- | :-- |
| `PreToolUse` | tool | Before a tool call, **before the permission gate** | `tool_name`, `tool_input`, `tool_use_id` |
| `PostToolUse` | tool | After a successful tool call | + `tool_response` |
| `PostToolUseFailure` | tool | After a failed call (Go error or error result) | + `tool_response`, `error` |
| `PermissionRequest` | tool | A permission prompt was shown (informational) | `tool_name`, `tool_input`, `tool_use_id` |
| `PermissionResult` | tool | A prompt resolved (informational) | + `decision`: `granted` / `denied` / `cancelled` |
| `SessionStart` | lifecycle | Session created or resumed at startup | `source`: `startup` / `resume` |
| `SessionEnd` | lifecycle | Process exit, every exit path | `reason`: `exit` (2 s total budget) |
| `TurnStart` | lifecycle | Before the provider stream of a model turn | — |
| `TurnEnd` | lifecycle | Turn finished successfully | `last_assistant_message` |
| `StopFailure` | lifecycle | Turn ended with a provider error | — |
| `Interrupt` | lifecycle | Turn was cancelled | — |
| `PreCompact` | lifecycle | Before a compaction summary request; **deny blocks compaction** | `trigger`: `auto` / `manual` |
| `PostCompact` | lifecycle | After a compaction summary was persisted | `trigger`: `auto` / `manual` |

Exactly **one** of `TurnEnd`, `StopFailure`, `Interrupt` fires per finished
model turn, from the turn's single exit funnel. Slash-command turns never
reach the stream and fire no turn hooks. In headless mode (`NonInteractive`),
**turn hooks are skipped; tool and session hooks still fire**.

On **tool events** the `matcher` regex filters by tool name; on lifecycle
events the matcher is ignored.

## Configuring hooks

Each event maps to an ordered list of hooks. Aggregation always follows
**configuration order**, never completion order.

```toml
[[hooks.PreToolUse]]
name    = "guard-shell"          # optional; used in diagnostics
matcher = "^shell_exec$"         # optional regex on the tool name; empty = all
command = "python3 guard.py"     # required; run through the platform shell
timeout = 10                     # optional seconds; 0/unset = 30
```

| Key | Type | Default | Meaning |
| :-- | :-- | :-- | :-- |
| `name` | string | `""` | Diagnostic label; the command is used when unset. |
| `matcher` | string | `""` | Regex tested against the tool name on tool events. Empty matches all. Ignored on lifecycle events. Must compile. |
| `command` | string | — | Required, non-empty. Runs via `bash -c` (unix) or `cmd /C` (Windows) in the payload's `cwd`. |
| `timeout` | int | `0` → 30 | Per-hook deadline in seconds. Negative values are rejected. |

Validation at config load (`hooks.NormalizeConfig`, idempotent): unknown
events and unknown keys are rejected, empty commands are rejected, matchers
must compile, negative timeouts are rejected, unset timeouts become 30, and
hooks are **deduplicated by command within each event** (first occurrence
wins). With no hooks configured the runner is nil and every hook path is a
nil check — zero overhead.

## What a hook receives

**stdin** — one JSON object (`hooks.Input`). Only set fields appear on the
wire; `event` and `hook_event_name` are always present (both carry the
canonical event name).

```json
{
  "event": "PreToolUse",
  "hook_event_name": "PreToolUse",
  "session_id": "…",
  "cwd": "/path/to/workspace",
  "tool_name": "shell_exec",
  "tool_input": {"command": "go test ./..."},
  "tool_use_id": "call_…"
}
```

| Field | Type | Present on |
| :-- | :-- | :-- |
| `event`, `hook_event_name` | string | always |
| `session_id` | string | when a session exists |
| `cwd` | string | workspace root |
| `tool_name` | string | tool events |
| `tool_input` | object | tool events — always a **parsed JSON object**, never a string; invalid/non-object arguments become `{}` |
| `tool_use_id` | string | tool events |
| `tool_response` | string | `PostToolUse`, `PostToolUseFailure` |
| `error` | string | `PostToolUseFailure` |
| `last_assistant_message` | string | `TurnEnd` |
| `decision` | string | `PermissionResult` |
| `source` | string | `SessionStart` |
| `reason` | string | `SessionEnd` |
| `trigger` | string | `PreCompact`, `PostCompact` |

**Environment** — the child gets the scrubbed environment (built-in
credential patterns + `[tools.shell] env_scrub` + every configured
`api_key_env` name, via `tools.ScrubbedEnv`) plus:

| Variable | Value |
| :-- | :-- |
| `GOCODE_EVENT` | canonical event name (always) |
| `GOCODE_SESSION_ID` | session id (when set) |
| `GOCODE_CWD`, `GOCODE_PROJECT_DIR`, `CLAUDE_PROJECT_DIR` | workspace root (when set; the `CLAUDE_*` name is compatibility) |
| `GOCODE_TOOL_NAME` | tool name (tool events) |
| `GOCODE_TOOL_INPUT_COMMAND` | `tool_input.command` when it is a string |
| `GOCODE_TOOL_INPUT_FILE_PATH` | `tool_input.file_path` when it is a string |

Captured stdout and stderr are each capped at **1 MiB** per hook.

## What a hook may answer

A hook answers with its **exit code**, optionally combined with a JSON object
on **stdout**. Only a valid JSON *object* counts; empty output, scalars,
arrays and malformed JSON fall back to exit-code semantics.

```json
{
  "decision": "deny",
  "reason": "writes outside the workspace are not allowed",
  "halt": false,
  "context": "Remember: this repo uses tabs.",
  "updated_input": {"command": "go test ./... -count=1"},
  "updated_output": "replacement tool output",
  "continue": true,
  "stopReason": "…",
  "hookSpecificOutput": {
    "permissionDecision": "allow",
    "permissionDecisionReason": "…",
    "updatedInput": {},
    "updatedToolOutput": "…",
    "additionalContext": "…"
  }
}
```

| Field | Effect |
| :-- | :-- |
| `decision` | `allow`/`approve` → allow; `deny`/`block` → deny; `ask` and unknown words → no opinion (the normal flow decides). Top-level `decision` wins over `hookSpecificOutput.permissionDecision`. |
| `halt` | End the turn after the current batch's results are published. |
| `reason` | Human-readable reason (deny/halt). Falls back to `hookSpecificOutput.permissionDecisionReason`. |
| `continue: false` | Folds to `halt: true`; `stopReason` becomes the reason fallback. |
| `context` | A string **or an array of strings** (newline-joined; non-string elements skipped). Appended to the tool result the model sees. `hookSpecificOutput.additionalContext` is appended after it. |
| `updated_input` | Patch object shallow-merged onto the tool input (falls back to `hookSpecificOutput.updatedInput`). |
| `updated_output` | Whole replacement of the tool output (falls back to `hookSpecificOutput.updatedToolOutput`). |

## Exit codes

| Exit | Meaning |
| :-- | :-- |
| `0` | Success. A valid stdout JSON object is honoured; otherwise no opinion. |
| `2` | **Deny.** stderr is the reason (fallback `blocked by hook`). The exit code is the decision — stdout cannot override it. |
| `49` | **Deny AND halt the turn.** stderr is the reason (fallback `turn halted by hook`). 49 sits outside the generic-error (1–30), sysexits (64–78) and signal (128+) ranges so it cannot be produced by accident. |
| any other non-zero | Non-blocking error: no opinion, a diagnostic is recorded — **unless** stdout carried a valid JSON object, which is then honoured and the exit code ignored. |

Timeout, cancellation, abandonment and start failures also yield **no
opinion**, with a diagnostic.

## Aggregation

Matching hooks run **in parallel**, each under its own deadline; results are
combined in **configuration order** (`hooks.Aggregate`):

- **Decision:** `deny` > `allow` > `none` — the strongest opinion wins.
- **Halt:** sticky OR of every hook's halt.
- **Reason:** newline-joined deny reasons (a halting non-deny hook also
  contributes), in config order.
- **Context:** every hook's context newline-joined; the caller appends it to
  the tool result.
- **`updated_input`:** the original input shallow-merged with every valid
  patch in order (top-level keys only). A patch that is not a JSON object is
  rejected with a diagnostic.
- **`updated_output`:** whole replacement — the **last** hook to set it wins.
- **`stopReason`:** the last non-empty `continue:false` stop reason.
- **Diagnostics:** abnormal outcomes (timeout, abandon, cancel, start
  failure, error exit, invalid matcher/patch) are collected; the engine has
  no logger dependency. The loop surfaces them as one sanitized
  `[hooks] <event>: …` notice; session hooks log them via `slog.Warn`.

## Timeouts and abandonment

- Each hook gets its own deadline: configured `timeout`, else **30 s**
  (`hooks.DefaultTimeout`).
- Past the deadline the runner waits at most the **abandon grace of 1 s**
  (`hooks.AbandonGrace`) for the executor to yield, then counts the hook as
  no opinion. A hook that yields late has no opinion whatever it printed.
- **Parent cancellation gets no grace** — its budget is already spent.
  `SessionEnd` fires under a **2 s total budget** (`hooks.SessionEndBudget`),
  so a per-hook timeout cannot outlive it.
- The executor goroutine writes to a buffered channel, so an abandoned hook
  can finish later without leaking a blocked send.

## How each event acts on the loop

- **`PreToolUse`** runs before `Approval.Prepare`. A **deny** blocks that one
  call with an error result (`Tool call blocked by hook. Reason: …`) — the
  model may retry. A **halt** ends the turn after the batch's results are
  published; every unreached call gets
  `Error: tool call not executed: turn halted by hook.` so history stays
  paired. An **allow** stamps a per-call approval the gate consumes exactly
  once (see below). `updated_input` rewrites the arguments the tool receives;
  `context` is appended to the result text.
- **`PostToolUse` / `PostToolUseFailure`** run after execution, in the single
  transcript boundary. They cannot undo the call: effects are output
  rewrite (`updated_output`), appended context, `Hook feedback: <reason>`
  on a post deny, and halt (turn ends once every result is in the
  transcript). All hook-added text crosses the redaction boundary.
- **`PermissionRequest` / `PermissionResult`** are **informational**: they
  ride the gate's async `PromptObserver` tap (off the requesting goroutine,
  background context so an audit hook still fires after the turn ends).
  Their decisions are ignored — only the prompt itself grants or denies.
- **`TurnStart`** fires before the provider stream; **`TurnEnd` /
  `StopFailure` / `Interrupt`** fire from the exit funnel on a detached
  context (the turn context may already be cancelled or spent). A halt is a
  normal termination: the turn ends cleanly and `TurnEnd` fires.
- **`PreCompact`** wraps the compaction summary request. A **deny blocks
  compaction entirely** (`agent.ErrCompactBlockedByHook`) — there is no
  placeholder fallback. **`PostCompact`** fires after the summary was
  persisted. Both carry `trigger` (`auto` or `manual`).
- **`SessionStart` / `SessionEnd`** fire in every mode (headless parity skips
  turn hooks only). `SessionEnd` is deferred in `main`, so every exit path —
  headless, TUI, plain, or a later startup error — runs it once.

## Security invariant

Hook commands are **trusted user configuration**: they run through the
platform shell without a sandbox. The security boundary is elsewhere and
stays unconditional:

- The **deny-list outranks every hook allow**, in every permission mode.
- **Redaction is unconditional**: tool results, hook-added text, persisted
  arguments and exports are always redacted.
- A hook **allow is per-call only**: the stamp is keyed by `toolCallID`,
  registered via `Approval.GrantHookApproval`, consumed exactly once by the
  gate **after** the config `auto_approve` allowlist and **before** keyed
  session grants, and cleared at every turn boundary. It can never approve a
  second call or outlive its turn, and never bypasses yolo's dangerous-call
  refusal.
- Hook children **never inherit provider API keys by default**: the
  credential scrub applies to hook environments exactly as it does to
  `shell_exec` and MCP children.
- Permission hooks are informational; they cannot grant or deny anything.

See [SECURITY.md](SECURITY.md) for the full threat model.

## Examples

```toml
# Hard-block shell_exec with exit 2 (deny); stderr becomes the reason.
[[hooks.PreToolUse]]
name    = "no-shell"
matcher = "^shell_exec$"
command = "echo 'shell is disabled on this machine' >&2; exit 2"

# Deny AND halt the turn with exit 49.
[[hooks.PreToolUse]]
matcher = "^file_(write|patch)$"
command = "grep -q /etc/passwd && { echo 'protected path' >&2; exit 49; }; exit 0"

# Approve read-only searches without a prompt (per-call; deny-list still wins).
[[hooks.PreToolUse]]
matcher = "^code_search$"
command = "echo '{\"decision\":\"allow\"}'"

# Add context after any file mutation.
[[hooks.PostToolUse]]
matcher = "^file_(write|patch)$"
command = "echo '{\"context\":\"Run go test ./... before finishing.\"}'"

# Rewrite a tool's output wholesale (last hook to set it wins).
[[hooks.PostToolUse]]
matcher = "^shell_exec$"
command = "summarize-output.sh"
timeout = 15

# Audit every finished turn (Claude Code-compatible stdin JSON).
[[hooks.TurnEnd]]
command = "cat >> \"$HOME/gocode-turns.jsonl\""

# Block automatic compaction during a release freeze.
[[hooks.PreCompact]]
command = "test -f .release-freeze && exit 2; exit 0"

# Session lifecycle. SessionEnd has a 2s total budget — keep it fast.
[[hooks.SessionStart]]
command = "echo \"start $(date -Is)\" >> \"$HOME/gocode-sessions.log\""

[[hooks.SessionEnd]]
command = "cleanup.sh"
timeout = 2
```

A minimal Claude Code-style script reads stdin and answers on stdout:

```bash
#!/usr/bin/env bash
payload=$(cat)
tool=$(printf '%s' "$payload" | jq -r '.tool_name // empty')
if [ "$tool" = "shell_exec" ]; then
  echo '{"decision":"deny","reason":"shell requires manual approval here"}'
fi
exit 0
```