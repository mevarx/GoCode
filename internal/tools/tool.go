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
