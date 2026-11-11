// file: core/archtest/testdata/guards/par/fail/fail.go
// version: 1.0.0
// guid: 82d20671-5765-4655-ba60-4c0a8740d2f4
// last-edited: 2026-11-10

// Package fail declares parallelism in every way the guard refuses.
package fail

import (
	"context"

	"github.com/falkcorp/mediacore/tasks"
)

type np = struct{}

var zero tasks.Parallelism // want: zero tasks.Parallelism var

// opts is filled by callers outside the loaded packages and not required.
type opts struct{ Par tasks.Parallelism }

// mixed is assigned a constructor and a zero literal.
var mixed = tasks.CPU()

func set() { mixed = tasks.Parallelism{} } // want: tasks.Parallelism{} is the undeclared zero value

func loop(ctx context.Context, par tasks.Parallelism) error {
	_, err := tasks.ForEach(ctx, par, []int{1}, func(ctx context.Context, i int) error { return nil }) // want: tasks.ForEach parallelism is not provably declared
	return err
}

func run(rc *tasks.RC, o opts, p np) (np, error) {
	_, _ = tasks.RunItems(rc, zero, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil })                         // want: tasks.RunItems parallelism is not provably declared
	_, _ = tasks.RunItems(rc, mixed, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil })                        // want: tasks.RunItems parallelism is not provably declared
	_, _ = tasks.RunItems(rc, o.Par, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil })                        // want: tasks.RunItems parallelism is not provably declared
	_, _ = tasks.RunSeq(rc, tasks.Sequential(""), nil, tasks.Total{}, func(ic *tasks.ItemRC, i int) error { return nil }) // want: tasks.Sequential needs a reason
	_, _ = tasks.RunItems(rc, tasks.Fixed(3, "  "), []int{1}, func(ic *tasks.ItemRC, i int) error { return nil })         // want: tasks.Fixed needs a reason
	return np{}, loop(rc.Context(), zero)
}

var noPar = tasks.Batch("bane.batch", tasks.BatchSpec[np, int, np]{Common: tasks.Common{}}) // want: with no Parallelism
