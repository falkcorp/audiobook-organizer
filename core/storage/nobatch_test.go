// file: core/storage/nobatch_test.go
// version: 1.0.1
// guid: ddd8f03c-fb5c-414a-914c-dbc940716950
// last-edited: 2026-11-10

// An external test package: archtest imports rbac, which imports storage
// (rbac.Store takes a *storage.Batch, 05-rbac §3.4), so an in-package test of
// storage that imports archtest would close an import cycle.
package storage_test

import (
	"os"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestNoBatchByValue (COR2-12): no core package takes or holds a
// storage.Batch by value.
func TestNoBatchByValue(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	archtest.NoBatchByValue(t, root)
}
