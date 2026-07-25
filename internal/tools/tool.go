package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolSpec defines schema metadata for a tool.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Tool defines the execution contract for agent capabilities.
type Tool interface {
	Spec() ToolSpec
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
	RequiresApproval() bool
}

// Result holds the execution output or diff of a tool invocation.
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

// Registry manages registered tool instances.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry initializes an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register registers a tool instance.
func (r *Registry) Register(t Tool) {
	r.tools[t.Spec().Name] = t
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Specs returns specifications of all registered tools.
func (r *Registry) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.Spec())
	}
	return specs
}

// List returns names of all registered tools.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
