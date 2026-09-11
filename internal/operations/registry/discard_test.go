// file: internal/operations/registry/discard_test.go
// version: 1.0.0
// guid: 3b7f1c9e-5d2a-4e8b-9c6f-0a1d2e3f4b5c
// last-edited: 2026-09-10

package registry_test

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestDiscard_RemovesFinishedAndInterruptedRows pins the Activity page's
// Discard button. On 2026-09-10 the button called Cancel; for a row the
// resume sweep had already marked interrupted_dropped that answers
// ErrOpNotFound (nothing to cancel), so the server returned 404 fifteen
// times and the rows stayed. Discard must remove every finished row and
// every interrupted_* row outright.
func TestDiscard_RemovesFinishedAndInterruptedRows(t *testing.T) {
	for _, status := range []string{
		"completed", "failed", "canceled",
		"interrupted_dropped", "interrupted_quiesced", "interrupted_ask", "interrupted_restart",
	} {
		t.Run(status, func(t *testing.T) {
			r, store := newTestRegistry(t)
			def := makeValidDef("test.discard")
			def.ResumePolicy = registry.ResumeRestart
			if err := r.RegisterOp(def); err != nil {
				t.Fatalf("RegisterOp: %v", err)
			}
			opID := insertOpV2(store, def.ID, def.Plugin, 0, status, `{}`)

			if err := r.Discard(opID); err != nil {
				t.Fatalf("Discard(%s row) returned error: %v", status, err)
			}

			if row, err := store.GetOperationV2(opID); err == nil && row != nil {
				t.Fatalf("row still present after Discard: status=%q", row.Status)
			}
			resumable, err := store.ListResumableOperationsV2()
			if err != nil {
				t.Fatalf("ListResumableOperationsV2: %v", err)
			}
			for _, row := range resumable {
				if row.ID == opID {
					t.Fatalf("discarded op %s is still in the resume candidate set", opID)
				}
			}
		})
	}
}

// A row the scheduler still owns is refused with ErrOpActive and left intact:
// deleting a queued row's index entry would silently drop a run the dispatcher
// was about to pick up. So is a status Discard has never heard of — the
// allow-list, not a deny-list, decides.
func TestDiscard_RefusesRowsTheSchedulerStillOwns(t *testing.T) {
	for _, status := range []string{"queued", "running", "waiting_deps", "some_future_status"} {
		t.Run(status, func(t *testing.T) {
			r, store := newTestRegistry(t)
			def := makeValidDef("test.discard-active")
			if err := r.RegisterOp(def); err != nil {
				t.Fatalf("RegisterOp: %v", err)
			}
			opID := insertOpV2(store, def.ID, def.Plugin, 0, status, `{}`)

			err := r.Discard(opID)
			if !errors.Is(err, registry.ErrOpActive) {
				t.Fatalf("Discard(%s row) = %v, want ErrOpActive", status, err)
			}
			row, gerr := store.GetOperationV2(opID)
			if gerr != nil || row == nil {
				t.Fatalf("row was deleted despite the refusal: %v", gerr)
			}
			if row.Status != status {
				t.Fatalf("status changed to %q by a refused Discard", row.Status)
			}
		})
	}
}

func TestDiscard_UnknownIDIsNotFound(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Discard("no-such-op"); !errors.Is(err, registry.ErrOpNotFound) {
		t.Fatalf("Discard(unknown) = %v, want ErrOpNotFound", err)
	}
}
