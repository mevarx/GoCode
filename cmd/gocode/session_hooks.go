package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/hooks"
)

// session lifecycle hooks: SessionStart fires
// when the process creates or resumes a session (source startup|resume), and
// SessionEnd fires on every exit path with reason "exit" under a 2s budget.
// Both fire in every mode — headless parity skips TURN hooks only.

// buildHookRunner constructs the shared hook engine from validated config.
// No hooks configured returns nil, which keeps every hook path a nil check
// (zero overhead). Credential-shaped env names from config are scrubbed from
// hook child environments on top of the built-in scrub.
func buildHookRunner(cfg config.Config) (*hooks.Runner, error) {
	if len(cfg.Hooks) == 0 {
		return nil, nil
	}
	runner, err := hooks.NewRunner(cfg.Hooks, nil)
	if err != nil {
		return nil, err
	}
	runner.EnvScrub = config.SecretEnvNames(cfg)
	return runner, nil
}

// fireSessionStart runs the SessionStart hooks for a new or resumed session.
func fireSessionStart(runner *hooks.Runner, sessionID, cwd, source string) {
	if !runner.Has(hooks.SessionStart) {
		return
	}
	agg := runner.Run(context.Background(), hooks.SessionStart, hooks.Input{
		SessionID: sessionID,
		CWD:       cwd,
		Source:    source,
	})
	logHookDiagnostics(hooks.SessionStart, agg)
}

// fireSessionEnd runs the SessionEnd hooks at process exit under a 2s
// budget. The runner's per-hook timeout cannot outlive it: a spent parent
// context gets no abandon grace.
func fireSessionEnd(runner *hooks.Runner, sessionID, cwd string) {
	if !runner.Has(hooks.SessionEnd) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hooks.SessionEndBudget)
	defer cancel()
	agg := runner.Run(ctx, hooks.SessionEnd, hooks.Input{
		SessionID: sessionID,
		CWD:       cwd,
		Reason:    "exit",
	})
	logHookDiagnostics(hooks.SessionEnd, agg)
}

// logHookDiagnostics records abnormal hook outcomes through the application
// logger (the engine itself has no logger dependency).
func logHookDiagnostics(event hooks.Event, agg hooks.Aggregated) {
	if len(agg.Diagnostics) == 0 {
		return
	}
	slog.Warn("hook diagnostics", "event", string(event), "detail", strings.Join(agg.Diagnostics, "; "))
}
