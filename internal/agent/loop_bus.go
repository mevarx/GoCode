package agent

import (
	"context"

	"github.com/mevarx/GoCode/internal/pubsub"
)

// Bus delivery classes for LoopEvent:
//
// - EventDelta / EventToolDelta: lossy, NO coalescing key. DEVIATION from
// the brief's suggested Key="msg"/toolCallID, forced by the v0.7.5
// headless contract: GoCode deltas are stream FRAMES (text fragments and
// per-frame arg previews), not whole-message snapshots. The
// headless protocol delivers every frame, and coalescing same-key frames
// deterministically drops records and streamed text (existing headless
// tests fail under the keyed mapping). The broker's coalescing lane is
// fully implemented and contract-tested for snapshot-style publishers
// (message/session updates); a frame stream must not use it.
// - EventToolResult / EventError / EventExit / EventTurnEnd: must-deliver.
// Terminal events bound the publisher at 50ms per subscriber instead of
// blocking it, and a stalled subscriber's loss is counted, never silent.
// - EventTurnStart / EventToolStart / EventNotice: lossy, no coalescing key.
//
// The mapping lives here, next to the loop, so every emit site inherits it
// and the exactly-once EventTurnEnd funnel publishes exactly once too.

// busEventFor classifies one loop event onto the bus. Exported shape only via
// the Bus field; the mapping itself is an internal contract tested in
// loop_bus_test.go.
func busEventFor(ev LoopEvent) pubsub.Event[LoopEvent] {
	be := pubsub.Event[LoopEvent]{
		Type:    pubsub.TypeAgentEvent,
		Payload: ev,
	}
	switch ev.Kind {
	case EventDelta, EventToolDelta:
		be.Type = pubsub.TypeMessage
	case EventToolResult, EventError, EventExit:
		be.MustDeliver = true
	case EventTurnEnd:
		be.Type = pubsub.TypeRunComplete
		be.MustDeliver = true
	case EventPermissionRequest:
		// permission vocabulary: must-deliver, unkeyed (every prompt and
		// every lifecycle transition is distinct; coalescing would drop one).
		be.Type = pubsub.TypePermissionRequest
		be.MustDeliver = true
	case EventPermissionNotification:
		be.Type = pubsub.TypePermissionNotification
		be.MustDeliver = true
	case EventPermissionModeChanged:
		be.Type = pubsub.TypePermissionModeChanged
		be.MustDeliver = true
	}
	return be
}

// publishBus sends one event on the loop's bus. Must-deliver events use the
// bounded blocking lane; everything else is non-blocking lossy. The context
// is Background because emit has no turn context at every call site — the
// 50ms per-subscriber bound still caps any wait, and cancellation of the turn
// itself is handled by the loop, not the bus.
func (a *AgentLoop) publishBus(ev LoopEvent) {
	be := busEventFor(ev)
	if be.MustDeliver {
		a.Bus.PublishMustDeliver(context.Background(), be)
		return
	}
	a.Bus.Publish(be)
}
