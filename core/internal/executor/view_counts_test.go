// file: core/internal/executor/view_counts_test.go
// version: 1.0.0
// guid: 8d4f2b61-7a09-4c3e-b5e8-1c6a9d3f7e20
// last-edited: 2026-11-28

package executor

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 reach R2: the census a caller sees counts only the runs it may view.

func total(c map[tasks.RunState]int64) (n int64) {
	for _, v := range c {
		n += v
	}
	return n
}

// TestVisibleCounts_ExcludesRunsTheCallerCannotView (R2): a viewer's census
// leaves out the run of a View-restricted kind, which Get answers not-found
// for; an admin's includes it. Killed by VisibleCounts answering the store's
// raw Counts: the viewer's total is then 2, not 1.
func TestVisibleCounts_ExcludesRunsTheCallerCannotView(t *testing.T) {
	h := reachHarness(t, nil)
	importRuns(t, h, []tasks.RunRecord{
		doneRun("patrol-1", "gotham.patrol", tasks.StateSucceeded),
		doneRun("cowl-1", "gotham.cowl", tasks.StateSucceeded),
	})
	counts := VisibleCounts(h.x)
	got, err := counts(viewerCtx())
	if err != nil || total(got) != 1 || got[tasks.StateSucceeded] != 1 {
		t.Fatalf("viewer's counts = %v, %v; want one succeeded run", got, err)
	}
	if _, err := h.x.Get(viewerCtx(), "cowl-1"); !isErr(err, tasks.ErrRunNotFound) {
		t.Fatalf("the run the viewer's count leaves out is gettable: %v", err)
	}
	got, err = counts(adminCtx())
	if err != nil || total(got) != 2 {
		t.Fatalf("admin's counts = %v, %v; want both runs", got, err)
	}
	if got, err = counts(context.Background()); err != nil || total(got) != 2 {
		t.Fatalf("a kernel-internal caller's counts = %v, %v; want both runs", got, err)
	}
}

// TestVisibleCounts_DecidesOncePerKind (R2): over 3,000 runs of ten kinds the
// viewer's census is the sum of the visible kinds, and the checker was asked
// at most once per kind, never once per run. A per-run check puts the call
// count at 3,000 and fails the bound.
func TestVisibleCounts_DecidesOncePerKind(t *testing.T) {
	h, cc, want, kinds := bigTable(t)
	got, err := VisibleCounts(h.x)(viewerCtx())
	if err != nil {
		t.Fatal(err)
	}
	if !equalCounts(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
	if n := cc.n(); n == 0 || n > kinds {
		t.Fatalf("checker asked %d times for %d kinds over 3000 runs; want between 1 and %d", n, kinds, kinds)
	}
}

// TestVisibleCounts_FollowsTheSnapshotRule (R2 over R3): the census applies
// the same rule to a kind with no live definition as Get does, so a count
// never includes a run Get answers not-found for.
func TestVisibleCounts_FollowsTheSnapshotRule(t *testing.T) {
	h := reachHarness(t, nil)
	putSnapshot(t, h, "gotham.retired", snapshotOfDef(t, taskDef("gotham.x", okBody, withView(rbac.Need(scopeSettings)))))
	putSnapshot(t, h, "gotham.open", snapshotOfDef(t, taskDef("gotham.x", okBody)))
	importRuns(t, h, []tasks.RunRecord{
		doneRun("retired-1", "gotham.retired", tasks.StateSucceeded),
		doneRun("open-1", "gotham.open", tasks.StateSucceeded),
		doneRun("nosnap-1", "gotham.nosnap", tasks.StateSucceeded),
	})
	for _, who := range []struct {
		name string
		ctx  context.Context
		want int64
	}{{"viewer", viewerCtx(), 1}, {"admin", adminCtx(), 2}, {"kernel-internal", context.Background(), 3}} {
		got, err := VisibleCounts(h.x)(who.ctx)
		if err != nil || total(got) != who.want {
			t.Errorf("%s counts = %v, %v; want %d runs", who.name, got, err, who.want)
		}
	}
}
