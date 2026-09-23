package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
}

type agentChunkMsg struct{ delta string }
type agentDoneMsg struct{ err error }
type agentToolMsg struct {
	name    string
	result  string
	isError bool
}
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
	spinner       spinner.Model

	modelPickerActive bool
	modelPicker       list.Model

	approvalActive bool
	approvalReq    ApprovalRequest
	approvalFocus  int

	bridge   *ApprovalBridge
	inputCh  chan string
	outputCh chan tea.Msg
	cancelCh chan struct{}

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

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorWarning)

	picker := NewModelPicker(pickerItems, 80, 24)

	return Model{
		streamBuf:     &strings.Builder{},
		textarea:      ta,
		spinner:       sp,
		providerName:  providerName,
		modelName:     modelName,
		version:       version,
		workspaceRoot: workspaceRoot,
		modelPicker:   picker,
		bridge:        bridge,
		inputCh:       inputCh,
		outputCh:      outputCh,
		cancelCh:      cancelCh,
		focused:       true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
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
		headerH := 1
		footerH := m.textareaHeight() + 3
		vpH := m.height - headerH - footerH
		if vpH < 1 {
			vpH = 1
		}
		atBottom := m.ready && m.viewport.AtBottom()
		if !m.ready {
			m.viewport = viewport.New(m.width, vpH)
			m.viewport.SetContent(m.renderMessages())
			m.ready = true
		} else {
			m.viewport.Width = m.width
			m.viewport.Height = vpH
			m.viewport.SetContent(m.renderMessages())
			if atBottom {
				m.viewport.GotoBottom()
			}
		}
		m.textarea.SetWidth(max(1, m.width-4))
		m.modelPicker.SetWidth(max(1, m.width-10))
		m.modelPicker.SetHeight(max(1, m.height-6))

	case tea.KeyMsg:
		if m.modelPickerActive {
			switch msg.Type {
			case tea.KeyCtrlL, tea.KeyEsc:
				m.modelPickerActive = false
				return m, nil
			case tea.KeyEnter:
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

		switch msg.Type {
		case tea.KeyCtrlL:
			m.modelPickerActive = true
			m.modelPicker.SetWidth(max(1, m.width-10))
			m.modelPicker.SetHeight(max(1, m.height-6))
			return m, nil

		case tea.KeyCtrlC:
			if m.streaming {
				if m.cancelRequested {
					return m, tea.Quit
				}
				m.cancelRequested = true
				m.interruptTurn()
				return m, nil
			}
			return m, tea.Quit

		case tea.KeyEsc:
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

		case tea.KeyEnter:
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
			inputCh := m.inputCh
			go func() { inputCh <- input }()
			cmds = append(cmds, m.listenOutput())

		case tea.KeyUp:
			if !m.textarea.Focused() {
				m.viewport.ScrollUp(3)
			}
		case tea.KeyDown:
			if !m.textarea.Focused() {
				m.viewport.ScrollDown(3)
			}
		case tea.KeyPgUp:
			m.viewport.ScrollUp(m.viewport.Height / 2)
		case tea.KeyPgDown:
			m.viewport.ScrollDown(m.viewport.Height / 2)
		}

	case agentChunkMsg:
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
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.addMessage(ChatMessage{Role: RoleSystem, Label: "⏹ Stopped", Content: "generation canceled — enter a new message to continue"})
			} else {
				m.addMessage(ChatMessage{Role: RoleError, Label: "Error", Content: msg.err.Error()})
			}
		}
		m.viewport.GotoBottom()
		cmds = append(cmds, m.listenOutput())

	case agentToolMsg:
		label := "🔧 " + msg.name
		role := RoleTool
		if msg.isError {
			role = RoleError
			label = msg.name
		}
		m.addMessage(ChatMessage{Role: role, Label: label, Content: msg.result})
		m.viewport.GotoBottom()
		cmds = append(cmds, m.listenOutput())

	case approvalRequestMsg:
		if m.approvalActive {
			return m, pollApproval(m.bridge)
		}
		m.approvalActive = true
		m.approvalReq = msg.req
		m.approvalFocus = 0
		return m, pollApproval(m.bridge)

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		cmds = append(cmds, spinCmd)
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

func (m Model) updateApproval(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyRight, tea.KeyTab:
		if m.approvalFocus == 0 {
			m.approvalFocus = 1
		} else {
			m.approvalFocus = 0
		}
	case tea.KeyEnter:
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
	case tea.KeyEsc:
		req := m.approvalReq
		m.approvalActive = false
		m.addMessage(ChatMessage{Role: RoleSystem, Label: "✗ Denied", Content: req.ToolName})
		m.viewport.GotoBottom()
		go func() { req.ReplyCh <- false }()
	case tea.KeyCtrlC:
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

func (m Model) View() string {
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

func (m *Model) renderStatusBar() string {
	left := statusProviderStyle.Render("GoCode") + statusSeparator + statusProviderStyle.Render(m.providerName) + statusSeparator + statusModelStyle.Render(m.modelName)
	if m.streaming {
		left = left + statusSeparator + statusStreamingStyle.Render(m.spinner.View()+" generating…")
	}

	contentWidth := max(1, m.width-2)
	path := workspaceShortName(m.workspaceRoot)
	right := ""
	if path != "" && contentWidth >= 38 {
		right = statusPathStyle.Render(path)
	}
	if lipgloss.Width(left)+lipgloss.Width(right) > contentWidth {
		leftText := "GoCode │ " + m.providerName + " │ " + m.modelName
		if m.streaming {
			leftText += " │ generating…"
		}
		return statusBarStyle.Width(contentWidth).Render(truncateToWidth(leftText, contentWidth))
	}
	if right != "" {
		left += statusBarStyle.Render(strings.Repeat(" ", contentWidth-lipgloss.Width(left)-lipgloss.Width(right))) + right
	} else if pad := contentWidth - lipgloss.Width(left); pad > 0 {
		left += statusBarStyle.Render(strings.Repeat(" ", pad))
	}
	return left
}

func (m *Model) renderInputArea() string {
	style := inputBoxStyle
	if !m.focused {
		style = inputBoxBlurStyle
	}
	return style.Width(max(1, m.width-2)).Render(m.textarea.View())
}

func (m *Model) renderHelpLine() string {
	text := "Enter send · Shift+Enter newline · Ctrl+L models · Esc clear · Ctrl+C quit · PgUp/PgDn scroll"
	if m.streaming {
		text = "Generating… · Ctrl+C stop; press again to quit"
		if m.width < 48 {
			text = "Generating… · Ctrl+C stop"
		}
	} else if m.width < 72 {
		text = "Enter send · Shift+Enter newline · Esc clear · Ctrl+C quit"
	}
	if !m.streaming && m.width < 48 {
		text = "Enter send · Esc clear · Ctrl+C quit"
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

	return modalOverlayStyle.Render(body)
}

func placeModal(base, modal string, totalW, totalH int) string {
	return lipgloss.Place(totalW, totalH, lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceBackground(lipgloss.AdaptiveColor{Light: "#f5f5f5", Dark: "#0d1117"}),
	)
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
