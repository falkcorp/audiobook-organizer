// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/unbundled_def/unbundled.go
// version: 1.0.0
// guid: 705d94f0-04f3-4c33-b047-d377eaebfebe
// last-edited: 2026-10-16

// Package unbundled_def: one def is in the bundle, two are not.
package unbundled_def

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

type Deps struct{}

var common = ops.Common{Title: "t", Help: "synthetic", Permission: "settings.manage"}

var listed = ops.Task("synthetic.listed", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: common})

// viaHelper is reached through a helper Ops calls.
func viaHelper() ops.Definition {
	return ops.Batch("synthetic.via-helper", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: common, Concurrency: ops.Sequential("a single remote session")})
}

var forgotten = ops.Task("synthetic.forgotten", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: common}) // want `ops.Task definition is not in the package's bundle \(Ops does not reach forgotten\)`

func orphan() ops.Definition {
	return ops.Fixer("synthetic.orphan", ops.FixerSpec[ops.NoParams, string]{Common: common}) // want `ops.Fixer definition is not in the package's bundle \(Ops does not reach orphan\)`
}

func Ops(d Deps) ops.Bundle {
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{listed, viaHelper()}}
}
