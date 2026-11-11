// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/sequential_empty_reason/seq.go
// version: 2.0.0
// guid: 350ee82a-b42b-42e1-a4ee-e6b7b00d305d
// last-edited: 2026-11-10

// Package sequential_empty_reason declares Sequential with no reason.
package sequential_empty_reason

import (
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

const blank = "   "

var common = tasks.Common{Title: "t", Help: "synthetic"}

func Register(r *module.Registrar) {
	a := tasks.Batch("seq.a", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common,
		Parallelism: tasks.Sequential("")}) // want `tasks.Sequential needs a reason`
	b := tasks.Batch("seq.b", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common,
		Parallelism: tasks.Sequential(blank)}) // want `tasks.Sequential needs a reason`
	c := tasks.Batch("seq.c", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common,
		Parallelism: tasks.Sequential("one remote session at a time")})
	r.Tasks(a, b, c)
}
