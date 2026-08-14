// file: internal/maintenance/jobs/normalize_primary_flags_test.go
// version: 1.0.0
// guid: 9f1c4e27-8b6d-4a35-b2e0-7c5d3f8a1e64
// last-edited: 2026-08-14

package jobs

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// normalizeFixture builds a store holding all five flag/group states. Only the
// two "unambiguous ungrouped" rows may ever be written; the grouped rows belong
// to primary ELECTION and a fixture without them cannot catch a job that
// guesses (the census memory's warning: nil, true, and false must all appear).
func normalizeFixture() (*database.MockStore, *map[string]bool, *sync.Mutex) {
	f, tr := false, true
	vg := "01VGROUP"
	books := []database.BookCore{
		{ID: "NIL-UNGROUPED"},
		{ID: "NIL-GROUPED", VersionGroupID: &vg},
		{ID: "FALSE-UNGROUPED", IsPrimaryVersion: &f},
		{ID: "FALSE-GROUPED", IsPrimaryVersion: &f, VersionGroupID: &vg},
		{ID: "TRUE-UNGROUPED", IsPrimaryVersion: &tr},
	}
	written := map[string]bool{}
	var mu sync.Mutex
	m := &database.MockStore{}
	m.GetAllBooksCoreFunc = func(limit, offset int) ([]database.BookCore, error) {
		if limit != 0 || offset != 0 {
			// One consistent snapshot, never offset pages (see #2443).
			panic("normalize-primary-flags must use a single limit-0 read")
		}
		return books, nil
	}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id}, nil
	}
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		if b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
			panic("job wrote a non-true flag: " + id)
		}
		written[id] = true
		return b, nil
	}
	return m, &written, &mu
}

func TestNormalizePrimaryFlags_DryRunWritesNothing(t *testing.T) {
	store, written, _ := normalizeFixture()
	j := &normalizePrimaryFlagsJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(*written) != 0 {
		t.Fatalf("dry-run wrote %v, want none", *written)
	}
}

func TestNormalizePrimaryFlags_WritesOnlyUnambiguousUngrouped(t *testing.T) {
	store, written, _ := normalizeFixture()
	j := &normalizePrimaryFlagsJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	w := *written
	if !w["NIL-UNGROUPED"] || !w["FALSE-UNGROUPED"] || len(w) != 2 {
		t.Fatalf("wrote %v, want exactly {NIL-UNGROUPED, FALSE-UNGROUPED}", w)
	}
	// The keep cases are the load-bearing half: writing a grouped book's flag
	// here could mint a second primary in its version group.
	for _, id := range []string{"NIL-GROUPED", "FALSE-GROUPED", "TRUE-UNGROUPED"} {
		if w[id] {
			t.Fatalf("job wrote %s — grouped/explicit-true rows must be untouched", id)
		}
	}
}
