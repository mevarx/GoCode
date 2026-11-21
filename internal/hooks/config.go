package hooks

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Engine constants.
const (
	// DefaultTimeout bounds one hook command when its config timeout is unset.
	DefaultTimeout = 30 * time.Second
	// AbandonGrace is how long the runner waits past a hook's deadline for the
	// executor to yield before abandoning the goroutine (no opinion either way).
	AbandonGrace = time.Second
	// HaltExitCode is the exit code meaning "deny AND halt the turn". It sits
	// outside the generic-error (1–30), sysexits (64–78) and signal (128+)
	// ranges so it cannot be produced by accident.
	HaltExitCode = 49
	// SessionEndBudget bounds the whole SessionEnd firing at process exit.
	SessionEndBudget = 2 * time.Second
	// DefaultTimeoutSeconds is DefaultTimeout expressed for config files.
	DefaultTimeoutSeconds = 30
)

// Hook is one configured hook command. Timeout is in seconds;
// zero selects DefaultTimeout. Matcher is a regex tested against the tool
// name on tool events (empty matches all) and is ignored on lifecycle events.
type Hook struct {
	Name    string `toml:"name" json:"name,omitempty"`
	Matcher string `toml:"matcher" json:"matcher,omitempty"`
	Command string `toml:"command" json:"command"`
	Timeout int    `toml:"timeout" json:"timeout,omitempty"`
}

// Config maps each event to its hooks in configuration order. Aggregation
// always follows this order, never completion order.
type Config map[Event][]Hook

// NormalizeConfig validates and canonicalizes a raw hook configuration:
// event names are normalized (case-insensitive, underscores stripped),
// unknown events and empty commands are rejected, matchers must compile,
// negative timeouts are rejected, unset timeouts default to 30s, and hooks
// are deduplicated by command within each event (first occurrence wins).
// It is idempotent, so config load and runner construction can both call it.
func NormalizeConfig(raw Config) (Config, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(Config, len(raw))
	for name, list := range raw {
		event, ok := NormalizeEvent(string(name))
		if !ok {
			return nil, fmt.Errorf("hooks: unknown event %q", name)
		}
		seen := make(map[string]bool, len(list))
		for _, h := range list {
			command := strings.TrimSpace(h.Command)
			if command == "" {
				return nil, fmt.Errorf("hooks.%s: command is required", event)
			}
			if h.Matcher != "" {
				if _, err := regexp.Compile(h.Matcher); err != nil {
					return nil, fmt.Errorf("hooks.%s: invalid matcher %q: %v", event, h.Matcher, err)
				}
			}
			if h.Timeout < 0 {
				return nil, fmt.Errorf("hooks.%s: timeout must not be negative (got %d)", event, h.Timeout)
			}
			if seen[command] {
				continue
			}
			seen[command] = true
			timeout := h.Timeout
			if timeout == 0 {
				timeout = DefaultTimeoutSeconds
			}
			out[event] = append(out[event], Hook{
				Name:    strings.TrimSpace(h.Name),
				Matcher: h.Matcher,
				Command: command,
				Timeout: timeout,
			})
		}
	}
	return out, nil
}

// label names a hook in diagnostics: its configured name, else its command.
func (h Hook) label() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Command
}
