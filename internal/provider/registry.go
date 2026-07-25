package provider

import "fmt"

// Registry manages available providers and active selection.
type Registry struct {
	providers map[string]Provider
	active    string
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register registers a provider. The first registered provider becomes active.
func (r *Registry) Register(p Provider) {
	name := p.Name()
	r.providers[name] = p
	if r.active == "" {
		r.active = name
	}
}

// Switch changes the active provider.
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

// Active returns the active provider instance.
func (r *Registry) Active() Provider {
	p, ok := r.providers[r.active]
	if !ok {
		panic(fmt.Sprintf("no active provider (active=%q, registered=%d)", r.active, len(r.providers)))
	}
	return p
}

// ActiveName returns the name of the active provider.
func (r *Registry) ActiveName() string {
	return r.active
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	return names
}
