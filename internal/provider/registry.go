package provider

import (
	"context"
	"fmt"
)

type Registry struct {
	providers map[string]Provider
	active    string
}

func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

func (r *Registry) Register(p Provider) {
	name := p.Name()
	r.providers[name] = p
	if r.active == "" {
		r.active = name
	}
}

func (r *Registry) Switch(name string) error {
	if _, ok := r.providers[name]; !ok {
		available := make([]string, 0, len(r.providers))
		for k := range r.providers {
			available = append(available, k)
		}
		return fmt.Errorf("unknown provider %q, available: %v", name, available)
	}
	r.active = name
	return nil
}

func (r *Registry) Active() (Provider, error) {
	p, ok := r.providers[r.active]
	if !ok {
		return nil, fmt.Errorf("no active provider configured (active=%q, registered=%d)", r.active, len(r.providers))
	}
	return p, nil
}

func (r *Registry) ActiveName() string {
	return r.active
}

func (r *Registry) Get(name string) Provider {
	return r.providers[name]
}

func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	return names
}

func (r *Registry) AllModels(ctx context.Context) map[string][]string {
	result := make(map[string][]string)
	for name, p := range r.providers {
		if models, err := p.Models(ctx); err == nil && len(models) > 0 {
			result[name] = models
		}
	}
	return result
}
