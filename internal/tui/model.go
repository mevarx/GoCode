package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
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
}

type agentChunkMsg struct{ delta string }
type agentDoneMsg struct{ err error }
type agentToolMsg struct {
	name       string
	result     string
	isError    bool
	toolCallID string
	diff       string
}

// Opens a pending card so the UI shows the call while awaiting approval.
type agentToolStartMsg struct {
	name       string
	args       string
	toolCallID string
}

// Notice renders before the program stops.
type agentExitMsg struct{ notice string }
type approvalRequestMsg struct{ req ApprovalRequest }

type Model struct {
	width  int
	height int
	ready  bool

	viewport  viewport.Model
	messages  []ChatMessage
	streamBuf *strings.Builder

	textarea textarea.Model
	focused  bool

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

	modelPickerActive bool
	modelPicker       list.Model

	approvalActive bool
	approvalReq    ApprovalRequest
	approvalFocus  int

	bridge   *ApprovalBridge
	inputCh  chan string
	outputCh chan tea.Msg
	cancelCh chan struct{}

	// Engine's authoritative model/provider; status bar reverts optimistic picker picks.
	agentSession *agent.Session
	registry     *provider.Registry

	// Maps tool call ID to transcript index so results replace pending cards.
	pendingTools map[string]int

	cancelRequested bool
}

