# oh-my-pi (omp) TUI — source-level reverse-engineering report

**Target:** `oh-my-pi` @ `840cc4b6bc94581509215d8db30fb04ad0ab7fea` (shallow clone, ~v18.22.2)
**Method:** READ-ONLY static analysis of the TypeScript sources. No code was executed, no tests run, no terminal observed. Every mechanism below is read off the source; nothing here is a runtime observation.
**Legend:** `[src]` = verified against source with file:line. `[doc]` = from `docs/*.md` only (not re-verified in code). `[inf]` = my inference from the code shape, flagged as such.

Path abbreviations used throughout:

| Abbrev | Real path |
|---|---|
| `tui/` | `packages/tui/src/` |
| `ca/` | `packages/coding-agent/src/` |
| `J:` | `tui/tui.ts` (4 869 lines) |
| `T:` | `tui/terminal.ts` (3 053 lines) |
| `C:` | `tui/prompt/composer.ts` (1 324 lines) |

---

## 0. Architecture overview

Three layers, each owning a different question:

1. **`packages/tui` — the engine.** A component tree (`Component.render(width) → readonly string[]`), a differential row renderer (`J:tui.ts`), terminal I/O + capability probing (`T:terminal.ts`), input decoding (`keys.ts`, `stdin-buffer.ts`), overlays/focus, and a library of widgets. It is product-agnostic: it does not know what a "message" or "tool call" is.
2. **`Composer` (`C:prompt/composer.ts`) — the layout owner.** `Composer implements TerminalFrameProvider` (`C:243`), installs itself with `this.ui.setFrameProvider(this)` (`C:376`), and on every frame decides *what is history* (append-only, already scrolled) vs *what is the mutable viewport* (the live tail: transcript tail, chrome, editor, status line). This is the piece that makes the streaming UI possible without the renderer ever guessing finality.
3. **`packages/coding-agent` — the application.** `ca/modes/interactive-mode.ts` (10 226 lines) wires sessions, controllers (`ca/modes/controllers/*`: streaming reveal, tool-args reveal, input, selectors), the tray (`ca/modes/tray.ts`), the HUD (`ca/modes/progress-hud.ts`), job widgets (`ca/src/job-widgets/`), and mounts transcript blocks into the engine's `TranscriptContainer` (`tui/chrome/transcript-container.ts`).

The critical architectural inversion, stated in `docs/tui-core-renderer.md` §1 and confirmed in code: **finality is an application decision, never inferred by the renderer**. The product hands the engine an ordered `HistoryBatch { id, rows, kind: "append" | "replay" }` plus the complete mutable `viewport` (`J:178-194`). The engine writes each accepted batch exactly once and acks its monotonic id (`J:4079-4085`), so retries and coalesced frames are safe.

### Screen layout (normal buffer)

```
 ┌─────────────────────────────────────────────────────────────┐
 │  native scrollback (terminal-owned, immutable)              │  ← history batches,
 │  …retired transcript blocks, already scrolled…               │    written once, never
 │                                                             │    audited or rewritten
 ├─────────────────────────────────────────────────────────────┤  ← anchor: viewportTop
 │  HEADER (welcome/gradient logo, extras)     [optional]      │  ┐
 │  ─────────────────────────────────────────────────────────  │  │
 │  TranscriptContainer: settled blocks + the live tail        │  │  mutable
 │    • user message, assistant markdown, thinking, tool cards │  │  viewport
 │      (ToolExecutionComponent), todo list, notices…          │  │  (diffed every
 │    • the tail block reflows to the current width            │  │   frame)
 │  ─────────────────────────────────────────────────────────  │  │
 │  chrome below transcript, in render order:                  │  │
 │    queued messages → HUD rows (judge/download progress) →   │  │
 │    working row (spinner · elapsed · shimmering intent · Stop)│  │
 │  ─────────────────────────────────────────────────────────  │  │
 │  editor / composer (box|band|pi|rail|rule|borderless shape) │  │
 │    + autocomplete popup + attachment chips                   │  │
 │  ─────────────────────────────────────────────────────────  │  │
 │  status line (model, context %, git, brand fade, cost …)    │  ┘
 └─────────────────────────────────────────────────────────────┘
```

Overlays (dialogs, pickers, Agent Hub, fireworks) are **screen-coordinate content composited over the viewport** (`J:3363-3395`) and never become history. A fullscreen overlay instead borrows the alternate screen (`J:4115-4199`), leaving all normal-screen accounting untouched.

---

## Q1. Rendering architecture

### 1.1 Component model

`Component` (`J:254-366`) is deliberately small and reference-equality-driven:

```ts
interface Component {
  debugId?: string; debugKind?: string; debugState?(): …; debugChildren?: readonly Component[];
  render(width: number): readonly string[];      // component-owned, immutable to callers
  describe?(cx: DescribeContext): NativeNode | null;   // TSP terminal path
  handleInput?(data: string): void;
  wantsKeyRelease?: boolean;                     // opt in to kitty release events
  capturesInput?(data: string): boolean;         // preflight before TUI-wide listeners
  invalidate?(): void;                           // theme change / repaint from scratch
  releaseRenderCaches?(): void;                  // drop derived rows only
  setIgnoreTight?(ignore: boolean): any;
  dispose?(): void;
}
```

The contract that makes the whole renderer fast: **an unchanged component returns the same array reference; a changed one must return a new array** (`J:240-253`). Container memoization keys on child array identity:

- `Container` (`J:537-667`) keeps `#memoLines`, `#memoChildLines`, `#memoWidth`. Children are *still rendered every frame* — renders carry side effects such as image placement registration — but the concatenated array is only rebuilt when some child returned a different reference at the same width (`J:540-548`).
- A component that mutates a previously returned array in place must implement `RenderStablePrefix` and declare how many leading rows survived (`J:250-252`).

`Focusable` (`J:392-397`) is separate from `Component`: `focused: boolean` plus optional `setUseTerminalCursor()`. Cursor position travels as a zero-width sentinel, **not** as a coordinate call: `CURSOR_MARKER = "\x1b_pi:c\x07"` (`J:423`), an APC sequence terminals ignore. The focused component emits it at the caret; the renderer strips it, records `{row, col}` (`J:4281-4311`), and places the hardware cursor inside the same synchronized-output frame.

### 1.2 Frame planning: history vs viewport

`TerminalFrameProvider` (`J:197-206`):

```ts
renderFrame(viewport: ViewportSize): TerminalFramePlan;
acknowledgeHistory(id: number): void;
renderResizeFrame?(viewport): readonly string[];   // transient resize buffer only
beginHistoryReplay?(): void;                        // re-offer ledger after reset/resize
beginHistoryFlush?(): void;                          // force retire before stop
```

`#renderProviderFrame` (`J:3549-3569`) is the hot path:

1. `imageBudget.beginPass()`, call `provider.renderFrame({columns, rows})`.
2. Enforce the layout contract: more rows than the screen throws in test/dev and is logged + sliced in prod (`J:3559-3564`).
3. Composite visible overlays into the viewport (`J:3565`).
4. Retry the whole pass if the image budget changed anything (`while (this.#imageBudget.endPass())`).
5. `#emitPlanFrame(...)`.

### 1.3 The differential write (`#emitPlanFrame`, `J:3802-4086`)

This is the core. In order:

1. **Batch acceptance.** An offered batch is accepted only if `offered.id > #acceptedHistoryBatchId`; a stale id is acked and dropped (`J:3815-3816`). Monotonic-id handshake.
2. **Replay split.** For `kind === "replay"`: prepend blank rows to make the short viewport explicit, count leading blank rows, then move `min(history.length, leadingBlankRows)` trailing history rows *into* the viewport's leading blanks (`J:3821-3836`). The result is one bottom-first ledger + final viewport, so **no intermediate replay frame is ever observable**.
3. **Preparation.** `#prepareLinesArray` twice — history verbatim (no marker stripping), viewport with marker stripping (`J:3839-3841`). Preparation = strip extension-link markers, classify the row (image marker / OSC 8 / ANSI validity / OSC 66 / non-ASCII / exact ASCII width), fit to width, coalesce adjacent SGR, and record a `PreparedLine` sidecar (`J:4264-4330`, `#classifyLine` at `J:4512`, terminal content at `J:4401`).
   - **Memoization** is by content *and* context: a cached row is reusable only under identical `raw`, `width`, `widthEpoch` (width-config generation), and `imageProtocol` (`J:4315-4330`). A per-frame content→row map (double-buffered, `#preparedLineMemo` / `…Spare`) lets a row that merely *scrolled* reuse its preparation (`J:930-935`, `J:4306-4310`).
4. **Destructive reset** (session replace, `/tree`, explicit clear, rebuild-mode resize): zero the anchor, erase history with `\x1b[H\x1b[2J\x1b[3J` — **ED2 before ED3**, because tmux implements ED2 by scrolling the screen into pane history, so doing ED3 first would let ED2 refill history with a copy of the old screen (`J:3896-3911`). Both erases precede image transmits (kitty/Ghostty treat ED2 as a graphics clear), with explicit kitty delete-all + per-id deletes first (`J:3878-3890`).
5. **Choose a write strategy** — three mutually exclusive paths:
   - **`#movedRowsSequence`** (`J:3701-3795`) — used only when synchronized output is OFF, geometry is stable, there is no replay, no forced repaint, and no live image placements (`J:3923-3933`). It models the physical screen as an array of line-keys, shows each scroll-bound row its *history* line first, scrolls by emitting `\n` on the bottom row (`J:3755-3769`), then applies up to `#MAX_ROW_MOVES = 3` DL/IL row moves (`J:3778-3783`, constant at `J:1027`), then rewrites only rows that still differ, then erases the tail band. Rationale: without DEC 2026 the terminal may present any prefix of the write, so every intermediate screen must be the previous frame *scrolled or shifted* — never a rewritten row beside its stale copy.
   - **`diffable`** (`J:3912-3960`) — the normal path: for each viewport row, `#rowNeedsRewrite` (`J:4604-4620`) compares the previous line string plus the preparation context, and only changed rows are emitted as `CSI {row};1H` + row bytes.
   - **full rewrite** (`J:3961-4007`) — erase live rows that would be pushed into scrollback, then write history rows then viewport rows top-down with `\n` separators, then `CSI …;1H ESC[J` to clear the tail.
6. **Row bytes** (`#lineRewriteSequence`, `J:4627-4676`). Every rewrite ends with `\r` (ConPTY can materialize a pending wrap into an untracked scroll, `J:4634-4638`). Four cases: OSC 66 spacer row (preserve the glyph columns, erase to their right, `J:4646-4647`); image row (`EL` + placement bytes, `J:4648-4651`); compact replay (`blankRow`, skip the erase, `J:4654-4660`); ASCII exact-width row (append `EL` unless the row truly fills the line, `J:4661-4664`); non-ASCII row (leading reset + `EL` + line, because the native measure can over-count combining scripts and leave stale cells, `J:4665-4673`).
7. **Cursor.** If a marker was found: emit `CSI row;col H` plus `CSI ?25h/?25l` and park `#parkedViewportOffset` (`J:4011-4018`). Otherwise hide the cursor and park it on the viewport's **top** row (`J:4019-4025`) — terminals keep the cursor attached to its logical line through rewrap, which is exactly what the post-resize anchor probe needs.
8. **One write.** `paintEndSequence` closes DEC 2026, the whole buffer goes out in a single `terminal.write`, `#notifyFrameWrite`, paint listeners, bookkeeping, then `acknowledgeHistory(id)` *after* the write returns (`J:4026-4085`).

### 1.4 Synchronized output, and what "damage tracking" actually is

There is **no cell-level damage tracking**. Damage is **row-level**, computed two ways:

- **Identity diff** in the normal path: `previousLines[index] !== lines[index]` (`J:4614`). String inequality — cheap because unchanged components return identical strings from memoized arrays.
- **Row-move model** in the unsynchronized path: line-keys + `findRowMove` / `applyRowMove` (`J:3701-3795`, helpers in `tui/render/row-moves.ts`).

DEC 2026 (`?2026`) marks the begin/end of a paint so the terminal presents the frame atomically. The exact wrappers are built once at construction (`J:97-107, 1039-1041`):

```
PAINT_BEGIN      = HIDE_CURSOR + "\x1b[?2026h" + DISABLE_AUTOWRAP
PAINT_END        = ENABLE_AUTOWRAP + "\x1b[?2026l"
PAINT_BEGIN_NO_SYNC = HIDE_CURSOR + DISABLE_AUTOWRAP
```

