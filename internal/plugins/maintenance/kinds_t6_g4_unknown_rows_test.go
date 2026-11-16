// file: internal/plugins/maintenance/kinds_t6_g4_unknown_rows_test.go
// version: 1.0.2
// guid: d68c7f52-8a70-470d-a54a-4d79de21d579
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// t6g4FencingWriter is a t6g4Writer that behaves as the run's gated writer does
// once the run's context has ended: every row write and every recompute is
// refused with tasks.ErrFenced (core/tasks/writer.go). before runs ahead of each
// gated call and may end the run, which is how a test cancels a run at an exact
// write.
type t6g4FencingWriter struct {
	t6g4Writer
	fenced func() bool
	before func(method string)
}

func (w t6g4FencingWriter) gate(method string) error {
	if w.before != nil {
		w.before(method)
	}
	if w.fenced() {
		return fmt.Errorf("%s: %w", method, tasks.ErrFenced)
	}
	return nil
}

func (w t6g4FencingWriter) ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if err := w.gate("ModifyBookFileDeferAggregates"); err != nil {
		return nil, err
	}
	return w.t6g4Writer.ModifyBookFileDeferAggregates(bookID, fileID, fn)
}

func (w t6g4FencingWriter) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if err := w.gate("ModifyBookFile"); err != nil {
		return nil, err
	}
	return w.t6g4Writer.ModifyBookFile(bookID, fileID, fn)
}

func (w t6g4FencingWriter) RecomputeBookAggregates(bookID string) error {
	if err := w.gate("RecomputeBookAggregates"); err != nil {
		return err
	}
	return w.t6g4Writer.RecomputeBookAggregates(bookID)
}

// t6g4UnknownRowsNative runs the NATIVE duration-backfill in unknownRows mode
// over s, one worker, behind a fencing writer. cancelOn names the gated call
// (1-based, counted over row writes and recomputes alike) that ends the run
// before it is refused; zero never cancels. It returns the run's error.
func t6g4UnknownRowsNative(t *testing.T, s *database.PebbleStore, cancelOn int) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(fakeDeps{store: s})
	deps := t6g4RealDeps(p, &t6g4Calls{}, new([]tasks.Writer))
	inner := deps.NewWriter
	var calls atomic.Int64
	deps.NewWriter = func(w tasks.Writer) t6g4Writer {
		return t6g4FencingWriter{
			t6g4Writer: inner(w),
			fenced:     func() bool { return ctx.Err() != nil },
			before: func(string) {
				if cancelOn > 0 && int(calls.Add(1)) == cancelOn {
					cancel()
				}
			},
		}
	}
	def := p.durationBackfillKind(deps, nil)
	h := &t6g4Host{FakeRun: taskstest.NewFakeRun(ctx, durationBackfillKind), mode: tasks.ModeLive, w: &t6g4RunWriter{name: "native"}}
	_, err := runT6G4Kind(t, def, tasks.NewRC(h), `{"mode":"unknownRows","workers":1}`)
	return err
}

// requireNoBookIsLeftStale pins what a run owes, in two states.
//
// Right after a CANCELLED run (rerunDone false) a book that still has an unknown
// row may lag: a rerun finds that row and recomputes the book. A book with
// measured rows and NO unknown row is found by nothing, so its aggregate must
// already read every measured row: its recompute ran in the write that finished
// it.
//
// After a COMPLETED rerun (rerunDone true) no book may lag, whether or not it
// still has an unknown row. The recompute is owed by the book's state, so a book
// whose leftover rows all ended unwritten or failed must be recomputed too; a
// helper that skipped books with unknown rows here would hide exactly that. Each
// measured row is wantSec. It returns how many rows are measured.
func requireNoBookIsLeftStale(t *testing.T, s *database.PebbleStore, rows []database.BookFile, wantSec int, rerunDone bool) (measured int) {
	t.Helper()
	type tally struct{ known, unknown int }
	byBook := map[string]*tally{}
	for _, r := range rows {
		c := byBook[r.BookID]
		if c == nil {
			c = &tally{}
			byBook[r.BookID] = c
		}
		if _, ok := urRow(t, s, r.BookID, r.ID).DurationSec(); ok {
			c.known++
			measured++
		} else {
			c.unknown++
		}
	}
	for id, c := range byBook {
		if (c.unknown > 0 && !rerunDone) || c.known == 0 {
			continue
		}
		book, err := s.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, book.Duration, "book %s has measured rows and a stale aggregate: its recompute was owed and never ran", id)
		require.Equal(t, c.known*wantSec, *book.Duration, "book %s", id)
	}
	return measured
}

