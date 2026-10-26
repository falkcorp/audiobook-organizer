// file: internal/server/handlers/abs/collections_bounds_test.go
// version: 1.0.0
// guid: 2f8a6c41-9d03-4e57-b1a8-7c5e0d3f6b92
// last-edited: 2026-10-26

package abs_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// A dynamic collection can match the whole library, and the ABS surface builds a
// full library item for every member it serves. The expansion is cut to
// database.MaxMaterializedMembers; the served count stays the length of the served
// list (numBooks == len(books), the series-route lesson in collectionDTO) and the
// real count is reported separately as totalBooks.

// ghostIDs are member ids that name no book: they cost an id and are dropped on
// expansion, so a list of them is a cheap way to fill a collection past the bound.
func ghostIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("ghost-%05d", i)
	}
	return out
}

func withFixedViewerMembers(sets map[string][]string) harnessOpt {
	return func(o *abshandler.Options) {
		o.CollectionViewerMembers = func(_ context.Context, _ []database.Collection, _ string) (map[string][]string, error) {
			return sets, nil
		}
	}
}

func TestCollections_ABSExpansionOfADynamicCollectionIsCutAndReportsTheTotal(t *testing.T) {
	f := newViewerFixture(t)
	over := database.MaxMaterializedMembers + 300

	// A real book first, a real book PAST the bound: only the first is served.
	viewerList := append([]string{f.seed.singleID}, ghostIDs(over-2)...)
	viewerList = append(viewerList, f.seed.multiID)
	if len(viewerList) != over {
		t.Fatalf("fixture has %d ids, want %d", len(viewerList), over)
	}
	f.store.cols = append(f.store.cols,
		database.Collection{ID: "pv", Name: "The Long Halloween", Type: database.CollectionTypeDynamic, Query: "read_status:unstarted"},
		// A stored list that was already cut when it was saved says so on the row.
		database.Collection{ID: "stored", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:single",
			MaterializedBookIDs: append([]string{f.seed.singleID}, ghostIDs(database.MaxMaterializedMembers-1)...),
			MaterializedTotal:   12000},
		database.Collection{ID: "small", Name: "Year One", Type: database.CollectionTypeDynamic, Query: "title:single",
			MaterializedBookIDs: []string{f.seed.singleID}},
	)
	h := newHarness(t, "jwt", nil, withLibrary(f.seed), withUserData(fixtureUserData()),
		withCollections(f.store), withFixedViewerMembers(map[string][]string{"pv": viewerList}))
	h.seedUser(t, "alice", "alice", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "alice", "pw-pw-pw-pw")), "accessToken")

	rows := abscolResults(t, h, tok)

	pv := rowNamed(t, rows, "The Long Halloween")
	if got := bookIDsOf(pv); len(got) != 1 {
		t.Errorf("per-viewer: served %d books, want 1 (the real book past the bound is cut)", len(got))
	}
	if n, _ := pv["numBooks"].(float64); int(n) != len(bookIDsOf(pv)) {
		t.Errorf("per-viewer: numBooks = %v but %d books are served", pv["numBooks"], len(bookIDsOf(pv)))
	}
	if total, _ := pv["totalBooks"].(float64); int(total) != over {
		t.Errorf("per-viewer: totalBooks = %v, want %d", pv["totalBooks"], over)
	}

	stored := rowNamed(t, rows, "Hush")
	if total, _ := stored["totalBooks"].(float64); int(total) != 12000 {
		t.Errorf("stored cut list: totalBooks = %v, want the row's MaterializedTotal 12000", stored["totalBooks"])
	}

	small := rowNamed(t, rows, "Year One")
	if _, has := small["totalBooks"]; has {
		t.Errorf("a collection that was not cut reports no totalBooks, got %v", small["totalBooks"])
	}
	if n, _ := small["numBooks"].(float64); int(n) != 1 {
		t.Errorf("small: numBooks = %v, want 1", small["numBooks"])
	}
}
