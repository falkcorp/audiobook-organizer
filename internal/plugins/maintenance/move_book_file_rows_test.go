// file: internal/plugins/maintenance/move_book_file_rows_test.go
// version: 1.0.1
// guid: ec59dece-f7b5-45f6-83e1-f69b95f45340
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// mbfFixture mirrors the Warforged Sorcerer case: the target owns chapters
// 02-03 in dirA; the source owns chapter 01 from dirA plus its own files in
// dirB.
type mbfFixture struct {
	s                          *database.PebbleStore
	src, dst                   string
	ch01, srcOwn, slantNoTrack string
}

const (
	mbfDirA = "/lib/Kyle Johnson/Singularity Online/Warforged Sorcerer"
	mbfDirB = "/lib/Unknown Author/Warforged Sorcerer"
)

func newMBFFixture(t *testing.T) mbfFixture {
	t.Helper()
	s := newRepairPebble(t)
	f := mbfFixture{s: s, src: "01SRC0000000000000000000AA", dst: "01DST0000000000000000000BB"}
	for _, b := range []*database.Book{
		{ID: f.src, Title: "Warforged Sorcerer", FilePath: mbfDirA},
		{ID: f.dst, Title: "Warforged Sorcerer", FilePath: mbfDirA},
	} {
		_, err := s.CreateBook(b)
		require.NoError(t, err)
	}
	mk := func(id, book, path string, track, dur int, hash string) {
		require.NoError(t, s.CreateBookFile(&database.BookFile{ID: id, BookID: book, FilePath: path,
			TrackNumber: track, Duration: dur, FileSize: int64(dur) * 1000, FileHash: hash}))
	}
	f.ch01 = "01ROW01000000000000000000A"
	f.srcOwn = "01ROWSRC00000000000000000B"
	f.slantNoTrack = "01ROWNOTRK000000000000000C"
	// Chapter 01 sits under the source with track 5 (wrong), in the target's folder.
	mk(f.ch01, f.src, mbfDirA+"/Warforged Sorcerer - 01.mp3", 5, 854, "hash01")
	mk(f.srcOwn, f.src, mbfDirB+"/10 10 - Warforged Sorcerer.mp3", 10, 2330, "hashsrc")
	mk(f.slantNoTrack, f.src, mbfDirA+"/Slant.mp3", 3, 1000, "hashslant")
	mk("01ROWDST02000000000000000D", f.dst, mbfDirA+"/Warforged Sorcerer - 02.mp3", 2, 1684, "hash02")
	mk("01ROWDST03000000000000000E", f.dst, mbfDirA+"/Warforged Sorcerer - 03.mp3", 3, 2069, "hash03")
	return f
}

func mbfDuration(t *testing.T, s *database.PebbleStore, id string) int {
	t.Helper()
	b, err := s.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	if b.Duration == nil {
		return 0
	}
	return *b.Duration
}

func TestMoveBookFileRows_OmittedDryRunPreviews(t *testing.T) {
	f := newMBFFixture(t)
	before := mbfDuration(t, f.s, f.dst)

	rep, err := moveBookFileRows(context.Background(), mbfEnv{store: f.s},
		mbfParams{Moves: []mbfMove{{RowID: f.ch01, ToBookID: f.dst}}}, &fakeReporter{})
	require.NoError(t, err)
	require.True(t, rep.DryRun)
	require.Equal(t, 1, rep.Planned)
	require.Equal(t, 0, rep.Moved)
	r := rep.Moves[0]
	require.Equal(t, mbfStatusPlanned, r.Status)
	require.Equal(t, f.src, r.FromBookID)
	require.Equal(t, 5, r.OldTrack)
	require.Equal(t, 1, r.NewTrack)
	require.True(t, r.SameFolderAsTarget)
	// The fixture's two books share one folder as file_path, as on prod.
	require.Len(t, r.Warnings, 1)
	require.Contains(t, r.Warnings[0], "source book's file_path is this row's folder")

	row, err := f.s.GetBookFileByID(f.src, f.ch01)
	require.NoError(t, err)
	require.NotNil(t, row, "a preview must not move the row")
	require.Equal(t, before, mbfDuration(t, f.s, f.dst))
}

func TestMoveBookFileRows_LiveMovesKeepsHashSetsTrackRecomputesBoth(t *testing.T) {
	f := newMBFFixture(t)
	srcBefore, dstBefore := mbfDuration(t, f.s, f.src), mbfDuration(t, f.s, f.dst)
	require.Equal(t, 854+2330+1000, srcBefore)
	require.Equal(t, 1684+2069, dstBefore)

	live := false
	rep, err := moveBookFileRows(context.Background(), mbfEnv{store: f.s},
		mbfParams{Moves: []mbfMove{{RowID: f.ch01, ToBookID: f.dst}}, DryRunSnake: &live}, &fakeReporter{})
	require.NoError(t, err)
	require.False(t, rep.DryRun)
	require.Equal(t, 1, rep.Moved)
	require.Equal(t, 0, rep.Failed)
	require.Equal(t, mbfStatusMoved, rep.Moves[0].Status)

	gone, err := f.s.GetBookFileByID(f.src, f.ch01)
	require.NoError(t, err)
	require.Nil(t, gone, "row must no longer be under the source")
	row, err := f.s.GetBookFileByID(f.dst, f.ch01)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "hash01", row.FileHash, "the move must keep the row's hash")
	require.Equal(t, 1, row.TrackNumber, "track number comes from the filename")
	require.Equal(t, 854, row.Duration)

	require.Equal(t, srcBefore-854, mbfDuration(t, f.s, f.src))
	require.Equal(t, dstBefore+854, mbfDuration(t, f.s, f.dst))
	require.Len(t, rep.After, 2)

	// Nothing was deleted: the source still has its other two rows.
	srcRows, err := f.s.GetBookFiles(f.src)
	require.NoError(t, err)
	require.Len(t, srcRows, 2)
}

