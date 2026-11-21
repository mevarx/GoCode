package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/pubsub"
)

// hookApprovalGate builds a gate whose prompt hook FAILS the test if reached:
// a consumed hook approval must short-circuit the prompt entirely.
func hookApprovalGate(t *testing.T) (*ApprovalGate, *fakePermBus, *bool) {
	t.Helper()
	gate := NewApprovalGate()
	bus := &fakePermBus{}
	gate.Bus = bus
	prompted := false
	gate.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		prompted = true
		return ApprovalDeny, nil
	}
	return gate, bus, &prompted
}

func runPrepared(t *testing.T, execute func(context.Context) (Result, error)) Result {
	t.Helper()
	result, err := execute(context.Background())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return result
}

// A stamped call short-circuits the prompt, executes, and publishes a
// Granted notification for audit.
func TestHookApprovalShortCircuitsPromptWithGrantedAudit(t *testing.T) {
	gate, bus, prompted := hookApprovalGate(t)
	executed := false
	tool := decisionTool{name: "guarded", requires: true, executed: &executed}

	gate.GrantHookApproval("call-1")
	ctx := WithHookApproval(context.Background(), "call-1")
	execute, err := gate.PrepareWithID(ctx, tool, json.RawMessage(`{"x":1}`), "call-1")
	if err != nil {
		t.Fatalf("PrepareWithID: %v", err)
	}
	result := runPrepared(t, execute)

	if !executed {
		t.Error("hook-approved tool did not execute")
	}
	if *prompted {
		t.Error("hook approval did not short-circuit the prompt")
	}
	if result.Output != "ran" {
		t.Errorf("output = %q, want the plain tool output", result.Output)
	}
	reqs, notifs, _ := bus.snapshot()
	if len(reqs) != 0 {
		t.Errorf("hook approval published %d prompt requests, want 0", len(reqs))
	}
	var granted int
	for _, n := range notifs {
		if n.Granted {
			granted++
		}
	}
	if granted != 1 {
		t.Errorf("Granted notifications = %d, want exactly 1 for audit", granted)
	}
	if gate.hookApprovalCount() != 0 {
		t.Error("stamp was not consumed")
	}
}

// The stamp is consumed exactly once: a replay falls through to the prompt.
func TestHookApprovalConsumedExactlyOnce(t *testing.T) {
	gate, _, prompted := hookApprovalGate(t)
	executed := false
	tool := decisionTool{name: "guarded", requires: true, executed: &executed}

	gate.GrantHookApproval("call-1")
	ctx := WithHookApproval(context.Background(), "call-1")
	execute, _ := gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "call-1")
	runPrepared(t, execute)
	if !executed {
		t.Fatal("first call did not execute")
	}

	// Same ctx, same id, second prepare: the registration is gone, so the
	// normal prompt flow decides (the fake prompt denies).
	executed = false
	execute, _ = gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "call-1")
	result := runPrepared(t, execute)
	if executed {
		t.Error("replayed stamp approved a second call")
	}
	if !*prompted {
		t.Error("replay did not fall through to the prompt")
	}
	if !strings.Contains(result.Output, "denied") {
		t.Errorf("replay result = %q, want the prompt denial", result.Output)
	}
}

// SECURITY: the deny-list outranks a hook approval, unconditionally.
func TestHookApprovalDenyListWins(t *testing.T) {
	gate, bus, prompted := hookApprovalGate(t)
	gate.SetPermissions(nil, []string{"guarded"})
	executed := false
	tool := decisionTool{name: "guarded", requires: true, executed: &executed}

	gate.GrantHookApproval("call-1")
	ctx := WithHookApproval(context.Background(), "call-1")
	execute, _ := gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "call-1")
	result := runPrepared(t, execute)

	if executed {
		t.Error("hook allow bypassed the deny-list")
	}
	if *prompted {
		t.Error("deny-list denial prompted the user")
	}
	if !strings.Contains(result.Error, "denied by permissions configuration") {
		t.Errorf("result = %+v, want the deny-list wording", result)
	}
	reqs, notifs, _ := bus.snapshot()
	if len(reqs) != 0 {
		t.Error("deny-list path published a prompt")
	}
	for _, n := range notifs {
		if n.Granted {
			t.Error("deny-list path published a Granted notification")
		}
	}
	if gate.hookApprovalCount() != 1 {
		t.Error("deny-list must not consume the stamp (it never reached the slot)")
	}
}

// SECURITY: yolo's dangerous-call refusal also outranks the hook slot.
func TestHookApprovalYoloDangerRefusalWins(t *testing.T) {
	gate, _, _ := hookApprovalGate(t)
	if err := gate.SetMode(ModeYolo); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	gate.SetDangerTools([]string{"guarded"})
	executed := false
	tool := decisionTool{name: "guarded", requires: true, executed: &executed}

	gate.GrantHookApproval("call-1")
	ctx := WithHookApproval(context.Background(), "call-1")
	execute, _ := gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "call-1")
	result := runPrepared(t, execute)

	if executed {
		t.Error("hook allow bypassed yolo's dangerous-call refusal")
	}
	if !strings.Contains(result.Error, "refused in yolo mode") {
		t.Errorf("result = %+v, want the yolo refusal", result)
	}
}

