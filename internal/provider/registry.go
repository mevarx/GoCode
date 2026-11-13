package provider

import (
	"fmt"
	"strings"
)

// defaultContextWindow is the budget assumed for a model family the registry
// does not know. It is deliberately conservative: under-estimating a window is
// recoverable (compaction runs earlier), over-estimating loses a request to a
// provider-side context error.
const defaultContextWindow = 65536

// defaultContextWindows maps model-family prefixes to their context windows in
// tokens. Matching is longest-prefix and case-insensitive, so more specific
// registrations (and newer variants) can be added with RegisterContextWindow.
var defaultContextWindows = map[string]int{
	"gpt-4o":   128000,
	"gpt-4.1":  128000,
	"o1":       200000,
	"o3":       200000,
	"claude":   200000,
	"gemini":   1000000,
	"deepseek": 128000,
	"qwen":     128000,
	"kimi":     128000,
	"llama":    131072,
}

type Registry struct {
	providers map[string]Provider
	active    string
	// contextWindows holds per-model or per-family overrides registered by the
	// caller. They are consulted together with defaultContextWindows.
	contextWindows map[string]int
}

func NewRegistry() *Registry {
	return &Registry{
		providers:      make(map[string]Provider),
		contextWindows: make(map[string]int),
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

// RegisterContextWindow records the context window of a model or model family.
// The model is matched as a case-insensitive prefix, so registering "gpt-4o"
// also covers "gpt-4o-mini". A non-positive token count or empty prefix is
// ignored.
func (r *Registry) RegisterContextWindow(model string, tokens int) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || tokens <= 0 {
		return
	}
	if r.contextWindows == nil {
		r.contextWindows = make(map[string]int)
	}
	r.contextWindows[model] = tokens
}

// ContextWindow returns the context window for model in tokens. Registered
// prefixes win over built-in family defaults when they match at least as much
// text, so an explicit registration can always override a default. An unknown
// model falls back to defaultContextWindow.
func (r *Registry) ContextWindow(model string) int {
	key := strings.ToLower(strings.TrimSpace(model))

	bestLen := 0
	best := 0
	for prefix, tokens := range defaultContextWindows {
		if strings.HasPrefix(key, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			best = tokens
		}
	}
	for prefix, tokens := range r.contextWindows {
		// A registered entry with the same prefix replaces the default.
		if strings.HasPrefix(key, prefix) && len(prefix) >= bestLen {
			bestLen = len(prefix)
			best = tokens
		}
	}

	if best <= 0 {
		return defaultContextWindow
	}
	return best
}
