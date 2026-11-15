// file: internal/plugins/maintenance/kinds_t6_g4_real_test.go
// version: 1.0.0
// guid: 075c2d47-deca-45e6-8742-449c0093b857
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// The recorder fakes in kinds_t6_g4_test.go answer every read with a zero value,
// so a core run against them writes nothing and proves only the NewWriter
// handoff. The scenarios here put a REAL in-memory store behind both shells: the
// native one reads through headT6G4Store and writes through a spyT6G4Writer that
// records each call and then performs it, so a test asserts the resources the
// run recorded (equal to the kind's expected set, not merely a subset of what it
// declares) and the rows that changed.

var _ t6g4Writer = spyT6G4Writer{}

// t6g4RealDeps is a t6g4Deps over p's own store: reads through headT6G4Store,
// writes through a spy over headT6G4Writer. handed collects the tasks.Writer each
// NewWriter call received.
func t6g4RealDeps(p *Plugin, rec *t6g4Calls, handed *[]tasks.Writer) t6g4Deps {
	store := p.deps.OpsStore()
	return t6g4Deps{
		Store: headT6G4Store{OpsStore: store, deps: p.deps},
		NewWriter: func(w tasks.Writer) t6g4Writer {
			*handed = append(*handed, w)
			return spyT6G4Writer{inner: headT6G4Writer{store: store, deps: p.deps}, rec: rec}
		},
	}
}

// t6g4RealHost is a run host whose context carries a deadline (zero = none).
func t6g4RealHost(kind tasks.Kind, within time.Duration, w tasks.Writer) (*t6g4Host, func()) {
	ctx, cancel := context.Background(), func() {}
	if within > 0 {
		ctx, cancel = context.WithTimeout(ctx, within)
	}
	return &t6g4Host{FakeRun: taskstest.NewFakeRun(ctx, kind), mode: tasks.ModeLive, w: w}, cancel
}

// t6g4SameResources fails unless the resources the recorded calls wrote are
// exactly want, in any order.
func t6g4SameResources(t *testing.T, rec *t6g4Calls, want ...ops.Resource) {
	t.Helper()
	got := t6g4RecordedResources(t, rec.all())
	w := make([]string, len(want))
	for i, r := range want {
		w[i] = string(r)
	}
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Errorf("recorded writes touched %v, want exactly %v (calls %v)", got, w, rec.all())
	}
}

func init() {
	// transcribe-file-intros through the native shell, which sets the yield
	// margin: attempt 1 runs under a deadline inside it and yields after its first
	// batch with a cursor; attempt 2 restores that cursor and finishes the run.
	t6g4WriteScenarios[fileIntroKind] = func(t *testing.T) {
		fx := newIntroFx(t)
		fileIntroSlots = func() int { return 1 } // sequential: the prefix is exactly the first batch
		paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)
		rec := &t6g4Calls{}
		var handed []tasks.Writer
		def := fx.p.fileIntroTranscribeKind(t6g4RealDeps(fx.p, rec, &handed), nil)
		decl := tasks.CommonOf(def).Effects

		run1 := &t6g4RunWriter{name: "attempt 1"}
		h1, cancel := t6g4RealHost(fileIntroKind, time.Minute, run1)
		defer cancel()
		_, err := runT6G4Kind(t, def, tasks.NewRC(h1), "")
		if !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("attempt 1 inside the margin: err = %v, want tasks.ErrContinueInPlace (the native shell must set the yield margin and the checkpoint hook)", err)
		}
		raw, ok := h1.CheckpointJSON("")
		if !ok {
			t.Fatal("attempt 1 yielded without saving a cursor")
		}
		var cur fileIntroCursor
		if err := json.Unmarshal(raw, &cur); err != nil {
			t.Fatal(err)
		}
		if cur.DoneBatches != 1 || cur.Last == nil || cur.Stats.Done {
			t.Fatalf("cursor after the yield = %+v, want one batch done, a last key, counters not done", cur)
		}
		if got := len(fx.whisp.sentPaths()); got != fileIntroBatchGroups {
			t.Fatalf("attempt 1 sent %d files, want exactly the first batch (%d)", got, fileIntroBatchGroups)
		}

		fx.whisp.mu.Lock()
		fx.whisp.calls = nil
		fx.whisp.mu.Unlock()
		run2 := &t6g4RunWriter{name: "attempt 2"}
		h2, cancel2 := t6g4RealHost(fileIntroKind, 0, run2)
		defer cancel2()
		if err := h2.SetCheckpoint("", raw); err != nil {
			t.Fatal(err)
		}
		if _, err := runT6G4Kind(t, def, tasks.NewRC(h2), ""); err != nil {
			t.Fatalf("attempt 2 (restored): %v", err)
		}
		if got := len(fx.whisp.sentPaths()); got != len(paths)-fileIntroBatchGroups {
			t.Fatalf("attempt 2 sent %d files, want the remaining %d", got, len(paths)-fileIntroBatchGroups)
		}
		if len(handed) != 2 || handed[0] != tasks.Writer(run1) || handed[1] != tasks.Writer(run2) {
			t.Errorf("NewWriter was handed %v, want each attempt's own rc.Writer()", handed)
		}
		for _, b := range mustAllBooks(t, fx) {
			for _, f := range mustFiles(t, fx.s, b) {
				if f.IntroSourceHash != f.FileHash {
					t.Fatalf("file %s is not done after the second attempt: %+v", f.ID, f)
				}
			}
		}
		t6g4SameResources(t, rec, ops.ResBookFiles, ops.ResBooks)
		for _, r := range decl.WriteSet() {
			if r.Kind != string(ops.ResBookFiles) && r.Kind != string(ops.ResBooks) {
				t.Errorf("declared %q, which the run never wrote", r.Kind)
			}
		}
	}
}

// TestFileIntroHEADShell_DefRunWritesThroughTheStore drives HEAD's own def.Run
// (the v2 shell, not the core) over a fixture plugin in live mode: the shell must
// resolve the store, decode the params, wire the HEAD writer and reach the rows.
func TestFileIntroHEADShell_DefRunWritesThroughTheStore(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(3)
	rep := &fileIntroRep{id: "run-head-shell"}
	if err := fx.p.fileIntroTranscribeDef().Run(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range mustAllBooks(t, fx) {
		for _, f := range mustFiles(t, fx.s, b) {
			n++
			if f.IntroSourceHash != f.FileHash {
				t.Fatalf("HEAD's def.Run left file %s untranscribed: %+v", f.ID, f)
			}
		}
	}
	if n != 3 {
		t.Fatalf("fixture has %d files, want 3", n)
	}
}
