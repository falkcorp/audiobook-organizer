// file: internal/itunes/pid_integrity_test.go
// version: 1.0.0
// guid: c9a3e714-0d62-4b58-9f21-6e4a2c8b1d70
// last-edited: 2026-07-23

package itunes

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pidCensusMock is a minimal PIDIntegrityStore: a flat book_file list plus a
// book-by-id map (including soft-deleted books, which GetBookByID must return).
type pidCensusMock struct {
	files []database.BookFileCore
	books map[string]*database.Book
}

func (m *pidCensusMock) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return m.files, nil
}

func (m *pidCensusMock) GetBookByID(id string) (*database.Book, error) {
	return m.books[id], nil
}

func bf(id, bookID, path, pid string) database.BookFileCore {
	return database.BookFileCore{ID: id, BookID: bookID, FilePath: path, ITunesPersistentID: pid}
}

// TestComputePIDIntegrity exercises the duplicate-PID classification and the
// relocate-correctness probe with no real .itl (itlPath = "").
func TestComputePIDIntegrity(t *testing.T) {
	primary := true
	notPrimary := false

	store := &pidCensusMock{
		files: []database.BookFileCore{
			// UNIQUE — one owner, never a duplicate.
			bf("f_uniq", "b_uniq", "/mnt/x/uniq.m4b", "AAAA0001"),
			// SAME_FILE dup — two rows, same path, same PID.
			bf("f_sf1", "b_sf1", "/mnt/x/same.m4b", "BBBB0002"),
			bf("f_sf2", "b_sf2", "/mnt/x/same.m4b", "BBBB0002"),
			// DIFF_FILE dup across TWO live primaries → relocate order-dependent.
			bf("f_df1", "b_df1", "/mnt/x/one.m4b", "CCCC0003"),
			bf("f_df2", "b_df2", "/mnt/x/two.m4b", "CCCC0003"),
			// DIFF_FILE dup primary + NON-primary → NOT counted by the primaries probe.
			bf("f_pn1", "b_pn1", "/mnt/x/a.m4b", "DDDD0004"),
			bf("f_pn2", "b_pn2", "/mnt/x/b.m4b", "DDDD0004"),
			// empty PID — ignored entirely.
			bf("f_empty", "b_empty", "/mnt/x/none.m4b", ""),
		},
		books: map[string]*database.Book{
			"b_uniq": {ID: "b_uniq", IsPrimaryVersion: &primary},
			"b_sf1":  {ID: "b_sf1", IsPrimaryVersion: &primary},
			"b_sf2":  {ID: "b_sf2", IsPrimaryVersion: &notPrimary},
			"b_df1":  {ID: "b_df1", IsPrimaryVersion: &primary},
			"b_df2":  {ID: "b_df2", IsPrimaryVersion: &primary},
			"b_pn1":  {ID: "b_pn1", IsPrimaryVersion: &primary},
			"b_pn2":  {ID: "b_pn2", IsPrimaryVersion: &notPrimary},
		},
	}

	rep, err := ComputePIDIntegrity(store, "")
	if err != nil {
		t.Fatalf("ComputePIDIntegrity: %v", err)
	}

	if rep.DistinctPIDs != 4 { // AAAA(uniq), BBBB(same), CCCC(diff), DDDD(prim+nonprim)
		t.Errorf("DistinctPIDs = %d, want 4", rep.DistinctPIDs)
	}
	if rep.DuplicatePIDs != 3 { // BBBB, CCCC, DDDD
		t.Errorf("DuplicatePIDs = %d, want 3", rep.DuplicatePIDs)
	}
	if rep.DupSameFile != 1 {
		t.Errorf("DupSameFile = %d, want 1 (BBBB)", rep.DupSameFile)
	}
	if rep.DupDiffFile != 2 {
		t.Errorf("DupDiffFile = %d, want 2 (CCCC, DDDD)", rep.DupDiffFile)
	}
	if rep.FilesToClear != 3 { // one extra owner per dup PID
		t.Errorf("FilesToClear = %d, want 3", rep.FilesToClear)
	}
	// Only CCCC has two LIVE PRIMARY owners with differing paths.
	if rep.PIDsOnMultiplePrimariesDiffPath != 1 {
		t.Errorf("PIDsOnMultiplePrimariesDiffPath = %d, want 1 (CCCC only)", rep.PIDsOnMultiplePrimariesDiffPath)
	}
}
