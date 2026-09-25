// file: internal/plugins/maintenance/book_shape_report_test.go
// version: 1.1.0
// guid: 7b2d9e14-0c63-4a58-8f71-6d3a92c4e150
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/require"
)

// bookShapeFixture builds the store and the on-disk folder a shape test needs.
// Every shape below is detected from a CONSTRUCTED fixture -- real rows plus
// real files -- rather than from a hand-made report value, so the classifier is
// what is under test.
type bookShapeFixture struct {
	books []database.BookCore
	files []database.BookFileCore
}

func (f *bookShapeFixture) addBook(id, path string, state string, primary bool) {
	st, pr := state, primary
	f.books = append(f.books, database.BookCore{
		ID: id, Title: "t-" + id, FilePath: path,
		LibraryState: &st, IsPrimaryVersion: &pr,
	})
}

func (f *bookShapeFixture) addRows(bookID string, paths ...string) {
	for i, p := range paths {
		f.files = append(f.files, database.BookFileCore{
			ID: bookID + "-f" + string(rune('a'+i)), BookID: bookID, FilePath: p,
		})
	}
}

// store returns a MockStore whose WRITE methods all fail the test. The op's own
// store interface (bookShapeStore) has no write method at all, so this is the
// runtime half of a guarantee the compiler already enforces -- it is here to
// fail loudly if anyone ever widens that interface.
func (f *bookShapeFixture) store(t *testing.T) *database.MockStore {
	t.Helper()
	books := append([]database.BookCore(nil), f.books...)
	files := append([]database.BookFileCore(nil), f.files...)
	return &database.MockStore{
		GetAllBooksCoreFunc:     func(int, int) ([]database.BookCore, error) { return books, nil },
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		UpdateBookFunc: func(string, *database.Book) (*database.Book, error) {
			t.Fatalf("book-shape-report wrote a book: it is report-only")
			return nil, nil
		},
		DeleteBookFunc: func(string) error {
			t.Fatalf("book-shape-report deleted a book: it is report-only")
			return nil
		},
		CreateBookFileFunc: func(*database.BookFile) error {
			t.Fatalf("book-shape-report created a book_file: it is report-only")
			return nil
		},
		UpdateBookFileFunc: func(string, *database.BookFile) error {
			t.Fatalf("book-shape-report wrote a book_file: it is report-only")
			return nil
		},
		DeleteBookFileFunc: func(string) error {
			t.Fatalf("book-shape-report deleted a book_file: it is report-only")
			return nil
		},
		DeleteBookFilesByIDsFunc: func([]string) error {
			t.Fatalf("book-shape-report deleted book_files: it is report-only")
			return nil
		},
	}
}

func (f *bookShapeFixture) run(t *testing.T, params bookShapeReportParams) bookShapeReport {
	t.Helper()
	rep, err := buildBookShapeReport(context.Background(), f.store(t), params, &fakeReporter{})
	require.NoError(t, err)
	return rep
}

// findingsOf returns every finding of one shape.
func findingsOf(r bookShapeReport, shape string) []bookShapeFinding {
	var out []bookShapeFinding
	for _, f := range r.Findings {
		if f.Shape == shape {
			out = append(out, f)
		}
	}
	return out
}

// touchAudio creates n audio files in dir and returns their paths.
func touchAudio(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	paths := make([]string, 0, len(names))
	for _, n := range names {
		p := filepath.Join(dir, n)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		paths = append(paths, p)
	}
	return paths
}

// --- shape 0: oversized ---

func TestBookShape_Oversized(t *testing.T) {
	dir := t.TempDir()
	var f bookShapeFixture
	f.addBook("big", dir, "imported", true)
	names := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		names = append(names, string(rune('a'+i))+".mp3")
	}
	paths := touchAudio(t, dir, names...)
	f.addRows("big", paths...)

	got := f.run(t, bookShapeReportParams{OversizedThreshold: 3})
	over := findingsOf(got, shapeOversized)
	require.Len(t, over, 1)
	require.Equal(t, []string{"big"}, over[0].BookIDs)
	require.Equal(t, 5, over[0].OwnedRows)
	require.Equal(t, 5, over[0].DiskFiles, "owned vs on disk must both be visible")
	require.Equal(t, "dir", over[0].PathKind)
	require.Equal(t, "imported", over[0].LibraryState)
	require.Equal(t, "true", over[0].IsPrimary)
	require.Contains(t, over[0].Recommendation, "do not auto-split")

	// Anti-over-suppression: the same fixture under the default threshold is
	// not oversized, so the detector is not firing on everything.
	require.Empty(t, findingsOf(f.run(t, bookShapeReportParams{}), shapeOversized))
}

