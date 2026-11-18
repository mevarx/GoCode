package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// deltaProvider streams scripted chunks, one script per provider round.
type deltaProvider struct {
	scripts [][]provider.StreamChunk
	round   int
}

func (p *deltaProvider) Name() string { return "delta-script" }

func (p *deltaProvider) Models(context.Context) ([]string, error) { return []string{"m"}, nil }

func (p *deltaProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 8)
	if p.round < len(p.scripts) {
		for _, c := range p.scripts[p.round] {
			ch <- c
		}
		p.round++
	} else {
		ch <- provider.StreamChunk{Delta: "done"}
		ch <- provider.StreamChunk{Done: true}
	}
	close(ch)
	return ch, nil
}

func deltaEngine(t *testing.T, p provider.Provider, toolList ...tools.Tool) (*AgentLoop, *tools.ApprovalGate) {
	t.Helper()

	reg := provider.NewRegistry()
	reg.Register(p)
	toolReg := tools.NewRegistry()
	for _, tool := range toolList {
		if err := toolReg.Register(tool); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}
	sess := NewSession("m")
	gate := tools.NewApprovalGate()
	return NewAgentLoop(reg, sess, toolReg, gate), gate
}

// Streamed arguments are forwarded with the call id and accumulated preview,
// yet approval and execution still see only the complete JSON.
func TestLoopForwardsToolDeltasAndApprovesCompleteArgs(t *testing.T) {
	p := &deltaProvider{scripts: [][]provider.StreamChunk{{
		{ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "call_1", Name: "guarded", ArgsDelta: `{"path":`}},
		{ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "call_1", Name: "guarded", ArgsDelta: `"a.txt"}`}},
		{ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "guarded", Args: json.RawMessage(`{"path":"a.txt"}`)}}},
		{Done: true},
	}}}

	tool := &trackedTool{name: "guarded", requires: true}
	engine, gate := deltaEngine(t, p, tool)
	var approved []string
	gate.OnPresentChoice = func(_ string, args json.RawMessage, _ string) (tools.ApprovalChoice, error) {
		approved = append(approved, string(args))
		return tools.ApprovalOnce, nil
	}

	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	var deltas []LoopEvent
	firstStart := -1
	for i, ev := range events {
		switch ev.Kind {
		case EventToolDelta:
			deltas = append(deltas, ev)
		case EventToolStart:
			if firstStart < 0 {
				firstStart = i
			}
		}
	}

	if len(deltas) != 2 {
		t.Fatalf("expected 2 EventToolDelta, got %d: %+v", len(deltas), deltas)
	}
	if deltas[0].ToolCallID != "call_1" || deltas[0].Tool != "guarded" {
		t.Errorf("delta lost identity: %+v", deltas[0])
	}
	if deltas[0].Args != `{"path":` {
		t.Errorf("first preview = %q", deltas[0].Args)
	}
	if deltas[1].Args != `{"path":"a.txt"}` {
		t.Errorf("accumulated preview = %q", deltas[1].Args)
	}

	if firstStart < 0 {
		t.Fatal("no EventToolStart was emitted")
	}
	if got := events[len(events)-1].Kind; got != EventTurnEnd {
		t.Errorf("last event = %v, want EventTurnEnd", got)
	}

	// Approval must run on complete args only, never on a fragment.
	if len(approved) != 1 || approved[0] != `{"path":"a.txt"}` {
		t.Fatalf("approval saw %q, want only the complete args", approved)
	}
	if tool.execs != 1 || len(tool.lastArgs) != 1 || tool.lastArgs[0] != `{"path":"a.txt"}` {
		t.Errorf("tool executed with %q (execs=%d)", tool.lastArgs, tool.execs)
	}
}

// ANSI/control bytes in a fragment must be stripped before an event leaves the
// loop; a preview is never a raw provider buffer.
func TestToolDeltaPreviewIsSanitized(t *testing.T) {
	p := &deltaProvider{scripts: [][]provider.StreamChunk{{
		{ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "call_1", Name: "probe", ArgsDelta: "\x1b[31m{\"a\":\x07"}},
		{ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "probe", Args: json.RawMessage(`{"a":1}`)}}},
		{Done: true},
	}}}
	engine, _ := deltaEngine(t, p, &trackedTool{name: "probe"})

	var previews []string
	engine.Observe = func(ev LoopEvent) {
		if ev.Kind == EventToolDelta {
			previews = append(previews, ev.Args)
		}
	}
	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if len(previews) != 1 {
		t.Fatalf("expected 1 preview, got %d", len(previews))
	}
	if strings.ContainsAny(previews[0], "\x1b\x07") {
		t.Errorf("preview kept control bytes: %q", previews[0])
	}
	if previews[0] != `{"a":` {
		t.Errorf("sanitized preview = %q, want %q", previews[0], `{"a":`)
	}
}
