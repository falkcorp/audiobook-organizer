// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/unbundled_def_nobundle/nobundle.go
// version: 1.0.0
// guid: 9f0307f0-44c6-40e8-9910-3bb995d26b79
// last-edited: 2026-10-16

// Package unbundled_def_nobundle builds a def and declares no Ops at all.
package unbundled_def_nobundle

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

var Lonely = ops.Pipeline("synthetic.lonely", ops.PipelineSpec[ops.NoParams]{Common: ops.Common{Help: "synthetic"}}) // want `ops.Pipeline definition is not in the package's bundle \(this package declares no Ops\(Deps\) ops.Bundle\)`
