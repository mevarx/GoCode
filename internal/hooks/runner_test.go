package hooks

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCall records one executor invocation.
type fakeCall struct {
	command string
	cwd     string
	stdin   []byte
	env     []string
}

// fakeRecorder is a mutex-guarded executor recorder: parallel hook goroutines
// all write through it, so the race detector stays quiet.
type fakeRecorder struct {
	mu    sync.Mutex
	calls []fakeCall
}

func (f *fakeRecorder) record(command, cwd string, stdin []byte, env []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{command, cwd, append([]byte(nil), stdin...), append([]string(nil), env...)})
}

func (f *fakeRecorder) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// newTestRunner builds a runner over a normalized config with a fake executor.
func newTestRunner(t *testing.T, cfg Config, exec Executor) *Runner {
	t.Helper()
	r, err := NewRunner(cfg, exec)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// The matcher regex filters tool events; empty matches all.
func TestRunnerMatcherFiltersToolEvents(t *testing.T) {
	rec := &fakeRecorder{}
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		rec.record(command, cwd, stdin, env)
		return nil, nil, 0, nil
	}
	r := newTestRunner(t, Config{
		PreToolUse: []Hook{
			{Name: "guarded", Command: "guard", Matcher: "bash|write"},
			{Name: "all", Command: "audit"},
			{Name: "nomatch", Command: "never", Matcher: "^shell_"},
		},
	}, exec)

	for _, tool := range []string{"bash", "file_write", "file_read"} {
		rec.mu.Lock()
		rec.calls = nil
		rec.mu.Unlock()
		r.Run(context.Background(), PreToolUse, Input{ToolName: tool})
		calls := rec.snapshot()
		var ran []string
		for _, c := range calls {
			ran = append(ran, c.command)
		}
		// Hooks run in parallel: invocation order is not contracted, only
		// WHICH hooks run (aggregation order is tested separately).
		sort.Strings(ran)
		want := map[string][]string{
			"bash":       {"audit", "guard"},
			"file_write": {"audit", "guard"},
			"file_read":  {"audit"},
		}[tool]
		if strings.Join(ran, ",") != strings.Join(want, ",") {
			t.Errorf("tool %q ran %v, want %v", tool, ran, want)
		}
	}
}

// Lifecycle events ignore the matcher entirely.
func TestRunnerLifecycleIgnoresMatcher(t *testing.T) {
	rec := &fakeRecorder{}
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		rec.record(command, cwd, stdin, env)
		return nil, nil, 0, nil
	}
	r := newTestRunner(t, Config{
		SessionStart: []Hook{{Command: "notify", Matcher: "bash"}},
		TurnEnd:      []Hook{{Command: "log", Matcher: "^never_matches$"}},
	}, exec)

	r.Run(context.Background(), SessionStart, Input{Source: "startup"})
	r.Run(context.Background(), TurnEnd, Input{})
	calls := rec.snapshot()
	if len(calls) != 2 || calls[0].command != "notify" || calls[1].command != "log" {
		t.Errorf("lifecycle hooks with matchers did not all fire: %+v", calls)
	}
}

// Hooks dedup by command within an event at construction.
func TestRunnerDedupsByCommand(t *testing.T) {
	rec := &fakeRecorder{}
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		rec.record(command, cwd, stdin, env)
		return nil, nil, 0, nil
	}
	r := newTestRunner(t, Config{
		PreToolUse: []Hook{{Command: "same"}, {Command: "same"}, {Command: "other"}},
	}, exec)
	r.Run(context.Background(), PreToolUse, Input{ToolName: "bash"})
	if calls := rec.snapshot(); len(calls) != 2 {
		t.Errorf("ran %d hooks, want 2 after dedup", len(calls))
	}
}

// Matching hooks run in PARALLEL; aggregation stays in CONFIG order even
// when completion order is reversed.
func TestRunnerParallelExecConfigOrderAggregation(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	delays := map[string]time.Duration{"slow": 120 * time.Millisecond, "mid": 60 * time.Millisecond, "fast": 5 * time.Millisecond}
	var mu sync.Mutex
	var completion []string
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		cur := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(delays[command])
		mu.Lock()
		completion = append(completion, command)
		mu.Unlock()
		inFlight.Add(-1)
		return []byte(`{"decision":"deny","reason":"` + command + `"}`), nil, 0, nil
	}
	r := newTestRunner(t, Config{
		PreToolUse: []Hook{{Command: "slow"}, {Command: "mid"}, {Command: "fast"}},
	}, exec)

	start := time.Now()
	agg := r.Run(context.Background(), PreToolUse, Input{ToolName: "bash"})
	elapsed := time.Since(start)

	if elapsed >= 170*time.Millisecond {
		t.Errorf("hooks ran sequentially: %v (sum of delays is 185ms)", elapsed)
	}
	if maxInFlight.Load() < 2 {
		t.Errorf("max concurrency = %d, want parallel execution", maxInFlight.Load())
	}
	mu.Lock()
	gotCompletion := strings.Join(completion, ",")
	mu.Unlock()
	if gotCompletion != "fast,mid,slow" {
		t.Errorf("completion order = %s, want fast,mid,slow (test premise)", gotCompletion)
	}
	if agg.Reason != "slow\nmid\nfast" {
		t.Errorf("aggregated reason = %q, want config order slow\\nmid\\nfast", agg.Reason)
	}
	if agg.Decision != DecisionDeny {
		t.Errorf("decision = %v, want deny", agg.Decision)
	}
}

