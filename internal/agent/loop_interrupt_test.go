package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// assertNoDanglingToolCalls verifies every tool call id has a matching tool result;
// a single dangling id fails every later request.
func assertNoDanglingToolCalls(t *testing.T, history []provider.Message) {
	t.Helper()
	for i, m := range history {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		need := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			need[tc.ID] = false
		}
		for j := i + 1; j < len(history) && history[j].Role == "tool"; j++ {
			if _, ok := need[history[j].ToolCallID]; ok {
				need[history[j].ToolCallID] = true
			}
		}
		for id, found := range need {
			if !found {
				t.Errorf("history message %d: tool call %q has no result", i, id)
			}
		}
	}
}

// A guard-stopped turn returns without executing; synthetic results must close every call.
func TestGuardStopLeavesNoDanglingToolCalls(t *testing.T) {
	p := &recordingProvider{toolCalls: []provider.ToolCall{{
		ID:   "call_1",
		Name: "file_read",
		Args: json.RawMessage(`{"path":"missing.txt"}`),
	}}}
	sess := NewSession("m")
	engine := engineWith(p, sess)

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if p.callCount() == 0 {
		t.Fatal("provider was never called")
	}

	assertNoDanglingToolCalls(t, sess.History())
}

// cancelOnExecuteTool cancels from inside Execute, simulating an interrupt landing mid-batch.
type cancelOnExecuteTool struct {
	cancel context.CancelFunc
}

func (c *cancelOnExecuteTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{Name: "cancel_tool", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (c *cancelOnExecuteTool) RequiresApproval() bool { return false }

func (c *cancelOnExecuteTool) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	c.cancel()
	return tools.Result{Output: "ok"}, nil
}

// Interrupting a turn between two tool calls must still write a synthetic
// result for every call the batch never reached.
func TestInterruptMidBatchLeavesNoDanglingToolCalls(t *testing.T) {
	p := &recordingProvider{toolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "cancel_tool", Args: json.RawMessage(`{}`)},
		{ID: "call_2", Name: "file_read", Args: json.RawMessage(`{"path":"a.txt"}`)},
	}}
	sess := NewSession("m")

	toolReg := tools.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	toolReg.Register(&cancelOnExecuteTool{cancel: cancel})

	reg := provider.NewRegistry()
	reg.Register(p)
	engine := NewAgentLoop(reg, sess, toolReg, tools.NewApprovalGate())

	err := engine.RunTurn(ctx, "go")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	history := sess.History()
	assertNoDanglingToolCalls(t, history)

	var synthetic *provider.Message
	for i := range history {
		if history[i].Role == "tool" && history[i].ToolCallID == "call_2" {
			synthetic = &history[i]
		}
	}
	if synthetic == nil {
		t.Fatal("no tool result recorded for the interrupted second call")
	}
	if !strings.Contains(synthetic.Content, "interrupted") {
		t.Errorf("synthetic result should explain the interruption, got %q", synthetic.Content)
	}
}
