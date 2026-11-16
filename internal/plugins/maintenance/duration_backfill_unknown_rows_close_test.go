// file: internal/plugins/maintenance/duration_backfill_unknown_rows_close_test.go
// version: 1.0.0
// guid: 0b6a1f3e-8d52-4c79-9a14-5e7c2d90b3f6
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// urGatedStore is a Pebble store behind the run's gate: once fenced reports true,
// every row write and every recompute is refused with tasks.ErrFenced, as the
// native run's gated writer refuses them after the run ends. It counts the calls
// that get through: recomputes is explicit RecomputeBookAggregates calls (a
// recompute INSIDE ModifyBookFileReportingRecompute is not one), deferred and
// reporting are the two row writes.
type urGatedStore struct {
	*database.PebbleStore
	fenced                          func() bool
	failReporting                   error // when set, the reporting write fails with it (after being counted)
	recomputes, deferred, reporting atomic.Int64
}

func (g *urGatedStore) gate(method string) error {
	if g.fenced != nil && g.fenced() {
		return fmt.Errorf("%s: %w", method, tasks.ErrFenced)
	}
	return nil
}

func (g *urGatedStore) RecomputeBookAggregates(bookID string) error {
	if err := g.gate("RecomputeBookAggregates"); err != nil {
		return err
	}
	g.recomputes.Add(1)
	return g.PebbleStore.RecomputeBookAggregates(bookID)
}

func (g *urGatedStore) ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if err := g.gate("ModifyBookFileDeferAggregates"); err != nil {
		return nil, err
	}
	g.deferred.Add(1)
	return g.PebbleStore.ModifyBookFileDeferAggregates(bookID, fileID, fn)
}

func (g *urGatedStore) ModifyBookFileReportingRecompute(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if err := g.gate("ModifyBookFileReportingRecompute"); err != nil {
		return nil, err
	}
	g.reporting.Add(1)
	if g.failReporting != nil {
		return nil, g.failReporting
	}
	return g.PebbleStore.ModifyBookFileReportingRecompute(bookID, fileID, fn)
}

