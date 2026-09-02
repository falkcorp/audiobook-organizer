// file: internal/audiobooks/legacy_metadata_state_migration_test.go
// version: 1.0.0
// guid: 2a6f9d18-7c34-4e05-b1a8-9f3c6d0e4b72
// last-edited: 2026-09-02

package audiobooks

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// Before 2026-09-02 nothing ever deleted the pre-migration state blob. The
// per-field rows were written from it and the blob stayed, so for any book that
// had one, "unlock every field" deleted the rows and the very next read fell
// straight back through to the blob and found the field locked again. The
// unlock was INERT and the UI reported it as done.
//
// A book whose state was ALWAYS in rows never had a blob, so no test that
// starts from rows can observe this. This one starts from the blob.
func TestUnlockAfterLegacyMigrationIsNotInert(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	svc := NewAudiobookService(store)

	// 1. The user locked the title, back when state lived in the blob.
	blob, _ := json.Marshal(map[string]map[string]any{
		"title": {"override_value": "My Title", "override_locked": true},
	})
	if err := store.SetUserPreference(metastate.Key("b1"), string(blob)); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}

	// 2. Opening the book migrates the blob into rows.
	state, err := svc.loadMetadataState("b1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if entry, ok := state["title"]; !ok || !entry.OverrideLocked {
		t.Fatalf("state = %+v, want the legacy title lock migrated", state)
	}
	rows, _ := store.GetMetadataFieldStates("b1")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the blob migrated into 1 row", len(rows))
	}

	// The guard reads the lock through the same chokepoint every writer uses.
	locks, err := database.LoadFieldLocks(store, "b1")
	if err != nil {
		t.Fatalf("locks: %v", err)
	}
	if !locks.Locked(database.FieldKeyTitle) {
		t.Fatal("title must be locked before the unlock, or the test proves nothing")
	}

	// 3. The user unlocks everything: the row set is saved empty.
	if err := svc.saveMetadataState("b1", map[string]metadataFieldState{}); err != nil {
		t.Fatalf("save empty: %v", err)
	}

	// 4. It must STAY unlocked. This is the assertion that failed before the
	// blob was retired on save.
	locks, err = database.LoadFieldLocks(store, "b1")
	if err != nil {
		t.Fatalf("locks after unlock: %v", err)
	}
	if locks.Locked(database.FieldKeyTitle) {
		t.Error("title is locked again after unlocking: the legacy blob outlived its migration")
	}
	if locks.Any() {
		t.Errorf("locks after unlock = %v, want none", locks)
	}
	pref, _ := store.GetUserPreference(metastate.Key("b1"))
	if pref != nil && pref.Value != nil && *pref.Value != "" {
		t.Errorf("legacy blob still present after a successful save: %q", *pref.Value)
	}

	// And a re-read through the service agrees.
	state, err = svc.loadMetadataState("b1")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(state) != 0 {
		t.Errorf("state after unlock = %+v, want empty", state)
	}
}

// The non-empty half: a save that keeps one field must retire the blob too,
// and the kept field must survive.
func TestPartialUnlockAfterLegacyMigrationKeepsTheOtherLock(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	svc := NewAudiobookService(store)

	blob, _ := json.Marshal(map[string]map[string]any{
		"title":     {"override_value": "My Title", "override_locked": true},
		"publisher": {"override_value": "My Press", "override_locked": true},
	})
	if err := store.SetUserPreference(metastate.Key("b1"), string(blob)); err != nil {
		t.Fatalf("seed legacy blob: %v", err)
	}
	state, err := svc.loadMetadataState("b1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	delete(state, "title")
	if err := svc.saveMetadataState("b1", state); err != nil {
		t.Fatalf("save: %v", err)
	}

	locks, err := database.LoadFieldLocks(store, "b1")
	if err != nil {
		t.Fatalf("locks: %v", err)
	}
	if locks.Locked(database.FieldKeyTitle) {
		t.Error("the unlocked title came back from the legacy blob")
	}
	if !locks.Locked(database.FieldKeyPublisher) {
		t.Error("the publisher lock the user kept was lost")
	}
	// The rows answer the question here (the publisher row exists, so
	// LockedUserFields never reaches the fallback), which is exactly why the
	// blob's removal has to be asserted directly: a later save that empties
	// the rows would otherwise resurrect BOTH locks.
	if pref, _ := store.GetUserPreference(metastate.Key("b1")); pref != nil && pref.Value != nil && *pref.Value != "" {
		t.Errorf("legacy blob still present after a partial save: %q", *pref.Value)
	}
}