i.e. autowrap is disabled for the duration of the frame so a row that exactly fills the width cannot trigger a wrap/scroll, and the cursor is hidden so it never flickers across rewritten rows. For long tmux replays the writer **renews** the sync block every `SYNC_OUTPUT_RENEW_BYTES = 16 KiB` because tmux expires synchronized output mid-write (`J:3864-3877`).

### 1.5 Frame scheduling

`requestRender(force?, options?)` (`J:2820-2846`) has two flavours:

- `force` (or a clear-scrollback): set the forced-repaint/clear latches, then `scheduleImmediate` a `#executeRender` on the next `setImmediate` (`J:2822-2834`). `resetDisplay()` and `renderNow()` skip the hop and paint synchronously (`J:2812-2818`, `J:2860-2865`).
- ordinary: coalesce — `#renderRequested` guard, one `setImmediate`, then `#scheduleRender` (`J:2879-2886`).

`#scheduleRender` (`J:2943-2968`) computes the delay as the max of three terms:

| Term | Value | Source |
|---|---|---|
| cadence | `minFrameIntervalMs() = 1000 / tui.maxFps` (default 30 fps; clamped 1–120) | `tui/frame-rate.ts:8,16-18,30-38` |
| input cadence | `#INPUT_FRAME_INTERVAL_MS = 1000/30`, `Math.min(input, ceiling)` when a keystroke is waiting | `J:974-985`, `J:2951-2953` |
| adaptive backpressure | `min(200 ms, 2 × lastFrameCostMs) − elapsed` — targets ≤ ~50 % render duty cycle | `J:2955-2962`, `J:990-991` |
| input grace | `#inputRenderGraceUntilMs − now` (armed for Ctrl+C/Esc so double-press windows can drain) | `J:3094-3099` |

Native (TSP) frames ignore the cadence entirely: `delay = 0`, paced by terminal acks (`J:2964-2966`).

`#executeRender` (`J:2975-2982`) wraps `#doRender()` and records `#lastFrameCostMs` — the wall-clock cost that feeds the adaptive term. Before painting it may **defer** the frame: if `terminal.pendingOutputBytes > STDOUT_BACKLOG_CLEAR_BYTES` (256 KiB, `T:272`), the render parks on `onOutputDrain` and keeps its force/clear intent (`J:2989-3015`). Rationale in comment: composing another frame behind a backlog only queues a stale paint, and past the kernel PTY buffer writes block.

`#doRender` (`J:4089-4210`) dispatches in this order: native live → native render; TSP hello outstanding → return; resize alt/probe/in-place active → defer or paint the transient frame; fullscreen overlay → alt-screen paint; provider present → `#prepareResizeReplay` + `#renderProviderFrame`; else `#renderChildrenFrame` (bottom `height` rows of `this.render(width)`, for tests/simple embeds).

### 1.6 Resize

Resize is the most defensive code in the engine. Pieces:

- **Coalescing.** SIGWINCH stages a `#resizeSettleTimer` for `#RESIZE_VIEWPORT_SETTLE_MS = 120 ms` (`J:1007`, `J:2014`, `J:2139`); burst bookkeeping tracks `#resizeBurst{Grew,Shrank,WidthChanged,Resized,Pull}` (`J:891-907`) so a drag that returns to its starting size is still treated as destructive.
- **Anchor recovery.** Before repainting at the new geometry, the engine asks the terminal where the parked cursor actually is: it parks a distinct column per request (`CHA`) then `CSI 6n`, and attributes replies **by column tag** (`#cprColumnTags`, `J:908-923`, reply handling `J:3028-3063`). Column tagging exists because FIFO counting cannot survive dropped replies and age expiry is unsound. Timeout `#RESIZE_PROBE_TIMEOUT_MS = 200 ms` (`J:1009`).
- **Scrollback policy.** `ResizeScrollbackMode = "append" | "rebuild" | "preserve"` (`J:410`). `#prepareResizeReplay` (`J:3592-3634`) skips height-only resizes for `rebuild` unless the burst shrank or the host owns the grid (ConPTY), because rewrap is always a width change; `append` only replays on a width change; `preserve` never replays. Default is `preserve` for the raw TUI, `rebuild` for the coding agent `[doc]` (`docs/tui-core-renderer.md` §3).
- **In-place resize.** Warp re-reports its size on `?1049h/l`, which turns the alt-borrow resize into a flicker loop; `PI_TUI_RESIZE_IN_PLACE` (or Warp detection outside multiplexers/ConPTY) skips the replay and repaints once the drag settles (`J:126-135`, `J:3598-3604`).
- **Borrowed alt buffer.** Non-in-place resizes temporarily borrow the alternate screen (`#beginResizeAltPaint`, `#renderResizeAltFrame`, `#settleResizeAltPaint`) so the normal buffer's half-reflowed rows are never shown. The `Rebuilding…` notice is only shown when the replay exceeds `#RESIZE_REBUILD_NOTICE_MIN_BYTES = 7 MiB` (`J:1010-1019`, `J:3642-3645`).

### 1.7 Alternate screen

`#renderAltFrame` (`J:4731+`) diffs `#altPreviousLines` row by row exactly like the normal path, but keeps every normal-screen accounting field untouched (`J:1083-1096`). Entry re-pushes the modified-key reporting frame because enhanced keyboard modes can be buffer-local (`J:4128-4155`); exit pops it and fuses the restore into the next destructive repaint when a session replacement finished underneath the overlay (`J:4156-4192`).


---

## Q2. Input pipeline

### 2.1 Raw stdin -> sequences

`ProcessTerminal` (`T:785`) reads stdin and pipes it through `StdinBuffer` (`tui/stdin-buffer.ts`), which is a *streaming escape-sequence splitter*, not a key parser. Its job: turn arbitrary chunk boundaries into complete sequences and never leak probe/report bytes into the editor.

Sequence-resolver constants (`stdin-buffer.ts:30-117`):

| Constant | Value | Purpose |
|---|---|---|
| `timeout` (default) | **75 ms** | wait for an incomplete escape to complete, then flush it raw |
| `partialHoldTimeout` / `PARTIAL_HOLD_MAX_MS` | **150 ms** | extra hold for *unambiguous* partials (SGR mouse head, or any dangling escape while kitty is active) |
| `pasteTimeout` / `PASTE_INACTIVITY_TIMEOUT_MS` | **1000 ms** | bracketed-paste watchdog: assume truncated, deliver what arrived |
| `PASTE_MAX_BYTES` | **64 MiB** | paste byte cap |
| `KITTY_PRINTABLE_DEDUP_WINDOW_MS` | **25 ms** | swallow a buggy double-report (CSI-u event + bare printable) |
| `MAX_CSI_BYTES` | **4096** | bounded CSI scan — a malformed CSI cannot force re-inspection of a growing prefix |
| `MAX_STRING_SEQ_BYTES` | **16 MiB** | OSC/DCS/APC payload cap (clipboard, Sixel, kitty graphics) |
| `STRING_DISCARD_MAX_BYTES` / `_INACTIVITY_MS` | 32 MiB / **1000 ms** | after dropping a torn string, swallow the tail instead of typing base64 into the editor |
| `STRING_ABANDON_MS` / `STRING_DORMANT_MS` | **5000 / 10000 ms** | stalled string head is abandoned, then kept dormant for a late tail |
| `RAW_PASTE_CLASSIFICATION_TIMEOUT_MS` | **10 ms** | coalesce adjacent reads for unbracketed-paste classification |

Sequence families are distinguished by opener (`LEGACY_STRING_REPLY_OPENER`, `stdin-buffer.ts:86-87`): OSC (`ESC ]`), DCS (`ESC P`, incl. DECRQSS/XTGETTCAP/XTVERSION/DA3 forms), APC (`ESC _G` kitty graphics, `ESC _tsp;` Tern Surface Protocol, `ESC _25a1;` Glyph Protocol). Matching *partial* openers means a legacy Alt chord that diverges is recognized the moment it diverges, instead of being held for 75 ms.

Events emitted: `data` and `paste(content, enter?)` (`stdin-buffer.ts:482-490`). A trailing Enter that shared the paste's stdin read rides along, so paste+submit reach the same focused component before a paste-triggered overlay can steal focus (`stdin-buffer.ts:469-480`).

### 2.2 Key parsing

`keys.ts` supports **both** modern and legacy encodings and normalizes to a canonical `KeyId` string:

- **Kitty keyboard protocol**: `CSI codepoint[:shifted[:base]] ; modifiers[:event] [; text] u` — `KITTY_CSI_U_PATTERN` (`keys.ts:302`). Modifier bits: shift 1, alt 2, ctrl 4, super 8; lock mask caps(64)+num(128) stripped (`keys.ts:303-308`). Event types `:2` = repeat, `:3` = release (`KITTY_RELEASE_PATTERN`/`KITTY_REPEAT_PATTERN` at `keys.ts:300-301`; `isKeyRelease` `keys.ts:336`, `isKeyRepeat` `keys.ts:356`). Parsing delegates to a native helper `parseKittySequenceNative` (`keys.ts:371-381`), with a TS-side text decoder that also maps keypad operators and numpad text (`keys.ts:390-453`).
- **xterm `modifyOtherKeys`**: `CSI 27 ; mods ; keycode ~` (`keys.ts:309`, parse `471-504`).
- **Legacy**: plain bytes, `ESC[…` arrows/function keys, SGR mouse; `matchesKey(data, "ctrl+c")` parses then compares canonically (`keys.ts:549-560`).

`extractPrintableText` (`keys.ts:455-461`) is the "does this keystroke produce text?" gate: kitty CSI-u text-producing keys (keypad digits/operators, shifted symbols) decode; otherwise any string without control chars is text. This one function powers draft echo during the startup hold (`J:3171-3174`) and the push-to-talk gesture.

Keybinding matching is **not** chord/sequence based — one keystroke maps to one action:

- `KeybindingsManager` (`keybindings.ts:246-351`) compiles `definitions x user bindings` into `#matchKeysById: Map<action, Set<canonicalKey>>`, with aliasing (`canonicalKeyId` at `keybindings.ts:194-240`: modifier order `ctrl,shift,alt,super`, `esc`->`escape`, `return`->`enter`, implicit `shift+` for uppercase letters and shifted symbols).
- Hot path: parse once, then `matchesCanonical(canonical, action)` is a set lookup per action (`keybindings.ts:305-317`) — the comment says this exists so callers do not re-parse the raw sequence per probe.
- Conflicts are computed at build time and exposed via `getConflicts()` (`keybindings.ts:270-279`).
- `app-keybindings.ts` (715 lines) adds coding-agent actions and loads `keybindings.yml`; `keybinding-matchers.ts` adds *fallbacks* for actions that may be unbound (`app.tools.expand` -> Ctrl+O, `app.editor.external` -> Ctrl+G, `app.message.followUp` -> Ctrl+Q/Ctrl+Enter). Mechanism, not the table, is in scope here.

There is no mode stack in the keybinding engine: modal behaviour lives in the editor (vim) and in overlays, which take focus. Multi-key *gestures* are likewise app-level state machines, not patterns in the matcher: the composer's double-Ctrl+C exit compares `now - #lastInterruptAt < DOUBLE_INTERRUPT_MS` where `DOUBLE_INTERRUPT_MS = 500` (`C:31, 1306-1314`); vim holds pending operator/motion state in `VimState`; push-to-talk holds state in `SpaceHoldGesture` (§2.10).

### 2.3 Dispatch order

`TUI.#handleInput` (`J:3017-3151`) is the single funnel, in this exact order:

1. **TSP events** (`data.startsWith(TSP_PREFIX)`) go to the native backend, never to components (`J:3020-3023`).
2. **CPR replies** (`CSI row;col R`) are stripped while a resize probe is outstanding, attributed by column tag and geometry epoch (`J:3027-3063`).
3. **Arm the input latch**: `#inputRenderPending = true` *before any consumer runs*, so whichever consumer requests the paint is paced at 30 fps instead of the `tui.maxFps` ceiling (`J:3065-3069`).
4. **Startup hold** (TSP `deferInput`): only plain typing/Backspace/release through the originally focused component types live; everything else queues in typing order (`J:3071-3078`).
5. **Stale overlay focus repair**: a focused overlay that became invisible redirects focus to the topmost visible overlay or back to `preFocus` (`J:3083-3093`).
6. **Ctrl+C / Esc** arm `#INPUT_RENDER_GRACE_MS` so app-level double-press windows get one frame to drain queued input (`J:3094-3099`).
7. **`capturesInput` preflight** on the effective target — a component reserves a gesture before TUI-wide listeners and the debug shortcut see it (`J:3101-3112`).
8. **TUI input listeners** in registration order; each may `consume` or rewrite `data` (`J:3114-3129`). Scroll wheel handling and app-level palette hooks live here.
9. **Cell-size response** consumed (`J:3131-3134`), then **Shift+Ctrl+D** debug hook (`J:3136-3140`).
10. **Focused component** `handleInput(data)`, key releases filtered unless `wantsKeyRelease` (`J:3142-3150`).

