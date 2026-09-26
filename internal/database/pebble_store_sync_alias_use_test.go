// file: internal/database/pebble_store_sync_alias_use_test.go
// version: 1.1.0
// guid: 0b7e4f52-91c3-4d8a-a6e2-5f3c18d97b40
// last-edited: 2026-09-25

package database

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSyncAliasUse_RecordListSeed(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	got, seeded, err := store.ListSyncAliasUses("u1")
	if err != nil || got != nil || seeded {
		t.Fatalf("empty: got %v seeded=%v err=%v; want nil, false, nil", got, seeded, err)
	}

	for _, a := range []string{"alias-b", "alias-a", "alias-b"} {
		if err := store.RecordSyncAliasUse("u1", a); err != nil {
			t.Fatalf("RecordSyncAliasUse(%s): %v", a, err)
		}
	}
	// Another user whose id extends u1's must not leak into u1's scan.
	if err := store.RecordSyncAliasUse("u10", "alias-z"); err != nil {
		t.Fatal(err)
	}

	got, seeded, err = store.ListSyncAliasUses("u1")
	if err != nil || seeded || !slices.Equal(got, []string{"alias-a", "alias-b"}) {
		t.Fatalf("after record: got %v seeded=%v err=%v; want [alias-a alias-b], false", got, seeded, err)
	}

	if err := store.SeedSyncAliasUses("u1", []string{"alias-c", "alias-a"}, true); err != nil {
		t.Fatalf("SeedSyncAliasUses: %v", err)
	}
	got, seeded, err = store.ListSyncAliasUses("u1")
	if err != nil || !seeded || !slices.Equal(got, []string{"alias-a", "alias-b", "alias-c"}) {
		t.Fatalf("after seed: got %v seeded=%v err=%v; want [alias-a alias-b alias-c], true", got, seeded, err)
	}

	if got, seeded, err := store.ListSyncAliasUses("u10"); err != nil || seeded || !slices.Equal(got, []string{"alias-z"}) {
		t.Fatalf("u10: got %v seeded=%v err=%v", got, seeded, err)
	}
}

func TestSyncAliasUse_RejectsAmbiguousIDs(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	for _, tc := range [][2]string{{"", "a"}, {"u:1", "a"}, {"u1", ""}, {"u1", "a:b"}} {
		if err := store.RecordSyncAliasUse(tc[0], tc[1]); err == nil {
			t.Fatalf("RecordSyncAliasUse(%q, %q) accepted an ambiguous id", tc[0], tc[1])
		}
	}
	if _, _, err := store.ListSyncAliasUses("u:1"); err == nil {
		t.Fatal("ListSyncAliasUses accepted a user id containing ':'")
	}
}

// TestSyncAliasUse_PartialSeedLeavesUserUnseeded: complete=false records the
// aliases found but not the seeded mark, so the caller's next list retries
// the seed (abs/userdata.go seedAliasUses: a failed per-item lookup).
func TestSyncAliasUse_PartialSeedLeavesUserUnseeded(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	if err := store.SeedSyncAliasUses("u1", []string{"alias-a"}, false); err != nil {
		t.Fatalf("partial seed: %v", err)
	}
	got, seeded, err := store.ListSyncAliasUses("u1")
	if err != nil || seeded || !slices.Equal(got, []string{"alias-a"}) {
		t.Fatalf("after partial seed: got %v seeded=%v err=%v; want [alias-a], false", got, seeded, err)
	}
	// An empty partial seed writes nothing and is not an error.
	if err := store.SeedSyncAliasUses("u2", nil, false); err != nil {
		t.Fatalf("empty partial seed: %v", err)
	}
	if got, seeded, err := store.ListSyncAliasUses("u2"); err != nil || seeded || got != nil {
		t.Fatalf("u2: got %v seeded=%v err=%v; want nil, false", got, seeded, err)
	}
	if err := store.SeedSyncAliasUses("u1", []string{"alias-b"}, true); err != nil {
		t.Fatalf("complete seed: %v", err)
	}
	got, seeded, err = store.ListSyncAliasUses("u1")
	if err != nil || !seeded || !slices.Equal(got, []string{"alias-a", "alias-b"}) {
		t.Fatalf("after complete seed: got %v seeded=%v err=%v; want [alias-a alias-b], true", got, seeded, err)
	}
}

