package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Tool interface {
	Spec() ToolSpec
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
	RequiresApproval() bool
}

// Previewer is an optional interface that tools can implement to provide
// a preview of the proposed change before execution. This enables the
// approval-before-execution flow where the user sees a diff/command
// preview and approves before any filesystem changes are made.
type Previewer interface {
	Preview(ctx context.Context, args json.RawMessage) (Preview, error)
}

// Preview contains the information shown to the user before approval.
type Preview struct {
	Description string `json:"description,omitempty"`
	Diff        string `json:"diff,omitempty"`
	Command     string `json:"command,omitempty"`
	WorkDir     string `json:"work_dir,omitempty"`
}

func (p Preview) String() string {
	var parts []string
	if p.Description != "" {
		parts = append(parts, p.Description)
	}
	if p.Diff != "" {
		parts = append(parts, "\n"+p.Diff)
	}
	if len(parts) == 0 {
		return "(no preview available)"
	}
	return fmt.Sprintf("%s", joinParts(parts))
}

type Result struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
	Diff   string `json:"diff,omitempty"`
}

func (r Result) String() string {
	if r.Error != "" {
		return fmt.Sprintf("Error: %s", r.Error)
	}
	return r.Output
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Spec().Name] = t
}

func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

func (r *Registry) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.Spec())
	}
	return specs
}

func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

func joinParts(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}
