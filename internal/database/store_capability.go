// file: internal/database/store_capability.go
// version: 1.0.0
// guid: 9a41c7e0-58b2-4c33-8f6d-1b0e9d2a7c45
// last-edited: 2026-07-30

package database

// This file exists because of a production failure worth stating in full, so the
// next person to add a capability interface does not rediscover it the hard way.
//
// Narrow capability interfaces (SyncIdentityStore, SyncFileStore, BookmarkStore,
// ...) are deliberately NOT part of the big Store interface — the repo rule is
// that nobody edits store.go, so each new capability lives in its own file with
// its own interface, discovered at runtime by a type assertion.
//
// internal/server.indexedStore decorates the store during Server.Start() by
// EMBEDDING the Store interface:
//
//	type indexedStore struct { database.Store; server *Server }
//
// Embedding an interface promotes only THAT interface's method set. Every
// capability method is therefore invisible through the decorator, so a plain
// `s.(SyncIdentityStore)` returns nil once the wrap is installed — with no
// compile error, because a failed type assertion is a legal runtime outcome.
// The per-capability `var _ SyncIdentityStore = (*PebbleStore)(nil)` assertions
// prove the INNER type conforms and say nothing about a decorator.
//
// The wrap only happens when the Bleve search index opened successfully, which
// is true in production and false in most local runs — so the whole class of bug
// reproduces nowhere except prod. It surfaced on 2026-07-30 as
// "backfill-sync-ids: store does not implement the sync-identity capability
// interfaces", and it silently disabled the merge-follow hook that keeps ABS
// listening progress attached to a book across a dedup merge.
//
// The fix is for capability lookups to walk the decorator chain. A decorator
// opts in by exposing Unwrap; one that does not is treated as opaque on purpose
// (see StoreUnwrapper).

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
// store writes through it directly. indexedStore implements it because the
// capabilities in question (sync identity, sync files, bookmarks) touch
// keyspaces it does not index, so reaching past it loses nothing.
type StoreUnwrapper interface {
	Unwrap() Store
}

// asCapability resolves T from s, looking through any decorator chain that opts
// in via StoreUnwrapper. It returns the zero value and false when no layer
// implements T.
//
// The check runs on the OUTERMOST layer first, so a decorator that implements a
// capability itself (to add indexing or auditing to it) still wins over the
// inner store.
func asCapability[T any](s any) (T, bool) {
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
