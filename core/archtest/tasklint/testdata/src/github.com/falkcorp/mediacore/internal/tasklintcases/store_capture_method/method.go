// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/store_capture_method/method.go
// version: 2.0.0
// guid: 7e318b87-00f4-457e-9273-58fe640b24f8
// last-edited: 2026-11-10

// Package store_capture_method names a method as a body: its receiver is
// captured when the definition is built.
package store_capture_method

import (
	"github.com/falkcorp/mediacore/internal/tasklintcases/store"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

type plugin struct {
	db    store.Store
	books store.BookReader
}

func (p *plugin) run(rc *tasks.RC, params tasks.NoParams) (tasks.NoResult, error) {
	if _, err := p.books.GetBook("dick"); err != nil { // narrow: fine
		return tasks.NoResult{}, err
	}
	_, err := p.db.GetBook("jason") // want `Run captures p.db, a store, through p`
	return tasks.NoResult{}, err
}

// read takes the store as a parameter: a parameter is handed in, not captured.
func read(db store.Store) error { _, err := db.GetBook("tim"); return err }

func Register(r *module.Registrar, p *plugin) {
	r.Tasks(
		tasks.Task("method.run", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
			Common: tasks.Common{Title: "t", Help: "synthetic"}, Run: p.run}),
		tasks.Task("method.param", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
			Common: tasks.Common{Title: "t", Help: "synthetic"},
			Run: func(rc *tasks.RC, params tasks.NoParams) (tasks.NoResult, error) {
				return tasks.NoResult{}, read(nil)
			}}),
	)
}
