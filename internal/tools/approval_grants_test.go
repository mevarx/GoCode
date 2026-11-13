package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// grantTool is a configurable fake covering the approval surfaces: requiring
// approval, opting into the read-only turn flag, and optional previews.
type grantTool struct {
	name             string
	requiresApproval bool
	readOnly         bool
	executions       int
	lastArgs         json.RawMessage
}

func (t *grantTool) Spec() ToolSpec { return ToolSpec{Name: t.name} }

func (t *grantTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	t.executions++
	t.lastArgs = append(json.RawMessage(nil), args...)
	return Result{Output: "executed " + t.name}, nil
}

func (t *grantTool) RequiresApproval() bool { return t.requiresApproval }

func (t *grantTool) ReadOnly() bool { return t.readOnly }

// grantPreviewTool renders the command from its arguments so tests can see
// which arguments the second (post-edit) preview described.
type grantPreviewTool struct {
	grantTool
}

func (t *grantPreviewTool) Preview(_ context.Context, args json.RawMessage) (Preview, error) {
	var parsed struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(args, &parsed)
	return Preview{Description: "run: " + parsed.Command}, nil
}

// TestExactArgsSessionGrantBypassesPrompt is the core C5 guarantee: "a"
// approves once and remembers the exact invocation; identical (canonically
// normalized) args never prompt again, while changed args do.
func TestExactArgsSessionGrantBypassesPrompt(t *testing.T) {
	gate := NewApprovalGate()
	// Answers are consumed in order: grant once, then deny the changed call.
	// The granted call in between must not read a line at all.
	gate.SetInputReader(bufio.NewReader(strings.NewReader("a\nn\n")))
	tool := &grantTool{name: "mutate", requiresApproval: true}
	ctx := context.Background()

	var firstOut string
	var first Result
	firstOut = captureStdout(t, func() {
		first, _ = gate.WrapExecution(ctx, tool, json.RawMessage(`{"command":"ls","path":"a"}`))
	})
	if tool.executions != 1 {
		t.Fatalf("first call executions = %d, want 1", tool.executions)
	}
	if strings.Contains(firstOut, "(session grant)") {
		t.Fatalf("first call should have prompted, got %q", firstOut)
	}
	if first.Output != "executed mutate" {
		t.Fatalf("first result = %q", first.Output)
	}

	// Same object, different key order: canonical normalization must match.
	var secondOut string
	var second Result
	secondOut = captureStdout(t, func() {
		second, _ = gate.WrapExecution(ctx, tool, json.RawMessage(`{"path":"a","command":"ls"}`))
	})
	if containsApprovalPrompt(secondOut) {
		t.Fatalf("identical canonical args prompted again: %q", secondOut)
	}
	if tool.executions != 2 {
		t.Fatalf("granted call executions = %d, want 2", tool.executions)
	}
	if !strings.Contains(second.Output, "(session grant)") {
		t.Fatalf("grant bypass not reported in result: %q", second.Output)
	}

	// Different args must prompt again and can be denied.
	var thirdOut string
	var third Result
	thirdOut = captureStdout(t, func() {
		third, _ = gate.WrapExecution(ctx, tool, json.RawMessage(`{"command":"ls","path":"b"}`))
	})
	if !containsApprovalPrompt(thirdOut) {
		t.Fatalf("changed args did not prompt: %q", thirdOut)
	}
	if tool.executions != 2 {
		t.Fatalf("denied call executed: executions = %d, want 2", tool.executions)
	}
	if !strings.Contains(third.Output, "denied") {
		t.Fatalf("changed-args denial not reported: %q", third.Output)
	}

	if got := gate.SessionGrantCount(); got != 1 {
		t.Fatalf("SessionGrantCount = %d, want 1", got)
	}
}

func containsApprovalPrompt(out string) bool {
	return strings.Contains(out, "Approve? [")
}

