// file: internal/plugins/maintenance/series_denumber_safety_test.go
// version: 1.0.0
// guid: 8d3b6e52-9a14-4c70-b2f8-5e1a07c493d6
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The safety rules of PR #4684 round 2: the field locks, the scan stand-down,
// the delete re-check, a journal that voids what it did not write, and the
// property that nothing the op moves is a shape the grammar does not accept.
// Fixtures use the Batman catalogue only.

// addPlaced adds a numbered series holding one book already placed at seq: it
// counts as a sibling (it holds a book) but its book is held, so a test can
// give a series company without adding movers.
func (l *denumberLib) addPlaced(id int, name string, author int, bookID string, seq int) {
	l.addSeries(id, name, author)
	l.addBook(bookID, id, seq, "")
}

// lockBook makes the mock report a lock on field for one book from the nth read
// of that book's metadata state on (n = 1: from the first read).
func (l *denumberLib) lockBook(bookID, field string, fromRead int) *atomic.Int32 {
	var reads atomic.Int32
	inner := l.store.GetMetadataFieldStatesFunc
	l.store.GetMetadataFieldStatesFunc = func(id string) ([]database.MetadataFieldState, error) {
		if id == bookID && int(reads.Add(1)) >= fromRead {
			return []database.MetadataFieldState{{BookID: id, Field: field, OverrideLocked: true}}, nil
		}
		if inner != nil {
			return inner(id)
		}
		return nil, nil
	}
	return &reads
}

// finalChanges is the journal as the store holds it: a row rewritten under its
// id (a void) replaces the earlier one.
func (l *denumberLib) finalChanges() []database.OperationChange {
	idx := map[string]int{}
	var out []database.OperationChange
	for _, c := range l.changes {
		if i, ok := idx[c.ID]; ok {
			out[i] = c
			continue
		}
		idx[c.ID] = len(out)
		out = append(out, c)
	}
	return out
}

func (l *denumberLib) liveJournalRows() []database.OperationChange {
	var out []database.OperationChange
	for _, c := range l.finalChanges() {
		if !c.Voided && c.RevertedAt == nil {
			out = append(out, c)
		}
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// The positive grammar's corroboration, through the op
// ---------------------------------------------------------------------------

// A series with nothing but its own name behind the number is HELD, with the
// reason in the report: nothing is written.
func TestSeriesDenumberSafety_ALoneUncorroboratedSeriesIsHeldWithItsReason(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")

	rep := l.mustRun(`{"apply": true}`)

	l.assertNoWrites()
	if l.seriesOf("owls-5") != 11 || l.seqOf("owls-5") != "none" {
		t.Fatalf("the book moved: series %d position %s", l.seriesOf("owls-5"), l.seqOf("owls-5"))
	}
	if report := readFile(t, l.reportPath); !strings.Contains(report, "position_uncorroborated") {
		t.Errorf("the report does not name the reason:\n%s", report)
	}
	if !strings.Contains(joinLogs(rep), "nothing to apply") {
		t.Errorf("summary %q should say there is nothing to apply", joinLogs(rep))
	}
}

// Two OTHER same-author numbered series that hold a book make a run of three with
// this one, which corroborates the number for a book that has no position.
func TestSeriesDenumberSafety_ASiblingRunCorroboratesABookWithNoPosition(t *testing.T) {
	l := newDenumberLib(t)
	for n, id := range map[int]int{4: 14, 5: 15, 6: 16} {
		l.addSeries(id, fmt.Sprintf("Court of Owls %02d", n), denumberAuthor)
		l.addBook(fmt.Sprintf("owls-%d", n), id, -1, "")
	}

	l.mustRun(`{"apply": true}`)

	base := l.createdID("Court of Owls")
	for n := 4; n <= 6; n++ {
		id := fmt.Sprintf("owls-%d", n)
		if l.seriesOf(id) != base || l.seqOf(id) != strconv.Itoa(n) {
			t.Errorf("%s: series %d position %s, want base %d at %d", id, l.seriesOf(id), l.seqOf(id), base, n)
		}
	}
}

// ---------------------------------------------------------------------------
// M1: the owner's field locks
// ---------------------------------------------------------------------------

func TestSeriesDenumberSafety_ALockedBookIsHeldAtPlanning(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		seq         int
	}{
		{"series_name locked, no position", database.FieldKeySeriesName, -1},
		{"series_name locked, own position matches", database.FieldKeySeriesName, 5},
		{"series_position locked, no position to keep", database.FieldKeySeriesPosition, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newDenumberLib(t)
			l.addSeries(10, "Court of Owls", denumberAuthor)
			l.addSeries(11, "Court of Owls 05", denumberAuthor)
			l.addBook("owls-5", 11, tc.seq, "")
			l.addPlaced(12, "Court of Owls 04", denumberAuthor, "sib-4", 99)
			l.addPlaced(13, "Court of Owls 06", denumberAuthor, "sib-6", 99)
			l.lockBook("owls-5", tc.field, 1)

			l.mustRun(`{"apply": true}`)

			if l.seriesOf("owls-5") != 11 || l.deleted[11] || len(l.liveJournalRows()) != 0 {
				t.Fatalf("a locked book was touched: series %d deleted=%v rows=%v", l.seriesOf("owls-5"), l.deleted[11], l.liveJournalRows())
			}
			if l.indexOf("update:owls-5") >= 0 {
				t.Errorf("the locked book was written: %v", l.events)
			}
			if report := readFile(t, l.reportPath); !strings.Contains(report, "held: series_locked") {
				t.Errorf("the report does not hold the book as series_locked:\n%s", report)
			}
		})
	}
}

