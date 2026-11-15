// file: internal/plugins/maintenance/kinds_t6_g4_unknown_rows_test.go
// version: 1.0.0
// guid: d68c7f52-8a70-470d-a54a-4d79de21d579
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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

// requireNoBookIsLeftStale pins what a cancelled run owes. A book that still has
// an unknown row is found again by a rerun, which finishes it and recomputes it,
// so its aggregate may lag meanwhile. A book with measured rows and NO unknown
// row is found by nothing, so its aggregate must already read every measured row
// (each row is wantSec): its recompute ran in the item that finished it. It
// returns how many rows are measured.
func requireNoBookIsLeftStale(t *testing.T, s *database.PebbleStore, rows []database.BookFile, wantSec int) (measured int) {
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
		if c.unknown > 0 || c.known == 0 {
			continue
		}
		book, err := s.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, book.Duration, "book %s has every row measured and a stale aggregate: its recompute was owed past its item, and no rerun will find it", id)
		require.Equal(t, c.known*wantSec, *book.Duration, "book %s", id)
	}
	return measured
}

// The native port of TestUnknownRows_CancelledRunStillRecomputesWrittenBooks.
// HEAD's runner has an ungated store, so its final sweep recomputes after a
// cancel. Natively the recompute is a gated write and is refused once the run
// ends, so no book is left to a sweep: the rows the cancel cut are still
// unknown, and the rerun, which re-plans from the store, finishes the book.
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
	require.Equal(t, 1, requireNoBookIsLeftStale(t, s, rows, 30), "only the row written before the cancel is measured")

	require.NoError(t, t6g4UnknownRowsNative(t, s, 0), "the rerun re-plans from the store and finishes the book")
	require.Equal(t, 3, requireNoBookIsLeftStale(t, s, rows, 30))
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

	// Gated calls in order: write(first row), recompute(first book), write(second
	// row). Ending the run at the third refuses the second row's write.
	_ = t6g4UnknownRowsNative(t, s, 3)
	require.Equal(t, 1, requireNoBookIsLeftStale(t, s, rows, 30), "exactly the first row was written")
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

	// Gated calls in order: write(first borrower), recompute(first borrower),
	// write(second borrower). The third is refused.
	_ = t6g4UnknownRowsNative(t, s, 3)
	require.Equal(t, 1, requireNoBookIsLeftStale(t, s, rows, 30), "exactly the first borrower was written")
}
