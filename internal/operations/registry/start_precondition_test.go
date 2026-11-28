// file: internal/operations/registry/start_precondition_test.go
// version: 1.0.0
// guid: 0e6c1d93-47a8-4b25-9f1e-b3d52a7c8e64
// last-edited: 2026-11-28

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestEnqueueOp_StartPrecondition: a definition's StartPrecondition is asked by
// EnqueueOp, sees the options the start carries, and a refusal enqueues nothing
// and comes back as a *StartRefusedError carrying its reason. A start it allows
// queues as usual; a definition with no precondition is unaffected; a check that
// itself fails is not mistaken for a refusal.
func TestEnqueueOp_StartPrecondition(t *testing.T) {
	r, store := newTestRegistry(t)

	var seen []registry.EnqueueOptions
	gated := makeValidDef("test.start-gated")
	gated.StartPrecondition = func(_ context.Context, req registry.EnqueueOptions) error {
		seen = append(seen, req)
		if req.ActorUserID == "" {
			return registry.RefuseStart("no account to start this for")
		}
		if req.ActorUserID == "01HZBROKEN0000000000000000" {
			return errors.New("the check itself broke")
		}
		return nil
	}
	open := makeValidDef("test.start-ungated")
	for _, def := range []registry.OperationDef{gated, open} {
		if err := r.RegisterOp(def); err != nil {
			t.Fatalf("RegisterOp(%s): %v", def.ID, err)
		}
	}

	id, err := r.EnqueueOp(context.Background(), gated.ID, nil)
	var refused *registry.StartRefusedError
	if !errors.As(err, &refused) || refused.Reason != "no account to start this for" {
		t.Fatalf("a start with no actor: id=%q err=%v, want a StartRefusedError with its reason", id, err)
	}
	if id != "" {
		t.Errorf("a refused start returned id %q", id)
	}
	if rows := activeRowsForDef(t, store, gated.ID); len(rows) != 0 {
		t.Errorf("a refused start left %d active rows", len(rows))
	}

	_, err = r.EnqueueOp(context.Background(), gated.ID, nil,
		registry.WithActor("01HZBROKEN0000000000000000"))
	if err == nil || errors.As(err, &refused) {
		t.Errorf("a failing check was reported as %v, want a plain error", err)
	}

	id, err = r.EnqueueOp(context.Background(), gated.ID, nil,
		registry.WithActor("01HZBATMAN00000000000000AA"), registry.WithActorPermissions([]string{"settings.manage"}))
	if err != nil || id == "" {
		t.Fatalf("a start with an actor: id=%q err=%v", id, err)
	}
	last := seen[len(seen)-1]
	if last.ActorUserID != "01HZBATMAN00000000000000AA" || len(last.ActorPermissions) != 1 {
		t.Errorf("the precondition saw %+v, want the actor and permissions the start carries", last)
	}
	if rows := activeRowsForDef(t, store, gated.ID); len(rows) != 1 {
		t.Errorf("an allowed start left %d active rows, want 1", len(rows))
	}

	if _, err := r.EnqueueOp(context.Background(), open.ID, nil); err != nil {
		t.Errorf("a definition with no precondition was refused: %v", err)
	}
}

// TestRetryInterrupted_StartPrecondition: an in-place retry is a start by the
// retrying request, so the def's precondition is asked with that request's
// options (not the actor the row was first queued under). A refusal comes back
// as a *StartRefusedError before any write: the row keeps its status, its actor
// and its manual-retry count. A retry the precondition allows re-queues the same
// row as usual.
func TestRetryInterrupted_StartPrecondition(t *testing.T) {
	store := newFakeStore()
	var seen []registry.EnqueueOptions
	def := makeValidDef("test.retry-gated")
	def.ResumePolicy = registry.ResumeDrop
	def.StartPrecondition = func(_ context.Context, req registry.EnqueueOptions) error {
		seen = append(seen, req)
		if req.ActorUserID == "" {
			return registry.RefuseStart("no account to retry this for")
		}
		return nil
	}
	r := startedRetryRegistry(t, store, def)

	const firstActor = "01HZBATMAN00000000000000AA"
	actor := firstActor
	id := "01HZINTERRUPTED000000000000"
	if err := store.InsertOperationV2(database.OperationV2Row{
		ID: id, DefID: def.ID, Plugin: "test", Status: "interrupted_dropped", Params: `{}`,
		QueuedAt: time.Now().UTC(), ActorUserID: &actor,
	}); err != nil {
		t.Fatal(err)
	}

	err := r.RetryInterrupted(t.Context(), id, "robin")
	var refused *registry.StartRefusedError
	if !errors.As(err, &refused) || refused.Reason != "no account to retry this for" {
		t.Fatalf("a retry with no account: err = %v, want a StartRefusedError with its reason", err)
	}
	row := rowOf(t, store, id)
	if row.Status != "interrupted_dropped" || row.ManualRetryCount != 0 || row.ActorUserID == nil || *row.ActorUserID != firstActor {
		t.Errorf("a refused retry changed the row: status=%q manual_retry_count=%d actor=%v", row.Status, row.ManualRetryCount, row.ActorUserID)
	}

	const retrier = "01HZGORDON000000000000000A"
	if err := r.RetryInterrupted(t.Context(), id, "gordon", registry.WithActor(retrier)); err != nil {
		t.Fatalf("a retry by an account: %v", err)
	}
	if last := seen[len(seen)-1]; last.ActorUserID != retrier {
		t.Errorf("the precondition saw actor %q, want the retrying request's %q, not the row's", last.ActorUserID, retrier)
	}
	waitForRetryStatus(t, store, id, "completed")
}

// TestValidateOpDef_StartPreconditionRefusesPathsThatBypassIt: a def may not
// set StartPrecondition together with a path that creates its run without the
// starting request's actor (a batch flush, a merge into another request's queued
// run, a requeueing resume), so a def cannot rely on the precondition through a
// path that skips it. The server refuses to boot on it.
func TestValidateOpDef_StartPreconditionRefusesPathsThatBypassIt(t *testing.T) {
	allow := func(context.Context, registry.EnqueueOptions) error { return nil }
	cases := []struct {
		name   string
		mutate func(*registry.OperationDef)
		want   string
	}{
		{"batchable", func(d *registry.OperationDef) { d.Batchable = true }, "Batchable"},
		{"merge queued params", func(d *registry.OperationDef) {
			d.MergeQueuedParams = func(a, _ json.RawMessage) (json.RawMessage, bool, error) { return a, false, nil }
		}, "MergeQueuedParams"},
		{"resume requeue", func(d *registry.OperationDef) { d.ResumePolicy = registry.ResumeRequeue }, "ResumeRequeue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValidDef("test.gated-bypass")
			def.StartPrecondition = allow
			tc.mutate(&def)
			err := registry.ValidateOpDef(def)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateOpDef = %v, want a refusal naming %s", err, tc.want)
			}
			def.StartPrecondition = nil
			if err := registry.ValidateOpDef(def); err != nil {
				t.Errorf("the same def without a precondition was refused: %v", err)
			}
		})
	}
	ok := makeValidDef("test.gated-ok")
	ok.StartPrecondition = allow
	ok.ResumePolicy = registry.ResumeDrop
	if err := registry.ValidateOpDef(ok); err != nil {
		t.Errorf("a def with a precondition and no bypassing path was refused: %v", err)
	}
}
