package hooks

import (
	"strings"
	"testing"
)

func TestNormalizeConfigCanonicalizesAndDefaults(t *testing.T) {
	raw := Config{
		"pre_tool_use": []Hook{
			{Name: " guard ", Command: " echo hi ", Matcher: "bash|write"},
		},
		"TURNEND": []Hook{
			{Command: "notify"},
		},
	}
	got, err := NormalizeConfig(raw)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("normalized %d events, want 2", len(got))
	}
	pre := got[PreToolUse]
	if len(pre) != 1 {
		t.Fatalf("PreToolUse has %d hooks, want 1", len(pre))
	}
	if pre[0].Name != "guard" || pre[0].Command != "echo hi" {
		t.Errorf("name/command not trimmed: %+v", pre[0])
	}
	if pre[0].Timeout != DefaultTimeoutSeconds {
		t.Errorf("timeout = %d, want the %ds default", pre[0].Timeout, DefaultTimeoutSeconds)
	}
	if _, ok := got[TurnEnd]; !ok {
		t.Error("TURNEND did not normalize to TurnEnd")
	}
}

func TestNormalizeConfigDedupsByCommandPerEvent(t *testing.T) {
	raw := Config{
		PreToolUse: []Hook{
			{Name: "first", Command: "guard"},
			{Name: "dup", Command: "guard"},
			{Name: "other", Command: "audit"},
		},
		PostToolUse: []Hook{
			// Same command on a DIFFERENT event is not a duplicate.
			{Command: "guard"},
		},
	}
	got, err := NormalizeConfig(raw)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if len(got[PreToolUse]) != 2 {
		t.Fatalf("PreToolUse kept %d hooks, want 2 (dedup by command)", len(got[PreToolUse]))
	}
	if got[PreToolUse][0].Name != "first" {
		t.Errorf("dedup must keep the FIRST occurrence, got %q", got[PreToolUse][0].Name)
	}
	if len(got[PostToolUse]) != 1 {
		t.Errorf("cross-event command was wrongly deduped")
	}
}

func TestNormalizeConfigRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  Config
		want string
	}{
		{"unknown event", Config{"Bogus": []Hook{{Command: "x"}}}, "unknown event"},
		{"empty command", Config{PreToolUse: []Hook{{Name: "x"}}}, "command is required"},
		{"blank command", Config{PreToolUse: []Hook{{Command: "   "}}}, "command is required"},
		{"bad matcher", Config{PreToolUse: []Hook{{Command: "x", Matcher: "["}}}, "invalid matcher"},
		{"negative timeout", Config{PreToolUse: []Hook{{Command: "x", Timeout: -1}}}, "negative"},
	}
	for _, tc := range cases {
		if _, err := NormalizeConfig(tc.raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestNormalizeConfigIdempotent(t *testing.T) {
	raw := Config{
		"pre_tool_use": []Hook{{Command: "a"}, {Command: "a"}, {Command: "b", Timeout: 5}},
	}
	first, err := NormalizeConfig(raw)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	second, err := NormalizeConfig(first)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(second[PreToolUse]) != len(first[PreToolUse]) {
		t.Fatalf("second pass changed hook count: %d -> %d", len(first[PreToolUse]), len(second[PreToolUse]))
	}
	for i := range first[PreToolUse] {
		if first[PreToolUse][i] != second[PreToolUse][i] {
			t.Errorf("hook %d changed on re-normalization: %+v -> %+v", i, first[PreToolUse][i], second[PreToolUse][i])
		}
	}
}

func TestNormalizeConfigEmpty(t *testing.T) {
	got, err := NormalizeConfig(nil)
	if err != nil || got != nil {
		t.Errorf("nil config: got (%v, %v), want (nil, nil)", got, err)
	}
}
