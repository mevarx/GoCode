// Package hooks implements the hook engine:
// shell hooks on 13 lifecycle events with Claude Code-compatible stdin/stdout
// JSON payloads, exit-code semantics, parallel execution with per-hook
// timeouts, and config-order aggregation.
//
// Hook commands are trusted user configuration: they run through the platform
// shell without a sandbox. The security boundary is elsewhere and stays
// unconditional — a hook allow can never bypass the permission deny-list or
// transcript redaction, and a hook approval is per-call only.
package hooks

import "strings"

// Event is one of the 13 hook lifecycle events.
type Event string

const (
	// PreToolUse runs before a tool call; it may allow, deny, halt, rewrite
	// the input or add context.
	PreToolUse Event = "PreToolUse"
	// PostToolUse runs after a successful tool call; it may rewrite the
	// output, add context or halt.
	PostToolUse Event = "PostToolUse"
	// PostToolUseFailure runs after a failed tool call (Go error or an error
	// result); it may add context or halt.
	PostToolUseFailure Event = "PostToolUseFailure"
	// PermissionRequest is informational: a permission prompt was shown.
	PermissionRequest Event = "PermissionRequest"
	// PermissionResult is informational: a prompt resolved (carries decision).
	PermissionResult Event = "PermissionResult"
	// SessionStart fires when a session is created or resumed (source field).
	SessionStart Event = "SessionStart"
	// SessionEnd fires at process exit (reason field, 2s budget).
	SessionEnd Event = "SessionEnd"
	// TurnStart fires before the provider stream of a model turn.
	TurnStart Event = "TurnStart"
	// TurnEnd fires when a turn finishes successfully (last_assistant_message).
	TurnEnd Event = "TurnEnd"
	// StopFailure fires when a turn ended with a provider error.
	StopFailure Event = "StopFailure"
	// Interrupt fires when a turn was cancelled.
	Interrupt Event = "Interrupt"
	// PreCompact fires before a compaction summary request; deny BLOCKS
	// compaction.
	PreCompact Event = "PreCompact"
	// PostCompact fires after a compaction summary was persisted.
	PostCompact Event = "PostCompact"
)

// AllEvents lists every event in spec order. Exactly one of TurnEnd,
// StopFailure and Interrupt fires per finished model turn.
var AllEvents = []Event{
	PreToolUse, PostToolUse, PostToolUseFailure,
	PermissionRequest, PermissionResult,
	SessionStart, SessionEnd,
	TurnStart, TurnEnd, StopFailure, Interrupt,
	PreCompact, PostCompact,
}

// toolEvents carry a tool name and honor the matcher regex; every other event
// is a lifecycle event whose hooks ignore the matcher.
var toolEvents = map[Event]bool{
	PreToolUse:         true,
	PostToolUse:        true,
	PostToolUseFailure: true,
	PermissionRequest:  true,
	PermissionResult:   true,
}

// IsToolEvent reports whether the event carries a tool call (matcher applies).
func (e Event) IsToolEvent() bool { return toolEvents[e] }

// String returns the canonical event name.
func (e Event) String() string { return string(e) }

// NormalizeEvent resolves an event name case-insensitively with underscores
// removed, so "pre_tool_use" and "PRETOOLUSE" both resolve to PreToolUse
// Unknown names report false.
func NormalizeEvent(name string) (Event, bool) {
	key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "_", ""))
	for _, e := range AllEvents {
		if strings.ToLower(string(e)) == key {
			return e, true
		}
	}
	return "", false
}
