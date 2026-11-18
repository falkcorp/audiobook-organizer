// file: internal/plugins/maintenance/series_denumber_review3b_apply_test.go
// version: 1.0.0
// guid: 0b7d2e5a-6c41-4f93-8a1e-d35c9f72b406
// last-edited: 2026-11-18

package maintenance

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ─── M2: the op takes part in the dispatcher's write-set gate ───────────────

// The apply moves books between series, creates and deletes series rows. The
// dispatcher serializes ops by their declared write sets
// (registry/writeset_gate_test.go proves the deferral itself): an op that
// declares none bypasses the gate in both directions, so dedup.series-prune,
// series-normalize, series-merge, entities.series-rename, repairs.apply and
// cleanup-series (whose derived effects are books and series) all ran beside it.
func TestSeriesDenumber_Round3b_M2_DeclaresTheBooksAndSeriesWriteSet(t *testing.T) {
	writes := (&Plugin{}).seriesDenumberDef().Writes
	for _, want := range []sdk.Resource{sdk.ResBooks, sdk.ResSeries} {
		if !slices.Contains(writes, want) {
			t.Errorf("maintenance.series-denumber declares Writes %v, want it to include %q so the dispatcher never runs it beside another series writer", writes, want)
		}
	}
}

// Every other op that declares the series resource conflicts with this one under
// the gate's rule (writes intersect), including the repairs apply def of this
// package.
func TestSeriesDenumber_Round3b_M2_ConflictsWithTheOtherSeriesWriters(t *testing.T) {
	mine := (&Plugin{}).seriesDenumberDef().Writes
	other := (&Plugin{}).repairsApplyDef().Writes
	overlap := false
	for _, w := range other {
		if slices.Contains(mine, w) {
			overlap = true
		}
	}
	if !overlap {
		t.Errorf("series-denumber writes %v do not intersect repairs.apply writes %v: the gate would let them run together", mine, other)
	}
}

// ─── m1: a source deleted by someone else leaves the moves revertible ───────

// The source row is already gone when the op re-reads it (before anything is
// journaled for it). The books it moved out are journaled and their series_id
// rows can be reverted only if the series exists again, so the op journals the
// delete from the plan's snapshot of the row, live, and counts it.
func TestSeriesDenumber_Round3b_M1_ASourceAlreadyGoneAtTheRereadStillJournalsItsRow(t *testing.T) {
	l := oneRunLib(t)
	// refOverride runs with the fixture's lock held, so it must not take it.
	l.refOverride = func(id int) (int, error) {
		if id != 11 {
			l.deleted[11] = true // someone deletes the emptied source after the plan
		}
		return 0, nil
	}

	l.mustRun(`{"apply": true}`)

	rows := seriesRowDeleteRows(l)
	if len(rows) != 1 || rows[0].Voided || rows[0].RevertedAt != nil {
		t.Fatalf("want one LIVE series_row_delete row for the source that was already gone, got %+v", rows)
	}
	snap, err := undo.DecodeSeriesRowDelete(&rows[0])
	if err != nil || snap.ID != 11 || snap.Name != "Court of Owls 05" {
		t.Errorf("snapshot = %+v (%v), want the planned row of series 11", snap, err)
	}
	if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept: already deleted by someone else") {
		t.Errorf("the outcome report does not say the series was already gone:\n%s", applied)
	}
}

// Both already-gone shapes are in the summary, as their own count.
func TestSeriesDenumber_Round3b_M1_TheSummaryCountsSeriesDeletedBySomeoneElse(t *testing.T) {
	l := oneRunLib(t)
	l.refsAtDelete = func(id int) (int, error) {
		l.mu.Lock()
		l.deleted[id] = true
		l.mu.Unlock()
		return 0, nil
	}
	rep := l.mustRun(`{"apply": true}`)
	if got := summaryNumber(t, rep, `(\d+) emptied series already deleted by someone else`); got != 1 {
		t.Errorf("summary counts %d series deleted by someone else, want 1", got)
	}
}

