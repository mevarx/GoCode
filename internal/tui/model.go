package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/safetext"
	"github.com/mevarx/GoCode/internal/tools"
)

type ChatRole int

const (
	RoleUser ChatRole = iota
	RoleAssistant
	RoleTool
	RoleSystem
	RoleError
)

type ChatMessage struct {
	Role    ChatRole
	Label   string
	Content string
	Diff    string
	// Done marks a successful tool result, so its settled label uses the
	// success style. RoleError already carries failure styling.
	Done bool
	// wrap marks prose reports from GoCode, rather than fixed-column tool output.
	wrap bool
}

type agentChunkMsg struct{ delta string }
type agentDoneMsg struct{ err error }

// agentTurnStartMsg arms the status-bar clock; the engine emits it before the
// first delta of a turn, so the elapsed time is measured, not guessed.
type agentTurnStartMsg struct{}

// agentTurnMetricsMsg carries the finished turn's summary (tokens, elapsed,
// context). metrics is nil for a command-less settlement or a caller that
// emits the kind without a body. cancelled marks an settlement: a queued
// turn dropped by cancel before it entered the stream (no metrics, no run).
type agentTurnMetricsMsg struct {
	metrics   *agent.TurnMetrics
	cancelled bool
}

type agentToolMsg struct {
	name       string
	result     string
	isError    bool
	toolCallID string
	diff       string
	// steeringApplied marks the engine's "queued steering applied" notice so
	// the model can clear the queued-steering chip.
	steeringApplied bool
}

// Opens a pending card so the UI shows the call while awaiting approval.
type agentToolStartMsg struct {
	name       string
	args       string
	toolCallID string
}

// agentToolDeltaMsg carries a streamed argument fragment; the pending card is
// upserted by toolCallID, so deltas never create duplicate cards.
type agentToolDeltaMsg struct {
	name       string
	args       string
	toolCallID string
}

// Notice renders before the program stops.
type agentExitMsg struct{ notice string }
type approvalRequestMsg struct{ req ApprovalRequest }

// mouseWheelLines is how far one wheel notch scrolls the transcript. Three
// lines matches a terminal's own line scroll and the PgUp/PgDn granularity the
// help line advertises.
const mouseWheelLines = 3

type Model struct {
	width  int
	height int
	ready  bool

	viewport  viewport.Model
	messages  []ChatMessage
	streamBuf *strings.Builder

	textarea    textarea.Model
	focus       focusState
	focusedItem int

	// transcript view state: per-item expansion and horizontal scroll, the
	// rendered item geometry (filled by renderMessages), and the sidebar's
	// scroll offset.
	expanded      map[int]bool
	hOffset       map[int]int
	spans         []itemSpan
	sidebarOffset int

	// @file completion popup and status toast.
	fileComp fileCompletion
	toast    toastState
	toastSeq int

	// persistent prompt history (opt-in via enablePersistentHistory).
	historyFile    string
	persistHistory bool

	providerName  string
	modelName     string
	version       string
	workspaceRoot string
	streaming     bool

	// In model, not a global: Bubble Tea copies the model by value on Update.
	mascot mascot
	// False stops redrawing entirely instead of burning CPU while idle.
	animating bool
	// Incremented per turn so stale frames can't re-arm the ticker.
	animEpoch int
	// The shared clock's latch (animclock.go): one outstanding tick at a
	// time, its arm sequence (lost-tick recovery supersedes stale arms), and
	// the last accepted tick time the recovery window is measured from.
	clockArmed bool
	clockSeq   int
	lastTickAt time.Time

	// Modal surfaces share one focus stack (overlay.go); approval always wins.
	overlays     overlayStack
	modelPicker  list.Model
	palette      list.Model
	paletteItems []list.Item

	// sessionPicker is the /sessions overlay. Its rows come from the same store
	// listing the engine's text command prints, loaded off the update loop.
	sessionPicker list.Model
	sessionItems  []list.Item

	// pickerRefreshInFlight dedupes the model-picker refresh: opening the
	// picker while a collection is still running must not start a second one.
	pickerRefreshInFlight bool
	// pickerKeep is the model a refresh should re-select once an active
	// filter's matches land; nil when no filtered refresh is pending.
	pickerKeep *ModelItem

	// completion is the in-progress Tab cycle and its help-line echo.
	completion     completionState
	completionHint string

	// promptHistory is the in-memory Alt+P/Alt+N recall list.
	promptHistory promptHistory

	approvalReq   ApprovalRequest
	approvalFocus int
	// Tools the user granted exact-args approval for during this UI session,
	// displayed in the modal footer. The authoritative copy lives in the gate.
	sessionGrants map[string]bool

	// dialog state: input grace, reopen-skip identity, quit focus, diff
	// view toggles, the per-turn read-only badge, session picker mode, the
	// theme picker/editor and the sidebar data feed.
	grace             approvalGrace
	lastApprovalClose time.Time
	lastApprovalID    string
	quitFocus         int
	diffSplit         bool
	diffFullscreen    bool
	readOnlyTurn      bool
	sessionMode       sessionMode
	renameInput       textinput.Model
	themePicker       list.Model
	themeItems        []list.Item
	themeBaseName     string
	editor            themeEditor
	sidebar           SidebarInfo
	planStore         *tools.PlanStore

	bridge   *ApprovalBridge
	inputCh  chan string
	outputCh chan tea.Msg
	cancelCh chan struct{}

	// Engine's authoritative model/provider; status bar reverts optimistic picker picks.
	agentSession *agent.Session
	registry     *provider.Registry
	// engine is the running agent loop. The queued-pills chip reads its
	// dispatcher's pending-turn count live; nil in model-only tests.
	engine *agent.AgentLoop

	// Maps tool call id to the live card so results replace pending cards, and
	// deltas never create duplicates.
	pendingTools map[string]*toolCard

	// steerCh carries input typed while a turn is streaming to the engine's
	// steering queue; queuedSteer drives the chip above the input area.
	steerCh     chan string
	queuedSteer int

	// lastTurn is the most recent finished turn's summary, shown in the status
	// bar; turnStarted is when the running turn began (zero when idle).
	lastTurn    agent.TurnMetrics
	turnStarted time.Time

	// motion is the [tui] animation preference; the live policy is resolved
	// per call against the terminal's reduced-motion signal.
	motion MotionSettings
	// transparent skips painting the app background, so the terminal's own
	// backdrop shows through ([tui] transparent).
	transparent bool
	// themeName is the active theme, kept on the model so a switch can report
	// its current selection without re-reading package state.
	themeName string
	// revealShown is how many bytes of streamBuf the transcript displays while
	// streamed text is paced; the rest is revealed one tick at a time.
	revealShown int

	// Rendered-block cache: settled transcript messages render once; only the
	// live assistant tail and mutating tool cards render again.
	cache renderCache

	// dirty marks transcript content the viewport has not seen yet; the shared
	// clock tick applies it, so a chunk burst cannot stack tick chains.
	dirty bool
	// viewportFlushes counts transcript applications; coalescing tests read it.
	viewportFlushes int

	cancelRequested bool
}

