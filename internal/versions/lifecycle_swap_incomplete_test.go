// file: internal/versions/lifecycle_swap_incomplete_test.go
// version: 1.0.0
// guid: 2c3a5cd5-875d-4b04-9627-713ffb43e692
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// listFailsStore fails only the listing of the versions a swap left unfinished.
type listFailsStore struct{ *database.PebbleStore }

func (s listFailsStore) ListSwappingBookVersions() ([]database.BookVersion, error) {
	return nil, errors.New("injected: list failed")
}

// The nightly cleanup reports the books whose swap did not finish and moves
// nothing for them: the disk and the version statuses are as the swap left them.
func TestCleanupTrashedVersions_ReportsBooksWhoseSwapDidNotFinish(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failFileRow = f.rowOf(alt.ID).ID
	f.swapWith(fs, alt)
	disk := tree(t, f.bookDir)

	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard)

	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if res.SwapIncompleteErr != nil {
		t.Fatalf("SwapIncompleteErr = %v", res.SwapIncompleteErr)
	}
	if len(res.SwapIncomplete) != 1 || res.SwapIncomplete[0].BookID != f.book.ID {
		t.Fatalf("SwapIncomplete = %+v, want the one stuck book", res.SwapIncomplete)
	}
	if got := res.SwapIncomplete[0].Resume; got != SwapResumeFinish {
		t.Errorf("Resume = %q, want %q (refusal %q)", got, SwapResumeFinish, res.SwapIncomplete[0].ResumeRefusal)
	}
	if res.Failed != 0 {
		t.Errorf("Failed = %d: a stuck swap is reported, not a failed purge", res.Failed)
	}
	if got := tree(t, f.bookDir); got != disk {
		t.Errorf("the cleanup changed the disk:\nbefore:\n%s\nafter:\n%s", disk, got)
	}
	if f.status(f.active.ID) != database.BookVersionStatusSwappingOut || f.status(alt.ID) != database.BookVersionStatusSwappingIn {
		t.Errorf("statuses = %s/%s, the cleanup must leave the swap as it found it", f.status(f.active.ID), f.status(alt.ID))
	}
}

func TestCleanupTrashedVersions_NoUnfinishedSwapReportsNone(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")

	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard)

	if err != nil || res.SwapIncompleteErr != nil || len(res.SwapIncomplete) != 0 {
		t.Fatalf("res = %+v err = %v, want nothing to report", res, err)
	}
}

// A listing that fails is reported as its own error, so a night that could not
// look is not mistaken for one with nothing to report; the purges are unaffected.
func TestCleanupTrashedVersions_UnlistableSwapsAreAnError(t *testing.T) {
	f := newSwapFixture(t)

	res, err := cleanupTrashedVersionsAt(listFailsStore{f.store}, time.Now(), testGuard)

	if err != nil {
		t.Fatalf("cleanup err = %v: the purges did not fail", err)
	}
	if res.SwapIncompleteErr == nil {
		t.Fatal("SwapIncompleteErr is nil, want the listing error")
	}
	if len(res.SwapIncomplete) != 0 {
		t.Errorf("SwapIncomplete = %+v, want none", res.SwapIncomplete)
	}
}
