// file: internal/metafetch/service_writeback_duprows_test.go
// version: 1.0.0
// guid: 2c65b832-5fea-41f4-9b1e-551dd05d7eb8
// last-edited: 2026-08-21

package metafetch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Tests for dedupeBookFilesByPath (DUPROW-1).
//
// The prod incident these pin: book 01KZR9GEH5ZQW9CV1EN130Y7C0 held 42
// book_file rows for 21 distinct paths, so every file was tag-written twice,
// the path organizer was handed 42 "files" for a 21-file book, and the second
// rename pass for a path failed with ENOENT because the first had already
// moved the file.

func TestDedupeBookFilesByPath_CollapsesExactDuplicates(t *testing.T) {
	t.Run("prod_shape_42_rows_21_paths", func(t *testing.T) {
		// Interleave the twins the way the DB returns them, so a helper that
		// only collapsed ADJACENT duplicates would still fail this.
		var in []database.BookFile
		var wantPaths []string
		for i := 0; i < 21; i++ {
			p := fmt.Sprintf("/mnt/bigdata/books/Book/%02d.m4b", i)
			wantPaths = append(wantPaths, p)
			in = append(in, database.BookFile{ID: fmt.Sprintf("a%02d", i), FilePath: p})
		}
		for i := 0; i < 21; i++ {
			in = append(in, database.BookFile{
				ID:       fmt.Sprintf("b%02d", i),
				FilePath: fmt.Sprintf("/mnt/bigdata/books/Book/%02d.m4b", i),
			})
		}
		require.Len(t, in, 42, "fixture must reproduce the prod row count")

		out := dedupeBookFilesByPath("01KZR9GEH5ZQW9CV1EN130Y7C0", in)

		require.Len(t, out, 21)
		gotPaths := make([]string, 0, len(out))
		for _, f := range out {
			gotPaths = append(gotPaths, f.FilePath)
		}
		assert.Equal(t, wantPaths, gotPaths, "survivors must keep first-seen order")
		assert.Len(t, in, 42, "the input slice must not be mutated in place")
	})

	t.Run("single_row_returned_unchanged", func(t *testing.T) {
		in := []database.BookFile{{ID: "f1", FilePath: "/a/1.m4b"}}
		assert.Equal(t, in, dedupeBookFilesByPath("book-1", in))
	})

	t.Run("nil_input", func(t *testing.T) {
		assert.Nil(t, dedupeBookFilesByPath("book-1", nil))
	})
}

// TestDedupeBookFilesByPath_KeepsTheFingerprintedTwin is the data-loss
// assertion: a fingerprint costs a full-file decode and cannot be recovered by
// guessing, so the twin that carries one must be the survivor even when it is
// not the row the DB happened to return first.
func TestDedupeBookFilesByPath_KeepsTheFingerprintedTwin(t *testing.T) {
	in := []database.BookFile{
		{ID: "f1", FilePath: "/a/1.m4b"},
		{ID: "f2", FilePath: "/a/1.m4b", AcoustIDFingerprint: []byte{0x01}},
	}

	out := dedupeBookFilesByPath("book-1", in)

	require.Len(t, out, 1)
	assert.Equal(t, "f2", out[0].ID, "the fingerprinted row must survive")
	assert.NotEmpty(t, out[0].AcoustIDFingerprint)
}

