package provider

import "fmt"

// Registry holds the configured providers, keyed by Name(). It is read-only after
// construction and safe for concurrent use.
type Registry struct {
	providers map[string]Provider
	def       string
}

// NewRegistry builds a registry from the given providers. defaultName selects the
// provider used when a request does not specify one; it must be present.
func NewRegistry(defaultName string, ps ...Provider) (*Registry, error) {
	m := make(map[string]Provider, len(ps))
	for _, p := range ps {
		if p == nil {
			continue
		}
		m[p.Name()] = p
	}
	if _, ok := m[defaultName]; !ok {
		return nil, fmt.Errorf("provider registry: default provider %q is not registered", defaultName)
	}
	return &Registry{providers: m, def: defaultName}, nil
}

// Get returns the provider with the given name. An empty name resolves to the
// default provider.
func (r *Registry) Get(name string) (Provider, error) {
	if name == "" {
		name = r.def
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", name)
	}
	return p, nil
}

// Default returns the default provider.
func (r *Registry) Default() Provider {
	return r.providers[r.def]
}

// Names returns the registered provider names (for diagnostics).
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}
