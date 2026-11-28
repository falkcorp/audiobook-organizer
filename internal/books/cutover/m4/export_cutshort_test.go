// file: internal/books/cutover/m4/export_cutshort_test.go
// version: 1.0.0
// guid: 3d8a6f21-7b94-4c05-a1e2-9f0c5d7b3e48
// last-edited: 2026-11-28

package m4_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 M4: a diagnostics export HEAD never finished imports as a terminal
// canceled run, never as work the executor could pick up.

func TestDiagnosticsExportKindIsTheRenameTarget(t *testing.T) {
	if got := kindrename.CoreKinds()["diagnostics.export"]; got != m4.DiagnosticsExportKind {
		t.Fatalf("kindrename maps diagnostics.export to %q, m4 cuts %q short", got, m4.DiagnosticsExportKind)
	}
}

// TestCutover_UnfinishedDiagnosticsExportImportsCanceled: every HEAD state that
// is not terminal becomes canceled with ExportRestartReason, a finish time of
// HEAD's own, and counted; a terminal export and a run of another kind in the
// same states import as before.
func TestCutover_UnfinishedDiagnosticsExportImportsCanceled(t *testing.T) {
	done := epoch.Add(time.Hour)
	const export, other = "diagnostics.export", "dedup.book-scan"
	type cell struct {
		name      string
		def       string
		st        state.V3
		wait      string
		stop, why string
		want      tasks.RunState
		reason    string
		cut       bool
	}
	cells := []cell{
		{name: "queued", def: export, st: state.V3Queued, want: tasks.StateCanceled, cut: true},
		{name: "queued behind a dependency", def: export, st: state.V3Queued, wait: state.WaitDeps, want: tasks.StateCanceled, cut: true},
		{name: "paused (queued, wait pause)", def: export, st: state.V3Queued, wait: "pause", want: tasks.StateCanceled, cut: true},
		{name: "running", def: export, st: state.V3Running, want: tasks.StateCanceled, cut: true},
		{name: "stopping for shutdown", def: export, st: state.V3Stopping, stop: state.StopShutdown, want: tasks.StateCanceled, cut: true},
		{name: "stopping for quiesce", def: export, st: state.V3Stopping, stop: state.StopQuiesce, want: tasks.StateCanceled, cut: true},
		{name: "interrupted", def: export, st: state.V3Interrupted, why: state.WhyCrash, want: tasks.StateCanceled, cut: true},
		{name: "awaiting decision", def: export, st: state.V3AwaitingDecision, want: tasks.StateCanceled, cut: true},
		{name: "succeeded is unchanged", def: export, st: state.V3Succeeded, want: tasks.StateSucceeded},
		{name: "failed is unchanged", def: export, st: state.V3Failed, want: tasks.StateFailed},
		{name: "user cancel is unchanged", def: export, st: state.V3Stopping, stop: state.StopUser, want: tasks.StateCanceled, reason: state.StopUser},
		{name: "another kind running stays interrupted", def: other, st: state.V3Running, want: tasks.StateInterrupted, reason: state.WhyCrash},
		{name: "another kind queued stays queued", def: other, st: state.V3Queued, want: tasks.StateQueued},
	}
	var specs []runSpec
	for i, c := range cells {
		s := runSpec{id: runID(i + 1), def: c.def, st: c.st, wait: c.wait, stop: c.stop, why: c.why}
		if c.st == state.V3Succeeded || c.st == state.V3Failed {
			s.completed = new(done)
		}
		specs = append(specs, s)
	}
	r := newRig(t, withRuns(nil, specs...))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		for i, c := range cells {
			rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(i+1)))
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			wantReason := c.reason
			if c.cut {
				// The words are the contract (the lead's, and an operator reads them),
				// so the test spells them out instead of reading the constant.
				wantReason = "imported; restart the export"
			}
			if rec.State != c.want || rec.Reason != wantReason {
				t.Errorf("%s: imported %q reason %q, want %q reason %q", c.name, rec.State, rec.Reason, c.want, wantReason)
			}
			if c.cut && (rec.Finished.IsZero() || rec.Finished.Before(rec.Created)) {
				t.Errorf("%s: finished %v (created %v), want a time of HEAD's own at or after creation", c.name, rec.Finished, rec.Created)
			}
		}
	})
	if got, want := strings.Count(r.logs.String(), "an unfinished diagnostics export is imported canceled"), 8; got != want {
		t.Errorf("the log names %d cut-short exports, want %d:\n%s", got, want, r.logs.String())
	}
}
