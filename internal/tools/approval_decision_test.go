package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// decisionTool lets a test pick a name and whether the gate must ask first.
type decisionTool struct {
	name     string
	requires bool
	executed *bool
}

func (d decisionTool) Spec() ToolSpec { return ToolSpec{Name: d.name} }

func (d decisionTool) RequiresApproval() bool { return d.requires }

func (d decisionTool) Execute(context.Context, json.RawMessage) (Result, error) {
	if d.executed != nil {
		*d.executed = true
	}
	return Result{Output: "ran"}, nil
}

type decision struct {
	tool     string
	approved bool
	auto     bool
}

func recordDecisions(gate *ApprovalGate) *[]decision {
	var got []decision
	gate.OnDecision = func(toolName string, approved, auto bool) {
		got = append(got, decision{toolName, approved, auto})
	}
	return &got
}

func TestOnDecisionReportsAutoApproved(t *testing.T) {
	gate := NewApprovalGateWithPermissions([]string{"safe"}, nil)
	got := recordDecisions(gate)

	executed := false
	if _, err := gate.WrapExecution(context.Background(), decisionTool{name: "safe", requires: true, executed: &executed}, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !executed {
		t.Error("auto-approved tool did not execute")
	}
	if len(*got) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(*got))
	}
	d := (*got)[0]
	if d.tool != "safe" || !d.approved || !d.auto {
		t.Errorf("unexpected decision: %+v", d)
	}
}

func TestOnDecisionReportsPolicyDenial(t *testing.T) {
	gate := NewApprovalGateWithPermissions(nil, []string{"danger"})
	got := recordDecisions(gate)

	executed := false
	res, err := gate.WrapExecution(context.Background(), decisionTool{name: "danger", requires: true, executed: &executed}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if executed {
		t.Error("denied tool executed")
	}
	if res.Error == "" {
		t.Error("expected a denial error result")
	}
	if len(*got) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(*got))
	}
	d := (*got)[0]
	if d.approved || !d.auto {
		t.Errorf("denial should be recorded as not approved, policy-decided: %+v", d)
	}
}

func TestOnDecisionReportsUserDenial(t *testing.T) {
	gate := NewApprovalGate()
	gate.OnPresent = func(string, json.RawMessage, string) (bool, error) { return false, nil }
	got := recordDecisions(gate)

	executed := false
	if _, err := gate.WrapExecution(context.Background(), decisionTool{name: "ask", requires: true, executed: &executed}, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if executed {
		t.Error("user-denied tool executed")
	}
	if len(*got) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(*got))
	}
	d := (*got)[0]
	if d.approved || d.auto {
		t.Errorf("user denial should be recorded as not approved, not auto: %+v", d)
	}
}

// The hook is observation only: a nil hook must not change execution.
func TestOnDecisionNilKeepsExecution(t *testing.T) {
	gate := NewApprovalGateWithPermissions([]string{"safe"}, nil)
	executed := false
	if _, err := gate.WrapExecution(context.Background(), decisionTool{name: "safe", requires: true, executed: &executed}, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Error("tool did not execute without a decision observer")
	}
}
