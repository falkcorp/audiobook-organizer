// file: internal/plugins/maintenance/series_denumber_review3_apply_test.go
// version: 1.3.0
// guid: 9b3d7e20-4c16-4a58-b1e9-6f0d2a85c734
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The apply-time rules of the PR #4698 round-3 review: the delete reads the row
// again (m2), the base is read again before the write (m10), and the report counts
// every hold (m5, m6, m7). Fixtures are Batman titles and characters only.

// changeSeriesAfterPlanning applies mutate to series id once the plan has been
// made, as another writer would between the plan and the delete.
func (l *denumberLib) changeSeriesAfterPlanning(id int, mutate func(s *database.Series)) {
	l.afterMembership = func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for i := range l.series {
			if l.series[i].ID == id {
				mutate(&l.series[i])
			}
		}
	}
}

// oneRunLib is "Court of Owls 05" (series 11, one book) inside the run 04, 05,
// 06, with no base yet.
func oneRunLib(t *testing.T) *denumberLib {
	t.Helper()
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	return l
}

// ─── m2: the delete is of the row as it is at delete time ───────────────────

func TestSeriesDenumber_Round3_M2_ASourceChangedSincePlanningIsNotDeleted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(s *database.Series)
	}{
		{"renamed", func(s *database.Series) { s.Name = "Court of Owls: The Return" }},
		{"given another author", func(s *database.Series) { a := denumberAuthor + 1; s.AuthorID = &a }},
		{"given no author", func(s *database.Series) { s.AuthorID = nil }},
		{"held by a repair", func(s *database.Series) { s.HeldBy = "series-prune-fixer" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := oneRunLib(t)
			l.changeSeriesAfterPlanning(11, tc.mutate)

			l.mustRun(`{"apply": true}`)

			if l.seriesOf("owls-5") == 11 {
				t.Fatalf("fixture: the book did not move")
			}
			if l.deleted[11] {
				t.Errorf("the series was deleted although it changed since the plan")
			}
			if rows := l.changesOfType(undo.ChangeTypeSeriesRowDelete); len(rows) != 0 {
				t.Errorf("%d series_row_delete rows for a delete that did not happen", len(rows))
			}
			applied := readFile(t, l.reportPath+".applied")
			if !strings.Contains(applied, "kept: the series was renamed, re-authored or held since the plan") {
				t.Errorf("the outcome report does not say why the series was kept:\n%s", applied)
			}
		})
	}
}

// With nothing changed, the delete journals the row it read at delete time: the
// snapshot a revert recreates the series from is the series that was deleted.
func TestSeriesDenumber_Round3_M2_TheDeleteJournalsTheRowReadAtDeleteTime(t *testing.T) {
	l := oneRunLib(t)

	l.mustRun(`{"apply": true}`)

	rows := l.changesOfType(undo.ChangeTypeSeriesRowDelete)
	if len(rows) != 1 || !l.deleted[11] {
		t.Fatalf("%d series_row_delete rows, deleted=%v, want one and the series gone", len(rows), l.deleted[11])
	}
	var snap undo.SeriesSnapshot
	if err := json.Unmarshal([]byte(rows[0].OldValue), &snap); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	if snap.ID != 11 || snap.Name != "Court of Owls 05" || snap.AuthorID == nil || *snap.AuthorID != denumberAuthor {
		t.Errorf("snapshot %+v is not the deleted row", snap)
	}
}

// ─── m10: the base is read again before the first write into it ─────────────

// existingBaseLib is the base series (10) holding one book at position 1, and the
// numbered series "Court of Owls 05" (11) in the run 04, 05, 06.
func existingBaseLib(t *testing.T) *denumberLib {
	t.Helper()
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addBookT("talon-1", 10, 1, "", "Year One")
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	return l
}

func (l *denumberLib) assertOwls5Stayed(want string) {
	l.t.Helper()
	if l.seriesOf("owls-5") != 11 || l.seqOf("owls-5") != "none" {
		l.t.Errorf("the book moved: series %d at %s", l.seriesOf("owls-5"), l.seqOf("owls-5"))
	}
	if l.deleted[11] || len(l.liveJournalRows()) != 0 {
		l.t.Errorf("a skipped plan left a delete (%v) or journal rows %v", l.deleted[11], l.liveJournalRows())
	}
	if applied := readFile(l.t, l.reportPath+".applied"); !strings.Contains(applied, want) {
		l.t.Errorf("the outcome report does not name %q:\n%s", want, applied)
	}
}

