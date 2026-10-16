// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/unregistered_bundle/bundle.go
// version: 1.0.0
// guid: 3e6e4bfb-0bf5-440d-868f-b97e64f6e37d
// last-edited: 2026-10-16

// Package unregistered_bundle defines a bundle the fixtures' catalog does
// not list.
package unregistered_bundle

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

type Deps struct{}

func Ops(d Deps) ops.Bundle { // want `unregistered_bundle defines Ops\(...\) ops.Bundle but opscatalog.All does not reference unregistered_bundle.Ops`
	return ops.Bundle{Plugin: "synthetic"}
}
