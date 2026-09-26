// file: internal/database/book_file_copies_test.go
// version: 1.2.0
// guid: 2b7e9c41-5d06-4f38-a1c2-8e4d7f0b3a95
// last-edited: 2026-09-26

package database

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DUR-SAME-FOLDER-COPIES: the copy evidence itself. Every case is checked both
// ways round, since IsBookFileCopy must be symmetric.
func TestIsBookFileCopy_CopySuffixAndUnknownDuration(t *testing.T) {
	cases := []struct {
		name string
		a, b BookFile
		want bool
	}{
		{"_copy1 twin beside its original: same size and duration",
			BookFile{FilePath: "/lib/A/Flame/Flame - 01.m4a", FileSize: 5000, Duration: 3600},
			BookFile{FilePath: "/lib/A/Flame/Flame - 01_copy1.m4a", FileSize: 5000, Duration: 3600}, true},
		{"_copy2 twin whose duration was never measured, same folder",
			BookFile{FilePath: "/lib/A/Flame/Flame - 01.m4a", FileSize: 5000, Duration: 3600},
			BookFile{FilePath: "/lib/A/Flame/Flame - 01_copy2.m4a", FileSize: 5000}, true},
		{"both twins unmeasured, same folder",
			BookFile{FilePath: "/lib/A/Flame/Flame - 01.m4a", FileSize: 5000},
			BookFile{FilePath: "/lib/A/Flame/Flame - 01_copy1.m4a", FileSize: 5000}, true},
		{"_copyN suffix in OriginalFilename too",
			BookFile{FilePath: "/lib/A/Flame/x.m4a", OriginalFilename: "Flame - 01_copy1.m4a", FileSize: 5000, Duration: 3600},
			BookFile{FilePath: "/lib/A/Flame/Flame - 01.m4a", FileSize: 5000, Duration: 3600}, true},
		// Axiom in prod: organize suffixed a DIFFERENT chapter that collided.
		{"Axiom: distinct chapter collision-renamed to _copy1, different size",
			BookFile{FilePath: "/lib/A/Axiom/Axiom - 02.m4a", FileSize: 41_000_000, Duration: 2400},
			BookFile{FilePath: "/lib/A/Axiom/Axiom - 02_copy1.m4a", FileSize: 38_500_000, Duration: 2250}, false},
		{"Axiom: distinct _copy1 chapter, different size, durations unmeasured",
			BookFile{FilePath: "/lib/A/Axiom/Axiom - 02.m4a", FileSize: 41_000_000},
			BookFile{FilePath: "/lib/A/Axiom/Axiom - 02_copy1.m4a", FileSize: 38_500_000}, false},
		{"same size, durations 2 s apart",
			BookFile{FilePath: "/lib/A/B/01.m4a", FileSize: 5000, Duration: 3600},
			BookFile{FilePath: "/lib/A/B/01_copy1.m4a", FileSize: 5000, Duration: 3602}, false},
		{"one duration unknown, different folders: no evidence",
			BookFile{FilePath: "/lib/A/B/01.m4a", FileSize: 5000, Duration: 3600},
			BookFile{FilePath: "/srv/old/B/01.m4a", FileSize: 5000}, false},
		{"_copy with no number is a different name",
			BookFile{FilePath: "/lib/A/B/01.m4a", FileSize: 5000, Duration: 3600},
			BookFile{FilePath: "/lib/A/B/01_copy.m4a", FileSize: 5000, Duration: 3600}, false},
		// <Book>/<Book> - N/<same name>: the shared name is the layout.
		{"chapter folders: same name and size, both unmeasured",
			BookFile{FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 03/32.m4b", FileSize: 9000},
			BookFile{FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 04/32.m4b", FileSize: 9000}, false},
		{"chapter folders: same name, size and duration",
			BookFile{FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 03/32.m4b", FileSize: 9000, Duration: 600},
			BookFile{FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 04/32.m4b", FileSize: 9000, Duration: 600}, false},
		{"chapter folders: a shared hash still counts",
			BookFile{FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 03/32.m4b", FileHash: "h", FileSize: 9000},
			BookFile{FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 04/32.m4b", FileHash: "h", FileSize: 9000}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsBookFileCopy(c.a, c.b))
			assert.Equal(t, c.want, IsBookFileCopy(c.b, c.a), "symmetric")
		})
	}
}

func TestStripCopySuffix(t *testing.T) {
	for in, want := range map[string]string{
		"01.m4a":               "01.m4a",
		"01_copy1.m4a":         "01.m4a",
		"Book - 02_copy12.m4b": "Book - 02.m4b",
		"_copy1.m4a":           "_copy1.m4a",
		"01_copy.m4a":          "01_copy.m4a",
		"01_copy1_copy2.m4a":   "01_copy1.m4a",
		"noext_copy3":          "noext",
	} {
		assert.Equal(t, want, stripCopySuffix(in), in)
	}
}

// DUR-SAME-FOLDER-COPIES: the counted set per book shape. Every case names the
// rows that must be excluded; every other row must count.
func TestSplitOwnFolderFiles_Copies(t *testing.T) {
	const flame = "/lib/Travis Bagwell/Awaken Online: Flame"
	cases := []struct {
		name     string
		bookPath string
		rows     []BookFile
		copies   []string
	}{
		{
			// Awaken Online: Flame's shape: `_copy1` twins and zero-duration
			// rows all inside the own folder. The unmeasured 03 is a real
			// chapter and must stay counted so zero_rows_only can fill it.
			name:     "Flame: same-folder _copyN twins, one unmeasured",
			bookPath: flame,
			rows: []BookFile{
				{ID: "01", FilePath: flame + "/Flame - 01.m4a", FileSize: 5000, Duration: 3600},
				{ID: "01c", FilePath: flame + "/Flame - 01_copy1.m4a", FileSize: 5000},
				{ID: "02", FilePath: flame + "/Flame - 02.m4a", FileSize: 6000, Duration: 4000},
				{ID: "02c", FilePath: flame + "/Flame - 02_copy1.m4a", FileSize: 6000, Duration: 4000},
				{ID: "03", FilePath: flame + "/Flame - 03.m4a", FileSize: 7000},
			},
			copies: []string{"01c", "02c"},
		},
		{
			name:     "Axiom: distinct-size _copy1 chapters all count",
			bookPath: "/lib/A/Axiom",
			rows: []BookFile{
				{ID: "02", FilePath: "/lib/A/Axiom/Axiom - 02.m4a", FileSize: 41_000_000, Duration: 2400},
				{ID: "02c", FilePath: "/lib/A/Axiom/Axiom - 02_copy1.m4a", FileSize: 38_500_000, Duration: 2250},
				{ID: "03", FilePath: "/lib/A/Axiom/Axiom - 03.m4a", FileSize: 40_000_000},
				{ID: "03c", FilePath: "/lib/A/Axiom/Axiom - 03_copy1.m4a", FileSize: 39_000_000},
			},
		},
		{
			name:     "the _copy1 row listed first: the original is kept",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "c", FilePath: "/lib/A/B/01_copy1.m4a", FileSize: 5000, Duration: 3600},
				{ID: "o", FilePath: "/lib/A/B/01.m4a", FileSize: 5000, Duration: 3600},
			},
			copies: []string{"c"},
		},
		{
			name:     "a measured twin is kept over an unmeasured original",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "o", FilePath: "/lib/A/B/01.m4a", FileSize: 5000},
				{ID: "c", FilePath: "/lib/A/B/01_copy1.m4a", FileSize: 5000, Duration: 3600},
			},
			copies: []string{"o"},
		},
		{
			name:     "a present twin is kept over a missing original",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "o", FilePath: "/lib/A/B/01.m4a", FileSize: 5000, Duration: 3600, Missing: true},
				{ID: "c", FilePath: "/lib/A/B/01_copy1.m4a", FileSize: 5000, Duration: 3600},
			},
			copies: []string{"o"},
		},
		{
			// Present beats measured: listing a dead track is worse than
			// reading short until the backfill fills the counted zero row.
			name:     "a present unmeasured twin is kept over a missing measured original",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "o", FilePath: "/lib/A/B/01.m4a", FileSize: 5000, Duration: 3600, Missing: true},
				{ID: "c", FilePath: "/lib/A/B/01_copy1.m4a", FileSize: 5000},
			},
			copies: []string{"o"},
		},
		{
			// DUR "pairs of out-of-folder rows": two copies of one file, both
			// outside the own folder and neither a copy of an own row. The
			// non-iTunes row is kept although the iTunes row comes first
			// (COPY-KEEPER-NON-ITUNES-OUTSIDE; see keeperLess rule 3).
			name:     "two out-of-folder copies of each other count once",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "own", FilePath: "/lib/A/B/01.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
				{ID: "itunes", FilePath: "/srv/books/itunes/A/B/02.m4b", FileHash: "h2", FileSize: 200, Duration: 70},
				{ID: "old", FilePath: "/srv/old/B/02.m4b", FileHash: "h2", FileSize: 200, Duration: 70},
			},
			copies: []string{"itunes"},
		},
		{
			// COPY-KEEPER-NON-ITUNES-OUTSIDE: the cluster has no own-folder
			// row, so rule 2 cannot decide. The unmeasured non-iTunes row is
			// kept over the measured books/itunes/** twin, so zero_rows_only
			// fills it instead of skipping the whole book as "itunes".
			name:     "no own row in the cluster: non-iTunes row kept over measured iTunes twin",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "own", FilePath: "/lib/A/B/01.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
				{ID: "itunes", FilePath: "/srv/books/itunes/A/B/02.m4b", FileHash: "h2", FileSize: 200, Duration: 70},
				{ID: "old", FilePath: "/srv/old/B/02.m4b", FileHash: "h2", FileSize: 200},
			},
			copies: []string{"itunes"},
		},
		{
			// Presence still ranks above the frozen rule: a missing
			// non-iTunes row does not displace a present iTunes twin.
			name:     "present iTunes row kept over a missing non-iTunes twin",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "own", FilePath: "/lib/A/B/01.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
				{ID: "old", FilePath: "/srv/old/B/02.m4b", FileHash: "h2", FileSize: 200, Missing: true},
				{ID: "itunes", FilePath: "/srv/books/itunes/A/B/02.m4b", FileHash: "h2", FileSize: 200, Duration: 70},
			},
			copies: []string{"old"},
		},
		{
			// Own folder beats measured: the unmeasured own row is kept and
			// the measured iTunes twin is the copy (review finding
			// 2026-09-25; see keeperLess).
			name:     "unmeasured own row kept over a measured iTunes twin",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "itunes", FilePath: "/srv/books/itunes/A/B/01.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
				{ID: "own", FilePath: "/lib/A/B/01.m4b", FileHash: "h1", FileSize: 100},
			},
			copies: []string{"itunes"},
		},
		{
			name:     "out-of-folder _copy1 of an own row",
			bookPath: "/lib/A/B",
			rows: []BookFile{
				{ID: "own", FilePath: "/lib/A/B/01.m4b", FileSize: 100, Duration: 60},
				{ID: "old", FilePath: "/srv/old/B/01_copy1.m4b", FileSize: 100, Duration: 60},
			},
			copies: []string{"old"},
		},
		{
			// No present own-folder row: a same-folder twin is still a copy
			// (that evidence needs no own folder), rows in different folders
			// are not matched (#3552's no-basis rule).
			name:     "no own basis: same-folder twin only",
			bookPath: "/lib/A/Gone",
			rows: []BookFile{
				{ID: "x", FilePath: "/srv/old/X/01.mp3", FileSize: 1000, Duration: 100},
				{ID: "xc", FilePath: "/srv/old/X/01_copy1.mp3", FileSize: 1000, Duration: 100},
				{ID: "y", FilePath: "/srv/old/Y/01.mp3", FileSize: 1000, Duration: 100},
			},
			copies: []string{"xc"},
		},
		{
			// CHAPTER-SUBFOLDER-NN-ROWS' layout: every chapter file has the
			// same name. Equal sizes on two chapters must not collapse them.
			name:     "chapter-folder layout: every chapter counts",
			bookPath: "/lib/A/Steamforged Sorcery",
			rows: []BookFile{
				{ID: "c1", FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 01/32.m4b", FileSize: 9000, Duration: 600},
				{ID: "c2", FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 02/32.m4b", FileSize: 9000, Duration: 600},
				{ID: "c3", FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 03/32.m4b", FileSize: 9000},
				{ID: "c4", FilePath: "/lib/A/Steamforged Sorcery/Steamforged Sorcery - 04/32.m4b", FileSize: 9100},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			book := &Book{FilePath: c.bookPath}
			s := SplitOwnFolderFiles(book, c.rows)
			got := ownFolderIDs(s.Copies)
			if len(c.copies) == 0 {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, c.copies, got)
			}
			counted := OwnFolderFiles(book, c.rows)
			assert.Len(t, counted, len(c.rows)-len(c.copies))

			// Excluding a PRESENT copy never loses a duration on its own
			// side of the own folder and of the iTunes tree: a present,
			// measured copy always has a counted twin of its size that is
			// measured, sits inside the own folder, or is a non-iTunes row
			// kept over an iTunes copy. (An unmeasured own row is kept over
			// a measured out-of-folder twin, an unmeasured non-iTunes row
			// over a measured books/itunes/** twin, and a missing measured
			// copy may give way to a present unmeasured twin; the backfill
			// fills all three. See keeperLess.)
			for _, cp := range s.Copies {
				if cp.Duration <= 0 || cp.Missing {
					continue
				}
				found := false
				for _, k := range counted {
					ownRow := s.Dir != "" && pathutil.IsWithin(k.FilePath, s.Dir)
					overITunes := pathutil.UnderFrozenITunesTree(cp.FilePath) && !pathutil.UnderFrozenITunesTree(k.FilePath)
					found = found || (k.FileSize == cp.FileSize && (k.Duration > 0 || ownRow || overITunes))
				}
				assert.True(t, found, "copy %s carried a duration no counted twin has", cp.ID)
			}
		})
	}
}

