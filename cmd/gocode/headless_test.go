package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// headlessScriptProvider replays one queued chunk slice per Stream call, so a
// test can shape a whole turn without a network provider.
type headlessScriptProvider struct {
	mu      sync.Mutex
	scripts [][]provider.StreamChunk
}

func (p *headlessScriptProvider) Name() string { return "headless-script" }

func (p *headlessScriptProvider) Models(context.Context) ([]string, error) {
	return []string{"test-model"}, nil
}

func (p *headlessScriptProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	var chunks []provider.StreamChunk
	if len(p.scripts) > 0 {
		chunks = p.scripts[0]
		p.scripts = p.scripts[1:]
	} else {
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

// headlessFakeTool records every execution so a test can prove a denied call
// never reached Execute.
type headlessFakeTool struct {
	name     string
	requires bool
	output   string

	mu   sync.Mutex
	args []json.RawMessage
}

func (f *headlessFakeTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        f.name,
		Description: "fake " + f.name,
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

func (f *headlessFakeTool) Execute(_ context.Context, args json.RawMessage) (tools.Result, error) {
	f.mu.Lock()
	f.args = append(f.args, append(json.RawMessage(nil), args...))
	f.mu.Unlock()
	return tools.Result{Output: f.output}, nil
}

func (f *headlessFakeTool) RequiresApproval() bool { return f.requires }

func (f *headlessFakeTool) executedArgs() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.args...)
}

// newHeadlessLoop builds a real AgentLoop over a scripted provider and the
// given approval gate.
func newHeadlessLoop(t *testing.T, p provider.Provider, gate *tools.ApprovalGate, toolList ...tools.Tool) *agent.AgentLoop {
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
	return loop
}

// decodeHeadlessRecords parses stdout as strict JSONL: anything that is not
// one JSON object per line is a protocol violation.
func decodeHeadlessRecords(t *testing.T, raw string) []map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	records := make([]map[string]any, 0, len(lines))
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("stdout line %d is not JSON: %v (%q)", i+1, err, line)
		}
		records = append(records, rec)
	}
	return records
}

func countRecords(records []map[string]any, kind string) int {
	n := 0
	for _, rec := range records {
		if rec["type"] == kind {
			n++
		}
	}
	return n
}

// A headless turn streams protocol records only: delta and turn_done, with no
// banner or prompt text mixed into stdout.
func TestRunHeadlessStreamsProtocolOnly(t *testing.T) {
	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{{
		{Delta: "hello "},
		{Delta: "world"},
		{Done: true},
	}}}
	loop := newHeadlessLoop(t, p, nil)

	input := strings.Join([]string{
		`{"type":"user","text":"hi"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless returned error: %v", err)
	}

	raw := out.String()
	if strings.Contains(raw, "GoCode — Terminal Coding Agent") || strings.Contains(raw, "> ") {
		t.Fatalf("stdout contains human UI text: %q", raw)
	}

	records := decodeHeadlessRecords(t, raw)
	if got := countRecords(records, "delta"); got != 2 {
		t.Errorf("expected 2 delta records, got %d (%q)", got, raw)
	}
	if got := countRecords(records, "turn_done"); got != 1 {
		t.Errorf("expected 1 turn_done record, got %d (%q)", got, raw)
	}

	var text strings.Builder
	for _, rec := range records {
		if rec["type"] == "delta" {
			text.WriteString(rec["text"].(string))
		}
	}
	if text.String() != "hello world" {
		t.Errorf("delta text = %q, want %q", text.String(), "hello world")
	}
}

// EOF without an explicit exit record is a clean shutdown.
func TestRunHeadlessEOFEndsCleanly(t *testing.T) {
	p := &headlessScriptProvider{}
	loop := newHeadlessLoop(t, p, nil)

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(""), &out, loop); err != nil {
		t.Fatalf("EOF must end cleanly, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for empty input, got %q", out.String())
	}
}

// Slash commands are turns too: their output is a notice and the turn still
// closes with turn_done, without any provider call.
func TestRunHeadlessSlashCommandEmitsNoticeAndTurnDone(t *testing.T) {
	p := &headlessScriptProvider{}
	loop := newHeadlessLoop(t, p, nil)

	input := strings.Join([]string{
		`{"type":"user","text":"/help"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless returned error: %v", err)
	}

	records := decodeHeadlessRecords(t, out.String())
	if got := countRecords(records, "notice"); got != 1 {
		t.Errorf("expected one notice record, got %d (%q)", got, out.String())
	}
	if got := countRecords(records, "turn_done"); got != 1 {
		t.Errorf("expected one turn_done record, got %d (%q)", got, out.String())
	}
}

// An unknown request type is a protocol error: the driver gets an error record
// and a non-zero exit.
func TestRunHeadlessUnknownInputTypeFails(t *testing.T) {
	p := &headlessScriptProvider{}
	loop := newHeadlessLoop(t, p, nil)

	var out bytes.Buffer
	err := runHeadless(context.Background(), strings.NewReader(`{"type":"bogus"}`+"\n"), &out, loop)
	if err == nil {
		t.Fatal("unknown input type must return an error so the process exits non-zero")
	}

	records := decodeHeadlessRecords(t, out.String())
	if got := countRecords(records, "error"); got != 1 {
		t.Fatalf("expected one error record, got %d (%q)", got, out.String())
	}
	if !strings.Contains(records[0]["text"].(string), "unknown input type") {
		t.Errorf("unexpected error text: %q", records[0]["text"])
	}
}

func TestRunHeadlessInvalidInputJSONFails(t *testing.T) {
	p := &headlessScriptProvider{}
	loop := newHeadlessLoop(t, p, nil)

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader("{not json}\n"), &out, loop); err == nil {
		t.Fatal("malformed request JSON must return an error")
	}
	if got := countRecords(decodeHeadlessRecords(t, out.String()), "error"); got != 1 {
		t.Errorf("expected one error record, got %d", got)
	}
}

