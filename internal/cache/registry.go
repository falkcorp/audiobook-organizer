// file: internal/cache/registry.go
// version: 1.2.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-18

package cache

import (
	"sort"
	"sync"
)

// Introspectable is the interface a cache must implement to be registered.
type Introspectable interface {
	Keys() []string
	Name() string
	Len() int
}

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Introspectable)
)

// register adds a cache to the global registry (called by New()).
func register(c Introspectable) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[c.Name()] = c
}

// Lookup returns a registered cache by name, or (nil, false) if not found.
func Lookup(name string) (Introspectable, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := registry[name]
	return c, ok
}

// All returns a sorted list of registered cache names.
func All() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