// --- shape 1: duplicate book rows at one path, and the overlap words ---

func TestBookShape_DuplicateRowsAtPath_OverlapClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		aRows     []string
		bRows     []string
		wantShape string
		wantWord  string
	}{
		// A strict subset is a CONTAINMENT, but the sets do not overlap FULLY --
		// the shape and the overlap word answer different questions.
		{"subset-is-partial-overlap", []string{"1.mp3"}, []string{"1.mp3", "2.mp3"}, shapeContainment, "overlap partially"},
		{"identical-is-full-overlap", []string{"1.mp3", "2.mp3"}, []string{"1.mp3", "2.mp3"}, shapeContainment, "overlap fully (identical"},
		{"partial-overlap", []string{"1.mp3", "3.mp3"}, []string{"1.mp3", "2.mp3"}, shapePartialOverlap, "overlap partially"},
		{"no-overlap", []string{"1.mp3"}, []string{"2.mp3"}, shapePartition, "do not overlap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			all := map[string]struct{}{}
			for _, n := range append(append([]string{}, tc.aRows...), tc.bRows...) {
				all[n] = struct{}{}
			}
			names := make([]string, 0, len(all))
			for n := range all {
				names = append(names, n)
			}
			touchAudio(t, dir, names...)

			var f bookShapeFixture
			f.addBook("a", dir, "organized", true)
			f.addBook("b", dir, "imported", false)
			for _, n := range tc.aRows {
				f.addRows("a", filepath.Join(dir, n))
			}
			for _, n := range tc.bRows {
				f.addRows("b", filepath.Join(dir, n))
			}

			got := f.run(t, bookShapeReportParams{})
			dup := findingsOf(got, shapeDuplicateRowsAtPath)
			require.Len(t, dup, 1)
			require.Equal(t, []string{"a", "b"}, dup[0].BookIDs)
			require.Contains(t, dup[0].Detail, tc.wantWord)
			require.Len(t, findingsOf(got, tc.wantShape), 1, "expected shape %s, got %v", tc.wantShape, got.ShapeCounts)
		})
	}
}

// --- shape 2: partition vs disjoint-unaccounted (the disk-count gate) ---

func TestBookShape_PartitionRequiresTheDiskCount(t *testing.T) {
	dir := t.TempDir()
	touchAudio(t, dir, "1.mp3", "2.mp3", "3.mp3")

	var f bookShapeFixture
	f.addBook("a", dir, "organized", true)
	f.addBook("b", dir, "organized", false)
	f.addRows("a", filepath.Join(dir, "1.mp3"))
	f.addRows("b", filepath.Join(dir, "2.mp3"))

	// Union 2 != 3 on disk: NOT a partition, and the orphan is reported too.
	got := f.run(t, bookShapeReportParams{})
	require.Len(t, findingsOf(got, shapeDisjointUnaccounted), 1)
	require.Empty(t, findingsOf(got, shapePartition))
	orph := findingsOf(got, shapeOrphanedFiles)
	require.Len(t, orph, 1)
	require.Equal(t, 2, orph[0].OwnedRows)
	require.Equal(t, 3, orph[0].DiskFiles)
	require.Contains(t, orph[0].Detail, "1 audio file(s) on disk are owned by no book row")

	// Own the third file and the union now equals the disk count: a partition,
	// with no orphan left.
	f.addRows("b", filepath.Join(dir, "3.mp3"))
	got = f.run(t, bookShapeReportParams{})
	part := findingsOf(got, shapePartition)
	require.Len(t, part, 1)
	require.Contains(t, part[0].Detail, "NEITHER is complete")
	require.Contains(t, part[0].Recommendation, "MERGE and re-own")
	require.Empty(t, findingsOf(got, shapeOrphanedFiles))
}

// A union that reaches the right SIZE while containing rows for files that are
// no longer on disk is not a partition of the folder: merging on it would be
// merging on missing rows. The library holds ~66,753 such rows.
func TestBookShape_PartitionRejectsMissingRows(t *testing.T) {
	dir := t.TempDir()
	touchAudio(t, dir, "1.mp3", "2.mp3")

	var f bookShapeFixture
	f.addBook("a", dir, "organized", true)
	f.addBook("b", dir, "organized", false)
	f.addRows("a", filepath.Join(dir, "1.mp3"))
	// b's row names a file that does NOT exist: the union is 2 and the disk
	// count is 2, so an integer comparison would call this a partition.
	f.addRows("b", filepath.Join(dir, "gone.mp3"))

	got := f.run(t, bookShapeReportParams{})
	require.Empty(t, findingsOf(got, shapePartition), "a missing row must not satisfy the partition gate")
	require.Len(t, findingsOf(got, shapeDisjointUnaccounted), 1)
	// And the file on disk that nobody owns is still surfaced, even though the
	// folder holds as many rows as it holds files.
	orph := findingsOf(got, shapeOrphanedFiles)
	require.Len(t, orph, 1)
	require.Contains(t, orph[0].Detail, "2.mp3")
}

