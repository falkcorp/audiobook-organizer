// file: internal/database/storecap.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-31

package database

// These two helpers predate store_capability.go by five weeks and independently
// discovered the same decorator problem, each with its own hand-inlined copy of
// the unwrap walk and a locally-declared `unwrapper` interface. They now delegate
// to AsCapability so there is exactly one implementation to reason about.
//
// Worth knowing before you copy this pattern: for THESE two capabilities the walk
// is defensive, not load-bearing. OpsV2Store and AIJobsStore are subsets of Store's
// method set, so a decorator that embeds the Store interface satisfies them on the
// outermost layer and never needs unwrapping. That is precisely why GetOpsV2 and
// GetAIJobs kept working in production while AsSyncIdentityStore silently returned
// nil — the capabilities that broke are the ones with at least one method outside
// Store. TestCapabilityFamiliesDifferInVisibility pins the distinction down and
// fails if a capability crosses the line in either direction.
//
// Behaviour is otherwise unchanged except that the walk is now depth-bounded
// (maxUnwrapDepth): the old loops would spin forever on a decorator whose Unwrap
// returned itself. Both also treated a nil Unwrap() result as "keep going" and
// relied on the `store != nil` loop condition to catch it on the next pass, which
// worked but only by accident of ordering; AsCapability rejects it explicitly.

// GetOpsV2 resolves the OpsV2Store capability from store, walking any Unwrap()
// decorator chain. Returns nil if store is nil or no layer satisfies OpsV2Store.
//
// Use this instead of scattering `store.(database.OpsV2Store)` type assertions.
func GetOpsV2(store Store) OpsV2Store {
	if v2, ok := AsCapability[OpsV2Store](store); ok {
		return v2
	}
	return nil
}

// GetAIJobs resolves the AIJobsStore capability from store, walking any Unwrap()
// decorator chain. Returns nil if store is nil or no layer satisfies AIJobsStore.
//
// Use this instead of scattering `store.(database.AIJobsStore)` type assertions.
func GetAIJobs(store Store) AIJobsStore {
	if aij, ok := AsCapability[AIJobsStore](store); ok {
		return aij
	}
	return nil
}
