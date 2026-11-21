package hooks

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// Decision precedence: deny > allow > none, regardless of order.
func TestAggregateDecisionPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		results []Decision
		want    Decision
	}{
		{"all none", []Decision{DecisionNone, DecisionNone}, DecisionNone},
		{"allow beats none", []Decision{DecisionNone, DecisionAllow}, DecisionAllow},
		{"deny beats allow", []Decision{DecisionAllow, DecisionDeny}, DecisionDeny},
		{"deny first stays", []Decision{DecisionDeny, DecisionAllow, DecisionNone}, DecisionDeny},
		{"empty", nil, DecisionNone},
	}
	for _, tc := range cases {
		var results []HookResult
		for _, d := range tc.results {
			results = append(results, HookResult{Decision: d})
		}
		if got := Aggregate(results, nil); got.Decision != tc.want {
			t.Errorf("%s: decision = %v, want %v", tc.name, got.Decision, tc.want)
		}
	}
}

// Halt is a sticky OR across every hook.
func TestAggregateHaltStickyOR(t *testing.T) {
	results := []HookResult{
		{Decision: DecisionAllow},
		{Halt: true},
		{Decision: DecisionNone},
	}
	if got := Aggregate(results, nil); !got.Halt {
		t.Error("halt did not stick")
	}
	if got := Aggregate([]HookResult{{}, {}}, nil); got.Halt {
		t.Error("halt set without any halting hook")
	}
}

// Reasons join deny reasons plus halting non-deny reasons, in config order;
// a non-halting allow's reason does not contribute.
func TestAggregateReasonJoin(t *testing.T) {
	results := []HookResult{
		{Decision: DecisionDeny, Reason: "first deny"},
		{Decision: DecisionAllow, Reason: "allow chatter"},
		{Decision: DecisionNone, Halt: true, Reason: "halting none"},
		{Decision: DecisionDeny, Reason: "second deny"},
		{Decision: DecisionAllow, Halt: true, Reason: "halting allow"},
	}
	got := Aggregate(results, nil)
	want := "first deny\nhalting none\nsecond deny\nhalting allow"
	if got.Reason != want {
		t.Errorf("reason = %q, want %q", got.Reason, want)
	}
}

// Context joins every hook's context with newlines, in config order.
func TestAggregateContextJoin(t *testing.T) {
	results := []HookResult{
		{Context: "alpha"},
		{Decision: DecisionDeny, Context: "beta"},
		{},
		{Context: "gamma"},
	}
	if got := Aggregate(results, nil); got.Context != "alpha\nbeta\ngamma" {
		t.Errorf("context = %q", got.Context)
	}
}

// updated_input shallow-merges top-level keys in config order onto the
// original input; nested objects are replaced whole, not deep-merged.
func TestAggregateUpdatedInputShallowMergeOrder(t *testing.T) {
	original := json.RawMessage(`{"command":"ls","nested":{"a":1,"b":2},"keep":true}`)
	results := []HookResult{
		{UpdatedInput: json.RawMessage(`{"command":"pwd","extra":1}`)},
		{UpdatedInput: json.RawMessage(`{"command":"whoami","nested":{"c":3}}`)},
	}
	got := Aggregate(results, original)
	var obj map[string]any
	if err := json.Unmarshal(got.UpdatedInput, &obj); err != nil {
		t.Fatalf("merged input not JSON: %v (%s)", err, got.UpdatedInput)
	}
	want := map[string]any{
		"command": "whoami",                        // last patch wins per key
		"nested":  map[string]any{"c": float64(3)}, // shallow: whole replacement
		"keep":    true,
		"extra":   float64(1),
	}
	if !reflect.DeepEqual(obj, want) {
		t.Errorf("merged = %#v, want %#v", obj, want)
	}
}

// A patch that is not a JSON object is rejected with a diagnostic; valid
// patches around it still apply.
func TestAggregateUpdatedInputNonObjectRejected(t *testing.T) {
	original := json.RawMessage(`{"a":1}`)
	results := []HookResult{
		{UpdatedInput: json.RawMessage(`{"b":2}`)},
		{UpdatedInput: json.RawMessage(`[1,2]`)},
		{UpdatedInput: json.RawMessage(`"str"`)},
		{UpdatedInput: json.RawMessage(`null`)},
		{UpdatedInput: json.RawMessage(`{invalid`)},
		{UpdatedInput: json.RawMessage(`{"c":3}`)},
	}
	got := Aggregate(results, original)
	var obj map[string]any
	if err := json.Unmarshal(got.UpdatedInput, &obj); err != nil {
		t.Fatalf("merged input not JSON: %v", err)
	}
	if !reflect.DeepEqual(obj, map[string]any{"a": float64(1), "b": float64(2), "c": float64(3)}) {
		t.Errorf("merged = %#v", obj)
	}
	if len(got.Diagnostics) != 4 {
		t.Errorf("diagnostics = %d, want 4 (one per rejected patch): %v", len(got.Diagnostics), got.Diagnostics)
	}
	for _, d := range got.Diagnostics {
		if !strings.Contains(d, "not a JSON object") {
			t.Errorf("unexpected diagnostic %q", d)
		}
	}
}

// No patches means no rewrite at all (nil, not a re-marshal of the original).
func TestAggregateUpdatedInputUnsetStaysNil(t *testing.T) {
	got := Aggregate([]HookResult{{Decision: DecisionAllow}}, json.RawMessage(`{"a":1}`))
	if got.UpdatedInput != nil {
		t.Errorf("UpdatedInput = %s, want nil without patches", got.UpdatedInput)
	}
	// An invalid original input merges onto {}.
	got = Aggregate([]HookResult{{UpdatedInput: json.RawMessage(`{"a":1}`)}}, json.RawMessage(`not json`))
	if string(got.UpdatedInput) != `{"a":1}` {
		t.Errorf("merge onto invalid original = %s", got.UpdatedInput)
	}
}

// updated_output is a whole replacement; the LAST hook to set it wins.
func TestAggregateUpdatedOutputLastWins(t *testing.T) {
	results := []HookResult{
		{UpdatedOutput: strp("first")},
		{Decision: DecisionDeny},
		{UpdatedOutput: strp("last")},
		{UpdatedOutput: strp("")}, // an empty replacement is still a replacement
	}
	got := Aggregate(results, nil)
	if got.UpdatedOutput == nil || *got.UpdatedOutput != "" {
		t.Errorf("last-wins failed: %v", got.UpdatedOutput)
	}
	results = results[:3]
	got = Aggregate(results, nil)
	if got.UpdatedOutput == nil || *got.UpdatedOutput != "last" {
		t.Errorf("last-wins failed: %v", got.UpdatedOutput)
	}
}

// Diagnostics ride the aggregation, and results stay in config order.
func TestAggregatePreservesOrderAndDiagnostics(t *testing.T) {
	results := []HookResult{
		{Name: "a", Diagnostic: "timed out"},
		{Name: "b"},
	}
	got := Aggregate(results, nil)
	if len(got.Results) != 2 || got.Results[0].Name != "a" || got.Results[1].Name != "b" {
		t.Errorf("results reordered: %+v", got.Results)
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0] != "timed out" {
		t.Errorf("diagnostics = %v", got.Diagnostics)
	}
}

// stopReason aggregates last-wins for the turn-halt display.
func TestAggregateStopReasonLastWins(t *testing.T) {
	got := Aggregate([]HookResult{{StopReason: "one"}, {StopReason: "two"}}, nil)
	if got.StopReason != "two" {
		t.Errorf("StopReason = %q, want two", got.StopReason)
	}
}
