package pubsub

import (
	"context"
	"encoding/json"
)

// Wire-vocabulary discriminators.
// Every event that crosses a process boundary is {type, payload}; the tags
// are stable strings, additive only.
const (
	// TypeMessage carries streaming message parts (deltas, tool arg previews).
	TypeMessage = "message"
	// TypePermissionRequest opens a permission prompt (publishes it).
	TypePermissionRequest = "permission_request"
	// TypePermissionNotification reports a prompt's state transitions.
	TypePermissionNotification = "permission_notification"
	// TypePermissionModeChanged reports a permission mode switch.
	TypePermissionModeChanged = "permission_mode_changed"
	// TypeRunComplete reports a finished turn/run.
	TypeRunComplete = "run_complete"
	// TypeSession carries session lifecycle/state changes.
	TypeSession = "session"
	// TypeAgentEvent carries agent notices, errors and tool events.
	TypeAgentEvent = "agent_event"
)

// PermissionRequest is the payload of a permission prompt.
// defined the shape and lanes; adds the keyed-grant fields and starts
// publishing it from the approval gate. Args is the raw tool-call JSON the
// user is asked to approve — the same bytes the gate already presents, so
// redaction stays the gate's job.
//
// The wire uses `tool`/`args` field names; the shape is the published
// contract and the schema is additive-only. Path is normalized to a DIRECTORY and GrantKey
// narrows the grant (shell tools: the command string).
type PermissionRequest struct {
	ID          string          `json:"id"`
	Tool        string          `json:"tool"`
	Action      string          `json:"action,omitempty"`
	Args        json.RawMessage `json:"args,omitempty"`
	Description string          `json:"description,omitempty"`
	Danger      bool            `json:"danger,omitempty"`
	// DangerReason carries the classifier's human-readable reason.
	DangerReason string `json:"danger_reason,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	Path         string `json:"path,omitempty"`
	GrantKey     string `json:"grant_key,omitempty"`
}

// PermissionNotification is the prompt state machine the UI renders: all three
// flags false means a prompt is OPEN; exactly one of
// Granted, Denied, Cancelled is true once it resolves. Cancelled means the
// turn was cancelled while the prompt was open.
type PermissionNotification struct {
	ID        string `json:"id"`
	Tool      string `json:"tool,omitempty"`
	Granted   bool   `json:"granted"`
	Denied    bool   `json:"denied"`
	Cancelled bool   `json:"cancelled"`
	// Reason carries denial/cancellation detail (additive).
	Reason string `json:"reason,omitempty"`
}

// Open reports whether the notification describes an open prompt (the
// all-false state).
func (n PermissionNotification) Open() bool {
	return !n.Granted && !n.Denied && !n.Cancelled
}

// PublishPermissionRequest publishes a prompt opening on the permission
// request lane. Must-deliver: a UI that never sees the prompt
// cannot answer it. Nil-safe so can wire it incrementally. No coalescing
// key: every request is a distinct prompt.
func PublishPermissionRequest(ctx context.Context, b *Broker[PermissionRequest], req PermissionRequest) {
	if b == nil {
		return
	}
	b.PublishMustDeliver(ctx, Event[PermissionRequest]{
		Type:        TypePermissionRequest,
		Payload:     req,
		MustDeliver: true,
	})
}

// PublishPermissionNotification publishes a prompt state transition on the
// permission notification lane. Must-deliver and unkeyed: the UI must see
// every transition (open → granted/denied/cancelled), not just the latest.
// Nil-safe for incremental wiring.
func PublishPermissionNotification(ctx context.Context, b *Broker[PermissionNotification], n PermissionNotification) {
	if b == nil {
		return
	}
	b.PublishMustDeliver(ctx, Event[PermissionNotification]{
		Type:        TypePermissionNotification,
		Payload:     n,
		MustDeliver: true,
	})
}

// PermissionModeChanged reports a permission mode switch (: mode
// changes publish on the bus). Previous is empty for the initial install.
type PermissionModeChanged struct {
	Mode     string `json:"mode"`
	Previous string `json:"previous,omitempty"`
}

// PublishPermissionModeChanged publishes a mode switch. Must-deliver and
// unkeyed: observers must see every transition. Nil-safe for incremental
// wiring.
func PublishPermissionModeChanged(ctx context.Context, b *Broker[PermissionModeChanged], change PermissionModeChanged) {
	if b == nil {
		return
	}
	b.PublishMustDeliver(ctx, Event[PermissionModeChanged]{
		Type:        TypePermissionModeChanged,
		Payload:     change,
		MustDeliver: true,
	})
}
