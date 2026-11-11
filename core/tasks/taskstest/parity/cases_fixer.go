// file: core/tasks/taskstest/parity/cases_fixer.go
// version: 1.0.1
// guid: 10ba4b66-7bc7-4bd3-941e-d63df81b5a44
// last-edited: 2026-11-10

package parity

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	relocateKind = "arkham.relocate"
	scopeRun     = rbac.Permission("arkham:run")
	scopeApprove = rbac.Permission("arkham:approve")
)

// inmates are the 20 rows of the fixer case.
func inmates() []string {
	return []string{
		"joker", "penguin", "riddler", "two-face", "scarecrow", "bane", "clayface", "mr-freeze", "poison-ivy", "harley-quinn",
		"killer-croc", "ras-al-ghul", "hush", "black-mask", "mad-hatter", "firefly", "deadshot", "man-bat", "ventriloquist", "calendar-man",
	}
}

// cellBlock is the world the fixer reads and writes: inmate -> cell.
type cellBlock struct {
	mu    sync.Mutex
	cells map[string]string
}

func newCellBlock() *cellBlock {
	c := &cellBlock{cells: map[string]string{}}
	for _, n := range inmates() {
		c.cells[n] = "a-wing"
	}
	return c
}

func (c *cellBlock) get(n string) string { c.mu.Lock(); defer c.mu.Unlock(); return c.cells[n] }
func (c *cellBlock) set(n, v string)     { c.mu.Lock(); c.cells[n] = v; c.mu.Unlock() }

func proposed(n string) string { return "arkham-" + n }

// relocateDef is the Fixer: one row per inmate, whose Inputs carry the
// current cell (as HEAD's rows carry the current field), so a row someone
// else changed has a new fingerprint. Apply moves the inmate and, once,
// parks on the block row without writing it until the run's context ends
// (the stop), as HEAD's test does.
func relocateDef(world *cellBlock, block string, once *sync.Once, parked chan struct{}, after func([]string)) tasks.Definition {
	return tasks.Fixer(relocateKind, tasks.FixerSpec[Params, string]{
		Common: baseCommon(relocateKind, []DefOpt{func(c *tasks.Common) {
			c.Perms.Approve = rbac.Need(scopeApprove)
			c.Approval = &tasks.Approval{PlanRequired: true}
		}}),
		Candidates: func(*tasks.RC, Params) (tasks.Source[string], error) {
			return tasks.Source[string]{Items: inmates(), Key: func(s string) string { return s }}, nil
		},
		Load: func(_ *tasks.RC, _ Params, id string) (string, bool, error) {
			return id, slices.Contains(inmates(), id), nil
		},
		Evaluate: func(_ *tasks.ItemRC, _ Params, n string) (tasks.Row, error) {
			cur := world.get(n)
			return tasks.Row{ID: n,
				Inputs:   map[string]string{"cell": cur},
				Current:  map[string]string{"cell": cur},
				Proposed: map[string]string{"cell": proposed(n)}}, nil
		},
		Apply: func(ic *tasks.ItemRC, _ tasks.Writer, _ Params, row tasks.Row) error {
			if row.ID == block {
				parkedNow := false
				once.Do(func() { parkedNow = true })
				if parkedNow {
					send(parked, struct{}{})
					<-ic.Context().Done()
					return context.Cause(ic.Context())
				}
			}
			world.set(row.ID, proposed(row.ID))
			return nil
		},
		Parallelism: tasks.Sequential("one cell block at a time"),
		AfterApply: func(_ *tasks.RC, applied []string) error {
			after(slices.Clone(applied))
			return nil
		},
	})
}

func fixerCase() Case {
	return Case{
		Name: "FixerResumeReportsWritesOfEveryAttempt", Head: "fixer_resume_real_writer_test.go:TestFixerResumeRealWriter",
		Setup: "a Fixer over 20 rows (applied in row-ID order) is planned, approved and applied; the apply run is stopped by Shutdown while parked on " +
			"row ras-al-ghul, which it has not written; between attempts a foreign actor sets scarecrow, a row after the stop, to its proposed value; " +
			"the undo-then-reset property needs a ChangeSink fake and is not ported",
		Action:   "reboot; the resumed attempt finishes the apply",
		Expected: "the run succeeds; AfterApply receives every row the run wrote across both attempts (those applied before the stop, from their marks, and the rest) and not the foreign row, refused as changed since the plan",
		Run: func(t *testing.T, k *Kit) {
			world := newCellBlock()
			parked := make(chan struct{}, 4)
			var once sync.Once // the park happens in the first attempt only
			var got []string
			var gmu sync.Mutex
			after := func(a []string) { gmu.Lock(); got = a; gmu.Unlock() }
			defs := func() []tasks.Definition { return Defs(relocateDef(world, inmates()[11], &once, parked, after)) }

			k.Boot(defs(), WithPrincipal())
			ref, err := k.X.Start(AsUser("robin", scopeRun), relocateKind, tasks.StartRequest{Mode: tasks.ModeRequestPreview})
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			k.WaitState(ref.ID, tasks.StateSucceeded)
			if err := k.X.Approve(AsUser("gordon", scopeRun, scopeApprove), ref.ID, tasks.ApproveRequest{Rows: inmates()}); err != nil {
				t.Fatalf("approve: %v", err)
			}
			var apply tasks.RunID
			for _, r := range k.List([]tasks.Kind{relocateKind}) {
				if r.PlanRef == ref.ID {
					apply = r.ID
				}
			}
			if apply == "" {
				t.Fatal("approving the plan created no apply run")
			}
			select {
			case <-parked:
			case <-time.After(30 * time.Second):
				t.Fatalf("the apply never reached the block row (state %s)", k.Get(apply).State)
			}
			if err := k.X.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			if r := k.Get(apply); r.State != tasks.StateInterrupted {
				t.Fatalf("apply run is %s (reason %q), want interrupted", r.State, r.Reason)
			}

			foreign := "scarecrow" // applied after the block row in row-ID order
			world.set(foreign, proposed(foreign))

			k.Boot(defs(), WithPrincipal())
			k.WaitState(apply, tasks.StateSucceeded)
			want := slices.DeleteFunc(slices.Clone(inmates()), func(n string) bool { return n == foreign })
			gmu.Lock()
			g := slices.Clone(got)
			gmu.Unlock()
			slices.Sort(g)
			slices.Sort(want)
			if !slices.Equal(g, want) {
				t.Errorf("AfterApply got %d rows %v, want %d rows (all but %s): %s", len(g), g, len(want), foreign, fmt.Sprint(slices.DeleteFunc(slices.Clone(want), func(n string) bool { return slices.Contains(g, n) })))
			}
			for _, n := range inmates() {
				if c := world.get(n); c != proposed(n) {
					t.Errorf("%s is in %q, want %q", n, c, proposed(n))
				}
			}
		},
	}
}