// TestSyncAliasUse_ListIsConsistentWithAConcurrentSeed: ListSyncAliasUses
// reads the alias keys and the seeded flag from one snapshot. Read apart, a
// seed committing between the two reads is reported as seeded=true with none
// of its aliases, and the caller (abs/userdata.go usedAliases) then skips the
// seed and sends no alias rows. For each user a reader spins until it sees
// seeded while the seed commits; seeded must always come with the alias.
func TestSyncAliasUse_ListIsConsistentWithAConcurrentSeed(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	// Many prior aliases lengthen the iteration, widening the window between
	// the alias scan and the flag read that a non-snapshot read has.
	var prior []string
	for i := range 200 {
		prior = append(prior, fmt.Sprintf("prior-%03d", i))
	}
	for u := range 300 {
		user := fmt.Sprintf("user%03d", u)
		if err := store.SeedSyncAliasUses(user, prior, false); err != nil {
			t.Fatal(err)
		}
	}
	for u := range 300 {
		user := fmt.Sprintf("user%03d", u)
		done := make(chan struct{})
		var bad []string
		go func() {
			defer close(done)
			for {
				got, seeded, err := store.ListSyncAliasUses(user)
				if err != nil {
					bad = []string{"err: " + err.Error()}
					return
				}
				if seeded {
					if !slices.Contains(got, "seeded-alias") {
						bad = got
					}
					return
				}
			}
		}()
		if err := store.SeedSyncAliasUses(user, []string{"seeded-alias"}, true); err != nil {
			t.Fatal(err)
		}
		<-done
		if bad != nil {
			t.Fatalf("%s: ListSyncAliasUses returned seeded=true without the seeded alias (%d aliases): a torn read",
				user, len(bad))
		}
	}
}

// TestSyncAliasUse_SeedCutoffIsWriteOnce:the first call persists its time and
// every later call, including after a restart (the store reopened on the same
// path), returns that same time. A cutoff that moved on restart would re-open
// the seed window (abs/userdata.go aliasSeedSince).
func TestSyncAliasUse_SeedCutoffIsWriteOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cutoff-db")
	first := time.Date(2026, 9, 26, 3, 4, 5, 678_000_000, time.UTC)

	store, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := store.SyncAliasUseSeedCutoff(first)
	if err != nil || !got.Equal(first) {
		_ = store.Close()
		t.Fatalf("first call = %v, %v; want %v", got, err, first)
	}
	if got, err := store.SyncAliasUseSeedCutoff(first.Add(time.Hour)); err != nil || !got.Equal(first) {
		_ = store.Close()
		t.Fatalf("second call = %v, %v; want the stored %v", got, err, first)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if got, err := reopened.SyncAliasUseSeedCutoff(first.Add(30 * 24 * time.Hour)); err != nil || !got.Equal(first) {
		t.Fatalf("after restart = %v, %v; want the stored %v (the cutoff must not move)", got, err, first)
	}
}

// TestSyncAliasUse_SeedCutoffCorruptValueIsAnError: an unreadable stored
// cutoff is an error, never silently replaced by "now", which would move it.
func TestSyncAliasUse_SeedCutoffCorruptValueIsAnError(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	if err := store.db.Set(syncAliasUseSeedCutoffKey, []byte("not a time"), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := store.SyncAliasUseSeedCutoff(time.Now()); err == nil {
		t.Fatalf("corrupt cutoff returned %v with no error", got)
	}
	v, closer, err := store.db.Get(syncAliasUseSeedCutoffKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	if string(v) != "not a time" {
		t.Fatalf("stored cutoff was rewritten to %q", v)
	}
}
