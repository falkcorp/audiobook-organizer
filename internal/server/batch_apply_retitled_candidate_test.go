// file: internal/server/batch_apply_retitled_candidate_test.go
// version: 1.0.0
// guid: b3e71d58-4a02-4f96-8c1d-5e90a2f7c643
// last-edited: 2026-10-10

package server

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
)

// A retitle used to delete the book's cache row; it now keeps it, marked
// Stale. The bulk-apply planner (the dry run's per-book decision) must refuse
// the kept candidate as identity_stale, the candidate fetch must select the
// book, and the refetch must replace the row with a current one.
func TestBulkApply_RetitledCandidateRefusedIdentityStale(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.audible.answer = answersTitle("Title 000200", "Author 07")
	b := f.book(t, "Title 000200")

	first := f.run(t, b.ID)[b.ID]
	if first.Candidate == nil {
		t.Fatalf("fixture: the first fetch must cache a candidate, got status %q", first.Status)
	}
	row, err := f.store.GetMetadataCache(b.ID)
	if err != nil || row == nil || len(row.Candidates) == 0 || row.Stale {
		t.Fatalf("fixture: want a fresh row with candidates, got %+v (%v)", row, err)
	}
	control := planCachedApply(f.mfs, f.store, b.ID, nil, nil)
	if control.Gate != nil && control.Gate.Reason == applygate.ReasonIdentityStale {
		t.Fatalf("control: an unchanged book must not read as identity_stale: %+v", control.Gate)
	}

	// Retitle the book. The row must survive, marked stale.
	cur, err := f.store.GetBookByID(b.ID)
	if err != nil || cur == nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	cur.Title = "Title 000201"
	if _, err := f.store.UpdateBook(b.ID, cur); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
	row, err = f.store.GetMetadataCache(b.ID)
	if err != nil || row == nil || !row.Stale {
		t.Fatalf("the retitle must keep the row marked Stale, got %+v (%v)", row, err)
	}

	refused, applied := 0, 0
	plan := planCachedApply(f.mfs, f.store, b.ID, nil, nil)
	switch {
	case plan.Reason == applySkipGateBlocked && plan.Gate != nil && plan.Gate.Reason == applygate.ReasonIdentityStale:
		refused++
	case plan.Reason == "":
		applied++
	}
	t.Logf("retitled books: identity_stale=%d applied=%d", refused, applied)
	if refused != 1 || applied != 0 {
		t.Fatalf("plan = %q gate %+v; want 1 refused as identity_stale and 0 applied", plan.Reason, plan.Gate)
	}

	// The candidate fetch selects it for refetch.
	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 0)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !slices.Contains(sel.IDs, b.ID) {
		t.Fatalf("selected %v; want the retitled book %s", sel.IDs, b.ID)
	}

	// The refetch replaces the row with a current one.
	f.resetCalls()
	f.run(t, sel.IDs...)
	if f.audible.calls.Load() == 0 {
		t.Fatal("the refetch asked no provider")
	}
	row, err = f.store.GetMetadataCache(b.ID)
	if err != nil || row == nil || row.Stale {
		t.Fatalf("the refetch must replace the row and clear Stale, got %+v (%v)", row, err)
	}
}