func promptTextareaStyles() textarea.Styles {
	t := activeTheme.Tokens
	state := textarea.StyleState{
		Text:        lipgloss.NewStyle().Foreground(t.FgBase),
		Placeholder: lipgloss.NewStyle().Foreground(t.FgSubtle),
		Selection:   lipgloss.NewStyle().Background(t.BgLessVisible),
	}
	blurred := state
	blurred.Text = lipgloss.NewStyle().Foreground(t.FgSubtle)
	return textarea.Styles{
		Focused: state, Blurred: blurred,
		Cursor: textarea.CursorStyle{Color: t.Primary, Shape: tea.CursorBlock, Blink: true},
	}
}

func NewModel(providerName, modelName, version, workspaceRoot string, bridge *ApprovalBridge, inputCh chan string, steerCh chan string, outputCh chan tea.Msg, cancelCh chan struct{}, pickerItems []list.Item, motion MotionSettings) Model {
	ta := textarea.New()
	ta.SetStyles(promptTextareaStyles())
	ta.Prompt = "" // GoCode renders its own prompt column.
	ta.Placeholder = "Type your message… (Enter to send, Shift+Enter for newline)"
	ta.Focus()
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	picker := NewModelPicker(pickerItems, 80, 24)
	specs := agent.Commands()
	paletteItems := commandPaletteItems(specs)
	palette := NewCommandPalette(specs, 80, 24)
	sessions := NewSessionPicker(nil, 80, 24)
	themes := newThemePickerList(nil, 80, 24)

	return Model{
		streamBuf:     &strings.Builder{},
		textarea:      ta,
		mascot:        newMascot(),
		providerName:  providerName,
		modelName:     modelName,
		version:       version,
		workspaceRoot: workspaceRoot,
		modelPicker:   picker,
		palette:       palette,
		paletteItems:  paletteItems,
		sessionPicker: sessions,
		themePicker:   themes,
		bridge:        bridge,
		inputCh:       inputCh,
		steerCh:       steerCh,
		outputCh:      outputCh,
		cancelCh:      cancelCh,
		pendingTools:  make(map[string]*toolCard),
		sessionGrants: make(map[string]bool),
		motion:        motion,
		themeName:     activeTheme.Name,
		focusedItem:   -1,
	}
}

