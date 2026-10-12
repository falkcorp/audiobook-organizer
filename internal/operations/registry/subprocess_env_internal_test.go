// file: internal/operations/registry/subprocess_env_internal_test.go
// version: 1.0.0
// guid: 7c1d9e42-5a3b-4f68-b0e7-2d4a6c8e1f35
// last-edited: 2026-10-11

package registry

import (
	"slices"
	"testing"
)

func TestChildBaseEnv_StripsPprofAddr(t *testing.T) {
	parent := []string{"PATH=/bin", "ABK_PPROF_ADDR=localhost:6060", "ABK_PPROF_ADDR_OTHER=keep", "HOME=/h"}
	got := childBaseEnv(parent)
	want := []string{"PATH=/bin", "ABK_PPROF_ADDR_OTHER=keep", "HOME=/h"}
	if !slices.Equal(got, want) {
		t.Fatalf("childBaseEnv = %v, want %v", got, want)
	}
	if len(parent) != 4 || parent[1] != "ABK_PPROF_ADDR=localhost:6060" {
		t.Fatalf("childBaseEnv mutated its input: %v", parent)
	}
}

// The name is repeated from package main's pprof_debug.go (pprofAddrEnv); a
// rename there must be mirrored here or children would inherit the listener.
func TestChildBaseEnv_PprofNameMatchesRootPackage(t *testing.T) {
	if envPprofAddr != "ABK_PPROF_ADDR" {
		t.Fatalf("envPprofAddr = %q", envPprofAddr)
	}
}