func TestSeriesDenumber_Round3_M10_ALiveBookTakingThePositionAfterPlanningSkipsTheMove(t *testing.T) {
	l := existingBaseLib(t)
	l.afterMembership = func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		sid, seq := 10, 5
		l.books["latecomer"] = &database.Book{ID: "latecomer", SeriesID: &sid, SeriesSequence: &seq, Title: "Zero Year"}
	}

	l.mustRun(`{"apply": true}`)

	l.assertOwls5Stayed("skipped: position_collision")
	if l.seriesOf("latecomer") != 10 {
		t.Errorf("the book already in the base was touched: series %d", l.seriesOf("latecomer"))
	}
}

func TestSeriesDenumber_Round3_M10_ATrashedBookTakingThePositionAfterPlanningSkipsTheMove(t *testing.T) {
	l := existingBaseLib(t)
	// The plan reads the library once (bulkReads 1) and the trash after it. The apply
	// reads the base's members again (bulkReads 2) and then its trash: that read, and
	// not the plan's, finds a book restored into position 5 meanwhile.
	inner := l.store.ListSoftDeletedBooksFunc
	l.store.ListSoftDeletedBooksFunc = func(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
		out, err := inner(limit, offset, olderThan)
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.bulkReads >= 2 {
			sid, seq := 10, 5
			out = append(out, database.Book{ID: "restored-later", SeriesID: &sid, SeriesSequence: &seq, Title: "Zero Year"})
			l.extraRefs[10] = 1
		}
		return out, err
	}

	l.mustRun(`{"apply": true}`)

	l.assertOwls5Stayed("skipped: position_collision")
}

func TestSeriesDenumber_Round3_M10_ARowOnlyTheCountSeesAfterPlanningSkipsTheMove(t *testing.T) {
	l := existingBaseLib(t)
	l.afterMembership = func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.extraRefs[10]++ // a row no listing returns: its position is unknown
	}

	l.mustRun(`{"apply": true}`)

	l.assertOwls5Stayed("skipped: target_has_unseen_members")
}

// At planning time, a trashed member holds its position: the decision must see it.
func TestSeriesDenumber_Round3_M10_ATrashedMemberHoldsItsPositionAtPlanning(t *testing.T) {
	l := existingBaseLib(t)
	sid, seq := 10, 5
	l.trashed = append(l.trashed, &database.Book{ID: "trashed-5", SeriesID: &sid, SeriesSequence: &seq, Title: "Zero Year"})
	l.extraRefs[10] = 1

	l.mustRun(`{"apply": true}`)

	l.assertNoWrites()
	if l.seriesOf("owls-5") != 11 {
		t.Errorf("a book moved onto the position a trashed book holds: series %d", l.seriesOf("owls-5"))
	}
	if report := readFile(t, l.reportPath); !strings.Contains(report, "held: position_collision") {
		t.Errorf("the report does not name position_collision:\n%s", report)
	}
}

// ─── m5, m6, m7: what the report holds back, and how it counts ──────────────

// An author id with no author row folds every series of the name into one base
// exactly as no author does: held as no_author.
func TestSeriesDenumber_Round3_M5_AnAuthorIDWithNoAuthorRowIsHeldAsNoAuthor(t *testing.T) {
	const ghost = 77
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 05", ghost)
	l.addBook("owls-5", 11, -1, "")
	l.addRunAround("Court of Owls", ghost, 5, 20)

	l.mustRun(`{"apply": true}`)

	l.assertNoWrites()
	if report := readFile(t, l.reportPath); !strings.Contains(report, "no_author") {
		t.Errorf("the report does not hold the series as no_author:\n%s", report)
	}
}

// A lone unpadded name is listed, not dropped: the summary counts it under its
// code, so it cannot be mistaken for a series the planner never saw.
func TestSeriesDenumber_Round3_M6_ALoneUnpaddedNameIsCountedAsUncorroborated(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Court of Owls 5", denumberAuthor)
	l.addBook("owls-5", 11, -1, "")
	l.addSeries(12, "Dark Knight 3", denumberAuthor)
	l.addBook("dark-3", 12, -1, "")

	rep := l.mustRun(`{}`)

	if !strings.Contains(joinLogs(rep), "position_uncorroborated=2") {
		t.Errorf("the summary does not count the two lone names under position_uncorroborated:\n%s", joinLogs(rep))
	}
}

