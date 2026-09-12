// file: internal/audiobooks/revert_series_rename_roundtrip_test.go
// version: 1.0.0
// guid: 9f41c6ab-2d78-4e03-b5c9-6e1a7d84f3b2
// last-edited: 2026-09-12

package audiobooks_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// mergeRenamed runs the real write site, dedup.MergeSeries with a custom name,
// against a Pebble store and returns the store and the renamed series id.
func mergeRenamed(t *testing.T) (*database.PebbleStore, int) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	keep, err := store.CreateSeries("Old Name", nil)
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	if _, err := dedup.MergeSeries(context.Background(), store, "op1", keep.ID, nil, "New Name", nil); err != nil {
		t.Fatalf("MergeSeries: %v", err)
	}
	return store, keep.ID
}

// The write site records a series-scoped row, and reverting the operation
// renames the series back, name index included.
func TestSeriesRename_RecordThenRevert_RoundTrip(t *testing.T) {
	store, id := mergeRenamed(t)

	changes, err := store.GetOperationChanges("op1")
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, err %v, want one row", changes, err)
	}
	c := changes[0]
	if c.ChangeType != undo.ChangeTypeSeriesRename || c.SeriesID == nil || *c.SeriesID != id ||
		c.OldValue != "Old Name" || c.NewValue != "New Name" || c.BookID != "" {
		t.Fatalf("row = %+v (series_id %v), want series_rename of series %d Old Name -> New Name", c, c.SeriesID, id)
	}

	result, err := audiobooks.NewRevertService(store).RevertOperation("op1")
	if err != nil || result.Restored != 1 {
		t.Fatalf("RevertOperation: %v (result %+v)", err, result)
	}
	s, _ := store.GetSeriesByID(id)
	if s == nil || s.Name != "Old Name" {
		t.Fatalf("series after revert = %+v, want Old Name", s)
	}
	if byName, _ := store.GetSeriesByName("Old Name", nil); byName == nil || byName.ID != id {
		t.Errorf("GetSeriesByName(Old Name) = %+v, want series %d", byName, id)
	}
}

// The preflight and the revert agree on every series_rename outcome: a row the
// preflight files as Safe is restored, and a row it files under a series
// conflict bucket is refused by the revert and left unmarked.
func TestSeriesRename_PreflightMatchesRevert(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, store *database.PebbleStore, id int)
		bucket func(r *undo.UndoConflictReport) int
	}{
		{"clean", func(*testing.T, *database.PebbleStore, int) {}, func(r *undo.UndoConflictReport) int { return r.Safe }},
		{"renamed since", func(t *testing.T, store *database.PebbleStore, id int) {
			if err := store.UpdateSeriesName(id, "Third Name"); err != nil {
				t.Fatal(err)
			}
		}, func(r *undo.UndoConflictReport) int { return len(r.SeriesRenamedSince) }},
		{"old name taken", func(t *testing.T, store *database.PebbleStore, _ int) {
			if _, err := store.CreateSeries("Old Name", nil); err != nil {
				t.Fatal(err)
			}
		}, func(r *undo.UndoConflictReport) int { return len(r.SeriesNameTaken) }},
		{"series deleted", func(t *testing.T, store *database.PebbleStore, id int) {
			if err := store.DeleteSeries(id); err != nil {
				t.Fatal(err)
			}
		}, func(r *undo.UndoConflictReport) int { return len(r.SeriesDeleted) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, id := mergeRenamed(t)
			tc.mutate(t, store, id)

			report, err := undo.PreflightUndoConflicts(store, "op1")
			if err != nil {
				t.Fatalf("preflight: %v", err)
			}
			if tc.bucket(report) != 1 {
				t.Fatalf("report = %+v, want the row in the %q bucket", report, tc.name)
			}
			safe := report.Safe == 1

			result, _ := audiobooks.NewRevertService(store).RevertOperation("op1")
			if result == nil {
				t.Fatal("RevertOperation returned no result")
			}
			if safe != (result.Restored == 1) || safe == (result.Failed == 1) {
				t.Errorf("preflight safe=%v but revert restored=%d failed=%d", safe, result.Restored, result.Failed)
			}
			changes, _ := store.GetOperationChanges("op1")
			if marked := changes[0].RevertedAt != nil; marked != safe {
				t.Errorf("row marked reverted = %v, want %v", marked, safe)
			}
		})
	}
}