func TestMoveBookFileRows_Refusals(t *testing.T) {
	f := newMBFFixture(t)
	// Each case gets its own row: a row named twice is refused as a duplicate
	// before any other check.
	mkSrc := func(id, name string, track int, pid string) {
		require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: id, BookID: f.src,
			FilePath: mbfDirA + "/" + name, TrackNumber: track, Duration: 10, ITunesPersistentID: pid}))
	}
	itunesRow := "01ROWITUNES00000000000000F"
	mkSrc(itunesRow, "Warforged Sorcerer - 04.mp3", 4, "ABCDEF0123456789")
	collide := "01ROWCOLLIDE0000000000000J"
	mkSrc(collide, "03 - Warforged Sorcerer.mp3", 9, "")
	toGone := "01ROWTOGONE00000000000000K"
	mkSrc(toGone, "Warforged Sorcerer - 05.mp3", 5, "")
	pinnedWrong := "01ROWPINNED00000000000000M"
	mkSrc(pinnedWrong, "Warforged Sorcerer - 06.mp3", 6, "")
	gone := "01GONE00000000000000000GGG"
	yes := true
	_, err := f.s.CreateBook(&database.Book{ID: gone, Title: "Gone", FilePath: "/lib/gone", MarkedForDeletion: &yes})
	require.NoError(t, err)

	live := false
	rep, err := moveBookFileRows(context.Background(), mbfEnv{store: f.s}, mbfParams{Moves: []mbfMove{
		{RowID: f.slantNoTrack, ToBookID: f.dst},
		{RowID: collide, ToBookID: f.dst},
		{RowID: itunesRow, ToBookID: f.dst},
		{RowID: toGone, ToBookID: gone},
		{RowID: f.srcOwn, ToBookID: f.src},
		{RowID: "01NOPE00000000000000000000", ToBookID: f.dst},
		{RowID: pinnedWrong, ToBookID: f.dst, FromBookID: f.dst},
		{RowID: f.ch01, ToBookID: f.dst},
		{RowID: f.ch01, ToBookID: f.dst},
	}, DryRunSnake: &live}, &fakeReporter{})
	require.NoError(t, err)

	want := []struct{ status, reasonPrefix string }{
		{mbfStatusRefused, "no track number in filename"},
		{mbfStatusRefused, "target already has track 3"},
		{mbfStatusRefused, "row carries an iTunes link"},
		{mbfStatusRefused, "target book is soft-deleted"},
		{mbfStatusRefused, "row already belongs to the target book"},
		{mbfStatusRefused, "row not found"},
		{mbfStatusRefused, "row not found under book"},
		{mbfStatusMoved, ""},
		{mbfStatusRefused, "row named more than once"},
	}
	require.Len(t, rep.Moves, len(want))
	for i, w := range want {
		require.Equal(t, w.status, rep.Moves[i].Status, "move %d: %+v", i, rep.Moves[i])
		require.Contains(t, rep.Moves[i].Reason, w.reasonPrefix, "move %d", i)
	}
	require.Equal(t, 1, rep.Moved)
	require.Equal(t, 8, rep.Refused)

	// Refused rows stayed where they were.
	for _, id := range []string{f.slantNoTrack, collide, itunesRow, toGone, f.srcOwn, pinnedWrong} {
		row, err := f.s.GetBookFileByID(f.src, id)
		require.NoError(t, err)
		require.NotNil(t, row, "refused row %s must not move", id)
	}
}

func TestMoveBookFileRows_EmptyMovesAndBadMode(t *testing.T) {
	f := newMBFFixture(t)
	_, err := moveBookFileRows(context.Background(), mbfEnv{store: f.s}, mbfParams{}, &fakeReporter{})
	require.Error(t, err)

	tr, fa := true, false
	_, err = moveBookFileRows(context.Background(), mbfEnv{store: f.s},
		mbfParams{Moves: []mbfMove{{RowID: f.ch01, ToBookID: f.dst}}, DryRunSnake: &fa, DryRun: &tr}, &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
	row, err := f.s.GetBookFileByID(f.src, f.ch01)
	require.NoError(t, err)
	require.NotNil(t, row)
}

func TestMoveBookFileRows_DryRunResolver(t *testing.T) {
	f := false
	got, err := (mbfParams{}).dryRun()
	require.NoError(t, err)
	require.True(t, got, "{} must preview")
	got, err = (mbfParams{DryRunSnake: &f}).dryRun()
	require.NoError(t, err)
	require.False(t, got)
}

func TestTrackFromFilename(t *testing.T) {
	cases := []struct {
		path string
		want int
		ok   bool
	}{
		{"/a/Warforged Sorcerer - 01.mp3", 1, true},
		{"/a/Slant - 87.mp3", 87, true},
		{"/a/01 - Warforged Sorcerer - Kyle Johnson.mp3", 1, true},
		{"/a/1-00-First Search Result.mp3", 0, false},
		{"/a/12.mp3", 12, true},
		{"/a/Slant.mp3", 0, false},
		{"/a/Chapter 00.mp3", 0, false},
		{"/a/Book Two.m4b", 0, false},
	}
	for _, c := range cases {
		got, ok := trackFromFilename(c.path)
		require.Equal(t, c.ok, ok, c.path)
		require.Equal(t, c.want, got, c.path)
	}
}
