// file: core/archtest/testdata/guards/par/zero/zero.go
// version: 1.0.0
// guid: 5aeb8513-a6a3-443a-902a-f54534d2c227
// last-edited: 2026-11-10

// Package zero is 05-tasks §5T.9's R7 pair, flagged half: a
// tasks.Parallelism{} literal, a zero var, Sequential("") and Fixed(n, ""),
// plus a //mediacore:required field nobody validates.
package zero

import "github.com/falkcorp/mediacore/tasks"

var lit = tasks.Parallelism{} // want: tasks.Parallelism{} is the undeclared zero value

var unset tasks.Parallelism // want: zero tasks.Parallelism var

var seq = tasks.Sequential("") // want: tasks.Sequential needs a reason

var fixed = tasks.Fixed(4, "") // want: tasks.Fixed needs a reason

// Options marks its field required but never validates it: not clause (c).
type Options struct {
	Parallelism tasks.Parallelism //mediacore:required
}

func Use(rc *tasks.RC, o Options) error {
	_, err := tasks.RunItems(rc, o.Parallelism, []int{1}, func(ic *tasks.ItemRC, i int) error { return nil }) // want: tasks.RunItems parallelism is not provably declared
	return err
}
