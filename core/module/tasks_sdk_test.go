// file: core/module/tasks_sdk_test.go
// version: 1.0.0
// guid: 5d0c7b84-31ea-4f69-b2a7-9e46c1d83f05
// last-edited: 2026-11-22

package module

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/tasks"
)

// sinkStub is a tasks.ChangeSink that records nothing.
type sinkStub struct{}

func (sinkStub) Record(context.Context, tasks.RunID, []tasks.Change) error { return nil }
func (sinkStub) Stands(context.Context, tasks.RunID, string) (bool, error) { return true, nil }

// TestRegistrar_TasksFlipSurface: Lanes, ChangeSink and Health record, and
// Close returns them in call order, cloned so a later call cannot reach the
// kernel's copy (F5-18).
func TestRegistrar_TasksFlipSurface(t *testing.T) {
	r := newTestRegistrar(t, false)
	sink := sinkStub{}
	r.Lanes(tasks.Lane{ID: "gotham"}, tasks.Lane{ID: "metropolis"})
	r.ChangeSink(sink)
	r.Health(health.Check{ID: "batcave.memdb"}, health.Check{ID: "batcave.cave-feed"})
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	rec := r.Close()
	if len(rec.Lanes) != 2 || rec.Lanes[0].ID != "gotham" || rec.Lanes[1].ID != "metropolis" {
		t.Fatalf("Lanes = %+v", rec.Lanes)
	}
	if rec.ChangeSink != sink {
		t.Fatalf("ChangeSink = %v", rec.ChangeSink)
	}
	if len(rec.Health) != 2 || rec.Health[0].ID != "batcave.memdb" || rec.Health[1].ID != "batcave.cave-feed" {
		t.Fatalf("Health = %+v", rec.Health)
	}
	rec.Lanes[0].ID = "tampered"
	rec.Health[0].ID = "tampered"
	if again := r.Close(); again.Lanes[0].ID != "gotham" || again.Health[0].ID != "batcave.memdb" {
		t.Fatalf("a caller's edit of Close's slices reached the registrar: %+v %+v", again.Lanes, again.Health)
	}
}

// TestRegistrar_TasksFlipRefusals: each malformed declaration is a recorded
// problem of the right class, never a silent drop or a panic.
func TestRegistrar_TasksFlipRefusals(t *testing.T) {
	cases := map[string]struct {
		call func(r *Registrar)
		want error
	}{
		"lane without id":         {func(r *Registrar) { r.Lanes(tasks.Lane{}) }, ErrInvalidRegistration},
		"lane twice":              {func(r *Registrar) { r.Lanes(tasks.Lane{ID: "gotham"}, tasks.Lane{ID: "gotham"}) }, ErrInvalidRegistration},
		"lane twice, two calls":   {func(r *Registrar) { r.Lanes(tasks.Lane{ID: "gotham"}); r.Lanes(tasks.Lane{ID: "gotham"}) }, ErrInvalidRegistration},
		"change sink nil":         {func(r *Registrar) { r.ChangeSink(nil) }, ErrInvalidRegistration},
		"change sink twice":       {func(r *Registrar) { r.ChangeSink(sinkStub{}); r.ChangeSink(sinkStub{}) }, ErrChangeSinkDuplicate},
		"health foreign":          {func(r *Registrar) { r.Health(health.Check{ID: "arkham.cells"}) }, ErrInvalidRegistration},
		"health bare module":      {func(r *Registrar) { r.Health(health.Check{ID: "batcave."}) }, ErrInvalidRegistration},
		"health no namespace":     {func(r *Registrar) { r.Health(health.Check{ID: "memdb"}) }, ErrInvalidRegistration},
		"health twice":            {func(r *Registrar) { r.Health(health.Check{ID: "batcave.a"}, health.Check{ID: "batcave.a"}) }, ErrInvalidRegistration},
		"health prefix lookalike": {func(r *Registrar) { r.Health(health.Check{ID: "batcave-annex.a"}) }, ErrInvalidRegistration},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestRegistrar(t, false)
			tc.call(r)
			if err := r.Err(); !errors.Is(err, tc.want) {
				t.Fatalf("Err = %v, want %v", err, tc.want)
			}
		})
	}
	// A refused change sink does not displace the first one.
	r := newTestRegistrar(t, false)
	first := sinkStub{}
	r.ChangeSink(first)
	r.ChangeSink(nil)
	if rec := r.Close(); rec.ChangeSink != first {
		t.Fatalf("the first sink was displaced: %v", rec.ChangeSink)
	}
}

// TestRegistrar_TasksFlipAfterClose: the three new calls are record-only
// like every other, so a call after Close is ProblemRegistrarClosed.
func TestRegistrar_TasksFlipAfterClose(t *testing.T) {
	r := newTestRegistrar(t, false)
	r.Close()
	r.Lanes(tasks.Lane{ID: "gotham"})
	r.ChangeSink(sinkStub{})
	r.Health(health.Check{ID: "batcave.late"})
	var ve *ValidationError
	if err := r.Err(); !errors.Is(err, ErrRegistrarClosed) || !errors.As(err, &ve) || len(ve.Problems) != 3 {
		t.Fatalf("Err = %v, want three ProblemRegistrarClosed", err)
	}
	if rec := r.Close(); len(rec.Lanes) != 0 || rec.ChangeSink != nil || len(rec.Health) != 0 {
		t.Fatalf("a late call took effect: %+v", rec)
	}
}
