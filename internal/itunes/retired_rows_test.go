// file: internal/itunes/retired_rows_test.go
// version: 1.2.0
// guid: 0b6e92d4-5a17-4c3f-8d20-e4f1a7c93b58
// last-edited: 2026-10-23

package itunes

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredAwareMock is a PIDIntegrityStore + PIDCoverageStore that behaves like
// the production store: GetAllBookFilesCore returns the LIVE rows only, and the
// retired rows are reachable only through the RetiredBookFileReader capability.
// The compile-time assertion below keeps it a full implementation: a method
// added to the interface otherwise makes the capability lookup fail quietly and
// the tests fall back to the live-only path.
type retiredAwareMock struct {
	pidCensusMock
	retired []database.BookFileCore
	books2  []database.Book
}

func (m *retiredAwareMock) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return append(append([]database.BookFileCore{}, m.files...), m.retired...), nil
}

func (m *retiredAwareMock) GetBookFilesIncludingRetired(string) ([]database.BookFile, error) {
	return nil, nil
}

func (m *retiredAwareMock) GetRetiredBookFiles(string) ([]database.BookFile, error) {
	return nil, nil
}

func (m *retiredAwareMock) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, nil
}

func (m *retiredAwareMock) GetBookFileByPIDIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}

func (m *retiredAwareMock) GetBookFileByPathIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}

func (m *retiredAwareMock) ListBooksByITunesPID(int, int) ([]database.Book, error) {
	return m.books2, nil
}

var _ database.RetiredBookFileReader = (*retiredAwareMock)(nil)

func retiredBF(id, bookID, path, pid string) database.BookFileCore {
	f := bf(id, bookID, path, pid)
	f.Retired = true
	return f
}

// A retired row that keeps the PID a live row on the holder also carries is not
// a duplicate: the integrity census must not count it as a second owner (a repair
// plan built from that group would clear the retired row's PID), and must report
// it in its own field.
func TestComputePIDIntegrityRetiredRowIsNotADuplicate(t *testing.T) {
	primary := true
	store := &retiredAwareMock{
		pidCensusMock: pidCensusMock{
			files: []database.BookFileCore{bf("f_live", "b_holder", "/mnt/x/h.m4b", "AAAA0001")},
			books: map[string]*database.Book{"b_holder": {ID: "b_holder", IsPrimaryVersion: &primary}},
		},
		retired: []database.BookFileCore{
			retiredBF("f_ret", "b_holder", "/mnt/x/shell.m4b", "AAAA0001"),
			retiredBF("f_ret2", "b_holder", "/mnt/x/shell2.m4b", ""),
		},
	}
	r, err := ComputePIDIntegrity(store, "")
	if err != nil {
		t.Fatalf("ComputePIDIntegrity: %v", err)
	}
	if r.DuplicatePIDs != 0 || r.FilesToClear != 0 {
		t.Errorf("retired row made a duplicate: dup=%d clear=%d", r.DuplicatePIDs, r.FilesToClear)
	}
	if r.FilesWithPID != 1 || r.DistinctPIDs != 1 {
		t.Errorf("live totals must exclude retired rows: files=%d distinct=%d", r.FilesWithPID, r.DistinctPIDs)
	}
	if r.RetiredFilesWithPID != 1 {
		t.Errorf("RetiredFilesWithPID = %d, want 1 (the PID-less retired row is not counted)", r.RetiredFilesWithPID)
	}
}

// A track whose only holder is a retired row is accounted for, not an orphan; a
// track held by a live row and a retired row is Healthy with no residual duplicate.
func TestComputeMergeOrphanCensusRetiredOwner(t *testing.T) {
	primary := true
	store := &retiredAwareMock{
		pidCensusMock: pidCensusMock{
			files: []database.BookFileCore{bf("f_live", "b_holder", "/mnt/x/h.m4b", "AAAA0001")},
			books: map[string]*database.Book{"b_holder": {ID: "b_holder", IsPrimaryVersion: &primary}},
		},
		retired: []database.BookFileCore{
			retiredBF("f_r1", "b_holder", "/mnt/x/s1.m4b", "AAAA0001"), // same PID as the live row
			retiredBF("f_r2", "b_holder", "/mnt/x/s2.m4b", "BBBB0002"), // only holder of BBBB0002
		},
	}
	c, err := computeMergeOrphanCensus(store, []string{"AAAA0001", "BBBB0002", "CCCC0003"}, nil)
	if err != nil {
		t.Fatalf("computeMergeOrphanCensus: %v", err)
	}
	if c.Healthy != 1 || c.RetiredVersionOwner != 1 || c.NoLiveOwner != 1 || c.StaleOwner != 0 {
		t.Errorf("buckets healthy=%d retired=%d noLive=%d stale=%d, want 1/1/1/0",
			c.Healthy, c.RetiredVersionOwner, c.NoLiveOwner, c.StaleOwner)
	}
	if c.Healthy+c.StaleOwner+c.NoLiveOwner+c.RetiredVersionOwner != c.TracksInITL {
		t.Errorf("buckets no longer partition TracksInITL=%d", c.TracksInITL)
	}
	if c.ResidualDuplicatePIDs != 0 {
		t.Errorf("a retired row counted as a residual duplicate: %d", c.ResidualDuplicatePIDs)
	}
	var sawRetired bool
	for _, s := range c.Samples {
		if s.PID == "BBBB0002" && s.Bucket == BucketRetiredVersion {
			sawRetired = true
		}
	}
	if !sawRetired {
		t.Errorf("no %q sample for the retired-only PID: %+v", BucketRetiredVersion, c.Samples)
	}
}

