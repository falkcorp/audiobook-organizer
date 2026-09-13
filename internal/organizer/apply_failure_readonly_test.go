// file: internal/organizer/apply_failure_readonly_test.go
// version: 1.0.0
// guid: 6c1e9a47-3b82-4f5d-a0e6-8d27b4f19c35
// last-edited: 2026-09-13

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// prefStore is a MockStore backed by a map that counts writes.
func prefStore(t *testing.T) (*database.MockStore, map[string]string, *int) {
	t.Helper()
	prefs := map[string]string{}
	writes := 0
	return &database.MockStore{
		GetUserPreferenceForUserFunc: func(_, key string) (*database.UserPreferenceKV, error) {
			v, ok := prefs[key]
			if !ok {
				return nil, nil
			}
			return &database.UserPreferenceKV{Key: key, Value: v}, nil
		},
		SetUserPreferenceForUserFunc: func(_, key, value string) error {
			writes++
			prefs[key] = value
			return nil
		},
	}, prefs, &writes
}

// The read-only check reports a stale record as not blocking and leaves it in
// place; the real rename path's check clears it. The preview and the pre-apply
// check use the read-only one, so they never write.
func TestApplyRenameBlockedReadOnly_NeverClears(t *testing.T) {
	store, prefs, writes := prefStore(t)
	gone := filepath.Join(t.TempDir(), "occupant-that-no-longer-exists.mp3")
	RecordApplyRenameFailure(store, ApplyRenameFailure{BookID: "b1", TargetPath: gone, OccupantPath: gone})
	*writes = 0

	if ApplyRenameBlockedReadOnly(store, "b1", []string{gone}) {
		t.Fatal("a record whose occupant is gone must not block")
	}
	if *writes != 0 {
		t.Fatalf("read-only check wrote %d preference(s)", *writes)
	}
	if _, ok := LoadApplyRenameFailure(store, "b1"); !ok {
		t.Fatal("read-only check cleared the record")
	}

	if ApplyRenameBlocked(store, "b1", []string{gone}) {
		t.Fatal("a record whose occupant is gone must not block")
	}
	if prefs[ApplyRenameFailurePrefix+"b1"] != "" {
		t.Fatal("ApplyRenameBlocked must still clear a stale record")
	}
}
