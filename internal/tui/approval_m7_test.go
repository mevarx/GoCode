package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/tools"
)

// the bridge carries the classifier's danger reason into the modal
// request (the modal already focuses Deny and renders the warning when
// Danger is non-empty). The legacy choice path keeps Danger empty.
func TestBridgePromptCarriesDanger(t *testing.T) {
	bridge := NewApprovalBridge()
	type result struct {
		choice tools.ApprovalChoice
		err    error
	}
	done := make(chan result, 1)
	go func() {
		c, err := bridge.RequestApprovalPrompt(tools.ApprovalPrompt{
			ID:       "req-1",
			ToolName: "shell_exec",
			Args:     json.RawMessage(`{"command":"rm -rf /"}`),
			Preview:  "run: rm -rf /",
			Danger:   "recursive forced delete (rm -rf)",
		})
		done <- result{c, err}
	}()

	select {
	case req := <-bridge.RequestCh:
		if req.Danger != "recursive forced delete (rm -rf)" {
			t.Fatalf("modal request danger = %q", req.Danger)
		}
		if req.ToolName != "shell_exec" || string(req.Args) != `{"command":"rm -rf /"}` {
			t.Fatalf("modal request = %+v", req)
		}
		if req.ChoiceCh == nil {
			t.Fatal("choice channel missing")
		}
		req.ChoiceCh <- tools.ApprovalGrantExact
	case <-time.After(3 * time.Second):
		t.Fatal("bridge never delivered the prompt")
	}

	select {
	case r := <-done:
		if r.err != nil || r.choice != tools.ApprovalGrantExact {
			t.Fatalf("bridge answer = (%v, %v)", r.choice, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge never returned the answer")
	}
}

func TestBridgeLegacyChoiceHasNoDanger(t *testing.T) {
	bridge := NewApprovalBridge()
	done := make(chan tools.ApprovalChoice, 1)
	go func() {
		c, _ := bridge.RequestApprovalChoice("file_write", json.RawMessage(`{}`), "preview")
		done <- c
	}()
	select {
	case req := <-bridge.RequestCh:
		if req.Danger != "" {
			t.Fatalf("legacy path danger = %q, want empty", req.Danger)
		}
		req.ChoiceCh <- tools.ApprovalOnce
	case <-time.After(3 * time.Second):
		t.Fatal("no request")
	}
	select {
	case c := <-done:
		if c != tools.ApprovalOnce {
			t.Fatalf("choice = %v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer")
	}
}