// Init arms only the commands that must run at startup. There is deliberately
// no mascot tick: the idle pose is static, and animation chains arm when a turn
// starts, so an open session costs no CPU while nothing moves.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		pollApproval(m.bridge),
		m.listenOutput(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case agentTurnStartMsg:
		m.turnStarted = msgNow()
		m.readOnlyTurn = false
		cmds = append(cmds, m.listenOutput())

	case agentTurnMetricsMsg:
		if msg.metrics != nil {
			m.lastTurn = *msg.metrics
		}
		if msg.cancelled {
			// a queued turn was settled before it ran. Show the terminal
			// state once; the prompt itself is already in the transcript.
			m.addMessage(ChatMessage{Role: RoleSystem, Label: "⏹ Stopped", Content: "queued turn cancelled before it started"})
			m.reflow()
		}
		cmds = append(cmds, m.listenOutput())

	case tea.WindowSizeMsg:
		oldWidth, oldHeight := m.width, m.height
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		if oldWidth > 0 && oldHeight > 0 {
			m.updateLayoutEpoch(oldWidth, oldHeight, m.width, m.height)
		}

		// The outer frame reserves one cell on every edge; the sidebar and
		// chat panes are measured from the remaining rectangle.
		g := calculateLayout(m.width, m.height)
		// Entering compact hides the sidebar: force focus back to the editor
		// and clamp any sidebar scroll offset.
		if g.compact && m.focus == focusSidebar {
			m.setFocus(focusEditor)
		}
		m.sidebarOffset = min(m.sidebarOffset, m.sidebarMaxOffset())
		m.textarea.SetWidth(max(1, g.chatWidth-cellWidth(promptColumn(false))))
		vpH := m.viewportHeight()

		atBottom := m.ready && m.viewport.AtBottom()
		if !m.ready {
			m.viewport = viewport.New(viewport.WithWidth(max(1, g.chatWidth)), viewport.WithHeight(vpH))
			// The transcript owns wheel scrolling (see the tea.MouseMsg case),
			// so the widget must not scroll a second time for the same notch.
			m.viewport.MouseWheelEnabled = false
			m.viewport.SetContent(m.renderMessages())
			m.ready = true
			// The prompt owns the keyboard once the UI is ready.
			m.setFocus(focusEditor)
		} else {
			m.viewport.SetWidth(max(1, g.chatWidth))
			m.viewport.SetHeight(vpH)
			m.viewport.SetContent(m.renderMessages())
			if atBottom {
				m.viewport.GotoBottom()
			}
		}
		m.dirty = false
		m.modelPicker.SetWidth(max(1, m.width-10))
		m.modelPicker.SetHeight(max(1, m.height-6))
		m.resizePalette()
		m.resizeSessionPicker()
		m.resizeThemePicker()

	case tea.KeyPressMsg:
		// dispatch contract lives in handleKey (focus.go): ctrl+c before
		// dialogs (approval keeps deny+quit), dialogs first, esc-busy cancel,
		// then focus routing. It owns the key entirely.
		return m.handleKey(msg)

	case agentChunkMsg:
		m.mascot.setState(mascotWorking, msgNow())
		// Strip escapes at ingestion so the stream buffer never holds them.
		m.streamBuf.WriteString(safetext.Sanitize(msg.delta))
		// Adopt the new text into the display buffer: with pacing off (the
		// default) it renders on arrival; with pacing on the shared tick
		// advances the reveal. Either way one clock tick applies a single
		// viewport update, so a fast token stream never re-renders per token.
		if m.refreshLiveContent(msgNow(), false) {
			m.markDirty()
		}
		if tick := m.armClockTick(); tick != nil {
			cmds = append(cmds, tick)
		}
		cmds = append(cmds, m.listenOutput())

	case agentDoneMsg:
		m.streaming = false
		m.cancelRequested = false
		// Any steering the last round never drained is gone with the turn.
		if m.queuedSteer != 0 {
			m.queuedSteer = 0
			m.reflow()
		}
		// A turn end snaps the reveal: the last frame of the turn shows the
		// whole answer, not whatever the pacer had reached.
		m.finalizeStreamingMessage()
		// Cancelled turn is user action, not failure: relax to idle.
		switch {
		case msg.err != nil && !errors.Is(msg.err, context.Canceled):
			m.mascot.setState(mascotError, msgNow())
		case msg.err == nil:
			m.mascot.setState(mascotSuccess, msgNow())
		default:
			m.mascot.setState(mascotIdle, msgNow())
		}
		m.animating = false
		// Freeze the clock and supersede any tick still in flight, so a late
		// arrival from the finished turn cannot repaint or re-arm.
		m.clockArmed = false
		m.clockSeq++
		// Settle the live widgets once with pacing off: a card still pending
		// shows its whole preview and keeps its last frame — the frozen clock
		// never advances it again.
		if m.refreshLiveContent(msgNow(), false) {
			m.markDirty()
		}
		// Revert optimistic picker pick to engine truth.
		if m.agentSession != nil && m.registry != nil {
			m.providerName = m.registry.ActiveName()
			m.modelName = m.agentSession.Model()
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.addMessage(ChatMessage{Role: RoleSystem, Label: "⏹ Stopped", Content: "generation canceled — enter a new message to continue"})
			} else {
				m.addMessage(ChatMessage{Role: RoleError, Label: "Error", Content: safetext.Sanitize(msg.err.Error())})
				cmds = append(cmds, m.showToast(toastError, msg.err.Error()))
			}
		}
		// The last chunks may still be waiting on the coalescing tick; a turn
		// end is discrete, so its final text appears now.
		if m.dirty {
			m.flushNow()
		}
		cmds = append(cmds, m.listenOutput())

	case agentToolStartMsg:
		if m.streaming {
			m.mascot.setState(mascotWorking, msgNow())
		}
		// Reuse a card created by an argument delta; no duplicate card. The start
		// event arrives when the engine commits to the call, which is the edge
		// between "arguments still streaming" and "call in flight".
		m.upsertPendingToolCard(msg.name, msg.args, msg.toolCallID, toolCardRunning)
		// Tool start is discrete: show it now rather than on the next tick.
		m.flushNow()
		cmds = append(cmds, m.listenOutput())

	case agentToolDeltaMsg:
		if m.streaming {
			m.mascot.setState(mascotWorking, msgNow())
		}
		m.upsertPendingToolCard(msg.name, msg.args, msg.toolCallID, toolCardPending)
		// Argument previews stream like assistant text, so they ride the same
		// coalesced flush instead of redrawing per fragment.
		m.markDirty()
		if tick := m.armClockTick(); tick != nil {
			cmds = append(cmds, tick)
		}
		cmds = append(cmds, m.listenOutput())

	case agentToolMsg:
		if msg.steeringApplied {
			m.queuedSteer = 0
			m.reflow()
		}
		if m.streaming {
			m.mascot.setState(mascotWorking, msgNow())
		}
		toolName := safetext.Sanitize(msg.name)
		result := safetext.Sanitize(msg.result)
		diff := safetext.Sanitize(msg.diff)
		label := "🔧 " + toolName
		role := RoleTool
		done := false
		switch {
		case msg.isError:
			// RoleError renders "✗ name: result" in the danger style.
			role = RoleError
			label = toolName
		case msg.toolCallID != "":
			// A real tool result: the settled card leads with its outcome so the
			// transcript reads done even in flattened colour.
			label = toolDoneGlyph + " " + toolName
			done = true
		}
		if card, ok := m.pendingTools[msg.toolCallID]; ok && card.index < len(m.messages) {
			idx := card.index
			m.messages[idx].Role = role
			m.messages[idx].Label = label
			m.messages[idx].Content = result
			m.messages[idx].Diff = diff
			m.messages[idx].Done = done
			delete(m.pendingTools, msg.toolCallID)
			// A finished card is discrete: its result is visible immediately.
			m.flushNow()
		} else {
			m.addMessage(ChatMessage{Role: role, Label: label, Content: result, Diff: diff, Done: done,
				wrap: toolName == "GoCode" && msg.toolCallID == ""})
		}
		if msg.isError {
			// Errors toast alongside the transcript entry.
			cmds = append(cmds, m.showToast(toastError, toolName+": "+result))
		}
		cmds = append(cmds, m.listenOutput())

	case agentExitMsg:
		if msg.notice != "" {
			m.addMessage(ChatMessage{Role: RoleSystem, Label: "GoCode", Content: safetext.Sanitize(msg.notice)})
		}
		m.streaming = false
		return m, tea.Quit

	case sessionListMsg:
		cmds = append(cmds, m.applySessionList(msg))

	case pickerRefreshMsg:
		m.pickerRefreshInFlight = false
		cmds = append(cmds, m.applyPickerRefresh(msg.items))

	case approvalRequestMsg:
		if m.overlays.has(overlayApproval) {
			return m, pollApproval(m.bridge)
		}
		// Approval always wins focus: an open picker, palette or session list
		// is dropped, not stacked under the modal where its keys go nowhere.
		m.clearCompletion()
		m.overlays.push(overlayApproval)
		m.approvalReq = msg.req
		m.approvalReq.ToolName = safetext.Sanitize(msg.req.ToolName)
		m.approvalReq.Preview = safetext.Sanitize(msg.req.Preview)
		m.approvalReq.Args = json.RawMessage(safetext.Sanitize(string(msg.req.Args)))
		m.approvalReq.Danger = safetext.Sanitize(msg.req.Danger)
		m.approvalFocus = 0
		if m.approvalReq.Danger != "" {
			// Danger-biased default: a reflexive Enter refuses.
			m.approvalFocus = 1
		}
		now := msgNow()
		if id := approvalRequestID(m.approvalReq); id == m.lastApprovalID && now.Sub(m.lastApprovalClose) < approvalGraceReopenSkip {
			m.grace.skip()
		} else {
			m.grace.open(now)
		}
		return m, pollApproval(m.bridge)

	case animTickMsg:
		// The one shared clock tick. A tick from a previous turn
		// or one superseded by lost-tick recovery is stale: it neither
		// rewrites content nor re-arms.
		if !m.acceptClockTick(msg) {
			break
		}
		// One tick does every job: advance the opt-in reveal, refresh the
		// visible spinners and elapsed badges, and apply everything buffered
		// since the last flush in a single viewport update.
		if m.refreshLiveContent(msg.at, true) {
			m.markDirty()
		}
		if m.dirty {
			m.flushTranscript()
		}
		// The mascot rides the same clock; its springs keep their fps20 math
		// and slower states step on whole-frame multiples of their cadence.
		if m.animating && mascotStepDue(m.mascot.state, clockFrame(msg.at)) {
			m.mascot.step(msg.at)
		}
		cmds = append(cmds, m.armClockTick())

	case fileListMsg:
		// The @file listing landed; install it only if the popup is still open.
		if m.fileComp.open {
			m.fileComp.items = msg.files
			m.fileComp.loading = false
			m.refilterFiles()
		}

	case toastExpiryMsg:
		// Sequence-stamped: a stale expiry cannot clear a newer toast.
		m.expireToast(msg.seq)

	case list.FilterMatchesMsg:
		// Fuzzy filtering is asynchronous: the list hands back a command that
		// produces matches, and they only apply when the message is fed back.
		// Forward them to whichever filtered overlay is on top.
		if m.overlays.has(overlayApproval) {
			break
		}
		if kind, ok := m.overlays.top(); ok {
			var cmd tea.Cmd
			switch kind {
			case overlayPalette:
				m.palette, cmd = m.palette.Update(msg)
			case overlayPicker:
				m.modelPicker, cmd = m.modelPicker.Update(msg)
				// A filtered refresh parked the identity until the matches land;
				// restore the cursor now that the visible rows are known.
				if keep := m.pickerKeep; keep != nil {
					m.pickerKeep = nil
					m.selectModelItem(*keep)
				}
			case overlaySession:
				m.sessionPicker, cmd = m.sessionPicker.Update(msg)
			}
			cmds = append(cmds, cmd)
		}

	case tea.MouseWheelMsg:
		// Modal input must not change the transcript behind the dialog.
		if m.ready && m.overlays.empty() {
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				m.scrollTranscript(-mouseWheelLines)
			case tea.MouseWheelDown:
				m.scrollTranscript(mouseWheelLines)
			}
		}
		return m, nil
	}

	// Keys are fully owned by handleKey; pastes only reach the prompt when it
	// is the focused surface.
	_, isKey := msg.(tea.KeyPressMsg)
	_, isPaste := msg.(tea.PasteMsg)
	pasteOK := !isPaste || m.focus == focusEditor || m.focus == focusNone
	if !isKey && pasteOK && !m.overlays.has(overlayApproval) && forwardToTextarea(msg) {
		var taCmd tea.Cmd
		m.textarea, taCmd = m.textarea.Update(msg)
		cmds = append(cmds, taCmd)
	}

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) updateApproval(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	// Input grace: absorb reflex keystrokes until the quiet window passes.
	// Absorbed keys never reach the decision logic — grace cannot approve.
	if m.grace.absorb(msgNow()) {
		return m, nil
	}
	binding, ok := lookupBinding(keyCtxApproval, msg.String())
	if !ok {
		return m, nil
	}
	switch binding.Action {
	case actApprovalToggle:
		if m.approvalFocus == 0 {
			m.approvalFocus = 1
		} else {
			m.approvalFocus = 0
		}
	case actApprovalConfirm:
		if m.approvalFocus == 0 {
			return m.resolveApproval(tools.ApprovalOnce, "✓ Approved", RoleSystem)
		}
		return m.resolveApproval(tools.ApprovalDeny, "✗ Denied", RoleError)
	case actApprovalAllow:
		return m.resolveApproval(tools.ApprovalOnce, "✓ Approved", RoleSystem)
	case actApprovalReject:
		return m.resolveApproval(tools.ApprovalDeny, "✗ Denied", RoleError)
	case actApprovalGrant:
		if m.sessionGrants == nil {
			m.sessionGrants = make(map[string]bool)
		}
		// "/commit" is a synthetic slash-command prompt, not a gate tool; the
		// gate keeps no grant for it, so the footer must not claim one.
		if m.approvalReq.ToolName != "/commit" {
			m.sessionGrants[m.approvalReq.ToolName] = true
		}
		return m.resolveApproval(tools.ApprovalGrantExact, "✓ Approved (exact args this session)", RoleSystem)
	case actApprovalReadOnly:
		return m.resolveApproval(tools.ApprovalReadOnlyTurn, "✓ Read-only tools allowed this turn", RoleSystem)
	case actApprovalDiffToggle:
		m.diffSplit = !m.diffSplit
	case actApprovalDiffFullscreen:
		m.diffFullscreen = !m.diffFullscreen
	case actApprovalDenyQuit:
		// Quit without approving; the requester sees a denial.
		req := m.approvalReq
		m.overlays.pop(overlayApproval)
		m.sendApprovalReply(req, tools.ApprovalDeny)
		return m, tea.Quit
	}
	return m, nil
}

