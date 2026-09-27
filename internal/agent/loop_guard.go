package agent

import (
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
)

// DefaultMaxToolIterations bounds how many provider round-trips a single user
// turn may make. Without a ceiling, a model that keeps requesting tools — or a
// tool that keeps failing in a way the model retries identically — produces an
// unbounded loop that burns tokens and API quota with no user-visible signal.
//
// The limit is deliberately generous: legitimate multi-step work rarely needs
// more than a few dozen tool round-trips in one turn.
const DefaultMaxToolIterations = 50

// DefaultMaxRepeatedToolCalls is how many times the identical tool call
// (same name and same arguments) may repeat within one turn before the loop
// stops. A model that retries the exact same call after the exact same result
// is not making progress and will not make progress with another attempt.
const DefaultMaxRepeatedToolCalls = 3

// LoopGuardConfig carries user-configurable limits. Zero values mean "use the
// built-in default", so an empty config block behaves identically to no block.
type LoopGuardConfig struct {
	// MaxIterations caps provider round-trips per turn.
	MaxIterations int
	// MaxRepeatedCalls caps identical tool calls (same name and arguments)
	// within one turn.
	MaxRepeatedCalls int
}

// LoopGuard bounds an agent turn. Both the plain loop (agent/loop.go) and the
// TUI loop (tui/run.go) construct one so the two paths cannot drift again on
// termination behaviour.
type LoopGuard struct {
	// MaxIterations caps provider round-trips per turn. Zero means
	// DefaultMaxToolIterations.
	MaxIterations int
	// MaxRepeatedCalls caps repeated identical tool calls. Zero means
	// DefaultMaxRepeatedToolCalls.
	MaxRepeatedCalls int
	// ToolCalls counts every tool call seen this turn, for reporting.
	ToolCalls int

	iterations     int
	recentCalls    map[string]int
	consecutiveRep int
}

// NewLoopGuard returns a guard with defaults applied.
func NewLoopGuard() *LoopGuard {
	return NewLoopGuardWithConfig(LoopGuardConfig{})
}

// NewLoopGuardWithConfig returns a guard configured from user settings,
// falling back to defaults for any unset or non-positive value.
func NewLoopGuardWithConfig(cfg LoopGuardConfig) *LoopGuard {
	g := &LoopGuard{
		MaxIterations:    cfg.MaxIterations,
		MaxRepeatedCalls: cfg.MaxRepeatedCalls,
		recentCalls:      make(map[string]int),
	}
	if g.MaxIterations <= 0 {
		g.MaxIterations = DefaultMaxToolIterations
	}
	if g.MaxRepeatedCalls <= 0 {
		g.MaxRepeatedCalls = DefaultMaxRepeatedToolCalls
	}
	return g
}

func (g *LoopGuard) maxIterations() int {
	if g.MaxIterations <= 0 {
		return DefaultMaxToolIterations
	}
	return g.MaxIterations
}

func (g *LoopGuard) maxRepeatedCalls() int {
	if g.MaxRepeatedCalls <= 0 {
		return DefaultMaxRepeatedToolCalls
	}
	return g.MaxRepeatedCalls
}

// CheckCall is called with each batch of tool calls the model requests. It
// records the calls and reports whether the turn may continue. A non-nil
// error means the turn must stop, and the message is suitable for showing to
// the user as the turn's final output.
//
// Calls must be recorded before Execute runs so that the cap also bounds a
// loop of *failing* tools, not only a loop of succeeding ones.
func (g *LoopGuard) CheckCall(toolCalls []provider.ToolCall) error {
	g.iterations++
	g.ToolCalls += len(toolCalls)

	if g.iterations > g.maxIterations() {
		return fmt.Errorf("stopped after %d tool iterations (limit %d) — the model kept requesting tools without finishing. Narrow the request and try again.",
			g.iterations-1, g.maxIterations())
	}

	if len(toolCalls) > 0 {
		// A batch containing any repeat beyond the limit stops the turn.
		// Consecutive identical calls are the common runaway shape: the model
		// retries a failing call with unchanged arguments.
		for _, tc := range toolCalls {
			key := tc.Name + "\x00" + string(tc.Args)
			g.recentCalls[key]++
			if g.recentCalls[key] > g.maxRepeatedCalls() {
				return fmt.Errorf("stopped: tool %q was called %d times with identical arguments. The previous attempts already produced a result; calling it again with the same input cannot change the outcome. Adjust the approach or the arguments.",
					tc.Name, g.recentCalls[key])
			}
		}
		// A batch that is entirely new work resets the consecutive counter.
		allNew := true
		for _, tc := range toolCalls {
			if g.recentCalls[tc.Name+"\x00"+string(tc.Args)] > 1 {
				allNew = false
				break
			}
		}
		if allNew {
			g.consecutiveRep = 0
		} else {
			g.consecutiveRep++
		}
	}

	return nil
}

// Summary returns a one-line description of the turn's tool usage, or an empty
// string when no tools were used.
func (g *LoopGuard) Summary() string {
	if g.ToolCalls == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("%d tool call(s)", g.ToolCalls)}
	if g.iterations > 1 {
		parts = append(parts, fmt.Sprintf("%d iteration(s)", g.iterations))
	}
	if g.iterations >= g.maxIterations() {
		parts = append(parts, "at iteration limit")
	}
	return "[GoCode] " + strings.Join(parts, ", ") + "."
}