func NewModel(providerName, modelName, version, workspaceRoot string, bridge *ApprovalBridge, inputCh chan string, outputCh chan tea.Msg, cancelCh chan struct{}, pickerItems []list.Item) Model {
	ta := textarea.New()
	ta.Placeholder = "Type your message… (Enter to send, Shift+Enter for newline)"
	ta.Focus()
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	picker := NewModelPicker(pickerItems, 80, 24)

	return Model{
		streamBuf:     &strings.Builder{},
		textarea:      ta,
		mascot:        newMascot(),
		providerName:  providerName,
		modelName:     modelName,
		version:       version,
		workspaceRoot: workspaceRoot,
		modelPicker:   picker,
		bridge:        bridge,
		inputCh:       inputCh,
		outputCh:      outputCh,
		cancelCh:      cancelCh,
		pendingTools:  make(map[string]int),
		focused:       true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tickUntil(cadence(mascotIdle), m.animEpoch),
		pollApproval(m.bridge),
		m.listenOutput(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)

		// Size input first; viewport gets the leftover.
		//
		// v2 Width is border-box: size textarea to interior or placeholder wraps.
		boxWidth := max(1, m.width-2)
		m.textarea.SetWidth(max(1, boxWidth-2*rowPadCompact-2))

		// Measure chrome instead of hardcoding; widget height drifts with text.
		footerH := lipgloss.Height(m.renderInputArea()) + lipgloss.Height(m.renderHelpLine())
		headerH := lipgloss.Height(m.renderStatusBar())
		vpH := m.height - headerH - footerH
		if vpH < 1 {
			vpH = 1
		}

		atBottom := m.ready && m.viewport.AtBottom()
		if !m.ready {
			m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(vpH))
			m.viewport.SetContent(m.renderMessages())
			m.ready = true
		} else {
			m.viewport.SetWidth(m.width)
			m.viewport.SetHeight(vpH)
			m.viewport.SetContent(m.renderMessages())
			if atBottom {
				m.viewport.GotoBottom()
			}
		}
		m.modelPicker.SetWidth(max(1, m.width-10))
		m.modelPicker.SetHeight(max(1, m.height-6))

	case tea.KeyPressMsg:
		if m.modelPickerActive {
			switch msg.String() {
			case "ctrl+l", "esc":
				m.modelPickerActive = false
				return m, nil
			case "enter":
				if !m.modelPicker.SettingFilter() {
					if sel := m.modelPicker.SelectedItem(); sel != nil {
						if item, ok := sel.(ModelItem); ok {
							m.providerName = item.Provider
							m.modelName = item.Model
							m.modelPickerActive = false
							inputCh := m.inputCh
							go func() {
								inputCh <- "/provider " + item.Provider
								inputCh <- "/model " + item.Model
							}()
							return m, m.listenOutput()
						}
					}
				}
			}
			var pCmd tea.Cmd
			m.modelPicker, pCmd = m.modelPicker.Update(msg)
			return m, pCmd
		}

		if m.approvalActive {
			return m.updateApproval(msg)
		}

		switch msg.String() {
		case "ctrl+l":
			m.modelPickerActive = true
			m.modelPicker.SetWidth(max(1, m.width-10))
			m.modelPicker.SetHeight(max(1, m.height-6))
			return m, nil

		case "ctrl+c":
			if m.streaming {
				if m.cancelRequested {
					return m, tea.Quit
				}
				m.cancelRequested = true
				m.interruptTurn()
				return m, nil
			}
			return m, tea.Quit

		case "esc":
			if m.streaming {
				if m.cancelRequested {
					return m, tea.Quit
				}
				m.cancelRequested = true
				m.interruptTurn()
				return m, nil
			}
			if m.textarea.Value() != "" {
				m.textarea.Reset()
			}
			return m, nil

		case "enter":
			if m.streaming {
				return m, nil
			}
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}
			switch strings.ToLower(input) {
			case "exit", "quit", "/exit", "/quit":
				return m, tea.Quit
			}
			m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: input})
			m.textarea.Reset()
			m.streaming = true
			m.streamBuf.Reset()
			// No tokens yet, so show thinking rather than streaming.
			m.mascot.setState(mascotThinking, msgNow())
			m.animating = true
			m.animEpoch++
			inputCh := m.inputCh
			go func() { inputCh <- input }()
			cmds = append(cmds, m.listenOutput(), tickUntil(cadence(mascotThinking), m.animEpoch))

		case "up":
			if !m.textarea.Focused() {
				m.viewport.ScrollUp(3)
			}
		case "down":
			if !m.textarea.Focused() {
				m.viewport.ScrollDown(3)
			}
		case "pgup":
			m.viewport.ScrollUp(m.viewport.Height() / 2)
		case "pgdown":
			m.viewport.ScrollDown(m.viewport.Height() / 2)
		}

	case agentChunkMsg:
		m.mascot.setState(mascotWorking, msgNow())
		m.streamBuf.WriteString(msg.delta)
		m.setStreamingMessage(m.streamBuf.String())
		if m.viewport.AtBottom() {
			m.viewport.GotoBottom()
		}
		cmds = append(cmds, m.listenOutput())

	case agentDoneMsg:
		m.streaming = false
		m.cancelRequested = false
		m.streamBuf.Reset()
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
		// Revert optimistic picker pick to engine truth.
		if m.agentSession != nil && m.registry != nil {
			m.providerName = m.registry.ActiveName()
			m.modelName = m.agentSession.Model()
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.addMessage(ChatMessage{Role: RoleSystem, Label: "⏹ Stopped", Content: "generation canceled — enter a new message to continue"})
			} else {
				m.addMessage(ChatMessage{Role: RoleError, Label: "Error", Content: msg.err.Error()})
			}
		}
		m.viewport.GotoBottom()
		cmds = append(cmds, m.listenOutput())

	case agentToolStartMsg:
		if m.streaming {
			m.mascot.setState(mascotWorking, msgNow())
		}
		trimmed := strings.TrimSpace(msg.args)
		if len(trimmed) > 200 {
			trimmed = trimmed[:197] + "..."
		}
		content := "running…"
		if trimmed != "" {
			content += "\n" + trimmed
		}
		m.messages = append(m.messages, ChatMessage{
			Role:    RoleTool,
			Label:   "🔧 " + msg.name,
			Content: content,
		})
		if msg.toolCallID != "" {
			m.pendingTools[msg.toolCallID] = len(m.messages) - 1
		}
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		cmds = append(cmds, m.listenOutput())

	case agentToolMsg:
		if m.streaming {
			m.mascot.setState(mascotWorking, msgNow())
		}
		label := "🔧 " + msg.name
		role := RoleTool
		if msg.isError {
			role = RoleError
			label = msg.name
		}
		if idx, ok := m.pendingTools[msg.toolCallID]; ok && idx < len(m.messages) {
			m.messages[idx].Role = role
			m.messages[idx].Label = label
			m.messages[idx].Content = msg.result
			m.messages[idx].Diff = msg.diff
			delete(m.pendingTools, msg.toolCallID)
			m.viewport.SetContent(m.renderMessages())
		} else {
			m.addMessage(ChatMessage{Role: role, Label: label, Content: msg.result, Diff: msg.diff})
		}
		m.viewport.GotoBottom()
		cmds = append(cmds, m.listenOutput())

	case agentExitMsg:
		if msg.notice != "" {
			m.addMessage(ChatMessage{Role: RoleSystem, Label: "GoCode", Content: msg.notice})
			m.viewport.GotoBottom()
		}
		m.streaming = false
		return m, tea.Quit

	case approvalRequestMsg:
		if m.approvalActive {
			return m, pollApproval(m.bridge)
		}
		m.approvalActive = true
		m.approvalReq = msg.req
		m.approvalFocus = 0
		return m, pollApproval(m.bridge)

	case frameMsg:
		m.mascot.step(msg.at)
		// Re-arm only current turn so late frames can't keep old chains ticking.
		if m.animating && msg.epoch == m.animEpoch {
			cmds = append(cmds, tickUntil(cadence(m.mascot.state), m.animEpoch))
		}
	}

	if !m.approvalActive {
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
	switch msg.String() {
	case "left", "right", "tab":
		if m.approvalFocus == 0 {
			m.approvalFocus = 1
		} else {
			m.approvalFocus = 0
		}
	case "enter":
		approved := m.approvalFocus == 0
		req := m.approvalReq
		m.approvalActive = false
		label := "✓ Approved"
		role := RoleSystem
		if !approved {
			label = "✗ Denied"
			role = RoleError
		}
		m.addMessage(ChatMessage{Role: role, Label: label, Content: req.ToolName})
		m.viewport.GotoBottom()
		go func() { req.ReplyCh <- approved }()
	case "esc":
		req := m.approvalReq
		m.approvalActive = false
		m.addMessage(ChatMessage{Role: RoleSystem, Label: "✗ Denied", Content: req.ToolName})
		m.viewport.GotoBottom()
		go func() { req.ReplyCh <- false }()
	case "ctrl+c":
		req := m.approvalReq
		m.approvalActive = false
		go func() { req.ReplyCh <- false }()
		return m, tea.Quit
	}
	return m, nil
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
	return v
}

