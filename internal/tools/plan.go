package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Plan item statuses accepted by the plan tool.
const (
	PlanStatusPending    = "pending"
	PlanStatusInProgress = "in_progress"
	PlanStatusDone       = "done"
)

// PlanItem is one entry in an agent-maintained task list.
type PlanItem struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

// PlanStore holds the current plan. It is shared by the plan tool, which
// writes it, and the agent loop, which renders it into the system prompt.
type PlanStore struct {
	mu    sync.RWMutex
	items []PlanItem
}

// NewPlanStore returns an empty store.
func NewPlanStore() *PlanStore {
	return &PlanStore{}
}

// Replace swaps the whole plan for items, copying the slice so callers cannot
// mutate the store through their own slice.
func (s *PlanStore) Replace(items []PlanItem) {
	next := make([]PlanItem, len(items))
	copy(next, items)
	s.mu.Lock()
	s.items = next
	s.mu.Unlock()
}

// Items returns a copy of the current plan.
func (s *PlanStore) Items() []PlanItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]PlanItem, len(s.items))
	copy(items, s.items)
	return items
}

// Render returns the checklist in a stable, plain-text form:
//
//   - [x] (1) Write the parser
//   - [~] (2) Add tests
//   - [ ] (3) Update docs
//
// An empty plan renders as the empty string.
func (s *PlanStore) Render() string {
	items := s.Items()
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, item := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("- [")
		sb.WriteString(planStatusMark(item.Status))
		sb.WriteString("] ")
		if item.ID != "" {
			sb.WriteString("(")
			sb.WriteString(item.ID)
			sb.WriteString(") ")
		}
		sb.WriteString(item.Text)
	}
	return sb.String()
}

func planStatusMark(status string) string {
	switch status {
	case PlanStatusDone:
		return "x"
	case PlanStatusInProgress:
		return "~"
	default:
		return " "
	}
}

// PlanTool lets the model maintain a task list for multi-step work. It only
// records text; it cannot touch the workspace, so it needs no approval.
type PlanTool struct {
	Store *PlanStore
}

type planToolArgs struct {
	Items *[]PlanItem `json:"items"`
}

func (p *PlanTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "plan",
		Description: "Maintain a task list for multi-step work. Pass the complete list in order; each item has an id, text, and status (pending, in_progress, done). Use it for tasks with three or more steps and update it as work progresses.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["items"],
			"properties": {
				"items": {
					"type": "array",
					"description": "The complete plan, in execution order",
					"items": {
						"type": "object",
						"required": ["id", "text", "status"],
						"properties": {
							"id": {
								"type": "string",
								"description": "Short stable identifier for the step"
							},
							"text": {
								"type": "string",
								"description": "What the step does"
							},
							"status": {
								"type": "string",
								"enum": ["pending", "in_progress", "done"],
								"description": "Current state of the step"
							}
						}
					}
				}
			}
		}`),
	}
}

// RequiresApproval is false: the tool only records text in the session.
func (p *PlanTool) RequiresApproval() bool {
	return false
}

func (p *PlanTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var a planToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}
	if a.Items == nil {
		return Result{Error: "items is required"}, nil
	}

	items := make([]PlanItem, len(*a.Items))
	for i, item := range *a.Items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return Result{Error: fmt.Sprintf("plan item %d: text cannot be empty", i+1)}, nil
		}
		status := strings.TrimSpace(item.Status)
		switch status {
		case PlanStatusPending, PlanStatusInProgress, PlanStatusDone:
		default:
			return Result{Error: fmt.Sprintf("plan item %d: unknown status %q (expected pending, in_progress, or done)", i+1, item.Status)}, nil
		}
		items[i] = PlanItem{
			ID:     strings.TrimSpace(item.ID),
			Text:   text,
			Status: status,
		}
	}

	if p.Store == nil {
		return Result{Error: "plan store is not configured"}, nil
	}

	p.Store.Replace(items)
	if len(items) == 0 {
		return Result{Output: "Plan cleared."}, nil
	}
	return Result{Output: p.Store.Render()}, nil
}