func TestComputePIDCoverageRetiredOnlyIsResolvedButNotFileLevel(t *testing.T) {
	store := &retiredAwareMock{
		pidCensusMock: pidCensusMock{files: []database.BookFileCore{bf("f_live", "b1", "/m/a.m4b", "AAAA0001")}},
		retired: []database.BookFileCore{
			retiredBF("f_r", "b1", "/m/s.m4b", "BBBB0002"),
			retiredBF("f_r_dup", "b1", "/m/s2.m4b", "AAAA0001"), // live row also holds it: not retired-only
		},
	}
	tracks := []coverageTrack{
		{TrackID: 1, PID: "AAAA0001"}, {TrackID: 2, PID: "BBBB0002"}, {TrackID: 3, PID: "CCCC0003"},
	}
	r, err := computePIDCoverage(store, tracks, nil)
	if err != nil {
		t.Fatalf("computePIDCoverage: %v", err)
	}
	b := r.Library
	if b.ResolvedFileLevel != 1 || b.ResolvedRetiredOnly != 1 || b.ResolvedEither != 2 || b.Unresolved != 1 {
		t.Errorf("file=%d retiredOnly=%d either=%d unresolved=%d, want 1/1/2/1",
			b.ResolvedFileLevel, b.ResolvedRetiredOnly, b.ResolvedEither, b.Unresolved)
	}
	if r.DBBookFilePIDs != 1 {
		t.Errorf("DBBookFilePIDs = %d, want the live-only 1", r.DBBookFilePIDs)
	}
	if len(r.UnresolvedSamples) != 1 || r.UnresolvedSamples[0].PID != "CCCC0003" {
		t.Errorf("unresolved samples = %+v, want only CCCC0003", r.UnresolvedSamples)
	}
}

func TestComputeCrossTypeCollisionsRetiredOwner(t *testing.T) {
	primary := true
	store := &retiredAwareMock{
		pidCensusMock: pidCensusMock{
			files: []database.BookFileCore{bf("f_live", "b_live", "/m/live.mp3", "BBBBBBBB00000002")},
			books: map[string]*database.Book{
				"b_live": {ID: "b_live", Title: "Live", IsPrimaryVersion: &primary},
				"b_hold": {ID: "b_hold", Title: "Holder", IsPrimaryVersion: &primary},
			},
		},
		retired: []database.BookFileCore{
			retiredBF("f_r_same", "b_hold", "/m/r1.mp3", "BBBBBBBB00000002"), // live row too: owner stays the live row
			retiredBF("f_r_only", "b_hold", "/m/r2.mp3", "DDDDDDDD00000004"), // only claimant
		},
	}
	tracks := []ITLTrack{
		{PersistentID: pidHexToBytes("BBBBBBBB00000002"), Genre: "Rock", Name: "Song"},
		{PersistentID: pidHexToBytes("DDDDDDDD00000004"), Genre: "Podcast", Name: "Episode"},
		{PersistentID: pidHexToBytes("EEEEEEEE00000005"), Genre: "Podcast", Name: "Unclaimed"},
	}
	r, err := computeCrossTypeCollisions(store, tracks)
	if err != nil {
		t.Fatalf("computeCrossTypeCollisions: %v", err)
	}
	if r.NonABOwned != 2 || r.NonABUnowned != 1 || r.CrossTypeCollisions != 2 {
		t.Errorf("ownedByAny=%d unowned=%d collisions=%d, want 2/1/2", r.NonABOwned, r.NonABUnowned, r.CrossTypeCollisions)
	}
	if r.CollisionsRetiredOnlyOwner != 1 {
		t.Errorf("CollisionsRetiredOnlyOwner = %d, want 1", r.CollisionsRetiredOnlyOwner)
	}
	for _, s := range r.Samples {
		if s.PID == "BBBBBBBB00000002" && s.OwnerFileID != "f_live" {
			t.Errorf("sample owner = %q, want the live row f_live", s.OwnerFileID)
		}
	}
}
