// file: internal/database/store_global_test.go
// version: 1.0.0
// guid: 7c1e3a2b-4d5f-4a6b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-08-22

package database

import (
	"sync"
	"testing"
)

// TestGlobalStoreConcurrentAccess drives InitializeStore-style writes
// (SetGlobalStore, the same setter InitializeStore now calls), the real
// CloseStore, and GetGlobalStore concurrently. Run under `go test -race`:
// before the InitializeStore/CloseStore fix, CloseStore's bare
// `store := globalStore; globalStore = nil` raced with GetGlobalStore's
// RLock because neither held globalStoreMu; the race detector flagged it.
// With the fix, CloseStore takes globalStoreMu.Lock() around that sequence,
// so every access goes through the mutex and -race reports nothing.
//
// This test calls the real exported CloseStore/SetGlobalStore/GetGlobalStore
// (not a reimplementation of their locking) so that removing the lock added
// to CloseStore is actually observable here — see the mutation-test note in
// the PR description.
func TestGlobalStoreConcurrentAccess(t *testing.T) {
	// Save/restore so this test doesn't leak state into other tests in
	// the package that call GetGlobalStore/SetGlobalStore.
	prev := GetGlobalStore()
	t.Cleanup(func() {
		SetGlobalStore(prev)
	})

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for i := 0; i < goroutines; i++ {
		// Writer: mimics InitializeStore setting a new store.
		go func() {
			defer wg.Done()
			SetGlobalStore(&MockStore{})
		}()

		// Clearer: the real CloseStore, exercised concurrently with the
		// writer and reader below.
		go func() {
			defer wg.Done()
			_ = CloseStore()
		}()

		// Reader: mimics any GetGlobalStore() caller.
		go func() {
			defer wg.Done()
			_ = GetGlobalStore()
		}()
	}

	wg.Wait()
}
