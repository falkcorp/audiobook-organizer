// file: internal/organizer/plan_unique_targets_test.go
// version: 1.2.0
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
//
// The healthy-book tests (Kept*) pin that the fix renames nothing the old
// planner named distinctly: the renumbering decision is made on planned
// targets, not on raw track numbers.
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

// planNames plans with {title} - {track:02d} and returns SegmentID -> target
// base name.
func planNames(t *testing.T, files []database.BookFile) map[string]string {
	t.Helper()
	entries, err := planTargetPaths(t.TempDir(), "{author}/{title}", "{title} - {track:02d}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	assertUniqueTargets(t, entries)
	got := make(map[string]string, len(entries))
	for _, e := range entries {
		got[e.SegmentID] = filepath.Base(e.TargetPath)
	}
	return got
}

func assertNames(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("planned %d files %v, want %d %v", len(got), got, len(want), want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s -> %q, want %q", id, got[id], w)
		}
	}
}

func assertRefused(t *testing.T, files []database.BookFile, pattern string) {
	t.Helper()
	entries, err := planTargetPaths(t.TempDir(), "{author}", pattern, files, dupeTestVars, dupeTestOpts)
	if !errors.Is(err, ErrDuplicateRenameTarget) {
		t.Fatalf("pattern %q: err = %v, entries = %+v; want ErrDuplicateRenameTarget", pattern, err, entries)
	}
}

