// file: internal/itunes/relocate_test.go
// version: 1.0.0
// guid: 2f7b9c04-6d1e-4a83-b50f-9c2e1a7d3068
// last-edited: 2026-07-22

package itunes

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestRelocateOpsFromTracks exercises the per-file relocate diff: multi-file
// books match by each file's own PID, unmatched/unmappable files are skipped (not
// removed), and the op set NEVER contains removes or adds.
func TestRelocateOpsFromTracks(t *testing.T) {
	mappings := []PathMapping{{From: "W:", To: "/mnt/bigdata/books"}}
	primary := true
	notPrimary := false

	fpA1 := "/mnt/bigdata/books/audiobook-organizer/Author/Book/01.m4b"
	fpA2 := "/mnt/bigdata/books/audiobook-organizer/Author/Book/02.m4b"
	fpUnmatched := "/mnt/bigdata/books/audiobook-organizer/Author/Solo/01.m4b"
	fpCorrect := "/mnt/bigdata/books/audiobook-organizer/Author/Correct/01.m4b"
	fpUnmap := "/mnt/other/not-mapped/01.m4b" // not under the W: mapping → unmappable

	wantA1, ok := canonicalWinLocationForFile(fpA1, "AAAA0001", "test", mappings)
	if !ok {
		t.Fatalf("fpA1 should canonicalize")
	}
	wantA2, ok := canonicalWinLocationForFile(fpA2, "AAAA0002", "test", mappings)
	if !ok {
		t.Fatalf("fpA2 should canonicalize")
	}
	wantCorrect, ok := canonicalWinLocationForFile(fpCorrect, "CCCC0001", "test", mappings)
	if !ok {
		t.Fatalf("fpCorrect should canonicalize")
	}

	// ITL tracks keyed by UPPER-hex PID. A1/A2 sit at an OLD itunes path (differ →
	// relocate); CORRECT already sits at its wanted location; UNMAP exists but its
	// file can't map; UNMATCHED is deliberately ABSENT (never-imported → P2 add).
	itlTracks := map[string]*ITLTrack{
		"AAAA0001": {Location: `W:\itunes\old\01.m4b`},
		"AAAA0002": {Location: `W:\itunes\old\02.m4b`},
		"CCCC0001": {Location: wantCorrect},
		"DDDD0001": {Location: `W:\itunes\old\unmap.m4b`},
	}

	store := &mockRebuildStore{
		books: map[string]*database.Book{
			"book1": {ID: "book1", IsPrimaryVersion: &primary},
			"book2": {ID: "book2", IsPrimaryVersion: &primary},
			"book3": {ID: "book3", IsPrimaryVersion: &primary},
			"book4": {ID: "book4", IsPrimaryVersion: &notPrimary}, // skipped entirely
			"book5": {ID: "book5", IsPrimaryVersion: &primary},
		},
		bookFiles: map[string][]database.BookFile{
			"book1": {
				{ITunesPersistentID: "AAAA0001", FilePath: fpA1},
				{ITunesPersistentID: "AAAA0002", FilePath: fpA2},
			},
			"book2": {{ITunesPersistentID: "BBBB0001", FilePath: fpUnmatched}},
			"book3": {{ITunesPersistentID: "CCCC0001", FilePath: fpCorrect}},
			"book4": {{ITunesPersistentID: "EEEE0001", FilePath: fpA1}},
			"book5": {{ITunesPersistentID: "DDDD0001", FilePath: fpUnmap}},
		},
	}

	ops, preview, err := relocateOpsFromTracks(itlTracks, store, mappings)
	if err != nil {
		t.Fatalf("relocateOpsFromTracks: %v", err)
	}

	// The load-bearing safety property: a relocate never removes or adds a track.
	if len(ops.Removes) != 0 {
		t.Errorf("Removes must be empty, got %d", len(ops.Removes))
	}
	if len(ops.Adds) != 0 {
		t.Errorf("Adds must be empty, got %d", len(ops.Adds))
	}
	if len(ops.MetadataUpdates) != 0 {
		t.Errorf("MetadataUpdates must be empty, got %d", len(ops.MetadataUpdates))
	}

	if preview.FilesConsidered != 5 {
		t.Errorf("FilesConsidered=%d, want 5 (book4 non-primary skipped)", preview.FilesConsidered)
	}
	if preview.Matched != 4 {
		t.Errorf("Matched=%d, want 4 (A1,A2,CORRECT,UNMAP)", preview.Matched)
	}
	if preview.ToRelocate != 2 {
		t.Errorf("ToRelocate=%d, want 2", preview.ToRelocate)
	}
	if preview.AlreadyCorrect != 1 {
		t.Errorf("AlreadyCorrect=%d, want 1", preview.AlreadyCorrect)
	}
	if preview.UnmatchedFiles != 1 {
		t.Errorf("UnmatchedFiles=%d, want 1", preview.UnmatchedFiles)
	}
	if preview.Unmappable != 1 {
		t.Errorf("Unmappable=%d, want 1", preview.Unmappable)
	}
	if len(ops.LocationUpdates) != 2 {
		t.Fatalf("LocationUpdates=%d, want 2", len(ops.LocationUpdates))
	}

	got := map[string]string{}
	for _, u := range ops.LocationUpdates {
		got[u.PersistentID] = u.NewLocation
	}
	if got["AAAA0001"] != wantA1 {
		t.Errorf("A1 NewLocation=%q, want %q", got["AAAA0001"], wantA1)
	}
	if got["AAAA0002"] != wantA2 {
		t.Errorf("A2 NewLocation=%q, want %q", got["AAAA0002"], wantA2)
	}
}
