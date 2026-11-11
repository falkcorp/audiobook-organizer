// file: core/archtest/testdata/guards/par/pass/pass.go
// version: 1.0.0
// guid: 7d0a84ec-ea71-44fe-9ec3-f00a20a1a0fe
// last-edited: 2026-11-10

// Package pass declares parallelism by every accepted clause.
package pass

import (
	"context"

	"github.com/falkcorp/mediacore/tasks"
)

type np = struct{}

// fromVar is (b): a variable whose every assignment is a constructor.
var fromVar = tasks.Fixed(4, "a rate-limited provider")

func pick(fast bool) tasks.Parallelism {
	if fast {
		return tasks.CPU()
	}
	return tasks.Sequential("one session")
}

// loop takes the parallelism as a parameter: (b) over every call site.
func loop(ctx context.Context, par tasks.Parallelism) error {
	_, err := tasks.ForEach(ctx, par, []int{1}, func(ctx context.Context, i int) error { return nil })
	return err
}

func run(rc *tasks.RC, p np) (np, error) {
	if _, err := tasks.RunItems(rc, tasks.CPU(), []int{1}, func(ic *tasks.ItemRC, i int) error { return nil }); err != nil {
		return np{}, err
	}
	if _, err := tasks.RunItems(rc, fromVar, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil }); err != nil {
		return np{}, err
	}
	local := tasks.Sequential("ordered writes")
	local = tasks.Fixed(2, "disk-bound")
	if _, err := tasks.RunItems(rc, local, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil }); err != nil {
		return np{}, err
	}
	if err := loop(rc.Context(), pick(true)); err != nil {
		return np{}, err
	}
	return np{}, loop(rc.Context(), tasks.FromParam("workers", 2, 8))
}

var batch = tasks.Batch("robin.batch", tasks.BatchSpec[np, int, np]{Common: tasks.Common{}, Parallelism: tasks.CPU()})
