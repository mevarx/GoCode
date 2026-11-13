package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// verifyProvider answers each provider round with the tool calls configured
// for that round, then plain text once the rounds are exhausted. It records
// every history it receives so tests can see what the next round observed.
type verifyProvider struct {
	mu        sync.Mutex
	rounds    [][]provider.ToolCall
	histories [][]provider.Message
}

func (p *verifyProvider) Name() string { return "verify-fake" }

func (p *verifyProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *verifyProvider) Stream(ctx context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	snapshot := make([]provider.Message, len(history))
	copy(snapshot, history)
	p.histories = append(p.histories, snapshot)
	round := len(p.histories) - 1
	var calls []provider.ToolCall
	if round < len(p.rounds) {
		calls = append(calls, p.rounds[round]...)
	}
	p.mu.Unlock()

	ch := make(chan provider.StreamChunk, 2)
	go func() {
		defer close(ch)
		if len(calls) > 0 {
			ch <- provider.StreamChunk{ToolCalls: calls}
			return
		}
		ch <- provider.StreamChunk{Delta: "done", Done: true}
	}()
	return ch, nil
}

func (p *verifyProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.histories)
}

func (p *verifyProvider) lastHistory() []provider.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.histories) == 0 {
		return nil
	}
	return p.histories[len(p.histories)-1]
}

// verifyFakeTool is a workspace-mutating fake unless readOnly is set.
type verifyFakeTool struct {
	name     string
	readOnly bool
	ran      int32
}

func (t *verifyFakeTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        t.name,
		Description: "test tool",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

func (t *verifyFakeTool) RequiresApproval() bool { return false }

func (t *verifyFakeTool) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	atomic.AddInt32(&t.ran, 1)
	return tools.Result{Output: "ok"}, nil
}

func (t *verifyFakeTool) ReadOnly() bool { return t.readOnly }

func (t *verifyFakeTool) executions() int { return int(atomic.LoadInt32(&t.ran)) }

func newVerifyEngine(p provider.Provider, reg *tools.Registry) (*AgentLoop, *[]LoopEvent) {
	preg := provider.NewRegistry()
	preg.Register(p)
	loop := NewAgentLoop(preg, NewSession("m"), reg, tools.NewApprovalGate())
	events := &[]LoopEvent{}
	loop.Observe = func(ev LoopEvent) { *events = append(*events, ev) }
	return loop, events
}

func failingVerifyCommand() string {
	if runtime.GOOS == "windows" {
		return "echo VERIFY_FAILED & exit /b 1"
	}
	return "echo VERIFY_FAILED; exit 1"
}

func succeedingVerifyCommand() string {
	if runtime.GOOS == "windows" {
		return "exit /b 0"
	}
	return "true"
}

func autoVerifyNotices(events []LoopEvent) []string {
	var notices []string
	for _, ev := range events {
		if ev.Kind == EventNotice && strings.Contains(ev.Text, "[auto-verify]") {
			notices = append(notices, ev.Text)
		}
	}
	return notices
}

func autoVerifyUserMessages(loop *AgentLoop) []string {
	var messages []string
	for _, msg := range loop.Session.History() {
		if msg.Role == "user" && strings.Contains(msg.Content, "[auto-verify]") {
			messages = append(messages, msg.Content)
		}
	}
	return messages
}

func TestAutoVerifyFailureNotifiesAndContinues(t *testing.T) {
	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "fake_edit", Args: json.RawMessage(`{}`)},
	}}}
	reg := tools.NewRegistry()
	tool := &verifyFakeTool{name: "fake_edit"}
	reg.Register(tool)

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = failingVerifyCommand()

	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if tool.executions() != 1 {
		t.Fatalf("expected the fake tool to run once, ran %d times", tool.executions())
	}
	if p.callCount() < 2 {
		t.Fatalf("loop did not continue after verify failure: %d provider calls", p.callCount())
	}

	notices := autoVerifyNotices(*events)
	if len(notices) != 1 {
		t.Fatalf("expected exactly one auto-verify notice, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "failed (exit 1)") {
		t.Fatalf("failure notice missing exit code: %q", notices[0])
	}
	if !strings.Contains(notices[0], "VERIFY_FAILED") {
		t.Fatalf("failure notice missing command output: %q", notices[0])
	}

	messages := autoVerifyUserMessages(loop)
	if len(messages) != 1 {
		t.Fatalf("expected one auto-verify user message, got %d: %v", len(messages), messages)
	}
	if !strings.Contains(messages[0], "[auto-verify] Command failed") || !strings.Contains(messages[0], "VERIFY_FAILED") {
		t.Fatalf("user message missing failure details: %q", messages[0])
	}

	sawInNextRound := false
	for _, msg := range p.lastHistory() {
		if msg.Role == "user" && strings.Contains(msg.Content, "[auto-verify] Command failed") {
			sawInNextRound = true
		}
	}
	if !sawInNextRound {
		t.Fatal("next provider round did not see the verify failure message")
	}
}