// A record already absorbed into another is retired: it must not be grouped,
// because a merge recommendation for a merged-away row is work already done.
func TestBookShape_MergedAwayRecordsAreExcluded(t *testing.T) {
	dir := t.TempDir()
	touchAudio(t, dir, "1.mp3")

	var f bookShapeFixture
	f.addBook("live", dir, "organized", true)
	f.addBook("gone", dir, "organized", false)
	into := "live"
	f.books[1].MergedIntoBookID = &into
	f.addRows("live", filepath.Join(dir, "1.mp3"))
	f.addRows("gone", filepath.Join(dir, "1.mp3"))

	got := f.run(t, bookShapeReportParams{})
	require.Equal(t, 1, got.MergedAway)
	require.Equal(t, 0, got.MultiBookPaths)
	require.Empty(t, findingsOf(got, shapeDuplicateRowsAtPath))
	require.Empty(t, findingsOf(got, shapeContainment))
}

// --- shape 3: different content, which must never be recommended for a merge ---

func TestBookShape_DifferentContent_IsNeverAMerge(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	touchAudio(t, dir, "1.mp3")
	touchAudio(t, elsewhere, "shelf.mp3")

	var f bookShapeFixture
	f.addBook("a", dir, "organized", true)
	f.addBook("b", dir, "imported", true)
	f.addRows("a", filepath.Join(dir, "1.mp3"))
	f.addRows("b", filepath.Join(elsewhere, "shelf.mp3"))

	got := f.run(t, bookShapeReportParams{})
	dc := findingsOf(got, shapeDifferentContent)
	require.Len(t, dc, 1)
	require.Contains(t, dc[0].Detail, "OUTSIDE this path")
	require.Contains(t, dc[0].Recommendation, "DO NOT MERGE")
	// The ladder must not have called this disjoint-and-mergeable: the two sets
	// ARE disjoint, and with the disk count at 1 vs a union of 2 the partition
	// gate is the only thing standing between them and a merge if the order
	// were wrong.
	require.Empty(t, findingsOf(got, shapePartition))
	require.Empty(t, findingsOf(got, shapeContainment))
}

// --- shape 4: corrupt paths ---

func TestBookShape_ConcatenatedAbsolutePath(t *testing.T) {
	require.True(t, hasConcatenatedAbsolutePath("/data/books/A/The Title/data/books/B/The Title"))
	require.False(t, hasConcatenatedAbsolutePath("/data/books/A/The Title"))
	require.False(t, hasConcatenatedAbsolutePath("relative/data/data/x"))
	// The trailing-segment guard: a repeat as the LAST segment is not a second
	// absolute path.
	require.False(t, hasConcatenatedAbsolutePath("/data/books/data"))

	dir := "/data/books/A/The Title"
	var f bookShapeFixture
	f.addBook("bad", dir, "organized", true)
	f.addRows("bad", dir+"/data/books/B/The Title/1.mp3")

	got := f.run(t, bookShapeReportParams{SkipDiskStat: true})
	cp := findingsOf(got, shapeConcatenatedPath)
	require.Len(t, cp, 1)
	require.Equal(t, []string{"bad"}, cp[0].BookIDs)
	require.Contains(t, cp[0].Recommendation, "CORRUPT ROW")
	require.Equal(t, "not-statted", cp[0].PathKind)
	require.Equal(t, -1, cp[0].DiskFiles)
}