// ─── m2 / m3: a failed void, a failed undo row ──────────────────────────────

// failSeriesRowDeleteWrites makes the journal refuse series_row_delete rows:
// voided ones when void is true, the first write of the row otherwise.
func failSeriesRowDeleteWrites(l *denumberLib, void bool) {
	inner := l.store.CreateOperationChangeFunc
	l.store.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		if c.ChangeType == undo.ChangeTypeSeriesRowDelete && c.Voided == void {
			return errors.New("journal unavailable")
		}
		return inner(c)
	}
}

// The series stays (a book references it at delete time) and its undo row cannot
// be voided: a live row now describes a delete that did not happen, so the run
// is failed, not successful with a row a revert would act on.
func TestSeriesDenumber_Round3b_M2_AFailedVoidOfTheDeleteRowFailsTheRun(t *testing.T) {
	l := oneRunLib(t)
	l.refsAtDelete = func(int) (int, error) { return 1, nil }
	failSeriesRowDeleteWrites(l, true)

	_, err := l.run(`{"apply": true}`)

	if err == nil {
		t.Fatalf("a run that left a live undo row for a delete that did not happen was reported as success")
	}
	if !strings.Contains(err.Error(), "failed 1") {
		t.Errorf("the failure summary %q does not count the failed void", err)
	}
}

// The undo row cannot be written, so nothing is deleted. The series stays, the
// run fails, and the report names the series and says why instead of leaving a
// gap; the "NOT deleted" count includes it.
func TestSeriesDenumber_Round3b_M3_AnUndoRowThatCannotBeWrittenIsAReportedKeptSeries(t *testing.T) {
	l := oneRunLib(t)
	failSeriesRowDeleteWrites(l, false)

	_, err := l.run(`{"apply": true}`)

	if err == nil {
		t.Fatalf("a failed undo-row write was reported as success")
	}
	if !strings.Contains(err.Error(), "1 emptied series NOT deleted") {
		t.Errorf("the failure summary %q does not count the series that was kept", err)
	}
	if l.deleted[11] {
		t.Errorf("the series was deleted although its undo row was not written")
	}
	if applied := readFile(t, l.reportPath+".applied"); !strings.Contains(applied, "kept: undo row not written") {
		t.Errorf("the outcome report has no row for the series whose undo row failed:\n%s", applied)
	}
}

// ─── n1: the dry run and the apply reconcile on deletes ─────────────────────

var summaryCounters = map[string]*regexp.Regexp{}

// summaryNumber is the first capture of pattern in the reporter's last summary
// line (or the dry run's description).
func summaryNumber(t *testing.T, rep *fakeReporter, pattern string) int {
	t.Helper()
	re := summaryCounters[pattern]
	if re == nil {
		re = regexp.MustCompile(pattern)
		summaryCounters[pattern] = re
	}
	for _, msg := range slices.Backward(rep.logs) {
		if m := re.FindStringSubmatch(msg); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("summary number %q: %v", m[1], err)
			}
			return n
		}
	}
	t.Fatalf("no log line matches %q:\n%s", pattern, strings.Join(rep.logs, "\n"))
	return 0
}

// The dry run says how many emptied series it would delete, and an apply that
// meets no interference deletes exactly that many.
func TestSeriesDenumber_Round3b_N1_TheDryRunsWouldDeleteMatchesTheApply(t *testing.T) {
	l := oneRunLib(t)

	dry := l.mustRun(`{}`)
	would := summaryNumber(t, dry, `(\d+) emptied series would be deleted`)
	if would != 1 {
		t.Fatalf("dry run would delete %d, want 1", would)
	}

	applied := l.mustRun(`{"apply": true}`)
	if got := summaryNumber(t, applied, `(\d+) emptied series deleted,`); got != would {
		t.Errorf("apply deleted %d, the dry run said %d", got, would)
	}
	if got := summaryNumber(t, applied, `against (\d+) the dry run expected to delete`); got != would {
		t.Errorf("apply summary expects %d, the dry run said %d", got, would)
	}
}

