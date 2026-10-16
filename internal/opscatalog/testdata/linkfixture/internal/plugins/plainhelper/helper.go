// file: internal/opscatalog/testdata/linkfixture/internal/plugins/plainhelper/helper.go
// version: 1.0.0
// guid: 57e8a76a-b1ac-41f8-800f-d35ff851147a
// last-edited: 2026-10-16

// Package plainhelper is a link-check fixture with no bundle and no service
// registration: the check must ignore it, including its method named Ops.
package plainhelper

import "example.test/fixture/pkg/ops"

type T struct{}

func (T) Ops() ops.Bundle { return ops.Bundle{} }

func Helper() int { return 1 }
