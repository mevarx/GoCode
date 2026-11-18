# oh-my-pi (omp) — Agent Harness: Reverse-Engineered from Source

**Target:** `C:\Users\shree\AppData\Local\hermes\cache\scratch\oh-my-pi` @ `840cc4b6bc94581509215d8db30fb04ad0ab7fea` (main, ~v18.22.2)
**Method:** read-only static analysis. No code executed. Claims tagged `[src]` (read in source), `[docs]` (docs claim, not fully verified), `[inf]` (inference from code structure).

---

## 0. Architecture overview

Five layers, bottom-up:

```
┌──────────────────────────────────────────────────────────────────────────┐
│ packages/coding-agent   CLI app: AgentSession, tools, prompts, MCP,      │
│                         extensions/hooks, advisor, plan-mode, IRC, TUI   │
├──────────────────────────────────────────────────────────────────────────┤
│ packages/agent          provider-agnostic loop: agentLoop/runLoopBody,   │
│  (pi-agent-core)        compaction, append-only context, speculation,    │
│                         live steering, output budget, replay policy      │
├──────────────────────────────────────────────────────────────────────────┤
│ packages/ai             multi-provider client: streamSimple + dialects,  │
│  (pi-ai)                providers/*, registry/, usage/, error/, auth/    │
├──────────────────────────────────────────────────────────────────────────┤
│ packages/tui            terminal renderer (owned by another worker)      │
├──────────────────────────────────────────────────────────────────────────┤
│ crates/*  (Rust, napi)  shell/PTY, grep/glob, edit, ast, vfs, diff, ...   │
└──────────────────────────────────────────────────────────────────────────┘
```

### ASCII data flow — one user prompt

```
 user types prompt
        │
        ▼
 AgentSession.prompt()  ──► agent.prompt()  ──► agentLoop(prompts, ctx, cfg, signal)
                                                        │
                                    ┌───────────────────┴────────────────────┐
                                    │ runLoopBody  (agent-loop.ts:1253)       │
                                    │  outer while(true)                      │
                                    │   inner while(hasMoreToolCalls ||       │
                                    │               pendingMessages.length)   │
                                    └───────────────────┬────────────────────┘
                                                        │
       ┌────────────────────────────────────────────────┼──────────────────────────────┐
       │ 1. drain steering/follow-ups  (getSteeringMessages, getFollowUpMessages)       │
       │ 2. prepareProviderCall  ── transformContext → convertToLlm → normalize →       │
       │                           appendOnlyContext.build → normalizeTools →           │
       │                           transformProviderContext → sentToolDefinitions       │
       │ 3. beforeModelCall gate  (may stop the run: AgentPreModelCallResult)           │
       │ 4. streamAssistantResponse                                                     │
       │      streamSimple(model, ctx, opts)  ──► provider adapter                      │
       │         │  AssistantMessageEvent: start / text_* / thinking_* /                │
       │         │                         toolcall_* / image_end / done | error        │
       │         ├─► AgentEvent.message_start / message_update (snapshot)               │
       │         └─► on done: message_end + transformAssistantMessage                   │
       │ 5. prepareToolCallDispatch (ids, intent, schema validate, beforeToolCall)      │
       │ 6. executeToolCalls  ── concurrency scheduler (exclusive/shared) ──►           │
       │        tool.execute(id, args, signal, onUpdate, toolContext)                   │
       │        emits tool_execution_start/update/end + ToolResultMessage               │
       │ 7. emitTurnEnd → onTurnEnd hook (advisor, compaction checks, title, ...)       │
       │ 8. loop: hasMoreToolCalls = runnableStop && toolCalls.length>0                 │
       └────────────────────────────────────────────────────────────────────────────────┘
                                                        │
                                     agent_end { messages, telemetry?, coverage? }
```

Event vocabulary (three nested levels) — see §Q1.4.

---

## Q1. Turn lifecycle — one prompt end-to-end

### Q1.1 Entry points

| Layer | Symbol | File |
|---|---|---|
| App | `AgentSession.prompt(text, options)` | `packages/coding-agent/src/session/agent-session.ts:9090` |
| Runtime | `Agent.#runLoop(messages?, options?, continuationSignal?)` | `packages/agent/src/agent.ts:1896` |
| Core | `agentLoop(prompts, context, config, signal, streamFn)` | `packages/agent/src/agent-loop.ts:655` |
| Core | `runLoopBody(...)` — the actual state machine | `packages/agent/src/agent-loop.ts:1253` |

`agentLoop` is a thin wrapper: it clones the context with the new prompts appended, pushes
`agent_start`, then awaits `runLoop` → `runLoopBody` (`agent-loop.ts:664-681`).
`agentLoopContinue` (same file, :737) skips the prompt append and is used for retries/compaction
continuations; it refuses to continue from an assistant tail **unless** that tail has unpaired
runnable tool calls (`unpairedToolCallTail`, :716 — the "tool replay" resume path).

The `Agent` class consumes the returned `EventStream<AgentEvent, AgentMessage[]>` in a
`for await` loop and mirrors state (`streamMessage`, `pendingToolCalls`, `error`, `isStreaming`)
before re-emitting to subscribers (`agent.ts:2145-2219`).

### Q1.2 The two nested loops

```
runLoopBody
  ├─ deadline arm (setTimeout in ≤2^31-1 ms slices)         :1265-1279
  ├─ resume-tail tool replay (unpaired tool calls)          :1348-1385
  └─ outer while(true)                                       :1388
       └─ inner while(hasMoreToolCalls || pendingMessages)   :1392
            1. deadline check                                :1393
            2. pause gate park (agentPauseGate)              :1401
            3. drain pendingMessages into context            :1408-1416
            4. syncContextBeforeModelCall (systemPrompt+tools refresh) :1429
            5. getToolChoice → soft/hard tool directive      :1434-1458
            6. prepareProviderCall                           :1460
            7. openLiveSteering                              :1462
            8. beforeModelCall gate → may stop the run       :1463,1477
            9. turn_start                                    :1515-1518
           10. streamAssistantResponse                       :1526
           11. error/aborted → synthesize aborted results, end :1633-1675
           12. hasMoreToolCalls = runnableStop && calls>0     :1700-1701
           13. soft-requirement non-compliance gate           :1712-1753
           14. executeToolCalls                              :1755
           15. emitTurnEnd + onTurnEnd hook                   :1847
           16. re-dequeue steering/asides/follow-ups          :1865-1893
       └─ onBeforeYield, late steering + stop asides + follow-ups, else break :1902-1922
```

`runnableStop` is deliberately wider than `toolUse`: both `"toolUse"` **and** `"stop"` run tools,
because Anthropic Opus routinely emits tool calls under `end_turn`; only `"length"` is excluded
(truncated args) — see the long comment at `agent-loop.ts:1677-1689`.

### Q1.3 Where the system prompt and tool list are refreshed

`Agent` supplies `syncContextBeforeModelCall`, which runs before every model call:
it awaits `#runBeforeModelCallHooks`, then **overwrites** `context.systemPrompt` from
`#state.systemPrompt` and `context.tools` from `#toolsForModel(model)`
(`agent.ts:2054-2062`). That is the seam where late-bound prompt content (todo reminders, plan
mode, MCP instructions, dynamic tool mounting) reaches the wire without rebuilding the loop.

### Q1.4 Event vocabulary (three nested levels)

**(a) Provider → agent: `AssistantMessageEvent`** (`packages/ai/src/types.ts`, [docs] `docs/provider-streaming-internals.md:18-28`):
`start`, `text_start|text_delta|text_end`, `thinking_start|thinking_delta|thinking_end`,
`toolcall_start|toolcall_delta|toolcall_end`, `image_end`, terminal `done` (`reason: "stop"|"length"|"toolUse"`)
or `error` (`reason: "aborted"|"error"`).

**(b) Agent → host: `AgentEvent`** (`packages/agent/src/types.ts:1327-1356`) [src]:

```ts
agent_start { resumedToolCallIds? }
agent_end   { messages, telemetry?, coverage? }
turn_start
turn_end    { message, toolResults }
message_start { message }            // user, assistant, toolResult
message_update { message, assistantMessageEvent }   // assistant streaming only
message_end { message }
tool_execution_start  { toolCallId, toolName, args, intent? }
tool_execution_update { toolCallId, toolName, args, partialResult }
tool_stream_update    { toolCallId, toolName, update }   // tool-owned arg stream
tool_execution_end    { toolCallId, toolName, result, isError? }
```

**(c) App → UI: `AgentSessionEvent`** (`packages/coding-agent/src/session/agent-session-events.ts`) —
mirrors (b) plus session concerns (queue changes, compaction, retry, advisor notes). The TUI
worker owns rendering; the harness-side contract is that `message_update.message` snapshots are
**read-only** (see Q2.4).

### Q1.5 Termination conditions

The run ends when, after a turn with no runnable tool calls, the outer loop's stop-boundary drain
finds no late steering, no `"stop"`-boundary asides, and no follow-up messages
(`agent-loop.ts:1912-1922`). Earlier exits: deadline exceeded (`:1393,1853,1896`), external abort
(`endAbortedBeforeModel`, :1289), `beforeModelCall` gate stop (:1477), a terminal tool-result abort
reason (`TERMINAL_TOOL_RESULT_ABORT_REASON`, :1806), or a thrown error.

---

## Q2. Streaming — normalization, partial JSON, abort, live steering, TTSR

### Q2.1 Provider dispatch (`packages/ai`)

`streamSimple(model, context, options)` (`packages/ai/src/stream.ts:1217`) is the single entry point used
by the loop (`agent-loop.ts:2110`: `const streamFunction = streamFn || streamSimple`). It:

1. attaches an inference session id (`withInferenceSessionId`, :1222);
2. strips unverified thinking controls when no effort was requested (`withoutUnverifiedThinking`, :1226);
3. if the model needs it, applies a glyph codec around context and exec handlers (:1230-1241);
4. delegates to `streamSimpleRequest` (:1264), which resolves sampling-param compatibility,
   transport fetch, and — when the API key is a **resolver** — wraps the attempt in an auth-retry loop
   that buffers events until the first *replay-unsafe* event, so a retryable 401 can be retried without
   duplicating output (:1274-1357).