// TestDedupeBookFilesByPath_KeeperOrderMatchesRankKeeper mirrors the sub-cases
// in internal/plugins/maintenance/dedupe_book_file_rows_test.go. The order is
// duplicated by hand because rankKeeper is unexported in another package; if
// either side changes, this test is the tripwire.
func TestDedupeBookFilesByPath_KeeperOrderMatchesRankKeeper(t *testing.T) {
	t.Run("fingerprint_beats_duration", func(t *testing.T) {
		out := dedupeBookFilesByPath("book-1", []database.BookFile{
			{ID: "dur", FilePath: "/a/1.m4b", Duration: 3600},
			{ID: "fp", FilePath: "/a/1.m4b", AcoustIDFingerprint: []byte{0x01}},
		})
		require.Len(t, out, 1)
		assert.Equal(t, "fp", out[0].ID)
	})

	t.Run("duration_beats_nothing", func(t *testing.T) {
		out := dedupeBookFilesByPath("book-1", []database.BookFile{
			{ID: "bare", FilePath: "/a/1.m4b"},
			{ID: "dur", FilePath: "/a/1.m4b", Duration: 3600},
		})
		require.Len(t, out, 1)
		assert.Equal(t, "dur", out[0].ID)
	})

	t.Run("hash_beats_nothing", func(t *testing.T) {
		out := dedupeBookFilesByPath("book-1", []database.BookFile{
			{ID: "bare", FilePath: "/a/1.m4b"},
			{ID: "hash", FilePath: "/a/1.m4b", FileHash: "deadbeef"},
		})
		require.Len(t, out, 1)
		assert.Equal(t, "hash", out[0].ID)
	})

	t.Run("full_tie_smallest_id_wins_deterministically", func(t *testing.T) {
		// Built fresh each time so the two runs share no slice backing array:
		// this asserts determinism of the RULE, not of one call's aliasing.
		build := func() []database.BookFile {
			return []database.BookFile{
				{ID: "zzz", FilePath: "/a/1.m4b"},
				{ID: "aaa", FilePath: "/a/1.m4b"},
				{ID: "mmm", FilePath: "/a/1.m4b"},
			}
		}
		first := dedupeBookFilesByPath("book-1", build())
		second := dedupeBookFilesByPath("book-1", build())
		require.Len(t, first, 1)
		require.Len(t, second, 1)
		assert.Equal(t, "aaa", first[0].ID, "lexicographically smallest ID must win")
		assert.Equal(t, first[0].ID, second[0].ID, "a dry run and the run after it must pick the same survivor")
	})
}

func TestDedupeBookFilesByPath_NormalizesPathBeforeComparing(t *testing.T) {
	out := dedupeBookFilesByPath("book-1", []database.BookFile{
		{ID: "f1", FilePath: "/a/b/1.mp3"},
		{ID: "f2", FilePath: "/a/./b/1.mp3"},
		{ID: "f3", FilePath: "/a/b/1.mp3 "},
	})

	require.Len(t, out, 1, "Clean+TrimSpace variants of one path are one file")
	assert.Equal(t, "f1", out[0].ID)
}

// TestDedupeBookFilesByPath_DistinctPathsAllSurvive is the anti-over-suppression
// test. Without it, a helper that returned only the first row would pass every
// other assertion in this file.
func TestDedupeBookFilesByPath_DistinctPathsAllSurvive(t *testing.T) {
	in := make([]database.BookFile, 0, 21)
	for i := 0; i < 21; i++ {
		in = append(in, database.BookFile{
			ID:       fmt.Sprintf("f%02d", i),
			FilePath: fmt.Sprintf("/mnt/bigdata/books/Book/%02d.m4b", i),
		})
	}

	out := dedupeBookFilesByPath("book-1", in)

	require.Len(t, out, 21, "a known-good input must survive the new guard intact")
	for i, f := range out {
		assert.Equal(t, in[i].ID, f.ID, "row %d must be preserved in order", i)
	}
}

// TestDedupeBookFilesByPath_EmptyPathRowsPassThrough pins that "unknown" is
// never treated as "duplicate": two rows with no path are two rows, not one.
// Dropping them is internal/organizer/pipeline.go's job, not this helper's.
func TestDedupeBookFilesByPath_EmptyPathRowsPassThrough(t *testing.T) {
	out := dedupeBookFilesByPath("book-1", []database.BookFile{
		{ID: "e1", FilePath: ""},
		{ID: "e2", FilePath: "   "},
		{ID: "f1", FilePath: "/a/1.m4b"},
	})

	require.Len(t, out, 3)
	ids := []string{out[0].ID, out[1].ID, out[2].ID}
	assert.Equal(t, []string{"e1", "e2", "f1"}, ids)
}
