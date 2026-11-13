package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// trackedTool is a concurrency-aware fake used by the parallel execution tests.
type trackedTool struct {
	name     string
	readOnly bool
	requires bool
	sleep    time.Duration
	mu       sync.Mutex
	running  int
	maxRun   int
	execs    int
	lastArgs []string
}

func (t *trackedTool) Spec() tools.ToolSpec { return tools.ToolSpec{Name: t.name} }

func (t *trackedTool) RequiresApproval() bool { return t.requires }

func (t *trackedTool) ReadOnly() bool { return t.readOnly }

func (t *trackedTool) Execute(_ context.Context, args json.RawMessage) (tools.Result, error) {
	t.mu.Lock()
	t.running++
	if t.running > t.maxRun {
		t.maxRun = t.running
	}
	t.execs++
	t.lastArgs = append(t.lastArgs, string(args))
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.running--
		t.mu.Unlock()
	}()

	time.Sleep(t.sleep)
	return tools.Result{Output: string(args)}, nil
}

func (t *trackedTool) stats() (execs, maxRun int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.execs, t.maxRun
}

func parallelEngine(sess *Session, reg *tools.Registry, gate *tools.ApprovalGate) *AgentLoop {
	preg := provider.NewRegistry()
	return NewAgentLoop(preg, sess, reg, gate)
}

func callsFor(names ...string) []provider.ToolCall {
	calls := make([]provider.ToolCall, len(names))
	for i, name := range names {
		calls[i] = provider.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Name: name,
			Args: json.RawMessage(fmt.Sprintf(`{"id":%d}`, i)),
		}
	}
	return calls
}

func toolMessageIDs(sess *Session) []string {
	var ids []string
	for _, m := range sess.History() {
		if m.Role == "tool" {
			ids = append(ids, m.ToolCallID)
		}
	}
	return ids
}

