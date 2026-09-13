// file: internal/server/series_membership_hoist_test.go
// version: 1.0.0
// guid: 8c4e2a19-6b3d-4f70-9e15-a2d7c80b3f64
// last-edited: 2026-09-13

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mustSeriesMembers builds the hoisted membership map a caller of
// mergeSeriesGroupHelper passes in, through the same fail-closed entry point
// production uses.
func mustSeriesMembers(t *testing.T, store any, ids ...int) database.SeriesBooksMap {
	t.Helper()
	m, err := database.SeriesMembershipAllVersions(store, ids)
	if err != nil {
		t.Fatalf("SeriesMembershipAllVersions: %v", err)
	}
	return m
}

// TestMergeSeriesGroupHelper_HoistedMapFollowsAChainedMerge merges A into B and
// then B into C through ONE hoisted membership map, the way a normalize pass
// would if a keeper were ever also merged away. After the first merge B holds
// A's books; a map that was not updated still lists B as {B1} only, so the
// second merge repoints one book, the stale-LOW refCounts pass the guard, and
// B is deleted with A1/A2 still on it. members.Move is what prevents that;
// removing it from mergeSeriesGroupHelper fails this test.
//
// The per-series getter is wired to fail: every read must come from the map.
func TestMergeSeriesGroupHelper_HoistedMapFollowsAChainedMerge(t *testing.T) {
	const a, b, c = 10, 20, 30
	seriesOf := map[string]int{"A1": a, "A2": a, "B1": b}

	store := &database.MockStore{}
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		t.Errorf("per-series GetBooksBySeriesIDAllVersions(%d) called; membership must be hoisted", id)
		return nil, nil
	}
	store.GetBooksBySeriesIDsAllVersionsFunc = func(ids []int) (map[int][]database.BookCore, error) {
		out := map[int][]database.BookCore{}
		for _, id := range ids {
			out[id] = nil
		}
		for _, bid := range []string{"A1", "A2", "B1"} {
			sid := seriesOf[bid]
			out[sid] = append(out[sid], database.BookCore{ID: bid, SeriesID: &sid})
		}
		return out, nil
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := seriesOf[id]
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	store.UpdateBookFunc = func(id string, bk *database.Book) (*database.Book, error) {
		seriesOf[id] = *bk.SeriesID
		return bk, nil
	}
	deleted := map[int]bool{}
	store.DeleteSeriesFunc = func(id int) error {
		deleted[id] = true
		return nil
	}

	members := mustSeriesMembers(t, store, a, b, c)
	// Read once up front, as the real pass does -- so stale-LOW for b.
	refCounts := map[int]int{a: 2, b: 1}

	if _, _, err := mergeSeriesGroupHelper(store, b, []int{a}, refCounts, members); err != nil {
		t.Fatalf("merge a->b: %v", err)
	}
	if _, _, err := mergeSeriesGroupHelper(store, c, []int{b}, refCounts, members); err != nil {
		t.Fatalf("merge b->c: %v", err)
	}

	for bid, sid := range seriesOf {
		if deleted[sid] {
			t.Errorf("book %s still holds deleted series %d (stranded by a stale hoisted map)", bid, sid)
		}
		if sid != c {
			t.Errorf("book %s ended on series %d, want %d", bid, sid, c)
		}
	}
}