// The native port of TestUnknownRows_CancelledRunStillRecomputesWrittenBooks.
// HEAD's runner has an ungated store, so its final sweep recomputes after a
// cancel. Natively the recompute is a gated write and is refused once the run
// ends, so the rows the cancel cut are still unknown, and the rerun, which
// re-plans from the store, finishes the book.
//
// This test CANNOT fail on the pre-38b1771b0 code, and is not meant to: its book
// still has an unknown row, so the old end-of-run sweep (which the gate refused)
// was never the only path to a correct total; the rerun's own sweep ran, and the
// book ended right. What changed in 38b1771b0, and later here, is the books with
// NO unknown row left and the books whose leftover rows end unwritten, which the
// tests below pin. This one pins the ordinary case: a cut run plus a rerun
// reaches the right total.
func TestDurationBackfillNative_UnknownRowsCancelledRunLeavesTheRestToARerun(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	var rows []database.BookFile
	for i := 0; i < 3; i++ {
		rows = append(rows, urFile(t, s, book.ID, filepath.Join(dir, fmt.Sprintf("%02d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 }))
	}

	// Gated calls in order: write(row 0), write(row 1). The book still has rows to
	// settle after the first, so no recompute runs between them. Ending the run at
	// the second refuses it.
	require.Error(t, t6g4UnknownRowsNative(t, s, 2), "the run was cancelled")
	require.Equal(t, 1, requireNoBookIsLeftStale(t, s, rows, 30, false), "only the row written before the cancel is measured")

	require.NoError(t, t6g4UnknownRowsNative(t, s, 0), "the rerun re-plans from the store and finishes the book")
	require.Equal(t, 3, requireNoBookIsLeftStale(t, s, rows, 30, true))
	got, err := s.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.Equal(t, 90, *got.Duration)
}

// Two books claim one file, so one group writes both rows. The run ends after
// the first row's write: that book has no unsettled row left, so it is
// recomputed in the item that wrote it, before the cut; the other book's row
// was not written and is still unknown. (A group that settled its rows only
// after the whole loop left the first book with a measured row and a stale
// aggregate that no rerun finds, because the row is no longer unknown.)
func TestDurationBackfillNative_UnknownRowsAGroupCutAfterOneRowStillRecomputesThatBook(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	rows := urSamePath(t, s, "", "")

	// Gated calls in order: write-and-recompute(first row: it finishes its book, so
	// it is one ModifyBookFile), write-and-recompute(second row). Ending the run at
	// the second refuses it.
	_ = t6g4UnknownRowsNative(t, s, 2)
	require.Equal(t, 1, requireNoBookIsLeftStale(t, s, rows, 30, false), "exactly the first row was written")
}

// Books that only borrow a sibling's duration are pass-1 rows. The same rule:
// the book is recomputed by the item that wrote its row, not by a step after
// the pass, which a cancel never reaches.
func TestDurationBackfillNative_UnknownRowsAPassOneBookIsRecomputedInItsOwnItem(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	shared := filepath.Join(dir, "01.mp3")
	donor := urBook(t, s, "Batman: Year One", dir)
	urFile(t, s, donor.ID, shared, func(f *database.BookFile) { f.Duration = database.Secs(30) })
	var rows []database.BookFile
	for i := 0; i < 2; i++ {
		b := urBook(t, s, fmt.Sprintf("Batman: Issue %d", i+1), dir)
		rows = append(rows, urFile(t, s, b.ID, shared, nil))
	}

	// Gated calls in order: write-and-recompute(first borrower), write-and-recompute
	// (second borrower). The second is refused.
	_ = t6g4UnknownRowsNative(t, s, 2)
	require.Equal(t, 1, requireNoBookIsLeftStale(t, s, rows, 30, false), "exactly the first borrower was written")
}

// The recompute is owed by a book's STATE, not by the run's writes. A book with
// rows in both passes is not finished until pass 2 is: a cancel in pass 2 leaves
// its pass-1 rows unreflected in the total. The rerun re-plans and finds only the
// rows the cancel cut; when every one of them ends unwritten (the file is gone) or
// failed (the probe fails), the rerun writes no measured row for the book. A rule
// of "recompute the books this run wrote" never reaches it, and nothing else
// does, so the total stays short for good. HEAD's ungated sweep recomputes it.
//
// On dd5df5afa the final assertions fail: the total still reads 0 after the rerun
// (requireNoBookIsLeftStale wants 60), because the rerun touched no book.
//
// A book's total reads 0, not nil, before anything is measured: creating a row
// recomputes the book, and the recompute stores the sum it found, zero. So the
// precondition compares the VALUE, which is what a stale total is.
func TestDurationBackfillNative_UnknownRowsAMixedBookCutInPassTwoIsRecomputedByARerunThatWritesNothingForIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// leftover turns the cut row into a row the rerun does not measure.
		leftover func(t *testing.T, path string)
	}{
		{"leftover row is unwritten", func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) }},
		{"leftover row fails its probe", func(t *testing.T, _ string) { stubFFprobe(t, "exit 1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubFFprobe(t, "echo 30.0")
			s := newRepairPebble(t)
			dir := t.TempDir()
			shared := filepath.Join(dir, "00-shared.mp3")
			donor := urBook(t, s, "Batman: Year One", dir)
			urFile(t, s, donor.ID, shared, func(f *database.BookFile) { f.Duration = database.Secs(30) })

			// One book, three unknown rows: one borrows the donor's duration (pass 1),
			// two are probed (pass 2).
			book := urBook(t, s, "Batman: The Long Halloween", dir)
			var rows []database.BookFile
			rows = append(rows, urFile(t, s, book.ID, shared, func(f *database.BookFile) { f.TrackNumber = 1 }))
			cut := filepath.Join(dir, "03.mp3")
			rows = append(rows,
				urFile(t, s, book.ID, filepath.Join(dir, "02.mp3"), func(f *database.BookFile) { f.TrackNumber = 2 }),
				urFile(t, s, book.ID, cut, func(f *database.BookFile) { f.TrackNumber = 3 }))

			// Gated calls in order (one worker): write(borrower), write(first probe),
			// write-and-recompute(last probe). Ending the run at the third refuses it.
			require.Error(t, t6g4UnknownRowsNative(t, s, 3), "the run was cut in pass 2")
			require.Equal(t, 2, requireNoBookIsLeftStale(t, s, rows, 30, false), "the borrowed row and the first probed row are written")
			stale, err := s.GetBookByID(book.ID)
			require.NoError(t, err)
			require.Zero(t, durtest.Sec(stale.Duration), "the cut run owes the book a recompute: it has two measured rows and a total that reads none")

			// The rerun's only candidate is the cut row, and it ends without a measurement.
			tc.leftover(t, cut)
			require.NoError(t, t6g4UnknownRowsNative(t, s, 0))
			require.Equal(t, 2, requireNoBookIsLeftStale(t, s, rows, 30, true), "the rerun measured nothing new")
			got, err := s.GetBookByID(book.ID)
			require.NoError(t, err)
			require.NotNil(t, got.Duration)
			require.Equal(t, 60, *got.Duration, "the total reads the two rows the cut run wrote")
		})
	}
}