func (m Model) render() string {
	if !m.ready {
		return "\n  Initializing GoCode…\n"
	}

	base := lipgloss.JoinVertical(lipgloss.Left,
		m.renderStatusBar(),
		m.viewport.View(),
		m.renderInputArea(),
	)

	hint := m.renderHelpLine()
	base = lipgloss.JoinVertical(lipgloss.Left, base, hint)

	if m.approvalActive {
		return placeModal(base, m.renderApprovalModal(), m.width, m.height)
	}

	if m.modelPickerActive {
		return placeModal(base, modalOverlayStyle.Render(m.modelPicker.View()), m.width, m.height)
	}

	return base
}

// Below this the bar drops model id rather than truncating to a stub.
const minModelWidth = 12

func (m *Model) renderStatusBar() string {
	contentWidth := max(1, m.width-2)

	// Mascot has no textual fallback, so it's dropped last; model id truncates first.
	head := m.mascot.inline(msgNow(), mascotFaceStyles) +
		statusSeparator + statusProviderStyle.Render(m.providerName)

	var suffix string
	if m.streaming {
		suffix = statusSeparator + statusStreamingStyle.Render(m.mascot.state.String())
	}

	// When tight, drop workspace path first, model id second.
	var right string
	if path := workspaceShortName(m.workspaceRoot); path != "" {
		needed := lipgloss.Width(path) + lipgloss.Width(statusSeparator)
		fixed := lipgloss.Width(head) + lipgloss.Width(suffix)
		if fixed+needed+minModelWidth+lipgloss.Width(statusSeparator) <= contentWidth {
			right = statusPathStyle.Render(path)
		}
	}

	left := head
	room := contentWidth - lipgloss.Width(head) - lipgloss.Width(suffix) -
		lipgloss.Width(right) - lipgloss.Width(statusSeparator)
	if room >= minModelWidth {
		left += statusSeparator + statusModelStyle.Render(truncateToWidth(m.modelName, room))
	}
	left += suffix

	gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap > 0 {
		left += statusBarStyle.Render(strings.Repeat(" ", gap))
	}
	return left + right
}

