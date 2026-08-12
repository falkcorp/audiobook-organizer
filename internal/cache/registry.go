// file: internal/cache/registry.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-08-11

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

// Invalidatable is the optional second interface a registered cache may
// satisfy to allow bulk invalidation from outside the package. *Cache[T]
// satisfies it.
//
// It is deliberately separate from Introspectable rather than folded into it:
// registration should not require a cache to expose a destructive operation,
// and keeping the write capability opt-in means a future read-only or
// externally-backed registry entry cannot be emptied by accident.
type Invalidatable interface {
	InvalidateAll()
	Len() int
}

// InvalidateByName empties one registered cache and reports how many entries
// it held. ok is false when no cache of that name is registered, and
// invalidatable is false when the cache exists but does not expose bulk
// invalidation — the caller needs to tell those apart to answer correctly.
//
// The count is read BEFORE invalidating, because InvalidateAll returns
// nothing and Len afterwards is always zero.
func InvalidateByName(name string) (dropped int, ok bool, invalidatable bool) {
	registryMu.RLock()
	c, found := registry[name]
	registryMu.RUnlock()
	if !found {
		return 0, false, false
	}
	inv, canInvalidate := c.(Invalidatable)
	if !canInvalidate {
		return 0, true, false
	}
	n := inv.Len()
	inv.InvalidateAll()
	return n, true, true
}

// InvalidateAllRegistered empties every registered cache that supports bulk
// invalidation and returns a per-cache count of the entries dropped. Caches
// that do not support it are skipped and named in the second return value, so
// an operator sees what was NOT cleared rather than assuming a clean sweep.
func InvalidateAllRegistered() (dropped map[string]int, skipped []string) {
	registryMu.RLock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	registryMu.RUnlock()
	sort.Strings(names)

	dropped = make(map[string]int, len(names))
	for _, name := range names {
		n, _, canInvalidate := InvalidateByName(name)
		if !canInvalidate {
			skipped = append(skipped, name)
			continue
		}
		dropped[name] = n
	}
	return dropped, skipped
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