// The policy allowlist decides what runs: an allowlisted tool executes, a
// mutating tool outside the allowlist is denied before Execute.
func TestRunHeadlessPolicyDeniesNonAllowlistedTool(t *testing.T) {
	read := &headlessFakeTool{name: "file_read", requires: true, output: "file body"}
	mutate := &headlessFakeTool{name: "file_write", requires: true, output: "written"}

	p := &headlessScriptProvider{scripts: [][]provider.StreamChunk{
		{{ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "file_read", Args: json.RawMessage(`{"path":"a.txt"}`)},
			{ID: "c2", Name: "file_write", Args: json.RawMessage(`{"path":"a.txt"}`)},
		}}},
		{{Delta: "done"}, {Done: true}},
	}}

	policy := headlessPolicy{AutoApprove: []string{"file_read"}}
	loop := newHeadlessLoop(t, p, newHeadlessApprovalGate(policy), read, mutate)

	input := strings.Join([]string{
		`{"type":"user","text":"read then write"}`,
		`{"type":"exit"}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := runHeadless(context.Background(), strings.NewReader(input), &out, loop); err != nil {
		t.Fatalf("runHeadless returned error: %v", err)
	}

	if got := len(read.executedArgs()); got != 1 {
		t.Errorf("allowlisted read-only tool ran %d times, want 1", got)
	} else if string(read.executedArgs()[0]) != `{"path":"a.txt"}` {
		t.Errorf("read tool executed with args %s", read.executedArgs()[0])
	}
	if got := len(mutate.executedArgs()); got != 0 {
		t.Errorf("non-allowlisted mutating tool executed %d times, want 0", got)
	}

	// The denial is recorded as the tool's result, so the model can see it.
	var denied bool
	for _, msg := range loop.Session.History() {
		if msg.Role == "tool" && strings.Contains(msg.Content, "denied") {
			denied = true
		}
	}
	if !denied {
		t.Error("denial result was not recorded in history")
	}
}

// A tool omitted from the policy never prompts: headless has no human, so the
// gate hook itself must deny.
func TestHeadlessApprovalGateNeverPrompts(t *testing.T) {
	gate := newHeadlessApprovalGate(headlessPolicy{})

	if gate.IsAutoApproved("file_write") {
		t.Error("empty policy must not auto-approve anything")
	}
	choice, err := gate.OnPresentChoice("file_write", json.RawMessage(`{}`), "")
	if err != nil {
		t.Fatalf("OnPresentChoice returned error: %v", err)
	}
	if choice != tools.ApprovalDeny {
		t.Errorf("OnPresentChoice = %v, want ApprovalDeny", choice)
	}

	// Deny wins even when the same tool is allowlisted.
	both := newHeadlessApprovalGate(headlessPolicy{
		AutoApprove: []string{"shell_exec"},
		Deny:        []string{"shell_exec"},
	})
	tool := &headlessFakeTool{name: "shell_exec", requires: true}
	execute, err := both.Prepare(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	result, err := execute(context.Background())
	if err != nil {
		t.Fatalf("denied execution returned error: %v", err)
	}
	if !strings.Contains(result.Error, "denied") {
		t.Errorf("denial result = %+v", result)
	}
	if got := len(tool.executedArgs()); got != 0 {
		t.Errorf("denied tool executed %d times, want 0", got)
	}
}

func TestLoadHeadlessPolicy(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(valid, []byte(`{"auto_approve":["file_read","code_search"],"deny":["shell_exec"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := loadHeadlessPolicy(valid)
	if err != nil {
		t.Fatalf("valid policy failed: %v", err)
	}
	if len(policy.AutoApprove) != 2 || policy.AutoApprove[0] != "file_read" {
		t.Errorf("auto_approve = %v", policy.AutoApprove)
	}
	if len(policy.Deny) != 1 || policy.Deny[0] != "shell_exec" {
		t.Errorf("deny = %v", policy.Deny)
	}

	if empty, err := loadHeadlessPolicy(""); err != nil || len(empty.AutoApprove) != 0 {
		t.Errorf("empty path must mean deny-all, got %+v, %v", empty, err)
	}

	blank := filepath.Join(dir, "blank.json")
	if err := os.WriteFile(blank, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if blankPolicy, err := loadHeadlessPolicy(blank); err != nil || len(blankPolicy.Deny) != 0 {
		t.Errorf("blank policy file must mean deny-all, got %+v, %v", blankPolicy, err)
	}

	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"auto_approve":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHeadlessPolicy(invalid); err == nil {
		t.Error("malformed policy must be a startup error")
	}

	if _, err := loadHeadlessPolicy(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing policy file must be a startup error")
	}
}