func (m *Model) renderInputArea() string {
	style := inputBoxStyle
	// Blurred while streaming; focused prompt would invite typing that's dropped.
	if !m.focused || m.streaming {
		style = inputBoxBlurStyle
	}
	return style.Width(max(1, m.width-2)).Render(m.textarea.View())
}

// Shows only keys that work now; mid-turn that's just interrupt.
func (m *Model) renderHelpLine() string {
	var text string
	switch {
	case m.streaming:
		text = "Ctrl+C stop · press again to quit · PgUp/PgDn scroll"
		if m.cancelRequested {
			text = "Ctrl+C again to quit"
		}
	case m.width < 48:
		text = "Enter send · Esc clear · Ctrl+C quit"
	case m.width < 72:
		text = "Enter send · Shift+Enter newline · Esc clear · Ctrl+C quit"
	default:
		text = "Enter send · Shift+Enter newline · Ctrl+L models · Esc clear · Ctrl+C quit · PgUp/PgDn scroll"
	}
	return inputHintStyle.Render(truncateToWidth(text, m.width))
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
	if width == 1 {
		return "…"
	}
	var result strings.Builder
	for _, r := range text {
		next := result.String() + string(r)
		if lipgloss.Width(next)+lipgloss.Width("…") > width {
			break
		}
		result.WriteRune(r)
	}
	return result.String() + "…"
}

func (m *Model) renderMessages() string {
	if len(m.messages) == 0 {
		return renderBanner(m.providerName, m.modelName, m.version, m.width)
	}
	parts := make([]string, 0, len(m.messages)*3)
	for _, msg := range m.messages {
		parts = append(parts, m.renderMessage(msg))
	}
	return strings.Join(parts, "\n")
}

func (m *Model) renderMessage(msg ChatMessage) string {
	switch msg.Role {
	case RoleUser:
		label := userLabelStyle.Render("  ▶ " + msg.Label)
		content := userBubbleStyle.Width(max(1, m.width-6)).Render(msg.Content)
		return label + "\n" + content + "\n"
	case RoleAssistant:
		label := asstLabelStyle.Render("  ✦ " + msg.Label)
		content := asstContentStyle.Width(max(1, m.width-4)).Render(msg.Content)
		return label + "\n" + content + "\n"
	case RoleTool:
		label := toolLabelStyle.Render("  " + msg.Label)
		content := toolBubbleStyle.Width(max(1, m.width-6)).Render(msg.Content)
		if msg.Diff != "" {
			content += "\n" + m.renderDiff(msg.Diff)
		}
		return label + "\n" + content + "\n"
	case RoleError:
		return errorStyle.Render("  ✗ "+msg.Label+": "+msg.Content) + "\n"
	case RoleSystem:
		return systemStyle.Render("  "+msg.Label+" "+msg.Content) + "\n"
	}
	return ""
}