func TestBookShape_CollisionSuffixExplosion(t *testing.T) {
	base := "/data/books/Author/The Title"
	var f bookShapeFixture
	f.addBook("boom", base, "organized", true)
	f.addRows("boom",
		base+" - 2/117.mp3",
		base+" - 3/117.mp3",
		base+" - 4/117.mp3",
	)

	got := f.run(t, bookShapeReportParams{SkipDiskStat: true})
	cs := findingsOf(got, shapeCollisionSuffixExplosion)
	require.Len(t, cs, 1)
	require.Contains(t, cs[0].Detail, `3 sibling`)
	require.Contains(t, cs[0].Detail, `117.mp3`)
	require.Contains(t, cs[0].Recommendation, "COLLAPSE")

	// Two suffixed dirs is under the default minimum, and two DIFFERENT
	// basenames is a different folder layout, not this defect.
	var few bookShapeFixture
	few.addBook("boom", base, "organized", true)
	few.addRows("boom", base+" - 2/117.mp3", base+" - 3/117.mp3")
	require.Empty(t, findingsOf(few.run(t, bookShapeReportParams{SkipDiskStat: true}), shapeCollisionSuffixExplosion))

	var varied bookShapeFixture
	varied.addBook("boom", base, "organized", true)
	varied.addRows("boom", base+" - 2/1.mp3", base+" - 3/2.mp3", base+" - 4/3.mp3")
	require.Empty(t, findingsOf(varied.run(t, bookShapeReportParams{SkipDiskStat: true}), shapeCollisionSuffixExplosion))
}

// CHAPTER-SUBFOLDER-NN-ROWS: the same one-basename signature inside a book
// folder named after the prefix is a real chapter-folder layout. It must not
// be reported as a retry loop (whose advice is to collapse the directories)
// nor marked corrupt.
func TestBookShape_ChapterFolderLayoutIsNotARetryLoop(t *testing.T) {
	book := "/data/books/Author/Steamforged Sorcery"
	var f bookShapeFixture
	f.addBook("layout", book, "organized", true)
	f.addRows("layout",
		book+"/Steamforged Sorcery - 01/32.m4b",
		book+"/Steamforged Sorcery - 02/32.m4b",
		book+"/Steamforged Sorcery - 03/32.m4b",
	)

	got := f.run(t, bookShapeReportParams{SkipDiskStat: true})
	require.Empty(t, findingsOf(got, shapeCollisionSuffixExplosion))
	cl := findingsOf(got, shapeChapterFolderLayout)
	require.Len(t, cl, 1)
	require.Equal(t, []string{"layout"}, cl[0].BookIDs)
	require.Contains(t, cl[0].Detail, `32.m4b`)
	require.Contains(t, cl[0].Recommendation, "KEEP the rows")
	require.NotContains(t, cl[0].Recommendation, "COLLAPSE")
}

// --- the empty-member branches, which must never reach the merge shapes ---

func TestBookShape_EmptyMembers(t *testing.T) {
	dir := t.TempDir()
	touchAudio(t, dir, "1.mp3")

	var one bookShapeFixture
	one.addBook("a", dir, "organized", true)
	one.addBook("b", dir, "imported", false)
	one.addRows("a", filepath.Join(dir, "1.mp3"))
	got := one.run(t, bookShapeReportParams{})
	require.Len(t, findingsOf(got, shapeOneSideEmpty), 1)
	require.Empty(t, findingsOf(got, shapePartition), "∅ and B are disjoint; a merge must never be recommended on that")

	var none bookShapeFixture
	none.addBook("a", dir, "organized", true)
	none.addBook("b", dir, "imported", false)
	got = none.run(t, bookShapeReportParams{})
	require.Len(t, findingsOf(got, shapeAllMembersEmpty), 1)
}

// --- a book that is BOTH oversized and a duplicate-group member ---
//
// The eight Gene Wolfe twins are each oversized and all eight are one duplicate
// group. Reporting only one of those facts would hide half the problem.
func TestBookShape_OversizedAndDuplicateAreBothReported(t *testing.T) {
	dir := t.TempDir()
	paths := touchAudio(t, dir, "1.mp3", "2.mp3", "3.mp3")

	var f bookShapeFixture
	f.addBook("twin1", dir, "imported", true)
	f.addBook("twin2", dir, "imported", true)
	f.addRows("twin1", paths...)
	f.addRows("twin2", paths...)

	got := f.run(t, bookShapeReportParams{OversizedThreshold: 2})
	require.Len(t, findingsOf(got, shapeOversized), 2, "both twins are oversized")
	require.Len(t, findingsOf(got, shapeDuplicateRowsAtPath), 1)
	// Equal sets: each is a subset of the other, which is the containment shape.
	require.Len(t, findingsOf(got, shapeContainment), 1)
}

// --- a folder that cannot be read is reported, not fatal ---

