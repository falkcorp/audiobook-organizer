// file: internal/scanner/chapter_grouping_test.go
// version: 1.3.0
// guid: 950c3ace-c8d3-4885-9a1d-d90682d41415
// last-edited: 2026-10-03

package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

func TestChapterGroupKey(t *testing.T) {
	cases := []struct {
		stem     string
		wantKey  string
		wantKind chapterKeyKind
	}{
		// Leading numbers (the only shape recognised before 2026-09-28).
		{"01 - My Book", "my book", chapterKeyLeading},
		{"02. My Book", "my book", chapterKeyLeading},
		{"98", "", chapterKeyLeading},
		// Trailing numbers.
		{"My Title 01", "my title", chapterKeyTrailingNumber},
		{"My Title - 12", "my title", chapterKeyTrailingNumber},
		{"My_Title_03", "my_title", chapterKeyTrailingNumber},
		// "Book N" right before the number is a series position, even when the
		// word is part of the title: a trailing number there is never stripped.
		{"My Book 01", "", chapterKeyNone},
		// Chapter / Part / Disc / CD / Track markers, either end.
		{"My Book - Chapter 12", "my book", chapterKeyMarker},
		{"My Book Part 3", "my book", chapterKeyMarker},
		{"My Book - Pt. 4", "my book", chapterKeyMarker},
		{"My Book Disc 2", "my book", chapterKeyMarker},
		{"My Book (CD 1)", "my book", chapterKeyMarker},
		{"My Book_Disc2", "my book", chapterKeyMarker},
		{"My Book Part 3 of 12", "my book", chapterKeyMarker},
		{"Chapter 12", "", chapterKeyMarker},
		{"Chapter 01 - My Book", "my book", chapterKeyMarker},
		{"Disc 2 - My Book", "my book", chapterKeyMarker},
		// N of M keeps the total in the key.
		{"My Book 3 of 12", "my book|of 12", chapterKeyOfTotal},
		{"My Book (03 of 12)", "my book|of 12", chapterKeyOfTotal},
		// Both ends at once.
		{"01 Genesis 001", "genesis", chapterKeyLeading},
		// "Discworld" is NOT a disc marker: a marker must be followed by a
		// number. Without a number anywhere it is not chapter-numbered at all.
		{"Discworld", "", chapterKeyNone},
		{"Terry Pratchett - Discworld", "", chapterKeyNone},
		{"Discworld - Mort", "", chapterKeyNone},
		{"The Discworld Companion", "", chapterKeyNone},
		// With a trailing number it is the generic trailing-number shape, not
		// a marker; the duration guard is what keeps whole novels apart.
		{"Discworld 5", "discworld", chapterKeyTrailingNumber},
		// Series positions are separate books, not chapters.
		{"Mistborn Book 2", "", chapterKeyNone},
		{"Wheel of Time Vol. 3", "", chapterKeyNone},
		{"Wheel of Time Volume 3", "", chapterKeyNone},
		// No numbering.
		{"A Standalone Title", "", chapterKeyNone},
	}
	for _, tc := range cases {
		t.Run(tc.stem, func(t *testing.T) {
			key, kind := chapterGroupKey(tc.stem)
			require.Equal(t, tc.wantKind, kind, "kind for %q", tc.stem)
			require.Equal(t, tc.wantKey, key, "key for %q", tc.stem)
		})
	}
}

// stubChapterDurations makes every consolidation duration read return sec
// (or per-path overrides), and restores the reader and threshold afterwards.
func stubChapterDurations(t *testing.T, sec int, perPath map[string]int) {
	t.Helper()
	prevReader := chapterFileDurationSec
	prevThreshold := config.AppConfig.ChapterConsolidationThresholdMin
	t.Cleanup(func() {
		chapterFileDurationSec = prevReader
		config.AppConfig.ChapterConsolidationThresholdMin = prevThreshold
	})
	config.AppConfig.ChapterConsolidationThresholdMin = 10
	chapterFileDurationSec = func(p string) int {
		if d, ok := perPath[p]; ok {
			return d
		}
		return sec
	}
}

func pathsIn(dir string, names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(dir, n)
	}
	return out
}

