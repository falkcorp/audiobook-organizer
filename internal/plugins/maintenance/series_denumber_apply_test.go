// file: internal/plugins/maintenance/series_denumber_apply_test.go
// version: 1.2.0
// guid: 4c93e07a-1d62-4b8e-a5f3-90b7c1de2846
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestRunSeriesDenumber_ApplyRepointsNonPrimaryVersions covers the APPLY path,
// which the rest of series_denumber_test.go does not touch -- those tests all
// exercise the pure planner (SeriesDenumber), so reverting the getter used by
// the apply loop left every one of them green.
//
// The op repoints each book of the numbered series onto the base series and then
// deletes the numbered one. It guards that delete with movedAll -- but movedAll
// starts true and is only ever set false INSIDE the loop over the rows the
// getter returned, so a row the getter excluded cannot flip it. Reading the
// filtered listing getter therefore deletes the series with that row still
// pointing at it, and the guard reports success.
//
// Fixture: "Discworld 05" is zero-padded, which the planner treats as
// high-confidence and thus apply-eligible with no applyMedium flag, and an
// existing "Discworld" gives it a base to merge onto.
func TestRunSeriesDenumber_ApplyRepointsNonPrimaryVersions(t *testing.T) {
	const (
		baseID     = 10
		numberedID = 11
		primary    = "dw05-primary"
		nonPrimary = "dw05-alternate-rip"
	)
	authorID := 3

	store := &database.MockStore{}

	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: baseID, Name: "Discworld", AuthorID: &authorID},
			{ID: numberedID, Name: "Discworld 05", AuthorID: &authorID},
		}, nil
	}
	store.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{baseID: 4, numberedID: 1}, nil
	}

	// The two getters DISAGREE -- that disagreement is the whole test. Core is
	// the listing getter and hides the alternate rip; AllVersions does not.
	store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == numberedID {
			return []database.BookCore{{ID: primary}}, nil
		}
		return nil, nil
	}
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == numberedID {
			return []database.BookCore{{ID: primary}, {ID: nonPrimary}}, nil
		}
		return nil, nil
	}

	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := numberedID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}

	repointed := map[string]int{}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			repointed[id] = *b.SeriesID
		}
		return b, nil
	}

	deleted := []int{}
	store.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	p := &Plugin{deps: fakeDeps{store: store}}
	raw := json.RawMessage(`{"apply": true}`)
	if err := p.runSeriesDenumber(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runSeriesDenumber: %v", err)
	}

	// Guard the fixture itself: if the planner stopped treating this shape as
	// eligible, nothing would be applied and the assertions below would pass
	// vacuously against a run that did no work at all.
	if len(deleted) == 0 {
		t.Fatalf("no series deleted -- the plan was not applied, so this test " +
			"proves nothing; check that 'Discworld 05' is still high-confidence")
	}

	if got, ok := repointed[primary]; !ok || got != baseID {
		t.Errorf("primary %s: repointed to %d (present=%v), want %d", primary, got, ok, baseID)
	}

	if got, ok := repointed[nonPrimary]; !ok || got != baseID {
		t.Errorf("non-primary %s was NOT repointed to %d (present=%v, got %d) -- "+
			"series %d was deleted anyway because movedAll only sees rows the "+
			"getter returned, so this row now references a series that does not "+
			"exist", nonPrimary, baseID, ok, got, numberedID)
	}
}

// TestRunSeriesDenumber_RefusesToDeleteSeriesWithOnlyTrashedMembers is the
// SERIES-DENUMBER-TRASHED-GAP regression (TODO.md:2901). It is the sibling of
// the test above, but that fixture leaves at least one row for the getter to
// enumerate. This one leaves ZERO: every member of "Discworld 05" is trashed,
// so GetBooksBySeriesIDAllVersions -- which excludes soft-deleted rows by
// design, same as the getter above -- returns an empty slice. The per-book
// loop that would ever set movedAll=false therefore never runs, movedAll
// stays at its vacuously-true zero value, and on the pre-fix code
// DeleteSeries(numberedID) fires unconditionally even though
// GetAllSeriesBookRefCountsFunc reports 2 rows still referencing it.
//
// Pre-fix failure (captured before the fix landed):
//
//	--- FAIL: TestRunSeriesDenumber_RefusesToDeleteSeriesWithOnlyTrashedMembers (0.00s)
//	    series_denumber_apply_test.go:145: series 11 ("Discworld 05") was
//	        deleted even though the unfiltered reference guard reports 2 rows
//	        still pointing at it -- movedAll was vacuously true because
//	        GetBooksBySeriesIDAllVersions enumerated zero (all trashed) rows
func TestRunSeriesDenumber_RefusesToDeleteSeriesWithOnlyTrashedMembers(t *testing.T) {
	const (
		baseID     = 10
		numberedID = 11
	)
	authorID := 3

	store := &database.MockStore{}

	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: baseID, Name: "Discworld", AuthorID: &authorID},
			{ID: numberedID, Name: "Discworld 05", AuthorID: &authorID},
		}, nil
	}
	// The DISPLAY counter: both series show 0 for the numbered one, because
	// every member is trashed and this counter (like the listing getters)
	// excludes soft-deleted rows. The planner does not filter on Books, so
	// this alone does not stop "Discworld 05" from being a candidate.
	store.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{baseID: 4, numberedID: 0}, nil
	}
	// AllVersions ALSO excludes trashed rows (see its doc comment in
	// pebble_store.go) -- it enumerates zero books for the numbered series,
	// exactly the case that leaves movedAll's loop body unexecuted.
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		return nil, nil
	}
	// The UNFILTERED guard: 2 trashed book rows still hold the numbered
	// series. This is what the pre-fix code never consults before deleting.
	store.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
		return map[int]int{numberedID: 2}, nil
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		t.Fatalf("GetBookByID(%s) called -- no rows should be moved when AllVersions enumerated none", id)
		return nil, nil
	}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		t.Fatalf("UpdateBook(%s) called -- no rows should be moved when AllVersions enumerated none", id)
		return b, nil
	}

	deleted := []int{}
	store.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	p := &Plugin{deps: fakeDeps{store: store}}
	raw := json.RawMessage(`{"apply": true}`)
	if err := p.runSeriesDenumber(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runSeriesDenumber: %v", err)
	}

	for _, id := range deleted {
		if id == numberedID {
			t.Fatalf("series %d (%q) was deleted even though the unfiltered reference guard "+
				"reports 2 rows still pointing at it -- movedAll was vacuously true because "+
				"GetBooksBySeriesIDAllVersions enumerated zero (all trashed) rows",
				numberedID, "Discworld 05")
		}
	}
}

