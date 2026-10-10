// file: internal/operations/registry/fence_binding_test.go
// version: 1.0.0
// guid: 7d2a4c1e-9b63-4f08-a5d1-3e6c8b0f2a97
// last-edited: 2026-10-10

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestFence_ExecuteRunBindsTheFenceToTheRunContext pins the call site in
// executeRun (worker.go: newFence(1, runCtx.Done())), which the unit test of
// newFence alone does not. The fence a run reads from its context must watch
// THAT context's Done channel, so a def Timeout or a parent-context cancel
// fences the run at the instant the context ends, before executeRun's select
// gets to revoke. Building the fence unbound (newFence(1, nil)), or before the
// timeout layer, leaves only the revoke and reopens that window.
//
// The probe compares channels rather than calling Check() after ctx.Done():
// that races the revoke in executeRun's select and passes either way.
//
// Mutation check: change worker.go's newFence(1, runCtx.Done()) to
// newFence(1, nil) (or to the parent context's Done) and this fails with
// "the run's fence is not bound to the run context".
func TestFence_ExecuteRunBindsTheFenceToTheRunContext(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
	}{
		{name: "def timeout layer", timeout: 20 * time.Millisecond},
		{name: "default timeout", timeout: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			r := registry.New(store, slog.Default(), 2, nil)

			type probe struct{ bound, hasFence bool }
			got := make(chan probe, 1)
			def := makeValidDef("test.fence.binding")
			def.Timeout = tc.timeout
			def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
				f := registry.FenceFromContext(ctx)
				got <- probe{
					bound:    f.BoundToForTest(ctx.Done()),
					hasFence: f.Epoch() != 0,
				}
				if tc.timeout > 0 {
					<-ctx.Done() // run into the def timeout
				}
				return nil
			}
			if err := r.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			r.Start(t.Context())

			id, err := r.EnqueueOp(t.Context(), def.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case p := <-got:
				if !p.hasFence {
					t.Fatal("the run context carries no fence")
				}
				if !p.bound {
					t.Fatal("the run's fence is not bound to the run context's Done channel: " +
						"a timeout or parent cancel would not fence the run until executeRun's revoke")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the run never started")
			}
			awaitTerminal(t, store, id)
		})
	}
}

// awaitTerminal waits for opID's row to leave queued/running.
func awaitTerminal(t *testing.T, store *fakeStore, opID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		switch store.statusOf(opID) {
		case "queued", "running":
			time.Sleep(5 * time.Millisecond)
		default:
			return
		}
	}
	t.Errorf("op %s did not finish; status %q", opID, store.statusOf(opID))
}