// A hook that yields past its deadline has NO opinion, whatever it printed.
func TestRunnerTimeoutIsNoOpinion(t *testing.T) {
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		// Late output must not count: the deadline already passed.
		return []byte(`{"decision":"deny","reason":"late"}`), []byte("late stderr"), 2, ctx.Err()
	}
	r := &Runner{
		config:                 map[Event][]compiledHook{PreToolUse: {{Hook: Hook{Command: "slow"}}}},
		exec:                   exec,
		DefaultTimeoutOverride: 30 * time.Millisecond,
		AbandonGraceOverride:   50 * time.Millisecond,
	}
	agg := r.Run(context.Background(), PreToolUse, Input{ToolName: "bash"})
	if agg.Decision != DecisionNone || agg.Halt {
		t.Errorf("timed-out hook had an opinion: %+v", agg)
	}
	if len(agg.Diagnostics) != 1 || !strings.Contains(agg.Diagnostics[0], "timed out") {
		t.Errorf("diagnostics = %v, want a timeout note", agg.Diagnostics)
	}
}

// Past the deadline the runner waits at most the abandon grace, then moves
// on; an executor that ignores cancellation is abandoned, not waited for.
func TestRunnerAbandonGraceBoundsTheWait(t *testing.T) {
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		// Deliberately ignores ctx: the runner must abandon it.
		time.Sleep(300 * time.Millisecond)
		return []byte(`{"decision":"deny"}`), nil, 0, nil
	}
	r := &Runner{
		config:                 map[Event][]compiledHook{PreToolUse: {{Hook: Hook{Command: "stubborn"}}}},
		exec:                   exec,
		DefaultTimeoutOverride: 30 * time.Millisecond,
		AbandonGraceOverride:   20 * time.Millisecond,
	}
	start := time.Now()
	agg := r.Run(context.Background(), PreToolUse, Input{ToolName: "bash"})
	elapsed := time.Since(start)

	if elapsed > 250*time.Millisecond {
		t.Errorf("runner waited %v for an abandoned hook; grace must bound the wait", elapsed)
	}
	if agg.Decision != DecisionNone {
		t.Errorf("abandoned hook had an opinion: %+v", agg)
	}
	if len(agg.Diagnostics) != 1 || !strings.Contains(agg.Diagnostics[0], "abandoned") {
		t.Errorf("diagnostics = %v, want an abandon note", agg.Diagnostics)
	}
}

// Parent cancellation yields no opinion without spending the abandon grace.
func TestRunnerParentCancelIsNoOpinion(t *testing.T) {
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return nil, nil, -1, ctx.Err()
	}
	r := &Runner{
		config:                 map[Event][]compiledHook{TurnEnd: {{Hook: Hook{Command: "x"}}}},
		exec:                   exec,
		DefaultTimeoutOverride: time.Minute,
		AbandonGraceOverride:   time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	agg := r.Run(ctx, TurnEnd, Input{})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("cancelled run spent %v, want prompt no-opinion", elapsed)
	}
	if agg.Decision != DecisionNone || agg.Halt {
		t.Errorf("cancelled hook had an opinion: %+v", agg)
	}
	if len(agg.Diagnostics) != 1 || !strings.Contains(agg.Diagnostics[0], "cancelled") {
		t.Errorf("diagnostics = %v, want a cancel note", agg.Diagnostics)
	}
}

