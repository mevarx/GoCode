package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/tools"
)

// TestApprovalModalAllowExactKey checks the "a" key: it approves the call,
// records the tool as granted for the footer, and sends the grant choice.
func TestApprovalModalAllowExactKey(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m = updated.(Model)

	choiceCh := make(chan tools.ApprovalChoice, 1)
	m.approvalActive = true
	m.approvalReq = ApprovalRequest{
		ToolName: "shell_exec",
		Args:     json.RawMessage(`{"command":"ls"}`),
		Preview:  "run ls?",
		ChoiceCh: choiceCh,
	}

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "allow exact args this session") {
		t.Fatalf("modal footer missing the allow-exact hint:\n%s", view)
	}
	if !strings.Contains(view, "read-only tools this turn") {
		t.Fatalf("modal footer missing the read-only hint:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'a'})
	m = updated.(Model)
	if m.approvalActive {
		t.Fatal("modal stayed open after 'a'")
	}
	if !m.sessionGrants["shell_exec"] {
		t.Fatal("session grant not recorded by the model")
	}

	select {
	case choice := <-choiceCh:
		if choice != tools.ApprovalGrantExact {
			t.Fatalf("choice = %v, want ApprovalGrantExact", choice)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine never received the grant choice")
	}

	// Reopening the modal shows the used grant in the footer.
	m.approvalActive = true
	m.approvalReq = ApprovalRequest{
		ToolName: "file_write",
		Args:     json.RawMessage(`{"path":"a.go"}`),
		Preview:  "write a.go?",
		ChoiceCh: make(chan tools.ApprovalChoice, 1),
	}
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "granted: shell_exec") {
		t.Fatalf("footer does not list the used grant:\n%s", view)
	}
}

// TestApprovalModalReadOnlyKey checks that "r" sends the per-turn read-only
// choice instead of a plain approval.
func TestApprovalModalReadOnlyKey(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m = updated.(Model)

	choiceCh := make(chan tools.ApprovalChoice, 1)
	m.approvalActive = true
	m.approvalReq = ApprovalRequest{
		ToolName: "mcp_search",
		Args:     json.RawMessage(`{"query":"x"}`),
		Preview:  "search?",
		ChoiceCh: choiceCh,
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r'})
	m = updated.(Model)
	if m.approvalActive {
		t.Fatal("modal stayed open after 'r'")
	}

	select {
	case choice := <-choiceCh:
		if choice != tools.ApprovalReadOnlyTurn {
			t.Fatalf("choice = %v, want ApprovalReadOnlyTurn", choice)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine never received the read-only choice")
	}
}

// TestApprovalChoiceBridgeRoundTrip verifies the bridge carries the richer
// choice and that legacy yes/no requests still resolve.
func TestApprovalChoiceBridgeRoundTrip(t *testing.T) {
	bridge := NewApprovalBridge()
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m = updated.(Model)
	m.bridge = bridge

	result := make(chan tools.ApprovalChoice, 1)
	go func() {
		choice, err := bridge.RequestApprovalChoice("shell_exec", json.RawMessage(`{"command":"ls"}`), "run ls?")
		if err != nil {
			t.Errorf("bridge error: %v", err)
		}
		result <- choice
	}()

	msg := pollApproval(bridge)()
	updated, _ = m.Update(msg)
	m = updated.(Model)
	if !m.approvalActive {
		t.Fatal("choice request did not open the modal")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	select {
	case choice := <-result:
		if choice != tools.ApprovalOnce {
			t.Fatalf("choice = %v, want ApprovalOnce", choice)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("choice requester never received a reply")
	}
}