// The summary names every hold code with its count, and the counts add up to the
// books the plans hold back.
func TestSeriesDenumber_Round3_M7_TheSummaryCountsEveryHoldCode(t *testing.T) {
	l := newDenumberLib(t)
	// source_holds_multiple_books: two distinct books in one numbered series.
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5-a", 11, -1, "")
	l.addBook("owls-5-b", 11, -1, "")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	// own_position_differs.
	l.addSeries(31, "Dark Knight 03", denumberAuthor)
	l.addBook("dark-3", 31, 9, "")
	l.addRunAround("Dark Knight", denumberAuthor, 3, 40)
	// series_locked.
	l.addSeries(51, "Arkham Cell 02", denumberAuthor)
	l.addBook("cell-2", 51, -1, "")
	l.addRunAround("Arkham Cell", denumberAuthor, 2, 60)
	l.lockBook("cell-2", database.FieldKeySeriesName, 1)
	// sibling_run_rejected: three parts with one title.
	for i, n := range []int{1, 2, 3} {
		l.addSeries(71+i, "Mad Hatter 0"+string(rune('0'+n)), denumberAuthor)
		l.addBookT("hatter-"+string(rune('0'+n)), 71+i, -1, "", "Mad Love")
	}

	rep := l.mustRun(`{}`)
	out := joinLogs(rep)

	// own_position_differs is 7: dark-3 and the six siblings the run-arounds add,
	// each already at position 99. 2 + 7 + 1 + 3 = 13, the books of the 12 plans.
	for _, code := range []string{"source_holds_multiple_books=2", "own_position_differs=7", "series_locked=1", "sibling_run_rejected=3"} {
		if !strings.Contains(out, code) {
			t.Errorf("the summary lacks %q:\n%s", code, out)
		}
	}
}

// ─── n5, n7 ─────────────────────────────────────────────────────────────────

// Repeated spaces in the name do not reach the base: it is created as "Court of
// Owls", the name the next lookup finds.
func TestSeriesDenumber_Round3_N5_RepeatedSpacesAreCollapsedInANewBase(t *testing.T) {
	l := newDenumberLib(t)
	for i, n := range []string{"04", "05", "06"} {
		l.addSeries(11+i, "Court  of   Owls "+n, denumberAuthor)
		l.addBook("owls-"+n, 11+i, -1, "")
	}

	l.mustRun(`{"apply": true}`)

	if len(l.created) != 1 || l.created[0].Name != "Court of Owls" {
		t.Fatalf("created %+v, want one base named %q", l.created, "Court of Owls")
	}
}

// A nil scan controller refuses the apply: the shared helper answers "not held"
// with no error, and this op treats that as a refusal because it writes series
// rows library-wide.
func TestSeriesDenumber_Round3_N7_ANilScanControllerRefusesTheApply(t *testing.T) {
	_, err := standDownForApply(context.Background(), nil, &opIDReporter{id: denumberOpID})

	if err == nil || !strings.Contains(err.Error(), "no scan controller") {
		t.Fatalf("err = %v, want a refusal naming the missing scan controller", err)
	}
}

// ─── m1: a lock found late on one version writes none of the book's versions ─

// The primary version is locked after planning. Every version is checked on its
// fresh row before the first is written, so the alternate version is never
// written and taken back: no update, no journal row, not even a voided one.
func TestSeriesDenumber_Round3_M1_ALockFoundOnALaterVersionWritesNoVersionAtAll(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(10, "Court of Owls", denumberAuthor)
	l.addSeries(11, "Court of Owls 05", denumberAuthor)
	l.addBook("owls-5-primary", 11, -1, "vg")
	l.addBook("owls-5-alt", 11, -1, "vg")
	l.addRunAround("Court of Owls", denumberAuthor, 5, 20)
	// The first read of the primary's locks is the plan's; every later one finds the lock.
	l.lockBook("owls-5-primary", database.FieldKeySeriesName, 2)

	l.mustRun(`{"apply": true}`)

	if l.seriesOf("owls-5-alt") != 11 || l.seriesOf("owls-5-primary") != 11 {
		t.Fatalf("a version moved: alt in %d, primary in %d", l.seriesOf("owls-5-alt"), l.seriesOf("owls-5-primary"))
	}
	if l.indexOf("update:owls-5-alt") >= 0 || l.indexOf("update:owls-5-primary") >= 0 {
		t.Errorf("a version was written and taken back instead of never written: %v", l.events)
	}
	if len(l.changes) != 0 {
		t.Errorf("%d journal rows (voided or live) for a book that was never written", len(l.changes))
	}
	if l.deleted[11] {
		t.Errorf("the series was deleted while its book stayed")
	}
}