Built-in API ids are enumerated in `packages/ai/src/api-registry.ts:19-38` (18 ids: `openai-completions`,
`openai-responses`, `openrouter`, `openai-codex-responses`, `azure-openai-responses`, `anthropic-messages`,
`bedrock-converse-stream`, `google-generative-ai`, `google-gemini-cli`, `google-vertex`, `kiro-api`,
`ollama-chat`, `cursor-agent`, `factory-droid-agent`, `gitlab-duo-agent`, `devin-agent`,
`antigravity-acp`, `apple-foundation-models`). Everything else is an **extension-registered custom API**
(`registerCustomApi`, :81) and may not shadow a built-in name (:72).

### Q2.2 Unified event contract

`AssistantMessageEvent` (`packages/ai/src/types.ts:1798-1821`) [src]:

```ts
start
text_start|text_delta|text_end             (contentIndex, delta/content, partial)
thinking_start|thinking_delta|thinking_end
toolcall_start|toolcall_delta|toolcall_end  (toolcall_end carries the final ToolCall)
image_end                                   (complete ImageContent)
done  { reason: "stop"|"length"|"toolUse", message }
error { reason: "aborted"|"error", error: AssistantMessage }
```

Every event carries `partial: AssistantMessage` — the mutable in-flight message the provider keeps
appending to. `AssistantMessageEventStream` (utils/event-stream.ts) exposes async iteration plus
`result()`; `done`/`error` resolve `result()`, `fail()` rejects it.

### Q2.3 Representative provider: Anthropic

`packages/ai/src/providers/anthropic.ts` translates the Messages SSE stream:

* `content_block_start` opens a block in an `openBlocks` map keyed by wire index, mapping to a
  `contentIndex` in the assistant message; `ignored` blocks (e.g. server tool) are tracked but skipped
  (:2960-3110).
* `content_block_delta` is typed by `delta.type` (:3111-3197): `text_delta` appends and emits
  `text_delta`; `thinking_delta` likewise; `input_json_delta` appends to a **symbol-keyed buffer**
  (`block[kStreamingPartialJson]`), reparses through `parseStreamingJsonThrottled`, and emits
  `toolcall_delta`; `signature_delta` updates `thinkingSignature` with **no event**.
* `content_block_stop` emits the matching `*_end` (reparsing arguments once more at the boundary).
* Envelope anomalies (delta for an unopened index, duplicate `content_block_start`, events after a
  terminal stop) are reported via `reportAnthropicEnvelopeAnomaly` and dropped rather than thrown — a
  deliberate leniency layer for flaky compatible endpoints.

`streamedReplayUnsafeContent` latches on the first text/thinking/tool-arg delta (:3134, :3147, :3169):
that is the flag provider-owned retries consult to refuse replaying a partially delivered turn.

### Q2.4 Partial tool-argument JSON

`parseStreamingJson` / `parseStreamingJsonThrottled` (`packages/utils/src/json-parse.ts`; described in
`docs/provider-streaming-internals.md:202-213`): try `JSON.parse`, fall back to a repairing `RelaxedJson`
parser, else return `{}`. The throttled variant reparses only when the buffer has grown by
`max(STREAMING_JSON_PARSE_MIN_GROWTH, len >> 5)` — a geometric gate (documented floor 256 code units) so
100 KB of arguments is not reparsed on every token. The final parse at `toolcall_end` is unconditional
[src: call site `anthropic.ts:3171`].

Google is the exception: function calls arrive as **structured objects**, so the adapter emits one
synthetic `toolcall_delta` containing `JSON.stringify(arguments)` [docs].

### Q2.5 Watchdogs and stall handling

`packages/ai/src/utils/idle-iterator.ts` defaults first-event and idle budgets to **300,000 ms**; caller
options `streamFirstEventTimeoutMs` / `streamIdleTimeoutMs` win, `0` disables. Env: generic
`PI_STREAM_IDLE_TIMEOUT_MS` / `PI_STREAM_FIRST_EVENT_TIMEOUT_MS`, OpenAI-family
`PI_OPENAI_STREAM_IDLE_TIMEOUT_MS` / `..._FIRST_EVENT_TIMEOUT_MS`. Providers can declare
server-requested local work with `trackLocalWork()` so a long local step is not read as a stall
[docs: provider-streaming-internals.md:263-276].

### Q2.6 Abort layering (three signals, one listener)

`streamAssistantResponse` builds signals in stages (`agent-loop.ts:2118-2140`):

```
loop signal ──┐
              ├─► requestSignal ──┐
harmony abort ┘                   ├─► finalRequestSignal ──► provider options.signal
promptToolAbort (owned dialect) ──┘
```

* `requestSignal` = external/loop abort + harmony-leak abort.
* `finalRequestSignal` adds the owned-dialect abort, which fires when the model fabricates a
  `<tool_response>` block (`wrapInbandToolStream` callback, :2271-2277) — this cancels the HTTP request
  **without** tripping the loop's external-abort bookkeeping.
* The event loop registers **one** abort listener for the whole stream and races every
  `iterator.next()` against a single promise (`abortRacePromise`, :2338-2365) — no per-event listener
  churn.
* On abort: `finishAbortedStream` calls `iterator.return()`, discards speculation, and synthesizes an
  aborted assistant message from the latest partial (`emitAbortedAssistantMessage`, :2875).

### Q2.7 Live steering (provider-side injection)

`LiveSteeringChannel` (`packages/agent/src/live-steering.ts`) is the agent half of a provider capability
(OpenAI Responses `response.steer`) that can splice a user message into the **response currently being
streamed**. Three methods:

* `wait(signal)` — resolves when steering is queued; never consumes. Once anything is `deferred` it stops
  waiting, so later input cannot overtake earlier input.
* `claim(signal)` — dequeues a batch, converts it to provider `UserMessage[]` via
  `transformContext → convertToLlm → normalizeMessagesForProvider`, returns `{messages, accept, reject}`.
  If the conversion yields anything but plain text user messages (e.g. images, which a
  provider-context transform might rewrite), the claim is **deferred** instead (:70-73).
* `accept()` → pushes into `accepted`; `reject()` → `deferred`.

The loop records both lists after each response (`agent-loop.ts:1602-1606`), marks accepted user messages
`liveSteered = true` (:1869), and injects `deferred` at the next boundary. `openLiveSteering` (:1994)
refuses to open a channel when the model uses an **owned dialect** (in-band tool calling has no
provider-side steering).

### Q2.8 Time-traveling stream rules (TTSR) — harness-side lifecycle

A rule can interrupt a response *while it is streaming* because the violation is visible in the tokens.
Flow [src: `packages/coding-agent/src/session/ttsr-coordinator.ts`; docs: `docs/ttsr-injection-lifecycle.md`]:

1. **Match during stream.** `AgentSession.#handleAgentEvent` forwards `message_update` to the
   `TtsrCoordinator`, which checks `text_delta`, `thinking_delta`, `toolcall_delta` and finalizes on
   `*_end`. Regex matching is synchronous and incremental: single-line conditions rescan only from the
   start of the last line; multi-line conditions rescan the whole buffer only while ≤4 KB or after 25%
   growth, with an exact `final` check at block end [docs §2].
2. **Decide.** Per-rule `interruptMode`: `always` | `prose-only` | `tool-only` | `never`.
3. **Interrupt.** Set abort-pending, create a resume gate, call `agent.abort()` **immediately**
   (synchronously) with a tool-scoped abort reason so siblings get a different message, emit
   `ttsr_triggered` fire-and-forget.
4. **Retry.** A post-prompt task scheduled with `{ delayMs: 50 }` (`ttsr-coordinator.ts:768`) revalidates
   retry token / prompt generation / abort state / target message identity, optionally discards the
   partial assistant output (`contextMode: "discard"`), appends a hidden `custom_message`
   (`customType: "ttsr-injection"`) rendered from `ttsr-interrupt.md`, persists a `ttsr_injection` entry,
   and schedules `agent.continue()`.
5. **Non-interrupting.** Tool-source matches become `afterToolCall` passive `additionalContext` (a
   separate developer message after the batch); prose matches become a deferred `agent.followUp()` with a
   "not interrupted" notice.
6. **Judged rules** (`question`): after the message completes, the judge model scores yes/no per
   candidate; `JUDGED_RULE_THRESHOLD = 0.7` (`packages/coding-agent/src/export/ttsr.ts:58`) [src];
   survivors are delivered as an aside. `onBeforeYield` waits up to 5 s for in-flight judgments.

### Q2.9 Dialects — in-band tool calling (owned tool protocol)

`packages/ai/src/dialect/` implements tool calling for models with no native tool API. Eleven dialects
(`dialect/factory.ts:14-26`): `glm`, `hermes`, `kimi`, `xml`, `anthropic`, `deepseek`, `minimax`,
`harmony`, `qwen3`, `gemini`, `gemma`. A `DialectDefinition` (`dialect/types.ts:40-51`) supplies:

* a `prompt` (rendered into the system prompt by `renderInbandToolPrompt`),
* a streaming `InbandScanner` turning raw text into `InbandScanEvent`s — including `toolStart` /
  `toolArgDelta` / `toolEnd`, and `impliedThinkingEnd` for templates that prefill `thinking`,
* renderers for tool calls / results / thinking / whole transcripts (`encodeInbandToolHistory`).

When a dialect is owned, `prepareProviderCall` sets `tools: undefined`, appends the in-band tool prompt to
the system prompt, and re-encodes history (`agent-loop.ts:2069-2078`); the stream is then wrapped by
`wrapInbandToolStream` so the resulting text is re-materialized as native `toolCall` blocks — the rest of
the loop never knows the difference.

---

## Q3. Context management

### Q3.1 Compaction triggers (six paths)

