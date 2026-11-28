// file: core/archtest/guards/testdata/scopes/exempt/exempt.go
// version: 1.0.0
// guid: 5e9b3d72-1a48-4c06-b8f3-0d7a2c61e984
// last-edited: 2026-11-28

// Package exempt names a foreign scope and is loaded under an exemption by the
// test, like core's test-support packages and examples.
package exempt

import "github.com/falkcorp/mediacore/rbac"

var r = rbac.Need("gotham:fixture")
