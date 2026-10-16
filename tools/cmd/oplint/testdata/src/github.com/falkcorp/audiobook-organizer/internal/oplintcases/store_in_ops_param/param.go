// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/store_in_ops_param/param.go
// version: 1.0.0
// guid: 7e318b87-00f4-457e-9273-58fe640b24f8
// last-edited: 2026-10-16

// Package store_in_ops_param passes a struct that is not named Deps to Ops.
package store_in_ops_param

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

type Inputs struct {
	DB database.Store // want `Ops parameter field DB is database.Store or implements it`
}

func Ops(in Inputs) ops.Bundle { // want `store_in_ops_param defines Ops`
	return ops.Bundle{Plugin: "synthetic"}
}
