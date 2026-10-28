// file: internal/versions/lifecycle_test.go
// version: 1.1.0
// guid: 8f2c6d1a-4b7e-4c93-a5d0-1e9b3f7a2c58
// last-edited: 2026-10-28

package versions

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// promoteNewestAltForTest flips the status of the book's most recently
// ingested alt to active and moves no file. It is a fixture, not a lifecycle
// step: the trash handler used to do this to the book's next version after it
// trashed the active one, which is how a book came to serve the files of a
// trashed version (see BookServesFilesError). Production now swaps first
// (PickSwapTarget, RunVersionSwap); the tests that model the old flow, to check
// the purge-side guards against exactly it, build the state with this.
func promoteNewestAltForTest(store database.Store, bookID string) error {
	all, err := store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return fmt.Errorf("list versions: %w", err)
	}
	var best *database.BookVersion
	for i := range all {
		v := &all[i]
		if v.Status != database.BookVersionStatusAlt {
			continue
		}
		if best == nil || v.IngestDate.After(best.IngestDate) {
			best = v
		}
	}
	if best == nil {
		return nil
	}
	best.Status = database.BookVersionStatusActive
	if err := store.UpdateBookVersion(best); err != nil {
		return fmt.Errorf("promote %s: %w", best.ID, err)
	}
	return nil
}