// Three independent read-only calls must overlap: each sleeps 100ms, so the
// batch must take well under the 300ms a sequential loop would need.
func TestReadOnlyToolCallsRunConcurrently(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	slow := &trackedTool{name: "slow_read", readOnly: true, sleep: 100 * time.Millisecond}
	if err := reg.Register(slow); err != nil {
		t.Fatalf("register: %v", err)
	}
	engine := parallelEngine(sess, reg, tools.NewApprovalGate())
	m := newTurnMetrics(engine)

	start := time.Now()
	if err := engine.handleToolCalls(context.Background(), callsFor("slow_read", "slow_read", "slow_read"), m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed >= 250*time.Millisecond {
		t.Fatalf("three 100ms read-only calls took %v; expected concurrent execution (<250ms)", elapsed)
	}
	if execs, _ := slow.stats(); execs != 3 {
		t.Fatalf("executions = %d, want 3", execs)
	}
	if ids := toolMessageIDs(sess); len(ids) != 3 {
		t.Fatalf("tool messages = %v, want 3 results", ids)
	}
	if m.toolLatency["slow_read"] <= 0 {
		t.Fatalf("per-call latency not recorded: %v", m.toolLatency)
	}
}

// The concurrency bound must hold for larger batches, and the results must
// still be complete and ordered by call.
func TestReadOnlyToolCallsRespectConcurrencyBound(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	// Classified read-only by built-in name; no ReadOnly interface method.
	reader := &trackedTool{name: "file_read", sleep: 25 * time.Millisecond}
	if err := reg.Register(reader); err != nil {
		t.Fatalf("register: %v", err)
	}
	engine := parallelEngine(sess, reg, tools.NewApprovalGate())
	m := newTurnMetrics(engine)

	names := make([]string, 8)
	for i := range names {
		names[i] = "file_read"
	}
	calls := callsFor(names...)
	if err := engine.handleToolCalls(context.Background(), calls, m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}

	execs, maxRun := reader.stats()
	if execs != 8 {
		t.Fatalf("executions = %d, want 8", execs)
	}
	if maxRun > toolExecutionLimit {
		t.Fatalf("observed %d concurrent read-only calls, bound is %d", maxRun, toolExecutionLimit)
	}
	if maxRun < 2 {
		t.Fatalf("read-only calls did not overlap (max %d)", maxRun)
	}

	ids := toolMessageIDs(sess)
	if len(ids) != len(calls) {
		t.Fatalf("tool messages = %v, want %d", ids, len(calls))
	}
	for i, id := range ids {
		if id != calls[i].ID {
			t.Fatalf("result %d has id %q, want %q (call order must be preserved)", i, id, calls[i].ID)
		}
	}
}

// Results and result events follow call order even when completion order is
// reversed by differing durations.
func TestToolResultsFollowCallOrder(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	fast := &trackedTool{name: "read_fast", readOnly: true, sleep: 5 * time.Millisecond}
	slow := &trackedTool{name: "read_slow", readOnly: true, sleep: 60 * time.Millisecond}
	for _, tool := range []*trackedTool{fast, slow} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	engine := parallelEngine(sess, reg, tools.NewApprovalGate())

	var mu sync.Mutex
	var resultIDs []string
	engine.Observe = func(ev LoopEvent) {
		if ev.Kind != EventToolResult {
			return
		}
		mu.Lock()
		resultIDs = append(resultIDs, ev.ToolCallID)
		mu.Unlock()
	}
	m := newTurnMetrics(engine)

	calls := callsFor("read_slow", "read_fast")
	if err := engine.handleToolCalls(context.Background(), calls, m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}

	ids := toolMessageIDs(sess)
	for i, id := range ids {
		if id != calls[i].ID {
			t.Fatalf("session result %d has id %q, want %q", i, id, calls[i].ID)
		}
	}
	for i, id := range resultIDs {
		if id != calls[i].ID {
			t.Fatalf("event result %d has id %q, want %q", i, id, calls[i].ID)
		}
	}

	// The first message must carry the slow call's args, proving the session
	// kept call order rather than completion order.
	history := sess.History()
	if len(history) == 0 || !strings.Contains(history[0].Content, `"id":0`) {
		t.Fatalf("first tool message = %+v", history)
	}
}

// Approval prompts for a batch must be presented sequentially in call order
// before any tool executes; a denied call never runs and does not block others.
func TestBatchApprovalsPromptBeforeExecution(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	first := &trackedTool{name: "first_write", requires: true, sleep: time.Millisecond}
	denied := &trackedTool{name: "denied_write", requires: true}
	third := &trackedTool{name: "third_write", requires: true, sleep: time.Millisecond}
	for _, tool := range []*trackedTool{first, denied, third} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	gate := tools.NewApprovalGate()

	var mu sync.Mutex
	var prompts []string
	gate.OnPresentChoice = func(toolName string, _ json.RawMessage, _ string) (tools.ApprovalChoice, error) {
		mu.Lock()
		defer mu.Unlock()
		// No tool may have run while any prompt is pending.
		for _, tool := range []*trackedTool{first, denied, third} {
			if execs, _ := tool.stats(); execs != 0 {
				t.Errorf("tool %s executed before approval finished", tool.name)
			}
		}
		prompts = append(prompts, toolName)
		if toolName == "denied_write" {
			return tools.ApprovalDeny, nil
		}
		return tools.ApprovalOnce, nil
	}

	engine := parallelEngine(sess, reg, gate)
	m := newTurnMetrics(engine)
	if err := engine.handleToolCalls(context.Background(), callsFor("first_write", "denied_write", "third_write"), m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}

	if strings.Join(prompts, ",") != "first_write,denied_write,third_write" {
		t.Fatalf("prompt order = %v", prompts)
	}
	if execs, _ := denied.stats(); execs != 0 {
		t.Fatalf("denied tool executed %d times", execs)
	}
	if execs, _ := first.stats(); execs != 1 {
		t.Fatalf("first tool executions = %d, want 1", execs)
	}
	if execs, _ := third.stats(); execs != 1 {
		t.Fatalf("third tool executions = %d, want 1 (a denial must not block others)", execs)
	}

	var denial string
	for _, msg := range sess.History() {
		if msg.ToolCallID == "call_1" {
			denial = msg.Content
		}
	}
	if !strings.Contains(denial, "User denied execution of denied_write") {
		t.Fatalf("denial result = %q", denial)
	}
}

// Mutating calls must never overlap, even when several appear in one batch.
func TestMutatingToolCallsStaySerialized(t *testing.T) {
	sess := NewSession("m")
	reg := tools.NewRegistry()
	writer := &trackedTool{name: "file_write", sleep: 15 * time.Millisecond}
	if err := reg.Register(writer); err != nil {
		t.Fatalf("register: %v", err)
	}
	engine := parallelEngine(sess, reg, tools.NewApprovalGate())
	m := newTurnMetrics(engine)

	calls := callsFor("file_write", "file_write", "file_write", "file_write")
	if err := engine.handleToolCalls(context.Background(), calls, m); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}

	execs, maxRun := writer.stats()
	if execs != len(calls) {
		t.Fatalf("executions = %d, want %d", execs, len(calls))
	}
	if maxRun != 1 {
		t.Fatalf("mutating calls overlapped: max concurrency %d", maxRun)
	}
	if ids := toolMessageIDs(sess); len(ids) != len(calls) {
		t.Fatalf("tool messages = %v, want %d", ids, len(calls))
	}
}
