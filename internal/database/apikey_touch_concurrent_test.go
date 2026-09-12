// file: internal/database/apikey_touch_concurrent_test.go
// version: 1.0.1
// guid: 7d4a9e02-5b81-4c37-a6f0-2e9c8b31d570
// last-edited: 2026-09-12

package database

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestTouchAPIKeyLastUsed_ConcurrentTouchesDoNotLoseIncrements is the regression
// guard for a lost-update race that was live in production.
//
// Every authenticated request spawned a goroutine calling TouchAPIKeyLastUsed,
// and the implementation was an unsynchronised read-modify-write:
//
//	k, _ := p.GetAPIKey(id)   // both goroutines read UseCount = N
//	k.UseCount++              // both compute N+1
//	p.db.Set(...)             // both write N+1 — one increment is gone
//
// Concurrent requests on the SAME key therefore undercounted UseCount, and
// LastUsedAt/LastUsedIP recorded whichever goroutine happened to write last
// rather than the most recent request. Nothing about shutdown was required to
// trigger it: ordinary concurrent traffic was enough.
//
// The count is the assertion because it is the only field whose correct value is
// knowable independent of scheduling.
func TestTouchAPIKeyLastUsed_ConcurrentTouchesDoNotLoseIncrements(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	key, err := store.CreateAPIKey(&APIKey{UserID: "user-1", Name: "concurrency-probe"})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	const touches = 200
	var wg sync.WaitGroup
	now := time.Now()
	for range touches {
		wg.Go(func() {
			if err := store.TouchAPIKeyLastUsed(key.ID, now, "127.0.0.1"); err != nil {
				t.Errorf("touch: %v", err)
			}
		})
	}
	waitGroupOrFatal(t, &wg, "concurrent API-key touch workers")

	got, err := store.GetAPIKey(key.ID)
	if err != nil {
		t.Fatalf("read back api key: %v", err)
	}
	if got == nil {
		t.Fatal("api key vanished")
	}
	if got.UseCount != touches {
		t.Errorf("UseCount = %d after %d concurrent touches, want %d — %d increments were lost to "+
			"an unsynchronised read-modify-write", got.UseCount, touches, touches, touches-got.UseCount)
	}
}