`#inputTarget()` (`J:3157-3162`) is subtle: an overlay with no `handleInput` leaves input with the focus it displaced, so key-holder overlays (badges, HUDs) do not swallow typing.

### 2.4 Vim mode

`vim.ts` (1 366 lines) is **a pure state machine**, documented as such (`vim.ts:6-12`): it reads a `VimBuffer` snapshot (`{lines, cursorLine, cursorCol}`) and returns `VimCommand` values; the editor keeps undo, kill-ring, placeholder tokens and `onChange`. Motions are therefore unit-testable without a terminal.

- Modes: `insert | normal | visual | visual-line | replace` (`vim.ts:14`).
- Command algebra (`vim.ts:34-46`): `move`, `mode`, `yank`, `delete`, `openLine`, `paste`, `undo`, `replace`, `join`, `indent`, `retype`. `to` is always *exclusive*, so inclusive (`e`) and exclusive (`w`) motions compose identically.
- Motions/objects: grapheme-aware stepping (`vim.ts:61-108`), `w/b/e`, `f/t` with pending key, text objects (`iw/aw`, brackets, quotes), paragraph objects, bracket matching (`vim.ts:171-472`).
- `.` repeat: an insert session is diffed into `VimInsertRecord {deleteBefore, deleteAfter, text}` (`diffInsertSession`, `vim.ts:110-145`) and replayed via `retype`.
- `VimState` (`vim.ts:508`) holds pending operator/motion/count/register state; `visualRange` (`vim.ts:1350`) yields the selection range.

### 2.5 Emacs editing and the kill ring

`kill-ring.ts` (51 lines): ring buffer, `MAX_ENTRIES = 60`, `push(text, {prepend, accumulate})` merges consecutive kills (backward deletions prepend, forward append), `peek()`, `rotate()` for yank-pop cycling. Bound to `tui.editor.yank` (Ctrl+Y) / `yankPop` (Alt+Y) (`keybindings.ts:115-116`).

### 2.6 Bracketed paste

Two implementations, deliberately redundant:

- `StdinBuffer` paste mode: detects `ESC[200~ … ESC[201~` across chunks, with inactivity watchdog + 64 MiB cap (`stdin-buffer.ts:25-27`, `557+`).
- `BracketedPasteHandler` (`bracketed-paste.ts:64-137`) for callers that bypass `StdinBuffer`: same markers, same 64 MiB `DEFAULT_BYTE_LIMIT`, strips nested starts.
- `decodeReencodedPasteControls` (`bracketed-paste.ts:32-39`) repairs tmux `extended-keys` re-encoding of control bytes inside pastes — both formats (`ESC [ 106 ; 5 u` and `ESC [ 27 ; 5 ; 106 ~`) back to the literal byte, so Ctrl+J survives as LF instead of leaking `[106;5u` into the buffer.

### 2.7 Mouse

`mouse.ts` (110 lines) decodes SGR reports `ESC[<b;col;row M|m` into `SgrMouseEvent` (`mouse.ts:9-44`): 0-based `col`/`row`, `release`, `wheel: -1|1|null` (horizontal wheel 66/67 deliberately *not* a direction — that bug made selectors scroll up then back down), `motion`, `leftClick`. `routeSelectListMouse` (`mouse.ts:77-95`) is the shared wheel/hover/click router; `MouseRoutable` (`mouse.ts:104-110`) is the component contract.

Tracking state is three-valued in the engine: `"off" | "inline" | "full"` (`J:122`, `J:1090`). `full` while a fullscreen overlay is up (unless `mouseTracking: false`), `inline` when the product opts in via `tui.mouse` on the normal buffer, else `off` (`J:4118-4127`, applied at `J:4193-4195`).

### 2.8 Autocomplete

`autocomplete.ts` (1 375 lines) defines `AutocompleteProvider.getSuggestions(...)` (`autocomplete.ts:269-302`) and `CombinedAutocompleteProvider` (`autocomplete.ts:658`), which multiplexes slash commands (`autocomplete.ts:89`), `@`-file mentions (`autocomplete.ts:117`), `skill:` namespaces (`autocomplete.ts:509+`), model mentions, prompt actions, emoji, image paths and internal URLs.

Scoring: `subsequenceScore` (`autocomplete.ts:191`), command scoring (`autocomplete.ts:389`), directory cache 100 entries / evict 50 (`autocomplete.ts:641-642`). Async `@` completion is debounced `AT_PARTIAL_DELAY_MS = 150 ms` with `AT_FUZZY_TIMEOUT_MS = 1500 ms` (`autocomplete.ts:15,20`). Rendering lives in `CustomEditor` (`tui/prompt/custom-editor.ts`, 2 139 lines) with `setAutocompleteMaxVisible` (`C:384`).

### 2.9 Windows-specific input

Four mechanisms, all in the engine:

1. **`ENABLE_VIRTUAL_TERMINAL_INPUT`** via `kernel32` FFI so modified keys arrive as VT sequences (`T:1311-1347`, restore at `1349-1359`).
2. **win32-input-mode decoder** (`tui/windows-input-mode.ts`): enables `CSI ?9001h`, then decodes `CSI Vk;Sc;Uc;Kd;Cs;Rc _` records into legacy bytes when expressible, or kitty CSI-u for chords legacy encoding loses (modified Enter/Tab/Escape, Ctrl+Shift+letter). It also reassembles the `Vk=0` byte-per-record form ConPTY hosts emit, which would otherwise turn Alt+Up into `ESC` plus `[1;3A` typed as garbage (`windows-input-mode.ts:1-30`).
3. **AltGr repair** (`tui/windows-altgr.ts`): Windows delivers AltGr as Ctrl+Alt, which would fire shortcuts instead of typing; the module recognizes the composed-character case.
4. **Relay grace constants** (`T:47-68`): `WIN32_RELAYED_ESCAPE_TIMEOUT_MS = 75`, `CSI_INTRODUCER_GRACE_MS = 150`, `INBAND_TERMINATOR_GRACE_MS = 300`, `WIN32_RELAYED_STRING_TIMEOUT_MS = 5000`.

Also engine-level: ConPTY writes are chunked at `MAX_CONPTY_WRITE_CHUNK_BYTES = 16 KiB` (`T:150,175`) because ConPTY has a write-size limit.

### 2.10 Push-to-talk (space hold)

`space-hold.ts` is worth recording as interaction engineering: a *held* key emits OS auto-repeat, so instead of a fixed timer the gesture recognizes a hold by **metronomic inter-key gaps**:

- `SPACE_REPEAT_MAX_GAP_MS = 120` (faster than a deliberate tap), `SPACE_REPEAT_JITTER_MS = 18`, `SPACE_REPEAT_JITTER_RATIO = 0.35`, `SPACE_HOLD_MECHANICAL_RUN = 2` consecutive mechanical deltas to arm, `SPACE_HOLD_RELEASE_MS = 250` idle to end, `SPACE_HOLD_STALL_SLACK_MS = 100` so an event-loop stall is not read as a release (`space-hold.ts:16-35`, classifier `41-45`).
- Text typed during the run is tracked and deleted when the hold is recognized (`space-hold.ts:47-65`), so a false start never leaves stray spaces.

---

## Q3. Screen layout, composition, focus, scrolling

### 3.1 Who owns the layout

`Composer.renderFrame` (`C:437-603`) is the layout algorithm:

```
roots = runtimeMounted ? [...runtimeChildren, statusHost]
                       : [header, bootstrapInputGap, editor, statusHost]
transcriptIndex = roots.findIndex(r => r instanceof TranscriptContainer)
preRoots  = render(roots before transcript)          // header, extras
after     = render(roots after transcript)           // HUD, working row, editor, status line
active    = transcript.renderViewport(width, rows - before - belowFloor, frame)
drop      = max(0, before + active + after - rows)   // clip from the top
mutable   = [...before, ...active, ...after].slice(drop)
[optional slack rows spliced in at the live tail]
return { history, viewport: linkHints.paint(mutable) }
```

Notable decisions, each with the failure it prevents stated in-source:

- **Chrome floor.** `#chromeFloor` is the *minimum* height the below-transcript chrome has taken (`C:480-509`). Retirement is billed against the floor, not the current height, so a HUD that opens and closes cannot clip finished transcript rows. Rows a transient HUD displaces are held back until it clears, subject to `IDLE_CHROME_SETTLE_MS = 250 ms` (`C:33`, timer `C:349-351`) because a turn-scoped HUD can close a few frames after the last block finalizes.
- **Working row is billed at current height**, not floored, because it grows and shrinks every run; flooring it would clip rows on shrink and make the whole screen jump (`C:496-508`).
- **Slack goes below the live rows, never above** (`C:565-577`): above, it splits on-screen history from the streaming tail and reads as a hole punched into a table; below, it reads as the empty tail of a short document.
- **Compaction allocator** (one row per block) engages only when a block genuinely cannot retire (`C:524-530`).
- `AnimationFrame = { now, tick: floor(now / 80) }` (`C:515`; interface at `tui/chrome/transcript-container.ts:14-17`) — one frame object shared by retirement and viewport render, so both see identical animation state.

### 3.2 Regions

| Region | Owner | Notes |
|---|---|---|
| Native scrollback | terminal | retired history batches; immutable, never re-audited (`J:3896-3911`) |
| Header / welcome | `tui/prompt/welcome.ts` (673 lines) | gradient logo, one-shot intro, retires into history |
| Transcript | `tui/chrome/transcript-container.ts` | settled blocks + live tail; block spans for click hit-testing (`:349`, `:754`) |
| Chrome below transcript | composer roots | queued messages, HUD rows, working row, todo list, subagent panels |
| Editor / composer | `CustomEditor` + composer shapes (`tui/components/composer/{box,band,pi,rail,rule,claude,borderless}`) | shape from `composerShape` (`C:391`) |
| Status line | `tui/status-line/component.ts` (4 087 lines) | segments: model, context, git, brand, cost, reset countdowns |
| Overlays | `tui/overlays/*` (68 files) | composited over the viewport, or alt-screen when `fullscreen` |

Composer shapes are a registry (`tui/components/composer/registry.ts`); each supplies border/rail geometry plus a status-line attachment (`attachToEditor`, `C:400`).

### 3.3 Overlay and focus management

- `showOverlay(component, options) -> OverlayHandle {hide, setHidden, isHidden}` (`J:525-532`). Options: `width`/`minWidth`/`maxHeight` (number or `"50%"`), `anchor` (9 anchors, `J:430-439`), `offsetX/Y`, `row`/`col`, `margin`, `visible(termW, termH)` evaluated every frame, `fullscreen`, `mouseTracking` (`J:470-520`).
- Compositing (`#compositeOverlaysIntoWindow`, `J:3369-3395`): render each visible overlay at its resolved width, cap to `maxHeight` (keeping the *bottom* when anchored bottom), then `compositeLineAt(existing, overlayLine, col, width, termWidth)` per row. Overlay content never enters history.
- Focus: `setFocus(component)` flips `focused` on the previous/next `Focusable` and syncs hardware-cursor mode (`setUseTerminalCursor`). Overlays record `preFocus`; losing visibility restores it (`J:3083-3093`). `OverlayFocusOwner.ownsOverlayFocusTarget` (`J:369-379`) lets an overlay root delegate focus to children — which is what lets a fullscreen overlay keep the real hardware cursor for IME.

### 3.4 Scrolling model

There is **no scroll offset in the engine**. This is the most important design choice in the whole TUI:

> "The renderer never probes the user's scroll position. This keeps updates safe while the user is reading older terminal history." — `docs/tui-core-renderer.md` §3; the code never reads scroll state anywhere.

Scrolling is **native terminal scrolling**: content that finalizes is written once into scrollback and thereafter the terminal owns it. Consequences visible in code:

- `#providerViewportTop` is the screen row where the mutable viewport begins (`J:850-853`); appending K history rows moves the anchor down by K, and rows that scroll off the top are exactly the oldest history rows (`J:3854-3860`).
- If a frame shrinks, the composer keeps the input on the bottom row and parks the slack below the live rows, because rows above the live area may already be in scrollback and "no later frame can pull them back down" (`C:539-577`).
- Wheel input is delivered to whatever component is focused (input listener or `routeSelectListMouse`) — it scrolls *selectors and panes*, never the transcript. The transcript's scrollback is the terminal's own.

---

## Q4. ANIMATION INVENTORY

### 4.0 The three animation substrates

