package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestStreamedDeltaDropsEscapeSequences(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(agentChunkMsg{delta: "hello\x1b[2Jworld"})
	m = updated.(Model)

	if got := m.streamBuf.String(); strings.Contains(got, "\x1b[2J") {
		t.Fatalf("stream buffer retained injected sequence: %q", got)
	} else if !strings.Contains(got, "helloworld") {
		t.Fatalf("visible delta text lost: %q", got)
	}
}

func TestApprovalRequestFieldsDropsEscapeSequences(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m = updated.(Model)

	req := ApprovalRequest{
		ToolName: "shell_\x1b[2Jexec",
		Args:     json.RawMessage("{\"command\":\"echo \\u001b[2J hi\"}"),
		Preview:  "\x1b[2Jpreview\x1b[1;1Hbody",
		ReplyCh:  make(chan bool, 1),
	}
	updated, _ = m.Update(approvalRequestMsg{req: req})
	m = updated.(Model)

	if !m.approvalActive {
		t.Fatal("approval modal did not open")
	}
	view := m.View().Content

	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b[1;1H") {
		t.Fatalf("rendered modal contains injected sequence: %q", view)
	}
	for _, want := range []string{"shell_exec", "preview", "body", "echo "} {
		if !strings.Contains(view, want) {
			t.Errorf("visible modal text %q lost: %q", want, view)
		}
	}
}