// openModelPicker opens the model picker over whatever is open below it and
// starts a background refresh of the startup snapshot.
func (m *Model) openModelPicker() tea.Cmd {
	m.overlays.push(overlayPicker)
	m.modelPicker.SetWidth(max(1, m.width-10))
	m.modelPicker.SetHeight(max(1, m.height-6))
	// A parked selection belongs to the previous open; this one starts fresh.
	m.pickerKeep = nil
	return m.refreshModelPicker()
}

// updatePicker routes keys to the model picker; everything it does not claim
// stays with the bubbles list (filter input, paging, vim cursor keys).
func (m *Model) updatePicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+l", "esc":
		m.overlays.pop(overlayPicker)
		m.pickerKeep = nil
		return *m, nil
	case "enter":
		if !m.modelPicker.SettingFilter() {
			if sel := m.modelPicker.SelectedItem(); sel != nil {
				if item, ok := sel.(ModelItem); ok {
					m.providerName = item.Provider
					m.modelName = item.Model
					m.overlays.pop(overlayPicker)
					inputCh := m.inputCh
					go func() {
						inputCh <- "/provider " + item.Provider
						inputCh <- "/model " + item.Model
					}()
					return *m, nil
				}
			}
		}
	}
	var cmd tea.Cmd
	m.modelPicker, cmd = m.modelPicker.Update(msg)
	return *m, cmd
}

// submitPrompt sends the prompt as a new turn. History records the prompt
// before any command alias quits, so recall behaves like submission.
func (m *Model) submitPrompt() tea.Cmd {
	input := strings.TrimSpace(m.textarea.Value())
	if input == "" {
		return nil
	}
	m.promptHistory.add(input)
	m.promptHistory.resetNavigation()
	m.savePromptHistory()

	switch strings.ToLower(input) {
	case "exit", "quit", "/exit", "/quit":
		return tea.Quit
	}

	// A bare /sessions is a TUI affordance: it opens the picker instead of
	// printing the text list. An argument is a resume request, and the engine
	// handler still owns it end to end. The plain REPL and headless paths do
	// not run this code, so they keep the text listing.
	if strings.EqualFold(input, "/sessions") {
		m.textarea.Reset()
		return m.openSessionPicker()
	}

	// Sending a new prompt explicitly returns to the live conversation.
	m.followBottom()
	m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: input})
	m.textarea.Reset()
	m.streaming = true
	m.streamBuf.Reset()
	m.revealShown = 0
	// No tokens yet, so show thinking rather than streaming.
	m.mascot.setState(mascotThinking, msgNow())
	// Advance the epoch every turn so stale ticks can't match later, and
	// reset the clock latch so this turn's single chain can arm.
	m.animEpoch++
	m.clockArmed = false
	m.animating = m.decorativeMotion()
	inputCh := m.inputCh
	go func() { inputCh <- input }()
	return m.armClockTick()
}

// steerPrompt queues mid-turn input as steering: it is applied at the next
// provider round boundary, never injected into this stream.
func (m *Model) steerPrompt() {
	input := strings.TrimSpace(m.textarea.Value())
	if input == "" {
		return
	}
	m.promptHistory.add(input)
	m.promptHistory.resetNavigation()
	m.savePromptHistory()
	m.textarea.Reset()
	m.queuedSteer++
	m.reflow()
	if steerCh := m.steerCh; steerCh != nil {
		go func() { steerCh <- input }()
	}
}

// recallHistory moves through the in-memory prompt history. older=true walks
// back (Alt+P / empty Up), older=false forward (Alt+N).
func (m *Model) recallHistory(older bool) {
	var (
		text string
		ok   bool
	)
	if older {
		text, ok = m.promptHistory.prev(m.textarea.Value())
	} else {
		text, ok = m.promptHistory.next()
	}
	if !ok {
		return
	}
	m.setText(text)
}