// Whatever happens to a series the dry run expected to delete, the apply puts it
// in exactly one bucket: deleted, kept at delete time, already gone, or not
// emptied (a book stayed). The four add up to the expected count.
func TestSeriesDenumber_Round3b_N1_EveryExpectedDeleteLandsInOneBucket(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(l *denumberLib)
	}{
		{"deleted", func(l *denumberLib) {}},
		{"referenced at delete time", func(l *denumberLib) { l.refsAtDelete = func(int) (int, error) { return 1, nil } }},
		{"undo row not written", func(l *denumberLib) { failSeriesRowDeleteWrites(l, false) }},
		{"gone before the delete", func(l *denumberLib) {
			l.refsAtDelete = func(id int) (int, error) {
				l.mu.Lock()
				l.deleted[id] = true
				l.mu.Unlock()
				return 0, nil
			}
		}},
		{"gone before the re-read", func(l *denumberLib) {
			l.refOverride = func(id int) (int, error) {
				if id != 11 { // runs with the fixture's lock held
					l.deleted[11] = true
				}
				return 0, nil
			}
		}},
		{"a book did not move", func(l *denumberLib) { l.failUpdate["owls-5"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := oneRunLib(t)
			tc.setup(l)
			rep, _ := l.run(`{"apply": true}`) // some of these fail the run; the summary is what is read
			var sum int
			for _, pat := range []string{
				`(\d+) emptied series deleted,`,
				`(\d+) emptied series NOT deleted because`,
				`(\d+) emptied series already deleted by someone else`,
				`(\d+) sources? not emptied`,
			} {
				sum += summaryNumber(t, rep, pat)
			}
			if want := summaryNumber(t, rep, `against (\d+) the dry run expected to delete`); sum != want || want != 1 {
				t.Errorf("buckets add up to %d against %d expected (want 1 and 1)", sum, want)
			}
		})
	}
}

// A source the unfiltered reference guard holds (rows this run cannot move still
// name it) is NOT an expected delete: the dry run counts it under "would be
// merged but NOT deleted", not under "would be deleted", and the apply neither
// expects it nor counts it among the series kept at delete time, because it
// never reaches the delete.
func TestSeriesDenumber_Round3b_N1_AGuardHeldSourceIsNotAnExpectedDelete(t *testing.T) {
	l := oneRunLib(t)
	l.extraRefs[11] = 2

	dry := l.mustRun(`{}`)
	if got := summaryNumber(t, dry, `(\d+) emptied series would be deleted`); got != 0 {
		t.Errorf("dry run would delete %d, want 0 (the guard holds the source)", got)
	}
	if got := summaryNumber(t, dry, `(\d+) would be merged but NOT deleted`); got != 1 {
		t.Errorf("dry run counts %d guard-held sources, want 1", got)
	}

	applied := l.mustRun(`{"apply": true}`)
	if got := summaryNumber(t, applied, `against (\d+) the dry run expected to delete`); got != 0 {
		t.Errorf("apply expected %d deletes, the dry run said 0", got)
	}
	if got := summaryNumber(t, applied, `(\d+) emptied series NOT deleted because`); got != 0 {
		t.Errorf("apply counts %d series kept at delete time, want 0: the guard-held source never reached the delete", got)
	}
	if got := summaryNumber(t, applied, `(\d+) plans' sources kept by the reference guard`); got != 1 {
		t.Errorf("apply counts %d guard-held sources, want 1", got)
	}
	if len(seriesRowDeleteRows(l)) != 0 || l.deleted[11] {
		t.Errorf("a guard-held source was journaled or deleted")
	}
}
