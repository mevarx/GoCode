package hooks

import (
	"testing"
)

// The exit-code table: 0 honours stdout JSON, 2 denies with the
// stderr reason, 49 denies AND halts, any other non-zero is non-blocking
// unless stdout carried a valid JSON object.
func TestExitCodeSemantics(t *testing.T) {
	cases := []struct {
		name         string
		stdout       string
		stderr       string
		code         int
		wantDecision Decision
		wantHalt     bool
		wantReason   string
		wantDiag     bool
	}{
		{"0 no json", "", "", 0, DecisionNone, false, "", false},
		{"0 json allow", `{"decision":"allow"}`, "", 0, DecisionAllow, false, "", false},
		{"0 json deny", `{"decision":"deny","reason":"nope"}`, "", 0, DecisionDeny, false, "nope", false},
		{"0 json block", `{"decision":"block","reason":"nope"}`, "", 0, DecisionDeny, false, "nope", false},
		{"0 json approve", `{"decision":"approve"}`, "", 0, DecisionAllow, false, "", false},
		{"0 json halt", `{"halt":true,"reason":"stop"}`, "", 0, DecisionNone, true, "stop", false},
		{"0 invalid json", `{oops`, "", 0, DecisionNone, false, "", false},
		{"0 json array", `[1]`, "", 0, DecisionNone, false, "", false},
		{"2 stderr reason", "", "danger detected", 2, DecisionDeny, false, "danger detected", false},
		{"2 fallback", "", "  ", 2, DecisionDeny, false, "blocked by hook", false},
		{"2 ignores stdout allow", `{"decision":"allow"}`, "no", 2, DecisionDeny, false, "no", false},
		{"49 stderr reason", "", "halt now", HaltExitCode, DecisionDeny, true, "halt now", false},
		{"49 fallback", "", "", HaltExitCode, DecisionDeny, true, "turn halted by hook", false},
		{"other non-zero no json", "", "boom", 1, DecisionNone, false, "", true},
		{"other non-zero json override", `{"decision":"deny","reason":"from json"}`, "boom", 1, DecisionDeny, false, "from json", false},
		{"other non-zero json halt override", `{"halt":true}`, "", 7, DecisionNone, true, "", false},
		{"other non-zero invalid json", `not json`, "boom", 3, DecisionNone, false, "", true},
		{"signal-range exit", "", "", 137, DecisionNone, false, "", true},
	}
	for _, tc := range cases {
		base := HookResult{Name: "h", Command: "cmd"}
		got := classifyExit(base, []byte(tc.stdout), []byte(tc.stderr), tc.code)
		if got.Decision != tc.wantDecision || got.Halt != tc.wantHalt || got.Reason != tc.wantReason {
			t.Errorf("%s: got decision=%v halt=%v reason=%q, want %v/%v/%q",
				tc.name, got.Decision, got.Halt, got.Reason, tc.wantDecision, tc.wantHalt, tc.wantReason)
		}
		if (got.Diagnostic != "") != tc.wantDiag {
			t.Errorf("%s: diagnostic = %q, want present=%v", tc.name, got.Diagnostic, tc.wantDiag)
		}
	}
}

// continue:false folds to halt:true with stopReason as the reason fallback.
func TestOutputContinueFalseFoldsToHalt(t *testing.T) {
	out, ok := parseOutput([]byte(`{"continue":false,"stopReason":"user asked to stop"}`))
	if !ok {
		t.Fatal("valid JSON object not parsed")
	}
	got := applyOutput(HookResult{}, out)
	if !got.Halt {
		t.Error("continue:false did not fold to halt")
	}
	if got.Reason != "user asked to stop" {
		t.Errorf("reason = %q, want the stopReason", got.Reason)
	}
	if got.StopReason != "user asked to stop" {
		t.Errorf("StopReason = %q, want preserved", got.StopReason)
	}

	// An explicit reason wins over the stopReason fallback.
	out, _ = parseOutput([]byte(`{"continue":false,"stopReason":"sr","reason":"explicit"}`))
	got = applyOutput(HookResult{}, out)
	if got.Reason != "explicit" {
		t.Errorf("reason = %q, want the explicit reason", got.Reason)
	}

	// continue:true changes nothing.
	out, _ = parseOutput([]byte(`{"continue":true,"decision":"allow"}`))
	got = applyOutput(HookResult{}, out)
	if got.Halt || got.Decision != DecisionAllow {
		t.Errorf("continue:true altered the outcome: %+v", got)
	}
}