// TestParseApprovalResponse pins the documented keys; anything else denies.
func TestParseApprovalResponse(t *testing.T) {
	cases := map[string]ApprovalChoice{
		"y\n":       ApprovalOnce,
		" YES ":     ApprovalOnce,
		"a\n":       ApprovalGrantExact,
		"allow\n":   ApprovalGrantExact,
		"r\n":       ApprovalReadOnlyTurn,
		"read-only": ApprovalReadOnlyTurn,
		"e\n":       ApprovalEdit,
		"edit\n":    ApprovalEdit,
		"n\n":       ApprovalDeny,
		"\n":        ApprovalDeny,
		"garbage":   ApprovalDeny,
	}
	for input, want := range cases {
		if got := parseApprovalResponse(input); got != want {
			t.Errorf("parseApprovalResponse(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestSessionGrantsDieWithGate ensures grants are never carried across gates
// (a fresh process starts with no memory).
func TestSessionGrantsDieWithGate(t *testing.T) {
	first := NewApprovalGate()
	first.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		return ApprovalGrantExact, nil
	}
	tool := &grantTool{name: "mutate", requiresApproval: true}
	if _, err := first.WrapExecution(context.Background(), tool, json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatalf("first gate: %v", err)
	}
	if first.SessionGrantCount() != 1 {
		t.Fatalf("first gate grants = %d, want 1", first.SessionGrantCount())
	}

	second := NewApprovalGate()
	prompted := 0
	second.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		prompted++
		return ApprovalDeny, nil
	}
	res, err := second.WrapExecution(context.Background(), tool, json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("second gate: %v", err)
	}
	if prompted != 1 {
		t.Fatalf("new gate did not prompt: prompted = %d", prompted)
	}
	if !strings.Contains(res.Output, "denied") {
		t.Fatalf("new gate result = %q, want denial", res.Output)
	}
	if second.SessionGrantCount() != 0 {
		t.Fatalf("second gate inherited grants: %d", second.SessionGrantCount())
	}
}

// TestReadOnlyTurnFlag covers "r": read-only tools are auto-approved for the
// turn, mutating tools are not, and BeginTurn restores prompting.
func TestReadOnlyTurnFlag(t *testing.T) {
	ctx := context.Background()
	gate := NewApprovalGate()
	readTool := &grantTool{name: "read_like", requiresApproval: true, readOnly: true}
	mutating := &grantTool{name: "mutate", requiresApproval: true}

	prompts := 0
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		prompts++
		return ApprovalReadOnlyTurn, nil
	}

	// The "r" answer applies to the pending read-only call as well.
	res, err := gate.WrapExecution(ctx, readTool, json.RawMessage(`{"path":"a"}`))
	if err != nil {
		t.Fatalf("read-only prompt: %v", err)
	}
	if readTool.executions != 1 || !strings.Contains(res.Output, "executed") {
		t.Fatalf("read-only call not approved by r: %q", res.Output)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}

	// Later read-only calls bypass the prompt for the rest of the turn.
	if _, err := gate.WrapExecution(ctx, readTool, json.RawMessage(`{"path":"b"}`)); err != nil {
		t.Fatalf("second read-only call: %v", err)
	}
	if readTool.executions != 2 {
		t.Fatalf("read-only executions = %d, want 2", readTool.executions)
	}
	if prompts != 1 {
		t.Fatalf("read-only flag still prompted: prompts = %d", prompts)
	}

	// A mutating tool is never covered by the flag.
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		return ApprovalDeny, nil
	}
	res, err = gate.WrapExecution(ctx, mutating, json.RawMessage(`{"path":"c"}`))
	if err != nil {
		t.Fatalf("mutating prompt: %v", err)
	}
	if mutating.executions != 0 {
		t.Fatalf("mutating tool auto-approved by read-only flag")
	}
	if !strings.Contains(res.Output, "denied") {
		t.Fatalf("mutating denial = %q", res.Output)
	}

	// A new user turn clears the flag.
	gate.BeginTurn()
	promptedAgain := 0
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		promptedAgain++
		return ApprovalDeny, nil
	}
	if _, err := gate.WrapExecution(ctx, readTool, json.RawMessage(`{"path":"d"}`)); err != nil {
		t.Fatalf("post-BeginTurn call: %v", err)
	}
	if promptedAgain != 1 {
		t.Fatalf("read-only flag survived BeginTurn: prompts = %d", promptedAgain)
	}
	if readTool.executions != 2 {
		t.Fatalf("read-only call executed after BeginTurn denial: %d", readTool.executions)
	}
}

