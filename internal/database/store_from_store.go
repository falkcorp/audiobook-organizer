// file: internal/database/store_from_store.go
// version: 1.0.0
// guid: 5e9c2a71-4b36-48df-9107-8c3d6f2b1a94
// last-edited: 2026-08-19

package database

// Store-taking constructors for the sibling stores that share the main PebbleDB
// handle.
//
// Each of these has a *pebble.DB-taking twin (NewEmbeddingStore,
// NewPebbleMetricsStore, NewAIScanStoreFromDB, NewPebbleActivityStore). Those
// twins forced every caller to do the unwrap itself:
//
//	ps := database.AsPebbleStore(store)
//	if ps == nil { return (*database.EmbeddingStore)(nil), nil }
//	return database.NewEmbeddingStore(ps.DB()), nil
//
// which is five packages naming *PebbleStore concretely to reach one method.
// Handing them an interface{ DB() *pebble.DB } would not have helped: it trades
// a dependency on our concrete type for a dependency on pebble's, and pulls
// github.com/cockroachdb/pebble into five import graphs that do not otherwise
// need it.
//
// Moving the unwrap in here is what actually removes the coupling. AsPebbleStore
// stays, but production callers outside this package no longer need it, so
// *PebbleStore becomes something only internal/database resolves — the
// precondition for ever splitting that type. See
// docs/plans/2026-08-19-split-the-pebblestore-surface.md.
//
// Each returns a TYPED NIL on a non-Pebble backend, exactly as the call sites
// did by hand, because their consumers already nil-check the concrete pointer.

// NewEmbeddingStoreFromStore builds the embedding store from any store that
// resolves to a *PebbleStore through the decorator chain, or returns nil.
func NewEmbeddingStoreFromStore(store any) *EmbeddingStore {
	ps := AsPebbleStore(store)
	if ps == nil {
		return nil
	}
	return NewEmbeddingStore(ps.DB())
}

// NewPebbleMetricsStoreFromStore builds the metrics store from any store that
// resolves to a *PebbleStore through the decorator chain, or returns nil.
func NewPebbleMetricsStoreFromStore(store any) *PebbleMetricsStore {
	ps := AsPebbleStore(store)
	if ps == nil {
		return nil
	}
	return NewPebbleMetricsStore(ps.DB())
}

// NewAIScanStoreFromStore builds the AI scan store from any store that resolves
// to a *PebbleStore through the decorator chain. It returns (nil, nil) on a
// non-Pebble backend — absence, not an error — and propagates a genuine
// construction failure.
func NewAIScanStoreFromStore(store any) (*AIScanStore, error) {
	ps := AsPebbleStore(store)
	if ps == nil {
		return nil, nil
	}
	return NewAIScanStoreFromDB(ps.DB())
}

// NewPebbleActivityStoreFromStore builds the activity store from any store that
// resolves to a *PebbleStore through the decorator chain, or returns nil.
func NewPebbleActivityStoreFromStore(store any) *PebbleActivityStore {
	ps := AsPebbleStore(store)
	if ps == nil {
		return nil
	}
	return NewPebbleActivityStore(ps.DB())
}