func TestConsolidateChapterGroups_NumberingShapes(t *testing.T) {
	const dir = "/lib/Author/Work"
	cases := []struct {
		name      string
		files     []string
		durSec    int
		wantBooks int
		wantSegs  int // segments of the first multi-file book; 0 = none expected
	}{
		{"trailing numbers", pathsIn(dir, "Book Title 01.mp3", "Book Title 02.mp3", "Book Title 03.mp3", "Book Title 04.mp3"), 300, 1, 4},
		{"part N", pathsIn(dir, "Title - Part 1.mp3", "Title - Part 2.mp3", "Title - Part 3.mp3"), 300, 1, 3},
		{"chapter N", pathsIn(dir, "Chapter 1.mp3", "Chapter 2.mp3", "Chapter 3.mp3"), 300, 1, 3},
		{"disc N", pathsIn(dir, "Title Disc 1.mp3", "Title Disc 2.mp3", "Title Disc 3.mp3"), 300, 1, 3},
		{"N of M", pathsIn(dir, "Title 1 of 3.mp3", "Title 2 of 3.mp3", "Title 3 of 3.mp3"), 300, 1, 3},
		{"bare chapter numbers", pathsIn(dir, "97.mp3", "98.mp3", "99.mp3"), 300, 1, 3},
		// A Discworld shelf: trailing numbers, but every file is a whole
		// novel, so nothing merges.
		{"discworld shelf of long novels", pathsIn(dir, "Discworld 01.mp3", "Discworld 02.mp3", "Discworld 03.mp3"), 8 * 3600, 3, 0},
		{"discworld titles are not disc markers", pathsIn(dir, "Discworld - Mort.mp3", "Discworld - Sourcery.mp3", "Discworld - Eric.mp3"), 300, 3, 0},
		{"series books stay apart", pathsIn(dir, "Mistborn Book 1.mp3", "Mistborn Book 2.mp3", "Mistborn Book 3.mp3"), 300, 3, 0},
		{"two files are not a sequence", pathsIn(dir, "Title 01.mp3", "Title 02.mp3"), 300, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubChapterDurations(t, tc.durSec, nil)
			books := consolidateChapterGroups(context.Background(), tc.files)
			require.Len(t, books, tc.wantBooks)
			if tc.wantSegs > 0 {
				require.Len(t, books[0].SegmentFiles, tc.wantSegs)
				require.Equal(t, tc.durSec*tc.wantSegs, books[0].Duration)
			} else {
				for _, b := range books {
					require.Empty(t, b.SegmentFiles, "%s was merged", b.FilePath)
				}
			}
		})
	}
}

// TestConsolidateChapterGroups_SegmentsInNaturalOrder: "Chapter 2" before
// "Chapter 10", while FilePath stays the first file handed in.
func TestConsolidateChapterGroups_SegmentsInNaturalOrder(t *testing.T) {
	stubChapterDurations(t, 300, nil)
	files := pathsIn("/lib/W", "Chapter 1.mp3", "Chapter 10.mp3", "Chapter 2.mp3")
	books := consolidateChapterGroups(context.Background(), files)
	require.Len(t, books, 1)
	require.Equal(t, files[0], books[0].FilePath)
	require.Equal(t, pathsIn("/lib/W", "Chapter 1.mp3", "Chapter 2.mp3", "Chapter 10.mp3"), books[0].SegmentFiles)
}

// willNames is one title's chapters, n of them, all under one trailing-number
// key.
func willNames(n int) []string {
	var names []string
	for i := 1; i <= n; i++ {
		names = append(names, fmt.Sprintf("Will of the Empress %03d.mp3", i))
	}
	return names
}

// TestConsolidateChapterGroups_ProvenSequenceHasNoCeiling is S1 of the
// 2026-09-28 review: a group that passed "one key, 3+ files, every file short"
// cannot be an author shelf, so it becomes one book however many files it
// has. Refusing it (the first version of this change) imported nothing and
// said so only in a slog line.
func TestConsolidateChapterGroups_ProvenSequenceHasNoCeiling(t *testing.T) {
	stubChapterDurations(t, 300, nil)
	names := append(willNames(maxDirectoryBookFiles+50), "Readme Standalone.mp3")
	for _, mode := range []consolidateMode{consolidateUntagged, consolidateOversized} {
		books := consolidateChapterGroupsMode(context.Background(), pathsIn("/lib/W", names...), mode)
		require.Len(t, books, 2, "mode %d: one book for the sequence, one for the standalone file", mode)
		require.Len(t, books[0].SegmentFiles, maxDirectoryBookFiles+50)
		require.True(t, books[0].chapterSequence, "the sequence must carry its exemption from the row backstop")
		require.False(t, books[1].chapterSequence)
	}
}