// resolveApproval closes the modal, records the outcome in the transcript, and
// answers the waiting engine. The modal does not edit arguments; see
// ApprovalBridge.RequestApprovalChoice for that limitation.
func (m Model) resolveApproval(choice tools.ApprovalChoice, label string, role ChatRole) (Model, tea.Cmd) {
	req := m.approvalReq
	m.overlays.pop(overlayApproval)
	m.lastApprovalClose = msgNow()
	m.lastApprovalID = approvalRequestID(req)
	m.grace.skip()
	if choice == tools.ApprovalReadOnlyTurn {
		m.readOnlyTurn = true
	}
	m.addMessage(ChatMessage{Role: role, Label: label, Content: req.ToolName})
	m.sendApprovalReply(req, choice)
	var cmd tea.Cmd
	if choice == tools.ApprovalGrantExact {
		// A session grant is confirmed with a toast.
		cmd = m.showToast(toastSuccess, "Session grant: "+req.ToolName)
	}
	return m, cmd
}

// sendApprovalReply answers whichever channel the requester used. The legacy
// bool channel only carries approval; grants and the read-only flag are
// conveyed through the choice channel the gate installs.
func (m Model) sendApprovalReply(req ApprovalRequest, choice tools.ApprovalChoice) {
	if req.ChoiceCh != nil {
		ch := req.ChoiceCh
		go func() { ch <- choice }()
	}
	if req.ReplyCh != nil {
		ch := req.ReplyCh
		approved := choice.Approved()
		go func() { ch <- approved }()
	}
}

// grantedToolNames lists tools the user granted exact-args approval to, in a
// stable order for the modal footer.
func (m *Model) grantedToolNames() []string {
	names := make([]string, 0, len(m.sessionGrants))
	for name := range m.sessionGrants {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Model) interruptTurn() {
	select {
	case m.cancelCh <- struct{}{}:
	default:
	}
}

// View renders the whole UI.
//
// v2 view is declarative: AltScreen/MouseMode must be set on every return.
// Routing through newView guarantees that.
func (m Model) View() tea.View {
	return m.newView(m.render())
}

func (m Model) newView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// The app paints its own background so unfilled cells match the theme.
	// Transparent mode, NO_COLOR and TERM=dumb leave the terminal's own
	// background alone; a nil colour is the "do not paint" signal.
	v.BackgroundColor = m.backgroundColor()
	return v
}

// backgroundColor resolves the painted app background for this frame.
func (m Model) backgroundColor() color.Color {
	return backgroundFor(m.transparent, capabilities.NoColor, capabilities.Dumb)
}

// backgroundFor is the pure decision behind View: no background when the user
// asked for transparency or the terminal cannot render colour, otherwise the
// active theme's bg_base.
func backgroundFor(transparent, noColor, dumb bool) color.Color {
	if transparent || noColor || dumb {
		return nil
	}
	return activeTheme.Tokens.BgBase
}

// ThemeName reports the active theme's name, e.g. for the status bar or the
// theme picker's current selection.
func (m Model) ThemeName() string { return m.themeName }

// ApplyTheme switches the live theme and invalidates every cached transcript
// render: cached strings carry the old colours, so they must not be reused.
// The viewport keeps its cached item heights — only the rendered blocks are
// dropped, and the transcript is re-applied at the same width.
func (m *Model) ApplyTheme(name string) error {
	th, err := ActivateTheme(name)
	if err != nil {
		return err
	}
	m.refreshThemeView(th)
	return nil
}

func (m *Model) refreshThemeView(th Theme) {
	m.themeName = th.Name
	m.textarea.SetStyles(promptTextareaStyles())
	m.cache.invalidate()
	if m.ready {
		m.flushNow()
	}
}

func (m Model) render() string {
	if !m.ready {
		return "\n  Initializing GoCode…\n"
	}
	g := calculateLayout(m.width, m.height)
	chat := m.viewport.View()
	if chip := m.renderSteerChip(); chip != "" {
		chat = lipgloss.JoinVertical(lipgloss.Left, chat, chip)
	}
	chat = lipgloss.JoinVertical(lipgloss.Left, chat, m.renderInputAreaWithPopup())
	if !g.compact {
		chatW := max(1, g.chatWidth)
		sidebar := m.renderSidebar(g.sidebarWidth-1, max(1, m.height-7))
		chat = lipgloss.NewStyle().Width(chatW).Render(chat)
		chat = lipgloss.JoinHorizontal(lipgloss.Top, chat, "  ", sidebar)
	} else {
		chat = lipgloss.JoinVertical(lipgloss.Left, m.renderCompactHeader(g.chatWidth), "", chat)
	}
	status := truncateToWidth(m.renderStatusBar(), max(1, m.width-2))
	if m.toastActive(msgNow()) {
		// A transient toast replaces the whole status/help row.
		status = m.renderToast(max(1, m.width-2))
	}
	base := lipgloss.JoinVertical(lipgloss.Left, "", chat, status, "")
	base = lipgloss.NewStyle().Padding(0, 1).MaxWidth(m.width).MaxHeight(m.height).Render(base)
	if m.overlays.has(overlayApproval) {
		return placeModal(base, m.renderApprovalModal(), m.width, m.height)
	}
	if kind, ok := m.overlays.top(); ok {
		switch kind {
		case overlayPalette:
			return placeModal(base, modalOverlayStyle.Render(m.palette.View()), m.width, m.height)
		case overlayPicker:
			return placeModal(base, modalOverlayStyle.Render(m.modelPicker.View()), m.width, m.height)
		case overlaySession:
			return placeModal(base, modalOverlayStyle.Render(m.sessionPicker.View()), m.width, m.height)
		case overlayQuit:
			return placeModal(base, m.renderQuitDialog(), m.width, m.height)
		case overlayTheme:
			return placeModal(base, m.renderThemePicker(), m.width, m.height)
		case overlayThemeEditor:
			return placeModal(base, m.renderThemeEditor(), m.width, m.height)
		}
	}
	return base
}

// Below this the bar drops model id rather than truncating to a stub.
const minModelWidth = 12

