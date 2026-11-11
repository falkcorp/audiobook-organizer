// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/missing_help/help.go
// version: 2.0.0
// guid: c6caa0bc-0d38-4c45-9831-bae708916c84
// last-edited: 2026-11-10

// Package missing_help declares kinds without Help.
package missing_help

import (
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

func Register(r *module.Registrar) {
	a := tasks.Task("help.a", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
		Common: tasks.Common{Title: "a"}}) // want `warning: tasks.Common has no Help`
	b := tasks.Task("help.b", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
		Common: tasks.Common{Title: "b", Help: " "}}) // want `warning: tasks.Common has no Help`
	c := tasks.Task("help.c", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
		Common: tasks.Common{Title: "c", Help: "Says what c does."}})
	d := tasks.Task("help.d", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
		Common: tasks.Common{"d", "", tasks.Effects{}, 0}}) // want `warning: tasks.Common has no Help`
	e := tasks.Task("help.e", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
		Common: tasks.Common{"e", "Positional help.", tasks.Effects{}, 0}})
	r.Tasks(a, b, c, d, e)
}
