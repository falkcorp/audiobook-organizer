// file: internal/plugins/maintenance/author_dedup_scan_test.go
// version: 1.0.0
// guid: 3c9e7a21-5b84-4f0d-9a6e-2d71c8f4b053
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// runAuthorDedupScan used to discard GetAllAuthorBookCounts' error with `_`,
// so a failed count ranked every duplicate group against an empty map and the
// op still reported success. It must fail instead.
func TestAuthorDedupScan_FailsWhenBookCountsFail(t *testing.T) {
	countErr := errors.New("count backend unavailable")
	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return []database.Author{{ID: 1, Name: "Brandon Sanderson"}, {ID: 2, Name: "Brandon  Sanderson"}}, nil
		},
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return nil, countErr },
	}
	p := &Plugin{deps: &fakeDeps{store: store}}

	err := p.runAuthorDedupScan(context.Background(), nil, &fakeReporter{})
	if !errors.Is(err, countErr) {
		t.Fatalf("runAuthorDedupScan error = %v, want it to wrap %v", err, countErr)
	}
}

func TestAuthorDedupScan_SucceedsWithCounts(t *testing.T) {
	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return []database.Author{{ID: 1, Name: "Brandon Sanderson"}}, nil
		},
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return map[int]int{1: 3}, nil },
	}
	p := &Plugin{deps: &fakeDeps{store: store}}

	if err := p.runAuthorDedupScan(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("runAuthorDedupScan: %v", err)
	}
}