// TestEditArgsRepromptsWithEditedPreview verifies "e": the gate collects new
// args, regenerates the preview, re-prompts, and executes the edited args.
func TestEditArgsRepromptsWithEditedPreview(t *testing.T) {
	gate := NewApprovalGate()
	tool := &grantPreviewTool{grantTool{name: "shell_like", requiresApproval: true}}
	var previews []string
	choices := 0
	gate.OnPresentChoice = func(_ string, _ json.RawMessage, preview string) (ApprovalChoice, error) {
		choices++
		previews = append(previews, preview)
		if choices == 1 {
			return ApprovalEdit, nil
		}
		return ApprovalOnce, nil
	}
	gate.EditArgs = func(string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"command":"echo edited"}`), nil
	}

	res, err := gate.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"echo original"}`))
	if err != nil {
		t.Fatalf("WrapExecution: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected result error: %q", res.Error)
	}
	if tool.executions != 1 {
		t.Fatalf("executions = %d, want 1", tool.executions)
	}
	if string(tool.lastArgs) != `{"command":"echo edited"}` {
		t.Fatalf("executed args = %s, want edited args", tool.lastArgs)
	}
	if len(previews) != 2 {
		t.Fatalf("preview count = %d, want 2", len(previews))
	}
	if !strings.Contains(previews[0], "echo original") {
		t.Fatalf("first preview missing original command: %q", previews[0])
	}
	if !strings.Contains(previews[1], "echo edited") {
		t.Fatalf("second preview missing edited command: %q", previews[1])
	}
}

// TestTerminalEditRepromptsWithRegeneratedPreview drives the actual line
// prompt: "e", one JSON line, then "y". The second render must show the
// edited command and execution must use the edited args.
func TestTerminalEditRepromptsWithRegeneratedPreview(t *testing.T) {
	gate := NewApprovalGate()
	tool := &grantPreviewTool{grantTool{name: "shell_like", requiresApproval: true}}
	gate.SetInputReader(bufio.NewReader(strings.NewReader("e\n{\"command\":\"echo edited\"}\ny\n")))
	gate.EditArgs = func(string, json.RawMessage) (json.RawMessage, error) {
		line, err := gate.readLine()
		if err != nil {
			return nil, err
		}
		return json.RawMessage(strings.TrimSpace(line)), nil
	}

	var out string
	var res Result
	out = captureStdout(t, func() {
		res, _ = gate.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"echo original"}`))
	})

	if res.Error != "" {
		t.Fatalf("unexpected result error: %q", res.Error)
	}
	if tool.executions != 1 {
		t.Fatalf("executions = %d, want 1", tool.executions)
	}
	if string(tool.lastArgs) != `{"command":"echo edited"}` {
		t.Fatalf("executed args = %s, want edited args", tool.lastArgs)
	}
	if strings.Count(out, "echo edited") < 2 {
		t.Fatalf("edited command not shown in re-rendered args+preview: %q", out)
	}
	if !strings.Contains(out, "Approve? [") {
		t.Fatalf("second prompt missing: %q", out)
	}
}

// TestEditArgsInvalidJSONDeniesWithoutExecuting guarantees a malformed edit
// cannot reach the tool.
func TestEditArgsInvalidJSONDeniesWithoutExecuting(t *testing.T) {
	gate := NewApprovalGate()
	tool := &grantPreviewTool{grantTool{name: "shell_like", requiresApproval: true}}
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		return ApprovalEdit, nil
	}
	gate.EditArgs = func(string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"command":`), nil
	}

	res, err := gate.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"echo original"}`))
	if err != nil {
		t.Fatalf("WrapExecution: %v", err)
	}
	if tool.executions != 0 {
		t.Fatalf("invalid edit executed the tool")
	}
	if !strings.Contains(res.Error, "invalid edited arguments") {
		t.Fatalf("result error = %q, want invalid-arguments error", res.Error)
	}

	// An editor error is treated as a denial, too.
	gate.EditArgs = func(string, json.RawMessage) (json.RawMessage, error) {
		return nil, context.Canceled
	}
	res, err = gate.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"echo original"}`))
	if err != nil {
		t.Fatalf("WrapExecution: %v", err)
	}
	if tool.executions != 0 {
		t.Fatalf("editor error executed the tool")
	}
	if res.Error == "" {
		t.Fatal("editor error was not reported")
	}
}

// TestEditArgsBoundedToOnePass prevents repeated edits from looping.
func TestEditArgsBoundedToOnePass(t *testing.T) {
	gate := NewApprovalGate()
	tool := &grantPreviewTool{grantTool{name: "shell_like", requiresApproval: true}}
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		return ApprovalEdit, nil
	}
	gate.EditArgs = func(string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"command":"echo edited"}`), nil
	}

	res, err := gate.WrapExecution(context.Background(), tool, json.RawMessage(`{"command":"echo original"}`))
	if err != nil {
		t.Fatalf("WrapExecution: %v", err)
	}
	if tool.executions != 0 {
		t.Fatalf("unbounded edit executed the tool")
	}
	if !strings.Contains(res.Error, "only once") {
		t.Fatalf("result error = %q, want one-edit bound", res.Error)
	}
}
