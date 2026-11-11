// file: core/internal/restoreswap/hooks_test.go
// version: 1.0.0
// guid: 6e1d8b34-a095-4f72-8c4e-d3b07a5f9e61
// last-edited: 2026-11-11

package restoreswap

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// hooksBoot runs P0a on a fresh restore fixture and returns the swapper, the
// fixture and the restore boot's outcome.
func hooksBoot(t *testing.T) (*Swapper, fixture, Outcome) {
	t.Helper()
	f := restoreFixture(t, restorejournal.KindArchive, "inside", true)
	s := newSwapper(t, f.mem, testBuild, testNow)
	out, err := s.RunP0a(context.Background())
	if err != nil || out.Boot != BootRestore || out.Last != StepSwapped {
		t.Fatalf("P0a = %+v, %v; want a restore boot at swapped", out, err)
	}
	return s, f, out
}

func journalSteps(t *testing.T, s *Swapper, id string) []string {
	t.Helper()
	recs, err := s.Journal(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Step)
	}
	return out
}

// TestRestoreBootLongWay: a journaling build's restore boot takes machine 1's
// long way, opened, restore_line, hooks_done, marker_written, applied, with no
// HooksOwed, and deletes the in-flight file at the end. Every method is
// idempotent.
func TestRestoreBootLongWay(t *testing.T) {
	ctx := context.Background()
	s, f, out := hooksBoot(t)
	id := out.ID
	for range 2 { // the second pass of each is the crash-loop re-call
		if err := s.Opened(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := s.RestoreLine(ctx, id, 7); err != nil {
			t.Fatal(err)
		}
		if err := s.Opened(ctx, id); err != nil { // P2 re-ran after the line: no second opened
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := s.HooksDone(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := s.MarkerWritten(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	inflight := filepath.Join(stateDir, "restore", id, "inflight.json")
	if _, ok := readFile(t, f.mem, inflight); !ok {
		t.Fatal("the in-flight file is gone before applied")
	}
	for range 2 {
		if err := s.HooksApplied(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{StepStaged, StepRechecked, StepPreviousMoved, StepStagedMoved, StepSidecars, StepSwapped, StepOpened,
		StepRestoreLine, StepHooksDone, StepMarkerWritten, StepApplied}
	if got := journalSteps(t, s, id); !slices.Equal(got, want) {
		t.Fatalf("journal steps = %v, want %v", got, want)
	}
	recs, _ := s.Journal(id)
	for _, r := range recs {
		if r.HooksOwed {
			t.Errorf("record %s carries HooksOwed: the hooks ran", r.Step)
		}
		if r.Step == StepRestoreLine && r.Note != "7" {
			t.Errorf("restore_line note = %q, want the line's seq", r.Note)
		}
	}
	if _, ok := readFile(t, f.mem, inflight); ok {
		t.Error("the in-flight file survives applied")
	}
}

// TestRestoreBootStepsRefuseOutOfOrder: no boot can skip the hooks. Each edge
// refuses a journal that is not at the step before it.
func TestRestoreBootStepsRefuseOutOfOrder(t *testing.T) {
	ctx := context.Background()
	s, _, out := hooksBoot(t)
	id := out.ID
	for name, call := range map[string]func() error{
		"restore_line before opened":   func() error { return s.RestoreLine(ctx, id, 1) },
		"hooks_done before opened":     func() error { return s.HooksDone(ctx, id) },
		"marker_written before opened": func() error { return s.MarkerWritten(ctx, id) },
		"applied before opened":        func() error { return s.HooksApplied(ctx, id) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	must(t, s.Opened(ctx, id))
	for name, call := range map[string]func() error{
		"hooks_done before restore_line":     func() error { return s.HooksDone(ctx, id) },
		"marker_written before restore_line": func() error { return s.MarkerWritten(ctx, id) },
		"applied before restore_line":        func() error { return s.HooksApplied(ctx, id) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	must(t, s.RestoreLine(ctx, id, 1))
	if err := s.MarkerWritten(ctx, id); err == nil {
		t.Error("marker_written before hooks_done: accepted")
	}
	if err := s.HooksApplied(ctx, id); err == nil {
		t.Error("applied before marker_written: accepted")
	}
	if got := journalSteps(t, s, id); got[len(got)-1] != StepRestoreLine {
		t.Errorf("a refused edge appended: %v", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestRestoreBootResumesAtRestoreLineAndHooksDone: a crash after restore_line or
// hooks_done is read by the next P0a as a restore boot again (never as
// corrupt), at that step, and the boot resumes the long way.
func TestRestoreBootResumesAtRestoreLineAndHooksDone(t *testing.T) {
	ctx := context.Background()
	s, f, out := hooksBoot(t)
	id := out.ID
	must(t, s.Opened(ctx, id))
	must(t, s.RestoreLine(ctx, id, 3))

	for _, step := range []string{StepRestoreLine, StepHooksDone} {
		restarted := newSwapper(t, f.mem, testBuild, testNow)
		o, err := restarted.RunP0a(ctx)
		if err != nil || o.Boot != BootRestore || o.ID != id || o.Last != step {
			t.Fatalf("restart at %s: P0a = %+v, %v; want a restore boot at that step", step, o, err)
		}
		if step == StepRestoreLine {
			must(t, restarted.Opened(ctx, id))
			must(t, restarted.HooksDone(ctx, id))
		}
	}
	restarted := newSwapper(t, f.mem, testBuild, testNow)
	must(t, restarted.MarkerWritten(ctx, id))
	must(t, restarted.HooksApplied(ctx, id))
	if got := journalSteps(t, restarted, id); got[len(got)-1] != StepApplied || got[len(got)-2] != StepMarkerWritten {
		t.Errorf("journal steps = %v", got)
	}
}

// TestRestoreBootSwapsBackFromHooksDone: a failure after the hooks (the marker
// write, P5, P6) swaps back from hooks_done like from any state between
// previous_moved and marker_written.
func TestRestoreBootSwapsBackFromHooksDone(t *testing.T) {
	ctx := context.Background()
	s, f, out := hooksBoot(t)
	id := out.ID
	must(t, s.Opened(ctx, id))
	must(t, s.RestoreLine(ctx, id, 3))
	must(t, s.HooksDone(ctx, id))
	if err := s.SwapBack(ctx, id, errors.New("the marker write failed")); !errors.Is(err, ErrSwappedBack) {
		t.Fatalf("SwapBack = %v, want ErrSwappedBack", err)
	}
	if got := journalSteps(t, s, id); got[len(got)-1] != StepSwappedBack {
		t.Errorf("journal steps = %v, want swapped_back last", got)
	}
	if got, ok := readFile(t, f.mem, filepath.Join(stateDir, locationFile)); !ok || got != f.location {
		t.Errorf("location file after the swap-back = %q (present %v), want the saved copy %q", got, ok, f.location)
	}
}