// The stamp is per-CALL: it cannot approve a different toolCallID, and an
// unconsumed stamp never outlives its turn.
func TestHookApprovalPerCallOnly(t *testing.T) {
	gate, _, prompted := hookApprovalGate(t)
	executed := false
	tool := decisionTool{name: "guarded", requires: true, executed: &executed}

	gate.GrantHookApproval("call-1")
	// A different call id with its own (unstamped) ctx must prompt.
	execute, _ := gate.PrepareWithID(context.Background(), tool, json.RawMessage(`{}`), "call-2")
	runPrepared(t, execute)
	if executed || !*prompted {
		t.Error("stamp for call-1 approved call-2")
	}

	// Even the SAME id without the ctx stamp must prompt: both halves are
	// required, so a leaked registration alone grants nothing.
	*prompted = false
	execute, _ = gate.PrepareWithID(context.Background(), tool, json.RawMessage(`{}`), "call-1")
	runPrepared(t, execute)
	if !*prompted {
		t.Error("registration without ctx stamp approved a call")
	}

	// BeginTurn clears unconsumed registrations.
	gate.BeginTurn()
	if gate.hookApprovalCount() != 0 {
		t.Error("BeginTurn left hook approvals behind")
	}
}

// Decision order: config auto_approve is checked BEFORE the hook slot (the
// stamp stays unconsumed), and the hook slot BEFORE keyed grants (the result
// carries no "(session grant)" marker when the hook path wins).
func TestHookApprovalDecisionOrder(t *testing.T) {
	// auto_approve wins first: the stamp is not consumed.
	gate := NewApprovalGateWithPermissions([]string{"guarded"}, nil)
	gate.GrantHookApproval("call-1")
	ctx := WithHookApproval(context.Background(), "call-1")
	tool := decisionTool{name: "guarded", requires: true}
	execute, _ := gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "call-1")
	runPrepared(t, execute)
	if gate.hookApprovalCount() != 1 {
		t.Error("auto_approve path consumed the hook stamp; it must be checked after")
	}

	// Hook slot beats an existing keyed grant for the same args.
	gate2, _, prompted := hookApprovalGate(t)
	args := json.RawMessage(`{"x":1}`)
	gate2.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		return ApprovalGrantExact, nil
	}
	execute, _ = gate2.PrepareWithID(context.Background(), tool, args, "seed")
	runPrepared(t, execute) // records the keyed grant
	if gate2.SessionGrantCount() != 1 {
		t.Fatal("seed call did not record a keyed grant")
	}
	gate2.OnPresentChoice = func(string, json.RawMessage, string) (ApprovalChoice, error) {
		*prompted = true
		return ApprovalDeny, nil
	}
	gate2.GrantHookApproval("call-2")
	ctx2 := WithHookApproval(context.Background(), "call-2")
	execute, _ = gate2.PrepareWithID(ctx2, tool, args, "call-2")
	result := runPrepared(t, execute)
	if *prompted {
		t.Error("hook-approved call prompted despite the stamp")
	}
	if strings.Contains(result.Output, "(session grant)") {
		t.Errorf("keyed-grant path ran before the hook slot: %q", result.Output)
	}
	if result.Output != "ran" {
		t.Errorf("output = %q, want the hook-approved execution", result.Output)
	}
}

// An empty call id can never carry a stamp.
func TestHookApprovalEmptyID(t *testing.T) {
	gate, _, prompted := hookApprovalGate(t)
	gate.GrantHookApproval("")
	ctx := WithHookApproval(context.Background(), "")
	if hookApprovalFromContext(ctx) != "" {
		t.Error("empty id stamped the context")
	}
	tool := decisionTool{name: "guarded", requires: true}
	execute, _ := gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "")
	runPrepared(t, execute)
	if !*prompted {
		t.Error("empty-id call skipped the prompt")
	}
}

// The Granted audit notification is a permission_notification payload on the
// bus shape (additive-only contract).
func TestHookApprovalGrantedNotificationShape(t *testing.T) {
	gate, bus, _ := hookApprovalGate(t)
	gate.GrantHookApproval("call-1")
	ctx := WithHookApproval(context.Background(), "call-1")
	tool := decisionTool{name: "guarded", requires: true}
	execute, _ := gate.PrepareWithID(ctx, tool, json.RawMessage(`{}`), "call-1")
	runPrepared(t, execute)

	_, notifs, _ := bus.snapshot()
	if len(notifs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifs))
	}
	n := notifs[0]
	var _ pubsub.PermissionNotification = n
	if !n.Granted || n.Denied || n.Cancelled || n.Tool != "guarded" || n.ID == "" {
		t.Errorf("Granted notification shape wrong: %+v", n)
	}
}
