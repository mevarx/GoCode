// The eval scenarios live here; the package documentation is in doc.go.

package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
	"github.com/mevarx/GoCode/internal/tools"
)

// scriptedProvider returns one queued chunk slice per Stream call and records
// the history it was given, so a test can assert what the model saw.
type scriptedProvider struct {
	mu      sync.Mutex
	scripts [][]provider.StreamChunk
	calls   [][]provider.Message
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Models(context.Context) ([]string, error) {
	return []string{"test-model"}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	snapshot := make([]provider.Message, len(history))
	copy(snapshot, history)
	p.calls = append(p.calls, snapshot)

	var chunks []provider.StreamChunk
	if len(p.scripts) > 0 {
		chunks = p.scripts[0]
		p.scripts = p.scripts[1:]
	} else {
		// A turn that keeps calling without exhausting the script ends here
		// instead of looping forever.
		chunks = []provider.StreamChunk{{Delta: "done"}, {Done: true}}
	}
	p.mu.Unlock()

	ch := make(chan provider.StreamChunk, len(chunks))
	go func() {
		defer close(ch)
		for _, chunk := range chunks {
			ch <- chunk
		}
	}()
	return ch, nil
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *scriptedProvider) historyForCall(i int) []provider.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.calls) {
		return nil
	}
	return p.calls[i]
}

// fakeTool is a scripted tool: it records the arguments Execute received.
type fakeTool struct {
	name     string
	requires bool
	output   string

	mu   sync.Mutex
	args []json.RawMessage
}

func (f *fakeTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        f.name,
		Description: "fake " + f.name,
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

func (f *fakeTool) Execute(_ context.Context, args json.RawMessage) (tools.Result, error) {
	f.mu.Lock()
	f.args = append(f.args, append(json.RawMessage(nil), args...))
	f.mu.Unlock()
	return tools.Result{Output: f.output}, nil
}

func (f *fakeTool) RequiresApproval() bool { return f.requires }

func (f *fakeTool) executions() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.args...)
}

// newLoop builds a real AgentLoop over the scripted provider.
func newLoop(t *testing.T, p provider.Provider, gate *tools.ApprovalGate, toolList ...tools.Tool) (*agent.AgentLoop, *agent.Session) {
	t.Helper()

	sess := agent.NewSession("test-model")
	sess.AddMessage(provider.Message{Role: "system", Content: "system prompt"})

	providers := provider.NewRegistry()
	providers.Register(p)

	toolReg := tools.NewRegistry()
	for _, tool := range toolList {
		if err := toolReg.Register(tool); err != nil {
			t.Fatalf("register tool %s: %v", tool.Spec().Name, err)
		}
	}
	if gate == nil {
		gate = tools.NewApprovalGate()
	}

	loop := agent.NewAgentLoop(providers, sess, toolReg, gate)
	loop.WorkspaceRoot = t.TempDir()
	return loop, sess
}

// runTurn collects the turn's events while running it.
func runTurn(t *testing.T, loop *agent.AgentLoop, input string) []agent.LoopEvent {
	t.Helper()

	var events []agent.LoopEvent
	loop.Observe = func(ev agent.LoopEvent) { events = append(events, ev) }
	if err := loop.RunTurn(context.Background(), input); err != nil {
		t.Fatalf("RunTurn(%q) returned error: %v", input, err)
	}
	return events
}

func lastToolMessage(history []provider.Message) (provider.Message, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "tool" {
			return history[i], true
		}
	}
	return provider.Message{}, false
}

