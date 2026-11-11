// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/opscatalog/catalog.go
// version: 1.0.0
// guid: 00d0a969-7d92-49ce-b6fc-4f9077beae96
// last-edited: 2026-10-16

// Package opscatalog is the fixtures' catalog. Every fixture package that
// defines Ops is listed, except unregistered_bundle; the last entry names no
// package's Ops and is itself a finding.
package opscatalog

import (
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/clean"
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/deletes_owner_review"
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/missing_help"
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/sequential_empty_reason"
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/store_in_deps"
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/unbundled_def"
	"github.com/falkcorp/audiobook-organizer/internal/oplintcases/writer_bypass"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

type Deps struct{}

func wrapped(d Deps) ops.Bundle { return ops.Bundle{} }

var All = []func(Deps) ops.Bundle{
	func(Deps) ops.Bundle { return clean.Ops(clean.Deps{}) },
	func(Deps) ops.Bundle { return deletes_owner_review.Ops(deletes_owner_review.Deps{}) },
	func(Deps) ops.Bundle { return missing_help.Ops(missing_help.Deps{}) },
	func(Deps) ops.Bundle { return sequential_empty_reason.Ops(sequential_empty_reason.Deps{}) },
	func(Deps) ops.Bundle { return store_in_deps.Ops(store_in_deps.Deps{}) },
	func(Deps) ops.Bundle { return unbundled_def.Ops(unbundled_def.Deps{}) },
	func(Deps) ops.Bundle { return writer_bypass.Ops(writer_bypass.Deps{}) },
	wrapped, // want `every opscatalog.All entry must name a package's Ops function`
}
