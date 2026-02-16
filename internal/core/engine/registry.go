package engine

import (
	"fmt"
	"sync"
)

// Registry manages registered engines by name.
type Registry struct {
	mu      sync.RWMutex
	engines map[string]Engine
}

func NewRegistry() *Registry {
	return &Registry{
		engines: make(map[string]Engine),
	}
}

func (r *Registry) Register(e Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engines[e.Name()] = e
}

func (r *Registry) Get(name string) (Engine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.engines[name]
	if !ok {
		return nil, fmt.Errorf("engine %q not found", name)
	}
	return e, nil
}

func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.engines))
	for name := range r.engines {
		names = append(names, name)
	}
	return names
}
