// file: internal/organizer/plan_unique_targets_test.go
// version: 1.0.0
// guid: 3f0b8e52-7c1d-4a9e-b6f4-2d85c9e1a703
// last-edited: 2026-09-13

// Tests for the unique-target invariant of planTargetPaths and the matching
// guard in RenameFiles.
//
// Production incident, 2026-09-13: metadata.batch-apply-cached applied
// metadata to three books, then the rename failed in phase 2 with
// "link <tmp> <dest>: file already exists" on targets named
// "<title> - 01 - 01.mp3" and "<title> - 02 - 02.mp3". Each book had two
// book_file rows carrying the SAME TrackNumber (a multi-disc rip whose track
// numbers restart on every disc). The pattern's {track:02d} gave both files
// "<title> - 01"; the collision retry then appended " - 01" -- the same track
// number that had collided -- so both files still planned one target. The
// retry's own collision flag was discarded, RenameFiles published the first
// file, and the second one's link(2) hit the file the batch had just put there.
package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func writeBody(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertUniqueTargets(t *testing.T, entries []FileRenameEntry) {
	t.Helper()
	seen := map[string]string{}
	for _, e := range entries {
		if prev, dup := seen[e.TargetPath]; dup {
			t.Errorf("target %q planned for both %q and %q", e.TargetPath, prev, e.SourcePath)
		}
		seen[e.TargetPath] = e.SourcePath
	}
}

// Two discs, each restarting at track 1: the exact prod shape.
func TestPlanTargetPaths_RepeatedTrackNumbersAcrossDiscsAreUnique(t *testing.T) {
	root, src := t.TempDir(), t.TempDir()
	files := []database.BookFile{
		{ID: "d1t1", FilePath: filepath.Join(src, "CD1", "01.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 1},
		{ID: "d1t2", FilePath: filepath.Join(src, "CD1", "02.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 1},
		{ID: "d2t1", FilePath: filepath.Join(src, "CD2", "01.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 2},
		{ID: "d2t2", FilePath: filepath.Join(src, "CD2", "02.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 2},
	}
	entries, err := planTargetPaths(root, "{author}/{title}", "{title} - {track:02d}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}
	assertUniqueTargets(t, entries)

	// Disc order, then track order: positions 1..4, and never the doubled
	// "NN - NN" suffix.
	want := map[string]string{"d1t1": "Book - 01.mp3", "d1t2": "Book - 02.mp3", "d2t1": "Book - 03.mp3", "d2t2": "Book - 04.mp3"}
	for _, e := range entries {
		if got := filepath.Base(e.TargetPath); got != want[e.SegmentID] {
			t.Errorf("%s -> %q, want %q", e.SegmentID, got, want[e.SegmentID])
		}
	}
}

// Same TrackNumber and no disc information at all (the Stormlight shape: two
// rows both tagged track 2). Still unique.
func TestPlanTargetPaths_RepeatedTrackNumberWithoutDiscIsUnique(t *testing.T) {
	root, src := t.TempDir(), t.TempDir()
	files := []database.BookFile{
		{ID: "a", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "b", FilePath: filepath.Join(src, "b.mp3"), Format: "mp3", TrackNumber: 2},
		{ID: "c", FilePath: filepath.Join(src, "c.mp3"), Format: "mp3", TrackNumber: 2},
	}
	for _, pattern := range []string{"{title} - {track:02d}", "{title} - {author}"} {
		entries, err := planTargetPaths(root, "{author}", pattern, files, dupeTestVars, dupeTestOpts)
		if err != nil {
			t.Fatalf("%s: planTargetPaths: %v", pattern, err)
		}
		if len(entries) != 3 {
			t.Fatalf("%s: got %d entries, want 3", pattern, len(entries))
		}
		assertUniqueTargets(t, entries)
	}
}

// A position-derived number (row with TrackNumber 0) must not land on another
// row's explicit number.
func TestPlanTargetPaths_PositionalNumberDoesNotCollideWithExplicit(t *testing.T) {
	root, src := t.TempDir(), t.TempDir()
	files := []database.BookFile{
		{ID: "x", FilePath: filepath.Join(src, "x.mp3"), Format: "mp3", TrackNumber: 2},
		{ID: "y", FilePath: filepath.Join(src, "y.mp3"), Format: "mp3"},
		{ID: "z", FilePath: filepath.Join(src, "z.mp3"), Format: "mp3"},
	}
	entries, err := planTargetPaths(root, "{author}", "{title} - {track:02d}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	assertUniqueTargets(t, entries)
}

// Anti-churn: a book whose explicit track numbers are already unique keeps
// them exactly -- disc ordering must not renumber a healthy library.
func TestPlanTargetPaths_UniqueTrackNumbersAreKept(t *testing.T) {
	root, src := t.TempDir(), t.TempDir()
	files := []database.BookFile{
		{ID: "t5", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3", TrackNumber: 5, DiscNumber: 1},
		{ID: "t9", FilePath: filepath.Join(src, "b.mp3"), Format: "mp3", TrackNumber: 9, DiscNumber: 1},
		{ID: "t7", FilePath: filepath.Join(src, "c.mp3"), Format: "mp3", TrackNumber: 7, DiscNumber: 2},
	}
	entries, err := planTargetPaths(root, "{author}", "{title} - {track:02d}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	want := map[string]string{"t5": "Book - 05.mp3", "t9": "Book - 09.mp3", "t7": "Book - 07.mp3"}
	for _, e := range entries {
		if got := filepath.Base(e.TargetPath); got != want[e.SegmentID] {
			t.Errorf("%s -> %q, want %q", e.SegmentID, got, want[e.SegmentID])
		}
	}
}

// RenameFiles must refuse a plan with two entries on one target before it
// moves anything. Before the fix the first file was published and the second
// failed on link(2), leaving the book half-renamed.
func TestRenameFiles_DuplicateTargetsFailBeforeAnyMove(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	a, b := filepath.Join(src, "a.mp3"), filepath.Join(src, "b.mp3")
	writeBody(t, a, "aaa")
	writeBody(t, b, "bbbb")
	target := filepath.Join(dst, "Book - 01 - 01.mp3")

	res, err := RenameFiles([]FileRenameEntry{
		{SegmentID: "a", SourcePath: a, TargetPath: target, ExpectedSize: 3},
		{SegmentID: "b", SourcePath: b, TargetPath: target, ExpectedSize: 4},
	}, nil)
	if err == nil {
		t.Fatal("RenameFiles accepted two entries with one target")
	}
	if !errors.Is(err, ErrDuplicateRenameTarget) {
		t.Errorf("error %v does not wrap ErrDuplicateRenameTarget", err)
	}
	if len(res.Succeeded) != 0 {
		t.Errorf("Succeeded = %+v, want none", res.Succeeded)
	}
	for _, p := range []string{a, b} {
		if _, serr := os.Stat(p); serr != nil {
			t.Errorf("source %s moved or lost: %v", p, serr)
		}
	}
	if _, serr := os.Lstat(target); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("target %s exists after a refused batch (stat err %v)", target, serr)
	}
}

// A file already at its target (a re-applied book) is a no-op, including when
// the two spellings differ only by cleaning.
func TestRenameFiles_SourceEqualsTargetIsNoOp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Book - 01.mp3")
	writeBody(t, p, "abc")
	before, _ := os.Stat(p)

	res, err := RenameFiles([]FileRenameEntry{
		{SegmentID: "a", SourcePath: p, TargetPath: filepath.Join(dir, ".", "Book - 01.mp3"), ExpectedSize: 3},
	}, nil)
	if err != nil {
		t.Fatalf("RenameFiles: %v", err)
	}
	if len(res.Succeeded) != 1 {
		t.Fatalf("Succeeded = %+v, want the one entry", res.Succeeded)
	}
	after, err := os.Stat(p)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("file was touched or lost: %v", err)
	}
	if m, _ := filepath.Glob(p + TmpRenameSuffix + "*"); len(m) != 0 {
		t.Errorf("temp left behind: %v", m)
	}
}