func (m *Model) renderApprovalModal() string {
	req := m.approvalReq

	// Clamp to terminal; Place returns input unchanged when too narrow.
	modalW := max(8, min(m.width, 56))

	title := modalTitleStyle.Render("⚠  Tool Approval Required")
	toolLine := "  Tool: " + modalToolNameStyle.Render(req.ToolName)

	argLines := []string{}
	var prettyArgs map[string]interface{}
	if err := json.Unmarshal(req.Args, &prettyArgs); err == nil {
		for k, v := range prettyArgs {
			val := fmt.Sprintf("%v", v)
			if len(val) > 120 {
				val = val[:117] + "..."
			}
			argLines = append(argLines, "  "+modalArgKeyStyle.Render(k+": ")+modalArgValStyle.Render(val))
		}
	} else {
		argLines = append(argLines, "  "+string(req.Args))
	}

	preview := ""
	if req.Preview != "" {
		preview = "\n  Preview:\n" + toolBubbleStyle.Render(req.Preview)
	}

	approveBtn := modalButtonApprove.Render("  ✓ Approve  ")
	denyBtn := modalButtonDeny.Render("  ✗ Deny  ")
	if m.approvalFocus == 0 {
		approveBtn = modalButtonFocused.Render("  ✓ Approve  ")
	} else {
		denyBtn = modalButtonFocused.Render("  ✗ Deny  ")
	}

	body := strings.Join(append(
		[]string{title, "", toolLine},
		append(argLines, preview, "", "  "+approveBtn+"   "+denyBtn, systemStyle.Render("  ← → Tab: switch  Enter: confirm  Esc: deny"))...,
	), "\n")

	return modalOverlayStyle.Width(modalW).Render(body)
}

func (m *Model) renderDiff(diff string) string {
	addStyle := lipgloss.NewStyle().Foreground(col.diffAddFg).Background(col.diffAddBg).Padding(0, rowPadCompact).MarginLeft(indentGutter)
	delStyle := lipgloss.NewStyle().Foreground(col.diffDelFg).Background(col.diffDelBg).Padding(0, rowPadCompact).MarginLeft(indentGutter)
	metaStyle := lipgloss.NewStyle().Foreground(col.diffMeta).MarginLeft(indentGutter + rowPadCompact)
	ctxStyle := lipgloss.NewStyle().Foreground(colorText).MarginLeft(indentGutter + rowPadCompact)
	w := max(1, m.width-6)
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index "):
			out = append(out, metaStyle.Width(w).Render(line))
		case strings.HasPrefix(line, "+"):
			out = append(out, addStyle.Width(w).Render(line))
		case strings.HasPrefix(line, "-"):
			out = append(out, delStyle.Width(w).Render(line))
		default:
			out = append(out, ctxStyle.Width(w).Render(line))
		}
	}
	return strings.Join(out, "\n")
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
	m.viewport.SetContent(m.renderMessages())
}

func (m *Model) setStreamingMessage(content string) {
	if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
		m.messages[len(m.messages)-1].Content = content
	} else {
		m.messages = append(m.messages, ChatMessage{
			Role:    RoleAssistant,
			Label:   "GoCode",
			Content: content,
		})
	}
	m.viewport.SetContent(m.renderMessages())
}

func (m *Model) finalizeStreamingMessage() {
	if m.streamBuf.Len() > 0 {
		content := m.streamBuf.String()
		m.streamBuf.Reset()
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
			m.messages[len(m.messages)-1].Content = content
		}
	}
}

func (m Model) textareaHeight() int {
	return m.textarea.Height()
}

func (m Model) listenOutput() tea.Cmd {
	return func() tea.Msg {
		return <-m.outputCh
	}
}

func pollApproval(bridge *ApprovalBridge) tea.Cmd {
	return func() tea.Msg {
		return approvalRequestMsg{req: <-bridge.RequestCh}
	}
}