func TestBookShape_UnreadableFolderIsReportedNotFatal(t *testing.T) {
	root := t.TempDir()
	// A regular file with a path component after it: ReadDir/Stat fails with
	// ENOTDIR for every user, including root, so this is portable in CI.
	require.NoError(t, os.WriteFile(filepath.Join(root, "notadir"), []byte("x"), 0o644))
	bad := filepath.Join(root, "notadir", "inner")

	good := t.TempDir()
	touchAudio(t, good, "1.mp3", "2.mp3")

	var f bookShapeFixture
	f.addBook("bad", bad, "imported", true)
	f.addBook("good", good, "organized", true)
	f.addRows("bad", filepath.Join(bad, "1.mp3"), filepath.Join(bad, "2.mp3"))
	f.addRows("good", filepath.Join(good, "1.mp3"))

	got := f.run(t, bookShapeReportParams{OversizedThreshold: 1})
	require.Equal(t, 1, got.UnreadableDirs)
	// The unreadable book is still classified, with an unknown disk count...
	over := findingsOf(got, shapeOversized)
	require.Len(t, over, 1)
	require.Equal(t, []string{"bad"}, over[0].BookIDs)
	require.Equal(t, "unreadable", over[0].PathKind)
	require.Equal(t, -1, over[0].DiskFiles)
	// ...and the readable folder in the same run is unaffected.
	orph := findingsOf(got, shapeOrphanedFiles)
	require.Len(t, orph, 1)
	require.Equal(t, good, orph[0].Dir)
}

// --- counts, the report file, and the promise that nothing is written ---

func TestBookShape_ReportFileMatchesTheCounts(t *testing.T) {
	dir := t.TempDir()
	paths := touchAudio(t, dir, "1.mp3", "2.mp3", "3.mp3")

	var f bookShapeFixture
	f.addBook("a", dir, "organized", true)
	f.addBook("b", dir, "imported", false)
	f.addRows("a", paths[0])
	f.addRows("b", paths[1])

	root := t.TempDir()
	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: f.store(t)}, root: root}}
	require.NoError(t, p.runBookShapeReport(context.Background(), nil, &fakeReporter{}))

	raw, err := os.ReadFile(filepath.Join(root, ".reports", "book-shape-report-unknown-op.tsv"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Equal(t, "shape\tdir\tbook_ids\tpath_kind\tlibrary_state\tis_primary_version\towned_rows\tdisk_files\tdetail\trecommendation", lines[0])

	got := f.run(t, bookShapeReportParams{})
	require.Len(t, lines[1:], len(got.Findings), "the report file must hold one row per finding")
	total := 0
	for _, n := range got.ShapeCounts {
		total += n
	}
	require.Equal(t, len(got.Findings), total, "the shape counts must add up to the findings")
	require.Equal(t, 2, got.TotalBooks)
	require.Equal(t, 2, got.TotalFileRows)
	require.Equal(t, 1, got.MultiBookPaths)
	for _, line := range lines[1:] {
		require.Equal(t, 10, len(strings.Split(line, "\t")), "every report row has 10 columns: %q", line)
	}
}

// The op declares read capability only, and offers no apply parameter. This is
// the assertion most likely to rot if someone later bolts an apply mode on.
func TestBookShape_IsDeclaredReadOnly(t *testing.T) {
	def := (&Plugin{deps: fakeDeps{}}).bookShapeReportDef()
	require.Equal(t, []sdk.Capability{sdk.CapLibraryRead}, def.Capabilities)
	require.Contains(t, def.Description, "REPORT-ONLY")

	// There is no apply switch: passing one changes nothing and writes nothing.
	dir := t.TempDir()
	touchAudio(t, dir, "1.mp3")
	var f bookShapeFixture
	f.addBook("a", dir, "organized", true)
	f.addRows("a", filepath.Join(dir, "1.mp3"))

	root := t.TempDir()
	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: f.store(t)}, root: root}}
	require.NoError(t, p.runBookShapeReport(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}))
	// The store's write funcs above fail the test if any of them were reached.
}

// The sweep runs concurrently, and `go test -race` only sees a Label or
// callback race if a test actually fans out. This one uses more groups than
// workers with an explicit concurrency.
func TestBookShape_ConcurrentSweepIsRaceFree(t *testing.T) {
	var f bookShapeFixture
	roots := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		dir := t.TempDir()
		roots = append(roots, dir)
		touchAudio(t, dir, "1.mp3", "2.mp3")
		id := "b" + string(rune('A'+i%26)) + string(rune('0'+i/26))
		f.addBook(id, dir, "organized", true)
		f.addRows(id, filepath.Join(dir, "1.mp3"))
	}

	got := f.run(t, bookShapeReportParams{Concurrency: 8})
	require.Equal(t, 32, got.Groups)
	require.Len(t, findingsOf(got, shapeOrphanedFiles), 32)
	// Deterministic ordering regardless of which worker finished first.
	for i := 1; i < len(got.Findings); i++ {
		require.LessOrEqual(t, got.Findings[i-1].Dir, got.Findings[i].Dir)
	}
	_ = roots
}
