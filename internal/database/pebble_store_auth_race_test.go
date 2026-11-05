// file: internal/database/pebble_store_auth_race_test.go
// version: 1.0.0
// guid: 6a5b6f78-7e66-426e-9046-11726edbf0a1
// last-edited: 2026-11-05

package database

import (
	"sync"
	"testing"
	"time"
)

// raceRounds is how many times each race below is replayed. One round opens
// the window only sometimes; enough rounds make a lost write show up on any
// run of the unfixed store (05-authn §11.1 item 6).
const raceRounds = 200

// raceTouchers is the number of goroutines hammering TouchAPIKeyLastUsed in
// each round. Fixed and small: this is a lock test, not a load test.
const raceTouchers = 8

// raceTouchIP is the client address the touches record; any string will do.
const raceTouchIP = "batcave"

// raceTouchKey runs raceTouchers goroutines touching id in a tight loop,
// waits until every one of them has touched at least once, runs write, then
// stops the touchers and waits for them. A touch that read the row before
// write committed and writes it back after is exactly the lost update the
// lock exists to prevent.
func raceTouchKey(t *testing.T, store *PebbleStore, id string, write func() error) {
	t.Helper()
	stop := make(chan struct{})
	var started, done sync.WaitGroup
	started.Add(raceTouchers)
	done.Add(raceTouchers)
	for i := 0; i < raceTouchers; i++ {
		go func() {
			defer done.Done()
			first := true
			for {
				if err := store.TouchAPIKeyLastUsed(id, time.Now(), raceTouchIP); err != nil {
					t.Errorf("touch: %v", err)
				}
				if first {
					started.Done()
					first = false
				}
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}
	started.Wait()
	if err := write(); err != nil {
		t.Errorf("write: %v", err)
	}
	// Let touches that were in flight across the write land before stopping.
	for i := 0; i < raceTouchers; i++ {
		if err := store.TouchAPIKeyLastUsed(id, time.Now(), raceTouchIP); err != nil {
			t.Errorf("touch: %v", err)
		}
	}
	close(stop)
	done.Wait()
}

// TestLegacyRevokeRacesTouch_NeverUnrevokes: a revoke (and an expiry stamp)
// racing the per-request TouchAPIKeyLastUsed is never overwritten by a touch
// that read the row before it. Before early fix 6, SetAPIKeyStatus and
// SetAPIKeyExpiry did their read-modify-write without apiKeyMu, so a touch
// holding the lock could write the pre-revoke row back over the revoke.
func TestLegacyRevokeRacesTouch_NeverUnrevokes(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	t.Run("revoke", func(t *testing.T) {
		lost := 0
		for round := 0; round < raceRounds; round++ {
			key, err := store.CreateAPIKey(&APIKey{UserID: "u-bruce", Name: "The Long Halloween", Status: "active"})
			if err != nil {
				t.Fatalf("round %d: create: %v", round, err)
			}
			raceTouchKey(t, store, key.ID, func() error { return store.RevokeAPIKey(key.ID) })
			got, err := store.GetAPIKey(key.ID)
			if err != nil || got == nil {
				t.Fatalf("round %d: get: %v / %v", round, got, err)
			}
			if got.Status != "revoked" || got.RevokedAt == nil {
				if lost == 0 {
					t.Logf("round %d: revoke undone by a touch: status=%q revoked_at=%v", round, got.Status, got.RevokedAt)
				}
				lost++
			}
		}
		if lost > 0 {
			t.Errorf("%d/%d revokes undone by a touch that read the row before the revoke", lost, raceRounds)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		lost := 0
		for round := 0; round < raceRounds; round++ {
			before := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			key, err := store.CreateAPIKey(&APIKey{UserID: "u-bruce", Name: "Year One", Status: "active", ExpiresAt: &before})
			if err != nil {
				t.Fatalf("round %d: create: %v", round, err)
			}
			want := before.Add(24 * time.Hour)
			raceTouchKey(t, store, key.ID, func() error { return store.SetAPIKeyExpiry(key.ID, want) })
			got, err := store.GetAPIKey(key.ID)
			if err != nil || got == nil {
				t.Fatalf("round %d: get: %v / %v", round, got, err)
			}
			if got.ExpiresAt == nil || !got.ExpiresAt.Equal(want) {
				if lost == 0 {
					t.Logf("round %d: expiry overwritten by a touch: got %v, want %v", round, got.ExpiresAt, want)
				}
				lost++
			}
		}
		if lost > 0 {
			t.Errorf("%d/%d expiry writes overwritten by a touch that read the row before them", lost, raceRounds)
		}
	})
}
