package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mevarx/GoCode/internal/provider"
)

// DefaultMaxToolIterations bounds provider round-trips per turn; without it a model
// requesting tools endlessly burns tokens and quota with no visible signal.
const DefaultMaxToolIterations = 50

// DefaultMaxRepeatedToolCalls caps identical repeats per turn; retrying the same
// call after the same result makes no progress.
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

// LoopGuard bounds an agent turn. Both plain and TUI loops use one so they
// cannot drift on termination behaviour.
type LoopGuard struct {
	// MaxIterations caps provider round-trips per turn. Zero means
	// DefaultMaxToolIterations.
	MaxIterations int
	// MaxRepeatedCalls caps repeated identical tool calls. Zero means
	// DefaultMaxRepeatedToolCalls.
	MaxRepeatedCalls int
	// ToolCalls counts every tool call seen this turn, for reporting.
	ToolCalls int

	iterations int
	// lastKeys holds canonical keys from the previous batch, so only consecutive
	// repeats count; cumulative counting aborted legitimate interleaved work.
	lastKeys map[string]bool
	// consecutiveRep counts consecutive batches repeating the preceding batch.
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
		lastKeys:         make(map[string]bool),
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

// CheckCall records a batch; non-nil error means the turn must stop (message is user-facing).
// Recorded before Execute so failing-tool loops are also bounded.
func (g *LoopGuard) CheckCall(toolCalls []provider.ToolCall) error {
	g.iterations++
	g.ToolCalls += len(toolCalls)

	if g.iterations > g.maxIterations() {
		return fmt.Errorf("stopped after %d tool iterations (limit %d) — the model kept requesting tools without finishing. Narrow the request and try again.",
			g.iterations-1, g.maxIterations())
	}

	if len(toolCalls) > 0 {
		// Only consecutive repeats count as runaway; interleaved calls reset the
		// streak, and keys are canonicalised so formatting cannot hide repeats.
		current := make(map[string]bool, len(toolCalls))
		repeatName := ""
		for _, tc := range toolCalls {
			key := canonicalCallKey(tc)
			current[key] = true
			if repeatName == "" && g.lastKeys[key] {
				repeatName = tc.Name
			}
		}
		if repeatName != "" {
			g.consecutiveRep++
			if g.consecutiveRep >= g.maxRepeatedCalls() {
				return fmt.Errorf("stopped: tool %q was called %d times in a row with identical arguments. The previous attempts already produced a result; calling it again with the same input cannot change the outcome. Adjust the approach or the arguments.",
					repeatName, g.consecutiveRep+1)
			}
		} else {
			g.consecutiveRep = 0
		}
		g.lastKeys = current
	}

	return nil
}

// canonicalCallKey normalises a call so semantically identical args compare equal.
// Non-JSON args fall back to raw bytes so malformed calls still deduplicate.
func canonicalCallKey(tc provider.ToolCall) string {
	args := strings.TrimSpace(string(tc.Args))
	var v any
	if err := json.Unmarshal([]byte(args), &v); err == nil {
		if canon, err := json.Marshal(v); err == nil {
			args = string(canon)
		}
	}
	return tc.Name + "\x00" + args
}

// Summary describes the turn's tool usage, or "" when no tools were used.
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
