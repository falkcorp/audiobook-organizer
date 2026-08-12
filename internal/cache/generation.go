// file: internal/cache/generation.go
// version: 1.0.0
// guid: 018cc7fe-8212-4cb8-95be-59b65e2b7ede
// last-edited: 2026-08-11

package cache

import (
	"strconv"
	"sync/atomic"
)

// Generation is a monotonic counter that makes cached responses derived from a
// mutable corpus UNREACHABLE once that corpus changes, instead of relying on
// every mutation site remembering to call InvalidateAll.
//
// Why this shape: the library list cache previously keyed on the raw query
// string alone. Merging books hard-deleted them from the store, but the cached
// list responses still contained the deleted rows and there was no
// Invalidate/InvalidateAll call for that cache anywhere in the codebase — so
// the library page served phantom books (rows that 404 on fetch, and version
// losers demoted to is_primary_version=false) until the entry's TTL expired or
// the process restarted. Three separate mutation handlers each invalidated
// some OTHER cache and forgot this one; adding a fourth invalidate call would
// have depended on every future handler author remembering too.
//
// Folding the counter into the cache key removes that obligation. After a bump
// every reader computes a new key, so pre-bump entries can never be read again.
// They are not actively deleted: they age out via TTL or LRU capacity. That is
// deliberate — correctness comes from unreachability, and reclamation is left
// to the mechanisms the cache already has.
//
// The zero value is ready to use. It is safe for concurrent use.
type Generation struct {
	n atomic.Uint64
}

// Bump advances the generation and returns the new value. Every cache key
// built from this Generation after the bump differs from every key built
// before it, so all prior entries become unreachable.
func (g *Generation) Bump() uint64 {
	if g == nil {
		return 0
	}
	return g.n.Add(1)
}

// Value returns the current generation.
func (g *Generation) Value() uint64 {
	if g == nil {
		return 0
	}
	return g.n.Load()
}

// Key builds a generation-scoped cache key of the form
// "<prefix><generation>:<suffix>".
//
// Callers MUST route every read and write of a generation-scoped cache through
// this method rather than formatting the key by hand: a reader and a writer
// that disagree on the key layout produce a cache that never hits, which is a
// silent performance regression rather than a visible failure.
//
// A nil Generation formats as generation 0, which keeps keys stable (and the
// cache useful) for stores that do not supply a counter.
func (g *Generation) Key(prefix, suffix string) string {
	return prefix + strconv.FormatUint(g.Value(), 10) + ":" + suffix
}
