// file: internal/database/pebble_store_sync_alias_use_test.go
// version: 1.0.0
// guid: 0b7e4f52-91c3-4d8a-a6e2-5f3c18d97b40
// last-edited: 2026-09-25

package database

import (
	"slices"
	"testing"
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

	if err := store.SeedSyncAliasUses("u1", []string{"alias-c", "alias-a"}); err != nil {
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
