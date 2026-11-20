// file: core/health/diagnostics/prune_test.go
// version: 0.1.0
// guid: cd083256-28c3-4268-bd7c-37226d671643
// last-edited: 2026-11-20

package diagnostics_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

func pruneTaskAt(root *os.Root, keep time.Duration) tasks.Definition {
	return diagnostics.PruneTask(root, func() time.Duration { return keep }, func() time.Time { return fixedNow })
}

func runPruneTask(t *testing.T, def tasks.Definition) diagnostics.PruneResult {
	t.Helper()
	raw, err := runDefinition(t, def, taskstest.NewFakeRun(context.Background(), def.Kind()).RC(), "")
	if err != nil {
		t.Fatalf("prune run: %v", err)
	}
	var res diagnostics.PruneResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	return res
}

// writeAged writes each name into dir with a modification time age before
// fixedNow (a negative age is a file dated after now).
func writeAged(t *testing.T, dir string, ages map[string]time.Duration) {
	t.Helper()
	for name, age := range ages {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := fixedNow.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func left(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestPruneTask_Conformance(t *testing.T) {
	taskstest.Conformance(t, pruneTaskAt(newFixture(t).root, time.Hour))
}

// The kind (05-health 5H.3.5) is scheduled from core.diagnostics.schedules.prune
// in Live mode, writes core/diagnostics, and is started by a scope the
// scheduler's system actor can hold (SEC2-15): core:system.manage is
// AuthRequired, so it must not be the start permission.
func TestPruneTask_Declaration(t *testing.T) {
	d := pruneTaskAt(newFixture(t).root, time.Hour)
	if d.Kind() != diagnostics.PruneKind || diagnostics.PruneKind != "core.diagnostics.prune" {
		t.Errorf("kind = %q", d.Kind())
	}
	if p := tasks.Problems(d); len(p) != 0 {
		t.Fatalf("definition problems: %v", p)
	}
	c := tasks.CommonOf(d)
	if c.Schedule == nil {
		t.Fatal("no schedule")
	}
	if key := c.Schedule.ConfigKey(); key != diagnostics.PruneScheduleKey || diagnostics.PruneScheduleKey != "core.diagnostics.schedules.prune" {
		t.Errorf("schedule key = %q", key)
	}
	if !c.Schedule.IsLive() || c.Schedule.IsPreview() {
		t.Errorf("Schedule live=%v preview=%v, want live", c.Schedule.IsLive(), c.Schedule.IsPreview())
	}
	if !slices.Equal(c.Effects.WriteSet(), []entity.Ref{tasks.Res("core", "diagnostics")}) || c.Effects.IsReadOnly() {
		t.Errorf("writes = %v, want one write on core/diagnostics", c.Effects.WriteSet())
	}
	if got := c.Perms.Start.Permissions(); !slices.Equal(got, []rbac.Permission{"core:tasks.manage"}) {
		t.Errorf("Perms.Start = %v, want core:tasks.manage (reachable by the system actor)", got)
	}
	if c.Resume != tasks.ResumeFromZero || c.Priority != tasks.PriorityLow || c.Timeout != diagnostics.PruneTimeout {
		t.Errorf("Resume %v Priority %v Timeout %v", c.Resume, c.Priority, c.Timeout)
	}
}

func TestPruneTask_NilRootOrRetentionFailsClosed(t *testing.T) {
	keep := func() time.Duration { return time.Hour }
	if len(tasks.Problems(diagnostics.PruneTask(nil, keep, nil))) == 0 {
		t.Error("no problem reported for a nil root")
	}
	if len(tasks.Problems(diagnostics.PruneTask(newFixture(t).root, nil, nil))) == 0 {
		t.Error("no problem reported for a nil retention")
	}
}

// The retention arithmetic: a bundle exactly retention old stays, one a
// nanosecond older goes; a file dated after now (a clock step) stays; an
// abandoned partial goes by the same rule; a name that is not a bundle's is
// never touched, however old.
func TestPruneTask_RetentionBoundary(t *testing.T) {
	f := newFixture(t)
	const keep = 6 * time.Hour
	writeAged(t, f.dir, map[string]time.Duration{
		"at-edge.zip":       keep,
		"past-edge.zip":     keep + time.Nanosecond,
		"future.zip":        -time.Hour,
		"fresh.zip":         time.Minute,
		"stopped.zip.tmp":   keep + time.Second,
		"young.zip.tmp":     time.Second,
		"arkham-notes.txt":  30 * 24 * time.Hour,
		"bad name.zip":      30 * 24 * time.Hour,
		"joker.zip.tmp.bak": 30 * 24 * time.Hour,
	})
	res := runPruneTask(t, pruneTaskAt(f.root, keep))
	if res.Removed != 2 || res.Retention != keep.String() {
		t.Errorf("result = %+v, want 2 removed at %s", res, keep)
	}
	want := []string{"arkham-notes.txt", "at-edge.zip", "bad name.zip", "fresh.zip", "future.zip", "joker.zip.tmp.bak", "young.zip.tmp"}
	if got := left(t, f.dir); !slices.Equal(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

// A zero or negative core.diagnostics.retention is the 24 h default, the rule
// OpenBundle and the download route apply, never "everything is old": a bundle
// the route would still serve is never pruned from under it.
func TestPruneTask_NonPositiveRetentionIsTheDefault(t *testing.T) {
	for _, keep := range []time.Duration{0, -time.Hour, -1 << 62} {
		t.Run(keep.String(), func(t *testing.T) {
			f := newFixture(t)
			writeAged(t, f.dir, map[string]time.Duration{
				"batcave.zip":     diagnostics.DefaultRetention,
				"wayne-manor.zip": diagnostics.DefaultRetention + time.Nanosecond,
				"fresh.zip":       time.Second,
			})
			res := runPruneTask(t, pruneTaskAt(f.root, keep))
			if res.Removed != 1 || res.Retention != diagnostics.DefaultRetention.String() {
				t.Errorf("result = %+v, want 1 removed at the default %s", res, diagnostics.DefaultRetention)
			}
			if got, want := left(t, f.dir), []string{"batcave.zip", "fresh.zip"}; !slices.Equal(got, want) {
				t.Errorf("left %v, want %v", got, want)
			}
			// Prune itself applies the same rule, so no caller can pass it a
			// zero and empty the directory.
			writeAged(t, f.dir, map[string]time.Duration{"gotham.zip": time.Second})
			if n, err := diagnostics.Prune(f.root, keep, fixedNow); err != nil || n != 0 {
				t.Errorf("Prune(%s) = %d, %v; want 0 (the default keeps a fresh bundle)", keep, n, err)
			}
		})
	}
}

// The retention is read at the start of each run: a changed value applies to
// the next pass without rebuilding the definition.
func TestPruneTask_RetentionReadEachRun(t *testing.T) {
	f := newFixture(t)
	writeAged(t, f.dir, map[string]time.Duration{"two-face.zip": 3 * time.Hour})
	keep := 4 * time.Hour
	def := diagnostics.PruneTask(f.root, func() time.Duration { return keep }, func() time.Time { return fixedNow })
	if res := runPruneTask(t, def); res.Removed != 0 {
		t.Fatalf("first pass removed %d, want 0", res.Removed)
	}
	keep = 2 * time.Hour
	if res := runPruneTask(t, def); res.Removed != 1 || res.Retention != "2h0m0s" {
		t.Fatalf("second pass = %+v, want 1 removed at 2h", res)
	}
}
