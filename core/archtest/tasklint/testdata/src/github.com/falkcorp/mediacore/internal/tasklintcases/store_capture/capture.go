// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/store_capture/capture.go
// version: 2.0.0
// guid: e2cfd53d-17ac-4105-8c89-1df3557fad92
// last-edited: 2026-11-10

// Package store_capture captures the wide store in definition bodies.
package store_capture

import (
	"github.com/falkcorp/mediacore/internal/tasklintcases/store"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

// wide embeds the whole store under a local name.
type wide interface {
	store.Store
}

// Deps declares wide fields; a declaration alone is not a capture, a body
// reaching one is.
type Deps struct {
	Store    store.Store
	Wide     wide
	Concrete *store.PebbleStore
	Books    store.BookReader // narrow: fine
}

var global store.Store

var common = tasks.Common{Title: "t", Help: "synthetic"}

// lookup is a same-package helper a body calls: one level deep.
func lookup(id string) error {
	_, err := global.GetBook(id) // want `Source captures global, a store, through global \(via a function it calls\)`
	return err
}

func Register(r *module.Registrar, d Deps, s store.Store) {
	r.Tasks(
		tasks.Task("capture.task", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common,
			Run: func(rc *tasks.RC, p tasks.NoParams) (tasks.NoResult, error) {
				_, err := global.GetBook("bruce") // want `Run captures global, a store, through global`
				return tasks.NoResult{}, err
			}}),
		tasks.Batch("capture.batch", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common,
			Parallelism: tasks.CPU(),
			Source: func(rc *tasks.RC, p tasks.NoParams) (tasks.Source[string], error) {
				return nil, lookup("selina")
			},
			Item: func(ic *tasks.ItemRC, p tasks.NoParams, id string) error {
				if _, err := d.Books.GetBook(id); err != nil { // narrow: fine
					return err
				}
				if _, err := d.Wide.GetBook(id); err != nil { // want `Item captures d.Wide, a store, through d`
					return err
				}
				local := &store.PebbleStore{} // declared inside the body: not a capture
				_, err := local.GetBook(id)
				return err
			},
			Finish: func(rc *tasks.RC, p tasks.NoParams, sum tasks.Summary) (tasks.NoResult, error) {
				_, err := d.Concrete.GetBook("alfred") // want `Finish captures d.Concrete, a store, through d`
				return tasks.NoResult{}, err
			}}),
		tasks.Fixer("capture.fixer", tasks.FixerSpec[tasks.NoParams, string]{Common: common,
			Parallelism: tasks.CPU(),
			Evaluate: func(ic *tasks.ItemRC, p tasks.NoParams, c string) (tasks.Row, error) {
				_, err := s.GetBook(c) // want `Evaluate captures s, a store, through s`
				return tasks.Row{}, err
			}}),
	)
}
