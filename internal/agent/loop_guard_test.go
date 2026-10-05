package agent

import (
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

func call(name, args string) []provider.ToolCall {
	return []provider.ToolCall{{ID: "c1", Name: name, Args: []byte(args)}}
}

func TestLoopGuardAllowsNormalTurn(t *testing.T) {
	g := NewLoopGuard()
	for i := 0; i < 10; i++ {
		// Distinct arguments each round: genuine forward progress.
		if err := g.CheckCall(call("file_read", `{"path":"f`+string(rune('a'+i))+`"}`)); err != nil {
			t.Fatalf("iteration %d should be allowed: %v", i, err)
		}
	}
	if g.ToolCalls != 10 {
		t.Errorf("expected 10 recorded tool calls, got %d", g.ToolCalls)
	}
}

func TestLoopGuardStopsIdenticalRetries(t *testing.T) {
	g := NewLoopGuard()
	limit := DefaultMaxRepeatedToolCalls

	for i := 0; i < limit; i++ {
		if err := g.CheckCall(call("file_read", `{"path":"a.txt"}`)); err != nil {
			t.Fatalf("call %d of %d should be allowed: %v", i+1, limit, err)
		}
	}

	err := g.CheckCall(call("file_read", `{"path":"a.txt"}`))
	if err == nil {
		t.Fatal("expected identical repeat to be stopped")
	}
	if !strings.Contains(err.Error(), "file_read") {
		t.Errorf("error should name the offending tool, got %q", err)
	}
}

func TestLoopGuardResetsOnDifferentArguments(t *testing.T) {
	g := NewLoopGuard()
	// Alternate arguments well beyond the repeat limit: never a stop.
	for i := 0; i < 20; i++ {
		if err := g.CheckCall(call("file_read", `{"path":"f`+strings.Repeat("x", i+1)+`"}`)); err != nil {
			t.Fatalf("iteration %d should be allowed: %v", i, err)
		}
	}
}

func TestLoopGuardStopsAtIterationCap(t *testing.T) {
	g := NewLoopGuardWithConfig(LoopGuardConfig{MaxIterations: 5})
	// Distinct calls so repeat detection never fires; only the cap can stop it.
	for i := 0; i < 5; i++ {
		if err := g.CheckCall(call("code_search", `{"query":"q`+strings.Repeat("y", i+1)+`"}`)); err != nil {
			t.Fatalf("iteration %d of 5 should be allowed: %v", i+1, err)
		}
	}
	err := g.CheckCall(call("code_search", `{"query":"over"}`))
	if err == nil {
		t.Fatal("expected iteration cap to stop the turn")
	}
	if !strings.Contains(err.Error(), "tool iterations") {
		t.Errorf("error should mention the iteration limit, got %q", err)
	}
}

func TestLoopGuardConfigOverridesDefaults(t *testing.T) {
	g := NewLoopGuardWithConfig(LoopGuardConfig{MaxIterations: 2, MaxRepeatedCalls: 1})
	if g.MaxIterations != 2 {
		t.Errorf("expected MaxIterations 2, got %d", g.MaxIterations)
	}
	if g.MaxRepeatedCalls != 1 {
		t.Errorf("expected MaxRepeatedCalls 1, got %d", g.MaxRepeatedCalls)
	}

	if err := g.CheckCall(call("shell_exec", `{"command":"ls"}`)); err != nil {
		t.Fatalf("first call should be allowed: %v", err)
	}
	if err := g.CheckCall(call("shell_exec", `{"command":"ls"}`)); err == nil {
		t.Fatal("second identical call should trip a limit of 1")
	}
}

func TestLoopGuardNonPositiveConfigFallsBackToDefaults(t *testing.T) {
	g := NewLoopGuardWithConfig(LoopGuardConfig{MaxIterations: -1, MaxRepeatedCalls: 0})
	if g.MaxIterations != DefaultMaxToolIterations {
		t.Errorf("expected default iterations, got %d", g.MaxIterations)
	}
	if g.MaxRepeatedCalls != DefaultMaxRepeatedToolCalls {
		t.Errorf("expected default repeat limit, got %d", g.MaxRepeatedCalls)
	}
}

// A failing tool retried identically is the runaway shape: the guard must stop it
// well before the iteration cap.
func TestLoopGuardStopsRunawayFailingTool(t *testing.T) {
	g := NewLoopGuardWithConfig(LoopGuardConfig{MaxIterations: 1000})
	stopped := 0
	for i := 0; i < 1000; i++ {
		if err := g.CheckCall(call("file_read", `{"path":"/nope"}`)); err != nil {
			stopped = i + 1
			break
		}
	}
	if stopped == 0 {
		t.Fatal("runaway identical failing calls were never stopped")
	}
	if stopped > DefaultMaxRepeatedToolCalls+1 {
		t.Errorf("expected stop within %d calls, took %d", DefaultMaxRepeatedToolCalls+1, stopped)
	}
	t.Logf("stopped after %d identical calls (iteration cap was 1000)", stopped)
}

// Same logical call in four textual shapes must count as a repeat; raw-byte keys let them evade the guard.
func TestLoopGuardCanonicalisesArguments(t *testing.T) {
	g := NewLoopGuard()
	shapes := []string{
		`{"a":1,"b":2}`,
		`{"b":2,"a":1}`,
		`{ "a" : 1, "b" : 2 }`,
		`{"a":1.0,"b":2}`,
	}
	for i, s := range shapes[:len(shapes)-1] {
		if err := g.CheckCall(call("file_read", s)); err != nil {
			t.Fatalf("call %d should be allowed: %v", i+1, err)
		}
	}
	if err := g.CheckCall(call("file_read", shapes[len(shapes)-1])); err == nil {
		t.Fatal("canonical repeats of the same call should trip the guard")
	}
}

// Repeats count only when consecutive; the old cumulative counter aborted real work.
func TestLoopGuardCountsConsecutiveRepeatsOnly(t *testing.T) {
	g := NewLoopGuard()
	// Interleave the repeated call with distinct calls, well past the limit:
	// the streak resets on every interleaved call, so this must never stop.
	for i := 0; i < DefaultMaxRepeatedToolCalls+3; i++ {
		if err := g.CheckCall(call("shell_exec", `{"command":"go test ./..."}`)); err != nil {
			t.Fatalf("interleaved repeat %d should be allowed: %v", i+1, err)
		}
		if err := g.CheckCall(call("file_read", `{"path":"f`+strings.Repeat("x", i+1)+`"}`)); err != nil {
			t.Fatalf("distinct call %d should be allowed: %v", i+1, err)
		}
	}
	for i := 0; i < DefaultMaxRepeatedToolCalls; i++ {
		if err := g.CheckCall(call("shell_exec", `{"command":"go test ./..."}`)); err != nil {
			t.Fatalf("consecutive repeat %d of %d should be allowed: %v", i+1, DefaultMaxRepeatedToolCalls, err)
		}
	}
	if err := g.CheckCall(call("shell_exec", `{"command":"go test ./..."}`)); err == nil {
		t.Fatal("consecutive streak beyond the limit should trip the guard")
	}
}

func TestLoopGuardSummary(t *testing.T) {
	empty := NewLoopGuard()
	if s := empty.Summary(); s != "" {
		t.Errorf("expected empty summary with no tool calls, got %q", s)
	}

	g := NewLoopGuard()
	_ = g.CheckCall(call("file_read", `{"path":"a"}`))
	s := g.Summary()
	if !strings.Contains(s, "1 tool call") {
		t.Errorf("expected tool call count in summary, got %q", s)
	}
}