// TestConsolidateOversized_NeverShattersASameTitleGroup is S2: in the
// oversized-directory path a same-key group becomes one book, stays per-file
// only when EVERY file is a whole book (a shelf), and is otherwise refused and
// counted -- including when the threshold is 0 and when a single long file
// sits among hundreds of short chapters.
func TestConsolidateOversized_NeverShattersASameTitleGroup(t *testing.T) {
	files := pathsIn("/lib/W", willNames(maxDirectoryBookFiles+10)...)
	t.Run("a zero threshold is the default, not off", func(t *testing.T) {
		// Until 2026-10-03 a 0 switched untagged consolidation off and the
		// untagged path returned one book per file. A stray 0 did exactly
		// that in production for eleven days, so 0 now resolves to the
		// default on BOTH paths.
		stubChapterDurations(t, 300, nil)
		config.AppConfig.ChapterConsolidationThresholdMin = 0
		books := consolidateChapterGroupsMode(context.Background(), files, consolidateOversized)
		require.Len(t, books, 1)
		require.Len(t, books[0].SegmentFiles, len(files))
		untagged := consolidateChapterGroups(context.Background(), files)
		require.Len(t, untagged, 1, "a zero threshold must not shatter untagged chapters into one book per file")
		require.Len(t, untagged[0].SegmentFiles, len(files))
	})
	t.Run("one long chapter among short ones is refused, not shattered", func(t *testing.T) {
		stubChapterDurations(t, 300, map[string]int{files[7]: 3 * 3600})
		ctx, rc := withScanRunCounters(context.Background())
		books := consolidateChapterGroupsMode(ctx, files, consolidateOversized)
		require.Empty(t, books)
		require.Equal(t, int64(1), rc.oversizedGroups.Load())
		require.Equal(t, int64(len(files)), rc.oversizedFiles.Load())
		// The untagged path still stands such a group per file, as before.
		require.Len(t, consolidateChapterGroups(context.Background(), files), len(files))
	})
	t.Run("unreadable durations are refused, not shattered", func(t *testing.T) {
		stubChapterDurations(t, 0, nil)
		ctx, rc := withScanRunCounters(context.Background())
		require.Empty(t, consolidateChapterGroupsMode(ctx, files, consolidateOversized))
		require.Equal(t, int64(1), rc.oversizedGroups.Load())
	})
	t.Run("every file a whole book is a shelf", func(t *testing.T) {
		stubChapterDurations(t, 9*3600, nil)
		books := consolidateChapterGroupsMode(context.Background(), files, consolidateOversized)
		require.Len(t, books, len(files))
	})
}

// TestGroupFileDurationSec_UsesTheStoredRowDuration is N1: a file whose
// present book_file row already records a duration for the same size is not
// probed again on every rescan.
func TestGroupFileDurationSec_UsesTheStoredRowDuration(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)
	probes := 0
	prev := chapterFileDurationSec
	t.Cleanup(func() { chapterFileDurationSec = prev })
	chapterFileDurationSec = func(string) int { probes++; return 42 }

	rows, err := f.store.GetBookFiles(f.parent.ID)
	require.NoError(t, err)
	var known, stale string
	for i, r := range rows {
		r.Duration = 777
		r.FileSize = 1 // every fixture file is one byte
		if i == 1 {
			r.FileSize = 99 // the file changed since the row was written
			stale = r.FilePath
		} else {
			known = r.FilePath
		}
		require.NoError(t, f.store.UpdateBookFile(r.ID, &r))
	}
	require.Equal(t, 777, groupFileDurationSec(known))
	require.Zero(t, probes, "a stored duration for the same file was probed again")
	require.Equal(t, 42, groupFileDurationSec(stale))
	require.Equal(t, 42, groupFileDurationSec(f.loose[0]))
	require.Equal(t, 2, probes)
}

// TestProcessBooksParallel_ProvenSequenceOverCeilingGetsAllItsRows is S1 end
// to end: the createBookFilesForBook backstop must not refuse the rows of a
// book the grouping already proved to be one chapter sequence.
func TestProcessBooksParallel_ProvenSequenceOverCeilingGetsAllItsRows(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	stubChapterDurations(t, 240, nil)
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)
	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	files := writeDirFixture(t, dir, willNames(maxDirectoryBookFiles+20), "Will of the Empress", func(int) bool { return true })
	books := groupFilesIntoBooks(context.Background(), files)
	require.Len(t, books, 1)
	require.NoError(t, ProcessBooksParallel(context.Background(), books, 2, nil, logger.New("test")))
	all, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	rows, err := store.GetBookFiles(all[0].ID)
	require.NoError(t, err)
	require.Len(t, rows, maxDirectoryBookFiles+20, "the row backstop refused a proven chapter sequence")
}