func (m *Model) renderStatusBar() string {
	contentWidth := max(1, m.width-2)
	// One clock sample for every animated part of the bar, so the mascot, the
	// spinner and the shimmer band can never disagree about the frame.
	now := msgNow()

	// Mascot has no textual fallback, so it's dropped last; model id truncates first.
	head := m.mascot.inline(now, mascotFaceStyles) +
		statusSeparator + statusProviderStyle.Render(m.providerName)
	if m.readOnlyTurn {
		head = badge("READONLY", activeTheme.Tokens.Info) + " " + head
	}

	var suffix string
	if m.streaming {
		suffix = statusSeparator + m.renderWorkingState(now)
	}

	// Metrics appear only while a turn runs and only when they fit: the
	// existing squeeze order (workspace path, then model id) is untouched.
	metrics := ""
	if m.streaming {
		metrics = m.renderTurnMetrics()
	}

	hint := m.renderHelpLine()
	// Keep the first shortcuts (or completion) readable beside long model IDs.
	hintRoom := min(32, cellWidth(hint)+2)
	fixed := cellWidth(head) + cellWidth(suffix) + cellWidth(statusSeparator)
	if contentWidth-fixed-minModelWidth < hintRoom {
		hintRoom = 0 // on narrow terminals, provider/working state win
	}

	// When tight, drop workspace path first, model id second.
	var right string
	if path := workspaceShortName(m.workspaceRoot); path != "" {
		styledPath := statusPathStyle.Render(path)
		if fixed+cellWidth(styledPath)+minModelWidth+hintRoom <= contentWidth {
			right = styledPath
		}
	}

	left := head
	room := contentWidth - fixed - cellWidth(right) - hintRoom
	if room >= minModelWidth {
		left += statusSeparator + statusModelStyle.Render(truncateToWidth(m.modelName, room))
	}
	if metrics != "" {
		// The suffix is appended after this, so its width is part of the price:
		// otherwise the cluster can push the bar past contentWidth.
		needed := lipgloss.Width(metrics) + lipgloss.Width(statusSeparator) + lipgloss.Width(suffix)
		if contentWidth-cellWidth(left)-cellWidth(right)-hintRoom >= needed {
			left += statusSeparator + statusMetricsStyle.Render(metrics)
		}
	}
	left += suffix

	gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap >= 8 {
		hint := truncateToWidth(hint, gap-2)
		left += statusBarStyle.UnsetPadding().Render(" ") + hint
		gap -= 1 + lipgloss.Width(hint)
	}
	if gap > 0 {
		left += statusBarStyle.UnsetPadding().Render(strings.Repeat(" ", gap))
	}
	return left + right
}

// renderWorkingState is the status bar's working row: the shared scramble
// glyph row followed by the state word. Motion on, the glyphs carry the
// theme gradient and the word carries the shimmer band; motion off, the same
// cells render in the plain streaming colour. Either way the glyphs and the
// word are present — a functional indicator degrades, it never disappears.
func (m *Model) renderWorkingState(now time.Time) string {
	glyphs := m.barSpinnerGlyphs(now)
	label := m.mascot.state.String()
	if !m.decorativeMotion() {
		return statusStreamingStyle.Render(glyphs + " " + label)
	}
	// Both branches carry the same style wrap so toggling motion never
	// reflows the bar: the padding lives in statusStreamingStyle.
	return statusStreamingStyle.Render(glyphs + renderShimmer(" "+label, now))
}

// renderTurnMetrics builds the compact "elapsed · tokens · ctx%" cluster. A
// missing measurement is omitted rather than rendered as zero.
func (m *Model) renderTurnMetrics() string {
	var parts []string
	if !m.turnStarted.IsZero() {
		parts = append(parts, formatTurnElapsed(time.Since(m.turnStarted)))
	}
	if m.lastTurn.PromptTokens > 0 || m.lastTurn.CompletionTokens > 0 {
		parts = append(parts, fmt.Sprintf("↑%s ↓%s",
			formatTokenCount(m.lastTurn.PromptTokens),
			formatTokenCount(m.lastTurn.CompletionTokens)))
	}
	if pct, ok := m.lastTurn.ContextPercent(); ok {
		parts = append(parts, fmt.Sprintf("ctx %d%%", int(pct+0.5)))
	}
	return strings.Join(parts, " ")
}

// formatTurnElapsed renders whole seconds, switching to m:ss past a minute.
func formatTurnElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d / time.Second)
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}

// formatTokenCount keeps the cluster narrow: 999, 1.2k, 3.4M.
func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (m *Model) renderInputArea() string {
	badge := m.streaming && (m.queuedSteer > 0 || m.queuedTurnCount() > 0)
	prompt := promptColumn(badge)
	lines := strings.Split(strings.TrimRight(m.textarea.View(), "\n"), "\n")
	for i, line := range lines {
		if i == 0 && badge {
			lines[i] = " !  " + line
		} else {
			lines[i] = prompt + line
		}
	}
	return lipgloss.NewStyle().Foreground(colorText).Width(max(1, m.chatContentWidth())).Render(strings.Join(lines, "\n"))
}

// renderInputAreaWithPopup prepends the @file completion popup above the prompt
// so it grows upward; viewportHeight measures this whole block, so the transcript
// yields the popup's rows automatically.
func (m *Model) renderInputAreaWithPopup() string {
	area := m.renderInputArea()
	if m.fileComp.open {
		if popup := m.renderFileCompletion(); popup != "" {
			area = lipgloss.JoinVertical(lipgloss.Left, popup, area)
		}
	}
	return area
}

// queuedTurnCount reads the dispatcher's pending-turn count. Nil-safe
// so model-only tests and the idle constructor pay nothing.
func (m *Model) queuedTurnCount() int {
	if m.engine == nil {
		return 0
	}
	return m.engine.QueuedTurnCount()
}

// renderSteerChip shows how many mid-turn messages are queued for the next
// round, plus how many accepted prompts are queued as their own turns.
// Empty when nothing is queued, so it costs no row while idle. One message
// reads as a sentence; a batch is a count, because the number is the only
// part that changes.
func (m *Model) renderSteerChip() string {
	turns := m.queuedTurnCount()
	if m.queuedSteer <= 0 && turns <= 0 {
		return ""
	}
	label := ""
	if m.queuedSteer == 1 {
		label = "1 steered message queued"
	} else if m.queuedSteer > 1 {
		label = fmt.Sprintf("%d queued", m.queuedSteer)
	}
	suffix := " · applied at the next round"
	if turns > 0 {
		turnLabel := "1 turn queued"
		if turns > 1 {
			turnLabel = fmt.Sprintf("%d turns queued", turns)
		}
		if label == "" {
			label = turnLabel
			suffix = " · runs after this turn"
		} else {
			label += " · " + turnLabel
		}
	}
	return steerChipStyle.Render("↳ " + label + suffix)
}

// viewportHeight is the transcript height left after the measured chrome,
// including the steering chip when it is visible.
func (m *Model) viewportHeight() int {
	g := calculateLayout(m.width, m.height)
	footerH := lipgloss.Height(m.renderInputAreaWithPopup()) + 1 // merged status/help row
	if chip := m.renderSteerChip(); chip != "" {
		footerH += lipgloss.Height(chip)
	}
	if g.compact {
		footerH += 2
	} // one-row header and one blank separator
	vpH := m.height - 2 /* outer frame */ - footerH
	if vpH < 1 {
		return 1
	}
	return vpH
}