Everything animated in omp is driven by one of exactly three mechanisms:

1. **Self-rescheduling timers + `requestComponentRender`** (JS clock). The component owns a `setInterval`/`setTimeout`, mutates a frame counter, and asks the TUI for a paint. The renderer still applies its own cadence/backpressure on top.
2. **Wall-clock phase reads (no timer per widget)**. The shared spinner clock (`tui/spinner-clock.ts`) derives a *phase* from `Date.now()`, so every spinner in the process shows the same glyph on the same frame, and timers are armed to the next *boundary* (`spinnerTickDelay`) so N spinners cost one repaint per period instead of N.
3. **Terminal-clocked effects (Tern Surface Protocol)**. On a TSP terminal the app describes `spinner`, `shimmer`, `elapsed`, `meter` nodes and the *terminal* animates them; the JS timers are explicitly skipped (`isNativeRendering()` guards everywhere: `loader.ts:314-318`, `assistant-message.ts:848`, `status-line/component.ts:1403-1405`, `startup-splash.ts:84-97`).

A global switch gates decorative motion only: `motionEffectsEnabled()` / `setMotionEffects()` (`tui/motion-effects.ts:1-30`) covers "the status-line blink and brand fade, the editor's magic-keyword sweep, native `fx` spans". Spinners and streaming reveal are *not* motion-gated; they are gated by `spinnerAnimated()` (`tui.spinnerInterval === 0`) and `smoothStreaming` respectively.

### 4.1 Inventory table

| # | Effect | Where | Looks like | Driver | Exact constants |
|---|---|---|---|---|---|
| 1 | Working-row / loader spinner | `tui/components/loader.ts` | braille `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` before a status label | self-rescheduling `setTimeout` on the shared clock | period `spinnerInterval()` default **80 ms**; `RENDER_INTERVAL_MS = 1000/30` when the message colorizer animates; `STATIC_REFRESH_MS = 1000` for a static spinner with a dynamic label (`loader.ts:20-28, 326-329`) |
| 2 | Tool-card spinner | `tui/chat/tool-execution.ts` | same glyphs inside a running tool card | one shared ticker for *all* live cards | `spinnerTickDelay(spinnerInterval())`; `tickSpinner(frame)` per registered block (`tool-execution.ts:330-372, 1053-1062`) |
| 3 | Status-line spinner | `tui/status-line/component.ts` | `status` frame set: 8 braille / 12 nerd / 4 ascii | repaint only when frames flow | `ACTIVE_WINDOW_REPAINT_MS = 250`, `STATIC_SPINNER_REPAINT_MS = 1000` (`:197-199`) |
| 4 | Thinking pulse (starburst) | `tui/chat/assistant-message.ts` | `✻ ✼ ❉ ❊ ✺ ✹ ✸ ✶` breathing in place | self-rescheduling timeout with per-frame dwell | 8 frames; dwell **70–230 ms**, raised-cosine `(1-cos(2πf/8))/2`, mean ≈150 ms, scaled by `spinnerInterval/80` (`:198-214, 852-859`) |
| 5 | Text shimmer — classic band | `tui/theme/shimmer.ts` | a bright band sweeping left→right across a label | computed per render from wall clock | `SHIMMER_SPEED_CELLS_PER_S = 30`; `CLASSIC_PADDING = 10`; `CLASSIC_BAND_HALF_WIDTH = 6`; cosine bump; tiers `≥0.65 high`, `≥0.22 mid` (`:12-34, 121-125`) |
| 6 | Text shimmer — KITT scanner | same | single head ping-ponging with a decaying tail | same | `KITT_HEAD_HALF = 0.6`, `KITT_TRAIL_LEN = 7`, quadratic decay `(1-t)²`, cycle `2*(len-1)` (`:26-28, 130-145, 214-232`) |
| 7 | Streaming assistant text reveal | `ca/modes/controllers/streaming-reveal.ts` | text appears at a bounded rate instead of in provider bursts | `setInterval` **1000/30** | `MIN_STEP = 3` graphemes/frame, `CATCHUP_FRAMES = 8` → `nextStep = max(3, ceil(backlog/8))` (`:8-10, 249-251`) |
| 8 | Tool-argument reveal | `ca/modes/controllers/tool-args-reveal.ts` | the args panel fills in smoothly | same 30 fps interval | `STREAMING_REVEAL_FRAME_MS = 1000/30`; incremental JSON-string decode for `write.content`, `edit.input`, `eval.code` (`:15-21, 599-605`) |
| 9 | Status-line brand fade | `status-line/component.ts` | `pi` mark dim-gray → accent when a turn starts | 40 ms frame timer while the fade runs | `BRAND_FADE_MS = 450`; `BRAND_FADE_FRAME_MS = 40`; cubic ease-in-out; retargets from the on-screen color so an interrupt never jumps (`:190-192, 1436-1493`) |
| 10 | Speculation blink | `status-line/component.ts` | context segment toggles while background compaction speculates | `setInterval` | **600 ms** half-period, gated by `motionEffectsEnabled()` (`:1399-1420`) |
| 11 | Editor magic-keyword sweep | `tui/prompt/custom-editor.ts` | gradient crawls across `/commands` and magic words while typing | 70 ms repaint chain | `SHIMMER_FRAME_MS = 70` (~14 fps), `SHIMMER_PERIOD_MS = 1800`, phase `(now % 1800)/1800` (`:884-888, 929-930, 1133`) |
| 12 | Startup splash | `tui/setup/scenes/splash.ts` + `tui/setup/startup-splash.ts` | π logo over rippling water under a twinkling starfield | `setInterval` **33 ms** | duration `SETUP_SPLASH_MS = 2600`; `progress = elapsed/2600`; `phase = progress*1.8`; shine `pos = (progress*2.5)%1`, `strength = 1 - 0.35*progress`; `surfaceTime = frame*0.13`; 3-term wave `0.5 sin(radial−t) + 0.3 sin(diag−0.7t) + 0.2 sin(cross−1.4t)`; stars advance every `frame>>3` (`splash.ts:10-11, 167-249`) |
| 13 | Setup splash→scene dissolve | `tui/setup/wizard-overlay.ts` | cross-dissolve | `setInterval` SETUP_TICK_MS | `SCENE_TRANSITION_MS = 420` (`:29-39, 227-233`) |
| 14 | Setup outro | `tui/setup/scenes/outro.ts` | logo settles and dims out | 33 ms tick | `SETUP_OUTRO_MS = 1200`; `gradientLogo(PI_LOGO, progress*1.2, {pos:(progress*2)%1, strength:1-progress})` (`:11, 50-53`) |
| 15 | Welcome/header intro | `tui/prompt/welcome.ts` | gradient sweeps backward then settles on the resting frame | `setInterval` **33 ms** | `INTRO_MS = 3000`; `INTRO_SWEEPS = 2.5`; `INTRO_SHINE_TRAVERSALS = 3`; `eased = 1-(1-p)³`, `shineStrength = (1-eased)^1.5` (`:643-673, 289-303`) |
| 16 | Rewind selector camera slide | `tui/overlays/rewind-selector.ts` | panes slide horizontally between variants | `setInterval` **16 ms** | `SLIDE_MS = 160`; ease-out cubic `1-(1-t)³` (`:195-197, 384-417`) |
| 17 | Codex-reset fireworks | `tui/overlays/codex-reset-fireworks.ts` | 6 bursts + starfield + banner in a 33 %-height overlay | `setInterval` **85 ms** | `FRAME_COUNT = 34` (≈2.9 s loop), bursts at frames 5/9/13/17/21/25 with fixed (x,y,color) (`:16-17, 83-90, 344-352`) |
| 18 | Todo completion strike | `tui/chat/tool-execution.ts` + `tui/tools/todo.ts` | strikethrough reveals left→right over a completed task | `setInterval` **65 ms** | `TODO_STRIKE_HOLD_FRAMES = 2` + `TODO_STRIKE_REVEAL_FRAMES = 12` = 14 frames ≈ 910 ms (`todo.ts:385-392`, `tool-execution.ts:1070-1094`) |
| 19 | Tool elapsed ticker | `tui/chat/tool-execution.ts` | `12s` badge on a running tool | `setInterval` **1000 ms** | only while partial, unsealed, not background-async (`:1030-1046`) |
| 20 | Countdown timers | `tui/chrome/countdown-timer.ts`, `tui/overlays/ask-dialog.ts` | `expires in 0:57` | `setInterval` 1000 ms + one `setTimeout` for expiry | per-second repaint skipped on TSP (described `elapsed` node counts down) (`countdown-timer.ts:53-85`, `ask-dialog.ts:1104-1118`) |
| 21 | Download/judge HUD rows | `ca/modes/progress-hud.ts` | right-aligned `label ━━━─── 12/40` | event-driven + reveal/retain timers | reveal `150 ms`, done retain `1500 ms`, failed retain `10000 ms`; bar width 18, min 4 (`:19-20, 144-149`) |
| 22 | Agent-hub clocks | `tui/overlays/agent-hub.ts` | ages/elapsed in the roster refresh | `setInterval` | `AGE_TICK_MS = 5000`; data coalesce `100 ms`; picker coalesce 2 ms/row capped 1000 ms (`:214-228`) |
| 23 | Jobs sheet / job widgets | `ca/modes/interactive-mode.ts`, `ca/job-widgets/registry.ts` | live job rows | `setInterval` | `JOBS_SHEET_REFRESH_MS = 250` (`:1403, 8892`); widget `refreshMs = 1000`, `minIngestMs = 250` (`registry.ts:41-46`) |
| 24 | Native TSP effects | `tui/native/*`, `tui/overlays/*` | terminal-drawn `fx: "pulse"`, `shimmer` nodes | terminal clock | `span(..., { fx: "pulse" })` for running job pills, spotlight stars, fireworks glyphs, btw "Answering" (`jobs-panel.ts:99-101`, `spotlight.ts:375-386`, `fireworks.ts:278`, `btw-history-panel.ts:169`) |
| 25 | Progress-bar indeterminate | `tui/components/progress-bar.ts` | static `indeterminate` glyph column, terminal animates on TSP | n/a (no JS timer) | `options.style.indeterminate` glyph (`:70`), `progress {value:null}` native node (`loader.ts:77`) |

Count: **25 distinct animated effects** — 21 own a JavaScript timer, 2 (classic and KITT shimmer) are pure functions of the wall clock evaluated at render time, and 2 are delegated to the terminal (native `fx`/`spinner`/`shimmer` nodes and the indeterminate progress bar, which has no JS animation at all).

### 4.2 The spinner clock in detail

This is the most reusable idea in the codebase and worth spelling out:

```
sharedSpinnerTick(now)   = floor(now / intervalMs)          // 0 when static
sharedSpinnerFrame(n,now)= sharedSpinnerTick(now) % n
spinnerTickDelay(period) = max(1, ceil((floor(now/period)+1)*period − now))
```

(`tui/spinner-clock.ts:40-67`.) Properties:

- **Shared phase.** Every spinner — working row, tool cards, model hub, OAuth selector — reads the same wall clock, so they are frame-locked to each other with no coordination.
- **One timer per period, not per widget.** `spinnerTickDelay` lands exactly on the next multiple of the period; the comment notes the float-residue bug it fixes (`100 % (1000/30)` reading as a full period) with a `1e-6` slack (`spinner-clock.ts:56-66`).
- **Cheap "did anything change?" test.** `Loader` caches the last `#spinnerTick` and only re-syncs its text when the tick index advanced (`loader.ts:386-397`).
- **Configurable and disableable.** `tui.spinnerInterval`: default **80 ms**, `0` = static (frame 0, no timer), minimum **50 ms** (`spinner-clock.ts:5-9, 69-91`). Static mode still repaints the row once a second so an elapsed/countdown label keeps moving (`loader.ts:322-329`).

**Backpressure inside the animation.** The loader does not trust `requestComponentRender` to be cheap:

```
boundedFrameCost = min(200 ms, max(lastFrameCostMs, requestCostMs))
backpressureDelay = boundedFrameCost * 9      // keep animation ≤ ~10 % CPU
nextDelay = max(spinnerTickDelay(period), backpressureDelay)
```

(`loader.ts:399-410`, constants `RENDER_BACKPRESSURE_MULTIPLIER = 9`, `MAX_BACKPRESSURE_FRAME_COST_MS = 200` at `:24-25`.) That is a hard, explicit CPU budget for animation, independent of the renderer's own adaptive term.

### 4.3 Shimmer mechanics

`shimmerSegments(segments, theme)` (`shimmer.ts:180-260+`) is a per-frame gradient painter with a fixed *velocity* rather than a fixed sweep duration, so smoothness does not depend on message length:

