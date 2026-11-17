// file: internal/server/metadata_candidate_retired.go
// version: 1.0.0
// guid: c8e41d7a-5b36-4f92-8a0d-6e1f3b9c2d75
// last-edited: 2026-11-17

package server

import (
	"runtime"

	"golang.org/x/sync/errgroup"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// retiredBookReader is the one read dropRetiredBookIDs needs.
type retiredBookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// dropRetiredBookIDs removes retired books (database.Book.IsRetired: in the
// trash, or merged into another book) from a list of ids the candidate fetch
// was asked to search, and says how many it removed. Order is kept.
//
// A fetch spends provider quota on every id it is handed, and no apply will
// accept the result for a retired book, so the list is cut here rather than
// left to the per-book skip in fetchCandidateForBook (which stays as the
// backstop for a list built earlier). An id the store cannot read is KEPT: this
// is a quota saving, not a gate, and the op re-reads the book.
//
// The list can be library-sized (a selection, a select-all review) and each id
// is a store read, so the reads run on a bounded pool (runtime.NumCPU workers).
// Each worker writes only its own slot of retired, so no lock is needed.
func dropRetiredBookIDs(books retiredBookReader, ids []string) (kept []string, retiredCount int) {
	retired := make([]bool, len(ids))
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for i, id := range ids {
		g.Go(func() error {
			if b, err := books.GetBookByID(id); err == nil && b.IsRetired() {
				retired[i] = true
			}
			return nil
		})
	}
	_ = g.Wait() // the workers never return an error
	kept = make([]string, 0, len(ids))
	for i, id := range ids {
		if retired[i] {
			retiredCount++
			continue
		}
		kept = append(kept, id)
	}
	return kept, retiredCount
}