// hookSpecificOutput maps permissionDecision (ask = no opinion) and supplies
// the alternate field shapes.
func TestOutputHookSpecificOutput(t *testing.T) {
	cases := []struct {
		name         string
		json         string
		wantDecision Decision
		wantReason   string
	}{
		{"allow", `{"hookSpecificOutput":{"permissionDecision":"allow"}}`, DecisionAllow, ""},
		{"deny with reason", `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"policy"}}`, DecisionDeny, "policy"},
		{"ask is no opinion", `{"hookSpecificOutput":{"permissionDecision":"ask"}}`, DecisionNone, ""},
		{"top-level wins", `{"decision":"deny","reason":"top","hookSpecificOutput":{"permissionDecision":"allow"}}`, DecisionDeny, "top"},
	}
	for _, tc := range cases {
		out, ok := parseOutput([]byte(tc.json))
		if !ok {
			t.Fatalf("%s: parse failed", tc.name)
		}
		got := applyOutput(HookResult{}, out)
		if got.Decision != tc.wantDecision || got.Reason != tc.wantReason {
			t.Errorf("%s: got %v/%q, want %v/%q", tc.name, got.Decision, got.Reason, tc.wantDecision, tc.wantReason)
		}
	}

	// Alternate shapes: updatedInput / updatedToolOutput / additionalContext.
	out, _ := parseOutput([]byte(`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"a":1},"updatedToolOutput":"replaced","additionalContext":"extra"}}`))
	got := applyOutput(HookResult{}, out)
	if string(got.UpdatedInput) != `{"a":1}` {
		t.Errorf("updatedInput fallback = %s", got.UpdatedInput)
	}
	if got.UpdatedOutput == nil || *got.UpdatedOutput != "replaced" {
		t.Errorf("updatedToolOutput fallback = %v", got.UpdatedOutput)
	}
	if got.Context != "extra" {
		t.Errorf("additionalContext = %q, want %q", got.Context, "extra")
	}

	// Top-level fields win over the hookSpecificOutput fallbacks.
	out, _ = parseOutput([]byte(`{"updated_input":{"b":2},"updated_output":"top","context":"c1","hookSpecificOutput":{"updatedInput":{"a":1},"updatedToolOutput":"hso","additionalContext":"c2"}}`))
	got = applyOutput(HookResult{}, out)
	if string(got.UpdatedInput) != `{"b":2}` || got.UpdatedOutput == nil || *got.UpdatedOutput != "top" {
		t.Errorf("top-level updated fields did not win: %+v", got)
	}
	if got.Context != "c1\nc2" {
		t.Errorf("context = %q, want c1\\nc2 (additionalContext appended)", got.Context)
	}
}

// context accepts a string or a string array; other shapes contribute nothing.
func TestOutputContextShapes(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"one"`, "one"},
		{`["a","b"]`, "a\nb"},
		{`["a",42,"b"]`, "a\nb"},
		{`[]`, ""},
		{`42`, ""},
		{`{"a":1}`, ""},
	}
	for _, tc := range cases {
		out, ok := parseOutput([]byte(`{"context":` + tc.raw + `}`))
		if !ok {
			t.Fatalf("context %s: parse failed", tc.raw)
		}
		got := applyOutput(HookResult{}, out)
		if got.Context != tc.want {
			t.Errorf("context %s = %q, want %q", tc.raw, got.Context, tc.want)
		}
	}
}

// updated_output distinguishes unset from empty-string replacement.
func TestOutputUpdatedOutputPointer(t *testing.T) {
	out, _ := parseOutput([]byte(`{"updated_output":""}`))
	got := applyOutput(HookResult{}, out)
	if got.UpdatedOutput == nil || *got.UpdatedOutput != "" {
		t.Errorf("empty replacement must be honoured, got %v", got.UpdatedOutput)
	}
	out, _ = parseOutput([]byte(`{}`))
	got = applyOutput(HookResult{}, out)
	if got.UpdatedOutput != nil {
		t.Errorf("unset updated_output must stay nil, got %v", *got.UpdatedOutput)
	}
}

func TestParseOutputRejectsNonObjects(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", `"str"`, `[1]`, `42`, `{bad`, `{"a":1} trailing`} {
		if _, ok := parseOutput([]byte(raw)); ok {
			t.Errorf("parseOutput(%q) accepted a non-object", raw)
		}
	}
}

// version is accepted and ignored; unknown fields do not break parsing.
func TestParseOutputToleratesVersionAndUnknownFields(t *testing.T) {
	out, ok := parseOutput([]byte(`{"version":1,"decision":"allow","future_field":{"x":1}}`))
	if !ok {
		t.Fatal("version/unknown fields broke parsing")
	}
	if out.Version != 1 || applyOutput(HookResult{}, out).Decision != DecisionAllow {
		t.Errorf("unexpected parse result: %+v", out)
	}
}
