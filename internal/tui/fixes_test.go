package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
)

// HIGH-10: modal must fit terminal and keep base frame underneath.
func TestApprovalModalClampsToTerminalSize(t *testing.T) {
	for _, w := range []int{20, 30, 40, 48, 51, 52, 60, 80} {
		m := newTestModel(nil)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 14})
		m = updated.(Model)
		m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: "transcript-marker"})
		m.approvalActive = true
		m.approvalReq = ApprovalRequest{
			ToolName: "file_write",
			Args:     json.RawMessage(`{"path":"a.go","content":"x"}`),
			ReplyCh:  make(chan bool, 1),
		}

		out := m.View().Content
		if got := lipgloss.Height(out); got > 14 {
			t.Errorf("width %d: modal view height %d exceeds terminal height 14", w, got)
		}
		for i, line := range strings.Split(out, "\n") {
			if lw := lipgloss.Width(line); lw > w {
				t.Errorf("width %d: line %d is %d cells wide", w, i, lw)
			}
		}
		if !strings.Contains(stripANSI(out), "file_write") {
			t.Errorf("width %d: approval modal content missing", w)
		}
	}

	// Modal overlays rather than replaces the frame.
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m = updated.(Model)
	m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: "transcript-marker"})
	m.approvalActive = true
	m.approvalReq = ApprovalRequest{
		ToolName: "file_write",
		Args:     json.RawMessage(`{"path":"a.go","content":"x"}`),
		ReplyCh:  make(chan bool, 1),
	}
	if got := stripANSI(m.View().Content); !strings.Contains(got, "transcript-marker") {
		t.Errorf("transcript not visible beneath the approval modal:\n%s", got)
	}
}

// HIGH-4: bridge approval renders as modal and answer flows back.
func TestApprovalPromptRendersFromTheBridge(t *testing.T) {
	bridge := NewApprovalBridge()
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.bridge = bridge

	result := make(chan bool, 1)
	go func() {
		ok, err := bridge.RequestApproval("shell_exec", json.RawMessage(`{"command":"ls"}`), "run ls?")
		if err != nil {
			t.Errorf("bridge error: %v", err)
		}
		result <- ok
	}()

	msg := pollApproval(bridge)()
	updated, _ = m.Update(msg)
	m = updated.(Model)
	if !m.approvalActive {
		t.Fatal("approval modal not active after bridge request")
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "shell_exec") {
		t.Errorf("modal does not show the tool name:\n%s", view)
	}
	if !strings.Contains(view, "run ls?") {
		t.Errorf("modal does not show the preview:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.approvalActive {
		t.Fatal("approval modal still active after Enter")
	}
	select {
	case ok := <-result:
		if !ok {
			t.Error("Enter on the Approve button should reply true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge requester never received a reply")
	}
}

// MEDIUM-5: status bar shows engine result; rejected picks must not linger.
func TestStatusBarReflectsEngineResultAfterTurn(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&fakeProvider{name: "ollama", models: []string{"llama3"}})
	sess := agent.NewSession("llama3")

	m := newTestModel(nil)
	m.agentSession = sess
	m.registry = reg
	m.providerName = "wrong-provider"
	m.modelName = "wrong-model"
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(agentDoneMsg{})
	m = updated.(Model)
	if m.providerName != "ollama" || m.modelName != "llama3" {
		t.Fatalf("status bar not corrected to engine state, got %q/%q", m.providerName, m.modelName)
	}
	bar := stripANSI(m.renderStatusBar())
	if !strings.Contains(bar, "ollama") || !strings.Contains(bar, "llama3") {
		t.Errorf("status bar does not show engine state: %q", bar)
	}
}