// A position lock protects the position only: a book already at its position
// takes no write to it, so the lock does not stop the repoint.
func TestSeriesDenumberSafety_APositionLockDoesNotHoldABookAlreadyAtItsPosition(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.lockBook("owls-5", database.FieldKeySeriesPosition, 1)

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 10 || l.seqOf("owls-5") != "5" {
		t.Fatalf("book in series %d at %s, want base 10 at 5", l.seriesOf("owls-5"), l.seqOf("owls-5"))
	}
}

func TestSeriesDenumberSafety_ABookWhoseLocksCannotBeReadIsHeld(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("metadata state unreadable")
	}

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 11 || l.deleted[11] {
		t.Fatalf("a book whose locks are unreadable was moved: series %d", l.seriesOf("owls-5"))
	}
	if report := readFile(t, l.reportPath); !strings.Contains(report, "held: series_locked") {
		t.Errorf("the report does not name the hold:\n%s", report)
	}
}

// A lock the owner sets AFTER the plan was made still holds the book: it is read
// again inside the write.
func TestSeriesDenumberSafety_ALockSetAfterPlanningHoldsTheBookInsideTheWrite(t *testing.T) {
	for _, field := range []string{database.FieldKeySeriesName, database.FieldKeySeriesPosition} {
		t.Run(field, func(t *testing.T) {
			l := newDenumberLib(t)
			l.addSeries(10, "Court of Owls", denumberAuthor)
			l.addSeries(11, "Court of Owls 05", denumberAuthor)
			l.addBook("owls-5", 11, -1, "")
			l.addPlaced(12, "Court of Owls 04", denumberAuthor, "sib-4", 99)
			l.addPlaced(13, "Court of Owls 06", denumberAuthor, "sib-6", 99)
			// The first read (planning) finds no lock; every later one does.
			reads := l.lockBook("owls-5", field, 2)

			l.mustRun(`{"apply": true}`)

			if reads.Load() < 2 {
				t.Fatalf("the book's locks were read %d times: the write did not read them again", reads.Load())
			}
			if l.seriesOf("owls-5") != 11 || l.indexOf("update:owls-5") >= 0 {
				t.Fatalf("the book was written although it was locked meanwhile: series %d", l.seriesOf("owls-5"))
			}
			if len(l.liveJournalRows()) != 0 || l.deleted[11] {
				t.Errorf("a skipped book left journal rows %v or lost its series (deleted=%v)", l.liveJournalRows(), l.deleted[11])
			}
			if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "series_locked") {
				t.Errorf("the outcome report does not name the lock:\n%s", applied)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// M2: the scan stand-down and the delete re-check
// ---------------------------------------------------------------------------

// sdnScanDeps is the plugin's deps with a scan controller a test steers.
type sdnScanDeps struct {
	*spCacheDeps
	acquireErr         error
	renewOK            bool
	acquired, released atomic.Int32
}

func (d *sdnScanDeps) AcquireScanStandDown(_ context.Context, _, _ string) (func(), error) {
	d.acquired.Add(1)
	if d.acquireErr != nil {
		return func() {}, d.acquireErr
	}
	return func() { d.released.Add(1) }, nil
}
func (d *sdnScanDeps) RenewScanStandDown(string) bool { return d.renewOK }
func (d *sdnScanDeps) ScanStandDownValid(string) bool { return d.renewOK }

func (l *denumberLib) steerScan(d *sdnScanDeps) {
	l.deps = func(base *spCacheDeps) ServerDeps {
		d.spCacheDeps = base
		return d
	}
}

func oneOwlsBook(l *denumberLib) {
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
}

func TestSeriesDenumberSafety_AnApplyStandsTheScanDownAndReleasesIt(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	d := &sdnScanDeps{renewOK: true}
	l.steerScan(d)

	l.mustRun(`{"apply": true}`)

	if d.acquired.Load() != 1 || d.released.Load() != 1 {
		t.Errorf("stand-down acquired %d times, released %d: want 1 and 1", d.acquired.Load(), d.released.Load())
	}
	if l.seriesOf("owls-5") != 10 {
		t.Errorf("the apply did not run: series %d", l.seriesOf("owls-5"))
	}
}

func TestSeriesDenumberSafety_ADryRunTakesNoStandDown(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	d := &sdnScanDeps{renewOK: true}
	l.steerScan(d)

	l.mustRun(`{}`)

	if d.acquired.Load() != 0 {
		t.Errorf("a dry run stood the scanner down %d times", d.acquired.Load())
	}
}

// A scan that will not stand down stops the apply before the library is read.
func TestSeriesDenumberSafety_AnApplyThatCannotStandTheScanDownWritesNothing(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	d := &sdnScanDeps{renewOK: true, acquireErr: errors.New("scan would not park")}
	l.steerScan(d)

	_, err := l.run(`{"apply": true}`)

	if err == nil || !strings.Contains(err.Error(), "stand-down") {
		t.Fatalf("err = %v, want a refusal naming the stand-down", err)
	}
	l.assertNoWrites()
	if l.bulkReads != 0 {
		t.Errorf("the library was read (%d bulk reads) before the refusal", l.bulkReads)
	}
}

// A lease that lapses stops the writes: nothing after the lapse is written.
func TestSeriesDenumberSafety_ALapsedLeaseStopsTheApplyBeforeAnyWrite(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	d := &sdnScanDeps{renewOK: false}
	l.steerScan(d)

	_, err := l.run(`{"apply": true}`)

	if err == nil || !strings.Contains(err.Error(), "lease lapsed") {
		t.Fatalf("err = %v, want the lapsed lease", err)
	}
	l.assertNoWrites()
	if d.released.Load() != 1 {
		t.Errorf("the stand-down was released %d times after the lapse, want 1", d.released.Load())
	}
}

// A book linked into the source between planning and the delete keeps the series:
// the references are counted again right before the delete.
func TestSeriesDenumberSafety_ABookLinkedIntoTheSourceAfterPlanningKeepsIt(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	l.afterMembership = func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		sid := 11
		l.books["intruder"] = &database.Book{ID: "intruder", SeriesID: &sid}
	}

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 10 {
		t.Fatalf("fixture: owls-5 did not move (series %d)", l.seriesOf("owls-5"))
	}
	if l.deleted[11] || len(l.changesOfType(undo.ChangeTypeSeriesRowDelete)) != 0 {
		t.Fatalf("the source was deleted with a book linked into it: events=%v", l.events)
	}
	if l.seriesOf("intruder") != 11 {
		t.Errorf("the intruder book was touched: series %d", l.seriesOf("intruder"))
	}
	if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept:") {
		t.Errorf("the outcome report does not say the series was kept:\n%s", applied)
	}
}

// A count that cannot be taken keeps the series and fails the run.
func TestSeriesDenumberSafety_ADeleteWhoseCountFailsIsHeldAndTheRunFails(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	l.refOverride = func(id int) (int, error) {
		if id == 11 {
			// The first answer is the planning-time read of the whole map (a different
			// call); this one is the per-series re-check.
			return 0, errors.New("count unavailable")
		}
		return 0, nil
	}

	_, err := l.run(`{"apply": true}`)

	if err == nil || !strings.Contains(err.Error(), "writes failed") {
		t.Fatalf("err = %v, want a failed run", err)
	}
	if l.deleted[11] {
		t.Errorf("the series was deleted although its references could not be counted")
	}
}

// A store that cannot count one series' references cannot re-check a delete, so
// the apply is refused before it reads anything.
func TestSeriesDenumberSafety_RefusesToApplyOnAStoreThatCannotRecountASeries(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	p := &Plugin{deps: fakeDeps{store: struct {
		*database.MockStore
		restoreOnly
	}{l.store, restoreOnly{}}}}

	err := p.runSeriesDenumber(context.Background(), json.RawMessage(l.withReport(`{"apply": true}`)), &opIDReporter{id: denumberOpID})

	if err == nil || !strings.Contains(err.Error(), "count one series") {
		t.Fatalf("err = %v, want a refusal naming the missing per-series count", err)
	}
	l.assertNoWrites()
}

type restoreOnly struct{}

func (restoreOnly) RestoreSeries(int, string, *int) error { return nil }

// ---------------------------------------------------------------------------
// M3 and the params
// ---------------------------------------------------------------------------

func TestSeriesDenumberSafety_ApplyMediumIsGone(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls [04]", denumberAuthor)
	l.addBook("owls-4", 11, -1, "")

	_, err := l.run(`{"apply": true, "applyMedium": true}`)

	if err == nil || !strings.Contains(err.Error(), "applyMedium") {
		t.Fatalf("err = %v, want applyMedium rejected as an unknown parameter", err)
	}
	l.assertNoWrites()

	// And medium stays report-only: the bracketed series is listed, not applied.
	l2 := newDenumberLib(t)
	l2.addSeries(11, "Court of Owls [04]", denumberAuthor)
	l2.addBook("owls-4", 11, 4, "")
	l2.mustRun(`{"apply": true}`)
	l2.assertNoWrites()
	if !strings.Contains(readFile(t, l2.reportPath), "Court of Owls [04]") {
		t.Errorf("the report does not list the medium candidate")
	}
}

func TestSeriesDenumberSafety_AnApplyWithoutAReportPathIsRefused(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	p := &Plugin{deps: fakeDeps{store: l.restoring()}}

	err := p.runSeriesDenumber(context.Background(), json.RawMessage(`{"apply": true}`), &opIDReporter{id: denumberOpID})

	if err == nil || !strings.Contains(err.Error(), "reportPath") {
		t.Fatalf("err = %v, want a refusal naming reportPath", err)
	}
	l.assertNoWrites()
	if l.bulkReads != 0 {
		t.Errorf("the library was read before the refusal")
	}
	// A dry run needs none.
	if err := p.runSeriesDenumber(context.Background(), json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Errorf("dry run refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// m1, n2: a run that stops or fails still reports and drops the cache
// ---------------------------------------------------------------------------

func TestSeriesDenumberSafety_ACancelledApplyStillDropsTheCacheAndWritesItsReport(t *testing.T) {
	l := newDenumberLib(t)
	for _, n := range []int{5, 6} {
		l.addSeries(10+n, fmt.Sprintf("Court of Owls %02d", n), denumberAuthor)
		l.addBook(fmt.Sprintf("owls-%d", n), 10+n, n, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := l.store.DeleteSeriesFunc
	l.store.DeleteSeriesFunc = func(id int) error {
		err := inner(id)
		cancel() // after the first plan's delete
		return err
	}

	_, err := l.runCtx(ctx, `{"apply": true}`)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if len(l.deleted) != 1 {
		t.Fatalf("%d series deleted, want exactly the first plan's", len(l.deleted))
	}
	if l.cache.series.Load() == 0 {
		t.Errorf("a cancelled apply that wrote did not drop the series cache")
	}
	applied := readFile(t, l.reportPath+".applied")
	if !strings.Contains(applied, "moved") || !strings.Contains(applied, "deleted") {
		t.Errorf("the outcome report of the cancelled run lacks what it wrote:\n%s", applied)
	}
}

func TestSeriesDenumberSafety_ARunWithAFailedWriteIsNotReportedAsSuccess(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	l.failUpdate["owls-5"] = true

	_, err := l.run(`{"apply": true}`)

	if err == nil || !strings.Contains(err.Error(), "writes failed") {
		t.Fatalf("err = %v, want the failed run reported", err)
	}
	if l.deleted[11] {
		t.Errorf("the source was deleted although its book was not moved")
	}
}

// ---------------------------------------------------------------------------
// m3: the journal rows of a write that did not happen are voided
// ---------------------------------------------------------------------------

func TestSeriesDenumberSafety_AFailedBookWriteVoidsItsJournalRows(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.addPlaced(12, "Court of Owls 04", denumberAuthor, "sib-4", 99)
	l.addPlaced(13, "Court of Owls 06", denumberAuthor, "sib-6", 99)
	l.failUpdate["owls-5"] = true

	_, _ = l.run(`{"apply": true}`)

	var voided int
	for _, c := range l.finalChanges() {
		if c.BookID != "owls-5" {
			continue
		}
		if !c.Voided || c.RevertedAt == nil {
			t.Errorf("a row for a write that failed stands: %+v", c)
		}
		voided++
	}
	if voided != 2 {
		t.Errorf("%d journal rows for the failed book, want its series_id and series_sequence rows both voided", voided)
	}
}

func TestSeriesDenumberSafety_AJournalFailureMidBookVoidsTheRowsAlreadyWritten(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.addPlaced(12, "Court of Owls 04", denumberAuthor, "sib-4", 99)
	l.addPlaced(13, "Court of Owls 06", denumberAuthor, "sib-6", 99)
	inner := l.store.CreateOperationChangeFunc
	l.store.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		if c.FieldName == "series_sequence" && !c.Voided {
			return errors.New("journal unavailable for the second row")
		}
		return inner(c)
	}

	_, _ = l.run(`{"apply": true}`)

	if l.seriesOf("owls-5") != 11 || l.indexOf("update:owls-5") >= 0 {
		t.Fatalf("the book was written although its second journal row failed")
	}
	for _, c := range l.finalChanges() {
		if c.BookID == "owls-5" && !c.Voided {
			t.Errorf("a row of a write that never happened stands: %+v", c)
		}
	}
}

func TestSeriesDenumberSafety_AFailedDeleteVoidsItsUndoRow(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	l.store.DeleteSeriesFunc = func(int) error { return errors.New("delete refused") }

	_, err := l.run(`{"apply": true}`)

	if err == nil {
		t.Fatalf("a failed delete was reported as success")
	}
	rows := 0
	for _, c := range l.finalChanges() {
		if c.ChangeType == undo.ChangeTypeSeriesRowDelete {
			rows++
			if !c.Voided || c.RevertedAt == nil {
				t.Errorf("the undo row of a delete that failed stands: %+v", c)
			}
		}
	}
	if rows != 1 {
		t.Errorf("%d series_row_delete rows, want 1 (voided)", rows)
	}
}

// ---------------------------------------------------------------------------
// m4, m5, m6: the base series
// ---------------------------------------------------------------------------

// CreateSeries resolves-or-creates. A row that came back with books in it was
// made by somebody else: it is not journaled as this run's creation (a revert
// would delete their series) and nothing moves into it.
func TestSeriesDenumberSafety_ABaseThatAlreadyExistedWhenCreatedIsNotJournaledOrUsed(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, 5, "")
	l.store.CreateSeriesFunc = func(name string, authorID *int) (*database.Series, error) {
		return &database.Series{ID: 500, Name: name, AuthorID: authorID}, nil
	}
	l.refOverride = func(id int) (int, error) {
		if id == 500 {
			return 3, nil
		}
		return 0, nil
	}

	rep, _ := l.run(`{"apply": true}`)

	if n := len(l.changesOfType(undo.ChangeTypeSeriesCreate)); n != 0 {
		t.Errorf("%d series_create rows for a row this run did not create", n)
	}
	if l.seriesOf("owls-5") != 11 {
		t.Errorf("the book moved into a row that was not this run's: series %d", l.seriesOf("owls-5"))
	}
	if !strings.Contains(joinLogs(rep), "1 skipped because the base appeared mid-run") {
		t.Errorf("summary %q does not count the skipped plan", joinLogs(rep))
	}
}

// The base is read again inside the write: one deleted since the plan (the
// prune op) takes no book.
func TestSeriesDenumberSafety_ABaseDeletedAfterPlanningTakesNoBook(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	l.afterMembership = func() {
		l.mu.Lock()
		l.deleted[10] = true
		l.mu.Unlock()
	}

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5") != 11 || l.indexOf("update:owls-5") >= 0 {
		t.Fatalf("the book was pointed at a deleted base: series %d", l.seriesOf("owls-5"))
	}
	if len(l.liveJournalRows()) != 0 || l.deleted[11] {
		t.Errorf("a skipped book left journal rows %v or lost its series", l.liveJournalRows())
	}
}

// A base that exists under another author row is never duplicated beside it.
func TestSeriesDenumberSafety_ABaseUnderAnotherAuthorIsNeverDuplicated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		author int // the existing base's author, 0 for none
	}{
		{"another author", denumberAuthor + 1},
		{"no author", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newDenumberLib(t)
			l.addSeries(10, "Court of Owls", tc.author)
			l.addSeries(11, "Court of Owls 05", denumberAuthor)
			l.addBook("owls-5", 11, 5, "")

			l.mustRun(`{"apply": true}`)

			l.assertNoWrites()
			if l.seriesOf("owls-5") != 11 || len(l.created) != 0 {
				t.Fatalf("a second base was created or the book moved: series %d created %v", l.seriesOf("owls-5"), l.created)
			}
			if report := readFile(t, l.reportPath); !strings.Contains(report, "held: base_under_another_author") {
				t.Errorf("the report does not name the hold:\n%s", report)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// m2: minBooks counts books, not versions
// ---------------------------------------------------------------------------

func TestSeriesDenumberSafety_MinBooksCountsABookOnceWhateverItsVersions(t *testing.T) {
	build := func() *denumberLib {
		l := newDenumberLib(t)
		l.addSeries(10, "Court of Owls", denumberAuthor)
		l.addSeries(11, "Court of Owls 05", denumberAuthor)
		l.addBook("owls-5-a", 11, 5, "vg-owls-5")
		l.addBook("owls-5-b", 11, 5, "vg-owls-5")
		return l
	}
	l := build()
	rep := l.mustRun(`{"apply": true, "minBooks": 2}`)
	l.assertNoWrites()
	if !strings.Contains(joinLogs(rep), "nothing to apply") {
		t.Errorf("one book with two versions satisfied minBooks 2: %q", joinLogs(rep))
	}
	l = build()
	l.mustRun(`{"apply": true, "minBooks": 1}`)
	if l.seriesOf("owls-5-a") != 10 || l.seriesOf("owls-5-b") != 10 {
		t.Errorf("minBooks 1 did not move both versions")
	}
}

// ---------------------------------------------------------------------------
// The property: nothing the op moves is a shape the grammar does not accept
// ---------------------------------------------------------------------------

// The oracle is written from the brief's grammar and shares no code with
// seriesdenumber.ParseName or the planner.
var sdnOracle = regexp.MustCompile(`(?i)^\s*(.+?)(?:\s*[-:,\x{2013}\x{2014}]\s*|\s+)(?:(?:book|bk|vol|volume|no)\.?\s*|#\s*)?(\d{1,3})\s*$`)

// sdnOracleBannedAnywhere are the part, format and edition words that hold a name
// wherever they sit; sdnOracleBannedLast are the connectors and volume words that
// may not be the last word of a base (the word right before the number).
var (
	sdnOracleBannedAnywhere = regexp.MustCompile(`(?i)\b(part|pt|disc|cd|edition|unabridged|abridged|omnibus)\b`)
	sdnOracleBannedLast     = regexp.MustCompile(`(?i)(^|\s)(of|to|and|book|bk|vol|volume|no)$|&$`)
)

// oracleParse returns the base and number of a name that satisfies the grammar,
// ok false otherwise. authors are the library's author names: a base that is one
// of them is held.
func oracleParse(name string, authors []string) (base string, n int, ok bool) {
	norm := strings.Join(strings.Fields(name), " ")
	m := sdnOracle.FindStringSubmatch(norm)
	if m == nil {
		return "", 0, false
	}
	n, _ = strconv.Atoi(m[2])
	if n < 1 || n > 99 {
		return "", 0, false
	}
	base = strings.TrimSpace(m[1])
	letters := 0
	for _, r := range base {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if base == "" || letters < 3 || strings.ContainsAny(base, "0123456789") ||
		sdnOracleBannedAnywhere.MatchString(base) || sdnOracleBannedLast.MatchString(base) {
		return "", 0, false
	}
	for _, a := range authors {
		if strings.EqualFold(base, a) {
			return "", 0, false
		}
	}
	return base, n, true
}

func TestSeriesDenumberSafety_PropertyNothingMovesOutsideTheGrammar(t *testing.T) {
	words := []string{"Court", "Owls", "Gotham", "Knights", "Arkham", "Bat", "Of", "Part", "Disc", "Edition", "Vol", "Book", "Bruce Wayne", "9", "2nd", "Unabridged", "&", "and", "to", "BW"}
	tails := []string{"%02d", "%d", "Book %d", "Vol. %d", "Vol %d", "#%d", "No %d", "Part %d", "Disc %d", "[%02d]", "%d of 3", "Book .%d", "%03d", "%d.5", "10%d", "1%d%d", "%d-%d"}
	seps := []string{" ", " - ", ": ", ", ", "-", ""}
	oracleAuthors := []string{"Bruce Wayne", "Alfred Pennyworth"}
	// The first cleanWords and cleanTails entries are shapes the grammar accepts:
	// drawn most of the time, so that enough books reach the move path for the
	// property to say something about it; the rest are the hostile shapes.
	const cleanWords, cleanTails = 6, 6
	rng := rand.New(rand.NewSource(11))
	moved := 0
	for iter := 0; iter < 400; iter++ {
		l := newDenumberLib(t)
		nextID := 100
		bases := []string{"", "", ""}
		for i := range bases {
			var parts []string
			for k, nw := 0, 1+rng.Intn(3); k < nw; k++ {
				if rng.Intn(10) < 7 {
					parts = append(parts, words[rng.Intn(cleanWords)])
				} else {
					parts = append(parts, words[rng.Intn(len(words))])
				}
			}
			bases[i] = strings.Join(parts, " ")
		}
		for i, n := 0, 3+rng.Intn(8); i < n; i++ {
			base := bases[rng.Intn(len(bases))]
			num := 1 + rng.Intn(9)
			tail := tails[rng.Intn(len(tails))]
			if rng.Intn(10) < 6 {
				tail = tails[rng.Intn(cleanTails)]
			}
			var shown string
			switch strings.Count(tail, "%") {
			case 2:
				shown = fmt.Sprintf(tail, num, num+1)
			default:
				shown = fmt.Sprintf(tail, num)
			}
			name := base + seps[rng.Intn(len(seps))] + shown
			author := denumberAuthor + rng.Intn(2)
			seq := -1
			switch rng.Intn(4) {
			case 0, 1:
				seq = num
			case 2:
				seq = 1 + rng.Intn(20)
			}
			l.addSeries(nextID, name, author)
			if rng.Intn(5) != 0 {
				l.addBook(fmt.Sprintf("b%d", nextID), nextID, seq, "")
			}
			nextID++
		}
		orig := map[string]database.Book{}
		origSeries := map[int]database.Series{}
		for _, s := range l.series {
			origSeries[s.ID] = s
		}
		for id, b := range l.books {
			orig[id] = *b
		}
		_, _ = l.run(`{"apply": true}`)

		for id, b := range l.books {
			before := orig[id]
			if before.SeriesID == nil || b.SeriesID == nil || *b.SeriesID == *before.SeriesID {
				continue
			}
			moved++
			src := origSeries[*before.SeriesID]
			base, n, ok := oracleParse(src.Name, oracleAuthors)
			if !ok {
				t.Fatalf("iter %d: book %s moved out of %q, which is not in the grammar", iter, id, src.Name)
			}
			if l.seqOf(id) != strconv.Itoa(n) {
				t.Fatalf("iter %d: book %s from %q is at position %s, want %d", iter, id, src.Name, l.seqOf(id), n)
			}
			var into *database.Series
			for i := range l.series {
				if l.series[i].ID == *b.SeriesID {
					into = &l.series[i]
				}
			}
			if into == nil || normSeriesName(into.Name) != normSeriesName(base) {
				t.Fatalf("iter %d: book %s from %q moved into %+v, want the base %q", iter, id, src.Name, into, base)
			}
			// Corroboration, from the library's original rows: the book's own
			// position was already n, or two other series of the same author with
			// this base and a book make a run of three with n.
			own := before.SeriesSequence != nil && *before.SeriesSequence == n
			if !own {
				nums := map[int]bool{n: true}
				for _, other := range origSeries {
					if other.ID == src.ID || other.AuthorID == nil || src.AuthorID == nil || *other.AuthorID != *src.AuthorID {
						continue
					}
					ob, on, ook := oracleParse(other.Name, oracleAuthors)
					if !ook || normSeriesName(ob) != normSeriesName(base) {
						continue
					}
					for _, bb := range orig {
						if bb.SeriesID != nil && *bb.SeriesID == other.ID {
							nums[on] = true
							break
						}
					}
				}
				run := 1
				for k := n - 1; nums[k]; k-- {
					run++
				}
				for k := n + 1; nums[k]; k++ {
					run++
				}
				if run < 3 {
					t.Fatalf("iter %d: book %s from %q moved with no corroboration (own position %v, run of %d)", iter, id, src.Name, before.SeriesSequence, run)
				}
			}
		}
	}
	if moved < 20 {
		t.Fatalf("only %d books moved across the generated libraries: the generator no longer reaches the move path", moved)
	}
	t.Logf("%d books moved across 400 generated libraries, every one inside the grammar", moved)
}
