// file: internal/operations/registry/external_write_hold_test.go
// version: 1.0.1
// guid: 9d4f1c62-3a85-4e07-b6d9-71c2e8a05f43
// last-edited: 2026-11-18

package registry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// HoldWriteSet is the write-set gate for a caller that is not a dispatched op
// (the operation revert, an HTTP handler). These tests pin both directions of
// the same Gate-3b contract the dispatcher gives its own ops: a hold is refused
// while a running op overlaps it, and a queued op overlapping a hold stays
// queued until the hold is released.

func writerDef(id string, writes []registry.Resource, run func(ctx context.Context)) registry.OperationDef {
	d := makeValidDef(id)
	d.Writes = writes
	d.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		if run != nil {
			run(ctx)
		}
		return nil
	}
	return d
}

func TestHoldWriteSet_RefusedWhileARunningOpWritesTheSameResource(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	started, release := make(chan struct{}), make(chan struct{})
	_ = r.RegisterOp(writerDef("test.hold-prune", []registry.Resource{registry.ResSeries}, func(c context.Context) {
		close(started)
		select {
		case <-release:
		case <-c.Done():
		}
	}))
	r.Start(ctx)
	opID, err := r.EnqueueOp(ctx, "test.hold-prune", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	<-started

	rel, err := r.HoldWriteSet("revert:op-x", "the revert of op-x", []registry.Resource{registry.ResBooks, registry.ResSeries})

	var conflict *registry.WriteSetConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("hold over a running writer: want *WriteSetConflict, got %v", err)
	}
	if rel != nil {
		t.Fatal("a refused hold must return no release")
	}
	if conflict.BlockerOpID != opID || conflict.BlockerDefID != "test.hold-prune" {
		t.Fatalf("conflict names %q/%q, want %q/test.hold-prune", conflict.BlockerOpID, conflict.BlockerDefID, opID)
	}
	if len(conflict.Resources) != 1 || conflict.Resources[0] != registry.ResSeries {
		t.Fatalf("overlap = %v, want [series]", conflict.Resources)
	}
	if !strings.Contains(conflict.Error(), "test.hold-prune") {
		t.Fatalf("message does not name the blocker: %s", conflict.Error())
	}

	close(release)
	awaitStatus(t, store, opID, "completed", 5*time.Second)
	rel, err = r.HoldWriteSet("revert:op-x", "the revert of op-x", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("hold after the writer finished: %v", err)
	}
	rel()
}

