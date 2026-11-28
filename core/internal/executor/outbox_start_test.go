// file: core/internal/executor/outbox_start_test.go
// version: 1.0.0
// guid: 7d2c4e19-3a85-4b60-9f1e-c58a0b6d2e73
// last-edited: 2026-11-28

package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// outboxStartRig boots gotham's executor with three kinds whose Start asks for
// core:system.manage (AuthRequired, so no module system actor can hold it, the
// shape of the settings.manage expansion):
//
//	gotham.relocate  OutboxSystemStart set: the outbox-started kind
//	gotham.plain     the same Start, flag unset
//	arkham.relocate  another module's kind with the flag set
//
// gotham lists arkham.relocate in TaskStarts when listed is true.
func outboxStartRig(t *testing.T, listed bool) *harness {
	t.Helper()
	manage := func(c *tasks.Common) {
		c.Perms.Start = rbac.AllOf("core:tasks.manage", "core:system.manage", "gotham:run")
	}
	flagged := func(c *tasks.Common) {
		manage(c)
		c.Perms.OutboxSystemStart = "a staged message of the module's own committed write"
	}
	var starts []tasks.Kind
	if listed {
		starts = []tasks.Kind{"arkham.relocate"}
	}
	return newHarness(t, []tasks.Definition{
		taskDef("gotham.relocate", okBody, flagged),
		taskDef("gotham.plain", okBody, manage),
		taskDef("arkham.relocate", okBody, flagged),
	}, func(o *Options) {
		withPrincipal(o)
		o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
		o.Manifests = []Manifest{{Module: "gotham", Enabled: true, TaskStarts: starts}, {Module: "arkham", Enabled: true}}
	})
}

// stageIn is Tx.StartRun's call: one start staged in a relay-shaped batch.
func stageIn(h *harness, kind tasks.Kind, trigger tasks.Trigger) (tasks.RunRef, error) {
	var ref tasks.RunRef
	err := h.e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
		var err error
		ref, err = h.e.StartIn(ctx, b, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: trigger})
		return err
	})
	return ref, err
}

// TestOutboxSystemStart_OwnerStartsItsFlaggedKind: the outbox's start of a kind
// that declares Perms.OutboxSystemStart goes through as the owner's system
// actor although that actor cannot hold Start.
func TestOutboxSystemStart_OwnerStartsItsFlaggedKind(t *testing.T) {
	h := outboxStartRig(t, false)
	ref, err := stageIn(h, "gotham.relocate", tasks.TriggerOutbox)
	if err != nil {
		t.Fatalf("the outbox's start of a flagged kind: %v, want it started", err)
	}
	if got := h.get(ref.ID).Actor; got != (tasks.Actor{AuthMethod: systemMethod, System: "gotham"}) {
		t.Fatalf("the run's actor = %+v, want gotham's system actor", got)
	}
}

// TestOutboxSystemStart_UnflaggedKindIsRefused: without the flag the same start
// is refused, as it was before the rule existed. This is the cell that fails
// when the flag is not what grants it.
func TestOutboxSystemStart_UnflaggedKindIsRefused(t *testing.T) {
	h := outboxStartRig(t, false)
	if _, err := stageIn(h, "gotham.plain", tasks.TriggerOutbox); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("the outbox's start of an unflagged kind: %v, want ErrNotPermitted", err)
	}
}

// TestOutboxSystemStart_OnlyTheOutboxPathTakesIt: the flag is read for the
// outbox's start and no other. A schedule or window start, the module's own
// client (outside a request: its system actor), and a person naming
// Trigger outbox in a Client.Start request are all refused, so no hand start
// gains anything from it.
func TestOutboxSystemStart_OnlyTheOutboxPathTakesIt(t *testing.T) {
	h := outboxStartRig(t, false)
	for _, trig := range []tasks.Trigger{tasks.TriggerSchedule, tasks.TriggerWindow, tasks.TriggerManual, ""} {
		if _, err := stageIn(h, "gotham.relocate", trig); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("StartIn with trigger %q: %v, want ErrNotPermitted", trig, err)
		}
	}
	mc, err := ModuleClient(h.x, "gotham")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mc.Start(context.Background(), "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerOutbox}); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("the module client's system start naming Trigger outbox: %v, want ErrNotPermitted", err)
	}
	gordon := asUser("gordon", "gotham:run")
	if _, err := h.x.Start(gordon, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerOutbox}); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("a person's start naming Trigger outbox: %v, want ErrNotPermitted", err)
	}
	// And the person who does hold Start still starts by hand, as before.
	holder := asUser("barbara", "core:tasks.manage", "core:system.manage", "gotham:run")
	if _, err := h.x.Start(holder, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive}); err != nil {
		t.Errorf("a person holding Start: %v, want started", err)
	}
}

// TestOutboxSystemStart_OtherModulesActorIsRefused: the exception belongs to the
// module that owns the kind. gotham's system actor on the outbox path is
// refused for arkham's flagged kind, whether or not gotham lists it in
// TaskStarts; arkham's own actor is not.
func TestOutboxSystemStart_OtherModulesActorIsRefused(t *testing.T) {
	for _, listed := range []bool{false, true} {
		h := outboxStartRig(t, listed)
		d := h.e.defs["arkham.relocate"]
		gotham := tasks.Actor{AuthMethod: systemMethod, System: "gotham"}
		if err := h.e.permitStart(d, gotham, starter{outbox: true}); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("TaskStarts listed=%v: gotham's actor on the outbox path for arkham's kind: %v, want ErrNotPermitted", listed, err)
		}
		arkham := tasks.Actor{AuthMethod: systemMethod, System: "arkham"}
		if err := h.e.permitStart(d, arkham, starter{outbox: true}); err != nil {
			t.Errorf("TaskStarts listed=%v: the owner's actor: %v, want permitted", listed, err)
		}
	}
}

// TestCheckOutboxStart is the P7 check's question, answered by the rule StartIn
// applies: an unregistered kind, an unflagged one and a kind of another module
// are each refused, the flagged own kind is not.
func TestCheckOutboxStart(t *testing.T) {
	h := outboxStartRig(t, true)
	cells := []struct {
		name   string
		module string
		kind   tasks.Kind
		want   error
	}{
		{"flagged own kind", "gotham", "gotham.relocate", nil},
		{"unflagged own kind", "gotham", "gotham.plain", ErrNotPermitted},
		{"unregistered kind", "gotham", "gotham.missing", ErrUnknownKind},
		{"another module's kind, listed in TaskStarts", "gotham", "arkham.relocate", ErrNotPermitted},
		{"the owner of the other module's kind", "arkham", "arkham.relocate", nil},
	}
	for _, c := range cells {
		err := CheckOutboxStart(h.x, c.module, c.kind)
		switch {
		case c.want == nil && err != nil:
			t.Errorf("%s: %v, want nil", c.name, err)
		case c.want != nil && !errors.Is(err, c.want):
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}
