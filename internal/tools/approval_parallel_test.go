package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// preparedTool is a fake for batch preparation tests. The tests drive it from
// one goroutine, so its counters need no lock.
type preparedTool struct {
	name     string
	requires bool
	readOnly bool
	execs    int
	output   string
}

func (t *preparedTool) Spec() ToolSpec { return ToolSpec{Name: t.name} }

func (t *preparedTool) RequiresApproval() bool { return t.requires }

func (t *preparedTool) ReadOnly() bool { return t.readOnly }

func (t *preparedTool) Execute(context.Context, json.RawMessage) (Result, error) {
	t.execs++
	return Result{Output: t.output}, nil
}

// Prepare must complete every prompt before any tool runs, and the returned
// closures must still carry the approved invocation.
func TestPreparePromptsBeforeAnyExecution(t *testing.T) {
	gate := NewApprovalGate()
	first := &preparedTool{name: "first", requires: true}
	second := &preparedTool{name: "second", requires: true}

	var prompts []string
	gate.OnPresentChoice = func(toolName string, _ json.RawMessage, _ string) (ApprovalChoice, error) {
		// Both prompts must happen while neither tool has executed.
		if first.execs != 0 || second.execs != 0 {
			t.Errorf("prompt for %s arrived after an execution", toolName)
		}
		prompts = append(prompts, toolName)
		return ApprovalOnce, nil
	}

	ctx := context.Background()
	runFirst, err := gate.Prepare(ctx, first, json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("Prepare(first): %v", err)
	}
	runSecond, err := gate.Prepare(ctx, second, json.RawMessage(`{"n":2}`))
	if err != nil {
		t.Fatalf("Prepare(second): %v", err)
	}
	if strings.Join(prompts, ",") != "first,second" {
		t.Fatalf("prompt order = %v, want [first second]", prompts)
	}
	if first.execs != 0 || second.execs != 0 {
		t.Fatal("Prepare executed a tool")
	}

	if res, err := runFirst(ctx); err != nil {
		t.Fatalf("first closure = (%+v, %v)", res, err)
	}
	if res, err := runSecond(ctx); err != nil {
		t.Fatalf("second closure = (%+v, %v)", res, err)
	}
	if first.execs != 1 || second.execs != 1 {
		t.Fatalf("executions = (%d, %d), want (1, 1)", first.execs, second.execs)
	}
}

// A denial is decided at prepare time but only reported when the closure runs;
// it must never reach Execute, even if the caller still invokes the closure.
func TestPrepareDenialNeverExecutes(t *testing.T) {
	gate := NewApprovalGate()
	deniedTool := &preparedTool{name: "deny_me", requires: true}
	okTool := &preparedTool{name: "run_me", requires: true}
	gate.OnPresentChoice = func(toolName string, _ json.RawMessage, _ string) (ApprovalChoice, error) {
		if toolName == "deny_me" {
			return ApprovalDeny, nil
		}
		return ApprovalOnce, nil
	}

	ctx := context.Background()
	denyRun, err := gate.Prepare(ctx, deniedTool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Prepare(denied): %v", err)
	}
	okRun, err := gate.Prepare(ctx, okTool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Prepare(ok): %v", err)
	}

	res, err := denyRun(ctx)
	if err != nil {
		t.Fatalf("denied closure error: %v", err)
	}
	if deniedTool.execs != 0 {
		t.Fatalf("denied tool executed %d times", deniedTool.execs)
	}
	if !strings.Contains(res.Output, "User denied execution of deny_me") {
		t.Fatalf("denial result = %q", res.Output)
	}

	if _, err := okRun(ctx); err != nil {
		t.Fatalf("approved closure error: %v", err)
	}
	if okTool.execs != 1 {
		t.Fatalf("approved tool executions = %d, want 1", okTool.execs)
	}
}

// The read-only turn flag set while preparing one call must apply to later
// calls in the same batch, exactly as when WrapExecution ran them in order.
func TestPrepareReadOnlyTurnAppliesToLaterCalls(t *testing.T) {
	gate := NewApprovalGate()
	reader := &preparedTool{name: "reader", requires: true, readOnly: true}
	writer := &preparedTool{name: "writer", requires: true}

	prompts := 0
	// Even with the flag set, a mutating tool still prompts; choosing
	// read-only again denies it.
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		prompts++
		return ApprovalReadOnlyTurn, nil
	}

	ctx := context.Background()
	runReader, err := gate.Prepare(ctx, reader, json.RawMessage(`{"q":"a"}`))
	if err != nil {
		t.Fatalf("Prepare(reader): %v", err)
	}
	if _, err := runReader(ctx); err != nil {
		t.Fatalf("reader closure: %v", err)
	}
	if reader.execs != 1 {
		t.Fatalf("reader executions = %d, want 1", reader.execs)
	}

	// Same read-only tool, new args: the per-turn flag covers it without a prompt.
	runReader2, err := gate.Prepare(ctx, reader, json.RawMessage(`{"q":"b"}`))
	if err != nil {
		t.Fatalf("Prepare(reader 2): %v", err)
	}
	if _, err := runReader2(ctx); err != nil {
		t.Fatalf("reader 2 closure: %v", err)
	}
	if prompts != 1 {
		t.Fatalf("read-only flag did not suppress prompt: %d prompts", prompts)
	}

	// A mutating tool is still gated: it must deny rather than inherit the flag.
	runWriter, err := gate.Prepare(ctx, writer, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Prepare(writer): %v", err)
	}
	res, err := runWriter(ctx)
	if err != nil {
		t.Fatalf("writer closure: %v", err)
	}
	if writer.execs != 0 {
		t.Fatalf("mutating tool ran under read-only-turn flag: %d executions", writer.execs)
	}
	if !strings.Contains(res.Output, "only read-only tools are allowed this turn") {
		t.Fatalf("writer result = %q", res.Output)
	}
}

// Prepare deliberately returns raw results; the transcript boundary is applied
// by WrapExecution or by the caller. This guards against future changes that
// silently drop redaction from either path.
func TestPrepareAndRedactBoundary(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	gate := NewApprovalGate()
	tool := &preparedTool{name: "leaky", output: "token=" + secret}

	run, err := gate.Prepare(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	raw, err := run(context.Background())
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	if !strings.Contains(raw.Output, secret) {
		t.Fatalf("Prepare should not redact; got %q", raw.Output)
	}

	redacted, _ := RedactToolResult(raw, nil)
	if strings.Contains(redacted.Output, secret) {
		t.Fatalf("RedactToolResult leaked the secret: %q", redacted.Output)
	}

	wrapped, err := gate.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("WrapExecution: %v", err)
	}
	if strings.Contains(wrapped.Output, secret) {
		t.Fatalf("WrapExecution leaked the secret: %q", wrapped.Output)
	}
}
