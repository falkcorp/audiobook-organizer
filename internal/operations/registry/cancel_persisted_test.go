// file: internal/operations/registry/cancel_persisted_test.go
// version: 1.0.0
// guid: 7e2c1b5a-4d3f-4a9e-8c6b-2f1d0e9a7b3c
// last-edited: 2026-09-10

package registry_test

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestCancel_PersistedNonRunningRowBecomesCanceled pins the fix for "why can't
// I force kill it and remove it from the resume pile".
//
// An op that is NOT running in-process but whose row is still in a live status
// (interrupted_quiesced after a restart, waiting_deps, a zombie "running" row
// with no handle) must be moved to a terminal "canceled" status by Cancel so
// that ListResumableOperationsV2 — the startup sweep's candidate list — never
// hands it back. Before the fix Cancel only knew how to flip a "queued" row
// (SetOperationV2StatusIfQueued) and answered ErrOpNotFound for everything
// else, so the cancel never touched the row and every restart resumed it.
func TestCancel_PersistedNonRunningRowBecomesCanceled(t *testing.T) {
	for _, status := range []string{"interrupted_quiesced", "waiting_deps", "running", "interrupted_ask"} {
		t.Run(status, func(t *testing.T) {
			r, store := newTestRegistry(t)
			def := makeValidDef("test.persisted-cancel")
			def.ResumePolicy = registry.ResumeRestart
			if err := r.RegisterOp(def); err != nil {
				t.Fatalf("RegisterOp: %v", err)
			}
			// The registry is deliberately NOT started: nothing holds a run
			// handle for this row, which is exactly the post-restart shape.
			opID := insertOpV2(store, def.ID, def.Plugin, 0, status, `{"resume_folder_idx":3}`)

			if err := r.Cancel(opID); err != nil {
				t.Fatalf("Cancel(%s row) returned error: %v", status, err)
			}

			row, err := store.GetOperationV2(opID)
			if err != nil {
				t.Fatalf("GetOperationV2: %v", err)
			}
			if row.Status != "canceled" {
				t.Fatalf("status after Cancel = %q, want %q", row.Status, "canceled")
			}
			if row.CompletedAt == nil {
				t.Fatal("CompletedAt is nil after Cancel; the row would still read as in-flight")
			}
			if row.ErrorMessage == nil || *row.ErrorMessage == "" {
				t.Fatal("ErrorMessage is empty after Cancel; the UI has no reason to show for the cancel")
			}

			resumable, err := store.ListResumableOperationsV2()
			if err != nil {
				t.Fatalf("ListResumableOperationsV2: %v", err)
			}
			for _, row := range resumable {
				if row.ID == opID {
					t.Fatalf("canceled op %s is still in the resumable set (status %q); the next boot would resume it", opID, row.Status)
				}
			}
		})
	}
}

// TestCancel_TerminalRowIsNotFound: a row that is already finished has nothing
// to cancel. Cancel reports ErrOpNotFound (the handler maps that to 404) and
// leaves the terminal status alone rather than rewriting history as "canceled".
func TestCancel_TerminalRowIsNotFound(t *testing.T) {
	for _, status := range []string{"completed", "failed", "canceled", "interrupted_dropped"} {
		t.Run(status, func(t *testing.T) {
			r, store := newTestRegistry(t)
			opID := insertOpV2(store, "test.terminal", "test", 0, status, "{}")

			err := r.Cancel(opID)
			if !errors.Is(err, registry.ErrOpNotFound) {
				t.Fatalf("Cancel(%s row) error = %v, want ErrOpNotFound", status, err)
			}
			row, _ := store.GetOperationV2(opID)
			if row.Status != status {
				t.Fatalf("terminal status was rewritten: got %q, want %q", row.Status, status)
			}
		})
	}
}

// TestCancel_UnknownIDIsNotFound keeps the pre-existing contract for an id the
// store has never seen.
func TestCancel_UnknownIDIsNotFound(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Cancel("no-such-op"); !errors.Is(err, registry.ErrOpNotFound) {
		t.Fatalf("Cancel(unknown) error = %v, want ErrOpNotFound", err)
	}
}
