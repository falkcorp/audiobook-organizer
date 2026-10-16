// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/store_in_deps/deps.go
// version: 1.0.0
// guid: e2cfd53d-17ac-4105-8c89-1df3557fad92
// last-edited: 2026-10-16

// Package store_in_deps puts the wide store where a narrow interface belongs.
package store_in_deps

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// wide embeds the whole store under a local name.
type wide interface {
	database.Store
}

type Deps struct {
	Store    database.Store        // want `Deps field Store is database.Store or implements it`
	Wide     wide                  // want `Deps field Wide is database.Store or implements it`
	Concrete *database.PebbleStore // want `Deps field Concrete is database.Store or implements it`
	Books    database.BookReader   // narrow: fine
}

var global database.Store

var common = ops.Common{Title: "t", Help: "synthetic", Permission: "settings.manage"}

func Ops(d Deps) ops.Bundle {
	t := ops.Task("synthetic.capture", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: common,
		Run: func(rc *ops.RC, p ops.NoParams) (ops.NoResult, error) {
			_, err := global.GetBook("synthetic-1") // want `definition captures global, which is database.Store or implements it`
			return ops.NoResult{}, err
		}})
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{t}}
}
