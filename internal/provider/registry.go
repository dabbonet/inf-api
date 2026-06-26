package provider

import (
	"strings"
	"sync"
)

// Registry maps channel names to provider Specs.
//
// Channels are derived from URL path prefixes ("/puter/...",
// "/codebuff/...") or from model lookups. The Registry is the
// single source of truth for "where does this request go".
type Registry struct {
	mu       sync.RWMutex
	byName   map[string]Spec
	byPrefix map[string]Spec
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byName:   make(map[string]Spec),
		byPrefix: make(map[string]Spec),
	}
}

// Register adds a spec to the registry, keyed by its Name and PathPrefix.
func (r *Registry) Register(s Spec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[strings.ToLower(s.Name)] = s
	if s.PathPrefix != "" {
		r.byPrefix[strings.ToLower(s.PathPrefix)] = s
	}
}

// GetByName returns the spec registered for the given channel name
// (case-insensitive). Returns false if not found.
func (r *Registry) GetByName(name string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[strings.ToLower(name)]
	return s, ok
}

// GetByPathPrefix returns the spec whose PathPrefix is a prefix of the
// supplied path (case-insensitive). Returns false if none match.
func (r *Registry) GetByPathPrefix(path string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := strings.ToLower(path)
	for _, s := range r.byPrefix {
		if strings.HasPrefix(p, strings.ToLower(s.PathPrefix)) {
			return s, true
		}
	}
	return Spec{}, false
}

// All returns every registered spec (for diagnostics).
func (r *Registry) All() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.byName))
	for _, s := range r.byName {
		out = append(out, s)
	}
	return out
}

// Names returns the channel names of every registered spec.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}
