// file: core/archtest/guards/testdata/par/passthrough/passthrough.go
// version: 1.0.1
// guid: fc0a663f-4fcf-41aa-9703-a1e389ccd720
// last-edited: 2026-11-10

// Package passthrough is 05-tasks §5T.9's R7 pass-through pair, accepted half:
// a tasks.Parallelism-typed field every assignment of which is a constructor
// (clause b) and a //mediacore:required options field the owner validates
// (clause c), so core's own RunItems(rc, o.Parallelism, ...) passes (COR-23).
package passthrough

import (
	"errors"

	"github.com/falkcorp/mediacore/tasks"
)

type np = struct{}

// plan is (b): its Parallelism field is only ever set to a constructor.
type plan struct{ Parallelism tasks.Parallelism }

func newPlan() plan { return plan{Parallelism: tasks.Fixed(8, "index shards")} }

// SearchOptions is (c): filled by callers, required and validated here.
type SearchOptions struct {
	//mediacore:required
	Parallelism tasks.Parallelism
}

func (o SearchOptions) validate() error {
	if err := o.Parallelism.Validate(); err != nil {
		return errors.Join(errors.New("search options"), err)
	}
	return nil
}

func Search(rc *tasks.RC, o SearchOptions) error {
	if err := o.validate(); err != nil {
		return err
	}
	_, err := tasks.RunItems(rc, o.Parallelism, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil })
	if err != nil {
		return err
	}
	pl := newPlan()
	_, err = tasks.RunItems(rc, pl.Parallelism, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil })
	return err
}