// ─── m3: the delete is the store's check-and-delete ─────────────────────────

func seriesRowDeleteRows(l *denumberLib) []database.OperationChange {
	var rows []database.OperationChange
	for _, c := range l.finalChanges() {
		if c.ChangeType == undo.ChangeTypeSeriesRowDelete {
			rows = append(rows, c)
		}
	}
	return rows
}

// A book is written into the emptied source AFTER this run's own count and
// before its delete. The op's early count (which sees zero) cannot catch it;
// the store's check-and-delete does, and the series stays, reported as kept. The
// journal row written ahead of the delete is voided, and the run is not a
// failure: a series somebody started using is not an error.
func TestSeriesDenumber_Round3_M3_ABookWrittenIntoTheSourceAtDeleteTimeKeepsTheSeries(t *testing.T) {
	l := oneRunLib(t)
	l.refsAtDelete = func(id int) (int, error) {
		if id == 11 {
			return 1, nil // a book row now names the source
		}
		return 0, nil
	}

	rep := l.mustRun(`{"apply": true}`)

	if l.deleted[11] {
		t.Fatalf("the source was deleted although a book references it at delete time: events=%v", l.events)
	}
	if l.indexOf("delete:11") >= 0 {
		t.Errorf("DeleteSeries ran for a referenced series: events=%v", l.events)
	}
	if l.indexOf("kept-at-delete:11") < 0 {
		t.Errorf("the store's check-and-delete was never asked: events=%v", l.events)
	}
	rows := seriesRowDeleteRows(l)
	if len(rows) != 1 || !rows[0].Voided || rows[0].RevertedAt == nil {
		t.Errorf("want one VOIDED series_row_delete row (written before the delete, voided when it kept the series), got %+v", rows)
	}
	if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept: a book row references it at delete time") {
		t.Errorf("the outcome report does not say the series was kept at delete time:\n%s", applied)
	}
	if !strings.Contains(joinLogs(rep), "1 emptied series NOT deleted") {
		t.Errorf("summary %q does not count the kept series", joinLogs(rep))
	}
	// The move itself stands and is undoable: the book is in the new base.
	if l.seriesOf("owls-5") == 11 {
		t.Errorf("the book did not move out of the numbered series")
	}
}

// The same series, deleted by someone else between this run's read and its
// delete: nothing to delete, not a failure, and no stranded journal row.
func TestSeriesDenumber_Round3_M3_ASourceDeletedByAnotherWriterBeforeTheDeleteIsKept(t *testing.T) {
	l := oneRunLib(t)
	l.refsAtDelete = func(id int) (int, error) {
		l.mu.Lock()
		l.deleted[id] = true // the other writer's delete lands first
		l.mu.Unlock()
		return 0, nil
	}

	l.mustRun(`{"apply": true}`)

	rows := seriesRowDeleteRows(l)
	if len(rows) != 1 || !rows[0].Voided {
		t.Errorf("want one voided series_row_delete row, got %+v", rows)
	}
	if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept: already deleted by someone else") {
		t.Errorf("the outcome report does not say the series was already gone:\n%s", applied)
	}
}

// A store whose delete fails outright is a failed run, the series stays, and the
// row written ahead of the delete is voided (the existing contract, now through
// the check-and-delete).
func TestSeriesDenumber_Round3_M3_AFailingCheckAndDeleteFailsTheRunAndVoidsItsRow(t *testing.T) {
	l := oneRunLib(t)
	l.refsAtDelete = func(int) (int, error) { return 0, errors.New("store unavailable") }

	_, err := l.run(`{"apply": true}`)

	if err == nil {
		t.Fatalf("a failed delete was reported as success")
	}
	if !strings.Contains(err.Error(), "1 emptied series NOT deleted") {
		t.Errorf("the failure summary %q does not count the series that was kept", err)
	}
	rows := seriesRowDeleteRows(l)
	if len(rows) != 1 || !rows[0].Voided {
		t.Errorf("want one voided series_row_delete row, got %+v", rows)
	}
	if l.deleted[11] {
		t.Errorf("the series was deleted although the check-and-delete failed")
	}
	// The failed series is a row in the outcome report, not a silent gap: a
	// degraded memdb makes the store refuse, and the operator reads why.
	if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept: the delete could not verify the series is unreferenced") {
		t.Errorf("the outcome report has no row for the series whose delete failed:\n%s", applied)
	}
}