// Scenario 1: the model asks for a read-only tool; the call executes with the
// exact arguments and its result is in history before the next provider call.
func TestToolSelectionExecutesAndAppendsResult(t *testing.T) {
	read := &fakeTool{name: "file_read", output: "package main\n"}

	p := &scriptedProvider{scripts: [][]provider.StreamChunk{
		{{ToolCalls: []provider.ToolCall{{
			ID:   "call-1",
			Name: "file_read",
			Args: json.RawMessage(`{"path":"main.go","offset":1,"limit":40}`),
		}}, Done: true}},
		{{Delta: "the file declares package main"}, {Done: true}},
	}}

	loop, sess := newLoop(t, p, nil, read)
	events := runTurn(t, loop, "what is in main.go?")

	executed := read.executions()
	if len(executed) != 1 {
		t.Fatalf("file_read executed %d times, want 1", len(executed))
	}
	if string(executed[0]) != `{"path":"main.go","offset":1,"limit":40}` {
		t.Errorf("tool received args %s", executed[0])
	}

	if p.callCount() != 2 {
		t.Fatalf("provider called %d times, want 2", p.callCount())
	}
	second := p.historyForCall(1)

	// The assistant tool-call message and its result must both precede the
	// second request, result last.
	if len(second) < 2 {
		t.Fatalf("second provider call history too short: %d messages", len(second))
	}
	assistantIdx := -1
	for i, msg := range second {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 1 {
			assistantIdx = i
			if msg.ToolCalls[0].ID != "call-1" || msg.ToolCalls[0].Name != "file_read" {
				t.Errorf("assistant tool call = %+v", msg.ToolCalls[0])
			}
		}
	}
	if assistantIdx < 0 {
		t.Fatal("assistant tool call missing from second provider history")
	}
	result, ok := lastToolMessage(second)
	if !ok {
		t.Fatal("tool result missing from second provider history")
	}
	if result.ToolCallID != "call-1" {
		t.Errorf("tool result call id = %q, want call-1", result.ToolCallID)
	}
	if !strings.Contains(result.Content, "package main") {
		t.Errorf("tool result content = %q", result.Content)
	}

	// The session transcript carries the same pair, in order.
	history := sess.History()
	var sawCall, sawResult bool
	for _, msg := range history {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 1 {
			sawCall = true
		}
		if msg.Role == "tool" && msg.ToolCallID == "call-1" {
			sawResult = sawCall
		}
	}
	if !sawResult {
		t.Errorf("tool result did not follow its call in history: %+v", history)
	}
	if len(events) == 0 {
		t.Fatal("loop emitted no events")
	}
}

// Scenario 2: a stream stops at the token limit after partial text; the partial
// answer is persisted and the user sees a truncation notice.
func TestTruncationPersistsPartialContent(t *testing.T) {
	p := &scriptedProvider{scripts: [][]provider.StreamChunk{{
		{Delta: "partial answer"},
		{Done: true, FinishReason: "length"},
	}}}

	loop, sess := newLoop(t, p, nil)
	events := runTurn(t, loop, "explain everything")

	var assistant string
	for _, msg := range sess.History() {
		if msg.Role == "assistant" {
			assistant = msg.Content
		}
	}
	if assistant != "partial answer" {
		t.Errorf("persisted assistant content = %q, want %q", assistant, "partial answer")
	}

	notices := 0
	for _, ev := range events {
		if ev.Kind == agent.EventNotice && strings.Contains(ev.Text, "token limit") {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("token-limit notices = %d, want 1", notices)
	}
}

// Scenario 3: a mutating tool under a denying gate never executes, and the
// denial lands in history as its result.
func TestApprovalGatingDeniesMutatingTool(t *testing.T) {
	mutate := &fakeTool{name: "file_write", requires: true, output: "written"}

	gate := tools.NewApprovalGate()
	gate.OnPresentChoice = func(string, json.RawMessage, string) (tools.ApprovalChoice, error) {
		return tools.ApprovalDeny, nil
	}

	p := &scriptedProvider{scripts: [][]provider.StreamChunk{
		{{ToolCalls: []provider.ToolCall{{
			ID:   "call-2",
			Name: "file_write",
			Args: json.RawMessage(`{"path":"out.txt","content":"x"}`),
		}}, Done: true}},
		{{Delta: "understood"}, {Done: true}},
	}}

	loop, sess := newLoop(t, p, gate, mutate)
	runTurn(t, loop, "write out.txt")

	if got := len(mutate.executions()); got != 0 {
		t.Fatalf("denied tool executed %d times, want 0", got)
	}

	result, ok := lastToolMessage(sess.History())
	if !ok {
		t.Fatal("denial result was not appended to history")
	}
	if !strings.Contains(result.Content, "User denied execution of file_write") {
		t.Errorf("history denial result = %q", result.Content)
	}
}

// Scenario 4: compaction replaces omitted turns with a summary while keeping
// the retained turns and the tool-call/tool-result pairing invariant.
func TestCompactionKeepsSummaryRetainedTurnsAndPairing(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "turn one"},
		{Role: "assistant", Content: "one done"},
		{Role: "user", Content: "turn two"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{
			ID:   "call-3",
			Name: "file_read",
			Args: json.RawMessage(`{"path":"a.txt"}`),
		}}},
		{Role: "tool", ToolCallID: "call-3", Content: "file body"},
		{Role: "user", Content: "turn three"},
		{Role: "assistant", Content: "three done"},
	}

	// Tiny budget documents intent; Compact bounds by turns, not tokens.
	cm := agent.NewContextManager(64)
	out := cm.Compact(history, 2)

	if len(out) == 0 || out[0].Role != "system" || out[0].Content != "system prompt" {
		t.Fatalf("system prompt not preserved: %+v", out)
	}
	if !strings.Contains(out[1].Content, "1 turns") || !strings.Contains(out[1].Content, "2 messages") {
		t.Errorf("summary placeholder = %q", out[1].Content)
	}

	joined := make([]string, 0, len(out))
	for _, msg := range out {
		joined = append(joined, msg.Content)
	}
	text := strings.Join(joined, "\n")
	if strings.Contains(text, "one done") {
		t.Error("omitted turn survived compaction")
	}
	if !strings.Contains(text, "three done") {
		t.Error("retained turn missing from compaction result")
	}

	// The call from the retained turn keeps its result immediately after it.
	paired := false
	for i, msg := range out {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			if i+1 >= len(out) || out[i+1].Role != "tool" || out[i+1].ToolCallID != msg.ToolCalls[0].ID {
				t.Errorf("tool call at %d is not followed by its result: %+v", i, out)
			}
			paired = true
		}
	}
	if !paired {
		t.Fatal("retained tool call was dropped by compaction")
	}

	// C2 invariant: the compacted window must already satisfy the wire pairing.
	if !reflect.DeepEqual(out, agent.EnsureToolPairing(out)) {
		t.Errorf("compacted history violates EnsureToolPairing:\n got %+v\nwant %+v",
			out, agent.EnsureToolPairing(out))
	}

	if got := cm.Compactions.Load(); got != 1 {
		t.Errorf("Compactions = %d, want 1", got)
	}
}

