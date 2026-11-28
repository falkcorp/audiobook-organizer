// file: internal/operations/registry/start_precondition_test.go
// version: 1.0.0
// guid: 0e6c1d93-47a8-4b25-9f1e-b3d52a7c8e64
// last-edited: 2026-11-28

package registry_test

import (
	"context"
	"errors"
	"testing"

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