// The executor receives the Claude Code-compatible stdin JSON, the cwd, and
// the scrubbed environment plus GOCODE_* additions.
func TestRunnerStdinEnvAndCWD(t *testing.T) {
	t.Setenv("GOCODE_TEST_API_KEY", "super-secret-value")
	rec := &fakeRecorder{}
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		rec.record(command, cwd, stdin, env)
		return nil, nil, 0, nil
	}
	r := newTestRunner(t, Config{PreToolUse: []Hook{{Command: "guard"}}}, exec)
	r.Run(context.Background(), PreToolUse, Input{
		SessionID: "s-1",
		CWD:       "C:/work",
		ToolName:  "bash",
		ToolInput: json.RawMessage(`{"command":"ls"}`),
		ToolUseID: "call_9",
	})
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("executor called %d times, want 1", len(calls))
	}
	call := calls[0]
	if call.cwd != "C:/work" {
		t.Errorf("cwd = %q", call.cwd)
	}
	var payload map[string]any
	if err := json.Unmarshal(call.stdin, &payload); err != nil {
		t.Fatalf("stdin not JSON: %v", err)
	}
	if payload["event"] != "PreToolUse" || payload["hook_event_name"] != "PreToolUse" {
		t.Errorf("event fields = %v / %v", payload["event"], payload["hook_event_name"])
	}
	if payload["session_id"] != "s-1" || payload["tool_name"] != "bash" || payload["tool_use_id"] != "call_9" {
		t.Errorf("payload identity fields wrong: %#v", payload)
	}
	if ti, ok := payload["tool_input"].(map[string]any); !ok || ti["command"] != "ls" {
		t.Errorf("tool_input not a parsed object: %#v", payload["tool_input"])
	}

	env := strings.Join(call.env, "\n")
	if strings.Contains(env, "super-secret-value") {
		t.Error("credential-shaped env var leaked to the hook child")
	}
	for _, want := range []string{"GOCODE_EVENT=PreToolUse", "GOCODE_SESSION_ID=s-1", "GOCODE_TOOL_NAME=bash", "GOCODE_TOOL_INPUT_COMMAND=ls", "CLAUDE_PROJECT_DIR=C:/work"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
}

// A tool event without input still delivers tool_input as {}.
func TestRunnerToolInputDefaultsToObject(t *testing.T) {
	rec := &fakeRecorder{}
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		rec.record(command, cwd, stdin, env)
		return nil, nil, 0, nil
	}
	r := newTestRunner(t, Config{PreToolUse: []Hook{{Command: "g"}}}, exec)
	r.Run(context.Background(), PreToolUse, Input{ToolName: "bash"})
	var payload map[string]any
	if err := json.Unmarshal(rec.snapshot()[0].stdin, &payload); err != nil {
		t.Fatalf("stdin not JSON: %v", err)
	}
	ti, ok := payload["tool_input"].(map[string]any)
	if !ok || len(ti) != 0 {
		t.Errorf("tool_input = %#v, want {}", payload["tool_input"])
	}
}

// No hooks for the event: the executor is never touched.
func TestRunnerNoMatchingHooksZeroWork(t *testing.T) {
	var called atomic.Bool
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		called.Store(true)
		return nil, nil, 0, nil
	}
	r := newTestRunner(t, Config{PreToolUse: []Hook{{Command: "g", Matcher: "^bash$"}}}, exec)
	agg := r.Run(context.Background(), PreToolUse, Input{ToolName: "file_read"})
	if called.Load() {
		t.Error("non-matching hook executed")
	}
	if agg.Decision != DecisionNone || len(agg.Results) != 0 {
		t.Errorf("unexpected aggregation: %+v", agg)
	}
	// A nil runner is inert too.
	var nilRunner *Runner
	if nilRunner.Has(PreToolUse) {
		t.Error("nil runner reports hooks")
	}
	if got := nilRunner.Run(context.Background(), PreToolUse, Input{}); got.Decision != DecisionNone {
		t.Error("nil runner produced an opinion")
	}
}

// Exit-code semantics end to end through Run: 49 denies AND halts.
func TestRunnerHaltExitCodeIntegration(t *testing.T) {
	exec := func(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
		return nil, []byte("security policy"), HaltExitCode, nil
	}
	r := newTestRunner(t, Config{PreToolUse: []Hook{{Command: "guard"}}}, exec)
	agg := r.Run(context.Background(), PreToolUse, Input{ToolName: "bash"})
	if agg.Decision != DecisionDeny || !agg.Halt || agg.Reason != "security policy" {
		t.Errorf("exit 49 aggregation = %+v", agg)
	}
}

// The real shell executor works on every platform (no skips): echo and a
// deliberate exit code through the platform shell (bash -c / cmd /C).
func TestShellExecutorRealSmoke(t *testing.T) {
	stdout, _, code, err := ShellExecutor(context.Background(), "echo hello", "", []byte("{}"), nil)
	if err != nil || code != 0 {
		t.Fatalf("echo: code=%d err=%v", code, err)
	}
	if !strings.Contains(string(stdout), "hello") {
		t.Errorf("echo stdout = %q", stdout)
	}
	_, _, code, err = ShellExecutor(context.Background(), "exit 3", "", nil, nil)
	if code != 3 {
		t.Errorf("exit code = %d (err=%v), want 3", code, err)
	}
}
