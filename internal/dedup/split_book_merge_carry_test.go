// file: internal/dedup/split_book_merge_carry_test.go
// version: 1.0.0
// guid: e52db43b-d7f1-4be6-947a-639edb4e616c
// last-edited: 2026-09-19

package dedup

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/stretchr/testify/require"
)

func carryStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func carryBook(t *testing.T, s *database.PebbleStore, title, path string, track int) (string, string) {
	t.Helper()
	b, err := s.CreateBook(&database.Book{Title: title, FilePath: path})
	require.NoError(t, err)
	f := &database.BookFile{BookID: b.ID, FilePath: path, Duration: 300, FileSize: 1 << 20, TrackNumber: track, DiscNumber: 1}
	require.NoError(t, s.CreateBookFile(f))
	return b.ID, f.ID
}

// A split-book / chapter merge soft-deletes each source. Listening progress
// stayed on the source and was lost at the scheduled purge; it must be carried
// onto the keep, journaled, and put back by UndoCombine along with the files.
func TestMergeSplitBookCluster_CarriesProgressAndUndoRestoresIt(t *testing.T) {
	s := carryStore(t)
	u, err := s.CreateUser("reader", "reader@example.invalid", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	keep, keepFile := carryBook(t, s, "01 - Tale", "/lib/A/Tale/01 - Tale.mp3", 9)
	src, srcFile := carryBook(t, s, "02 - Tale", "/lib/A/Tale/02 - Tale.mp3", 4)
	require.NoError(t, s.SetUserPosition(u.ID, src, srcFile, 120))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: src, Status: database.UserBookStatusInProgress, ProgressPct: 40}))

	res, err := MergeSplitBookClusterWithOptions(s, keep, []string{src}, "", SplitMergeOptions{FileOrder: []string{keepFile, srcFile}})
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 1, res.MergedSrcCount)

	pos, err := s.ListUserPositionsForBook(u.ID, keep)
	require.NoError(t, err)
	require.NotEmpty(t, pos, "the reader's position on the source must be carried onto the keep")
	st, err := s.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.NotNil(t, st, "the reader's book state must be carried onto the keep")

	// Track order stamped from the given order.
	files, err := s.GetBookFiles(keep)
	require.NoError(t, err)
	tracks := map[string]int{}
	for _, f := range files {
		tracks[f.ID] = f.TrackNumber
		require.Equal(t, 0, f.DiscNumber)
	}
	require.Equal(t, map[string]int{keepFile: 1, srcFile: 2}, tracks)

	_, err = merge.NewService(s).UndoCombine(res.JournalID)
	require.NoError(t, err)

	back, err := s.GetBookFiles(src)
	require.NoError(t, err)
	require.Len(t, back, 1, "undo must move the file back to the source")
	require.Equal(t, 4, back[0].TrackNumber)
	require.Equal(t, 1, back[0].DiscNumber)
	own, err := s.GetBookFiles(keep)
	require.NoError(t, err)
	require.Len(t, own, 1)
	require.Equal(t, 9, own[0].TrackNumber, "undo must restore the keep's own track number")
	srcPos, err := s.ListUserPositionsForBook(u.ID, src)
	require.NoError(t, err)
	require.NotEmpty(t, srcPos, "undo must restore the reader's position on the source")
}

func carryStrP(s string) *string { return &s }

// Metadata only the sources carry is filled onto an EMPTY keep field, never
// over a set one, never over a user lock, and undo empties it again.
func TestMergeSplitBookCluster_FillEmptyAppliedLockedAndUndone(t *testing.T) {
	s := carryStore(t)
	keep, _ := carryBook(t, s, "01 - Tale", "/lib/A/Tale/01 - Tale.mp3", 1)
	src, _ := carryBook(t, s, "02 - Tale", "/lib/A/Tale/02 - Tale.mp3", 2)
	author, err := s.CreateAuthor("Some Writer")
	require.NoError(t, err)
	series, err := s.CreateSeries("Some Saga", &author.ID)
	require.NoError(t, err)
	seq := 3
	_, err = s.ModifyBook(src, func(b *database.Book) error {
		b.ASIN = carryStrP("B0TESTASIN")
		b.Narrator = carryStrP("Some Reader")
		b.SeriesID, b.SeriesSequence = &series.ID, &seq
		b.AuthorID = &author.ID
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, s.SetBookAuthors(src, []database.BookAuthor{{BookID: src, AuthorID: author.ID, Role: "author"}}))
	// The keep's narrator is user-locked while empty: it must stay empty.
	require.NoError(t, s.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: keep, Field: database.FieldKeyNarrator, OverrideLocked: true}))

	res, err := MergeSplitBookClusterWithOptions(s, keep, []string{src}, "", SplitMergeOptions{FillEmpty: true})
	require.NoError(t, err)
	require.Empty(t, res.Errors)

	k, err := s.GetBookByID(keep)
	require.NoError(t, err)
	require.NotNil(t, k.ASIN)
	require.Equal(t, "B0TESTASIN", *k.ASIN)
	require.NotNil(t, k.SeriesID)
	require.Equal(t, series.ID, *k.SeriesID)
	require.NotNil(t, k.AuthorID)
	require.Equal(t, author.ID, *k.AuthorID)
	require.True(t, k.Narrator == nil || *k.Narrator == "", "a locked empty field must stay empty")
	authors, err := s.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Len(t, authors, 1)

	_, err = merge.NewService(s).UndoCombine(res.JournalID)
	require.NoError(t, err)
	k, err = s.GetBookByID(keep)
	require.NoError(t, err)
	require.Nil(t, k.ASIN, "undo must empty the filled ASIN")
	require.Nil(t, k.SeriesID, "undo must empty the filled series")
	require.Nil(t, k.AuthorID, "undo must empty the filled author")
	authors, err = s.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Empty(t, authors)
}

// A value the keep lacks on which two sources disagree is a conflict: the
// merge is refused before any write.
func TestMergeSplitBookCluster_FillEmptyConflictRefuses(t *testing.T) {
	s := carryStore(t)
	keep, _ := carryBook(t, s, "01 - Tale", "/lib/A/Tale/01 - Tale.mp3", 1)
	a, _ := carryBook(t, s, "02 - Tale", "/lib/A/Tale/02 - Tale.mp3", 2)
	b, _ := carryBook(t, s, "03 - Tale", "/lib/A/Tale/03 - Tale.mp3", 3)
	for id, asin := range map[string]string{a: "B0AAAAAAAA", b: "B0BBBBBBBB"} {
		_, err := s.ModifyBook(id, func(bk *database.Book) error { bk.ASIN = carryStrP(asin); return nil })
		require.NoError(t, err)
	}
	_, err := MergeSplitBookClusterWithOptions(s, keep, []string{a, b}, "", SplitMergeOptions{FillEmpty: true})
	require.Error(t, err)
	for _, id := range []string{a, b} {
		bk, gerr := s.GetBookByID(id)
		require.NoError(t, gerr)
		require.False(t, bk.IsSoftDeleted(), "a conflicted merge must write nothing")
	}
}