// urRunBehindTheGate runs the unknownRows walk through the shared runner with
// HEAD's loop runner and a gate that closes the moment the last pool has
// returned: before the walk's final sweep, which therefore cannot rescue a book
// that no item recomputed. (HEAD's own store is ungated, so there the sweep always
// can; behind the native gate it cannot, and the native kind runs this same
// function.) It returns the gated store, the persisted result and the run's error.
func urRunBehindTheGate(t *testing.T, s *database.PebbleStore, failWrites bool) (*urGatedStore, UnknownRowsResult, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &urGatedStore{PebbleStore: s, fenced: func() bool { return ctx.Err() != nil }}
	if failWrites {
		st.failReporting = errors.New("injected disk failure")
	}
	rep := &urReporter{}
	inner := t6g4RunEachHEAD(rep)
	pools := 0
	each := func(ctx context.Context, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error {
		err := inner(ctx, n, o, fn)
		if pools++; pools == 2 { // pass 1 and pass 2 are done; only the sweep is left
			cancel()
		}
		return err
	}
	err := New(fakeDeps{store: s}).t6g4RunUnknownRowsBackfill(ctx, st, st, each,
		func(res UnknownRowsResult) error { return registry.ReporterSetResult(rep, res) },
		durationReextractParams{Mode: durationModeUnknownRows, Workers: 1}, false, rep)
	require.Equal(t, 2, pools, "both passes ran")
	return st, rep.got(t), err
}

// urRequireTotalsMatchRows is the invariant the run owes: every book's stored
// total reads the sum of its measured rows, checked against the store.
func urRequireTotalsMatchRows(t *testing.T, s *database.PebbleStore) {
	t.Helper()
	rows, err := s.GetAllBookFilesCore()
	require.NoError(t, err)
	sums := map[string]int{}
	for _, r := range rows {
		sums[r.BookID] += 0
		if sec, ok := r.DurationSec(); ok {
			sums[r.BookID] += sec
		}
	}
	require.NotEmpty(t, sums)
	for id, want := range sums {
		book, err := s.GetBookByID(id)
		require.NoError(t, err)
		require.Equal(t, want, durtest.Sec(book.Duration), "book %s: the stored total is not the sum of its measured rows", id)
	}
}

// urCloseScenario builds a book that is OWED a recompute by its state and whose
// only unknown row, so the row that closes it, ends in a way that is not a
// measurement. The book has a measured row (C) whose duration was set without the
// recompute, so the stored total is stale (0) before the run, and one unknown row
// (B). The way B ends is what differs.
type urCloseScenario struct {
	name string
	// ffprobe is the stubbed probe script.
	ffprobe string
	// prepare readies B after it exists (remove its file, hook its probe).
	prepare func(t *testing.T, s *database.PebbleStore, b database.BookFile)
	// wantReporting and wantRecomputes are the calls that reach the gate: the
	// closing row is written WITH the recompute (reporting 1) unless it is not
	// written at all or its write is skipped, in which case the book is recomputed
	// by an explicit call (recomputes 1) and, for the skipped write, the attempt
	// still went through the reporting write. deferred is always 0: B is the only
	// row and the closer.
	wantReporting, wantRecomputes int64
	// failWrites makes the closing row's write fail with an error that is not
	// tasks.ErrFenced (the gated run only; the native kind's writer is not
	// injectable here).
	failWrites bool
	// check pins B's outcome in the result.
	check func(t *testing.T, res UnknownRowsResult)
}

var urCloseScenarios = []urCloseScenario{
	{
		// The closing row is left alone (its file is gone): nothing is written for it
		// (settleNoWrite), and the book's recompute is the closing item's.
		name:    "the closing row is left alone",
		ffprobe: "echo 30.0",
		prepare: func(t *testing.T, _ *database.PebbleStore, b database.BookFile) {
			require.NoError(t, os.Remove(b.FilePath))
		},
		check:         func(t *testing.T, res UnknownRowsResult) { require.Equal(t, 1, res.EnvironmentSkipped) },
		wantReporting: 0, wantRecomputes: 1,
	},
	{
		// The closing row's write only records a failed probe: it measures nothing,
		// and the book is still owed its recompute (an earlier measured row), done
		// in the same gated write.
		name:    "the closing row only records a failure",
		ffprobe: "exit 1",
		prepare: func(*testing.T, *database.PebbleStore, database.BookFile) {},
		check: func(t *testing.T, res UnknownRowsResult) {
			require.Equal(t, 1, res.ProbeFailed)
			require.Zero(t, res.ProbedKnown)
		},
		wantReporting: 1, wantRecomputes: 0,
	},
	{
		// The closing row changes while its probe runs, so its write is skipped
		// (wrote false): the recompute the book is owed is an explicit one.
		name:    "the closing write is skipped",
		ffprobe: "echo 30.0",
		prepare: func(t *testing.T, s *database.PebbleStore, b database.BookFile) {
			swapProbe(t, func(_ context.Context, _ string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
				info, err := real()
				_, modErr := s.ModifyBookFileDeferAggregates(b.BookID, b.ID, func(bf *database.BookFile) error { bf.FileHash = "rewritten"; return nil })
				require.NoError(t, modErr) // the row changes after the probe read the file
				return info, err
			})
		},
		check:         func(t *testing.T, res UnknownRowsResult) { require.Equal(t, 1, res.SkippedChanged) },
		wantReporting: 1, wantRecomputes: 1,
	},
	{
		// The closing row's write fails (not fenced): nothing of it is applied, and
		// the book it closes is still recomputed, by an explicit call.
		name:       "the closing write fails without being fenced",
		ffprobe:    "echo 30.0",
		prepare:    func(*testing.T, *database.PebbleStore, database.BookFile) {},
		failWrites: true,
		check: func(t *testing.T, res UnknownRowsResult) {
			require.Equal(t, 1, res.WriteErrors)
			require.Zero(t, res.ProbedKnown)
		},
		wantReporting: 1, wantRecomputes: 1,
	},
}

// urBuildCloseScenario creates the book of a urCloseScenario and returns it.
func urBuildCloseScenario(t *testing.T, s *database.PebbleStore, sc urCloseScenario) database.Book {
	t.Helper()
	stubFFprobe(t, sc.ffprobe)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	c := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
	b := urFile(t, s, book.ID, filepath.Join(dir, "02.mp3"), func(f *database.BookFile) { f.TrackNumber = 2; f.FileHash = "hash-a" })
	// C is measured WITHOUT the recompute: the book's total goes stale.
	_, err := s.ModifyBookFileDeferAggregates(book.ID, c.ID, func(bf *database.BookFile) error { return bf.SetMeasuredDuration(30) })
	require.NoError(t, err)
	stale, err := s.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Zero(t, durtest.Sec(stale.Duration), "the fixture's total starts stale")
	sc.prepare(t, s, b)
	return book
}

// A book owed a recompute by its state, whose last unknown row ends without a
// measurement, is recomputed by the item that settles that row. The walk's final
// sweep is no substitute: behind the run's gate the sweep is refused once the run
// has ended, so a book that no item recomputed stays stale. The gate here closes
// exactly when the sweep would start. Each scenario ends with the stored total
// equal to the sum of the book's measured rows, and the recompute made by the
// call the closer is meant to use (the write itself, or an explicit one).
func TestUnknownRows_ABookOwedARecomputeIsRecomputedByItsClosingRowWhateverItsOutcome(t *testing.T) {
	for _, sc := range urCloseScenarios {
		t.Run(sc.name, func(t *testing.T) {
			s := newRepairPebble(t)
			book := urBuildCloseScenario(t, s, sc)

			st, res, err := urRunBehindTheGate(t, s, sc.failWrites)

			require.NoError(t, err, "the book was recomputed before the gate closed")
			sc.check(t, res)
			require.Equal(t, 1, res.BooksRecomputed)
			require.Equal(t, sc.wantReporting, st.reporting.Load(), "closing writes that carried the recompute")
			require.Equal(t, sc.wantRecomputes, st.recomputes.Load(), "explicit recomputes")
			require.Zero(t, st.deferred.Load(), "the only row is the closer, and a closer never defers when its book is owed a recompute")
			requireAccounted(t, res)
			urRequireTotalsMatchRows(t, s)
			got, err := s.GetBookByID(book.ID)
			require.NoError(t, err)
			require.Equal(t, 30, durtest.Sec(got.Duration))
		})
	}
}

// The native kind runs the same scenarios to the same end state: every book's
// stored total is the sum of its measured rows (the kind's gate is the fencing
// writer of t6g4UnknownRowsNative, which does not close until a test cancels it).
func TestDurationBackfillNative_UnknownRowsABookOwedARecomputeEndsWithItsRowsTotal(t *testing.T) {
	for _, sc := range urCloseScenarios {
		if sc.failWrites {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			s := newRepairPebble(t)
			book := urBuildCloseScenario(t, s, sc)

			require.NoError(t, t6g4UnknownRowsNative(t, s, 0))

			urRequireTotalsMatchRows(t, s)
			got, err := s.GetBookByID(book.ID)
			require.NoError(t, err)
			require.Equal(t, 30, durtest.Sec(got.Duration))
		})
	}
}
