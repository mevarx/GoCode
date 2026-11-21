package hooks

import "testing"

// Event names normalize case-insensitively with underscores removed
// pre_tool_use and PRETOOLUSE both resolve to PreToolUse.
func TestNormalizeEvent(t *testing.T) {
	cases := []struct {
		in   string
		want Event
		ok   bool
	}{
		{"PreToolUse", PreToolUse, true},
		{"pre_tool_use", PreToolUse, true},
		{"PRETOOLUSE", PreToolUse, true},
		{"pRe_ToOl_UsE", PreToolUse, true},
		{"  posttoolusefailure  ", PostToolUseFailure, true},
		{"post_tool_use_failure", PostToolUseFailure, true},
		{"permissionrequest", PermissionRequest, true},
		{"permission_result", PermissionResult, true},
		{"sessionstart", SessionStart, true},
		{"SESSION_END", SessionEnd, true},
		{"turnstart", TurnStart, true},
		{"turn_end", TurnEnd, true},
		{"stopfailure", StopFailure, true},
		{"interrupt", Interrupt, true},
		{"precompact", PreCompact, true},
		{"POST_COMPACT", PostCompact, true},
		{"PreToolUseX", "", false},
		{"", "", false},
		{"tool_use", "", false},
		{"TurnFinished", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeEvent(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("NormalizeEvent(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// The vocabulary is exactly 13 events, and the tool/lifecycle split decides
// whether the matcher applies.
func TestEventVocabulary(t *testing.T) {
	if len(AllEvents) != 13 {
		t.Fatalf("AllEvents has %d entries, want 13", len(AllEvents))
	}
	seen := map[Event]bool{}
	for _, e := range AllEvents {
		if seen[e] {
			t.Errorf("duplicate event %q", e)
		}
		seen[e] = true
		if got, ok := NormalizeEvent(string(e)); !ok || got != e {
			t.Errorf("canonical name %q does not normalize to itself", e)
		}
	}
	toolEvents := []Event{PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest, PermissionResult}
	for _, e := range toolEvents {
		if !e.IsToolEvent() {
			t.Errorf("%q must be a tool event", e)
		}
	}
	lifecycle := []Event{SessionStart, SessionEnd, TurnStart, TurnEnd, StopFailure, Interrupt, PreCompact, PostCompact}
	for _, e := range lifecycle {
		if e.IsToolEvent() {
			t.Errorf("%q must be a lifecycle event", e)
		}
	}
}
