# oh-my-pi — Command Surface (static source analysis)

Repo: `C:\Users\shree\AppData\Local\hermes\cache\scratch\oh-my-pi`, commit `840cc4b6` (main, ~v18.22.2).
All findings are READ-ONLY static analysis of source + docs at that commit. Nothing was executed.
Labels: **[source-verified]** = read directly in the cited file; **[docs-only]** = only documented, not confirmed in the cited source path.

Paths are relative to the repo root. `CA` = `packages/coding-agent/src`, `TUI` = `packages/tui/src`.

---

## Q1 — CLI surface

### 1.1 Dispatch flow (how argv is routed)

`CA/cli.ts` is the only entry. Order of operations **[source-verified]**:

| Step | Where | Behavior |
|---|---|---|
| 1 | `cli.ts:442-444` | `omp zsh\|bash\|sh [args…]` → `runNativeShell(argv)` **before** profile bootstrap, flag parsing and the command table; the entire remaining argv belongs to the shell. |
| 2 | `cli.ts:448-452` | `argv[0] === SHELL_PROCESS_SELECTOR` (`__omp_worker_shell_process`) → `runShellProcess(argv.slice(1))`. |
| 3 | `cli.ts:453-494` | `extractProfileFlags` strips `--profile` / `--alias`; `setProfile` runs before any command routing. `--alias` creates a shell shortcut and exits. |
| 4 | `cli.ts:506` | `declareWorkerHostEntry()` (worker-host registration). |
| 5 | `cli.ts:513-522` | Any `argv[0]` starting with `__omp_worker_` → `runWorkerEntrypoint`; unknown selector = hard error. |
| 6 | `cli.ts:529-534` | `--license` → bundled notices, exit. |
| 7 | `cli.ts:535-540` | `--help/-h/--version/-v/help` (as argv[0]) pass through to the CLI runner. |
| 8 | `cli.ts:567-570` | `--smoke-test` → worker/dashboard smoke test. |
| 9 | `cli.ts:573-586` | `resolveCliArgv(argv)` (from `cli-commands.ts`) then `run({bin, version, argv, commands})`. |
| 10 | `cli-commands.ts:459-483` | If argv[0] is not a subcommand → argv is rewritten to `["launch", ...argv]`. A subcommand hidden behind global flags is **hoisted** to front (`omp --approval-mode=yolo acp` ⇒ `omp acp --approval-mode=yolo`). Leading launch-global flags are forwarded only to `LAUNCH_FLAG_COMMANDS` (`launch`, `acp`); for every other subcommand they are stripped (`cli-commands.ts:438-449`, #8891). |
| 11 | `cli-commands.ts:384-397` | Bare reserved words (`extensions`, `list`, `remove`, `uninstall`, `marketplace`, `discover`, `upgrade`, `enable`, `disable`) are rejected with a `omp plugin …` hint instead of being sent to the model (#4845). Multi-word forms only hint when they match plugin grammar. |

### 1.2 Subcommands — 56 registered (55 public + `__complete`) **[source-verified]**

Registry: `CA/cli-commands.ts:27-319`. Descriptions: `CA/cli/command-help.ts`. Per-command flags/args extracted from each `CA/commands/*.ts` `static flags` / `static args`.

| # | Command (aliases) | Purpose | Positionals | Key flags |
|---|---|---|---|---|
| 1 | `launch` | Default command; start a coding session | `messages…` | full launch surface (see 1.3) |
| 2 | `acp` | Run omp as an ACP server over stdio | — | accepts launch-global flags (`LAUNCH_FLAG_COMMANDS`) |
| 3 | `auth` | Export/import stored provider credentials as JSON | `action`, `file` | — |
| 4 | `auth-broker` | Manage the credential vault | `action`, `source` | `--json --bind --regenerate --via --provider --include-disabled --from-local --include-env --include-oauth --dry-run --trust-proxy-headers` |
| 5 | `auth-gateway` | HTTP forward proxy (`serve`) or stdio JSON lines | `action` | `--json --bind --regenerate --no-auth --trust-proxy-headers --clients --strict` |
| 6 | `agents` | Manage bundled task agents (`unpack` exports them) | `action` | `--force --json --dir --user --project` |
| 7 | `anonymize` | Redacted shareable copy of a session + subagents | `session` | `--out` |
| 8 | `bench` | TTFT/prefill vs decode benchmark, p50/p95 | `models…` | `--runs --max-tokens --prompt --profile --prefill-bytes --detailed --service-tier --account --json --par --cache…` |
| 9 | `browser-relay` | Local CDP relay for the browser prelude | `action` | `--port --token --dir --no-group --verbose` |
| 10 | `cleanse` | Detect+fix project diagnostics via parallel subagents | `request` | `--agents --model --tests --all` |
| 11 | `collab` | List collab hosts; `collab link <id\|pid>` prints a control link | `action`, `selector` | `--view --json` |
| 12 | `commit` | Generate a commit message, update changelogs | — | `--push --dry-run --no-changelog --legacy --context --model` |
| 13 | `completions` | Print a shell completion script | `shell` (bash\|zsh\|fish) | — |
| 14 | `__complete` (hidden) | Dynamic completion candidates (`models`, `sessions`) | `kind [-- prefix]` | `static hidden`, `static strict=false` (`CA/commands/complete.ts:16-29`) |
| 15 | `compress` | Rewrite a text file into dense prompt register | `files…` | `--out --inPlace --rounds --agents --model` |
| 16 | `config` | Manage configuration settings | `action key value` | `--json --scope` |
| 17 | `dry-balance` | Dry-run OAuth account balancing | `model` | `--model --count --concurrency --json --bench --config` |
| 18 | `find` | Semantic search for implementing files/line ranges | `query path` | `--keyword --hidden --json --quiet --no-judge --no-spotlight --offline` |
| 19 | `gc` | Storage garbage collection | — | `--apply --json --agent-dir --blobs --archive --wal --stale --compress --strip --cold-archive-after-days --retain…` |
| 20 | `grep` | Exercise the grep tool from the CLI | `pattern path` | `--glob --limit --context --files --count --no-gitignore` |
| 21 | `gallery` | Deterministic renderer gallery (tool/composer/status) | — | `--surface --tool --composer --segment --state --width --expanded --plain --screenshot --out --font --font-size` |
| 22 | `git` | Fullscreen git UI (diff, staging, commit composer) | `revision` | `--dir` |
| 23 | `grievances` | View/clean/push auto-QA tool issues | `action` | `--limit --tool --json --id --all` |
| 24 | `images` (`img`) | Inspect/diagnose/probe/purge image backends | `action` | `--json --apply --all --dir --timeout` |
| 25 | `if-bench` | Instruction-following / working-memory benchmark | `models…` | `--turns --length --max-tokens --nya-max --par --json` |
| 26 | `install` | Install/link an extension package (alias of `plugin install`/`link`) | `targets…` | `--json --force --dry-run --scope` |
| 27 | `join` | Join a shared collab session (same as `/join`) | `link` | — (`Args.join` is set internally, no flag — `CA/cli/args.ts:78-79`) |
| 28 | `login` | Provider login from the terminal (counterpart of `/login`) | `provider` | — |
| 29 | `models` | List/search/refresh models | `action pattern` | `--json --kind --extension --no-extensions --config` |
| 30 | `plugin` (`plugins`) | Manage plugins | `action targets…` | `--json --fix --force --dry-run --local --enable --disable --set --scope` |
| 31 | `predict` | Compare word-completion engines' ghost text | — | — |
| 32 | `ps` | List/control daemon-supervised background processes | `action name value` | `--all --json --plain --dir --follow --head --lines --grep --timeout --signal` |
| 33 | `say` | Local TTS synthesis + playback | `text…` | `--voice --speed --model --file --out` |
| 34 | `clip` | Upload a `/record` recording to live.omp.sh | `file` | `--title --description --server` |
| 35 | `play` | Replay a `/record` recording (space pause, q quit) | `file` | `--speed --idle-limit` |
| 36 | `share` | Share a saved session via encrypted link (same as `/share`) | `session` | `--gist` |
| 37 | `session` | Hand a session to another machine (`pack`/`unpack`/`moved`) or `lease` | `action source output` | `--cwd --to --json` |
| 38 | `setup` | Onboarding / optional-feature deps | `component` | `--check --json` |
| 39 | `shell` | Interactive shell console | — | `--cwd --timeout --no-snapshot` |
| 40 | `zsh` | Run zsh 5.9 on the built-in shell engine; argv passed through | `args…` | none parsed by omp (`cli.ts:442-444`) |
| 41 | `bash` | Run bash 5.3 on the built-in shell engine | `args…` | as above |
| 42 | `sh` | Run POSIX sh on the built-in shell engine | `args…` | as above |
| 43 | `read` | Show what the read tool returns for a path/URL/internal URI | `path` | — |
| 44 | `render` | Draw a whole session thread through the production transcript pipeline | `session` | `--width --height --timing --repaint --plain --quiet` |
| 45 | `skill` (`skills`) | Install/search/publish skills on skills.omp.sh | `action targets…` | `--global --yes --json --sort --scope --tag --dry-run --allow-secrets --registry --undo --package --expires` |
| 46 | `ssh` | Manage SSH host configs | `action targets…` | `--json --host --user --port --key --desc --compat --scope` |
| 47 | `stats` | Local usage dashboard | — | `--port --host --json --summary` |
| 48 | `stream` | Broadcast session screens/chat to a live channel | — | `--title --server --no-tui` |
| 49 | `update` | Check/install updates | — | `--force --check --plugins --canary --stable` |
| 50 | `usage` | Provider usage limits; `clients`/`accounts`/`reset` sub-actions | `action target` | `--json --provider --redact --history --days --extension --no-extensions` |
| 51 | `tiny-models` | Download tiny local models (titles, memory, completion) | `action model` | `--json` |
| 52 | `token` | Get API key / OAuth token for a provider | `provider` | `--raw --force-refresh --account --list` |
| 53 | `toks` | Count tokens with embedded offline tokenizers | `input…` | `--json` |
| 54 | `ttsr` | Inspect/test Time-Traveling Stream Rules | `action snippet` | `--file --rule --source --tool --path --agent --verbose --json --no-gitignore --max-bytes` |
| 55 | `worktree` (`wt`) | Add/list/clear git worktrees | `action path commit` | `--cwd --branch --force-branch --detach --quiet --all --dry-run --json` |
| 56 | `search` (`q`, `web-search`) | Test web search providers | `query` | `--model --recency --limit --compact` |

Alias map (`cli-commands.ts:150,181,259,310,316`): `img→images`, `plugins→plugin`, `skills→skill`, `wt→worktree`, `q`/`web-search→search`.

Docs-vs-code drift: `docs/cli-reference.md:267-317` lists **49** command rows and **omits `anonymize`, `auth`, `session`, and `__complete`** (the last is mentioned only in prose at :324). Alias spellings (`img`, `plugins`, `skills`, `wt`, `q`, `web-search`) are documented inline in the same cells.

### 1.3 Global launch flags **[source-verified: `CA/cli/flag-tables.ts`, `CA/cli/args.ts`]**

Authoritative classification lives in exactly one place (`flag-tables.ts:1-31` comment: "a string-valued flag exists in this CLI surface iff it has an entry here").

**String-valued (`STRING_SETTERS`, `flag-tables.ts:133-294`)** — each consumes the next token even if it looks like a flag:
`--cwd --config --add-dir --mode --fork --provider --model --smol --slow --goal --plan --prewalk-into --plan-yolo-into --max-time --warn-cost --max-cost --service-tier --spinner-interval --max-fps --motion --api-key --system-prompt --system-prompt-template --append-system-prompt --provider-session-id --prompt-cache-key --session-dir --models --tools --thinking --export --hook --extension/-e --trusted-extension --plugin-dir --skills --approval-mode`.

**Optional-value (`OPTIONAL_FLAGS`, `flag-tables.ts:303-308`)**: `--resume`, `-r`, `--restore`, `--session` (bare form ⇒ picker; `rejectEmpty: true`).
`--restore` also sets `resume` and (bare) keeps an already named session (`flag-tables.ts:96-99`).

**Valued enum checks**: `--mode` ∈ {text,json,rpc,acp,rpc-ui}; `--thinking` ∈ CLI_THINKING_LEVELS; `--approval-mode` ∈ {always-ask,always-ask-cached,write,yolo} (`flag-tables.ts:143-151, 253-262, 283-293`).

**Boolean / valueless (`args.ts:344-411` + `VALUELESS_FLAGS` `flag-tables.ts:352-383`)**:
`--help/-h --version/-v --allow-home --continue/-c --from-claude --from-codex --no-session --no-tools --no-lsp --no-pty --no-judge --no-spotlight --offline --hide-thinking --advisor --wait-async --external-thinking --prewalk --no-prewalk --plan-yolo --print/-p --print-thoughts --no-extensions --no-skills --no-rules --no-context-files --no-title --no-ui --auto-approve/--yolo --profile --alias`.

**Extension-shadowable**: only `--plan` (`flag-tables.ts:324`) — a bundled boolean extension can own `--plan`, in which case a value-like successor is its value but a flag-looking successor stays a separate flag. `parseArgs` gives extension flags precedence over built-ins (`args.ts:294-312`).

**`--` separator**: after it everything is literal prompt text (`args.ts:266-275, 424-428`). `@path` positionals become file attachments (`args.ts:412-419`).

**Validation rules** (hard errors):
- `--goal` requires interactive, fresh session, and no positional/@file/stdin prompt, no `--plan-yolo`/`--no-tools`, and `goal.enabled` (`args.ts:145-207`).
- `--restore` requires interactive, no continue/fork/import/goal/`--no-session`, and takes no prompt (`args.ts:160-181`).
- `--system-prompt` + `--system-prompt-template` mutually exclusive (`args.ts:463-465`).
- `--trusted-extension` must be absolute, non-empty, and cannot combine with `-e`/`--hook` (`args.ts:445-461`).
- Unknown flags are a hard error after the extension-aware reparse (`args.ts:509-519`).

**Drift**: `CA/commands/launch-help.ts:17-157` (the `--help` table) omits **`--fork`, `--session`, `--provider-session-id`, `--prompt-cache-key`, `--trusted-extension`, `--plugin-dir`** even though `args.ts` parses them. Conversely `launch-help.ts` adds nothing that `args.ts` cannot parse. `--plugin-dir` is only mentioned in the extra help text (`CA/cli/help-extra.ts:106`), and `--fork` only in `docs/cli-reference.md:89`.

### 1.4 Hidden worker selectors **[source-verified]**

Selector prefix is reserved (`packages/utils/src/worker-host.ts:4`): `__omp_worker_`. Any argv[0] with that prefix dispatches through `runWorkerEntrypoint` (`cli.ts:205-321`); an unrecognized one exits 1.

| Selector | Defined | Entry loaded |
|---|---|---|
| `__omp_worker_tiny_inference` | `cli.ts:196` | `./tiny/worker` |
| `__omp_worker_stats_sync` | `cli.ts:197` | `@oh-my-pi/omp-stats/sync-worker` |
| `__omp_worker_stats_spotlight` | `cli.ts:198` | `@oh-my-pi/omp-stats/spotlight-worker` |
| `__omp_worker_tab` | `cli.ts:199` | `./tools/browser/tab-worker-entry` |
| `__omp_worker_js_eval` | `cli.ts:200` | `./eval/js/worker-entry` |
| `__omp_worker_js_eval_process` | `cli.ts:201` | `./eval/js/process-entry` (IPC subprocess) |
| `__omp_worker_stt` | `cli.ts:202` | `./stt/asr-worker` |
| `__omp_worker_tts` | `cli.ts:203` | `./tts/tts-worker` |
| `__omp_worker_mnemopi_embed` | `mnemopi/embed-protocol` | `./mnemopi/embed-worker` |
| `__omp_worker_stats_activity` | `cli/worker-selectors.ts:17` | `./stats/activity-worker` |
| `__omp_worker_parent_watchdog` | `cli/worker-selectors.ts:13` | `./subprocess/parent-watchdog-worker` |
| `__omp_worker_daemon` | `daemon/protocol` | `./daemon/server` |
| `__omp_worker_auto_update` | `cli/worker-selectors.ts:9` | `./cli/auto-update` |
| `__omp_worker_computer` | `cli/worker-selectors.ts:11` | `./tools/computer/worker-entry` |
| `__omp_worker_subagent` | `cli/worker-selectors.ts:15` | `./thread/subagent-worker-entry` |
| `__omp_worker_shell_process` | `packages/utils/src/worker-host.ts:11` | `./exec/shell-process-worker` (dispatched earlier, `cli.ts:448`) |

Workers that use `parentPort` install a buffering inbox before importing their module so Bun's pre-spawn message flush is not lost (`worker-host.ts:38-96`; `cli.ts:233-305`).

### 1.5 How completions are generated from live metadata **[source-verified]**

- `omp completions <bash|zsh|fish>` (`CA/commands/completions.ts:33-60`) awaits **every** `commands` entry's `load()` and builds a `CliConfig` from the live registry (`completions.ts:19-31`), then `generateCompletion(shell, buildSpec(...))`.
- `buildSpec` walks the same declarative `static flags` / `static args` metadata `--help` renders (`CA/cli/completion-gen.ts:1-16`), so a new flag propagates with no edit here. Static candidates (enums, `BUILTIN_TOOL_NAMES`) are baked into the script (`completion-gen.ts:65-90`).
- Dynamic candidates call back into the hidden `omp __complete <kind>`: `MODEL_FLAGS={model,smol,slow,plan}`, `SESSION_FLAGS={resume,restore,fork,session}`, `DIR_FLAGS={session-dir,plugin-dir}` (`completion-gen.ts:68-72`). `__complete` prints `value\tdescription` lines — `models` from the bundled catalog (both `provider/id` and bare `id`), `sessions` from `SessionManager.list(cwd)` (`commands/complete.ts:20-67`).

---

---

## Q2 — Slash commands

### 2.1 Registration mechanism **[source-verified]**

`CA/slash-commands/builtin-registry.ts` is the single registry:

1. `BUILTIN_SLASH_COMMAND_REGISTRY` is a concatenation of **eight** per-area arrays (`builtin-registry.ts:44-53`): modes, collaboration, session, commit, lifecycle, marketplace, skills, control.
2. A `Map` is built from `name` **and every alias** (`:55-61`); `BUILTIN_SLASH_COMMAND_RESERVED_NAMES` is the key set (`:63`), used to stop custom/file commands from shadowing builtins.
3. `BUILTIN_SLASH_COMMAND_DEFS` projects specs into TUI autocomplete metadata (`:66-81`), materializing per-command `getArgumentCompletions` / `getInlineHint` from declarative `subcommands` / `inlineHint` (`:83-127`).
4. Dispatch: `executeBuiltinSlashCommand(text, runtime)` (`:147-202`) —
   - `parseSlashCommand` splits the line; no `/` ⇒ `false` (falls through to prompt);
   - unknown name ⇒ `false`;
   - **args on a command without `allowArgs` ⇒ `false`** (treated as plain text, `:156-158`);
   - collab guests: anything not in `COLLAB_GUEST_ALLOWED_COMMANDS` is refused with `/<cmd> is host-only during a collab session` and the draft is restored (`:161-170`); the allowlist lives in `CA/collab/guest.ts`;
   - prefers `handleTui`; otherwise adapts the text-mode `handle` (output → status line, plugin reload wired) (`:171-200`).
5. A second dispatcher exists for ACP/text: `executeAcpBuiltinSlashCommand` (`CA/slash-commands/acp-builtins.ts:58-69`) requires `handle` — TUI-only specs are skipped — and advertises only those in ACP `available_commands` (`acp-builtins.ts:32-51`, `available-commands.ts:46-62`).

**Parsing** (`CA/slash-commands/helpers/parse.ts:20-38`): the separator is the **earliest** whitespace *or* `:` — so `/foo bar` and `/foo:bar` both parse as name `foo`, args `bar`. `/force:` is therefore also a literal alias. `parseSubcommand` lowercases the verb and splits on first whitespace (`parse.ts:78-84`).

**TUI submit path** (`CA/modes/controllers/input-controller.ts`): `parseSlashCommand(text)?.name` decides `draftDetached` (`:1212-1214`; `DETACHED_DRAFT_COMMANDS` at `:208-218` = `plan, vibe, goal, guided-goal, tan, btw, omfg, handoff, cleanse` — commands whose handler takes ownership of the submitted draft, images included); a bare command can require a second Enter (`:1228-1237`); then `executeBuiltinSlashCommand` runs (`:1248`). `true` ⇒ consumed; a **string** return is the residual prompt (`:1259-1269`). After builtins, the same submit path tries: `side:true` file commands → `/btw`, then `/skill:<name>` (`:1299-1330`).

**Resolution order for "is this a command"** is spelled out in `CA/modes/utils/ui-helpers.ts:1440-1460`: builtin (with allowArgs check) → extension command (`extensionRunner.getCommand`) → custom TS command → file (markdown) command.

### 2.2 Builtin slash commands — 89 **[source-verified]**

Definition files under `CA/slash-commands/`. "Surface" column: **T** = TUI-only (`handleTui`), **A** = has text-mode `handle` (reachable from ACP/print), **A+T** = both.

| Command (aliases) | Args / subcommands | What it does | Defined | Surface |
|---|---|---|---|---|
| `/security` | plan, scan, status, cancel, scans, show, import, export, validate, compare, disposition | Plan/run/inspect/import/compare OMP-native security scans | builtin-modes.ts:237 | A |
| `/settings` | — | Open settings menu | builtin-modes.ts:258 | T |
| `/setup` (`providers`) | `providers` | Open provider setup | builtin-modes.ts:267 | T |
| `/plan` | args | Toggle plan mode (agent plans before executing) | builtin-modes.ts:285 | T |
| `/plan-review` | — | Re-open plan review for the latest plan | builtin-modes.ts:306 | T |
| `/vibe` | args | Toggle vibe mode (persistent fast/good worker sessions; read-only director toolset) | builtin-modes.ts:317 | T |
| `/goal` | set, show, pause, resume, drop, budget | Toggle goal mode (persistent autonomous objective) | builtin-modes.ts:335 | T |
| `/guided-goal` | args | Agent interviews you, then sets up goal mode | builtin-modes.ts:361 | T |
| `/loop` | `[count\|duration] [--while\|--until '<cmd>'] [prompt]` | Toggle loop mode: re-submits the next prompt after every yield, bounded/gated | builtin-modes.ts:373 | T |
| `/queue` | `<message>` | Queue a message for after the agent yields | builtin-modes.ts:400 | T |
| `/model` (`models`) | — | Switch model for this session (picker) | builtin-modes.ts:413 | A+T |
| `/switch` | `[model]` | Session-only model switch; fuzzy id, `provider/id`, `@role`, `:level` | builtin-modes.ts:457 | A+T |
| `/fast` | on, ultra, off, status | Toggle fast service tier (OpenAI priority/ultrafast, Anthropic speed=fast, Google priority) | builtin-modes.ts:508 | A+T |
| `/slow` | on, off, status | Toggle slow mode (flex tier; Anthropic low priority at session limit) | builtin-modes.ts:536 | A+T |
| `/skillful` | on, off, status | Toggle listing available skills in the system prompt (session only) | builtin-modes.ts:564 | A+T |
| `/extended-context` | on, off, status | Toggle extended context windows | builtin-modes.ts:620 | A+T |
| `/computer` | on, off, status | Toggle the native computer-use eval prelude | builtin-modes.ts:647 | A+T |
| `/ratchet` | args | Build/reuse an eval for an LLM flow, then hillclimb it unattended | builtin-modes.ts:692 | A+T |
| `/prewalk` | `restart`, `off` | Arm / restart / cancel a one-shot model handoff | builtin-modes.ts:726 | A |
| `/modelpreset` | list, save, switch, delete | Save and switch model presets (role models + thinking level) | builtin-modes.ts:801 | A+T |
| `/effort` | `[level]` | Set reasoning effort / thinking level; Shift+Tab cycles | builtin-modes.ts:851 | A+T |
| `/advisor` | on, off, status, dump, configure | Toggle the advisor (second model reviewing each turn) | builtin-collaboration.ts:63 | A+T |
| `/export` | `[path]` | Export session to HTML | builtin-collaboration.ts:180 | A+T |
| `/trace` | — | Open this session's trace in the stats dashboard | builtin-collaboration.ts:204 | A+T |
| `/dump` | `all`, `anon` | Copy transcript to clipboard + write LLM request JSON; `all` = zip incl. subagents | builtin-collaboration.ts:231 | A+T |
| `/share` | — | Share session via encrypted link (share server or secret gist) | builtin-collaboration.ts:293 | A+T |
| `/collab` | view, list, status, stop | Share this session live via a relay | builtin-collaboration.ts:319 | T |
| `/join` | `<link>` | Join a shared collab session | builtin-collaboration.ts:448 | T |
| `/leave` | — | Leave the collab session | builtin-collaboration.ts:480 | T |
| `/browser` | headless, visible | Toggle browser prelude headless vs visible | builtin-collaboration.ts:505 | A+T |
| `/copy` | args | Pick text/code from the conversation to copy | builtin-collaboration.ts:575 | T |
| `/open` | args | Open the last link (or pick one) in the browser | builtin-collaboration.ts:627 | T |
| `/todo` | edit, copy, expand, collapse, export, import, append, start, done, drop, rm | View/modify the agent todo list | builtin-session.ts:209 | A+T |
| `/session` | info, delete, pin | Session management | builtin-session.ts:247 | A+T |
| `/jobs` | full, kill | Async background jobs status | builtin-session.ts:334 | A+T |
| `/usage` | show, reset | Provider usage and limits; spend a saved reset | builtin-session.ts:410 | A+T |
| `/stats` | args | Launch the local stats dashboard | builtin-session.ts:457 | A+T |
| `/changelog` | full, last | Show changelog entries | builtin-session.ts:495 | A+T |
| `/hotkeys` | — | Show all keyboard shortcuts | builtin-session.ts:524 | T |
| `/tools` | — | Show tools currently visible to the agent | builtin-session.ts:533 | A+T |
| `/context` | — | Browse context usage and content | builtin-session.ts:562 | A+T |
| `/extensions` (`status`) | — | Extension Control Center dashboard | builtin-session.ts:581 | T |
| `/agents` | — | Agents hub (per-agent model, prewalk, advisor) | builtin-session.ts:591 | T |
| `/git` | args | Fullscreen git UI | builtin-session.ts:600 | T |
| `/local` | args | Open a session `local://` file in `$EDITOR` | builtin-session.ts:611 | T |
| `/hub` (`subagents`) | — | Live Agent Hub | builtin-session.ts:622 | T |
| `/branch` (`rewind`) | — | Rewind to a previous message, keeping the old path as a branch | builtin-session.ts:632 | T |
| `/fork` | pane, window, tab | Fork session, or open it in a multiplexer pane/window/tab | builtin-session.ts:642 | T |
| `/tree` | — | Navigate session tree (switch branches) | builtin-session.ts:682 | T |
| `/login` | `[provider]` | OAuth login | builtin-session.ts:691 | T |
| `/logout` | `[provider]` | OAuth logout | builtin-session.ts:744 | T |
| `/mcp` | add, list, remove, test, reauth, unauth, enable, disable, reconnect, reload, resources, prompts, notifications, help | Manage MCP servers | builtin-session.ts:767 | A+T |
| `/commit` | args | Commit only changes this agent + subagents made | builtin-commit.ts:15 | A |
| `/ssh` | add, list, remove, help | Manage SSH hosts | builtin-lifecycle.ts:229 | A+T |
| `/new` | — | Start a new session | builtin-lifecycle.ts:252 | T |
| `/fresh` | — | Reset provider stream state without changing the local transcript | builtin-lifecycle.ts:261 | A+T |
| `/clear` | — | Clear conversation context in place, keeping the session | builtin-lifecycle.ts:283 | T |
| `/delete` | — | Delete the current session and start a new one | builtin-lifecycle.ts:294 | T |
| `/compact` | modes (COMPACT_MODES) `[focus]` | Manually compact the context | builtin-lifecycle.ts:303 | A+T |
| `/shake` | elide, images, thinking | Drop heavy content from context | builtin-lifecycle.ts:322 | A+T |
| `/handoff` | `[focus instructions]` | Background compaction with a handoff summary | builtin-lifecycle.ts:351 | A+T |
| `/resume` | `[session]` | Resume a different session | builtin-lifecycle.ts:360 | T |
| `/pin` | `[session]` | Pin a session in the resume list | builtin-lifecycle.ts:396 | A |
| `/unpin` | `[session]` | Unpin a session | builtin-lifecycle.ts:411 | A |
| `/btw` | args | Side question, or browse BTW history | builtin-lifecycle.ts:426 | T |
| `/tan` | args | Full background agent on tangential work | builtin-lifecycle.ts:442 | T |
| `/omfg` | args | Forge a TTSR rule from a complaint | builtin-lifecycle.ts:455 | T |
| `/cleanse` | args | Detect+fix diagnostics with parallel subagents | builtin-lifecycle.ts:468 | T |
| `/retry` | — | Retry the last failed agent turn | builtin-lifecycle.ts:481 | A+T |
| `/debug` | — | Debug tools selector | builtin-lifecycle.ts:516 | T |
| `/memory` | view, stats, diagnose, queue, sync, clear, reset, enqueue, rebuild, `mm list/show/refresh/history/seed/delete/reload` | Memory maintenance | builtin-lifecycle.ts:525 | A+T |
| `/rename` | `[title]` | Rename session (omit to generate) | builtin-lifecycle.ts:615 | A+T |
| `/move` | `<dir>` | Move session to another directory | builtin-lifecycle.ts:693 | A+T |
| `/wt` (`worktree`) | args | Move session into a new worktree, changes included | builtin-lifecycle.ts:723 | A+T |
| `/add-dir` | `<dir>` | Add a workspace directory (multi-root) | builtin-lifecycle.ts:758 | A |
| `/remove-dir` | `<dir>` | Remove a workspace directory | builtin-lifecycle.ts:790 | A |
| `/dirs` | — | List workspace directories | builtin-lifecycle.ts:819 | A |
| `/exit` | — | Exit the application | builtin-lifecycle.ts:828 | T |
| `/restart` | — | Restart omp with the same launch flags, resuming this session + subagents | builtin-lifecycle.ts:833 | T |
| `/marketplace` | add, remove, update, list, discover, install, uninstall, installed, upgrade, help | Manage marketplace plugin sources/plugins | builtin-marketplace.ts:46 | A+T |
| `/plugins` (`plugin`) | list, enable, disable | View/manage installed plugins | builtin-marketplace.ts:426 | A |
| `/reload-plugins` | — | Reload all plugins (skills, commands, hooks, tools, agents, MCP) | builtin-marketplace.ts:490 | A+T |
| `/skills` | search, install, installed, update | Install/update skills from skills.omp.sh | builtin-skills.ts:66 | T |
| `/force` (`force:`) | `<tool-name> [prompt]` | Force the next turn to use a specific tool | builtin-control.ts:44 | A+T |
| `/live` | — | Start Codex-backed realtime voice mode | builtin-control.ts:94 | T |
| `/record` | — | Start/stop screen recording for `omp play` | builtin-control.ts:103 | T |
| `/pause` | — | Freeze all agents (main, subagents, advisor) | builtin-control.ts:112 | T |
| `/forget-approvals` | — | Forget every file/folder "Ask and remember" approval | builtin-control.ts:122 | A |
| `/quit` (`q`) | — | Quit the application | builtin-control.ts:132 | T |

Counts: **89** builtins; **48** expose a text-mode `handle` (ACP-visible), **41** are TUI-only. **9** aliases: `models`, `providers`, `status`, `subagents`, `rewind`, `worktree`, `plugin`, `force:`, `q`.

### 2.3 Non-builtin command sources **[source-verified]**

| Source | Discovery | Notes |
|---|---|---|
| **Bundled TS custom commands** (3) | `CA/extensibility/custom-commands/loader.ts:159-183` | `/green` (`bundled/ci-green/index.ts:47`, "Generate a prompt to iterate on CI failures until the branch is green"), `/review` (`bundled/review/index.ts:620`, "Launch interactive code review"), `/annotate` (`bundled/annotate/index.ts:346`, "Annotate a diff or text from a file, prompt, latest reply, or session"). |
| **User/project TS commands** | `loader.ts:86-147` | `<agentDir>/commands/<name>/index.{ts,js,mjs,cjs}` plus `getConfigDirs("commands")` — i.e. `.omp/commands`, `.claude/commands`, `.codex/commands`, `.gemini/commands` at user and project level (`CA/config.ts:17-22, 133-162`). Each dir must contain `index.*`; symlinked dirs accepted; dot-dirs skipped. A user/project command may **override** a bundled one; user-vs-project name conflicts are a load error (`loader.ts:226-254`). |
| **Markdown file commands** | `CA/extensibility/slash-commands.ts:77-125` | `loadCapability(slashCommandCapability)` — providers: builtin/native (`discovery/builtin.ts:370-399`, `<configdir>/commands/*.md`), agents, Claude (`discovery/claude.ts:610`), Claude plugins (`claude-plugins.ts:723`), Codex (`codex.ts:565`), OpenCode (`opencode.ts:532`), omp plugins (`omp-plugins.ts:384`). Frontmatter: `description`, `argument-hint`, `side: true`. Namespaced names (`plugin:cmd`) survive parsing because `parseFileSlashInvocation` takes the whole first token (`slash-commands.ts:131-136`). |
| **Embedded markdown command** (1) | `CA/task/commands.ts:13` + `slash-commands.ts:104-122` | `/init` (from `prompts/agents/init.md`), added only if no discovered command already uses the name. |
| **Skills as commands** | `CA/modes/skill-command.ts`, `CA/extensibility/skills.ts:794-796` | `/skill:<name> [args]`; gated by `skills.enableSkillCommands` (`extensibility/settings.ts:32`). Also accepted as a mid-prompt `/skill:<name>` token (`skills.ts:798-839`). |
| **Extension commands** | `CA/modes/utils/ui-helpers.ts:1449-1451`; ACP advertises them via `available-commands.ts:75-86` | Registered by extensions through the extension runner; names colliding with an ACP builtin (or a colon-namespaced prefix that is one) are filtered (`acp-builtins.ts:15-29`). |
| **MCP prompts** | `available-commands.ts:88-96` | Surfaced as custom commands whose `path` starts with `mcp:`. |

### 2.4 Argument passing & mode differences

- Builtin args are the raw remainder of the line (`parseSlashCommand` trims), then each handler parses its own verb with `parseSubcommand` (`parse.ts:78-84`); usage errors return `commandConsumed({failed:true})` (`parse.ts:66-75`) which print mode converts to a nonzero exit (`print-mode.ts:302-306, 366`).
- **File commands** are template-expanded, not "handled": `expandSlashCommand` → `parseCommandArgs` + `substituteArgs` + `prompt.render` with `{args, ARGUMENTS, arguments}` (`slash-commands.ts:139-157`), invoked from `CA/session/agent-session.ts:9269`. `side: true` commands run as a `/btw` side question in the TUI (`input-controller.ts:1299-1309`).
- **TUI**: full dispatch incl. `handleTui`-only commands, dashboards, pickers, draft detach/restore.
- **ACP** (`--mode acp`, `acp` subcommand): only commands with `handle`; advertised through `available_commands` (`modes/acp/acp-agent.ts:2764`).
- **Print/text mode** (`-p`, `--mode text|json|rpc`): **only a whitelist is dispatched** — `PRINT_DISPATCHED_COMMANDS = {compact, handoff, retry}` plus `/mcp reauth` (`print-mode.ts:327, 338-344`). Everything else keeps the historical behavior of being sent to the model as literal text. Only **CLI-authored** messages are eligible; stdin/@file merged input is never dispatched (`print-mode.ts:328-337`).

---

---

## Q3 — Keybindings

### 3.1 Mechanism **[source-verified]**

- Two declaration-merged tables: `TUI_KEYBINDINGS` (32 actions, `TUI/keybindings.ts:58-142`) and the app table (47 actions) spread into `KEYBINDINGS` (`TUI/app-keybindings.ts:95-296`). `AppKeybindings` is merged into the `Keybindings` interface via `declare module` (`app-keybindings.ts:79-81`). **Total: 79 rebindable actions.**
- `KeybindingsManager` (`TUI/keybindings.ts:247-338`) resolves each action to `userKeys ?? defaultKeys`, normalizes ids to lowercase, and precomputes a match-key set with aliases (`addKeyAliases`, `:222-228`, which also adds `shift+<symbol>` for shifted punctuation). `canonicalKeyId` (`:190-220`) normalizes modifier order (`ctrl,shift,alt,super`), `esc→escape`, `return→enter`, and promotes an uppercase base letter to `shift+<letter>`.
- User config: `~/.omp/agent/keybindings.yml` (also `.yaml`; legacy `keybindings.json` is read and **migrated to YAML**) — `app-keybindings.ts:440-506`. Format: flat YAML map `action-id: chord` or `action-id: [chord, …]`; `[]` disables (`docs/keybindings.md:7-25`). There is **no** nested `keybindings:` object in `config.yml` (`docs/keybindings.md:9`, [docs-only]).
- Named profiles: default profile's file is loaded first, profile file overrides action-by-action; inherited file is read-only during a named-profile startup (`app-keybindings.ts:515-548`).
- Legacy action names are migrated (`KEYBINDING_NAME_MIGRATIONS`, `:301-367`), e.g. `interrupt→app.interrupt`, `submit→tui.input.submit`, `toggleSessionNamedFilter→app.session.togglePath`.
- Conflict detection: two **user** bindings claiming one key are recorded (`keybindings.ts:265-279`, `getConflicts()`); defaults may overlap because they apply in different contexts (see 3.4).

### 3.2 TUI actions (editor / input / select) — 32 **[source-verified `TUI/keybindings.ts:58-142`]**

| Action | Default keys | Meaning |
|---|---|---|
| `tui.editor.cursorUp` | `Up` | Move cursor up |
| `tui.editor.cursorDown` | `Down` | Move cursor down |
| `tui.editor.cursorLeft` | `Left`, `Ctrl+B` | Move cursor left |
| `tui.editor.cursorRight` | `Right`, `Ctrl+F` | Move cursor right |
| `tui.editor.cursorWordLeft` | `Alt+Left`, `Ctrl+Left`, `Alt+B` | Word left |
| `tui.editor.cursorWordRight` | `Alt+Right`, `Ctrl+Right`, `Alt+F` | Word right |
| `tui.editor.cursorLineStart` | `Home`, `Ctrl+A` | Line start |
| `tui.editor.cursorLineEnd` | `End`, `Ctrl+E` | Line end |
| `tui.editor.jumpForward` | `Ctrl+]` | Jump to character |
| `tui.editor.jumpBackward` | `Ctrl+Alt+]` | Jump backward to character |
| `tui.editor.pageUp` / `pageDown` | `PgUp` / `PgDn` | Page scroll |
| `tui.editor.deleteCharBackward` | `Backspace` | Delete char backward |
| `tui.editor.deleteCharForward` | `Delete`, `Ctrl+D` | Delete char forward |
| `tui.editor.deleteWordBackward` | `Ctrl+W`, `Alt+Backspace`, `Ctrl+Backspace`, `Super+Alt+Backspace` | Delete word backward |
| `tui.editor.deleteWordForward` | `Alt+Delete`, `Alt+D`, `Ctrl+Delete`, `Super+Alt+Delete`, `Super+Alt+D` | Delete word forward |
| `tui.editor.deleteToLineStart` | `Ctrl+U` | Delete to line start |
| `tui.editor.deleteToLineEnd` | `Ctrl+K` | Delete to line end |
| `tui.editor.yank` | `Ctrl+Y` | Yank |
| `tui.editor.yankPop` | `Alt+Y` | Yank pop |
| `tui.editor.undo` | `Ctrl+-`, `Ctrl+_` | Undo |
| `tui.editor.spellingSuggestions` | `Ctrl+.` | Spelling replacements |
| `tui.input.newLine` | `Shift+Enter`, `Ctrl+J` | Insert newline |
| `tui.input.submit` | `Enter` | Submit |
| `tui.input.tab` | `Tab` | Tab / autocomplete |
| `tui.input.copy` | `Ctrl+C` | Copy selection |
| `tui.select.up` / `down` | `Up` / `Down` | Move selection |
| `tui.select.pageUp` / `pageDown` | `PgUp` / `PgDn` | Selection page |
| `tui.select.confirm` | `Enter` | Confirm |
| `tui.select.cancel` | `Escape`, `Ctrl+C` | Cancel |

### 3.3 App actions — 47 **[source-verified `TUI/app-keybindings.ts:95-296`]**

| Action | Default keys | Meaning |
|---|---|---|
| `app.interrupt` | `Esc` | Interrupt current operation |
| `app.clear` | `Ctrl+C` | Clear screen / cancel (first press clears draft, second exits) |
| `app.exit` | `Ctrl+D` | Exit (with a draft, `Ctrl+D` forward-deletes instead — see 3.4) |
| `app.suspend` | `Ctrl+Z` | Suspend application |
| `app.display.reset` | `Alt+L` | Reset terminal display |
| `app.thinking.cycle` | `Shift+Tab` | Cycle thinking level |
| `app.thinking.toggle` | `Ctrl+T` | Toggle thinking-block visibility |
| `app.model.cycleForward` | `Ctrl+P` | Cycle role models forward |
| `app.model.cycleBackward` | `Shift+Ctrl+P` | Cycle role models backward |
| `app.model.select` | `Alt+M` | Model selector / set roles |
| `app.model.selectTemporary` | `Alt+P` | Temporary model for this session |
| `app.tools.expand` | `Ctrl+O` | Expand tool output |
| `app.tools.toggleVisibility` | `Ctrl+Shift+O` | Show/hide tool activity |
| `app.ask.hide` | `Alt+H` | Hide/show pending `ask` dialog |
| `app.ask.note` | `n`, `Alt+N` | Note on highlighted ask option |
| `app.approval.approve` | `Ctrl+Enter`, `Ctrl+Q` | Approve a pending tool call |
| `app.editor.external` | `Ctrl+G` | Open `$VISUAL`/`$EDITOR` |
| `app.local.open` | `Alt+G` | Open `local://` file in external editor |
| `app.message.followUp` | `Ctrl+Q`, `Ctrl+Enter` | Send follow-up message |
| `app.retry` | `F5`, `Alt+R` | Retry last failed assistant turn |
| `app.message.dequeue` | `Alt+Up`, `Shift+Up` | Dequeue a queued message |
| `app.btw.inject` | `Alt+I` | Send a `/btw` answer's steering prompt |
| `app.clipboard.pasteImage` | win: `Ctrl+V`,`Alt+V`; mac: `Ctrl+V`,`Super+V`; else `Ctrl+V` | Paste image/text |
| `app.clipboard.pasteTextRaw` | `Ctrl+Shift+V`, `Alt+Shift+V` | Paste raw text |
| `app.clipboard.copyLine` | `Alt+Shift+L` | Copy current line |
| `app.clipboard.copyPrompt` | `Alt+Shift+C` | Copy prompt |
| `app.session.new` | *(unbound)* | Create new session |
| `app.session.tree` | *(unbound)* | Show session tree |
| `app.session.fork` | *(unbound)* | Fork session |
| `app.session.resume` | *(unbound)* | Resume session |
| `app.agents.hub` | `Alt+A` | Open Agent Hub |
| `app.session.observe` | `Ctrl+S` | Open Agent Hub (observe) |
| `app.session.togglePath` | `Ctrl+P` | Toggle session path display *(resume picker)* |
| `app.session.toggleSort` | `Ctrl+S` | Toggle session sort order *(picker)* |
| `app.session.rename` | `Ctrl+R` | Rename session *(picker)* |
| `app.session.delete` | `Backspace`, `Delete` | Delete selected session *(picker)* |
| `app.session.deleteNoninvasive` | `Ctrl+Backspace` | Delete session (non-invasive) |
| `app.session.pin` | `Alt+K` | Pin selected session |
| `app.session.unpin` | `Alt+Shift+K` | Unpin selected session |
| `app.tree.previousBranch` | *(unbound)* | Previous branch in `/tree` |
| `app.tree.nextBranch` | *(unbound)* | Next branch in `/tree` |
| `app.plan.toggle` | `Alt+Shift+P` | Toggle plan mode |
| `app.history.search` | `Ctrl+R` | Search prompt history |
| `app.links.hint` | `Alt+O` | Pick an extension link by hint tag |
| `app.stt.pushToTalk` | `Space` (hold) | Hold-to-record speech-to-text |
| `app.stt.toggle` | *(unbound)* | Toggle STT recording |
| `app.live.toggle` | `Ctrl+L` | Start/stop live voice mode |

**7** actions are unbound by default (`defaultKeys: []`): `app.session.new`, `app.session.tree`, `app.session.fork`, `app.session.resume`, `app.tree.previousBranch`, `app.tree.nextBranch`, `app.stt.toggle` (`app-keybindings.ts:212-227, 264-271, 288-291`).

### 3.4 Context-specific behavior and overlap resolution

- **Overlapping defaults are contextual**, not conflicts: `Ctrl+P` = model cycle in chat, session path toggle in the resume picker; `Ctrl+S` = observe hub in chat, sort toggle in the picker; `Ctrl+R` = history search in chat, rename in the picker. The picker-specific actions are only consulted by the session selector.
- **`Ctrl+D` is dual-role**: it is both `app.exit` and `tui.editor.deleteCharForward`. `/hotkeys` splits exit keys into "delete forward (with draft) / exit (empty prompt)" and "Exit" rows (`TUI/hotkeys-markdown.ts:66-91`).
- **Fallback de-escalation**: `app.message.followUp` drops its `Ctrl+Q` alternative when any *other* user binding claims `Ctrl+Q`; `app.message.dequeue` drops `Shift+Up` likewise (`app-keybindings.ts:584-609, 675-681`). Unbound follow-up/approve fall back to `Ctrl+Enter`,`Ctrl+Q` (`keybinding-matchers.ts:105-123`).
- **Platform defaults**: image paste is computed per platform (`app-keybindings.ts:86-90`); `Ctrl+Enter` is recognized from modifier-tagged LF for terminals that cannot deliver it (`keybinding-matchers.ts:75-80`).
- **Agent Hub** (`TUI/overlays/agent-hub.ts`): `Esc`/`Enter` close or drill in (`:2409, 2473, 2513`), `←`/`→` switch rack panes (`:2434, 2565`), `j`/`k` or `tui.select.down/up` move (`:2457-2465, 2581-2588`), `Tab` toggles split view (`:2546`), `PgUp`/`PgDn` page (`:2552-2556`), plus a typed filter (printable keys). Esc inside a filter clears the filter first (`:2515`).
- **Model browser / pickers** (`TUI/overlays/model-browser.ts:1499-1527`): `tui.select.*` only (`Up/Down/PgUp/PgDn/Enter/Esc`) plus `Home`/`End`. Other selectors follow the same `tui.select.*` contract via `keybinding-matchers.ts`.
- **Tray**: not a terminal keybinding surface — it is a native (Tern/TSP) component; selection, order, paging and motion are owned by the native host (`CA/modes/tray.ts:1-12`). No `app.*` actions are registered for it.
- **Vim mode** is **not** in the rebindable registry: keys are hardcoded in `TUI/vim.ts`. Enable with `tui.vimMode: true` (`docs/keybindings.md:99-107`). Modes: Insert → `Escape`; Normal; Visual (`v`); Visual-line (`V`). Normal-mode command letters are dispatched by a `switch` on the key (`vim.ts:877-1250`): `h j k l 0 ^ $ w b e f F t T ; , gg G % { } i a I A o O v V x D C d y c p P u . J > <`, counts `1-9`/`0` (`:581`), `r`/`R` replace, `Escape` cancel (`:615`). Text objects (`i`/`a` + `w W " ' \` ( ) b [ ] { } B < > p t s`) at `vim.ts:1065-1100`. `Ctrl`-chords, `Enter` and `Tab` never enter Normal-mode handling, so they keep app behavior in every mode (`docs/keybindings.md:172`).
- **`/hotkeys`** renders from the *effective* bindings (user remaps included), not the defaults (`TUI/hotkeys-markdown.ts:52-64, 189-200`); unbound actions print `Disabled`.

### 3.5 Key-hint rendering **[source-verified `TUI/key-hint-format.ts`]**

- Everything the user sees goes through `formatKeyHint` (`key-hint-format.ts:90-92`), which resolves symbols from the active theme's `key.*` symbol set (ascii words / unicode keycaps / nerd icons) and falls back to ASCII before a theme loads (`:12-21`).
- Modifiers are platform-aware: macOS `⌃ ⌥ ⇧ ⌘` (`key.ctrlMac`, `key.altMac`, `key.superMac`), other platforms `Ctrl`/`Alt`/`Super` (`:42-47`).
- Modifiers always print in canonical order regardless of the binding's order (`:80-89`); glyph modifiers join with `key.joiner`, word modifiers with `+` (`:112-116`).
- Alternatives join with `/`: `formatKeyHints(["f5","alt+r"]) → "F5/⌥R"` (`:128-131`). Double-tap gestures render as `←←` (`formatDoubleTap`, `:133-137`). Native tooltips always use unicode keycaps (`formatTooltipKey`, `:101-103`).

---

---

## Q4 — Magic keywords

Source of truth: `CA/modes/magic-keywords.ts:91-142` (table) + `TUI/prompt/magic-keywords.ts` (matcher) + `TUI/prompt/markdown-prose.ts` (masking). **[source-verified]**

### 4.1 The five keywords

| id | Word | requires (tools) | Settings key | Harness effect (one line) |
|---|---|---|---|---|
| `ultrathink` | `ultrathink` | — | `magicKeywords.ultrathink` | injects `ultrathink-notice`; when auto-thinking is active it also forces the **highest** supported effort for the turn, bypassing `providers.autoThinkingMaxEffort` (`CA/session/model-controls.ts:667-671`). |
| `chillax` | `chillax` | — | `magicKeywords.chillax` | injects `chillax-notice`; drops thinking to the lowest supported effort on models with per-message effort (steer relaxes the running turn; never raises, never enables) (`model-controls.ts:726-741`). |
| `orchestrate` | `orchestrate` | `task` | `magicKeywords.orchestrate` | injects the multi-agent orchestration contract, rendered with only the session's live tool names (`magic-keywords.ts:69-71`). |
| `workflow` | `workflowz` | `task` + `eval` | `magicKeywords.workflow` | injects the eval-kernel workflow contract; template is shaped by `taskBatch`, `scoutAvailable`, `evalTools`, `py`, `js`, `freshAgents`, `perCallModel` (`magic-keywords.ts:74-89`). |
| `jevify` | `jevify` | `eval` | `magicKeywords.jevify` | injects the bulk `judge()` classification contract (`magic-keywords.ts:65-66, 132-141`). |

Note the id≠word mismatch only for `workflow`: the settings key is `magicKeywords.workflow`, the trigger is `workflowz` (docs confirm, `docs/magic-keywords.md:55`).

Injection point: `CA/session/agent-session.ts:9037-9069`. Each matched keyword becomes a `role:"custom"`, `customType:"<id>-notice"`, `display:false`, `attribution:"user"` message queued ahead of the user message. A notice is **skipped** when any `requires` tool is not enabled (`agent-session.ts:9055-9058`). Enabled-ness = `magicKeywords.enabled` AND `magicKeywords.<id>` (`agent-session.ts:9033-9035`; settings rows at `CA/modes/settings.ts:1392-1410`).

### 4.2 Exact matching rules **[source-verified]**

- Case-sensitive, lowercase only: `magicKeywordRegex` has no `i` flag (`TUI/prompt/magic-keywords.ts:36-39`).
- Boundaries (`magic-keywords.ts:22-26`):
  - left: `(?<![\p{L}\p{N}_./\\-])(?<!::)` — not after a letter/digit/underscore/dot/slash/backslash/hyphen, nor after `::`.
  - right: `(?![\p{L}\p{N}_/\\-])(?!\.[\p{L}\p{N}_-])(?!\()` — not before a word char/slash/backslash/hyphen, not before a file-extension dot, not before `(`.
  - ⇒ `orchestrate,` matches; `orchestrated`, `orchestrate.ts`, `foo::orchestrate`, `orchestrate()` do not (docs example, `docs/magic-keywords.md:36`).
- Prose-only: matching runs against `maskNonProse(text)` (`markdown-prose.ts:160-234`), a **length-preserving** mask that blanks:
  1. fenced code blocks (backtick or tilde, per line, `markdown-prose.ts:167-200`);
  2. inline code spans (exact-length backtick runs, `:210-219`);
  3. HTML/XML comments, tags, and the **content between matching open/close tags** (`:125-151`, nesting-aware).
  Indices still address the original text, so painting works.
- `containsMagicKeyword(text, word)` = regex hits raw text **and** hits masked text (`markdown-prose.ts:242-245`).
- Matching uses the **expanded** prompt (after slash-command/prompt-template expansion); synthetic/agent-initiated prompts do not trigger notices (`docs/magic-keywords.md:39`, [docs-only] for the synthetic-prompt half).
- All matched keywords in one prompt each add their own notice.
- Also in this module but *not* keywords: `parseTurnBudget(text)` is evaluated on the same path (`agent-session.ts:9039-9040`) — turn-budget syntax, not a keyword.

### 4.3 Surfaces derived from the table

`MAGIC_KEYWORDS` drives (a) `magicKeywords.*` settings (`CA/modes/settings.ts:1392-1410`), (b) notice message types (`CA/session/queued-messages.ts:75`), (c) editor/sent-bubble gradients via `setMagicKeywords` (`CA/modes/interactive-mode.ts:2283`, `modes/startup-composer.ts:74`), (d) spelling-autocorrect exemption (`isMagicKeyword`, `TUI/prompt/magic-keywords.ts:81-84`). Gradient spec: `id → hue sweep [from,to]`, optional lightness sweep; `chillax` is the only one with `lightness: [62,45]` (`magic-keywords.ts:101-112`).

Global switch `magicKeywords.enabled` gates notices + editor animation but **not** static highlighting/spelling exemption (`docs/magic-keywords.md:3`, [docs-only] for that nuance; the source gate is `cfgMagicKeywordsEnabled` at `agent-session.ts:9034` and `editor.magicKeywordsEnabled` at `interactive-mode.ts:2293`).

---

---

## Q5 — Session-control commands (surface-level state effects)

All rows [source-verified] unless marked.

| Command | State it changes | Where |
|---|---|---|
| `/new` | Starts a fresh session (new session file, empty transcript); prior session stays on disk. | `builtin-lifecycle.ts:252-259` → `ctx.handleNewCommand()` |
| `/fresh` | **Only** resets provider stream state (response id / cache chain). Local transcript untouched; refused while streaming. | `:260-281` (`session.freshSession()`) |
| `/clear` | Drops conversation context **in place**, keeps the same session id/file. Refused while streaming. | `:282-291` |
| `/delete` | Deletes the current session file and starts a new session; `drop:true` keeps the successor lazy. | `:293-300`; `/session delete` path at `builtin-session.ts:274-301` |
| `/compact` | Rewrites context into a compaction summary (modes from `COMPACT_MODES`, optional focus). | `:302-320` (`handleCompact`) |
| `/shake` | Drops heavy content (`elide` tool results, `images`, `thinking`) without a summary. | `:321-349` |
| `/handoff` | Background compaction with a handoff summary (alias of `/compact handoff`). | `:350-357` |
| `/resume [id\|@claude\|@codex]` | Switches the live session to another transcript (or opens the picker / foreign-import picker). | `:359-394` |
| `/pin` / `/unpin` | Persists a pin flag on a session so it sorts to the top of the resume list. | `:395-424` |
| `/rename [title]` | Sets the session title (generates one when omitted). | `:614-692` |
| `/move <dir>` | Changes the session's working directory and rewrites its storage placement. | `:692-722` |
| `/wt` | Moves the session into a new git worktree (changes carried over). | `:722-757` |
| `/add-dir` / `/remove-dir` / `/dirs` | Mutates the session's multi-root workspace directory list. | `:757-827` |
| `/restart` | Re-execs omp with the same launch flags (one-shot session selectors dropped) plus `--restore`, resuming this session and running subagents. | `:832-846`; argv rewrite `CA/cli/flag-tables.ts:466-487` |
| `/exit`, `/quit` | Terminates the process. | `:827-831`, `builtin-control.ts:131-137` |
| `/model` | Opens the model selector / sets the session model (persisted as the session's model). | `builtin-modes.ts:412-455` |
| `/switch [model]` | Session-**only** (temporary) model change; does not rewrite role config. | `builtin-modes.ts:456-506` (`setModelTemporary`) |
| `/effort [level]` | Sets the session thinking level. | `builtin-modes.ts:850-884` |
| `/fast`, `/slow` | Toggle service tiers (OpenAI priority/ultraflex, Anthropic fast/low-priority, Google flex). | `builtin-modes.ts:507-562` |
| `/plan` | Toggles plan mode (read-only planning before execution). | `builtin-modes.ts:284-305` |
| `/vibe` | Enters/exits director mode: installs `vibe_*` tools, **reduces active tools to `read` + optional `todo` + 5 worker-control tools**, injects director instructions, persists worker lifecycle. Inline arg becomes the first directive; exit aborts the director turn, cancels workers, restores the toolset. | `builtin-modes.ts:316-334`; semantics `docs/vibe-mode.md:7-24` |
| `/goal` (+ set/show/pause/resume/drop/budget) | Toggles a persistent autonomous objective for the session, with budget accounting. | `builtin-modes.ts:334-360` |
| `/loop` | Arms loop mode so the next prompt re-submits after each yield, bounded by count/duration or `--while/--until`. | `builtin-modes.ts:372-398` |
| `/login` / `/logout` | OAuth credential add/remove for a provider (stored credentials, not session transcript). | `builtin-session.ts:690-766` |
| `/mcp …` | Adds/removes/tests/reauthorizes MCP servers; `reload`/`reconnect` change the live tool registry. | `builtin-session.ts:766-799` |
| `/export [--themes] [path]` | Writes the session as an HTML file; **no session state change**. `/dump` is the clipboard path. | `builtin-collaboration.ts:179-202` |
| `/dump [all\|anon]` | Clipboard + temp JSON sidecar; `all`/`anon` write zip archives to tmp. | `builtin-collaboration.ts:230-291` |
| `/share` | Uploads the transcript to the share server / secret gist and prints a link. | `builtin-collaboration.ts:292-318` |
| `/collab` | Starts/stops a live relay share of this session (view/list/status/stop). | `builtin-collaboration.ts:318-447` |
| `/join` / `/leave` | Attaches to / detaches from a remote collab session (guest replica; host-only builtins gated). | `builtin-collaboration.ts:447-504` |
| `/fork [pane\|window\|tab]` | Forks the session (new branch of the transcript), or relaunches it in a multiplexer pane/window/tab. | `builtin-session.ts:641-681` |
| `/branch` (`rewind`) | Rewinds to an earlier message, keeping the old path as a branch. | `builtin-session.ts:631-641` |
| `/tree` | Switches branches inside the session tree. | `builtin-session.ts:681-690` |
| `/commit` | Generates a commit message and commits only the agent's own changes (surface `CA/commit/`). | `builtin-commit.ts:15-42` |
| `/security` | Plans/runs/imports security scans (writes scan artifacts). | `builtin-modes.ts:236-256` |
| `/marketplace`, `/plugins`, `/skills`, `/reload-plugins` | Plugin/skill install state; `reload-plugins` rebuilds the live command/tool/skill registry. | `builtin-marketplace.ts:46-65, 425-504`, `builtin-skills.ts:65-75` |

---

---

## Q6 — Approval / permission modes and command-gating settings

### 6.1 Approval modes **[source-verified]**

`APPROVAL_MODES = ["always-ask","always-ask-cached","write","yolo"]` is the single list the type and validator read (`CA/tools/approval.ts:42-44`). Config key `tools.approvalMode`; schema default `yolo`, but a context with **no settings and no `--auto-approve` fails closed to `always-ask`** with no grants (`approval.ts:85-112`; `docs/approval-mode.md:18-25`).

| Mode | Auto-approves | Prompts for |
|---|---|---|
| `always-ask` | `read` | `write`, `exec` |
| `always-ask-cached` | `read` without file targets, granted files/folders | file reads, `write`, `exec` |
| `write` | `read`, `write` | `exec` |
| `yolo` (default) | `read`, `write`, `exec` | none |

- Per-call tier comes from the tool's `approval` declaration; **missing declaration ⇒ `exec`** (safe default); MCP tools always declare `write` (`docs/approval-mode.md:5-12`).
- User policy `tools.approval.<tool>: allow|deny|prompt` overrides the mode but cannot bypass a tool's own deny/prompt or a non-yolo safety override (`docs/approval-mode.md:10, 66-88`).
- `tools.restrictedFolders` forces approval regardless of mode, with no "always" grant (`docs/approval-mode.md:48-64`).
- CLI: `--approval-mode <mode>` overrides for the session; `--auto-approve`/`--yolo` force yolo and win over `--approval-mode` (`flag-tables.ts:283-293`; `docs/approval-mode.md:27`). Flag values are validated against the same four names.
- Grants live in memory (session) or `approval-cache.db` when "Remember across sessions" is on; `/forget-approvals` clears both (`builtin-control.ts:121-130`, `docs/approval-mode.md:37-46`).
- ACP sessions: approval prompts are surfaced to the client; `docs/approval-mode.md` §ACP sessions [docs-only].

### 6.2 Settings that gate the command surface **[source-verified id strings]**

| Setting | Effect on the command surface | Source |
|---|---|---|
| `magicKeywords.enabled` + `magicKeywords.<id>` (5) | Gate notice injection and the ultrathink/chillax effort changes; static highlighting survives | `CA/modes/settings.ts:1392-1410` |
| `goal.enabled` | `/goal`, `/guided-goal`, `--goal` refuse without it | `CA/goals/settings.ts:4`; `args.ts:204-206` |
| `prewalk.enabled` / `prewalk.auto` | `/prewalk`, `--prewalk`, `--prewalk-into`; `--no-prewalk` overrides | `CA/session/settings.ts:97`; `launch-help.ts:28-35` |
| `skills.enableSkillCommands` | Enables `/skill:<name>` commands and advertises them to ACP | `CA/extensibility/settings.ts:32`; `available-commands.ts:64-73` |
| `memory.backend` = `off\|local\|hindsight\|mnemopi` | Selects which `/memory` sub-actions exist (the `mm *` verbs are the mnemopi backend) | `CA/memory-backend/settings.ts:13`; `builtin-lifecycle.ts:525-550`; `docs/settings.md:872` |
| `browser.enabled` / `computer.enabled` | `/browser`, `/computer`, and the corresponding eval preludes | `CA/tools/browser/settings.ts:8`, `CA/tools/settings.ts:693` |
| `btw.inject` | Enables `app.btw.inject` (Alt+I) for sending a `/btw` answer into the main session | `CA/modes/settings.ts:960` |
| `stt.enabled` | Push-to-talk / `app.stt.*` | `CA/stt/settings.ts:10` |
| `tui.vimMode` | Vim editing mode (hardcoded keys) | `CA/modes/settings.ts:973` |
| `tools.xdev` (default `true`) | Mounts discoverable tools under `xd://` device URLs instead of flattening every schema — changes tool names the model sees, not the CLI/slash surface | `CA/tools/settings.ts:1095`; `docs/settings.md:691` |
| `plan.defaultOnStartup` | Starts in plan mode; **ignored in print mode**; conflicts with `--goal` | `CA/plan-mode/settings.ts:25`; `args.ts:201-203` |
| `async.printWake` | Print mode waits for background jobs (`--wait-async` is the flag form) | `CA/tools/settings.ts:1069` |
| `session.warnCost` / `session.maxCost` | `--warn-cost` / `--max-cost` defaults; max-cost exits print mode with status 3 | `launch-help.ts:142-148`; `docs/cli-reference.md:242-243` |

---

---

## Q7 — Environment variables affecting CLI/TUI behavior

Full catalogue: `docs/environment-variables.md` (775 lines, 11 sections, ~300 vars — most are provider credentials). Below is the subset that changes **CLI/TUI/command behavior**; "verified" marks vars I also confirmed in source.

### 7.1 Profiles, paths, config

| Variable | Effect | Status |
|---|---|---|
| `OMP_PROFILE` | Named profile selector; wins over `PI_PROFILE`; equivalent to `--profile` | [source-verified] `cli.ts:468` |
| `PI_PROFILE` | Legacy profile selector (only when `OMP_PROFILE` undefined) | [source-verified] `cli.ts:468` |
| `PI_CONFIG_DIR` | Config root dirname under home (default `.omp`) | [docs-only] `docs/environment-variables.md:612` |
| `PI_CODING_AGENT_DIR` | Full agent-directory override (default profile only) | [docs-only] :613; referenced in `help-extra.ts:87` |
| `PI_CODING_AGENT_SESSION_DIR` | Initial session dir override read by launch arg parsing | [source-verified] `args.ts:263` |
| `PI_CONFIG_FILES` | Path-list of settings overlays loaded before `--config` | [docs-only] :616 |
| `PI_PACKAGE_DIR` | Package asset base dir | [source-verified] `config.ts:63-69` |
| `OMP_WORKTREE_DIR` | Managed worktree dir (default `~/.omp/wt`) | [docs-only] :621 |

### 7.2 Model roles, titles, TUI overrides

| Variable | Effect | Status |
|---|---|---|
| `PI_SMOL_MODEL` / `PI_SLOW_MODEL` / `PI_PLAN_MODEL` | Ephemeral role-model overrides; the CLI flag wins | [source-verified] `main.ts:2047-2049`, `help-extra.ts:89-91` |
| `PI_NO_TITLE` | Disables auto session-title generation (≡ `--no-title`) | [source-verified] `main.ts:2040`, `interactive-mode.ts:2997` |
| `PI_MOTION` | `tui.motion` preset; `--motion` wins | [docs-only] :507 (flag help cites it, `launch-help.ts:133-136`) |
| `PI_SPINNER_INTERVAL` | Spinner frame interval; `--spinner-interval` wins | [docs-only] :508 |
| `PI_MAX_FPS` | Repaint ceiling; `--max-fps` wins | [docs-only] :509 |
| `PI_NO_PTY` | Disables the interactive PTY path for bash | [docs-only] :545 (flag help `launch-help.ts:83`) |

### 7.3 Runtime / diagnostics / storage

| Variable | Effect | Status |
|---|---|---|
| `PI_TIMING` | Prints a hierarchical timing-span tree to stderr; also disables the speculative startup composer | [source-verified] `cli.ts:549` |
| `PI_DEBUG_STARTUP` | One `[startup] <phase>:start/done` line per phase to stderr | [docs-only] :521 |
| `PI_COMPILED` | Marks a compiled binary as the process entry | [source-verified] `cli.ts:74` |
| `OMP_LOG_LEVEL` | Verbosity of the log file / service stdout | [docs-only] :522 |
| `OMP_SKIP_SETUP` | Skips automatic interactive setup scenes | [docs-only] :524 |
| `PI_TERMINAL_OWNER_PID` | Set by the interactive TUI; a differently-pid'd omp skips auto-start | [docs-only] :550 |
| `OMP_MCP_TIMEOUT_MS`, `OMP_MCP_STARTUP_TIMEOUT_MS`, `OMP_MCP_REQUIRE_READY` | MCP request/startup timeouts; `REQUIRE_READY=1` makes print mode exit 1 when a server is pending | [docs-only] :546-548 |
| `PI_SESSION_ID`, `PI_SESSION_FILE`, `PI_TOOL_CALL_ID` | **Set by** the bash tool for every command | [docs-only] :649-655 |
| `PI_BASH_NO_CI`, `PI_BASH_NO_LOGIN`, `PI_SHELL_PREFIX` (+ `CLAUDE_*` aliases) | Shell spawn behavior | [docs-only] :634-643 |

### 7.4 TUI runtime flags (shared `pi-tui`)

`PI_NOTIFICATIONS` (off), `PI_TUI_WRITE_LOG`, `PI_TUI_RAW_BACKSPACE_IS_CTRL`, `PI_HARDWARE_CURSOR`, `PI_NO_SYNC_OUTPUT`, `PI_NO_DECCARA`, `PI_NO_GLYPH_PROTOCOL`, `PI_TUI_NATIVE` (Tern probe: `0` never, `1` always), `PI_TUI_TSP_RECORD`, `PI_TUI_NATIVE_STATS`, `PI_DEBUG_REDRAW`, `PI_FORCE_IMAGE_PROTOCOL`, `PI_KITTY_PLACEHOLDERS`, `PI_NO_KITTY_PLACEHOLDERS`, `PI_TUI_RESIZE_IN_PLACE`, `PI_LOOP_SAMPLER` — all [docs-only], table at `docs/environment-variables.md:683-703`.

UI/session auto-detection (`COLORTERM`, `TERM`, `WT_SESSION`, `COLORFGBG`, `TERM_PROGRAM`, `TMUX_PANE`, `KITTY_WINDOW_ID`, `TERM_SESSION_ID`, `SHELL`, `ComSpec`) at `docs/environment-variables.md:665-679` [docs-only].

Other command-surface env vars: `PI_LINK_SCHEME` / `PI_LINK_HOST` (remote file links), `PUPPETEER_PROXY*` (browser launch), `PI_AUTO_QA` / `PI_AUTO_QA_PUSH` (tool-issue recording), `PI_BLOCKED_AGENT` (blocks a subagent type), `PI_TASK_MAX_OUTPUT_BYTES` / `_LINES` — `docs/environment-variables.md:704-729, 516-554` [docs-only].

**Doc drift check**: `CA/cli/help-extra.ts:19-94` advertises a *subset* of these (core provider keys + `OMP_PROFILE`, `PI_CODING_AGENT_DIR`, `PI_PACKAGE_DIR`, `PI_SMOL_MODEL`, `PI_SLOW_MODEL`, `PI_PLAN_MODEL`, `PI_NO_PTY`) and explicitly points at `docs/environment-variables.md` for the rest — consistent with the doc, not exhaustive.

---

---

## Notable findings & drift register

1. **The CLI is not `argv[0]`-strict**: `resolveCliArgv` hoists a subcommand past leading global flags (`omp --approval-mode=yolo acp` works) and *strips* launch-only flags for non-launch subcommands (`cli-commands.ts:459-483`) — a fix for #8891.
2. **Bare plugin words are hard errors, not prompts** (`marketplace`, `list`, `upgrade`, …) to prevent silent "argv leaks to the LLM" (`cli-commands.ts:338-397`, #4845/#2936/#1496).
3. **`--help` metadata drifts from the parser**: `launch-help.ts` omits `--fork`, `--session`, `--provider-session-id`, `--prompt-cache-key`, `--trusted-extension`, `--plugin-dir`, all of which `args.ts`/`flag-tables.ts` accept. Completions, by contrast, are generated from live metadata and cannot drift (`completion-gen.ts:1-16`).
4. **Print mode dispatches only 4 slash commands** (`/compact`, `/handoff`, `/retry`, `/mcp reauth`); everything else is sent to the model verbatim (`print-mode.ts:327-344`). ACP gets 48 of 89 builtins (those with `handle`).
5. **41 of 89 builtin slash commands are TUI-only** (`handleTui` with no text-mode `handle`), so they are invisible to ACP/print clients — including `/login`, `/new`, `/resume`, `/skills`.
6. **Keybindings are two merged tables totalling 79 actions**; 7 are unbound by default. Defaults intentionally collide across contexts (`Ctrl+P`, `Ctrl+R`, `Ctrl+S`), and one chord (`Ctrl+D`) is dual-role. Vim mode is *outside* the registry — hardcoded in `vim.ts`, so it cannot be remapped.
7. **Magic keywords are 5, not 4**, and the fifth (`jevify`) is easy to miss; `workflowz`'s settings key is `magicKeywords.workflow` (id ≠ word). Matching is prose-only via a length-preserving mask of fences/inline code/HTML *sections*.
8. **`--plan` is the only extension-shadowable flag**: a bundled boolean extension can take it over, changing whether the next token is consumed (`flag-tables.ts:324`, `args.ts:294-312`).
9. **Docs omission**: `docs/cli-reference.md` documents 49 of 56 subcommands (missing `anonymize`, `auth`, `session`, `__complete`).

---

## Checklist status

| Q | Status | Notes |
|---|---|---|
| Q1 CLI surface | **answered** | 56 subcommands (55 public + `__complete`), 6 aliases, 16 hidden worker selectors, full flag classification, completion generation. Drift noted: `--help` metadata omits 6 parseable flags; doc table omits `anonymize`/`auth`. |
| Q2 Slash commands | **answered** | 89 builtins + 3 bundled TS commands + 1 embedded markdown (`/init`) + dynamic sources; dispatch/parse/allowArgs/ACP/print-mode differences documented. |
| Q3 Keybindings | **answered** | 79 registry actions (32 TUI + 47 app) with defaults; vim keys, hub/picker contexts, tray = native; rebinding format + migration + hint rendering. |
| Q4 Magic keywords | **answered** | 5 keywords, exact regex boundaries, prose masking (fences/inline code/HTML), per-keyword tool requirements and effort effects. |
| Q5 Session-control commands | **answered** (surface level) | 35 commands with state effects; harness depth deliberately left to the harness worker. |
| Q6 Approval/gating | **answered** | 4 modes + policy layers + 12 gating settings. |
| Q7 Environment variables | **partial** | CLI/TUI-relevant vars enumerated and 9 spot-verified in source; the provider-credential half (~250 vars) is catalogued by doc only — out of scope for the command surface. |
| Unresolved | — | (a) Whether ACP clients can drive `/login` (spec is TUI-only; docs imply a host-issued login UI). (b) Exact per-overlay key maps beyond hub/pickers were not exhaustively enumerated — overlays share `tui.select.*` plus ad-hoc keys. |