`docs/compaction.md:70-79` enumerates: (1) manual `/compact`; (2) overflow recovery after a
context-overflow assistant error; (3) `stopReason === "length"` incomplete-output recovery; (4) post-turn
threshold maintenance; (5) **mid-turn** threshold maintenance before the next provider request
(`compaction.midTurnEnabled`, pinned on for subagents because a whole subagent assignment is one turn);
(6) idle maintenance. In every automatic path, context **promotion** (switch to a larger model) is tried
before compacting.

### Q3.2 Threshold math [src: `packages/agent/src/compaction/compaction.ts:338-436`]

```ts
effectiveReserveTokens = max(floor(contextWindow * 0.15), reserveTokens ?? 16384)
resolveBudgetReserveTokens: if the defaulted reserve is impossible for a small window -> 0.15 * window
promptBudgetTokens    = clamp(contextWindow - reserve, 0, contextWindow - 1)
resolveThresholdTokens: fixed thresholdTokens wins; else baseWindow clamp; else thresholdPercent;
                        default (percent <= 0) -> promptBudgetTokens
shouldCompact = enabled && strategy !== "off" && contextTokens > threshold
```

Defaults (`packages/coding-agent/src/session/context-settings.ts`): `keepRecentTokens = 20000` (:285),
`reserveTokens` unset, `idleThresholdTokens = 200000` (:318), `branchSummary.reserveTokens = 16384` (:627).
The measured count feeds `compactionContextTokens(providerTokens, storedEstimate)` = **max** of the two
(`compaction.ts:391`) — a provider-side compression transform cannot hide real history growth.

`compaction.methodOrder` default: `["remote", "snapcompact", "handoff", "shake", "soft"]` [docs:531];
`remote` = provider-native server compaction (OpenAI Responses compact V1/V2, Anthropic
`compact-2026-09-04` beta `compaction` block, Bedrock side request).

### Q3.3 What is summarized vs kept [src]

`findCutPoint(entries, tokenizer, start, end, keepRecentTokens)` (`compaction.ts:544`) walks **backwards**
over *valid cut points* accumulating tokens until the budget is exceeded. Valid cut points are user /
assistant / `bashExecution` / `hookMessage` / `branchSummary` / `compactionSummary` messages,
`custom_message` and `branch_summary` entries; **never a `toolResult`** (`findValidCutPoints:449`, hard
rule at docs:288) so a result can never separate from its call. The last surviving assistant turn is
counted with `countLatestTurn` (byte-exact replay, signatures included).

If the cut is not at a turn start it is a **split turn** and two summaries are produced — history summary
+ turn-prefix summary — merged as `<history>\n\n---\n\n**Turn Context (split turn):**\n\n<prefix>`
[docs:292-318]. Files touched are tracked cumulatively from `read`/`write`/`edit` tool calls and appended
as a `<files>` tree capped at 20 files [docs:370-396].

### Q3.4 Pre-compaction pruning [src: `packages/agent/src/compaction/pruning.ts`]

```
protectTokens    = 40_000   // newest tool-output tokens never pruned   (:78)
minimumSavings   = 20_000   // don't bother below this                   (:79)
MIN_PRUNE_TOKENS = 50       // never blank a result smaller than this    (:292)
```

Pruned results are replaced by `[Output truncated - N tokens]`. `skill` results, `skill://` reads and the
active plan file are protected. Results flagged `useless` (a zero-match search, a `wait` that elapsed with
nothing new) are instead blanked to `[Uneventful result elided]` — and only when the suffix after them is
small and inside the provider's prompt-cache lookback (Claude: 20 positions) or the session has idled past
the cache lifetime, so elision does not itself cost a cache rebuild [docs:259-267].

### Q3.5 Append-only context (prefix-cache stabilization)

`packages/agent/src/append-only-context.ts` — two mechanisms:

* **`StablePrefix`** (:99): snapshots `systemPrompt: string[]` + normalized `Tool[]` and freezes the byte
  sequence. Rebuilds only when the fingerprint changes; a fast path compares per-tool wire identity keys
  (name, description, strict, customWireName, intent mode/closure identity, and **object identity** of
  the memoized wire schema) instead of re-stringifying (:39-55, :169).