func TestHoldWriteSet_DefersAnOverlappingOpUntilReleased(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	var ran atomic.Bool
	_ = r.RegisterOp(writerDef("test.hold-merge", []registry.Resource{registry.ResSeries}, func(context.Context) { ran.Store(true) }))
	r.Start(ctx)

	rel, err := r.HoldWriteSet("revert:op-y", "the revert of op-y", []registry.Resource{registry.ResBooks, registry.ResSeries})
	if err != nil {
		t.Fatalf("HoldWriteSet: %v", err)
	}
	opID, err := r.EnqueueOp(ctx, "test.hold-merge", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	assertStaysQueued(t, store, opID, 500*time.Millisecond)
	if ran.Load() {
		t.Fatal("an op ran while a hold declared an overlapping write")
	}

	rel()
	awaitStatus(t, store, opID, "completed", 5*time.Second)
	if !ran.Load() {
		t.Fatal("the deferred op never ran after the hold was released")
	}
}

func TestHoldWriteSet_ADisjointOpIsNotDeferred(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	_ = r.RegisterOp(writerDef("test.hold-other", []registry.Resource{registry.ResAuthors}, nil))
	r.Start(ctx)
	rel, err := r.HoldWriteSet("revert:op-z", "the revert of op-z", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("HoldWriteSet: %v", err)
	}
	defer rel()

	opID, err := r.EnqueueOp(ctx, "test.hold-other", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	awaitStatus(t, store, opID, "completed", 5*time.Second)
}

func TestHoldWriteSet_TwoHoldsOverlapOnlyWhenTheirWritesDo(t *testing.T) {
	r := registry.New(newFakeStore(), slog.Default(), 4, nil)
	relA, err := r.HoldWriteSet("revert:a", "the revert of a", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, err = r.HoldWriteSet("revert:b", "the revert of b", []registry.Resource{registry.ResSeries, registry.ResBooks})
	var conflict *registry.WriteSetConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("overlapping second hold: want conflict, got %v", err)
	}
	if conflict.BlockerOpID != "revert:a" || conflict.Reason != "the revert of a" || conflict.BlockerDefID != "" {
		t.Fatalf("conflict = %+v, want the first hold named by holder and reason", conflict)
	}

	relC, err := r.HoldWriteSet("revert:c", "the revert of c", []registry.Resource{registry.ResAuthors})
	if err != nil {
		t.Fatalf("disjoint second hold: %v", err)
	}
	relC()
	relA()
	relB, err := r.HoldWriteSet("revert:b", "the revert of b", []registry.Resource{registry.ResSeries, registry.ResBooks})
	if err != nil {
		t.Fatalf("hold after the first was released: %v", err)
	}
	relB()
}

// The same holder id asking twice (two reverts of one operation at once) is
// refused, and the first claim survives the refusal.
func TestHoldWriteSet_ADuplicateHolderIsRefusedAndKeepsTheFirstClaim(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	var ran atomic.Bool
	_ = r.RegisterOp(writerDef("test.hold-dup", []registry.Resource{registry.ResSeries}, func(context.Context) { ran.Store(true) }))
	r.Start(ctx)
	rel, err := r.HoldWriteSet("revert:dup", "the revert of dup", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, err = r.HoldWriteSet("revert:dup", "the revert of dup", []registry.Resource{registry.ResSeries})
	var conflict *registry.WriteSetConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate holder: want conflict, got %v", err)
	}

	opID, _ := r.EnqueueOp(ctx, "test.hold-dup", nil)
	assertStaysQueued(t, store, opID, 300*time.Millisecond)
	rel()
	awaitStatus(t, store, opID, "completed", 5*time.Second)
}

func TestHoldWriteSet_ReleaseIsIdempotentAndNeverDropsALaterHold(t *testing.T) {
	r := registry.New(newFakeStore(), slog.Default(), 4, nil)
	first, err := r.HoldWriteSet("revert:k", "the revert of k", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	first()
	second, err := r.HoldWriteSet("revert:k", "the revert of k again", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("hold after release: %v", err)
	}
	first() // a stale second call must not release the new claim
	if _, err := r.HoldWriteSet("revert:other", "the revert of other", []registry.Resource{registry.ResSeries}); err == nil {
		t.Fatal("a stale release dropped the later hold")
	}
	second()
}

func TestHoldWriteSet_NoWritesClaimsNothingAndEmptyHolderIsRefused(t *testing.T) {
	r := registry.New(newFakeStore(), slog.Default(), 4, nil)
	rel, err := r.HoldWriteSet("revert:none", "nothing", nil)
	if err != nil || rel == nil {
		t.Fatalf("empty write-set: rel=%v err=%v", rel != nil, err)
	}
	rel()
	if _, err := r.HoldWriteSet("", "x", []registry.Resource{registry.ResSeries}); err == nil {
		t.Fatal("an empty holder id must be refused")
	}
}

func TestWriteSetOfDef(t *testing.T) {
	r := registry.New(newFakeStore(), slog.Default(), 4, nil)
	_ = r.RegisterOp(writerDef("test.ws-declared", []registry.Resource{registry.ResBooks, registry.ResSeries}, nil))
	_ = r.RegisterOp(writerDef("test.ws-none", nil, nil))

	got := r.WriteSetOfDef("test.ws-declared")
	if len(got) != 2 {
		t.Fatalf("declared write-set = %v", got)
	}
	got[0] = registry.ResAuthors // a caller mutating the answer must not change the def
	if again := r.WriteSetOfDef("test.ws-declared"); again[0] != registry.ResBooks {
		t.Fatalf("WriteSetOfDef leaked the def's slice: %v", again)
	}
	if n := len(r.WriteSetOfDef("test.ws-none")); n != 0 {
		t.Fatalf("def with no Writes: %d resources", n)
	}
	if n := len(r.WriteSetOfDef("test.unregistered")); n != 0 {
		t.Fatalf("unregistered def: %d resources", n)
	}
}

// The deferral log line for an op held back by a HOLD names the hold (held_by),
// not an op: a hold has no def id, and its reason used to be printed as
// running_def_id under a message that says "running op".
func TestHoldWriteSet_TheDeferralLogNamesTheHoldNotARunningOp(t *testing.T) {
	ctx := t.Context()
	var buf syncBuffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store := newFakeStore()
	r := registry.New(store, lg, 4, nil)
	_ = r.RegisterOp(writerDef("test.hold-logged", []registry.Resource{registry.ResSeries}, nil))
	r.Start(ctx)
	rel, err := r.HoldWriteSet("revert:op-z", "the revert of op-z", []registry.Resource{registry.ResSeries})
	if err != nil {
		t.Fatalf("HoldWriteSet: %v", err)
	}
	defer rel()
	opID, err := r.EnqueueOp(ctx, "test.hold-logged", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	assertStaysQueued(t, store, opID, 500*time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, `held_by="the revert of op-z"`) {
		t.Fatalf("the deferral line must name the hold as held_by:\n%s", out)
	}
	if strings.Contains(out, "running_def_id") || strings.Contains(out, "conflict with running op") {
		t.Fatalf("a hold is not a running op, but the line says so:\n%s", out)
	}
}

// syncBuffer is a bytes.Buffer safe for the dispatcher goroutine to write while
// the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
