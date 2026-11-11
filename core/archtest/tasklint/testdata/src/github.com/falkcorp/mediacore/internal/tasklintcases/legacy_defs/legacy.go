// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/legacy_defs/legacy.go
// version: 1.0.0
// guid: d57aff1a-5ffb-4d45-a17a-48a9b3d7b5c9
// last-edited: 2026-11-10

// Package legacy_defs holds an app's pre-core definitions, which -legacy-defs
// makes writer-bypass and store-capture read as definition bodies.
package legacy_defs

import (
	"github.com/falkcorp/mediacore/internal/legacyengine"
	"github.com/falkcorp/mediacore/internal/tasklintcases/store"
)

type plugin struct{ db store.Store }

func (p *plugin) run(id string) error {
	return p.db.UpdateBook(id, nil) // want `store.BookWriter.UpdateBook writes the store directly inside Run` `Run captures p.db, a store, through p`
}

func Defs(p *plugin, w store.BookWriter) []legacyengine.OperationDef {
	return []legacyengine.OperationDef{
		{ID: "legacy.method", Run: p.run},
		{ID: "legacy.lit", Run: func(id string) error {
			return w.DeleteBook(id) // want `store.BookWriter.DeleteBook writes the store directly inside Run`
		}},
		// Validate is not a body field: never read.
		{ID: "legacy.validate", Validate: func(id string) error { return w.DeleteBook(id) }},
	}
}
