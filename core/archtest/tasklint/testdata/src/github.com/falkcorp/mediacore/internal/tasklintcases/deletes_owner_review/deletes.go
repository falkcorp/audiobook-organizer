// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/deletes_owner_review/deletes.go
// version: 2.0.0
// guid: 77c542fd-e225-4255-866a-44c36cf134ae
// last-edited: 2026-11-10

// Package deletes_owner_review declares deletes in its Effects.
package deletes_owner_review

import (
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

func Register(r *module.Registrar) {
	c := tasks.Common{Title: "t", Help: "synthetic",
		Effects: tasks.Writes("joker.rows").AlsoDeletes("joker.rows")} // want `Effects.AlsoDeletes needs owner review`
	signed := tasks.Common{Title: "t", Help: "synthetic",
		Effects: tasks.Writes("joker.rows").AlsoDeletes("joker.rows")} //tasklint:allow synthetic fixture: owner approved deleting joker.rows
	bare := tasks.Common{Title: "t", Help: "synthetic",
		Effects: tasks.Writes("joker.rows").AlsoDeletes("joker.rows")} //tasklint:allow // want `//tasklint:allow needs a reason after it`
	r.Tasks(
		tasks.Task("deletes.unsigned", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: c}),
		tasks.Task("deletes.signed", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: signed}),
		tasks.Task("deletes.bare", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: bare}),
	)
}
