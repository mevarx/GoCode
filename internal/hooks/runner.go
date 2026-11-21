package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/mevarx/GoCode/internal/tools"
)

// Executor runs one hook command and reports its raw outcome. It is the
// injection seam: contract tests supply a fake, production uses ShellExecutor.
// A nil exitCode with non-nil err means the process never started (or was
// killed by its context); the runner classifies context errors itself.
type Executor func(ctx context.Context, command, cwd string, stdin []byte, env []string) (stdout, stderr []byte, exitCode int, err error)

// maxHookOutputBytes caps captured stdout/stderr per hook so a chatty command
// cannot balloon memory; the JSON contract only needs a bounded prefix.
const maxHookOutputBytes = 1 << 20

// ShellExecutor is the real executor. The command runs through the platform
// shell — "bash -c" on unix, "cmd /C" on windows — in the payload's cwd, with
// the payload JSON on stdin and the scrubbed environment plus GOCODE_*
// additions. Hook commands are trusted user configuration: no sandbox is
// applied, but credential-shaped environment variables are still scrubbed so
// a hook child never inherits provider API keys by default.
func ShellExecutor(ctx context.Context, command, cwd string, stdin []byte, env []string) ([]byte, []byte, int, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &cappedBuffer{}
	stderr := &cappedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if env != nil {
		cmd.Env = env
	}
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return stdout.Bytes(), stderr.Bytes(), code, err
}

// cappedBuffer keeps at most limit bytes and is safe for concurrent writes
// (os/exec copies stdout and stderr on separate goroutines).
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxHookOutputBytes - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// compiledHook pairs a configured hook with its compiled matcher. A nil re
// with a compileErr means the matcher was invalid; the hook never matches and
// the error surfaces as a diagnostic (config load rejects these up front).
type compiledHook struct {
	Hook
	re         *regexp.Regexp
	compileErr string
}

// Runner executes the configured hooks for an event. The zero value is not
// usable; construct with NewRunner. A nil *Runner is safe and inert, so
// callers without hooks pay nothing.
type Runner struct {
	config map[Event][]compiledHook
	exec   Executor
	// EnvScrub lists extra credential variable names/patterns to drop from
	// hook child environments, on top of the built-in scrub.
	EnvScrub []string
	// DefaultTimeoutOverride and AbandonGraceOverride let tests shrink the
	// budgets; zero selects the defaults (30s / 1s).
	DefaultTimeoutOverride time.Duration
	AbandonGraceOverride   time.Duration
}

// NewRunner normalizes the configuration and builds a runner. A nil executor
// selects ShellExecutor. Normalization is idempotent, so an already-validated
// config passes through unchanged.
func NewRunner(cfg Config, exec Executor) (*Runner, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if exec == nil {
		exec = ShellExecutor
	}
	r := &Runner{
		config: make(map[Event][]compiledHook, len(normalized)),
		exec:   exec,
	}
	for event, list := range normalized {
		for _, h := range list {
			ch := compiledHook{Hook: h}
			if h.Matcher != "" {
				re, err := regexp.Compile(h.Matcher)
				if err != nil {
					ch.compileErr = err.Error()
				} else {
					ch.re = re
				}
			}
			r.config[event] = append(r.config[event], ch)
		}
	}
	return r, nil
}

// Has reports whether any hook is configured for the event. Nil-safe: a nil
// runner has no hooks, which keeps the no-hooks path allocation-free.
func (r *Runner) Has(event Event) bool {
	return r != nil && len(r.config[event]) > 0
}

func (r *Runner) defaultTimeout() time.Duration {
	if r.DefaultTimeoutOverride > 0 {
		return r.DefaultTimeoutOverride
	}
	return DefaultTimeout
}

func (r *Runner) abandonGrace() time.Duration {
	if r.AbandonGraceOverride > 0 {
		return r.AbandonGraceOverride
	}
	return AbandonGrace
}

// match selects the hooks for one firing in config order. On tool events the
// matcher regex filters by tool name (empty matches all); on lifecycle events
// the matcher is ignored. Invalid matchers contribute a diagnostic
// and never match.
func (r *Runner) match(event Event, toolName string) ([]compiledHook, []string) {
	var matched []compiledHook
	var diags []string
	for _, h := range r.config[event] {
		if h.compileErr != "" {
			diags = append(diags, diagnosticf("hook %q has an invalid matcher: %s", h.label(), h.compileErr))
			continue
		}
		if event.IsToolEvent() && h.re != nil && !h.re.MatchString(toolName) {
			continue
		}
		matched = append(matched, h)
	}
	return matched, diags
}

