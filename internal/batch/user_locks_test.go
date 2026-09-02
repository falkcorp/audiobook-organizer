// file: internal/batch/user_locks_test.go
// version: 1.0.0
// guid: 1e5b8c47-9d20-4a63-b7f1-4c8e2a05d963
// last-edited: 2026-09-02

package batch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A batch update is a human editing many books at once. Until 2026-09-02 it
// wrote the columns and recorded no lock row at all, so every field the user
// set in bulk was overwritable by the next metadata fetch or scan -- while the
// UI told them edited fields are locked.
func batchLockFixture() (*MockBookStore, *BatchService) {
	store := NewMockBookStore()
	store.books["b1"] = &database.Book{ID: "b1", Title: "Old Title"}
	store.authors = map[int]string{7: "Isaac Asimov"}
	store.seriesNm = map[int]string{9: "Foundation"}
	return store, NewBatchService(store)
}

func lockedValue(t *testing.T, store *MockBookStore, bookID, field string) any {
	t.Helper()
	st, ok := store.locks[bookID][field]
	if !ok {
		t.Fatalf("no lock row for %s/%s; rows = %v", bookID, field, store.locks[bookID])
	}
	if !st.HasUserOverride() {
		t.Errorf("lock row for %s is not a user override: %+v", field, st)
	}
	return decodeOverride(st)
}

func decodeOverride(st database.MetadataFieldState) any {
	if st.OverrideValue == nil {
		return nil
	}
	return *st.OverrideValue
}

func TestUpdateAudiobooks_RecordsALockRowPerEditedField(t *testing.T) {
	store, svc := batchLockFixture()

	resp := svc.UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{"b1"},
		Updates: map[string]any{
			"title":     "New Title",
			"narrator":  "Scott Brick",
			"author_id": float64(7),
			"series_id": float64(9),
			// Not lockable: plumbing, and a row under this key would be read
			// by no guard.
			"file_path": "/x/y.m4b",
		},
	})
	if resp.Success != 1 {
		t.Fatalf("success = %d, want 1: %+v", resp.Success, resp.Results)
	}

	if got := lockedValue(t, store, "b1", database.FieldKeyTitle); got != `"New Title"` {
		t.Errorf("title override = %v", got)
	}
	if got := lockedValue(t, store, "b1", database.FieldKeyNarrator); got != `"Scott Brick"` {
		t.Errorf("narrator override = %v", got)
	}
	// The ids are resolved to the NAMES the vocabulary locks.
	if got := lockedValue(t, store, "b1", database.FieldKeyAuthorName); got != `"Isaac Asimov"` {
		t.Errorf("author_name override = %v, want the resolved name", got)
	}
	if got := lockedValue(t, store, "b1", database.FieldKeySeriesName); got != `"Foundation"` {
		t.Errorf("series_name override = %v, want the resolved name", got)
	}
	if _, ok := store.locks["b1"]["file_path"]; ok {
		t.Error("file_path is not lockable; a row under it is read by no guard")
	}

	// The guard every writer goes through must now see them.
	locks, err := database.LoadFieldLocks(store, "b1")
	if err != nil {
		t.Fatalf("locks: %v", err)
	}
	for _, key := range []string{
		database.FieldKeyTitle, database.FieldKeyNarrator,
		database.FieldKeyAuthorName, database.FieldKeySeriesName,
	} {
		if !locks.Locked(key) {
			t.Errorf("%s is not locked after a user batch edit", key)
		}
	}
}

func TestExecuteOperations_RecordsALockRow(t *testing.T) {
	store, svc := batchLockFixture()

	resp := svc.ExecuteOperations(&BatchOperationsRequest{
		Operations: []BatchOperationItem{{
			ID: "b1", Action: "update",
			Updates: map[string]any{"publisher": "Tor", "series_sequence": float64(3)},
		}},
	})
	if resp.Success != 1 {
		t.Fatalf("success = %d, want 1: %+v", resp.Success, resp.Results)
	}
	if got := lockedValue(t, store, "b1", database.FieldKeyPublisher); got != `"Tor"` {
		t.Errorf("publisher override = %v", got)
	}
	// series_sequence is this package's spelling of series_position.
	if got := lockedValue(t, store, "b1", database.FieldKeySeriesPosition); got != "3" {
		t.Errorf("series_position override = %v", got)
	}
}

// A lock row that cannot be written must fail the item, not be dropped: the
// column write already happened, and reporting success would tell the user
// their edit is protected when it is not.
func TestUpdateAudiobooks_LockWriteFailureFailsTheItem(t *testing.T) {
	store, svc := batchLockFixture()
	store.lockErr = errors.New("pebble: closed")

	resp := svc.UpdateAudiobooks(&BatchUpdateRequest{
		IDs:     []string{"b1"},
		Updates: map[string]any{"title": "New Title"},
	})
	if resp.Success != 0 || resp.Failed != 1 {
		t.Fatalf("success=%d failed=%d, want 0/1", resp.Success, resp.Failed)
	}
}

// A batch that touches no lockable field writes no rows at all.
func TestUpdateAudiobooks_NonLockableFieldsWriteNoRows(t *testing.T) {
	store, svc := batchLockFixture()

	resp := svc.UpdateAudiobooks(&BatchUpdateRequest{
		IDs:     []string{"b1"},
		Updates: map[string]any{"file_path": "/x/y.m4b", "library_state": "owned"},
	})
	if resp.Success != 1 {
		t.Fatalf("success = %d, want 1", resp.Success)
	}
	if len(store.locks["b1"]) != 0 {
		t.Errorf("rows = %v, want none for a plumbing-only update", store.locks["b1"])
	}
}
