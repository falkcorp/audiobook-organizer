// file: internal/itunes/cleanup_merged_test.go
// version: 1.1.0
// guid: 7d3a0c81-4e29-4b6f-90a5-1c8e2f7b0d43
// last-edited: 2026-10-01

package itunes

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mergedCleanupTracks builds an itlTracks-equivalent inITL set and drives the
// (unexported) core so we don't need a real .itl. It mirrors the split used by
// ComputeMergedTrackCleanup: a non-primary PID is removed only if no primary
// book_file also owns it, and a track absent from the DB (music) is never a
// candidate.
func TestComputeMergedTrackCleanup_core(t *testing.T) {
	primaryV := true
	nonPrimary := false

	store := &mockRebuildStore{
		books: map[string]*database.Book{
			"bookP":  {ID: "bookP", IsPrimaryVersion: &primaryV},
			"bookM":  {ID: "bookM", IsPrimaryVersion: &nonPrimary},
			"bookS":  {ID: "bookS", IsPrimaryVersion: &primaryV},
			"bookM2": {ID: "bookM2", IsPrimaryVersion: &nonPrimary},
		},
		bookFiles: map[string][]database.BookFile{
			"bookP":  {{ITunesPersistentID: "AAAA0001", FilePath: "/x/p.m4b"}},
			"bookM":  {{ITunesPersistentID: "NNNN0001", FilePath: "/x/n.m4b"}},
			"bookS":  {{ITunesPersistentID: "5A5A0001", FilePath: "/x/s.m4b"}},
			"bookM2": {{ITunesPersistentID: "5A5A0001", FilePath: "/x/s2.m4b"}},
		},
	}

	// inITL: the primary P1, the merged N1, the shared PID, plus a MUSIC track
	// with no DB book at all.
	inITL := map[string]bool{
		"AAAA0001": true, // primary
		"NNNN0001": true, // merged non-primary → remove
		"5A5A0001": true, // shared (primary+non-primary) → keep
		"1234BEEF": true, // music, not in DB → never a candidate
	}

	ops, preview := computeMergedCleanupFromInITL(inITL, store)

	if preview.ToRemove != 1 {
		t.Errorf("ToRemove=%d, want 1 (only NNNN0001)", preview.ToRemove)
	}
	if preview.SharedSkipped != 1 {
		t.Errorf("SharedSkipped=%d, want 1 (5A5A0001)", preview.SharedSkipped)
	}
	if !ops.Removes["NNNN0001"] {
		t.Errorf("expected NNNN0001 in Removes")
	}
	if ops.Removes["5A5A0001"] {
		t.Errorf("shared PID 5A5A0001 must NOT be removed")
	}
	if ops.Removes["AAAA0001"] {
		t.Errorf("primary PID AAAA0001 must NOT be removed")
	}
	if ops.Removes["1234BEEF"] {
		t.Errorf("music PID 1234BEEF (not in DB) must NOT be removed")
	}
	if len(ops.Adds) != 0 || len(ops.LocationUpdates) != 0 {
		t.Errorf("cleanup must emit only Removes")
	}
}

// TestComputeMergedTrackCleanup_keepsOnlyTrackOfALiveFile: the folder-books
// repair retires a folder-book (demoted, soft-deleted) that keeps its PID'd
// row, and creates a live book holding the same path with no PID. That PID is
// the file's only iTunes track and must not be removed. A non-primary PID
// whose file a live primary owns WITH its own track is still a duplicate and
// is removed.
func TestComputeMergedTrackCleanup_keepsOnlyTrackOfALiveFile(t *testing.T) {
	yes, no, del := true, false, true
	store := &mockRebuildStore{
		books: map[string]*database.Book{
			"folder":  {ID: "folder", IsPrimaryVersion: &no, MarkedForDeletion: &del},
			"created": {ID: "created", IsPrimaryVersion: nil},
			"dupLose": {ID: "dupLose", IsPrimaryVersion: &no},
			"dupWin":  {ID: "dupWin", IsPrimaryVersion: &yes},
		},
		bookFiles: map[string][]database.BookFile{
			"folder":  {{ITunesPersistentID: "F0000001", FilePath: "/it/Author/Book/01.mp3"}},
			"created": {{FilePath: "/it/Author/Book/01.mp3"}},
			"dupLose": {{ITunesPersistentID: "D0000001", FilePath: "/x/d.m4b"}},
			"dupWin":  {{ITunesPersistentID: "D0000002", FilePath: "/x/d.m4b"}},
		},
	}
	inITL := map[string]bool{"F0000001": true, "D0000001": true, "D0000002": true}
	ops, preview := computeMergedCleanupFromInITL(inITL, store)
	if ops.Removes["F0000001"] {
		t.Errorf("the only track of a file a live primary owns was removed")
	}
	if preview.PathHeldSkipped != 1 {
		t.Errorf("PathHeldSkipped=%d, want 1", preview.PathHeldSkipped)
	}
	if !ops.Removes["D0000001"] {
		t.Errorf("a duplicate track of a file whose primary has its own track must still be removed")
	}
}