// mutateSeriesNow changes series id as another writer would at the moment it is
// called (a test calls it from refsAtDelete, which runs after the op's own
// re-read and before the delete).
func (l *denumberLib) mutateSeriesNow(id int, mutate func(s *database.Series)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.series {
		if l.series[i].ID == id {
			mutate(&l.series[i])
		}
	}
}

// The series is renamed or re-authored AFTER the op's own re-read and before the
// delete. The undo row the op journaled holds the old name and author, so a
// delete that went ahead would leave a revert recreating a series nobody has
// any more. The store compares under its lock and refuses; the op keeps the
// series, voids the row, reports why, and does not fail the run.
func TestSeriesDenumber_Round3_M3_ASourceRenamedOrReauthoredAtDeleteTimeIsKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(s *database.Series)
	}{
		{"renamed", func(s *database.Series) { s.Name = "Court of Owls: The Return" }},
		{"given another author", func(s *database.Series) { a := denumberAuthor + 1; s.AuthorID = &a }},
		{"given no author", func(s *database.Series) { s.AuthorID = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := oneRunLib(t)
			l.refsAtDelete = func(id int) (int, error) {
				if id == 11 {
					l.mutateSeriesNow(11, tc.mutate)
				}
				return 0, nil
			}

			rep := l.mustRun(`{"apply": true}`)

			if l.deleted[11] {
				t.Fatalf("the series was deleted although it changed after the op read it: events=%v", l.events)
			}
			rows := seriesRowDeleteRows(l)
			if len(rows) != 1 || !rows[0].Voided {
				t.Errorf("want one VOIDED series_row_delete row, got %+v", rows)
			}
			if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept: the series was renamed or re-authored at delete time") {
				t.Errorf("the outcome report does not say why the series was kept:\n%s", applied)
			}
			if !strings.Contains(joinLogs(rep), "1 emptied series NOT deleted") {
				t.Errorf("summary %q does not count the kept series", joinLogs(rep))
			}
		})
	}
}

// A repair holds the series after the op's re-read and before the delete. The
// store refuses a held row; the op keeps it, voids the row and reports the hold
// as the reason, rather than as a failed delete.
func TestSeriesDenumber_Round3_M3_ASourceHeldAtDeleteTimeIsKeptAsHeld(t *testing.T) {
	l := oneRunLib(t)
	l.refsAtDelete = func(id int) (int, error) {
		if id == 11 {
			l.mutateSeriesNow(11, func(s *database.Series) { s.HeldBy = "series-prune-fixer" })
		}
		return 0, nil
	}

	rep := l.mustRun(`{"apply": true}`)

	if l.deleted[11] {
		t.Fatalf("a held series was deleted: events=%v", l.events)
	}
	rows := seriesRowDeleteRows(l)
	if len(rows) != 1 || !rows[0].Voided {
		t.Errorf("want one VOIDED series_row_delete row, got %+v", rows)
	}
	applied := readFile(t, l.reportPath+".applied")
	if !strings.Contains(applied, "kept: held by a repair at delete time") {
		t.Errorf("the outcome report does not say the series was held:\n%s", applied)
	}
	if strings.Contains(applied, "could not verify") {
		t.Errorf("a hold was reported as a failed check:\n%s", applied)
	}
	if !strings.Contains(joinLogs(rep), "1 emptied series NOT deleted") {
		t.Errorf("summary %q does not count the held series", joinLogs(rep))
	}
}

// A store that cannot do the check-and-delete cannot delete safely, so the
// apply is refused before it reads anything.
func TestSeriesDenumber_Round3_M3_RefusesToApplyOnAStoreWithoutTheCheckAndDelete(t *testing.T) {
	l := oneRunLib(t)
	p := &Plugin{deps: fakeDeps{store: struct {
		*database.MockStore
		restoreOnly
		countOnly
	}{l.store, restoreOnly{}, countOnly{}}}}

	err := p.runSeriesDenumber(context.Background(), json.RawMessage(l.withReport(`{"apply": true}`)), &opIDReporter{id: denumberOpID})

	if err == nil || !strings.Contains(err.Error(), "only if nothing references it") {
		t.Fatalf("err = %v, want a refusal naming the missing check-and-delete", err)
	}
	l.assertNoWrites()
}

type countOnly struct{}

func (countOnly) GetSeriesBookRefCount(int) (int, error) { return 0, nil }
