// file: internal/plugins/maintenance/series_denumber_apply_test.go
// version: 1.0.0
// guid: 4c93e07a-1d62-4b8e-a5f3-90b7c1de2846
// last-edited: 2026-08-24

package maintenance

import (
	"context"
	"encoding/json"
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