// Run fires every matching hook for the event IN PARALLEL and aggregates the
// results in config order. Each hook gets its own timeout
// (default 30s); past the deadline the runner waits at most the abandon grace
// (1s) for the executor to yield, then counts the hook as no opinion. Parent
// cancellation also yields no opinion. Diagnostics ride the returned
// Aggregated — the engine has no logger dependency.
func (r *Runner) Run(ctx context.Context, event Event, in Input) Aggregated {
	if r == nil {
		return Aggregated{}
	}
	matched, diags := r.match(event, in.ToolName)
	if len(matched) == 0 {
		return Aggregated{Diagnostics: diags}
	}

	in.Event = event
	in.HookEventName = string(event)
	if event.IsToolEvent() && len(in.ToolInput) == 0 {
		in.ToolInput = json.RawMessage("{}")
	}
	stdin, err := marshalStdin(in)
	if err != nil {
		return Aggregated{Diagnostics: append(diags, diagnosticf("payload marshal failed: %v", err))}
	}
	env := append(tools.ScrubbedEnv(r.EnvScrub), EnvAdditions(in)...)

	results := make([]HookResult, len(matched))
	var wg sync.WaitGroup
	for i, h := range matched {
		wg.Add(1)
		go func(i int, h compiledHook) {
			defer wg.Done()
			results[i] = r.runOne(ctx, event, h, in.CWD, stdin, env)
		}(i, h)
	}
	wg.Wait()

	agg := Aggregate(results, in.ToolInput)
	agg.Diagnostics = append(diags, agg.Diagnostics...)
	return agg
}

// runOne executes a single hook under its own deadline and classifies the
// outcome. The executor goroutine writes to a buffered channel, so an
// abandoned hook can still finish later without leaking a blocked send.
func (r *Runner) runOne(ctx context.Context, event Event, h compiledHook, cwd string, stdin []byte, env []string) HookResult {
	base := HookResult{Name: h.Name, Event: event, Matcher: h.Matcher, Command: h.Command}

	timeout := r.defaultTimeout()
	if h.Timeout > 0 {
		timeout = time.Duration(h.Timeout) * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type execOutcome struct {
		stdout, stderr []byte
		code           int
		err            error
	}
	ch := make(chan execOutcome, 1)
	go func() {
		stdout, stderr, code, err := r.exec(hctx, h.Command, cwd, stdin, env)
		ch <- execOutcome{stdout, stderr, code, err}
	}()

	var eo execOutcome
	select {
	case eo = <-ch:
	case <-hctx.Done():
		// Deadline or parent cancel. The executor's context is done too, so a
		// well-behaved command yields promptly; give it the abandon grace,
		// then move on with no opinion. A cancelled PARENT gets no grace: its
		// budget (e.g. SessionEnd's 2s) is already spent.
		if ctx.Err() == nil {
			timer := time.NewTimer(r.abandonGrace())
			select {
			case eo = <-ch:
			case <-timer.C:
				base.Diagnostic = diagnosticf("hook %q abandoned after its %s timeout", h.label(), timeout)
				return base
			}
			timer.Stop()
		} else {
			select {
			case eo = <-ch:
			default:
				base.Diagnostic = diagnosticf("hook %q cancelled: %v", h.label(), ctx.Err())
				return base
			}
		}
	}

	// A hook that yielded past its deadline (or after parent cancellation)
	// has no opinion, whatever it printed.
	if hctx.Err() != nil {
		if errors.Is(hctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			base.Diagnostic = diagnosticf("hook %q timed out after %s", h.label(), timeout)
		} else {
			base.Diagnostic = diagnosticf("hook %q cancelled: %v", h.label(), hctx.Err())
		}
		return base
	}
	if eo.err != nil {
		base.Diagnostic = diagnosticf("hook %q failed to run: %v", h.label(), eo.err)
		return base
	}
	return classifyExit(base, eo.stdout, eo.stderr, eo.code)
}
