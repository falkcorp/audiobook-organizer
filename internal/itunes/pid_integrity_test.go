// file: internal/itunes/pid_integrity_test.go
// version: 1.1.0
// guid: c9a3e714-0d62-4b58-9f21-6e4a2c8b1d70
// last-edited: 2026-07-24

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

func bfh(id, bookID, path, pid, hash string) database.BookFileCore {
	f := bf(id, bookID, path, pid)
	f.FileHash = hash
	return f
}

// TestComputeMergeOrphanCensus exercises the .itl-track bucketing + the merge
// provenance intersection (journal ∪ MergedIntoBookID) + the SHA gate, driving the
// pure core with an injected PID set (no binary .itl needed).
func TestComputeMergeOrphanCensus(t *testing.T) {
	primary := true
	notPrimary := false
	del := true

	store := &pidCensusMock{
		files: []database.BookFileCore{
			// HEALTHY — live primary owns the track.
			bf("f_h", "b_h", "/mnt/x/healthy.m4b", "AAAA0001"),
			// STALE (non-primary) but NOT a loser → stale_owner, not an orphan.
			bf("f_s", "b_s", "/mnt/x/stale.m4b", "BBBB0002"),
			// PROVABLE ORPHAN via the JOURNAL loser set; its FileHash H1 is ALSO
			// carried by a live-primary book_file → SHA-gated removable.
			bfh("f_j", "b_j", "/mnt/x/j.m4b", "CCCC0003", "H1"),
			bfh("f_hp", "b_hp", "/mnt/x/hp.m4b", "AAAA0009", "H1"), // live-primary carrier of H1
			// PROVABLE ORPHAN via MergedIntoBookID; FileHash H2 has NO live-primary
			// carrier → counted as provable orphan but NOT SHA-gated.
			bfh("f_m", "b_m", "/mnt/x/m.m4b", "DDDD0004", "H2"),
		},
		books: map[string]*database.Book{
			"b_h":  {ID: "b_h", IsPrimaryVersion: &primary},
			"b_s":  {ID: "b_s", IsPrimaryVersion: &notPrimary},
			"b_j":  {ID: "b_j", IsPrimaryVersion: &notPrimary, MarkedForDeletion: &del},
			"b_hp": {ID: "b_hp", IsPrimaryVersion: &primary},
			"b_m":  {ID: "b_m", IsPrimaryVersion: &notPrimary, MarkedForDeletion: &del,
				MergedIntoBookID: strptr("b_winner")},
		},
	}

	// .itl contains: the two healthy PIDs, the stale PID, both orphan PIDs, and
	// one PID owned by NO book_file (a user's direct non-AO import).
	itlPIDs := []string{"AAAA0001", "AAAA0009", "BBBB0002", "CCCC0003", "DDDD0004", "EEEE9999"}
	journalLosers := []string{"b_j"} // b_m comes only from MergedIntoBookID

	c, err := computeMergeOrphanCensus(store, itlPIDs, journalLosers)
	if err != nil {
		t.Fatalf("computeMergeOrphanCensus: %v", err)
	}

	if c.TracksInITL != 6 {
		t.Errorf("TracksInITL = %d, want 6", c.TracksInITL)
	}
	// AAAA0001 + AAAA0009 both owned by live primaries.
	if c.Healthy != 2 {
		t.Errorf("Healthy = %d, want 2", c.Healthy)
	}
	// BBBB0002 (non-primary, non-loser) + CCCC0003 + DDDD0004 (losers) = 3 stale.
	if c.StaleOwner != 3 {
		t.Errorf("StaleOwner = %d, want 3", c.StaleOwner)
	}
	// EEEE9999 has no owning book_file.
	if c.NoLiveOwner != 1 {
		t.Errorf("NoLiveOwner = %d, want 1", c.NoLiveOwner)
	}
	if c.Healthy+c.StaleOwner+c.NoLiveOwner != c.TracksInITL {
		t.Errorf("buckets must partition tracks: %d+%d+%d != %d",
			c.Healthy, c.StaleOwner, c.NoLiveOwner, c.TracksInITL)
	}
	// Both loser sources reconciled: CCCC (journal) + DDDD (merged_into).
	if c.ProvableMergeOrphans != 2 {
		t.Errorf("ProvableMergeOrphans = %d, want 2 (journal + merged_into)", c.ProvableMergeOrphans)
	}
	if c.JournalLoserIDs != 1 {
		t.Errorf("JournalLoserIDs = %d, want 1", c.JournalLoserIDs)
	}
	if c.MergedIntoLosers != 1 {
		t.Errorf("MergedIntoLosers = %d, want 1", c.MergedIntoLosers)
	}
	// Only CCCC's FileHash (H1) is carried by a live primary (b_hp).
	if c.SHAGatedRemovable != 1 {
		t.Errorf("SHAGatedRemovable = %d, want 1 (H1 only)", c.SHAGatedRemovable)
	}
	if c.ResidualDuplicatePIDs != 0 {
		t.Errorf("ResidualDuplicatePIDs = %d, want 0", c.ResidualDuplicatePIDs)
	}
}

func strptr(s string) *string { return &s }

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