// TestProcessBooksParallel_UntaggedProvenSequenceOverCeiling is S1 on the
// UNTAGGED path the review cited (the consolidation refusal at
// chapter_consolidation.go:267): a flat single-title folder of short,
// untagged chapter files over the ceiling must import as one book with every
// file, not import nothing.
func TestProcessBooksParallel_UntaggedProvenSequenceOverCeiling(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	stubChapterDurations(t, 240, nil)
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)
	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	n := maxDirectoryBookFiles + 20
	files := writeDirFixture(t, dir, willNames(n), "", func(int) bool { return false })
	books := groupFilesIntoBooks(context.Background(), files)
	require.Len(t, books, 1, "the untagged sequence must group into one book")
	require.Len(t, books[0].SegmentFiles, n)
	require.NoError(t, ProcessBooksParallel(context.Background(), books, 2, nil, logger.New("test")))
	all, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	rows, err := store.GetBookFiles(all[0].ID)
	require.NoError(t, err)
	require.Len(t, rows, n, "the untagged proven sequence lost rows to the ceiling")
}

// bibleNames is a flat single-work folder: three biblical books, chapters
// numbered per book, 300 files in all -- over the ceiling as one folder.
func bibleNames() []string {
	var names []string
	for _, bk := range []struct {
		n     int
		name  string
		count int
	}{{1, "Genesis", 120}, {2, "Exodus", 100}, {3, "Leviticus", 80}} {
		for c := 1; c <= bk.count; c++ {
			names = append(names, fmt.Sprintf("%02d %s %03d.mp3", bk.n, bk.name, c))
		}
	}
	return names
}

// TestGroupFilesIntoBooks_OversizedFlatWorkIsSubGrouped is task C: a flat
// directory over the ceiling whose files all share one album tag used to fall
// back to one book per file (a 1,188-file Bible became 1,188 books). It must
// now sub-group by filename stem, each marked as sharing its directory.
func TestGroupFilesIntoBooks_OversizedFlatWorkIsSubGrouped(t *testing.T) {
	stubChapterDurations(t, 240, nil)
	dir := t.TempDir()
	files := writeDirFixture(t, dir, bibleNames(), "The Bible", func(int) bool { return true })

	books := groupFilesIntoBooks(context.Background(), files)

	require.Len(t, books, 3, "want one book per biblical book, got %d", len(books))
	sizes := map[string]int{}
	for _, b := range books {
		require.True(t, b.sharesDirectory, "%s must keep its own path", b.FilePath)
		first := filepath.Base(b.SegmentFiles[0])
		sizes[strings.Fields(first)[1]] = len(b.SegmentFiles)
	}
	require.Equal(t, map[string]int{"Genesis": 120, "Exodus": 100, "Leviticus": 80}, sizes)
}

// TestGroupFilesIntoBooks_OversizedShelfOfNovelsStaysPerFile keeps the
// author-shelf refusal: a shared album tag over a folder of whole novels
// ("Series 1", "Series 2", …, hours each) merges nothing.
func TestGroupFilesIntoBooks_OversizedShelfOfNovelsStaysPerFile(t *testing.T) {
	stubChapterDurations(t, 9*3600, nil)
	dir := t.TempDir()
	var names []string
	for i := 1; i <= maxDirectoryBookFiles+9; i++ {
		names = append(names, fmt.Sprintf("Shelf Series %d.mp3", i))
	}
	files := writeDirFixture(t, dir, names, "Prolific Author", func(int) bool { return true })

	books := groupFilesIntoBooks(context.Background(), files)

	require.Len(t, books, len(files))
	for _, b := range books {
		require.Empty(t, b.SegmentFiles)
	}
}

// TestProcessBooksParallel_SubGroupsKeepDistinctPaths: the sub-grouped books
// of one folder are saved with their own first-file paths, not all normalized
// to the one directory.
func TestProcessBooksParallel_SubGroupsKeepDistinctPaths(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	stubChapterDurations(t, 240, nil)

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	files := writeDirFixture(t, dir, bibleNames(), "The Bible", func(int) bool { return true })
	books := groupFilesIntoBooks(context.Background(), files)
	require.Len(t, books, 3)
	require.NoError(t, ProcessBooksParallel(context.Background(), books, 2, nil, logger.New("test")))

	all, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	paths := map[string]int{}
	for _, b := range all {
		paths[b.FilePath]++
	}
	require.Len(t, all, 3, "one row per sub-group")
	require.Zero(t, paths[dir], "a sub-group was normalized to the shared directory")
	for p, n := range paths {
		require.Equal(t, 1, n, "%d books share path %s", n, p)
	}
	for _, b := range all {
		rows, err := store.GetBookFiles(b.ID)
		require.NoError(t, err)
		require.NotEmpty(t, rows, "book %s got no book_file rows", b.FilePath)
	}
}