// Two discs in two folders, each restarting at track 1: the exact prod shape.
func TestPlanTargetPaths_RepeatedTrackNumbersAcrossDiscsAreUnique(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "d1t1", FilePath: filepath.Join(src, "CD1", "01.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 1},
		{ID: "d1t2", FilePath: filepath.Join(src, "CD1", "02.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 1},
		{ID: "d2t1", FilePath: filepath.Join(src, "CD2", "01.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 2},
		{ID: "d2t2", FilePath: filepath.Join(src, "CD2", "02.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 2},
	})
	assertNames(t, got, map[string]string{"d1t1": "Book - 01.mp3", "d1t2": "Book - 02.mp3", "d2t1": "Book - 03.mp3", "d2t2": "Book - 04.mp3"})
}

// Review shape 2: DiscNumber is 0 on almost every prod row (only the importer
// writes it). Sorting by disc then track interleaved the discs; the folder is
// what separates them. CD10 sorts after CD2 (natural order).
func TestPlanTargetPaths_DiscZeroFoldersAreNotInterleaved(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "c10t1", FilePath: filepath.Join(src, "CD10", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "c2t2", FilePath: filepath.Join(src, "CD2", "02.mp3"), Format: "mp3", TrackNumber: 2},
		{ID: "c1t1", FilePath: filepath.Join(src, "CD1", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "c2t1", FilePath: filepath.Join(src, "CD2", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "c1t2", FilePath: filepath.Join(src, "CD1", "02.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{
		"c1t1": "Book - 01.mp3", "c1t2": "Book - 02.mp3",
		"c2t1": "Book - 03.mp3", "c2t2": "Book - 04.mp3",
		"c10t1": "Book - 05.mp3",
	})
}

// Review shape 3: a folder of disc-0 rows must not sort ahead of a disc-1
// folder just because 0 < 1. Folder order decides.
func TestPlanTargetPaths_MixedDiscZeroAcrossFoldersFollowsFolders(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "a1", FilePath: filepath.Join(src, "CD1", "01.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 1},
		{ID: "a2", FilePath: filepath.Join(src, "CD1", "02.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 1},
		{ID: "b1", FilePath: filepath.Join(src, "CD2", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "b2", FilePath: filepath.Join(src, "CD2", "02.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{"a1": "Book - 01.mp3", "a2": "Book - 02.mp3", "b1": "Book - 03.mp3", "b2": "Book - 04.mp3"})
}

// A flat folder whose rows all carry a disc number: disc separates them.
func TestPlanTargetPaths_FlatFolderOrderedByDiscWhenEveryRowHasOne(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "d2t1", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 2},
		{ID: "d1t1", FilePath: filepath.Join(src, "b.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 1},
		{ID: "d2t2", FilePath: filepath.Join(src, "c.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 2},
		{ID: "d1t2", FilePath: filepath.Join(src, "d.mp3"), Format: "mp3", TrackNumber: 2, DiscNumber: 1},
	})
	assertNames(t, got, map[string]string{"d1t1": "Book - 01.mp3", "d1t2": "Book - 02.mp3", "d2t1": "Book - 03.mp3", "d2t2": "Book - 04.mp3"})
}

// Tags that repeat a track number inside ONE folder (every file tagged track
// 1 is common) cannot order it, so the folder falls back to natural file-name
// order. Before the fallback these books were refused, and with write-back
// the pre-apply check then refused their metadata apply too.
func TestPlanTargetPaths_RepeatedTracksInOneFolderFallBackToFileNames(t *testing.T) {
	src := t.TempDir()

	// Every file tagged track 1: natural order, so Book 2 before Book 10.
	got := planNames(t, []database.BookFile{
		{ID: "b10", FilePath: filepath.Join(src, "Book 10.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "b2", FilePath: filepath.Join(src, "Book 2.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "b1", FilePath: filepath.Join(src, "Book 1.mp3"), Format: "mp3", TrackNumber: 1},
	})
	assertNames(t, got, map[string]string{"b1": "Book - 01.mp3", "b2": "Book - 02.mp3", "b10": "Book - 03.mp3"})

	// A partial repeat puts the whole folder in name order, not only the
	// clashing rows: tracks 1, 2, 2 as z, a, b gives a, b, z.
	got = planNames(t, []database.BookFile{
		{ID: "z", FilePath: filepath.Join(src, "p", "z.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "a", FilePath: filepath.Join(src, "p", "a.mp3"), Format: "mp3", TrackNumber: 2},
		{ID: "b", FilePath: filepath.Join(src, "p", "b.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{"a": "Book - 01.mp3", "b": "Book - 02.mp3", "z": "Book - 03.mp3"})

	// One folder mixing disc 0 and disc 2: disc is not trusted there, the
	// tracks repeat, so the names decide.
	got = planNames(t, []database.BookFile{
		{ID: "b", FilePath: filepath.Join(src, "m", "b.mp3"), Format: "mp3", TrackNumber: 1, DiscNumber: 2},
		{ID: "a", FilePath: filepath.Join(src, "m", "a.mp3"), Format: "mp3", TrackNumber: 1},
	})
	assertNames(t, got, map[string]string{"a": "Book - 01.mp3", "b": "Book - 02.mp3"})

	// The same book under a trackless pattern still plans distinct targets.
	entries, err := planTargetPaths(t.TempDir(), "{author}", "{title} - {author}", []database.BookFile{
		{ID: "b10", FilePath: filepath.Join(src, "Book 10.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "b2", FilePath: filepath.Join(src, "Book 2.mp3"), Format: "mp3", TrackNumber: 1},
	}, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("trackless pattern: %v", err)
	}
	assertUniqueTargets(t, entries)
}

// Names that do not really differ -- equal stems that differ only by
// extension, or by case and leading zeros -- cannot order a folder whose
// tracks repeat. Those books are refused, not guessed.
func TestPlanTargetPaths_IndistinguishableFileNamesAreRefused(t *testing.T) {
	src := t.TempDir()
	sameStem := []database.BookFile{
		{ID: "mp3", FilePath: filepath.Join(src, "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "m4a", FilePath: filepath.Join(src, "01.m4a"), Format: "m4a", TrackNumber: 1},
		{ID: "a", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3", TrackNumber: 1},
	}
	caseAndZeros := []database.BookFile{
		{ID: "p1", FilePath: filepath.Join(src, "Part 1.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "p01", FilePath: filepath.Join(src, "part 01.mp3"), Format: "mp3", TrackNumber: 1},
	}
	for _, pattern := range []string{"{title} - {track:02d}", "{title} - {author}"} {
		assertRefused(t, sameStem, pattern)
		assertRefused(t, caseAndZeros, pattern)
	}
}

// Anti-churn for the fallback: tags that DO order a folder win over names,
// so a healthy book is never reordered by file name.
func TestPlanTargetPaths_DistinctTracksAreNotReorderedByName(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "b", FilePath: filepath.Join(src, "b.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "a", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{"b": "Book - 01.mp3", "a": "Book - 02.mp3"})
}

// Shape (e): a position-derived number (row with TrackNumber 0) that lands on
// another row's explicit number is renumbered, and the explicit row sorts
// FIRST even though its name sorts last.
func TestPlanTargetPaths_ExplicitTrackSortsBeforePositional(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "a", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3"},
		{ID: "b", FilePath: filepath.Join(src, "b.mp3"), Format: "mp3"},
		{ID: "z", FilePath: filepath.Join(src, "z.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{"z": "Book - 01.mp3", "a": "Book - 02.mp3", "b": "Book - 03.mp3"})
}

// Shape (d): a Missing row keeps its OWN track number through a renumbering,
// so a repointed file comes back to it. old/x.mp3 is track 3; the two-CD
// clash numbers the present files 1, 2, then 4, 5.
func TestPlanTargetPaths_RenumberReservesMissingRowTrackNumber(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "gone", FilePath: filepath.Join(src, "old", "x.mp3"), Format: "mp3", TrackNumber: 3, Missing: true},
		{ID: "c1t1", FilePath: filepath.Join(src, "CD1", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "c1t2", FilePath: filepath.Join(src, "CD1", "02.mp3"), Format: "mp3", TrackNumber: 2},
		{ID: "c2t1", FilePath: filepath.Join(src, "CD2", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "c2t2", FilePath: filepath.Join(src, "CD2", "02.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{"c1t1": "Book - 01.mp3", "c1t2": "Book - 02.mp3", "c2t1": "Book - 04.mp3", "c2t2": "Book - 05.mp3"})
}

// When a renumbered book has a Missing row, the row keeps its slot so the
// survivors do not shift when it comes back.
func TestPlanTargetPaths_RenumberKeepsMissingRowSlot(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "c1t1", FilePath: filepath.Join(src, "CD1", "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "c1t2", FilePath: filepath.Join(src, "CD1", "02.mp3"), Format: "mp3", TrackNumber: 2, Missing: true},
		{ID: "c2t1", FilePath: filepath.Join(src, "CD2", "01.mp3"), Format: "mp3", TrackNumber: 1},
	})
	assertNames(t, got, map[string]string{"c1t1": "Book - 01.mp3", "c2t1": "Book - 03.mp3"})
}

// Review shape 1, healthy: a Missing row sharing track 1 with a present row
// plans no target, so the book keeps its old names -- no rename, no gap.
func TestPlanTargetPaths_KeptWhenOnlyAMissingRowRepeatsATrack(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "gone", FilePath: filepath.Join(src, "old", "01.mp3"), Format: "mp3", TrackNumber: 1, Missing: true},
		{ID: "t1", FilePath: filepath.Join(src, "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "t2", FilePath: filepath.Join(src, "02.mp3"), Format: "mp3", TrackNumber: 2},
		{ID: "t3", FilePath: filepath.Join(src, "03.mp3"), Format: "mp3", TrackNumber: 3},
	})
	assertNames(t, got, map[string]string{"t1": "Book - 01.mp3", "t2": "Book - 02.mp3", "t3": "Book - 03.mp3"})
}

// Review shape 4, healthy: two files on track 1 whose targets differ by
// extension never collided, so the book keeps its old names.
func TestPlanTargetPaths_KeptWhenRepeatedTrackDiffersByExtension(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "mp3", FilePath: filepath.Join(src, "01.mp3"), Format: "mp3", TrackNumber: 1},
		{ID: "m4a", FilePath: filepath.Join(src, "01.m4a"), Format: "m4a", TrackNumber: 1},
		{ID: "t2", FilePath: filepath.Join(src, "02.mp3"), Format: "mp3", TrackNumber: 2},
	})
	assertNames(t, got, map[string]string{"mp3": "Book - 01.mp3", "m4a": "Book - 01.m4a", "t2": "Book - 02.mp3"})
}

// Anti-churn: a book whose explicit track numbers are already unique keeps
// them exactly -- disc ordering must not renumber a healthy library.
func TestPlanTargetPaths_UniqueTrackNumbersAreKept(t *testing.T) {
	src := t.TempDir()
	got := planNames(t, []database.BookFile{
		{ID: "t5", FilePath: filepath.Join(src, "a.mp3"), Format: "mp3", TrackNumber: 5, DiscNumber: 1},
		{ID: "t9", FilePath: filepath.Join(src, "b.mp3"), Format: "mp3", TrackNumber: 9, DiscNumber: 1},
		{ID: "t7", FilePath: filepath.Join(src, "c.mp3"), Format: "mp3", TrackNumber: 7, DiscNumber: 2},
	})
	assertNames(t, got, map[string]string{"t5": "Book - 05.mp3", "t9": "Book - 09.mp3", "t7": "Book - 07.mp3"})
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