// TestRunSeriesDenumber_FailsClosedWhenGuardCannotAnswer proves the new
// safeToDelete guard fails CLOSED rather than falling back to a permissive
// answer: when the store cannot compute the unfiltered reference count, the
// whole run (apply in this case) must abort with an error instead of
// deleting anything. A silent fallback here is exactly the bug the guard
// exists to prevent, just moved one level up.
func TestRunSeriesDenumber_FailsClosedWhenGuardCannotAnswer(t *testing.T) {
	const (
		baseID     = 10
		numberedID = 11
	)
	authorID := 3

	store := &database.MockStore{}
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: baseID, Name: "Discworld", AuthorID: &authorID},
			{ID: numberedID, Name: "Discworld 05", AuthorID: &authorID},
		}, nil
	}
	store.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{baseID: 4, numberedID: 1}, nil
	}
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == numberedID {
			return []database.BookCore{{ID: "dw05-primary"}}, nil
		}
		return nil, nil
	}
	store.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
		return nil, errors.New("boom: store cannot compute unfiltered series references")
	}
	store.DeleteSeriesFunc = func(id int) error {
		t.Fatalf("DeleteSeries(%d) called -- the run should have aborted before reaching the merge loop", id)
		return nil
	}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		t.Fatalf("UpdateBook(%s) called -- the run should have aborted before reaching the merge loop", id)
		return b, nil
	}

	p := &Plugin{deps: fakeDeps{store: store}}
	raw := json.RawMessage(`{"apply": true}`)
	err := p.runSeriesDenumber(context.Background(), raw, &fakeReporter{})
	if err == nil {
		t.Fatal("runSeriesDenumber: want an error when the unfiltered reference guard cannot answer, got nil")
	}
}

// TestRunSeriesDenumber_DryRunReportsGuardHeldBack proves the dry-run preview
// (built BEFORE the params.Apply branch, same as the apply path) surfaces the
// same held-back set an apply run would produce, rather than reporting a plan
// that apply would then silently deviate from.
func TestRunSeriesDenumber_DryRunReportsGuardHeldBack(t *testing.T) {
	const (
		baseID     = 10
		numberedID = 11
	)
	authorID := 3

	store := &database.MockStore{}
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: baseID, Name: "Discworld", AuthorID: &authorID},
			{ID: numberedID, Name: "Discworld 05", AuthorID: &authorID},
		}, nil
	}
	store.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{baseID: 4, numberedID: 0}, nil
	}
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		return nil, nil
	}
	store.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
		return map[int]int{numberedID: 2}, nil
	}
	store.DeleteSeriesFunc = func(id int) error {
		t.Fatalf("DeleteSeries(%d) called -- this is a dry run (apply omitted)", id)
		return nil
	}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		t.Fatalf("UpdateBook(%s) called -- this is a dry run (apply omitted)", id)
		return b, nil
	}

	reporter := &fakeReporter{}
	p := &Plugin{deps: fakeDeps{store: store}}
	raw := json.RawMessage(`{}`)
	if err := p.runSeriesDenumber(context.Background(), raw, reporter); err != nil {
		t.Fatalf("runSeriesDenumber: %v", err)
	}

	found := false
	for _, l := range reporter.logs {
		if strings.Contains(l, "would be merged but") && strings.Contains(l, "NOT deleted") {
			found = true
			if !strings.Contains(l, "1 would be merged but") {
				t.Errorf("dry-run summary held-back count wrong, got line: %q", l)
			}
		}
	}
	if !found {
		t.Fatalf("dry-run summary did not report the guard-held-back count; logs: %v", reporter.logs)
	}
}