// reflow resizes the viewport after chrome appears or disappears mid-turn,
// without waiting for the next WindowSizeMsg.
func (m *Model) reflow() {
	if !m.ready {
		return
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetHeight(m.viewportHeight())
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// Shows only keys that work now; mid-turn that's just interrupt. The hint
// text is rendered from the keymap table, so it cannot drift from dispatch.
func (m *Model) renderHelpLine() string {
	if m.completionHint != "" {
		return inputHintStyle.Render(m.completionHint)
	}

	var text string
	switch {
	case m.streaming:
		text = helpFor(actCancelOrQuit, actScrollPageUp)
		if m.cancelRequested {
			text = "Ctrl+C again to quit"
		}
	case m.focus == focusMain:
		text = helpFor(actChatLineDown, actChatItemNext, actChatExpand, actChatCopy, actFocusEditor, actQuit)
	case m.focus == focusSidebar:
		text = helpFor(actSidebarDown, actFocusMain, actFocusEditor, actQuit)
	case m.width < 48:
		text = helpFor(actSubmit, actClearInput, actQuit)
	case m.width < 72:
		text = helpFor(actScrollPageUp, actComplete, actSubmit, actNewline, actClearInput, actQuit)
	default:
		text = helpFor(actScrollPageUp, actComplete, actSubmit, actNewline, actPaletteOpen, actOpenPicker, actClearInput, actQuit)
	}
	return inputHintStyle.Render(text)
}

func workspaceShortName(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(root))
}

func truncateToWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	return ansi.Truncate(text, width, "…")
}

func (m *Model) renderMessage(msg ChatMessage) string {
	switch msg.Role {
	case RoleUser:
		return userLabelStyle.Render(msg.Label) + "\n" + msg.Content
	case RoleAssistant:
		return asstLabelStyle.Render(msg.Label) + "\n" + m.renderMarkdown(msg.Content)
	case RoleTool:
		labelStyle := toolLabelStyle
		if msg.Done {
			labelStyle = toolDoneLabelStyle
		}
		body := labelStyle.Render(msg.Label)
		if msg.Content != "" {
			content := msg.Content
			if msg.wrap {
				// Leave room for the indented body style before measuring rows.
				content = ansi.Wrap(content, max(1, m.chatLineWidth()-toolBodyStyle.GetHorizontalFrameSize()), "")
			}
			body += "\n" + toolCardLines(content)
		}
		if msg.Diff != "" {
			body += "\n" + renderDiffView(msg.Diff, max(2, m.chatContentWidth()-2))
		}
		return body
	case RoleError:
		line := toolErrorGlyph + " " + msg.Label + ": " + msg.Content
		if msg.wrap {
			line = ansi.Wrap(line, m.chatLineWidth(), "")
		}
		return errorStyle.Render(line)
	case RoleSystem:
		return systemStyle.Render(msg.Label + " " + msg.Content)
	}
	return ""
}

func (m *Model) renderApprovalModal() string {
	req := m.approvalReq

	// Sizing: min(70, area-border); fullscreen (f) takes the screen.
	modalW := max(30, min(70, m.width-2))
	if m.diffFullscreen {
		modalW = max(30, m.width-2)
	}
	innerW := modalW - 4

	var b strings.Builder
	b.WriteString("Tool: " + modalToolNameStyle.Render(safetext.Sanitize(req.ToolName)))
	if req.Danger != "" {
		warn := lipgloss.NewStyle().Foreground(activeTheme.Tokens.Destructive)
		b.WriteString("\n" + warn.Render("⚠ Potentially dangerous: "+safetext.Sanitize(req.Danger)))
	}

	var prettyArgs map[string]interface{}
	if err := json.Unmarshal(req.Args, &prettyArgs); err == nil {
		for k, v := range prettyArgs {
			// Values are decoded after ingestion, so sanitize again for display.
			val := safetext.Sanitize(fmt.Sprintf("%v", v))
			if len(val) > 120 {
				val = val[:117] + "..."
			}
			b.WriteString("\n" + modalArgKeyStyle.Render(safetext.Sanitize(k)+": ") + modalArgValStyle.Render(val))
		}
	} else {
		b.WriteString("\n" + safetext.Sanitize(string(req.Args)))
	}

	if displayPreview := safetext.Sanitize(req.Preview); displayPreview != "" {
		b.WriteString("\n\nPreview:")
		if looksLikeDiff(displayPreview) {
			if m.diffSplit {
				b.WriteString("\n" + renderDiffSplit(displayPreview, innerW))
			} else {
				b.WriteString("\n" + renderDiffView(displayPreview, innerW))
			}
		} else {
			b.WriteString("\n" + toolBubbleStyle.Render(displayPreview))
		}
	}

	// On a flagged request approving is the destructive act: the approve
	// button takes the negative (error) fill when focused.
	negative := req.Danger != ""
	approveBtn := dialogButton("✓ Approve", m.approvalFocus == 0, negative)
	denyBtn := dialogButton("✗ Deny", m.approvalFocus == 1, false)
	b.WriteString("\n\n" + approveBtn + "  " + denyBtn)
	b.WriteString("\n" + dialogHelp(
		[2]string{"←→/tab", "switch"}, [2]string{"enter", "confirm"}, [2]string{"esc", "deny"},
		[2]string{"a", "grant session"}, [2]string{"r", "read-only turn"},
	))
	if looksLikeDiff(safetext.Sanitize(req.Preview)) {
		b.WriteString("\n" + dialogHelp([2]string{"t", "split/unified"}, [2]string{"f", "fullscreen"}))
	}
	if names := m.grantedToolNames(); len(names) > 0 {
		b.WriteString("\n" + systemStyle.Render("granted: "+strings.Join(names, ", ")))
	}

	return dialogFrame("Tool Approval Required", dialogNormal, b.String(), modalW)
}

func placeModal(base, modal string, totalW, totalH int) string {
	// Clamp first; Place bails when oversized, then overlay so transcript stays visible.
	clamped := lipgloss.NewStyle().
		MaxWidth(max(1, totalW)).
		MaxHeight(max(1, totalH)).
		Render(modal)

	canvas := lipgloss.Place(totalW, totalH, lipgloss.Left, lipgloss.Top, base,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(colorBg)),
	)

	mW := lipgloss.Width(clamped)
	mH := lipgloss.Height(clamped)
	colOff := max(0, (totalW-mW)/2)
	rowOff := max(0, (totalH-mH)/2)

	return lipgloss.NewCompositor(
		lipgloss.NewLayer(canvas).Z(0),
		lipgloss.NewLayer(clamped).X(colOff).Y(rowOff).Z(1),
	).Render()
}

func (m *Model) addMessage(msg ChatMessage) {
	if msg.Role != RoleAssistant {
		m.finalizeStreamingMessage()
	}
	m.messages = append(m.messages, msg)
	if len(m.messages) == 1 || m.viewport.AtBottom() {
		m.focusedItem = len(m.messages) - 1
	}
	// A message append is discrete: it appears in this frame, not the next.
	m.flushNow()
}

// toolCard is the live state of one tool call: where its card sits in the
// transcript, when the call started (the elapsed badge), how much of its
// argument preview has been revealed, and which lifecycle phase it is in.
type toolCard struct {
	index  int
	since  time.Time
	args   string
	shown  int
	status toolCardStatus
}

// toolCardStatus is the card's lifecycle phase. Two phases are live; done and
// error are terminal and replace the card with a settled message.
type toolCardStatus int

const (
	// toolCardPending: argument deltas are still arriving, nothing is executing.
	toolCardPending toolCardStatus = iota
	// toolCardRunning: the engine has announced the call (EventToolStart) and
	// owns it from here — approval may still be pending above the card, then
	// the tool runs. The engine emits no separate "approved" event, and the
	// approval gate is the only place that could know; announcing is the last
	// edge the UI can observe without reaching into the engine.
	toolCardRunning
)