- Position: `travel = (Date.now()/1000) * 30`, then `position = travel % (len + 20)` for classic, or a ping-pong `2*(len-1)` cycle for KITT (`:214-232`).
- Intensity: classic = `0.5*(1+cos(π·dist/6))` inside a 12-cell band; KITT = 1 at the head, then `(1-t)²` over 7 cells **behind** the head only ("LEDs don't predict the future", `:128-145`).
- Three tiers (`low <0.22 ≤ mid <0.65 ≤ high`), mapped to `DEFAULT_SHIMMER_PALETTE = {low:"dim", mid:"muted", high:"accent", bold:true}` (`:60-66`).
- Performance shape documented in-source: one `Date.now()`, one cached palette compile per (theme, palette) pair stashed on the palette object via a Symbol key, and **one ANSI open/close per run of same-tier chars**, not per character (`:73-119, 170-178`).
- `setShimmerMode("classic" | "kitt" | "disabled")` is pushed from the host's `display.shimmer` preference; `disabled` renders every tier as mid and skips the scan (`:36-45, 147-152`).

### 4.4 Streaming reveal — the "smooth streaming" damping

Both reveal controllers share one growth law (`streaming-reveal.ts:249-251`):

```
nextStep(backlog) = max(MIN_STEP=3, ceil(backlog / CATCHUP_FRAMES=8))
```

so a large backlog catches up in ~8 frames (sub-linear), and a slow stream still advances 3 graphemes per 33 ms tick. Grapheme-correct slicing is memoized: streaming blocks only grow by appending, and an append can only extend the *final* grapheme cluster, so `BlockUnitCounter` re-segments only from that cluster's start (`streaming-reveal.ts:73-122`). The whole controller can be switched off mid-stream, in which case the reveal snaps to the full target and pacing stops (`tool-args-reveal.ts:548-551`).

The tool-args variant additionally decodes `write.content` / `edit.input` / `eval.code` incrementally between throttled full-JSON parses, so a 1 MB file write updates the preview at reveal cadence instead of stalling for `STREAMING_JSON_PARSE_MIN_GROWTH` bytes at a time (`tool-args-reveal.ts:10-27, 69-140`).

### 4.5 Startup splash — the biggest set piece

`renderSetupSplash(width, height, elapsedMs)` (`splash.ts:167-249`) is a per-cell painter with four layers:

1. **Water** below the logo baseline: three sine terms at different spatial frequencies and speeds (`radial − t`, `diagonal − 0.7t`, `cross − 1.4t`, where `t = frame*0.13`), amplitude-weighted `0.5 / 0.3 / 0.2`, mapped through `waterChar(level)` and multiplied by an edge falloff and a vertical fade.
2. **Starfield** above the water: `skyGlyph(x, y, frame >> 3)` — twinkle steps every 8 frames (~264 ms).
3. **Hero**: the large π mark, each cell painted with a gradient escape.
4. **Skip hint** on a cleared strip at the bottom so it stays legible over water.

Gradient efficiency: every cell on a screen diagonal shares one gradient position, so escapes are resolved once per diagonal per frame (`diagonalEscapes`, `splash.ts:184-198`). Below `MIN_SCENE_WIDTH = 56` × `MIN_SCENE_HEIGHT = 22` it falls back to a compact centred logo (`:26-27, 174, 280-292`).

### 4.6 What is *not* animated

Worth recording so the inventory is trusted: there is no fade-in on messages, no typewriter effect for tool results, no animated borders, no cursor blink implementation (the hardware cursor is left to the terminal, and the software cursor is a static reverse-video cell), and no scroll animation. The HUD, tray and agent hub move only when data changes (plus their age/refresh clocks).


---

## Q5. Graphics and rich content

### 5.1 Image protocols

`ImageProtocol` (`tui/terminal-capabilities.ts:39-43`):

```ts
enum ImageProtocol { Kitty = "\x1b_G", Iterm2 = "\x1b]1337;File=", Sixel = "\x1bPq" }
```

Selection is per-terminal, from a static capability table (`terminal-capabilities.ts:593-654`):

| Terminal | Image protocol | trueColor | hyperlinks | notify | DECCARA | scroll→scrollback | OSC 66 text sizing |
|---|---|---|---|---|---|---|---|
| kitty | Kitty | yes | yes | OSC 99 | **yes** | yes | **yes** |
| ghostty | Kitty | yes | yes | OSC 9 | no | no | no |
| wezterm | Kitty | yes | yes | OSC 9 | no | no | no |
| iterm2 | iTerm2 | yes | yes | OSC 9 | no | no | no |
| vscode / alacritty / orca | none | yes | yes | Bell | no | no | no |
| warp | Kitty | yes | **no** | OSC 9 | no | no | no |
| otty | Kitty | yes | yes | OSC 99 | no | no | no |
| rio / rex / tern | Kitty | yes | yes | (OSC 99 for tern) | no | no | no |
| monstar | Kitty | yes | yes | OSC 9 | no | no | no |

Detection is env-var-first, TERM_PROGRAM second (`detectTerminalId`, `terminal-capabilities.ts:658-720`): `KITTY_WINDOW_ID`, `GHOSTTY_RESOURCES_DIR`, `WEZTERM_PANE`, `ITERM_SESSION_ID`, then `TERM_PROGRAM`.

SIXEL is not in the table because it is *probed at runtime*: either a positive XTSMGRAPHICS geometry reply or DA1 attribute 4 (`docs/tui-core-renderer.md` §5; code in `T:2537` probe timeout and `onSixelSupport`).

### 5.2 Kitty graphics: transmit-once, place-many

`tui/kitty-graphics.ts`:

- `KITTY_PLACEHOLDER = "\u{10eeee}"` (Plane 16 PUA) plus a **297-entry** row/column diacritic table (`ROWCOLUMN_DIACRITICS`, `:30-53`) → `KITTY_PLACEHOLDER_MAX_CELLS = 297` (`:54-55`). Unicode placeholders let an image occupy a *text* grid, so it reflows with rows instead of being an absolute overlay.
- `detectKittyUnicodePlaceholdersSupport()` (`:76`) gates placeholders per terminal/env; `kittyPlaceholdersFit(cols, rows)` (`:110`) bounds a grid to the diacritic table.
- `encodeKittyVirtualPlacement` / `encodeKittyPlaceholderGrid` / `renderKittyPlaceholderLines` (`:124-180`).
- `ImageBudget` (`tui/components/image.ts:109`, used at `J:1046`): `DEFAULT_MAX_INLINE_IMAGES = 8` live graphics; older ones are *demoted* — their pixels are deleted by id and the affected rows repaint with a height-preserving text fallback, never a history replay (`J:3919`, `#retireImagePlacements` at `J:3401-3414`).
- **Never retransmit base64 per frame** (`docs/tui-core-renderer.md` §6). Transmits are queued once (`takeTransmits`, `J:3920`) and placements are deleted/rewritten by id (`encodeKittyDeletePlacement`, `J:3410`).
- Ghostty can drop Kitty commands during its first post-startup settle window, so the *first* image paint is held `#GHOSTTY_INITIAL_IMAGE_DELAY_MS = 100 ms` (`J:1028-1031`, `J:2888-2911`).

SIXEL (`tui/sixel.ts`, 69 lines) is simpler because SIXEL has no delete command: `ensureSixelRasterWidth` normalizes the raster width, `cropSixelTop(sequence, pixels, maxHeightPixels)` re-encodes a top-clipped block so a viewport scrolled into the middle of an image draws only its visible slice (`J:3430-3452`).

iTerm2 inline images and transmit-and-display Kitty images **cannot be cropped**, so a block clipped at the top is omitted rather than painted over following text (`J:3416-3429`).

### 5.3 Rich text protocols

