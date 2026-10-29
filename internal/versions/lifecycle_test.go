// file: internal/versions/lifecycle_test.go
// version: 1.2.1
// guid: 8f2c6d1a-4b7e-4c93-a5d0-1e9b3f7a2c58
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// MarkTrashed refuses a version in swapping_out/swapping_in and leaves the
// caller's copy alone: a swap that stopped half way is not undone by trashing the
// version it left (a retried DELETE after RunVersionSwap returned
// ErrSwapIncomplete would do exactly that, and the cleanup would then delete
// files the book plays).
func TestMarkTrashed_RefusesASwappingVersion(t *testing.T) {
	for _, st := range []string{database.BookVersionStatusSwappingOut, database.BookVersionStatusSwappingIn} {
		v := &database.BookVersion{ID: "01BATMANBEGINS", BookID: "book-1", Status: st}
		err := MarkTrashed(v, time.Now())
		if !errors.Is(err, ErrVersionSwapping) {
			t.Fatalf("MarkTrashed(%s) = %v, want ErrVersionSwapping", st, err)
		}
		if !strings.Contains(err.Error(), st) || !strings.Contains(err.Error(), "ResumeVersionSwap") {
			t.Errorf("MarkTrashed(%s) error does not say what to do: %v", st, err)
		}
		if v.Status != st || v.TrashedAt != nil {
			t.Errorf("MarkTrashed changed the caller's copy: status=%s TrashedAt=%v", v.Status, v.TrashedAt)
		}
	}
	// The control: an alt is trashed.
	alt := &database.BookVersion{ID: "01BATMANBEGINS", BookID: "book-1", Status: database.BookVersionStatusAlt}
	if err := MarkTrashed(alt, time.Now()); err != nil || alt.Status != database.BookVersionStatusTrash {
		t.Errorf("MarkTrashed(alt) = %v, status=%s", err, alt.Status)
	}
}