// stateWord is the word rendered after the spinner. Pending says what the card
// is doing (waiting for arguments), running says it is executing. The ellipsis
// is the spinner primitive's job now — it cycles, and yields to the elapsed
// badge — so the word itself carries none.
func (s toolCardStatus) stateWord() string {
	if s == toolCardRunning {
		return "running"
	}
	return "pending"
}

// Terminal card markers. A finished card is a settled transcript entry, so the
// glyph carries the outcome in text: it survives flattened colour and it is
// what the transcript-searchable history shows.
const (
	toolDoneGlyph  = "✓"
	toolErrorGlyph = "✗"
)

// toolPreviewRunes is the argument preview width the card has always shown.
const toolPreviewRunes = 200

// truncateToolPreview caps a preview, rune-wise so a multi-byte argument can
// never be cut mid-character. Only the surviving window is decoded: a tool
// that streams a megabyte of arguments must not cost a megabyte-sized rune
// conversion on every reveal frame.
func truncateToolPreview(preview string) string {
	preview = strings.TrimSpace(preview)
	if len(preview) <= toolPreviewRunes {
		return preview
	}
	window := preview
	if budget := toolPreviewRunes * utf8.UTFMax; len(window) > budget {
		window = window[:budget]
	}
	runes := []rune(window)
	if len(window) == len(preview) && len(runes) <= toolPreviewRunes {
		return preview
	}
	return string(runes[:toolPreviewRunes-3]) + "..."
}

// toolCardContent renders a live card's body: the composed spinner head
// (scramble glyphs, lifecycle word, ellipsis or elapsed badge) and the
// revealed argument preview. Every refresh goes through this one composer, so
// a card's head and text always belong to the same frame.
func toolCardContent(head, preview string) string {
	if preview = truncateToolPreview(preview); preview != "" {
		return head + "\n" + toolFaintStyle.Render(preview)
	}
	return head
}

// toolCardLines keeps the card head (glyph, state word, elapsed) on the base
// surface and tints only the body, per the card anatomy.
func toolCardLines(content string) string {
	head, rest, found := strings.Cut(content, "\n")
	if !found || rest == "" {
		return head
	}
	return head + "\n" + toolBodyStyle.Render(rest)
}

// upsertPendingToolCard creates or updates the card for one tool call. Deltas,
// the start event and the result all share the tool call id, so a call owns
// exactly one card from its first fragment until its result replaces it.
func (m *Model) upsertPendingToolCard(name, args, toolCallID string, status toolCardStatus) {
	args = safetext.Sanitize(args)
	now := msgNow()
	if toolCallID == "" {
		m.finalizeStreamingMessage()
		// Without an id the card cannot be reused or refreshed, so it shows the
		// whole preview at once instead of a reveal that would never advance.
		m.messages = append(m.messages, ChatMessage{
			Role:    RoleTool,
			Label:   "🔧 " + safetext.Sanitize(name),
			Content: toolCardContent(m.toolSpinnerHead(status, now, now, 0), args),
		})
		return
	}
	if card, ok := m.pendingTools[toolCallID]; ok && card.index < len(m.messages) {
		card.args = args
		card.status = status
		if card.shown > len(args) {
			card.shown = len(args)
		}
		prefix, shown := m.revealPrefix(card.args, card.shown, false)
		card.shown = shown
		head := m.toolSpinnerHead(card.status, card.since, now, cardElapsed(card.since, now))
		m.messages[card.index].Content = toolCardContent(head, prefix)
		if name != "" {
			m.messages[card.index].Label = "🔧 " + safetext.Sanitize(name)
		}
		m.pendingTools[toolCallID] = card
		return
	}
	m.finalizeStreamingMessage()
	prefix, shown := m.revealPrefix(args, 0, false)
	m.messages = append(m.messages, ChatMessage{
		Role:    RoleTool,
		Label:   "🔧 " + safetext.Sanitize(name),
		Content: toolCardContent(m.toolSpinnerHead(status, now, now, 0), prefix),
	})
	m.pendingTools[toolCallID] = &toolCard{
		index:  len(m.messages) - 1,
		since:  now,
		args:   args,
		shown:  shown,
		status: status,
	}
}

// updateStreamingTail writes the revealed prefix into the live assistant
// block, appending the block when the turn's first tokens arrive. It reports
// whether the transcript changed, so callers mark the viewport dirty once per
// frame instead of once per widget.
func (m *Model) updateStreamingTail(content string) bool {
	if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
		if m.messages[len(m.messages)-1].Content == content {
			return false
		}
		m.messages[len(m.messages)-1].Content = content
		return true
	}
	if content == "" {
		// Nothing revealed yet: do not open an empty bubble.
		return false
	}
	m.messages = append(m.messages, ChatMessage{
		Role:    RoleAssistant,
		Label:   "GoCode",
		Content: content,
	})
	if len(m.messages) == 1 || m.viewport.AtBottom() {
		m.focusedItem = len(m.messages) - 1
	}
	return true
}

func (m *Model) finalizeStreamingMessage() {
	if m.streamBuf.Len() == 0 {
		return
	}
	// Snap any unrevealed text into place before appending a tool or notice.
	// A paced stream may not have created its assistant item yet.
	m.updateStreamingTail(m.streamBuf.String())
	m.streamBuf.Reset()
	m.revealShown = 0
	// Do not collapse a long answer while the user is reading inside it.
	i := len(m.messages) - 1
	if !m.viewport.AtBottom() && i < len(m.spans) && m.spans[i].lines > collapseLines && m.itemVisible(i) {
		if m.expanded == nil {
			m.expanded = make(map[int]bool)
		}
		m.expanded[i] = true
	}
	m.markDirty()
}

// forwardToTextarea reports whether a message can affect the input box. Agent
// traffic (streamed tokens, tool deltas, animation frames) cannot, and bubbles'
// textarea re-renders its value on every message it sees: forwarding a token
// stream costs tens of microseconds and hundreds of allocations per chunk for
// no visible change.
func forwardToTextarea(msg tea.Msg) bool {
	switch msg.(type) {
	case agentChunkMsg, agentToolDeltaMsg, agentToolStartMsg, agentToolMsg,
		agentDoneMsg, agentTurnStartMsg, agentTurnMetricsMsg, agentExitMsg,
		approvalRequestMsg, animTickMsg,
		sessionListMsg, pickerRefreshMsg, fileListMsg, toastExpiryMsg:
		return false
	}
	return true
}

func (m Model) listenOutput() tea.Cmd {
	// Init starts the only reader; only consumption of an engine event re-arms it.
	// Capture the channel, not the model: a closure over the receiver copies
	// the whole ~96 KB Model, and this runs once per streamed chunk.
	ch := m.outputCh
	return func() tea.Msg {
		return <-ch
	}
}

func pollApproval(bridge *ApprovalBridge) tea.Cmd {
	return func() tea.Msg {
		return approvalRequestMsg{req: <-bridge.RequestCh}
	}
}
