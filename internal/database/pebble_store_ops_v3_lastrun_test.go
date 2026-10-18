// file: internal/database/pebble_store_ops_v3_lastrun_test.go
// version: 1.0.0
// guid: 85eeef9b-b1ca-4aab-9d96-29548d3e624f
// last-edited: 2026-10-18

package database

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// TestLatestSettledRunV3 drives the per-definition last-run lookup through a
// real store: unfinished runs at the top of the index are skipped, a run of
// a longer def id sharing the prefix is not taken, a def with no finished run
// is nil, and a run queued earlier that finished later wins inside the
// lookahead.
func TestLatestSettledRunV3(t *testing.T) {
	p := newTimelineStore(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seq := 0
	insert := func(def string) string {
		t.Helper()
		seq++
		q := base.Add(time.Duration(seq) * time.Minute)
		id := newRunID()
		if err := p.InsertRunV3(RunRecord{ID: id, DefID: def, Priority: 5, QueuedAt: &q}); err != nil {
			t.Fatalf("InsertRunV3: %v", err)
		}
		return id
	}
	finishAt := func(id string, ev state.Event, at time.Time) {
		t.Helper()
		transition(t, p, id, state.EvDispatch, func(r *TransitionRequest) { r.Now = at.Add(-time.Second) })
		transition(t, p, id, ev, func(r *TransitionRequest) {
			r.Now = at
			if ev == state.EvReturnErr {
				r.Error = "boom"
			}
		})
	}

	if got, err := p.LatestSettledRunV3("test.none"); err != nil || got != nil {
		t.Fatalf("no runs: %+v, %v", got, err)
	}

	early := insert("test.a")         // queued first, finishes last
	middle := insert("test.a")        // finishes before early
	longer := insert("test.a:longer") // shares the prefix "test.a:"
	running := insert("test.a")       // newest, still running
	queued := insert("test.a")        // newest of all, still queued
	finishAt(middle, state.EvReturnNil, base.Add(2*time.Hour))
	finishAt(early, state.EvReturnErr, base.Add(3*time.Hour))
	finishAt(longer, state.EvReturnNil, base.Add(4*time.Hour)) // newer, but another def
	transition(t, p, running, state.EvDispatch, nil)
	_ = queued

	got, err := p.LatestSettledRunV3("test.a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != early || got.State != state.V3Failed {
		t.Fatalf("latest = %+v, want %s (queued earlier, finished later)", got, early)
	}

	onlyOpen := insert("test.open")
	transition(t, p, onlyOpen, state.EvDispatch, nil)
	if got, err := p.LatestSettledRunV3("test.open"); err != nil || got != nil {
		t.Fatalf("only an unfinished run: %+v, %v", got, err)
	}
}