* **`AppendOnlyLog`** (:240-280) + `AppendOnlyContextManager` (:337-420). `syncMessages` (:361) handles
  exactly three cases: **append** (same prefix, new tail), **compaction** (array shrank → clear + replay),
  and **in-place rewrite** (pruning, `transformContext` re-render, image strip) — for the last it truncates
  the log to `#longestStablePrefix(...)` and re-appends only the diverged tail. The comment names the
  failure it fixes: clearing on any digest change forced a full ~40k-token re-prefill every turn on
  llama.cpp-style backends when an extension rewrote a single message (#3406). `invalidateForModelChange()`
  / `resetSyncCursor()` handle model switches and compaction rewrites.

The docstring states the intent plainly (:1-15): stabilize the byte prefix so provider prefix caches hit
at the maximum rate; only the new user message is a miss each turn.

### Q3.6 Output budget (`fitOutputTokensToContextWindow`)

`packages/agent/src/output-budget.ts:81` — because Chat-Completions-style hosts reject
`prompt + max_tokens > window`, the loop shrinks `max_tokens` per request:

```
room = contextWindow - countPromptTokens(context) - OUTPUT_FIT_HEADWAY_TOKENS(64)
cap  = max(MIN_FITTED_OUTPUT_TOKENS(1024), room)
```

`countPromptTokens` anchors on the newest trustworthy provider usage report and locally counts only the
tail (10% tokenizer margin + 16 framing tokens per message, :18-29, :133-144). It is skipped when the host
ends generation at the window itself (`stopsOutputAtContextWindow`, e.g. Claude 4.5+), or for an
OpenRouter-hosted model with no caller cap (:87-101).

### Q3.7 Checkpoint / rewind

Not a loop primitive — a **tool pair**. `checkpoint` records `{checkpointMessageCount, checkpointEntryId,
startedAt}`; `rewind` truncates in-memory messages back to that count, writes a `REWIND_REPORT_MESSAGE`
custom message carrying the findings, and keeps the report in `CompletedRewindState`
(`packages/coding-agent/src/tools/checkpoint.ts:15-32`, `session/checkpoint-entries.ts`). Because the
entry id is recorded, rewind can branch the session tree rather than only truncate.

### Q3.8 Cache warming (rent vs buy)

`packages/coding-agent/src/session/cache-warmer.ts` — a ski-rental model over prompt-cache entries. A
refresh is scheduled at `min(90% of TTL, TTL - 10s)` and fires only when
`expected avoided cache-miss cost - refresh cost` clears a savings floor. Two phases: `streaming` (rent
while the run is active, bounded by the cheapest buy-out — rewriting the prefix once, or compacting then
rewriting the compacted context) and `idle` (fixed continuation probability, 30-minute window). Only
models whose catalog declares a cache lifetime are warmed at all (file header, :1-25).

### Q3.9 Speculative execution — priced, committed, rolled back

`packages/agent/src/speculative-execution.ts` + `types.ts:726-874`. Enabled per tool via
`tool.speculation` and globally via `config.speculativeToolExecution`.

* **Effect classes are the price** (`ToolSpeculationEffect`): `{kind:"pure"}` or
  `{kind:"local_read", resources:[{scheme:"file", path, access:"read"}]}`. Nothing that writes is
  eligible — that is the safety invariant, and it is why a speculative result can be discarded without a
  rollback journal.
* **Admission** happens twice: on `toolcall_start` for a tool-owned *stream session*
  (`speculationCoordinator.register`, `agent-loop.ts:2548-2574`) and on `toolcall_end` for the finalized
  call (`admitFinalized`, :2651). Each candidate carries a **fingerprint** of tool + args; a streamed
  session is released only if `matchesFinalArgs` still holds.
* **Claim/commit** (`speculative-execution.ts:617` → `#commitCandidate:551`): at dispatch `claim()`
  re-checks the fingerprint; mismatch discards (`fingerprint_mismatch`). The physical outcome promise is
  awaited; an error outcome discards. A host `validate` hook may veto; a host `commit` hook may return
  `{kind:"fallback"}` to force normal execution or `{kind:"failed"}`. Success marks the candidate
  `committed` and drains dependents (an eval can spawn child candidates with `dependencies`).
* **Rollback** is `discard(reason)` — only pure/local-read effects are eligible, so there is nothing to
  undo; the visible cost is wasted I/O. `SpeculativeOperationCoordinator.take(assistantMessage)`
  (static, :314) hands the coordinator from the streaming path to `executeToolCalls`, so the tool reads
  the pre-computed result via `claim()` and skips execution entirely (`agent-loop.ts:3704-3716`).
* Discarded wholesale on: final message not dispatchable (error/aborted/length), deadline exceeded,
  soft-tool non-compliance, run abort, provider stream failure, and any candidate never dispatched
  (`discardAll("candidate was not dispatched")`, :4015).

### Q3.10 Replay policy

`packages/agent/src/replay-policy.ts` — small but pointed. On provider replay:

* an assistant message that is a **provider refusal** (`stopDetails.type` `refusal`/`sensitive`) is
  **dropped** entirely, because replaying the refusal text feeds the classifier its own verdict and makes
  every later turn likelier to be refused (:11-15);
* a synthetic `assistant_stop_error` tool-result placeholder keeps the "not executed" fact but has its
  text replaced with a neutral constant (`PROVIDER_ERROR_NOT_EXECUTED_TEXT`, :7).

---

## Q4. Tool system

### Q4.1 Definition and registration

A tool is an `AgentTool` object (`packages/agent/src/types.ts:1142+` for `AgentToolExecFn`). The
harness-relevant fields [src]:

| Field | Role |
|---|---|
| `name`, `description`, `parameters` (TSchema), `strict` | wire definition |
| `execute(id, args, signal, onUpdate, ctx)` | the call |
| `concurrency?: "shared" \| "exclusive" \| ((args)=>...)` | batch scheduler slot (`resolveToolConcurrency`, `agent-loop.ts:119`) |
| `interruptible?: boolean \| ((args)=>boolean)` | may be hard-aborted mid-flight by steering/IRC |
| `handlesToolInterrupt?: boolean` | drains the interrupt itself (`wait`) |
| `approval?: ToolApproval` + `formatApprovalDetails`, `approvalCacheTargets`, `approvalRestrictionPaths`, `bindApprovalTargets` | permission model |
| `intent?: "require" \| "optional" \| "omit" \| ((args)=>string)` | injects/derives the `i` "concise intent" field |
| `customWireName?: string` | alias accepted on the wire (e.g. `xd://recall`) |
| `loadMode?: "essential" \| "discoverable"` | top-level schema vs. xdev-mounted |
| `openArgStream`, `speculation` | streaming-argument and speculative-execution hooks |
| `renderCall`, `renderResult` | UI only |

Registration paths: built-ins are constructed as classes under `packages/coding-agent/src/tools/*`
(`class ReadTool implements AgentTool`), extension tools come through `registerTool`, MCP tools through
the MCP bridge (named `mcp__<server>_<tool>`, `builtin-names.ts:121`), and SDK hosts may inject custom
tools. All of them pass through `wrapToolWithMetaNotice` (`tools/output-meta.ts:838`), which wraps
`execute` to spill oversized results (§Q4.5) and re-throw formatted errors.

**Essential vs discoverable**: `ESSENTIAL_BUILTIN_TOOL_NAMES` (`tools/essential-tools.ts:27-41`) —
`read, write, bash, edit, glob, find, eval, task, wait, learn, manage_skill, context_notes, new_context` —
always keep a top-level schema. Everything else can be demoted to *discoverable* and reached only through
`xd://`, which matters because `read`/`write` **are** the xdev transport.

**Withdrawn tool definitions**: `SentToolDefinitions` (`packages/agent/src/sent-tool-definitions.ts`)
remembers the last wire definition of every tool this agent sent. When a turn's tool set shrinks, the
withdrawn definitions ride along as `Context.inactiveTools` so a provider that keeps them declared
(Anthropic `tool_removal`) can re-declare them **byte-identically** — otherwise the prefix cache would
invalidate on every tool-set change.

### Q4.2 Inventory (29 built-ins + 3 hidden)

From `packages/coding-agent/src/tools/builtin-names.ts:3-33` and `BUILTIN_TOOL_HELP` (:49-83) [src], plus
`HIDDEN_TOOL_NAMES = ["yield","goal","think"]` (:85):

| # | Tool | One line | Default |
|---|---|---|---|
| 1 | `read` | Read files, dirs, URLs, archives, DBs — and the `xd://` transport | on |
| 2 | `edit` | In-place file edit (hashline / JSON-patch / apply-patch strategies) | on |
| 3 | `write` | Create/overwrite; executes `xd://<device>` | on |
| 4 | `bash` | Shell command (Rust PTY engine, backgroundable) | on |
| 5 | `grep` | Regex search over files (Rust) | on |
| 6 | `glob` | Find files by glob (Rust) | on |
| 7 | `find` | Semantic "find code by describing it" (needs judge model) | detected |
| 8 | `eval` | Python/JS cells with browser/computer/binary helpers | on |
| 9 | `task` | Launch subagents (fan-out, worktree isolation) | on |
| 10 | `wait` | Wait for background jobs / peer replies / services | on |
| 11 | `todo` | Session todo list | on |
| 12 | `ask` | Ask the user (interactive only) | on |
| 13 | `web_search` | Web search | on |
| 14 | `lsp` | Language-server queries | on |
| 15 | `ast_edit` | AST-pattern rewrite | on |
| 16 | `debug` | DAP debugging of a running process | on |
| 17 | `ast_grep` | AST-pattern search | off |
| 18 | `github` | Issues/PRs/repos via `gh` | off |
| 19 | `checkpoint` | Record a rewind point (message count + entry id) | off |
| 20 | `rewind` | Truncate back to a checkpoint, keep a report | off |
| 21 | `context_notes` | Read/replace persistent context notebook | off |
| 22 | `new_context` | Request a fresh window at the next safe boundary | off |
| 23 | `retain` | Write long-term memory | off |
| 24 | `recall` | Search long-term memory | off |
| 25 | `reflect` | Synthesize an answer from memory | off |
| 26 | `memory_edit` | Edit/forget memories | off |
| 27 | `learn` | Capture reusable lessons | off |
| 28 | `manage_skill` | Create/update/delete managed skills | off |
| 29 | `security_scan` | Run security scans | off |
| — | `yield` | **hidden** — submit subagent result (schema-validated, incremental or terminal) | child-only |
| — | `goal` | **hidden** — goal-mode state ops (`get` is read tier, rest write) | mode-only |
| — | `think` | **hidden** — private scratchpad thought (for models with reasoning off) | mode-only |

Not counted as tools but adjacent: `xd://` devices (`computer`, `browser`, `tab`, `resolve`,
`report_issue`, ssh, …) are mounted *behind* `read`/`write` rather than being separate schemas.

### Q4.3 Dispatch

Pre-dispatch (`prepareToolCallDispatch`, `agent-loop.ts:3163`) runs in **call order** for every tool call
on the assistant message, before `message_end` on the streamed path:

1. `ensureUniqueToolCallIds` — mint/repair ids (dedupe against dispatched ids in the run and the
   transcript; `MAX_TOOL_CALL_ID_LENGTH` import from `providers/transform-messages`).
2. `resolveToolForCall` — exact name, then `customWireName`, then `resolveFallbackTool` (host aliases);
   a resolved alias is written back onto the call so history/replay agree.
3. intent extraction / derivation (`extractIntent`, `tool.intent(args)`).
4. schema validation (`validateAgentToolArguments`) — failures are *recorded*, not thrown, so the batch
   keeps its order.
5. `beforeToolCall` hook — may `{block, reason}`, `{args}` (revalidated, then baked into the message), or
   `{additionalContext}`.

Execution (`executeToolCalls`, :3338): results are held until every earlier call has one, so
`message_start`/`message_end` for results are emitted **in assistant call order** even when tools finish
out of order (`flushResultMessages`, :3544). `tool_execution_end` fires immediately per call for live UI.

Concurrency scheduler (:3873-3992):

```
exclusive → start = Promise.all([lastExclusive, ...sharedTasks]); lastExclusive = task; clear shared
shared    → start = lastExclusive; sharedTasks.push(task)
            if approval tier of this call !== "read": sharedEffects.push(task)
```

A later `shared` call therefore waits on `Promise.allSettled(sharedEffects)` — i.e. on all earlier
*possibly-writing* siblings — without serializing pure reads. That is how a batch of 8 reads runs
concurrently while a write in the middle still orders correctly.

Interrupts (§Q1/Q2) are three-layered: `nonInterruptibleSignal` = external abort only;
`interruptibleSignal` = external + steering + IRC; `steeringSoftController` is a **cooperative** signal
that never kills anything (bash auto-backgrounds itself). A tool that already completed keeps its real
result even if its signal aborted at completion (`completedToolExecution`, :3827-3842).

### Q4.4 Permission / approval flow

`packages/coding-agent/src/tools/approval.ts`. Tiers are ordered `read(0) < write(1) < exec(2)`
(`TIER_RANK`, :131). Modes and the maximum tier each auto-approves (`APPROVAL_MODE_MAX_TIER`, :152):

| Mode | Auto-approves up to |
|---|---|
| `always-ask` | read (i.e. everything above read prompts) |
| `always-ask-cached` | read, plus previously granted cache keys |
| `write` | write |
| `yolo` | exec (nothing prompts) |

Resolution order (:348-355): (1) the tool's own `approval(args)` decision (default tier `exec`), possibly
carrying a `policyKey`; (2) user `tools.approval.<tool>` override (`allow`/`deny`/`prompt`); (3) mode vs
tier. Missing context is **fail-closed** → `always-ask` with no grants (:83-89). `tools.restrictedFolders`
forces a prompt regardless of mode or remembered grant (`approvalRestrictions`, :335). Remembered grants
are keyed by `approvalCacheTargets` (files/URLs a call touches), which is what makes `always-ask-cached`
useful for repeated edits to the same files.

The approval prompt is a UI concern; the harness contract is only `ToolApprovalDecision`
(`types.ts:1108`) — `"read"|"write"|"exec"` or `{tier, policy}` — plus `formatApprovalDetails` for the
body. A blocked call surfaces as `ToolCallBlockedError` and becomes a tool-error result, not a crash.

### Q4.5 Internal URL schemes (harness level)

One process-global `InternalUrlRouter` (`packages/coding-agent/src/internal-urls/router.ts:110-130`)
dispatches `scheme://` to stateless handlers, so any tool that takes a path (`read`, `write`, `edit`,
`grep`) can address non-file resources. Registered schemes [src]:

| Scheme | Purpose |
|---|---|
| `skill` | read a skill's files (`skill://<name>/...`) |
| `rule` | read rule documents |
| `memory` | long-term memory entries |
| `out` | **write-only** reply channel to the host/UI (`out://`) |
| `agent` | subagent outputs by id — `agent://<id>`, `agent://Parent.Child/reports/0/data` (JSON extraction) |
| `history` | session transcript views incl. `history://current/full` (pre-compaction messages) |
| `local` | session-local files, including `local://<N>.log` spills and `local://PLAN.md` |
| `proc` | running processes / background jobs |
| `cfg` | effective config, read-only, never prompts |
| `ssh` | remote host filesystem |
| `security` | security-scan findings |
| `vault` | secrets vault (approval-gated) |
| `issue` / `pr` / `mr` | GitHub issue / PR / GitLab MR context |
| `omp` | embedded omp documentation (`omp://<file>.md`) |
| `xd` | **device transport** — `read xd://` lists devices, `write xd://<tool>` executes one |
| `attachment` | user-attached files |
| `conflict` | git merge-conflict blocks: `conflict://<N>[/ours\|theirs\|base]`, writable |

`xd://` is the notable one: it turns `write` into a generic RPC surface for mounted tools (computer use,
browser, resolution devices, `report_issue`, …) with the mounted tool's own approval tier computed from
the decoded JSON payload (`xd-protocol.ts:28-32`).

### Q4.6 Result shaping, truncation, spills

Two layers:

1. **In-tool** — `ToolResultBuilder` (`tools/tool-result.ts`) accumulates `content` (text/image blocks),
   `details`, `isError`, `useless`, and an `OutputMeta` (`meta.truncation`, `limits`, `sourceUrl`,
   `diagnostics`, `minimized`).
2. **Post-tool wrapper** — `wrapToolWithMetaNotice` → `spillLargeResultToArtifact`
   (`tools/output-meta.ts:581`). If the joined text exceeds `tools.artifactSpillThreshold`
   (**50 KB** default, `tools/settings.ts:12-15`), the full text is saved as a session artifact and the
   inline result is elided to head **20 KB** + tail **20 KB / 500 lines** (`artifactHeadBytes`,
   `artifactTailBytes`, `artifactTailLines`), with a `[raw output: local://N.log]` footer appended.
   If the save fails, the result is still truncated but no recovery link is invented (:621-636).
   Programmatic callers (`context.programmatic`) skip the shaping so parsers see clean payloads (:785-787).

`useless: true` (set by `ToolResultBuilder.useless()`) propagates to the persisted `ToolResultMessage`
and drives compaction's elision (§Q3.4). Synthetic results for calls that never ran carry
`details.__synthetic = true` with a `source` discriminator (`assistant_stop_aborted`,
`assistant_stop_error`, `interrupt_skipped`, …) so UI/telemetry can distinguish "not executed" from
"failed" (`agent-loop.ts:4055-4090`).

---

## Q5. Subagents

### Q5.1 Fan-out — the `task` tool

`task` takes `{context, tasks:[{agent, task, solutionSpace, model?, outputSchema?, effort?}], ...}`
[docs: task-agent-discovery.md:79-92]. Each item is an assignment for a discovered **agent definition**
(`AgentDefinition`: `name`, `description`, `systemPrompt`, optional `tools`, `spawns`, prioritized
`model`, `thinkingLevel`, `output`, `blocking`, `structured`, `autoloadSkills`, `readSummarize`,
`prewalk`, `advisor`) parsed from markdown frontmatter (:27-50).

Discovery order (`src/task/discovery.ts`, docs:164-176): nearest project `.omp/agents` → user
`.omp/agents` → each enabled extension package's `agents/` → Claude marketplace plugin roots →
bundled. **First-wins dedup by exact name** (:182-194), so a project agent shadows a user agent shadows a
bundled one.

Guards: `task.disabledAgents`; parent `spawns` policy (`"*"`, `""`/false, CSV); `PI_BLOCKED_AGENT`;
`task.maxRecursionDepth` default **2** (:390) — at the cap `runSubprocess` strips `task` from the child's
tool list and empties its spawn policy.

### Q5.2 Isolation

`packages/coding-agent/src/task/worktree.ts` (+ `isolation-runner.ts`, `isolation-ownership.ts`).
Isolation is **backend-pluggable** (`cfgIsolationBackend`, `IsoBackendKind` from Rust natives):
a git worktree, or an `ovnfs` overlay (copy-on-write virtual FS, `task/ovnfs.ts`). The worktree module
records a `WorktreeBaseline` for the root repo **and nested git repos** (`RepoBaseline`:
`headCommit`, `staged`, `unstaged`, `untracked`, `untrackedPatch`), then applies the child's diff back to
the parent by patch sections (`splitPatchSections` / `patchSectionPaths`). `GIT_NETWORK_TIMEOUT_MS` is
**30 min**. Isolation directories are prefixed `t` + a 9-char digest (`TASK_ISOLATION_DIR_PREFIX`,
`..._DIGEST_CHARS`), and ownership is written to disk so a crashed run's worktrees can be reaped
(`writeIsolationOwner`, `needsNativeTeardown`).

### Q5.3 Typed, schema-validated yield

`yield` (`tools/yield.ts`) is the structured-result channel. Contract: submit
`{"data": <output>}` or `{"error": "message"}`; the tool validates `data` against the run's resolved
output schema (item `outputSchema` → agent frontmatter `output` → parent session `outputSchema`), with
`strict`/`permissive` schema modes. It supports **incremental** yields (a `type` names the section) and a
**terminal** yield that finalizes all sections. On exhausted `MAX_SCHEMA_RETRIES` it accepts the payload
and sets `details.schemaOverridden` so the executor's post-mortem does not re-reject the same data
(`YieldDetails` doc comment, :22-38).

The terminal yield is what makes a subagent stop: it aborts with
`TERMINAL_TOOL_RESULT_ABORT_REASON = Symbol.for("pi-agent-core.terminal-tool-result")`, which the loop
treats as a **graceful** stop — the completed tool batch is persisted and `onTurnEnd` still runs
(advisor review of the yield delta included), then the run ends before another model call
(`agent-loop.ts:804-840`, :1806).

### Q5.4 IRC — peer messaging

`packages/coding-agent/src/irc/`:

* **`bus.ts`** — process-global mailbox. A `send` never blocks on the recipient generating anything.
  Delivery: resolve the recipient through `AgentRegistry`; **parked/killed agents are revived** via
  `AgentLifecycleManager`, idle agents are woken with a real turn, busy agents get the message as a
  **non-interrupting aside at the next step boundary** (`AgentSession.deliverIrcMessage`).
* **`mesh.ts`** — cross-instance: joins the project daemon room, mirrors the roster, forwards messages to
  agents in *other* omp processes. Rate limits: `WAKE_BUDGET = 20` wakes per `WAKE_WINDOW_MS = 600000`
  (10 min), `RECEIPT_TIMEOUT_MS = 40000` (mesh.ts:40-43).
* **`wait`** (`tools/wait.ts`) is the receiving end: it blocks on an `IrcWaitFilter`
  (`from`, `directOnly`, `repliesFrom`), or on async jobs, launched services, or a parent agent, with
  windows tuned to stay inside the prompt-cache lifetime (so a long wait still returns on a warm cache).

IRC interrupts reach a running batch through `hasIrcInterrupts` → `checkAsideInterrupts`, which hard-aborts
*interruptible* tools only and raises the cooperative signal for the rest (`agent-loop.ts:3466-3497`).

### Q5.5 Lifecycle: park / revive / kill

`registry/agent-lifecycle.ts` (header comment :1-22) owns `idle → parked → revived`:

* on finish, the executor **adopts** the agent; the manager arms a TTL timer whenever it goes idle;
* on expiry it **parks**: disposes the live session, keeps the `AgentRef` + `sessionFile` on disk;
* `ensureLive(ref)` **revives** on demand — a killed (`aborted`) agent revives the same way from its
  transcript, reopening as a fresh `parked` generation then cold-reviving;
* only this manager flips `parked ↔ idle`; park/dispose is gated against concurrent `ensureLive`/peer
  sends, and every operation is bound to the exact `AgentRef` so stale async work cannot clobber a newer
  same-id ref. `AGENT_RELEASE_GRACE_MS = 5000`. A kill writes a tombstone file so a later scan lists the
  agent as killed rather than parked.

Cold revival restores the child's persisted tool list (including `yield`), model route, thinking level,
service tier, account pool, and advisor opt-in — which is why those are persisted in `session_init`
[docs:288, 316-318].

---

## Q6. Advisor / watchdog

### Q6.1 Shape

An advisor is a **second full agent** (own `Agent` instance, own `ToolSession` whose id is suffixed
`-advisor`, own append-only context, own usage ledger) attached to a primary session. It reviews the
primary's **transcript delta** and can only influence the primary through one channel: notes emitted by
its `advise` tool. It never approves actions and never mutates primary state
(`docs/advisor-watchdog.md:3-5`).

### Q6.2 Review loop [src]

1. On primary `turn_end`, `AgentSession` hands the delta since the advisor's cursor to `AdvisorRuntime`
   (`src/advisor/runtime.ts`). Deltas are rendered with reasoning, tool intent, and tool results; the
   session **secret obfuscator** runs over provider-bound messages and tool args first
   (docs:117). Previously injected advisor messages are filtered out so the advisor cannot review its own
   advice (:121).
2. Cadence: `reviewMode` `turn` (default) or `agent-end`; `reviewInterval` N (default 1) — skipped
   updates accumulate and are sent with the next scheduled review.
3. The advisor runs a normal agent turn with `advise` plus an investigative tool grant
   (`read`, `grep`, `glob`, plus `recall` when the memory backend provides it). A `WATCHDOG.yml` entry may
   grant any built-in, including `edit`/`write`/`bash`/`eval` — those run in the isolated advisor
   `ToolSession` but still honour the session's approval mode (docs:134-147).
4. Notes are rendered as XML-escaped `<advisory advisor=… severity=… guidance="weigh, don't blindly obey">`
   blocks (docs:157-163; `advise-tool.ts:49-56`).

Resets: any primary transcript rewrite (compaction, session switch/resume, branch/fork, context re-prime)
clears the advisor's private transcript and rewinds its cursor, so the next update replays the bounded
primary transcript instead of continuing from stale context (docs:123-132).

### Q6.3 Blocker semantics — the delivery decision [src]

`resolveAdvisorDeliveryChannel` (`src/advisor/advise-tool.ts:118-137`) returns
`"steer" | "aside" | "preserve"`:

```ts
if (preserveOnly && !streaming) return "preserve";
if (terminalIdle) { if (!meets(severity, continueSeverity)) return "preserve"; }
else if (!isInterruptingSeverity(severity)) return "aside";     // nit -> aside
if (autoResumeSuppressed && (aborting || !streaming)) return "preserve";
if (interruptImmuneTurnActive && severity !== "blocker") return terminalIdle ? "preserve" : "aside";
return "steer";
```

with `isInterruptingSeverity` = `concern | blocker` (`:66-68`) and rank `nit(1) < concern(2) < blocker(3)`
(`:174`). In words:

* **`nit` / omitted** — non-interrupting aside; at the next turn boundary. After a terminal answer it
  becomes a visible card, not a new turn.
* **`concern`** — interrupting steer while streaming; when idle on a terminal answer it starts a
  continuation only if it meets `advisor.continueSeverity` (default `concern`), otherwise it is preserved
  as a card (`#4840`).
* **`blocker`** — always steers; exempt from the `advisor.immuneTurns` cooldown and from the
  "terminal answer" suppression. Rationale in the comment: a blocker means the agent handed off broken or
  unexercised work.
* **`advisor.immuneTurns`** (default **3**, `src/advisor/settings.ts:43-48`) — after a concern/blocker
  steers, later non-blocker concerns downgrade to asides for N primary turns.
* **`autoResumeSuppressed`** — after a deliberate user interrupt the advisor may not auto-resume; the note
  is preserved as a card (docs:165).

### Q6.4 Emission guard (anti-noise)

`src/advisor/emission-guard.ts` (docs:185-196):

1. normalize (lowercase, NFKC, collapse non-alphanumerics) — `"Stop."` ≡ `"*Stop*"` ≡ `"stop"`;
2. drop content-free phrases (`stop`, `lgtm`, `no issue continue`, …);
3. severity-aware dedupe — a repeat at equal/lower severity is dropped, a higher re-raise is allowed;
   FIFO history caps at **4096** entries;
4. per-update non-blocker budget — `maxNotesPerUpdate` default **4** (range 1–32); a higher-severity note
   may displace the lowest-rank still-pending note from the same update; blockers are exempt.

### Q6.5 Catch-up, failure, cost

* `advisor.syncBacklog` — `off` (default) / `1` / `3` / `5` / `strict`. At primary turn end the primary
  waits while a scheduled advisor's backlog is at or above the threshold, capped at **30 s** for numeric
  modes; `strict` has no wall-clock cap. Aborts/failures release waiters (docs:198-227).
* Failures: credential rotation → model fallback (`retry.fallbackChains` keyed `advisor`) → bounded
  cooldown wait → pause. Three dropped-backlog cycles halt the runtime until an explicit reset.
* Cost: advisor usage is separate; `/advisor status` reports token/context from the live transcript, cost
  from a cumulative ledger. `advisor.evictStaleResults` (default true) replaces older reviews'
  `read`/`grep`/`glob` results with placeholders before each review (docs:397-415).
* Persistence: `__advisor.jsonl` / `__advisor.<slug>.jsonl` inside the owning session's artifacts dir, so
  `omp stats` attributes the spend and Agent Hub can show the transcript (docs:417-432).
* Advisors are explicitly **not peers**: excluded from IRC rosters, `agent://` broadcasts, and
  `history://` (docs:434).

### Q6.6 Watchdog files

* `WATCHDOG.md` — advisor-only guidance appended to the advisor system prompt (never the primary's), with
  `@path` imports (5 hops max), discovered at the agent dir plus every ancestor `WATCHDOG.md` /
  `.omp/WATCHDOG.md`; sorted user → far ancestor → cwd, wrapped in `<attention>` (docs:263-320).
* `WATCHDOG.yml` — the **roster**: `instructions`, `advisors[]` with `name`, `enabled`, `model`, `tools`,
  `reviewMode`, `reviewInterval`, `syncBacklog`, `scope` (`main|subagent|all`), `includeReasoning`,
  `maxNotesPerUpdate`, `instructions` (docs:322-384). Duplicate slugs resolve by specificity
  (project leaf > ancestor > user).

---

## Q7. Extensibility

Three independent mechanisms, all funnelling into the same loop seams:

| Mechanism | Unit | Entry point | Power |
|---|---|---|---|
| **Hooks** | a module exporting handlers | `extensibility/hooks/` | observe/mutate tool calls & context, block, inject context |
| **Extensions** | a `pi` factory with `ExtensionAPI` | `extensibility/extensions/` | register tools/providers/commands/shortcuts/renderers, full event surface, UI |
| **Custom tools** | a TS module exporting a `CustomTool` | `extensibility/custom-tools/` | add a tool with its own schema, approval tier, renderers |
| **MCP** | external server over stdio/HTTP/SSE | `mcp/` | tools, resources, prompts, notifications |

### Q7.1 Hook lifecycle points [docs:109-190]

*Session*: `session_start`, `session_before_switch`→`{cancel}`, `session_switch`, `session_clear`,
`session_before_branch`→`{cancel, skipConversationRestore}`, `session_branch`,
`session_before_compact`→`{cancel, compaction}`, `session.compacting`→`{context, prompt, preserveData}`,
`session_compact`, `session_before_tree`→`{cancel, summary}`, `session_tree`, `session_shutdown`.

*Agent/context*: `context`→`{messages}`, `before_agent_start`→`{message}`, `agent_start`, `agent_end`,
`turn_start`, `turn_end`, `auto_compaction_start/end`, `auto_retry_start/end`, `ttsr_triggered`,
`todo_reminder`.

*Tool*: `tool_call` (pre) → `{block, reason, input, additionalContext}`; `tool_result` (post) →
`{content, details, isError, additionalContext}`.

The pre-tool event runs **during argument preparation**, before scheduling, `tool_execution_start`, and
approval, so a replaced `input` is revalidated and becomes the displayed/persisted/executed call
(docs:185-190). That is precisely the same slot as `beforeToolCall` in the loop
(`prepareToolCallDispatch`), which is why a hook revision is "baked into the message" before any consumer
snapshot (`agent-loop.ts:3246-3255`).

`additionalContext` from either hook is **not** part of tool output: it is collected
(`record.reportedContext` / `preparedContext`, `agent-loop.ts:4022-4029`) and emitted as a separate
developer message after the batch settles (`injectExecutionAdditionalContext`, :1207).

### Q7.2 Extension API shape

An extension is a factory receiving `pi: ExtensionAPI` (docs:122-148). It can register: tools,
**providers** (`registerProvider`, including command-backed providers), slash commands, keyboard
shortcuts, message renderers, composer shapes, file write/delete fallbacks, and settings overrides. The
event surface mirrors the hooks plus prompt/turn events (`input`, `before_agent_start`,
`before_provider_request`, `after_provider_response`, `context`, `session_stop`, `cache_warming_decision`,
`turn_start`/`turn_end`), tool lifecycle, plan review, mode lifecycle, **subagent lifecycle**
(`before_subagent_spawn` can block or replace model patterns), reliability signals, link activation, MCP
notifications, user-command interception, and `resources_discover` (docs:521-727).

Two extension-only loop seams worth naming:

* `before_provider_request` — receives `{payload}`, returns a replacement payload; handlers chain. This is
  where a compression extension shrinks the request, which is exactly why compaction floors provider usage
  with a local estimate (`compactionContextTokens`).
* `context` — receives messages, returns `{messages}`; replacements chain **without rewriting persisted
  history**.
* `ctx.runEphemeralTurn` — an isolated side turn that does not touch the main transcript.
* `session_stop` — awaited before settle, advisory `{continue:true, additionalContext}` capped at **8**
  continuations; `{decision:"block"}` is a hard refusal until the hook allows completion.

### Q7.3 MCP integration points

`packages/coding-agent/src/mcp/` + `docs/mcp-runtime-lifecycle.md`:

1. discovery (`loadAllMCPConfigs`) → `MCPManager.connectServers` starts per-server connect + `tools/list`
   in parallel;
2. a **fast startup gate waits 250 ms**, then returns loaded `MCPTool`s, per-server failures, or cached
   `DeferredMCPTool`s for still-pending servers (doc:5-14) — so a slow MCP server never blocks startup;
3. `discoverAndLoadMCPTools()` converts tools to `LoadedCustomTool[]`; `createAgentSession` pushes them into
   `customTools` and the registry under `mcp__<server>_<tool>` (sanitized, 64-char cap with deterministic
   hash suffix; collisions resolved deterministically);
4. tools may be **mounted under `xd://`** instead of top-level, per the session's tool policy;
5. `MCPTool` calls through the live connection; `DeferredMCPTool` awaits `waitForConnection(server)` first;
   both do a reconnect + one retry for retriable connection failures, but **never replay** a request that
   may already have reached the server (stdio wrote its frame / HTTP reset after establishment);
6. late tool-list changes reach the live session through the manager callback; `/mcp reload` =
   `disconnectAll` + rediscovery + `session.refreshMCPTools`;
7. `structuredContent` is retained in `details.structuredContent`; server `isError` propagates;
8. teardown on explicit disconnect or owning `AgentSession` disposal — a subagent borrowing the parent's
   manager does not disconnect it.

The bridge (`mcp/tool-bridge.ts`) reuses the harness's own machinery: approval formatting from
`tools/approval.ts`, the internal-URL router for URI-valued results, and the standard spill/byte-cap path
for large text blocks.

### Q7.4 Custom tools

`CustomTool` (`extensibility/custom-tools/types.ts`) is the same shape as `AgentTool` plus TUI renderers
(`renderCall`/`renderResult`) and a richer context (settings, model registry, session manager, UI, exec,
logger, todo). Registered through the same adapters as built-ins, so they get approval tiers, `loadMode`,
intent injection, and the output-spill wrapper for free.

---

## Q8. Sessions

### Q8.1 On-disk format — JSONL, one header + a tree of entries

```
~/.omp/agent/sessions/<encoded-cwd>/<timestamp>_<sessionId>.jsonl
~/.omp/agent/blobs/<sha256>                     # externalized large payloads
~/.omp/agent/terminal-sessions/<terminal-id>    # breadcrumbs for --continue
```

(`docs/session.md:37-62`.) Files are JSONL. Current files begin with a fixed-width **256-byte
`type:"title"` slot** (so an auto-rename can be rewritten in place without reordering the journal),
followed by the `session` header and then `SessionEntry` records; loaders strip the physical slot and fold
its title into the logical header (:63-70).

Header (`SessionHeader`, :71-104): `{type:"session", version:3, id: UUIDv7, timestamp, cwd, title?,
titleSource?, additionalDirectories?, previousSessionFiles?, providerPromptCacheKey?, taskPromptCacheKey?,
parentSession?}`.

Every non-header entry carries `{type, id (8 hex chars, Snowflake fallback), parentId, timestamp}`
(:105-118). Entry taxonomy (:120-139): `message`, `model_usage`, `thinking_level_change`, `model_change`,
`service_tier_change`, `compaction`, `branch_summary`, `reset_boundary`, `custom`, `custom_message`,
`label`, `title_change`, `ttsr_injection`, `credential_pin`, `session_init`, `mode_change`.

Two storage alternatives exist behind the same interface (`session/session-storage.ts`):
`sql-session-storage.ts`, `indexed-session-storage.ts`, `redis-session-storage.ts`, `agent-storage.ts` —
JSONL is the default file backend, not the only one.

### Q8.2 Session tree

The model is **append-only tree + mutable leaf pointer** (docs:563-576):

* append → new entry with `parentId = leafId`, then advance the leaf;
* `branch(entryId)` moves **only** the pointer — existing entries stay;
* `resetLeaf()` sets `leafId = null`, so the next append is a new root (`parentId: null`);
* `branchWithSummary()` moves the leaf to a target and appends a `branch_summary`;
* `getEntries()` is insertion order; **there is no persisted leaf field** — loading rebuilds the leaf from
  the last physical entry, so a pointer-only branch needs a subsequent append to survive a reload;
* `createBranchedSession(leafId, {copyArtifacts})` mints a new identity containing just the root→leaf path.

### Q8.3 Resume / fork / export

Operation matrix (`docs/session-operations-export-share-fork-resume.md:19-38`):

| Op | Mutation | Notes |
|---|---|---|
| `/compact` | appends `compaction` | see Q3 |
| `/clear` | appends `reset_boundary` | keeps id/file/history; hides context through the boundary |
| `/new` | new identity + header | persistent mode materializes the header eagerly |
| `/delete` | deletes file+artifacts, switches to lazy successor | no empty file until content |
| `/fork` | new session file, current session switches to it | copies artifact dir in the background |
| `--fork <id\|path>` | new session fork into current cwd | copies source artifacts recursively |
| `/resume` / `--resume` | replace in-memory state | id/prefix matching, local-then-global |
| `--continue` | open breadcrumb or most-recent | creates one if none |
| `/export`, `--export` | none | HTML file |
| `/share` | none | encrypted link (gist or share server) |
| `/dump` | none | clipboard text (+ best-effort JSON sidecar) |

Resolution niceties worth noting: resume follows **session continuations** — if the conversation moved on
in a sibling file whose header names it in `continuationOf`, the copy with the newest turn is opened
instead (:457-464); a session whose recorded cwd vanished prompts to **re-root** (`manager.moveTo`), and a
fork can inherit `providerPromptCacheKey` so the child's first request still hits the parent's cache
(docs:96-100).

`buildSessionContext(entries, leafId?, byId?, options?)` (:577-600) is the single reconstruction function:
walk `parentId` to root (bounded against cycles), derive runtime state from the latest
`thinking_level_change` / `service_tier_change` / `model_change` / `mode_change`, choose the emission
boundary (later `reset_boundary` wins, else latest `compaction`), convert `message` / `custom_message` /
`branch_summary` entries, then drop dangling tool calls and unsafe aborted/error turns from model context.
`options.transcript: true` builds the **display** transcript instead — full history with compactions
rendered inline, which is why compaction no longer visually restarts the conversation
(docs/compaction.md:238-240).

### Q8.4 Persistence guarantees

Writes go through an ordered append pipeline with a per-message persistence tail
(`#messageEndPersistenceTail`, `agent-session.ts:1334-1336`) so concurrent `message_end` writes cannot
interleave. Compaction/prune rewrites go through targeted rewrite helpers (`discardEntryDurably` appends a
metadata branch marker and rewrites the journal). Large payloads are externalized to the blob store
(docs:641-653).

---

## Q9. System prompt

### Q9.1 Composition pipeline (`packages/coding-agent/src/system-prompt.ts`)

`buildSystemPrompt(options)` (:724) runs **one** Handlebars render plus a trailing project block:

1. **Template selection** (`:784-831`), in precedence order:
   explicit `customPrompt` / `systemPromptTemplate` (mutually exclusive, throws otherwise) →
   discovered `SYSTEM.md` (`discoverSystemPromptOverride`) → per-model `systemPromptTemplates` pattern
   match (`matchModelPattern`, :810) → bundled `prompts/system/system-prompt.md`.
   An empty or invalid discovered template falls back to the bundled one with a warning.
2. **Parallel preparation with a hard deadline**: context files, skills, workspace tree, active repo
   context, personality override, `SYSTEM.md` walk-up — each wrapped in `withDeadline(name, promise,
   fallback)` against **`SYSTEM_PROMPT_PREP_TIMEOUT_MS = 5000`** (:250, :858-880). A slow step degrades to
   its fallback and logs; the prompt still renders. This is a startup-latency guarantee, not an error path.
3. **Render** with `prompt.renderTracked(template, data, sourceItems)` (:1168) — a Handlebars instance
   (`packages/utils/src/prompt.ts:358`) with custom helpers (`arg`, `join`, `default`, comparisons,
   `ifAny`, …). `renderTracked` (`utils/src/prompt.ts:617`) compiles with `compileMarked` and returns
   `{text, marks}`: each mark is a byte span for one iterated item (`{{#each contextFiles}}`,
   `{{#each skills}}`), snapped to whole lines, outermost-first. `trackedSources(...)` turns those marks
   into the `sources` block list in the result, so the caller knows which context file or skill produced
   which lines of the final prompt.
4. **Block assembly** (:1180-1197):
   ```
   systemPrompt[0] = rendered.text                 // the whole static prompt, ONE block
   systemPrompt[1..n] = eval prelude guidance      // browser/computer preambles, if enabled
   systemPrompt[n+1] = <project-context> … </project-context>   // cwd-bound content
   ```
   The comment at :1186-1190 states the reason: context files, workspace tree/roots and the session append
   text are **deliberately placed after every static block** so sessions in different directories share a
   byte-identical static prefix and "the Anthropic head cache breakpoint lands right before this block".

The `data` object (:1113-1165) is the prompt's whole feature surface: `tools`/`toolInfo`/`toolInventory`/
`toolListMode`, `environment`, `contextFiles`, `agentsMdSearch`, `workspaceTree`, `internalUrls` (rendered
from the live router, so the prompt's URL documentation always matches the registered schemes),
`skills`, `rules`/`alwaysApplyRules`, `cwd`, `additionalWorkspaceRoots`, `model`, `delegationTier`,
`personality`, `intentTracing`/`intentField`, `taskBatch`, `scoutAvailable`, `taskIrcEnabled`,
`secretsEnabled`, `resultShaping`, `renderMermaid/Svg/Embeds`, `xdevTools`/`xdevDocs`, `subagent`, …

Two modes for tools: `toolListMode = !inlineToolDescriptors && nativeTools` (:1020) — with native tool
calling the prompt carries only a **compact name list**; otherwise the full inventory with descriptions and
parameters is rendered into the prompt (`renderToolInventory`, :1042) and the wire schemas are pruned of
descriptions (`pruneToolDescriptions` in `normalizeTools`).

`NULL_PROMPT=true` short-circuits to an empty prompt (:725) — used by benchmarking harnesses.

### Q9.2 Context files (AGENTS.md and friends)

`loadProjectContextFiles` (:385) + `docs/context-files.md`:

* Native provider: `~/.omp/agent/AGENTS.md` (user) and `<nearest-non-empty .omp>/AGENTS.md` (project).
  Discovery climbs from cwd toward the repo root and **stops at the first non-empty `.omp/`** — a missing
  `AGENTS.md` there does not continue upward (docs:32-45).
* Foreign conventions are discovered through provider adapters (`claude`, `codex`, `gemini`, `opencode`,
  `github`, `agents`, `agents-md`, `claude-md`, `custom-context`); foreign **user-level** sources are
  opt-in via `enabledProviders`, project-level ones are on by default.
* `RULES.md` is **not** a context file: it becomes an **always-apply rule** whose full body rides every
  request (sticky).
* `@path` imports expand recursively (5 hops), skip code fences, and leave missing targets literal.
* Deduplication: `dedupeContainedContextFiles` drops a file whose content is already contained in another
  (:354), and `withoutAgentsMd` (:376) implements the subagent rule below.

Per session type (`docs/system-prompt-customization.md:84-97`) [docs]:

| Session | AGENTS.md | CLAUDE/GEMINI.md | APPEND_SYSTEM.md | MCP tools | WATCHDOG.md | Personality |
|---|---|---|---|---|---|---|
| Main | discovered | discovered | yes | yes | no | configured |
| Task subagent | **not inherited** | inherited | no | inherited | no | `none` |
| Advisor | inherited | inherited | no | no | yes | none |

### Q9.3 Rulebook matching

`docs/rulebook-matching-pipeline.md` + `capability/rule.ts`, `capability/rule-buckets.ts`. All providers
normalize into one `Rule` shape (`name, path, content, globs?, alwaysApply?, description?, condition?,
astCondition?, question?, scope?, agents?, interruptMode?, repeatMode?, _source`). Identity is
`rule.name` — **dedup and precedence are name-based only** (first wins, higher-priority provider first).
`bucketRules(...)` then splits the set three ways: **always-apply** rules (full body in context),
**rulebook** rules (available via the system prompt + `rule://` URLs, matched by glob on demand), and
**TTSR** rules (registered with `TtsrManager`, §Q2.8).

### Q9.4 Per-model prompt adjustments

* `systemPromptTemplates`: an array of `{pattern, path}` matched against the model id (`matchModelPattern`,
  system-prompt.ts:812) — the documented route for model-family prompts.
* `includeModelInPrompt` — whether `{{model}}` renders at all (:1135).
* Model-family *behavioral* patches live outside the template, in the prompt fragments under
  `prompts/system/` (`gemini-tool-call-reminder.md`, `anthropic-usage-wrap-up.md`,
  `malformed-function-call-retry.md`, `length-stop-retry.md`, `empty-stop-retry.md`,
  `interrupted-thinking.md`), injected as additional messages at the moment they apply rather than baked
  into the static prompt.
* `intentField`/`intentGuidance` — injected only when intent tracing is on, and `PI_INTENT_PROMPT`
  overrides the guidance text.

---

## Q10. Provider layer

### Q10.1 Roles

`MODEL_ROLES` (`packages/coding-agent/src/config/model-roles.ts:52-88`) — chat roles:
`default`, `smol` (Fast), `slow` (Thinking), `vision`, `plan` (Architect), `commit`, `tiny`, `memory`,
`task` (Subtask), `advisor`, `widget`; kind roles: `image`, `web`, `speech`, `dictation`, `judge`.
Selectors accept `@role`, legacy `pi/<role>`, and `*` for default (:18-26). Custom roles can be introduced
by settings/config without a code change (`getKnownRoleIds`, :103-120).

Resolution notes: an invalid `modelRoles.advisor` **fails** rather than silently falling back; unset
advisor resolves through `slow` then the built-in slow chain, not the primary model
(`docs/advisor-watchdog.md:46`). `task` model precedence is
`task.agentModelOverrides` → agent frontmatter → parent's active model
(`docs/task-agent-discovery.md:238-244`), and per-call `model` is gated by `task.perCallModel`
(`off`/`eval`/`on`) against an operator-grant allowlist (:246-292).

### Q10.2 Registry / catalog

* `packages/catalog` generates `models.json` (huge, do not read) — the model catalog: ids, windows,
  `maxTokens`, `thinking.effortBudgets`, `compat` flags, `image-tokenization`, `prompt-cache` lifetimes,
  `remoteCompaction` capabilities. Catalog **rules** (KDL/TS keyed by provider/model patterns) supply
  per-model quirks: `providers/amazon-bedrock.kdl`, `compat.supportsServerCompaction`,
  `stops-output-at-context-window`, `shared-prompt-cache`, etc.
* `packages/ai/src/registry/` builds the runtime registry from config + catalog (`build.ts`, `registry.ts`,
  `derived.ts`, `hooks/`, `engine/`) and handles provider-specific auth/endpoint derivation
  (`amazon-bedrock.ts`, `kiro.ts`, `snowflake.ts`, `cloudflare-ai-gateway.ts`).
* `packages/ai/src/api-registry.ts` is only the **custom API** registry (§Q2.1) — not the model catalog.

### Q10.3 Auth

`packages/ai/src/auth/` is the credential engine: `store.ts` / `sqlite-credential-store.ts` (persistence),
`oauth.ts` + `registry/oauth/*` (per-provider flows), `refresh.ts`, `rotation.ts`, `pool.ts` (account
pools), `rank.ts`/`select.ts` (choose among siblings), `health.ts`, `resets.ts`/`usage-report.ts` (banked
usage-reset credits), `affinity.ts`, `cascade.ts`, `policy.ts`.

The harness sees only an `ApiKeyResolver`: `streamSimpleRequest` awaits `apiKeyResolver({lastChance, error,
signal})` and, when it throws, surfaces a `ConfigurationError` instead of masking it as "no API key"
(`stream.ts:1364-1377`). Retryable auth failures are buffered until the first replay-unsafe event so a 401
can rotate credentials without duplicating output (§Q2.1). `allowsMissingApiKey` providers skip the key
check entirely (:1380-1386). Subagent account pools (`task.agentAccountPools`) restrict a child to named
OAuth identities and fail rather than borrowing another account.

### Q10.4 Retry / error policy

Two tiers:

1. **Provider-owned** retries (bounded, replay-safe only): empty completions, strict-tool capability
   fallback, Azure transient retry, Codex WS→SSE fallback before replay-unsafe output
   (`docs/provider-streaming-internals.md:236-246`).
2. **Session-level** `TurnRecovery` (`docs/non-compaction-retry-policy.md`): classification requires
   `stopReason === "error"`, not context overflow, and a retryable `AIError` class (429/500/502/503/504,
   overload, rate/usage limit, transport/socket, premature WS close, provider-suggested retry) — **plus no
   replay-unsafe output** (committed text, images, tool calls, server-tool blocks block replay;
   thinking-only partials are safe to discard).
   Backoff: `min(baseDelayMs * 2^(attempt-1), 8000) * (75–100%)`, defaults `maxRetries 10`,
   `baseDelayMs 500`, `maxDelayMs 300000`. Context overflow is deliberately excluded and routed to
   compaction/promotion instead (:9-16). Model fallback chains (`retry.fallbackChains`,
   `retry.fallbackOn`) switch with delay 0.

Error taxonomy lives in `packages/ai/src/error/`: `classes.ts`, `retryable.ts`, `rate-limit.ts`,
`provider.ts`, `auth.ts`/`auth-classify.ts`, `body-error.ts`, `network-cause.ts`, `finalize.ts`,
`abort.ts`, `validation.ts`, `flags.ts`. `oneshot-retry.ts` is the bounded one-shot path for single-shot
requests (titles, summaries).

### Q10.5 Thinking / effort control

`ThinkingLevel` (`packages/agent/src/thinking.ts:8-19`) = `inherit | off | minimal | low | medium | high |
xhigh | max`. The loop reads it dynamically via `config.getReasoning()` on **every** model call
(`agent-loop.ts:2112`), so `/effort` mid-run takes effect next request. `disableReasoning` is separate.

Mapping to wire budgets is per provider (`stream.ts:1602-1640`):

```
ANTHROPIC_THINKING  = { minimal 1024, low 4096, medium 8192, high 16384, xhigh 32768, max 32768 }
BEDROCK_CLAUDE      = { minimal 1024, low 2048, medium 8192, high 16384, xhigh 16384, max 32768 }
```

plus `OUTPUT_FALLBACK_BUFFER = 4000` for the Anthropic budget-thinking `max_tokens` bump, and
`googleThinkingHeadroom` for Gemini (which counts thinking against `maxOutputTokens`). `:auto` is resolved
by a **classifier** (`src/auto-thinking/classifier.ts`): a `ChoiceQuestion` to the `judge` role, 3-class for
local on-device judges (`trivial|moderate|hard` → `low|high|xhigh`), clamped to the model's supported
efforts and never below `low`; failures fall back to a concrete level and the turn continues.

---

## Q11. The Rust native boundary

`crates/*` are Rust **napi-rs** addons (no WASM), loaded by `@oh-my-pi/pi-natives` through a validating ESM
loader that picks the right `.node` binary and re-exports generated named bindings
(`docs/natives-architecture.md:1-20`). The harness crosses that boundary for work where JS is the wrong
tool: **`crates/shell`** (PTY + process spawning, so `bash` gets a real terminal, job control, and
interrupt/background semantics), **`crates/builtins` + `crates/vfs` + `crates/xfs`** (grep/glob/read/write
primitives, workspace scans, file locks), **`crates/edit` + `crates/ast` + `crates/diff`** (hashline/apply-patch
editing, ast-grep matching and rewriting, structured diffs), **`crates/iso`** (worktree/overlay isolation
backends), **`crates/vcs`** (git operations, including the worktree baselines of §Q5.2), plus
`predict`, `voice`, `ast`. The agent loop itself never calls Rust directly: tools and session services call
typed `@oh-my-pi/pi-natives` exports, and results come back as plain JS values — which is why a tool result
is always a normal `AgentToolResult` regardless of whether the work happened in TS or Rust. Cancellation
crosses the boundary as an abort signal the native task polls (`docs/natives-rust-task-cancellation.md`).

---

## Checklist

| Q | Status | Notes |
|---|---|---|
| Q1 turn lifecycle | **answered** | loops, hooks, event vocabulary, termination — source-verified |
| Q2 streaming | **answered** | dispatch, dialects, partial JSON, abort layering, live steering, TTSR |
| Q3 context management | **answered** | thresholds verified; snapcompact/remote-compaction internals docs-only |
| Q4 tools | **answered** | inventory (29+3), dispatch, approval tiers, 19 URL schemes, spills |
| Q5 subagents | **answered** | fan-out, isolation backends, yield, IRC, park/revive — lifecycle from source header |
| Q6 advisor | **answered** | delivery matrix + guard from source; catch-up numbers from docs |
| Q7 extensibility | **answered** | hooks/extensions/MCP entry points located |
| Q8 sessions | **answered** | JSONL + tree semantics; storage backends listed, not read |
| Q9 system prompt | **answered** | single-render + project-context block, 5 s prep deadline, context-file rules |
| Q10 provider layer | **answered** | roles, registry, auth engine, retry constants, effort budgets |
| Q11 Rust boundary | **answered (paragraph)** | crate roles from layout + natives docs; individual crate internals not read |

### Unresolved / partial

* `packages/catalog` `models.json` was not read (explicitly excluded as huge); catalog **rule** semantics
  are therefore docs-level.
* `compaction/compaction-v2-streaming.ts` (912 lines), `compaction/openai.ts` (1184), `snapcompact` — the
  remote-compaction lanes are described from `docs/compaction.md`, not traced line by line.
* `agent-session.ts` is 16,913 lines; I traced the seams the loop depends on (event handling, prompt,
  retry, compaction entry points, TTSR) rather than every branch. Mode controllers (`modes/controllers/`)
  were out of scope (TUI worker).
* `spills` threshold is a **setting** (`tools.artifactSpillThreshold`, default 50 KB); other deployments may
  configure differently.
* `docs/stream.md` is about **terminal broadcasting**, not provider streaming — a doc-name trap; provider
  streaming is documented in `docs/provider-streaming-internals.md`.
