// file: internal/opscatalog/testdata/linkfixture/internal/plugins/synthbundle/synth.go
// version: 1.0.0
// guid: 591da2f3-2ab2-4486-a726-4942367216c6
// last-edited: 2026-10-16

// Package synthbundle is a link-check fixture: it defines a bundle that is in
// neither the fixture's All nor its binary. It is parsed, never compiled.
package synthbundle

import "example.test/fixture/pkg/ops"

type Deps struct{}

func Ops(d Deps) ops.Bundle { return ops.Bundle{Plugin: "synthbundle"} }
