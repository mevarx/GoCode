package agent

import (
	"context"

	"github.com/mevarx/GoCode/internal/pubsub"
	"github.com/mevarx/GoCode/internal/tools"
)

// permission bus wiring. The approval
// gate cannot import the agent package, so it publishes through the
// tools.PermissionBus interface; AgentLoop implements it by emitting the
// permission LoopEvent kinds, which busEventFor maps onto the must-deliver
// permission_request / permission_notification / permission_mode_changed
// lanes of the SAME bus the UI and headless already consume.
//
// The events ride emit, so the legacy Observe chain and the bus stay in
// lockstep (the bus≡Observe ordering contract). Front-ends that do not
// render permission kinds ignore them: the TUI's prompt transport remains
// the approval bridge this milestone, and the headless JSONL schema is
// unchanged (additive-only — no new record types are emitted yet).
var _ tools.PermissionBus = (*AgentLoop)(nil)

// PublishPermissionRequest opens a prompt on the bus. The context is ignored
// in favor of the emit path's bounded must-deliver lane: a cancelled turn
// must still deliver the prompt's terminal notifications.
func (a *AgentLoop) PublishPermissionRequest(_ context.Context, req pubsub.PermissionRequest) {
	a.emit(LoopEvent{
		Kind:              EventPermissionRequest,
		Tool:              req.Tool,
		ToolCallID:        req.ToolCallID,
		PermissionRequest: &req,
	})
}

// PublishPermissionNotification reports one lifecycle transition
// (requested/granted/denied/cancelled) on the bus.
func (a *AgentLoop) PublishPermissionNotification(_ context.Context, n pubsub.PermissionNotification) {
	a.emit(LoopEvent{
		Kind:                   EventPermissionNotification,
		Tool:                   n.Tool,
		PermissionNotification: &n,
	})
}

// PublishPermissionModeChanged reports a permission mode switch on the bus.
func (a *AgentLoop) PublishPermissionModeChanged(_ context.Context, change pubsub.PermissionModeChanged) {
	a.emit(LoopEvent{
		Kind:                  EventPermissionModeChanged,
		Text:                  change.Mode,
		PermissionModeChanged: &change,
	})
}