- **OSC 66** (Kitty text sizing): scaled headings/spans contribute their *declared cell width* to the width model, and a scale-`s` heading reserves `s-1` spacer rows; the renderer classifies spacer rows and preserves the glyph columns while erasing to their right (`J:1021-1024`, `J:4596-4597`, `J:4646-4647`). Maximum `s` is 7.
- **OSC 8** hyperlinks: `tui/render/hyperlink.ts` (308 lines) plus `link-hints.ts` (299 lines) — the "press a letter to open" overlay. Link hints are re-validated every frame: tags must still be drawn first, in the same order and columns, or the mode ends (`C:605-626`), which is what stops a hint from pointing at a label that scrolled away.
- **Glyph Protocol** (`tui/glyph-protocol.ts`): APC `ESC _ 25a1 ; <verb> [; k=v]* [; payload] ESC \` with verbs `s` (support), `q` (coverage), `r` (register), `c` (clear). It ships the TUI's private-use icon outlines to the terminal so the **nerd symbol preset renders without a patched font**. Registrations are fire-and-forget, session-scoped and confined to the PUA ranges (`:1-20`). Bundle generated by `scripts/gen-glyphs.ts` into `theme/glyph-bundle.json`.
- **DECCARA** (`tui/deccara.ts`): Kitty extends DECCARA to all SGR attributes, so a solid background panel can be painted as one rectangle (`ESC[2*x` … `ESC[Pt;Pl;Pb;Pr;<sgr>$r` … `ESC[*x`) instead of a full-width run of background-styled spaces per row. The module is a **pure planner**: it consumes the final ANSI strings, strips trailing background padding it can prove safe, and returns rectangles; it is emitted only when the bytes saved exceed the rectangle cost plus `DECSACE_WRAPPER_BYTES` (`:1-45`).
- **Tern Surface Protocol (TSP)**: an APC-based *semantic UI* channel. When the terminal answers the `hello` probe, the TUI stops painting rows and instead sends a described document (nodes with roles, spans, actions), reconciled into ops (`native/reconcile.ts`) and paced by terminal acks (`J:1899-1910`, `docs/tui-core-renderer.md` §5). This is why so many components carry a parallel `describe()` implementation.

### 5.4 Charts

Three-stage pipeline in `tui/charts/`:

1. `planChart(tableAnalysis)` → `ChartPlan` — local heuristics fitted on ~7 000 real assistant-answer tables (`chart-plan.ts:1-25`): label = first text column (never `#`/`Rank`), table order preserved, totals excluded from scale, units never share an axis, repeated labels get `qualifiers`, baseline rows become `reference`, decade-spanning values draw as factors of change. `worthCharting()` decides whether a picture beats the table.
2. `buildChart(plan)` → `ChartSpec`.
3. `renderChartSvg(spec)` → a standalone SVG using **figure tokens only** (`var(--fg)`, `var(--muted)`, `var(--c1…6)`, transparent background), so the host resolves them against the live theme (`chart-svg.ts:1-20`). Styled after Apple charts: hairline gridlines, rounded bars, smooth lines with area fill, dot legends, broken axes for outliers.

The SVG then goes through the ordinary `svg` fence path (5.5), i.e. it becomes an inline terminal image — or, on a TSP terminal, an `svg` figure the terminal draws itself.

### 5.5 SVG / images in the transcript

`tui/chat/svg-figure.ts` rasterizes off the JS thread via `rasterizeSvg` (native), sizes in **SVG user units per terminal row** (`PX_PER_ROW`) so `font-size="14"` text lands near the terminal's own glyph size, draws at device pixels and pads to whole cells so the terminal places it 1:1 (any resampling blurs glyph edges). Streaming fences re-raster at most every `STREAM_INTERVAL_MS = 200 ms`, keeping the previous raster on screen until the next lands; max edge `MAX_EDGE_PX = 4096` (`svg-figure.ts:20-40`).

Related: `image-loading.ts`, `figure-markdown.ts` (247 lines), `fence-figure.ts`, `embed-figure.ts`, `table-chart.ts` (a table that becomes a chart), `figure-size.ts`.

### 5.6 LaTeX

Two engines:

- **Inline** `$…$`: `latex-to-unicode.ts` maps to Unicode text (`½`, `(a+b)/c`).
- **Display** `$$…$$`, `\[…\]`: `latex-block.ts` is a real **2-D box layout engine** — `Box` = rectangle of padded lines with a `baseline` row — that stacks fractions and `\binom`, stretches delimiters (`\left…\right`, matrix brackets), lays out `matrix`/`cases`/`array` as baseline-aligned grids, places big-operator limits above/below (`\sum`, `\lim`, `\int\limits`), draws radicals, raises/lowers block scripts, draws labeled braces (`\underbrace{x}_{lbl}`), stacks `\overset`/`\underset`, and aligns `&` columns in `align` families. Flat runs delegate to `latexToUnicode`. Credited as a from-scratch reimplementation of txm's approach on an ANSI-aware Box model (`latex-block.ts:1-23`).

### 5.7 HTML forms (`ask` tool)

`tui/html-form/` (`parse.ts`, `data.ts`, `dialog.ts` — dialog is 1 000+ lines) turns an agent-authored HTML form into a terminal dialog: fieldsets become questions, the focused question is open and the rest fold to a summary row, options render as pills, dropdowns support ←/→, checkboxes/radios/notes (`n`), PgUp/PgDn to move between questions, `app.ask.hide` collapses to one row, and an optional `timeout` submits the current values through `onTimeout` (`html-form/dialog.ts:1-38`). It reuses `BracketedPasteHandler`, `SvgFigure` (for images in questions) and `ASK_RESERVED_ROWS`/`renderFoldedQuestion` from `chrome/ask-fold.ts`. On TSP it becomes a native `ask-card` (`native/ask-card.ts`) whose clicks arrive back as `NativeUiEvent`s.

### 5.8 Markdown pipeline

`tui/components/markdown.ts` is **5 289 lines** — the single largest component. Shape:

- A lexer (`lexDocument`, `:1639`) plus a hand-written renderer with per-construct layout: headings, lists, tables, blockquotes, fenced code (delegated to `render/code-cell.ts` for syntax highlighting + diff gutters), rules, footnotes.
- **Streaming support is explicit**, not incidental:
  - `streamingMarkdownSource(text)` (`:72-76`) normalizes tabs and OSC 8 terminators so an append-only publisher can compare settled prefixes with whole blocks.
  - `endsInsideOpenFence`, `holdPartialTableRow` (`:175`, `:229`) keep a half-typed fence/table from flickering.
  - `speculativeInlineClose` / `speculativeStreamingSource` (`:400-425`) close a dangling link or `$…$` **speculatively** so rendered math/link grows in place instead of raw LaTeX turning into symbols at the closing delimiter; a `$` body needs a LaTeX character, so a price (`$5 per`) is never speculated.
- Width handling routes through `wrapTextWithAnsi` everywhere (`:1014`, `:3386`, `:3809`, `:4428`), which is the shared ANSI-aware UAX#11 model (`docs/tui-core-renderer.md` §4).
- Caching: `clearRenderCache()` (`:1814`) and a `RenderedStringCache` for formatted strings; `releaseRenderCaches()` drops derived rows only, per the component contract.

`render/render-utils.ts` (1 316 lines) holds the shared primitives: `visibleWidth`, `truncateToWidth`, `sliceByColumn`, `wrapTextWithAnsi`, `replaceTabs`, `DEFAULT_TAB_WIDTH`, `coalesceAdjacentSgr` (`J:779`), plus the width-config epoch used as a cache key.


---

## Q6. Themes

Implementation: `packages/tui/src/theme/` (`theme.ts`, `theme-class.ts`, `loader.ts`, `schema.ts`, `schema-validation.ts`, `symbols.ts`, `shimmer.ts`, `color.ts`, `session-color.ts`, `dark.json`, `light.json`, `defaults.json`, `glyph-bundle.json`). Reference doc: `docs/theme.md` (385 lines) — marked `[doc]` where I did not re-verify.

### 6.1 File format `[doc]` + `[src]` for the JSON shape

Custom theme files are JSON validated by `validateThemeJson()` (`theme/schema-validation.ts`, using `@oh-my-pi/omptype`); types in `theme/schema.ts`; editor-facing JSON Schema at `theme/theme-schema.json`. Built-in embedded themes bypass runtime validation.

```jsonc
{
  "name": "dark",
  "vars": { "accent": "#febc38", "dimGray": "#5f6673", ... },   // reusable variables
  "colors": {
    "accent": "accent",          // var reference
    "text": "",                  // "" = terminal default fg
    "mdHeading": "#febc38",
    "toolDiffAdded": "...", ...
  },
  "export": { "pageBg": "...", "cardBg": "...", "infoBg": "..." },
  "terminal": { "ansi": [16 entries], "background": "..." },
  "symbols": {
    "preset": "unicode",                 // unicode | nerd | ascii
    "overrides": { "boxRound.topLeft": "…" },
    "spinnerFrames": ["-", "\\", "|", "/"]   // or { status: [...], activity: [...] }
  }
}
```

Color values accept a hex string, a numeric 256-color index, a `vars` reference (recursive; missing or circular throws), or `""` for terminal default (`ESC[39m` fg / `ESC[49m` bg).

Token groups, all required unless noted: core text/borders (11), background blocks (7), message/tool text (5 + optional `assistantMessageText`), markdown (10 + optional `mdExtensionLink`), tool diff + syntax (12), mode/thinking borders (9 + optional `thinkingMax`), status-line segments (13), agent name colors (`subagent1..6` optional), plus optional `export` / `terminal`.

A detail worth keeping: `assistantMessageText` governs **assistant prose**; the generic `text` token does not color assistant bodies (`docs/theme.md`). And `mdExtensionLink` is derived by blending `mdLink` 45 % / `text` 55 % when omitted, because such links open nothing in a browser and must read differently.

### 6.2 Loading and resolution `[doc]` + `[src]`

`loadThemeJson` order: (1) embedded built-ins (`dark.json`, `light.json`, every palette in `defaults.json`), (2) `<customThemesDir>/<name>.json`. Custom dir: `~/.omp/agent/themes`, `$PI_CODING_AGENT_DIR/themes`, or `~/.omp/profiles/<name>/agent/themes`. `getAvailableThemes()` merges + sorts, built-ins winning on name collision.

Pipeline: read JSON → parse → validate → resolve `vars` recursively → convert to ANSI for the live color mode. Conversion: hex via `Bun.color(..., "ansi-16m" | "ansi-256")`, numeric → `38;5`/`48;5`, `""` → default reset.

`theme` is a **live binding** (re-exported from `@oh-my-pi/pi-coding-agent`), so extension renderers must read `theme.fg(...)` inside their render callback rather than capturing an instance (`docs/theme.md` "Runtime switching").

### 6.3 Dark/light and appearance tracking

- Auto slot selection: OSC 11 background luminance first (unless the known-broken macOS/Zellij path), then `COLORFGBG` (index < 8 dark), then macOS appearance (`docs/theme.md`).
- Runtime tracking: DEC Mode 2031 (`?2031h`) push notifications, debounced 100 ms before re-querying OSC 11 (`T:1274-1286`); terminals without 2031 detect once at startup, deliberately **not** by polling, because an OSC 11/DA1 poll wiped the user's text selection on several terminals (#3297).
- Native Windows Terminal gets a scoped 30 s fallback poll: `WINDOWS_TERMINAL_OSC11_POLL_MS = 30_000` (`T:45`) after DECRQM confirms 2031 is unsupported.

### 6.4 Color system

`ThemeClass` compiles each token to an ANSI prefix once per color mode and caches it; `fg(token, text)` wraps text with open/close. `getColorHex` / `getFgAnsi` / `getAccentColorHex` are the accessors used by animations (the brand fade interpolates hex, the shimmer resolves tier ANSI). Session accents are derived per session name (`session-color.ts`), and agent-name colors are contrast-checked in OKLab against every surface a name appears on (`docs/theme.md`).

`tui.colorfulMarkdown` (default on) repaints headings/strong/emphasis/inline code/table headers from `terminal.ansi` instead of `mdHeading`/`mdCode` — H1 magenta, H2 yellow, H3–H6 blue, strong/emphasis magenta, inline code green, headers yellow.

### 6.5 Symbols

`theme/symbols.ts` (1 500+ lines) defines `SPINNER_FRAMES` per `SymbolPreset` (`:1513-1527`):

```ts
unicode: { status: ["⣾","⣽","⣻","⢿","⡿","⣟","⣯","⣷"],  activity: BRAILLE_10 }
nerd:    { status: [12 nerd glyphs],                        activity: BRAILLE_10 }
ascii:   { status: ["[|]","[/]","[-]","[\\]"],               activity: ["-","\\","|","/"] }
```

Box drawing is split: `boxRound.*` for all outlined chrome (rounded corners + tees/crosses from `boxSharp.*`), `boxSharp.*` for markdown tables only — markdown tables keep fully sharp corners (`docs/theme.md`).

In interactive mode a successful Glyph Protocol handshake upgrades a *default-provenance* Unicode preset to Nerd icons without changing the saved setting; an explicit preset is never upgraded; a live Tern surface forces Nerd process-locally and restores on close.

### 6.6 Customization points

1. Theme JSON (colors, vars, symbols, spinner frames, export colors, terminal palette).
2. Settings: `theme.dark`, `theme.light`, `symbolPreset`, `colorBlindMode`, `display.shimmer`, `tui.maxFps`, `tui.spinnerInterval`, `tui.motion`, `tui.mouse`, `composerShape`, `tui.colorfulMarkdown`, `tui.hardwareCursor`, `tui.imeSafeCursor`, `tui.autocompleteMaxVisible`, `tui.maxInlineImages`, `tui.resizeScrollback`, `tui.completeScrollback`.
3. Extension points: `ExtensionUIContext.custom`, custom tool `renderCall`/`renderResult` (+ `describeCall`/`describeResult` on TSP), `Theme` passed to factories.

---

## Q7. Terminal compatibility

### 7.1 Capability detection

`detectTerminalId(env)` (`terminal-capabilities.ts:658-720`) → `TerminalInfo` (`:96-140`, table at `:593-654`). A `RuntimeTerminal` clone lets runtime probes flip capabilities in place (`clone()`, `:126-131`).

Runtime probes are all **DA1-sentinel-ordered** so an unsupported terminal resolves without leaking probe bytes into the editor (`T:729-731` `PROBE_GUARD_BEGIN/END` = `ESC 7 ESC[?7l ESC[8m` … `ESC[28m ESC[?7h ESC 8`, which hides and protects the probe):

| Probe | Mechanism | Consequence |
|---|---|---|
| synchronized output | DECRQM `?2026` (`T:1296`) | begin/end markers vs plain writes |
| in-band resize | DECRQM `?2048` (`T:1297`) | enable only after confirmation |
| appearance changes | DECRQM `?2031` + `?2031h` (`T:1274-1298`) | live dark/light tracking |
| bracketed paste | DECRQM `?2004` (`T:1304`) | confirms DEC 2004 so the unbracketed-paste heuristic can trust the stall probe |
| scroll-to-bottom modes | DECRQM `?1010`/`?1011` (`T:1305-1307`) | disabled while omp owns the TTY so typing doesn't yank a scrolled-back reader to the tail |
| background color | OSC 11 (DA1-sentinel) | theme slot |
| OSC 99 notifications | query behind DA1 FIFO (`T:1257-1260`) | notification protocol |
| OSC 7501 program status | query requiring a real reply (`T:1261-1263`) | status records |
| Glyph Protocol | APC `25a1;s` (`T:1265-1268`) | bundled icons |
| TSP | APC `tsp` `hello`, timeout `TSP_TMUX_HELLO_TIMEOUT_MS = 1000` (`T:71, 1270-1272`) | semantic UI mode |
| SIXEL | XTSMGRAPHICS geometry or DA1 attr 4 (`T:2537`) | image protocol |
| cell size | OSC 1337 `ReportCellSize` (`J:119-120`) | image pixel sizing |

Private-CSI replies can split across stdin flushes, so reassembly retains partial replies until their terminator and never leaks them as user input — a stated requirement with typed sentinel owners (`docs/tui-core-renderer.md` §5).

### 7.2 Multiplexers

Registry (`terminal-multiplexer.ts:14-24`), in classification order: **herdr, tmux, screen, zellij, cmux, wmux, orca**. Precedence tiers: `session` (in-pane markers, outranks `TERM`) and `outerApp` (below `TERM`, so a multiplexer nested inside an app still wins) (`multiplexers/types.ts:1-6`).

Per-multiplexer policy fields that the engine consumes:

- `expandsRepPadding: true` (tmux, `multiplexers/tmux.ts:38`) → destructive replays use compact blank-row reuse (`compactReplay`, `J:3847`).
- `expiresSynchronizedOutput: true` (tmux, `:40`) → long replays renew the sync block every `SYNC_OUTPUT_RENEW_BYTES` (`J:3864-3877`).
- `hyperlinks`: `"render"` (herdr), `"drop"` (screen — a screen anywhere in the path vetoes links even under nested tmux), or `{forward:{termProgram, major, minor}}` (tmux 3.4+, `tmux.ts:29-31`).
- `backgroundColorCache` with `passthrough(query)` + `settleMs` — tmux answers OSC 11 from a cache, so a refresh must first pass the query through and wait for the cache to fill (`multiplexers/types.ts:56-64`, `T:1038-1041`).
- Notification tiers `pane` → `surface` → `inBand`, innermost wins (`types.ts:8-14`).

tmux specifics (`tui/tmux.ts`): at startup the local tmux server is asked for `#{client_termtype}` / `#{client_termname}` so the *client* terminal (kitty/ghostty/wezterm) selects the image protocol; the pane's own `tmux-*` TERM never does. Modified keys need tmux `extended-keys`; OSC notifications and Kitty graphics need `allow-passthrough` (`docs/tui-core-renderer.md` §5; `wrapTmuxPassthroughIfNeeded`).

### 7.3 Windows

Three distinct environments are handled:

1. **ConPTY hosted** (`isConPTYHosted()`, `T:692`) — writes chunked at 16 KiB (`T:150-175`); `hostOwnsGridOnResize === true` so a rebuild resize is allowed even for height-only changes (`J:3627`); per-read DEC 2004 reassertion is suppressed while a paste is in flight (`T:1918-1921`, `#rearmBracketedPaste` at `T:2794`).
2. **Native Windows console** (`#nativeWindowsConsole`, `T:954`) — `ENABLE_VIRTUAL_TERMINAL_INPUT` via `kernel32` FFI; win32-input-mode decoder; AltGr repair; crash-path `emergencyTerminalRestore()` that mirrors `stop()`'s mode resets without relying on the event loop (`T:397-410`).
3. **Windows Terminal quirks** — Kitty keyboard restore is `CSI = flags ; 1 u` rather than a pop, because WT 1.25–1.27 restores pre-push flags on `?1049l` and silently drops to legacy keys (`T:970-976`); Ctrl+Enter may arrive as `\n`, so `matchesEffectiveKey` recognizes modifier-tagged LF (`keybinding-matchers.ts:83-90`).

Also Windows-specific: `isWindowsTerminalSession()` (`keys.ts:34`) and `matchesRawBackspace` (`keys.ts:48`) for raw-mode backspace differences.

### 7.4 Fallbacks

- No truecolor → 256-color conversion at theme compile time.
- No synchronized output → the row-move (`DL/IL` + line-feed scroll) writer path (`J:3923-3933`).
- No Unicode placeholders → Kitty direct placements, or SIXEL, or text fallback on image demotion.
- No 2031 → one-shot appearance detection (+ WT poll).
- No TSP → row painting; no Glyph Protocol → plain Unicode/ASCII symbols.
- No kitty keyboard → legacy + `modifyOtherKeys` decoding.
- Unknown/ignored DECRQM → resolved as unsupported by the DA1 sentinel.


---

## Q8. Performance and robustness

### 8.1 Frame budget and throttling

| Mechanism | Where | Value / behaviour |
|---|---|---|
| FPS ceiling | `J:2943-2968` + `frame-rate.ts` | `tui.maxFps`, default **30**, integer 1–120, `parseMaxFps` throws on fractional/out-of-range |
| Input floor | `J:974-985, 2951-2953` | a frame a keystroke is waiting on is paced at **1000/30**, never slower than the ceiling |
| Adaptive backpressure | `J:2955-2962` | next frame ≥ `lastStart + 2×lastFrameCost`, capped at `#MAX_ADAPTIVE_RENDER_MS = 200` (≈5 fps floor) |
| Output gate | `J:2989-3015` + `T:272` | if `pendingOutputBytes > 256 KiB`, defer the frame until `onOutputDrain`; force/clear intent survives |
| Animation CPU budget | `loader.ts:399-410` | spinner re-arms no sooner than `9 × boundedFrameCost`, cost capped at 200 ms → ≤ ~10 % CPU |
| Component-scoped repaints | `J:2873-2876` | spinner/blink ticks call `requestComponentRender(component)`; containers reuse every other subtree (issue #4377 is cited in-source as the reason: a full tree walk at 30 fps cost ~5 % CPU, cascading to ~20 %) |
| Row prep memo | `J:4264-4330` | content+width+epoch+protocol keyed; a scrolled row reuses its prepared form |
| Container memo | `J:537-667` | concatenation rebuilt only when a child's array identity changes |
| Grapheme slice memo | `streaming-reveal.ts:73-122` | re-segments only from the boundary cluster |
| Shimmer palette memo | `shimmer.ts:73-119` | one compile per (theme, palette), ANSI per same-tier run |

### 8.2 Loop watchdog

`LoopWatchdog` (`tui/loop-watchdog.ts`, 197 lines): always-on event-loop lag probe, armed in `start()`, disarmed in `stop()`, `unref`'d so it never pins the process.

- `intervalMs` **250**, `thresholdMs` **250**, `sleepMs` **60 000** (`:86-89`).
- A tick `thresholdMs` past its recorded deadline = a block. Logged once per episode on the rising edge (`#wasBlocked`), with `blockedMs`, `cpuMs`, `phase` (from `takeLoopPhaseAttribution`), optional `phaseMs`, and stack attribution (`:171-191`).
- **Suspension vs block** is decided by CPU time, not duration: a gap > `sleepMs` with `cpuMs < blockedMs × 0.01` is system sleep, not a wedge (`CPU_BUSY_RATIO = 0.01`, `:37-38, 149-152`). Rationale in-source: cgroup throttling and scheduler contention make a CPU-bound loop consume far less CPU than wall time, so duration alone cannot distinguish them, and suppressing on duration discards the worst stalls.
- `isStalled()` (`:139-147`) also answers "was a block ending in the last `thresholdMs`?" — used by input handling to decide whether a burst of keystrokes was batched by a stall.

### 8.3 Loop sampler

`tui/loop-sampler.ts` uses **JavaScriptCore's sampling profiler** (`bun:jsc` `startSamplingProfiler` / `samplingProfilerStackTraces`) to name the code behind a block, because phase labels only cover a handful of call sites.

- Samples the JS thread every **1 ms** while it runs JS, nothing while idle.
- Disabled with `PI_LOOP_SAMPLER=0|false`, or automatically under `--cpu-prof` (the two profilers would steal each other's samples) (`:55-70`).
- No stop API: one process-wide profiler; an idle discard timer drops the buffer after `IDLE_DISCARD_MS = 2000` (`:44-49, 81-86`).
- `summarizeBlock(samples, blockedMs)` (`:150-240`): most frequent innermost **non-native** frame is the `cause`; callers present in ≥50 % of window samples are ranked by average depth and trimmed to the entry point plus the 3 nearest the cause (`TOP_LIMIT = 4`). Emits `{cause, share, top}`.

### 8.4 Output stall watchdog

`StdoutStallWatchdog` (`T:310-370`): bounds a never-draining stdout backlog **without** killing a single large but actively-draining frame (a `--resume` repaint of many inline images can be tens of MiB, #10430).

- `armBytes = MAX_STDOUT_BACKLOG_BYTES = 64 MiB`, `clearBytes = STDOUT_BACKLOG_CLEAR_BYTES = 256 KiB`, `stallMs = STDOUT_STALL_TIMEOUT_MS = 60 s`, poll `STDOUT_STALL_POLL_MS = 250` (`T:262-287`).
- An *episode* starts above `armBytes` and ends only when drained to `clearBytes`; during it, disconnection is declared only when the low-water mark makes **no progress** for `stallMs`. A slow-but-alive reader keeps lowering the mark; a wedged one is torn down.
- The TUI's render gate uses the same `clearBytes` value so the watchdog stays armed across exactly the range where frames are deferred (`T:264-273`, comment).

### 8.5 Robustness details worth recording

- **Never throw in the render hot path.** Over-wide viewport rows are truncated and a provider returning too many rows logs + slices in production (throws only in test/dev) (`J:3559-3564`); width helpers clamp (`docs/tui-core-renderer.md` §4).
- **Terminal safety on resize.** Erases are absolute-addressed below a row or cursor-relative, never a full-screen clear, because tmux and Windows conhost archive a screen-wide erase into pane history (#9597, #9780 documented at `J:3647-3686`).
- **ConPTY pending-wrap scroll.** Every rewrite ends at column zero (`J:4634-4638`).
- **Image budget retry loop.** `do { render } while (imageBudget.endPass())` — a demotion mid-render forces one clean re-composition rather than emitting inconsistent placements (`J:3555-3566`).
- **Kitty keyboard on alt-screen.** Entry re-pushes the modified-key frame; exit pops it, and `emergencyTerminalRestore()` mirrors every mode reset on the crash path (`T:397-410`).
- **Debug server** (`tui/debug-server.ts`, class `TuiDebugServer` at `:208`): a socket that serves ops `frame`, `doc`, `tsp`, `tree`, `values`, `quit` (`:313-330`), backed by `#debugPaint` (last prepared lines, window top, alt flag, cursor) and per-component `debugId`/`debugKind`/`debugState()`/`debugChildren` (`J:254-262`, `:941-949`). `doc` returns the reference document for a TSP surface (every frame applied by `native/apply.ts`), `tsp` returns recent frames. The repo's `.omp/tools/tui.ts` drives it against a real PTY `[doc]` (`docs/tui.md`).
- **Desktop notifications** (`tui/desktop-notify.ts`) and multiplexer routing (tiers pane → surface → inBand) are the out-of-band alert path, not part of rendering.

---

## Q9. Tool rendering, tray, and Agent Hub

### 9.1 Tool calls → cards

`ToolExecutionComponent` (`tui/chat/tool-execution.ts`, 2 382 lines) is a `Container` that owns one tool call's whole life:

- **Renderer selection** (`:589`): `toolRenderers[toolName]` (built-in) unless `useBuiltInRenderer === false`; a tool's own `renderCall`/`renderResult` take the custom path (`:607-608`).
- **Streaming vs final.** `updateArgs(args)` arrives repeatedly while the JSON streams (`:770`); `updateResult(result, isPartial)` flips `#isPartial` and, on the final frame, `#presentation.recheck()` and job-frame settlement (`:871-906`). `renderCall`/`renderResult` receive `{expanded, isPartial, spinnerFrame?}` `[doc]` (`docs/tui.md` §3).
- **Spinner lifecycle** (`#updateSpinnerAnimation`, `:968-1030`): a block animates only if (a) args are still streaming for a body-streaming tool, (b) it is a live partial whose renderer declares `animatedPendingPreview` / `animatedPartialResult`, or (c) it is a `wait` placeholder. `todo` and detached background jobs are deliberate *static* exceptions because their rows can be superseded or committed to scrollback. Registration is with the shared ticker (§4.2), so N live cards = 1 timer.
- **Elapsed ticker** only when `renderer.tickElapsed === true` and the block is partial/unsealed (`:1030-1046`).
- **Todo strike** runs after a `todo` result with `completedTasks` (`:1070-1094`).
- **Adapters**: `SafeToolRendererComponent` (`:147`) isolates a throwing custom renderer; `parksAsBackground` / `asyncJobState` / `isSettledJobFrame` / `settleWithJobFrame` (`:279-302`) reconcile a tool that becomes a background job; `toolRenderName` (`:73`) maps wire names to display names and degrades gracefully when the tool is missing.
- **Card chrome** is shared: `render/tool-card.ts` (430 lines) defines `ToolCardPhase = pending|running|partial|success|warning|error|info`, `variant: "framed" | "plain"` ("framed cards own their border; plain cards flow inline"), lazy `ToolCardContent`, and `ToolCardSnapshot` with `header`/`status`/`headerMeta`/`footerMeta`/`body`/`sections` (`tool-card.ts:20-50`). `render/output-block.ts` (390 lines) and `render/code-cell.ts` (358 lines) supply the framed block + syntax-highlighted code cell; `render/diff.ts` supplies the diff body.

### 9.2 Diff display

`tui/chrome/diff.ts` (330 lines):

- Parses canonical and legacy diff lines (`CANONICAL_DIFF_LINE` = `[+- ]<n>|content`, `LEGACY_DIFF_LINE`) so both omp's own diff format and raw `git diff` render.
- Indentation is visualized with dim glyphs: a tab becomes ` → ` (padded to `DEFAULT_TAB_WIDTH`), each leading space becomes `·` — but only before the first non-whitespace char (`:22-40`). This makes indentation-only changes visible.
- Word-level intra-line highlighting uses the native `diffWords`; SGR dim/normal is additive (`ESC[2m` / `ESC[22m`) so foreground/background colors survive.
- Diff bodies can also travel as structured `TspDiffHunk`s for TSP terminals (`:3`).

### 9.3 Tray

`ca/modes/tray.ts` (1 655 lines) is the subagent/job card cluster that hangs off the status row. Model:

- `TrayItem` is a union of `agent`, `job`, `done`; every item is a `TrayCardFace` with `look: boolean` — **"whether its closer look adds anything to its face; `tray.peek` on one that does not is ignored"** (`tray.ts:74-78`).
- `look` is computed, not declared: an agent gets `look: agentHasLook(session, text)` (`:524`, `:603`); a job gets `look: view !== undefined || output.split("\n").length > TRAY_OUT_LINES || (command !== "" && command !== name)` (`:655`) — i.e. it only advertises a closer look if it actually has more to show. Cards with nothing extra serialise with `look: false` (`:881`), and the peek handler returns early for them (`:1587-1589`), so `tray.peek` on a lean card leaves any open closer look untouched instead of opening an empty one.
- Peek lifecycle: `#peekRef` + `#peekDetail` are re-derived on every `refresh()`; if the card disappears or loses `look`, the peek is dropped (`:1465-1470`). `refresh()` returns "changed" only when faces, order, the closer look, or the main-agent projection changed, so a quiet tray costs nothing.
- Card content limits: `TRAY_CALL_ELAPSED_MIN_MS = 5000` (elapsed marker only after 5 s), `TRAY_TAIL_LIMIT = 5` operations in a closer look, `TRAY_DONE_NAMES = 3`, `TRAY_DONE_LIST_LIMIT = 50` finished agents in the done card's closer look, `TRAY_QUIET_CARD_LIMIT = 48`, `TRAY_OUT_LINES = 2` / `TRAY_PEEK_OUT_LINES = 3` / `…_BARE = 4` (`:45-65`).
- Actions (`:1576-1600`): `tray.peek` (open closer look), `tray.open` (focus/open the agent, ignored for a `done` card — its closer look lists the agents and each row opens one), plus per-card stop/dismiss. On a TSP terminal the tray is *host-rendered* (`setOnHost(cx.feature("tray"))`), and a caption replaces the status rows when the main agent shows none.

### 9.4 Agent Hub

Composition: `tui/overlays/agent-hub.ts` (~1 300 lines) + `agent-hub-projection.ts` (roster/activity projections) + `agent-hub-renderer.ts` (rows) + `agent-hub-types.ts`; host wiring in `ca/modes/agent-hub-runtime.ts` (111 lines) and `agents-hub.ts`/`agents-hub-deps.ts`.

- **Layout**: a frame of `top / divider / body / footer / bottom` rows (`agent-hub.ts:482-492`), body = `SplitPane` (roster | inspector) or, on narrow terminals, a single pane where **Tab replaces the roster with the selected-agent inspector** (`:456`). Thresholds: `SPLIT_MIN_WIDTH = 96`, `DETAIL_MIN_WIDTH = 34`, `ROSTER_MIN_WIDTH = 48` (`:117-119`).
- **Views**: `HubViewMode = "roster" | "tree"` (`:125`), with roster status scopes for filtering (a "phone" client filters by status, `:126-140`).
- **Roster data**: `AgentRegistry.global()` refs, filtered to the current session tree (`inSession`), plus persisted subagents registered while the overlay lives (`loadPersisted`), plus an `AgentActivityIndex` for live activity and an `IrcBus` for inter-agent messages (`agent-hub-runtime.ts:43-80`). Cost totals aggregate the whole subagent tree and deliberately exclude the main session's advisors, which the status line bills separately (`sumSubagentTreeCost`, `:85-111`).
- **Live transcript**: `openChat()` pushes a fullscreen `AgentTranscriptViewer` that tails the agent's session **file** incrementally (`agent-transcript-viewer.ts`, `POLL_MS` polling at `:234`), borrowing the alternate screen and restoring the Hub beneath it on close (`docs/tui.md`, "Built-in full-screen surfaces"). The parser is `parseSessionEntries` filtered to `message`/`model_change`, with assistant usage normalized on read (`agent-hub-runtime.ts:20-33`).
- **Steering input**: the inspector revives a parked/killed agent (`lifecycle`), then prompts or steers it — the header comment states "revives a parked or killed agent, then prompts/steers it" (`agent-hub.ts:9-12`). Steering therefore reuses the ordinary session prompt path rather than a separate channel.
- **Cadence**: `AGE_TICK_MS = 5000` for ages/elapsed, `DATA_CHANGE_RENDER_COALESCE_MS = 100` to coalesce bursts, picker coalescing 2 ms/row capped at 1000 ms, clock reuse 1 s, double-tap window 500 ms (`:214-230`).


---

## Appendix A. Component / module inventory

Sizes are line counts of `*.ts` per directory (generated with `wc -l`), useful as a weight map of the subsystem.

| Directory | Files | Lines | What lives there |
|---|---|---|---|
| `tui/src/components` | 26 | 19 737 | primitives: `text`, `box`, `editor`, `input`, `form`, `markdown` (5 289), `table`, `select-list`, `settings-list`, `scroll-view`, `scroll-viewport`, `sparkline`, `progress-bar`, `loader`, `image`, `tree-view`, `tab-bar`, `disclosure`, `menu-selection`, `key-value-list`, `section`, `metric`, `spacer`, `truncated-text`, `cancellable-loader`, `wizard-step` |
| `tui/src/components/layout` | 4 | 1 275 | `geometry`, `row`, `stack`, `split-pane` |
| `tui/src/components/composer` | 11 | 577 | composer shapes: `box`, `band`, `pi`, `rail`, `rule`, `borderless`, `claude`, `field`, `registry`, `types` |
| `tui/src/chat` | 47 | 16 065 | transcript block components: `assistant-message`, `thinking-display`, `tool-execution`, `user-message`, `bash-execution`, `svg-figure`, `table-chart`, `spotlight`, `todo-reminder`, `compaction-summary-message`, `transcript-*`, `read-tool-group`, `reaction`, ... |
| `tui/src/chrome` | 33 | 5 749 | frame/gutter chrome: `transcript-container`, `tool-activity`, `diff`, `streaming-panel`, `dynamic-border`, `message-frame`, `overlay-box`, `context-map`, `countdown-timer`, `live-board`, `qrcode`, `status-notice` |
| `tui/src/overlays` | 74 | 50 187 | every dialog/full-screen surface (see Appendix B) |
| `tui/src/status-line` | 15 | 7 636 | segment model + renderer (`component.ts` 4 087) |
| `tui/src/prompt` | 32 | 8 534 | `composer` (1 324), `custom-editor` (2 139), autocomplete family, `welcome`, `magic-keywords`, `gradient-highlight`, `queued-messages`, `typing-word` |
| `tui/src/theme` | 13 | 5 344 | theme class/loader/schema/symbols/shimmer/color + JSON |
| `tui/src/render` | 19 | 4 590 | `render-utils` (1 316), `tool-card`, `output-block`, `output-pane`, `code-cell`, `diff`-adjacent helpers, `hyperlink`, `link-hints`, `row-moves`, `tree-list`, `hanging-wrap`, `width-aware-text` |
| `tui/src/native` | 18 | 5 200 | TSP backend: `backend`, `reconcile`, `describe`, `encode`, `node`, `overlay`, `ask-card`, `blobs`, `measure`, `memo`, `quiet`, `state`, `apply` |
| `tui/src/html-form` | 3 | 3 189 | `parse`, `data`, `dialog` (ask-tool forms) |
| `tui/src/charts` | 3 | 11 267 | `chart-plan`, `chart-svg`, `table-data` |
| `tui/src/apps` | 12 | 4 141 | standalone TUI apps: git TUI, `ps-top`, `live-visualizer`, `shell-visualizer`, session picker, debug viewers |
| `tui/src/multiplexers` | 9 | 347 | per-multiplexer policy modules |
| `tui/src/setup` (+`scenes`) | 5 + 9 | 866 + 1 960 | startup wizard, splash, outro, composer/sign-in/theme/model scenes |
| `tui/src/tools` | 63 | 27 177 | per-tool renderers + tool-local UI models |
| `ca/src/modes` | — | — | `interactive-mode.ts` 10 226, `tray.ts` 1 655, `progress-hud.ts` 308, `agent-hub-runtime.ts` 111, `status-line-host.ts` 88, `warp-events.ts` 239, `startup-composer.ts` 132, controllers/ |
| `ca/src/modes/components` | 1 (+4) | 45 | **almost empty by design** — only `index.ts` and `extensions/{dashboard,inspector,mcp}-runtime.ts` + `state-manager.ts`; the actual UI components live in `@oh-my-pi/pi-tui` |

That last row is the single most surprising structural fact in the repo: the application's *view layer* is almost entirely in the `tui` package, and `coding-agent/src/modes` is orchestration (controllers, session wiring, keybinding/theme injection). A reader expecting a React-style app/components split will not find one.

### Appendix B. Overlay inventory (`tui/src/overlays`, 74 files)

Pickers/selectors: `model-picker`, `model-selector`, `model-browser`, `model-hub`, `session-selector`, `session-account-selector`, `theme-selector`, `thinking-selector`, `tree-selector`, `rewind-selector`, `plugin-selector`, `plugin-settings`, `settings-selector`, `queue-mode-selector`, `reset-usage-selector`, `show-images-selector`, `copy-selector`, `history-search`, `move-overlay`, `logout-account-selector`, `oauth-selector`, `mcp-add-wizard`.

Dialogs: `approval-dialog`, `ask-dialog` (+ `ask-fold`), `login-dialog`, `error-banner`, `plan-review-overlay`, `plan-save-overlay`, `plan-toc`, `hook-editor`, `hook-input`, `hook-selector`, `btw-panel` / `btw-history` / `btw-inject` / `btw-note`, `omfg-panel`, `cleanse-panel`, `report-panel`, `jobs-panel`, `job-tree`, `context-viewer` (+ `-format`, `-overview`), `usage-dashboard`, `stats-notice`, `session-info-overlay`, `link-browser`, `annotation-overlay`, `spotlight-sheet`.

Full-screen / live surfaces: `agent-hub` (+ `hub-frame`, `agent-hub-projection`, `agent-hub-renderer`, `agent-hub-types`), `agents-hub`, `agent-transcript-viewer`, `agent-activity`, `running-subagent-badge`, `pause-screen`, `codex-reset-fireworks`, `bordered-loader`, `composer-shape-preview`, `snapcompact-shape-preview`, `advisor-config`, `agent-identity`, `session-observer-registry`.

### Appendix C. Checklist

| Item | Status | Notes |
|---|---|---|
| **Q1** Rendering architecture — component model, diff algorithm, ANSI handling, scroll regions, cursor, scheduling, damage tracking, resize | **Answered** | §Q1.1-1.7. No cell-level damage tracking (row identity diff + row-move model); no scroll *regions* in the DECSTBM sense — the viewport is an anchored row range and scrolling is native. |
| **Q2** Input pipeline — stdin, key parsing, dispatch, keybinding matching, vim, kill-ring, paste, mouse, autocomplete, Windows | **Answered** | §Q2.1-2.10. Notably: no chord/sequence keybindings (single keystroke → action set lookup); vim is a pure state machine. |
| **Q3** Screen layout — regions, composition, overlay/focus, scrolling | **Answered** | §Q3.1-3.4 + diagram at top. |
| **Q4** COMPLETE animation inventory + constants | **Answered** | §Q4.1 table: 25 effects (21 JS-timer, 2 wall-clock shimmer, 2 terminal-clocked), all constants cited. |
| **Q5** Graphics & rich content — kitty, sixel, glyph protocol, DECCARA, images, charts, LaTeX, HTML forms, markdown | **Answered** | §Q5.1-5.8. |
| **Q6** Themes — format, loading, colors, dark/light, customization | **Answered** (format/loading partly `[doc]`) | §Q6.1-6.6. I did not read `theme-class.ts` (2 000+ lines) end to end; the color-compilation claim is from its call sites + doc. |
| **Q7** Terminal compatibility — detection, multiplexers, Windows, fallbacks | **Answered** | §Q7.1-7.4. |
| **Q8** Performance & robustness — watchdog, sampler, budget, throttling, debug server | **Answered** | §Q8.1-8.5. |
| **Q9** Tool rendering, tray peek/`look:false`, agent hub | **Answered** | §Q9.1-9.4. |
| **Unresolved / gaps** | — | (1) `interactive-mode.ts` (10 226 lines) was sampled, not read end to end; its controller wiring is described from the pieces I did read plus the layout contract. (2) `status-line/component.ts` (4 087) and `markdown.ts` (5 289) were read structurally + at the animation/pipeline hot spots, not exhaustively. (3) Exact per-terminal glyph sets in the nerd preset are only counted, not enumerated. (4) TSP wire schema (`native/reconcile.ts`, `pi-wire`) was not analyzed beyond its role. (5) `charts/chart-svg.ts` is 10k+ lines of drawing code — I characterized the pipeline and style, not every mark type. (6) Resize alt-borrow internals were read at the public seams (settle/probe/replay) but not line-by-line through `#settleResizeAltPaint`. |
