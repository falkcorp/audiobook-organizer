// file: internal/books/cutover/m4/state_test.go
// version: 1.0.0
// guid: 3b7e1c92-d405-4a68-9f13-a8c25e60d7b1
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/mediacore/tasks"
)

// specRow is one row of 05-tasks 5T.8.3's state table written out in the test,
// independently of m4.StateTable (which is the code's own copy): the spec's
// words, one row per qualifier.
type specRow struct {
	st             state.V3
	wait, stop, wy string
	want           tasks.RunState
	reason         string
}

func specTable() []specRow {
	return []specRow{
		{st: "queued", want: "queued"},
		{st: "queued", wait: "ready", want: "queued"},
		{st: "queued", wait: "pause", want: "queued"},
		{st: "queued", wait: "deps", want: "waiting_deps"},
		{st: "running", want: "interrupted", reason: "crash"},
		{st: "stopping", stop: "shutdown", want: "interrupted", reason: "shutdown"},
		{st: "stopping", stop: "user", want: "canceled", reason: "user"},
		{st: "stopping", stop: "quiesce", want: "interrupted_quiesced", reason: "quiesce"},
		{st: "stopping", stop: "timeout", want: "timed_out", reason: "timeout"},
		{st: "stopping", stop: "watchdog", want: "failed", reason: "watchdog"},
		{st: "interrupted", wy: "crash", want: "interrupted", reason: "crash"},
		{st: "interrupted", wy: "legacy", want: "interrupted", reason: "legacy"},
		{st: "interrupted", wy: "quiesced", want: "interrupted_quiesced", reason: "quiesced"},
		{st: "interrupted", want: "interrupted", reason: "legacy"}, // HEAD recorded no why
		{st: "awaiting_decision", want: "awaiting_decision"},
		{st: "succeeded", want: "succeeded"},
		{st: "failed", want: "failed"},
		{st: "timed_out", want: "timed_out"},
		{st: "canceled", want: "canceled"},
		{st: "dropped", want: "interrupted_dropped"},
	}
}

// TestM4_StateTable holds MapState to the spec's table row by row, and holds
// m4.StateTable (the table the rig tests walk) to the same rows.
func TestM4_StateTable(t *testing.T) {
	for _, c := range specTable() {
		name := string(c.st) + "/" + c.wait + c.stop + c.wy
		t.Run(name, func(t *testing.T) {
			got, reason, ok := m4.MapState(c.st, c.wait, c.stop, c.wy)
			if !ok || got != c.want || reason != c.reason {
				t.Fatalf("MapState = (%q, %q, %v), want (%q, %q, true)", got, reason, ok, c.want, c.reason)
			}
		})
	}
	// Every spec row appears in m4.StateTable, so a rig test walking that table
	// walks the spec's.
	have := map[specRow]bool{}
	for _, c := range m4.StateTable() {
		have[specRow{c.State, c.WaitReason, c.StopReason, c.InterruptedWhy, c.Want, c.Reason}] = true
	}
	for _, c := range specTable() {
		if c.st == "interrupted" && c.wy == "" {
			continue // the no-why row is MapState's, not a stored vocabulary row
		}
		if !have[c] {
			t.Errorf("m4.StateTable lacks the spec row %+v", c)
		}
	}
}

// TestM4_StateTableRefusesWhatItDoesNotList: a state or stop reason HEAD never
// wrote is not mapped (the run is quarantined, never guessed).
func TestM4_StateTableRefusesWhatItDoesNotList(t *testing.T) {
	for _, c := range []struct {
		st   state.V3
		stop string
	}{
		{"hibernating", ""},
		{"", ""},
		{"stopping", ""},
		{"stopping", "boredom"},
	} {
		if got, _, ok := m4.MapState(c.st, "", c.stop, ""); ok {
			t.Errorf("MapState(%q, stop %q) = %q, ok; want refused", c.st, c.stop, got)
		}
	}
}

// TestCutover_StateTableImported imports one run per table row through the
// real importer and reads each back from core's task store.
func TestCutover_StateTableImported(t *testing.T) {
	var specs []runSpec
	wantBy := map[string]specRow{}
	done := epoch.Add(time.Hour)
	for i, c := range specTable() {
		id := runID(i + 1)
		s := runSpec{id: id, def: "library.scan", st: c.st, wait: c.wait, stop: c.stop, why: c.wy, evts: 1}
		switch c.want {
		case "succeeded", "failed", "timed_out", "canceled", "interrupted_dropped":
			if c.st != "stopping" { // HEAD never recorded a finish for a stopping run
				s.completed = tptr(done)
			}
		}
		specs = append(specs, s)
		wantBy[id] = c
	}
	r := newRig(t, withRuns(nil, specs...))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		seen := 0
		for id, c := range wantBy {
			seen++
			rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(id))
			if err != nil {
				t.Errorf("%s (%+v): %v", id, c, err)
				continue
			}
			if rec.State != c.want || rec.Reason != c.reason {
				t.Errorf("%s: imported as (%q, %q), want (%q, %q)", id, rec.State, rec.Reason, c.want, c.reason)
			}
			if rec.State.Terminal() && rec.Finished.IsZero() {
				t.Errorf("%s: terminal with no Finished", id)
			}
			// A stopping run has no CompletedAt: it finished when its trail last
			// moved (the one event the seed writes, a second after it was queued).
			if c.st == "stopping" && rec.State.Terminal() && !rec.Finished.Equal(epoch.Add(time.Second)) {
				t.Errorf("%s: finished %v, want the last trail entry's time %v", id, rec.Finished, epoch.Add(time.Second))
			}
		}
		if seen != len(specTable()) {
			t.Errorf("checked %d runs, want %d", seen, len(specTable()))
		}
		if len(s.Left) != 0 {
			t.Errorf("%d source rows left", len(s.Left))
		}
		if len(s.Quarantine) != 0 {
			t.Errorf("quarantined %+v", s.Quarantine)
		}
	})
}

// TestCutover_PriorityMapped: HEAD's 0/1/2 are Low/Normal/High, each its own.
func TestCutover_PriorityMapped(t *testing.T) {
	want := map[int]tasks.Priority{0: tasks.PriorityLow, 1: tasks.PriorityNormal, 2: tasks.PriorityHigh}
	var specs []runSpec
	for p := range 3 {
		specs = append(specs, runSpec{id: runID(p + 1), def: "library.scan", st: "queued", priority: p})
	}
	r := newRig(t, withRuns(nil, specs...))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		for p, w := range want {
			rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(p+1)))
			if err != nil {
				t.Fatal(err)
			}
			if rec.Priority != w {
				t.Errorf("priority %d imported as %v, want %v", p, rec.Priority, w)
			}
		}
	})
}
