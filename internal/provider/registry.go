package provider

import "fmt"

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

func (r *Registry) Active() Provider {
	p, ok := r.providers[r.active]
	if !ok {
		panic(fmt.Sprintf("no active provider (active=%q, registered=%d)", r.active, len(r.providers)))
	}
	return p
}

func (r *Registry) ActiveName() string {
	return r.active
}

func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	return names
}
