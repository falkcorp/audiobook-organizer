// file: internal/playlist/evaluator_passes_test.go
// version: 1.0.0
// guid: 6a3d9e18-b524-4f07-8c61-d0e2f7a49b35
// last-edited: 2026-10-26

package playlist

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/search"
)

func hitsOf(n int) []search.SearchResult {
	out := make([]search.SearchResult, n)
	for i := range out {
		out[i] = search.SearchResult{BookID: string(rune('a' + i%26))}
	}
	return out
}

// One re-query is the normal worst case: documents were indexed between the count
// and the search, and the second pass asks for the total the first one reported.
func TestCollectAllHits_ReasksOnceForTheReportedTotal(t *testing.T) {
	var sizes []int
	ids, err := collectAllHits(3, func(size int) ([]search.SearchResult, uint64, error) {
		sizes = append(sizes, size)
		if len(sizes) == 1 {
			return hitsOf(3), 5, nil
		}
		return hitsOf(5), 5, nil
	})
	if err != nil || len(ids) != 5 {
		t.Fatalf("got %d ids, err %v; want 5 ids", len(ids), err)
	}
	if len(sizes) != 2 || sizes[0] != 3 || sizes[1] != 5 {
		t.Errorf("window sizes asked: %v, want [3 5]", sizes)
	}
}

// An index that grows faster than it is searched cannot hold the caller forever:
// the loop gives up with an error (never a short list passed off as complete).
func TestCollectAllHits_GivesUpOnAnIndexThatKeepsGrowing(t *testing.T) {
	passes := 0
	ids, err := collectAllHits(1, func(size int) ([]search.SearchResult, uint64, error) {
		passes++
		return hitsOf(size), uint64(size) + 1, nil // always one more than the window
	})
	if ids != nil || !errors.Is(err, ErrSearchIndexUnavailable) {
		t.Fatalf("got %d ids, err %v; want no ids and ErrSearchIndexUnavailable", len(ids), err)
	}
	if passes != searchAllIDsMaxPasses {
		t.Errorf("ran %d passes, want %d", passes, searchAllIDsMaxPasses)
	}
}
