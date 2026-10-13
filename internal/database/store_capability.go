// file: internal/database/store_capability.go
// version: 1.2.0
// guid: 9a41c7e0-58b2-4c33-8f6d-1b0e9d2a7c45
// last-edited: 2026-10-13

package database

// This file exists because of a production failure worth stating in full, so the
// next person to add a capability interface does not rediscover it the hard way.
//
// Narrow capability interfaces (SyncIdentityStore, SyncFileStore, BookmarkStore,
// ...) are deliberately NOT part of the big Store interface — the repo rule is
// that nobody edits store.go, so each new capability lives in its own file with
// its own interface, discovered at runtime by a type assertion.
//
// A decorator that EMBEDS the Store interface hides every one of them:
//
//	type decorator struct { database.Store }
//
// Embedding an interface promotes only THAT interface's method set. Every
// capability method is therefore invisible through the decorator, so a plain
// `s.(SyncIdentityStore)` returns nil once the wrap is installed -- with no
// compile error, because a failed type assertion is a legal runtime outcome.
// The per-capability `var _ SyncIdentityStore = (*PebbleStore)(nil)` assertions
// prove the INNER type conforms and say nothing about a decorator.
//
// The example that taught this was internal/server.indexedStore, which wrapped
// the server's store so search indexing could intercept book writes. It was
// removed in 07-S3: the store reports its writes to a ChangeObserver
// (change_observer.go) and the server's store is now the raw store, so THAT
// decorator no longer exists. The hazard class does not go away: other
// decorators wrap stores (find them with
// `grep -rn 'func (.*) Unwrap() database.Store'`), tests build their own, and a
// future layer will embed Store again. This is how it presented in production:
// the wrap only happened when the Bleve search index opened successfully, which
// was true in production and false in most local runs, so the whole class of bug
// reproduced nowhere except prod. It surfaced on 2026-07-30 as
// "backfill-sync-ids: store does not implement the sync-identity capability
// interfaces", and it silently disabled the merge-follow hook that keeps ABS
// listening progress attached to a book across a dedup merge.
//
// The fix is for capability lookups to walk the decorator chain. A decorator
// opts in by exposing Unwrap; one that does not is treated as opaque on purpose
// (see StoreUnwrapper).
//
// The same hazard applies to CONCRETE-type assertions, not just interfaces:
// `store.(*PebbleStore)` fails through a decorator too, and its failure mode is
// worse because such sites usually have a "different backend, degrade gracefully"
// fallback written for SQLite/mock. A wrapped Pebble store takes that fallback and
// looks like a supported configuration. Two prod jobs were silently degraded this
// way for weeks -- see AsPebbleStore. Resolve concrete stores through this file
// too, never with a bare assertion on a value obtained from Server.Store(): the
// call sites hold the raw store today, and AsCapability keeps them correct if a
// decorator is ever put back.

// maxUnwrapDepth bounds the walk so a decorator that accidentally returns itself
// — or a cycle built from two decorators pointing at each other — cannot hang a
// request. The real chain is one deep today; 16 is slack, not a design target.
const maxUnwrapDepth = 16

// StoreUnwrapper is implemented by a decorator that is willing to have narrow
// capability interfaces resolved against the store it wraps.
//
// Implementing it is an explicit statement: "callers may reach past me for
// capabilities I do not implement myself." Do NOT implement it on a decorator
// whose behaviour must not be bypassed — an access-control or read-only wrapper,
// for instance — because a caller that resolves a capability from the inner
// store writes through it directly. The removed indexedStore implemented it
// because the capabilities in question (sync identity, sync files, bookmarks)
// touch keyspaces it did not index, so reaching past it lost nothing.
type StoreUnwrapper interface {
	Unwrap() Store
}

// AsCapability resolves T from s, looking through any decorator chain that opts
// in via StoreUnwrapper. It returns the zero value and false when no layer
// implements T.
//
// T may be an interface (SyncIdentityStore, OpsV2Store, ...) or a concrete type
// (*PebbleStore) — both fail identically through a decorator, so both belong here.
//
// The check runs on the OUTERMOST layer first, so a decorator that implements a
// capability itself (to add indexing or auditing to it) still wins over the
// inner store.
//
// Exported because callers outside this package hit the same problem: package
// server's maintenance fixups and package maintenance/jobs both need to reach the
// concrete Pebble store through whatever decorators wrap it.
func AsCapability[T any](s any) (T, bool) {
	var zero T
	for depth := 0; s != nil && depth < maxUnwrapDepth; depth++ {
		if c, ok := s.(T); ok {
			return c, true
		}
		u, ok := s.(StoreUnwrapper)
		if !ok {
			// Not a decorator, or one that has not opted in. Stop here rather
			// than guess at its internals.
			return zero, false
		}
		inner := u.Unwrap()
		if inner == nil {
			return zero, false
		}
		s = inner
	}
	return zero, false
}

// AsPebbleStore resolves the concrete *PebbleStore out of s, looking through any
// opted-in decorator chain. Returns nil when there is no Pebble store in the
// chain — a genuinely non-Pebble backend (SQLite, a test double) or an opaque
// decorator.
//
// Use this instead of `store.(*PebbleStore)` on any value that came from
// Server.Store(). The bare assertion is what silently degraded these in prod,
// back when the Bleve indexedStore decorator was always installed:
//
//   - sweep-pebble-metrics-ttl logged "store is not a PebbleStore; skipping" and
//     no-opped, so expired metrics snapshots were never swept and grew unbounded.
//   - recompute-book-aggregates fell back to its interface path, skipping the
//     IsBookAggregatesBackfillDone sentinel and redoing the full 40k-book
//     backfill on every run.
//   - the maintenance wipe fixups either errored with "unsupported store type
//     *server.indexedStore" or took an approximate fallback that misses the
//     secondary-index prefixes.
//
// Every one of those sites had a deliberate non-Pebble fallback written for
// SQLite/mock, which is exactly why the failure was invisible: a wrapped Pebble
// store is indistinguishable from an unsupported backend at the assertion, and
// the fallback makes it look like a supported configuration rather than a bug.
func AsPebbleStore(s any) *PebbleStore {
	if ps, ok := AsCapability[*PebbleStore](s); ok {
		return ps
	}
	return nil
}