// The Flame shape end to end through RecomputeBookAggregates: a book whose
// stored Duration was inflated by summing twins is corrected DOWN to the
// counted sum, and the partial-data rule does not hold the inflated value.
func TestRecomputeBookAggregates_SameFolderCopiesCorrectDown(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	const dir = "/lib/Travis Bagwell/Awaken Online: Flame"
	b, err := store.CreateBook(&Book{Title: "Flame", FilePath: dir, Duration: new(15200), FileSize: new(int64(260_000_000))})
	require.NoError(t, err)
	rows := []BookFile{
		{FilePath: dir + "/Flame - 01.m4a", FileSize: 50_000_000, Duration: 3600},
		{FilePath: dir + "/Flame - 01_copy1.m4a", FileSize: 50_000_000, Duration: 3600},
		{FilePath: dir + "/Flame - 02.m4a", FileSize: 60_000_000, Duration: 4000},
		{FilePath: dir + "/Flame - 02_copy1.m4a", FileSize: 60_000_000}, // unmeasured twin
		{FilePath: dir + "/Flame - 03.m4a", FileSize: 40_000_000, Duration: 2000},
	}
	for i := range rows {
		f := rows[i]
		f.BookID = b.ID
		f.Format = "m4a"
		require.NoError(t, store.CreateBookFile(&f))
	}
	require.NoError(t, store.RecomputeBookAggregates(b.ID))
	got, err := store.GetBookByID(b.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.NotNil(t, got.FileSize)
	assert.Equal(t, 9600, *got.Duration, "3600 + 4000 + 2000: each twin counted once")
	assert.Equal(t, int64(150_000_000), *got.FileSize)
}

// Review finding (2026-09-25): legacy scanner.ComputeSegmentFileHash values
// hash only the first 1 MB, so distinct tracks that share an opening (the same
// album-only tags and a large embedded cover) carry one FileHash. A hash match
// between rows whose known sizes disagree is that
// collision, not a copy: the tracks of one book must all count.
func TestSplitOwnFolderFiles_LegacyPrefixHashCollision(t *testing.T) {
	const dir = "/lib/A/Prefix"
	rows := []BookFile{
		{ID: "01", FilePath: dir + "/01.mp3", FileHash: "prefix", FileSize: 10_000_000, Duration: 1800},
		{ID: "02", FilePath: dir + "/02.mp3", FileHash: "prefix", FileSize: 12_000_000, Duration: 2100},
		{ID: "03", FilePath: dir + "/03.mp3", FileHash: "prefix", FileSize: 11_000_000, Duration: 1950},
	}
	book := &Book{FilePath: dir}
	s := SplitOwnFolderFiles(book, rows)
	assert.Empty(t, ownFolderIDs(s.Copies), "distinct tracks sharing a 1 MB-prefix hash are not copies")
	assert.Len(t, OwnFolderFiles(book, rows), 3)

	t.Run("equal sizes, durations apart: still a copy", func(t *testing.T) {
		// The two durations can come from different measurements (iTunes'
		// Total Time vs the backfill's), so they never veto a hash match.
		a := BookFile{FilePath: "/srv/books/itunes/A/Prefix/01.mp3", FileHash: "prefix", FileSize: 5000, Duration: 1800}
		b := BookFile{FilePath: dir + "/01.mp3", FileHash: "prefix", FileSize: 5000, Duration: 1805}
		assert.True(t, IsBookFileCopy(a, b))
		assert.True(t, IsBookFileCopy(b, a))
	})
	t.Run("an unknown size or duration does not veto a shared hash", func(t *testing.T) {
		a := BookFile{FilePath: dir + "/01.mp3", FileHash: "h", FileSize: 5000, Duration: 1800}
		b := BookFile{FilePath: "/srv/other/01.mp3", FileHash: "h"}
		assert.True(t, IsBookFileCopy(a, b))
		assert.True(t, IsBookFileCopy(b, a))
	})

	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	d, sz := recomputeDuration(t, store, dir, rows)
	assert.Equal(t, 1800+2100+1950, d, "every track counts")
	assert.Equal(t, int64(33_000_000), sz)
}

// Review finding (2026-09-25): the keeper of a copy cluster is the own-folder
// row even when an out-of-folder twin (here the iTunes copy) carries the
// duration and the own row is still unmeasured. Keeping the iTunes row made
// zero_rows_only skip the whole book as "itunes" and ABS stream the iTunes
// file; keeping the own row lets the backfill fill it.
func TestSplitOwnFolderFiles_OwnRowKeptOverMeasuredOutOfFolderTwin(t *testing.T) {
	const dir = "/lib/A/Book"
	rows := []BookFile{
		{ID: "own01", FilePath: dir + "/01.m4a", FileHash: "h1", FileSize: 100},
		{ID: "own02", FilePath: dir + "/02.m4a", FileHash: "h2", FileSize: 200},
		{ID: "itunes01", FilePath: "/srv/books/itunes/A/Book/01 Track.m4a", FileHash: "h1", FileSize: 100, Duration: 60},
	}
	s := SplitOwnFolderFiles(&Book{FilePath: dir}, rows)
	assert.Equal(t, []string{"itunes01"}, ownFolderIDs(s.Copies))
	assert.Equal(t, []string{"own01", "own02"}, ownFolderIDs(s.Counted(rows)))
}