// Scenario 5: a transcript recorded for workspace A is never selected for
// workspace B, so one repository's messages cannot leak into another's turn.
func TestResumeIsScopedToWorkspace(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	defer store.Close()

	workspaceA := filepath.Join(dir, "repo-a")
	workspaceB := filepath.Join(dir, "repo-b")

	recA, err := store.CreateSessionWithWorkspace("session-a", "A", "prov", "model", workspaceA)
	if err != nil {
		t.Fatalf("create session A: %v", err)
	}
	if err := store.AppendMessage(recA.ID, provider.Message{Role: "user", Content: "secret A task"}); err != nil {
		t.Fatalf("append A message: %v", err)
	}

	// Workspace B must not resume A's transcript.
	resumedB, err := store.GetLastSessionForWorkspace(workspaceB)
	if err != nil {
		t.Fatalf("lookup workspace B: %v", err)
	}
	if resumedB != nil {
		t.Fatalf("workspace B resumed session %q from workspace A", resumedB.ID)
	}

	// A fresh B session carries only its own messages.
	recB, err := store.CreateSessionWithWorkspace("session-b", "B", "prov", "model", workspaceB)
	if err != nil {
		t.Fatalf("create session B: %v", err)
	}
	if err := store.AppendMessage(recB.ID, provider.Message{Role: "user", Content: "B task"}); err != nil {
		t.Fatalf("append B message: %v", err)
	}

	loadedB, err := store.GetSession(recB.ID)
	if err != nil {
		t.Fatalf("load session B: %v", err)
	}
	if len(loadedB.Messages) != 1 || loadedB.Messages[0].Content != "B task" {
		t.Errorf("workspace B transcript = %+v", loadedB.Messages)
	}

	// B now becomes the last session for B, while A still resolves for A.
	lastB, err := store.GetLastSessionForWorkspace(workspaceB)
	if err != nil {
		t.Fatalf("lookup workspace B: %v", err)
	}
	if lastB == nil || lastB.ID != recB.ID {
		t.Errorf("workspace B last session = %+v, want %s", lastB, recB.ID)
	}

	lastA, err := store.GetLastSessionForWorkspace(workspaceA)
	if err != nil {
		t.Fatalf("lookup workspace A: %v", err)
	}
	if lastA == nil || lastA.ID != recA.ID {
		t.Fatalf("workspace A last session = %+v, want %s", lastA, recA.ID)
	}
	if len(lastA.Messages) != 1 || lastA.Messages[0].Content != "secret A task" {
		t.Errorf("workspace A transcript = %+v", lastA.Messages)
	}
}