func TestAutoVerifySuccessNotifiesWithoutFailureMessage(t *testing.T) {
	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "fake_edit", Args: json.RawMessage(`{}`)},
	}}}
	reg := tools.NewRegistry()
	reg.Register(&verifyFakeTool{name: "fake_edit"})

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = succeedingVerifyCommand()

	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	notices := autoVerifyNotices(*events)
	if len(notices) != 1 || !strings.Contains(notices[0], "passed") {
		t.Fatalf("expected one success notice, got %v", notices)
	}
	if messages := autoVerifyUserMessages(loop); len(messages) != 0 {
		t.Fatalf("success must not add a failure message, got %v", messages)
	}
}

func TestAutoVerifySkipsReadOnlyOnlyRound(t *testing.T) {
	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "fake_view", Args: json.RawMessage(`{}`)},
	}}}
	reg := tools.NewRegistry()
	reg.Register(&verifyFakeTool{name: "fake_view", readOnly: true})

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = failingVerifyCommand()

	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if notices := autoVerifyNotices(*events); len(notices) != 0 {
		t.Fatalf("read-only round ran verify: %v", notices)
	}
	if messages := autoVerifyUserMessages(loop); len(messages) != 0 {
		t.Fatalf("read-only round added verify output: %v", messages)
	}
}

func TestAutoVerifySkipsPlanTool(t *testing.T) {
	store := tools.NewPlanStore()
	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "plan", Args: json.RawMessage(`{"items":[{"id":"1","text":"Step","status":"pending"}]}`)},
	}}}
	reg := tools.NewRegistry()
	reg.Register(&tools.PlanTool{Store: store})

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = failingVerifyCommand()

	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if len(store.Items()) != 1 {
		t.Fatal("plan tool did not execute")
	}
	if notices := autoVerifyNotices(*events); len(notices) != 0 {
		t.Fatalf("plan round ran verify: %v", notices)
	}
}

func TestAutoVerifyEmptyCommandNeverRuns(t *testing.T) {
	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "fake_edit", Args: json.RawMessage(`{}`)},
	}}}
	reg := tools.NewRegistry()
	reg.Register(&verifyFakeTool{name: "fake_edit"})

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = ""

	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if notices := autoVerifyNotices(*events); len(notices) != 0 {
		t.Fatalf("empty verify command ran: %v", notices)
	}
	if messages := autoVerifyUserMessages(loop); len(messages) != 0 {
		t.Fatalf("empty verify command added messages: %v", messages)
	}
}

func TestAutoVerifyRunsOncePerRound(t *testing.T) {
	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "fake_edit", Args: json.RawMessage(`{}`)},
		{ID: "c2", Name: "fake_write", Args: json.RawMessage(`{}`)},
	}}}
	reg := tools.NewRegistry()
	reg.Register(&verifyFakeTool{name: "fake_edit"})
	reg.Register(&verifyFakeTool{name: "fake_write"})

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = succeedingVerifyCommand()

	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if notices := autoVerifyNotices(*events); len(notices) != 1 {
		t.Fatalf("expected one verify run per round, got %d: %v", len(notices), notices)
	}
}

func TestAutoVerifyTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cmd.exe leaves the spawned child holding the output pipe; timeout kill semantics differ")
	}

	p := &verifyProvider{rounds: [][]provider.ToolCall{{
		{ID: "c1", Name: "fake_edit", Args: json.RawMessage(`{}`)},
	}}}
	reg := tools.NewRegistry()
	reg.Register(&verifyFakeTool{name: "fake_edit"})

	loop, events := newVerifyEngine(p, reg)
	loop.VerifyCommand = "exec sleep 10"
	loop.VerifyTimeout = 200 * time.Millisecond

	start := time.Now()
	if err := loop.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("verify timeout did not kill the command: took %s", elapsed)
	}

	notices := autoVerifyNotices(*events)
	if len(notices) != 1 || !strings.Contains(notices[0], "timed out") {
		t.Fatalf("expected a timeout notice, got %v", notices)
	}
	messages := autoVerifyUserMessages(loop)
	if len(messages) != 1 || !strings.Contains(messages[0], "timed out") {
		t.Fatalf("expected a timeout user message, got %v", messages)
	}
}

func TestCappedBufferBoundsOutput(t *testing.T) {
	b := &cappedBuffer{limit: 8}
	n, err := b.Write([]byte("1234567890"))
	if err != nil || n != 10 {
		t.Fatalf("Write returned (%d, %v), want (10, nil)", n, err)
	}
	if got := b.String(); got != "12345678" {
		t.Fatalf("buffer not capped: %q", got)
	}
	if !b.truncated {
		t.Fatal("expected truncation flag")
	}
}
