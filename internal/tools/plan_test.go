package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanStoreReplaceAndRender(t *testing.T) {
	store := NewPlanStore()
	if got := store.Render(); got != "" {
		t.Fatalf("empty plan should render empty, got %q", got)
	}

	items := []PlanItem{
		{ID: "1", Text: "Investigate", Status: PlanStatusDone},
		{ID: "2", Text: "Implement", Status: PlanStatusInProgress},
		{ID: "3", Text: "Verify", Status: PlanStatusPending},
	}
	store.Replace(items)

	want := "- [x] (1) Investigate\n- [~] (2) Implement\n- [ ] (3) Verify"
	if got := store.Render(); got != want {
		t.Fatalf("unexpected render:\n got: %q\nwant: %q", got, want)
	}

	// Items must be a copy: mutating it cannot change the store.
	got := store.Items()
	got[0].Text = "changed"
	if store.Items()[0].Text != "Investigate" {
		t.Fatal("Items returned the store's backing slice")
	}

	// Replace must copy too.
	items[1].Text = "changed"
	if store.Items()[1].Text != "Implement" {
		t.Fatal("Replace kept the caller's slice")
	}

	store.Replace(nil)
	if got := store.Render(); got != "" {
		t.Fatalf("cleared plan should render empty, got %q", got)
	}
}

func TestPlanStoreRenderWithoutID(t *testing.T) {
	store := NewPlanStore()
	store.Replace([]PlanItem{{Text: "No id", Status: PlanStatusPending}})
	if got, want := store.Render(), "- [ ] No id"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlanToolSpec(t *testing.T) {
	tool := &PlanTool{Store: NewPlanStore()}
	spec := tool.Spec()
	if spec.Name != "plan" {
		t.Errorf("expected tool name plan, got %q", spec.Name)
	}
	if tool.RequiresApproval() {
		t.Error("plan tool must not require approval")
	}

	var schema map[string]any
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
		t.Fatalf("parameters are not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("expected object schema, got %v", schema["type"])
	}
}

func TestPlanToolReplacesWholePlan(t *testing.T) {
	store := NewPlanStore()
	tool := &PlanTool{Store: store}

	first := `{"items":[
		{"id":"a","text":"One","status":"in_progress"},
		{"id":"b","text":"Two","status":"pending"}
	]}`
	res, err := tool.Execute(context.Background(), json.RawMessage(first))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "- [~] (a) One") || !strings.Contains(res.Output, "- [ ] (b) Two") {
		t.Fatalf("rendered checklist missing entries: %q", res.Output)
	}

	second := `{"items":[{"id":"c","text":"Only","status":"done"}]}`
	res, err = tool.Execute(context.Background(), json.RawMessage(second))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	items := store.Items()
	if len(items) != 1 || items[0].ID != "c" {
		t.Fatalf("plan was not replaced: %+v", items)
	}
	if got, want := store.Render(), "- [x] (c) Only"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlanToolRejectsUnknownStatus(t *testing.T) {
	store := NewPlanStore()
	tool := &PlanTool{Store: store}

	args := `{"items":[{"id":"a","text":"One","status":"blocked"}]}`
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "unknown status") {
		t.Fatalf("expected unknown status error, got %q", res.Error)
	}
	if len(store.Items()) != 0 {
		t.Fatal("invalid plan must not be stored")
	}
}

func TestPlanToolRejectsEmptyText(t *testing.T) {
	tool := &PlanTool{Store: NewPlanStore()}
	args := `{"items":[{"id":"a","text":"   ","status":"pending"}]}`
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "text cannot be empty") {
		t.Fatalf("expected empty text error, got %q", res.Error)
	}
}

func TestPlanToolRejectsInvalidJSON(t *testing.T) {
	tool := &PlanTool{Store: NewPlanStore()}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"items":`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "invalid arguments") {
		t.Fatalf("expected invalid arguments error, got %q", res.Error)
	}
}

func TestPlanToolEmptyListClearsPlan(t *testing.T) {
	store := NewPlanStore()
	store.Replace([]PlanItem{{ID: "a", Text: "One", Status: PlanStatusPending}})
	tool := &PlanTool{Store: store}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"items":[]}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if len(store.Items()) != 0 {
		t.Fatalf("expected cleared plan, got %+v", store.Items())
	}
}

func TestPlanToolRequiresItemsField(t *testing.T) {
	tool := &PlanTool{Store: NewPlanStore()}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "items is required") {
		t.Fatalf("expected missing items error, got %q", res.Error)
	}
}
