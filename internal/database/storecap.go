// file: internal/database/storecap.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-23

package database

// GetOpsV2 resolves the OpsV2Store capability from store, walking any Unwrap()
// decorator chain. Returns nil if store is nil or no layer satisfies OpsV2Store.
//
// Use this instead of scattering `store.(database.OpsV2Store)` type assertions.
func GetOpsV2(store Store) OpsV2Store {
	type unwrapper interface{ Unwrap() Store }
	for store != nil {
		if v2, ok := store.(OpsV2Store); ok {
			return v2
		}
		u, ok := store.(unwrapper)
		if !ok {
			break
		}
		store = u.Unwrap()
	}
	return nil
}

// GetAIJobs resolves the AIJobsStore capability from store, walking any Unwrap()
// decorator chain. Returns nil if store is nil or no layer satisfies AIJobsStore.
//
// Use this instead of scattering `store.(database.AIJobsStore)` type assertions.
func GetAIJobs(store Store) AIJobsStore {
	type unwrapper interface{ Unwrap() Store }
	for store != nil {
		if aij, ok := store.(AIJobsStore); ok {
			return aij
		}
		u, ok := store.(unwrapper)
		if !ok {
			break
		}
		store = u.Unwrap()
	}
	return nil
}
