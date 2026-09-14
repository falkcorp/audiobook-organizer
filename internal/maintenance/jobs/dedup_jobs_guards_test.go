// file: internal/maintenance/jobs/dedup_jobs_guards_test.go
// version: 1.0.0
// guid: 8b3d1f62-47a9-4c05-9e1b-6f2a8d4c7e91
// last-edited: 2026-09-13

package jobs

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A subdir with no audio is refused before any write. On main the book's
// rows were deleted first and a 0-file scan then reported success.
func TestVGPlanAuthorDirFix_EmptySubdirRefusesAndKeepsRows(t *testing.T) {
	s := ddRealStore(t)
	dir := t.TempDir()
	book := ddMustBook(t, s, &database.Book{Title: "Alpha", FilePath: dir})
	ddMustFile(t, s, &database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, "01.mp3")})

	_, err := vgPlanAuthorDirFix(s, book.ID, filepath.Join(dir, "empty"))
	if !errors.Is(err, errVGRefused) {
		t.Fatalf("err = %v, want errVGRefused", err)
	}
	if files, _ := s.GetBookFiles(book.ID); len(files) != 1 {
		t.Fatalf("rows = %d, want the one row untouched", len(files))
	}
	if got := ddMustGet(t, s, book.ID); got.FilePath != dir {
		t.Fatalf("book path changed to %q on a refused fix", got.FilePath)
	}
}

// Two rows with the same file name cannot be matched to one disk file, so
// the fix is refused rather than guessed.
func TestVGPlanAuthorDirFix_AmbiguousNamesRefuse(t *testing.T) {
	s := ddRealStore(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "Alpha")
	ddWriteAudio(t, filepath.Join(sub, "01.mp3"))
	book := ddMustBook(t, s, &database.Book{Title: "Alpha", FilePath: dir})
	ddMustFile(t, s, &database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, "x", "01.mp3")})
	ddMustFile(t, s, &database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, "y", "01.mp3")})

	if _, err := vgPlanAuthorDirFix(s, book.ID, sub); !errors.Is(err, errVGRefused) {
		t.Fatalf("err = %v, want errVGRefused", err)
	}
}

// Unlinking a group's primary as an outlier promotes a remaining member first,
// and the outlier becomes primary of its own new group.
func TestVGUnlinkOutliers_KeepsAPrimaryOnBothSides(t *testing.T) {
	yes, no := true, false
	vg := "g1"
	rows := map[string]*database.Book{
		"a": {ID: "a", VersionGroupID: &vg, IsPrimaryVersion: &no},
		"b": {ID: "b", VersionGroupID: &vg, IsPrimaryVersion: &no},
		"o": {ID: "o", VersionGroupID: &vg, IsPrimaryVersion: &yes},
	}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			cp := *rows[id]
			return &cp, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			cp := *b
			rows[id] = &cp
			return b, nil
		},
	}
	group := []database.BookCore{rows["a"].Core(), rows["b"].Core(), rows["o"].Core()}
	if err := vgUnlinkOutliers(store, group, []database.BookCore{rows["o"].Core()}); err != nil {
		t.Fatalf("vgUnlinkOutliers: %v", err)
	}
	if a := rows["a"]; a.IsPrimaryVersion == nil || !*a.IsPrimaryVersion {
		t.Error("old group lost its primary: the lowest-ID remaining member must be promoted")
	}
	o := rows["o"]
	if o.VersionGroupID == nil || *o.VersionGroupID == vg {
		t.Fatal("outlier was not moved to a new group")
	}
	if o.IsPrimaryVersion == nil || !*o.IsPrimaryVersion {
		t.Error("outlier must be primary of its new singleton group")
	}
}

// A soft-delete that absorbs files clears FilePath and demotes the primary.
func TestDDSoftDeleteBook_ClearPathDemotesPrimary(t *testing.T) {
	yes := true
	vg := "g"
	p := &softDeleteProbe{book: &database.Book{ID: "d", FilePath: "/lib/x.m4b", VersionGroupID: &vg, IsPrimaryVersion: &yes}}
	if err := ddSoftDeleteBook(p, "d", true); err != nil {
		t.Fatal(err)
	}
	w := p.lastWrite
	if w.FilePath != "" || w.IsPrimaryVersion == nil || *w.IsPrimaryVersion {
		t.Fatalf("write = path %q primary %v, want path cleared and primary false", w.FilePath, w.IsPrimaryVersion)
	}
}
