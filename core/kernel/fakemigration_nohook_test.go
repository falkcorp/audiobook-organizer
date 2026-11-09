// file: core/kernel/fakemigration_nohook_test.go
// version: 1.0.0
// guid: f9c7871d-a1e4-465b-bbaa-9c3848f67513
// last-edited: 2026-11-09

//go:build !unitlifecycle

package kernel

import (
	"go/build"
	"slices"
	"testing"
)

// TestFakeMigration_AbsentWithoutTheTag: without the unitlifecycle tag the
// default build compiles the no-hook twin and not the hook, and the variable
// that drives the hook does nothing, so no release binary can be made to sleep
// through its boot (03 §3.5.1 item 4).
func TestFakeMigration_AbsentWithoutTheTag(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("go/build: %v", err)
	}
	if !slices.Contains(pkg.GoFiles, "fakemigration_nohook.go") {
		t.Errorf("GoFiles %v lack fakemigration_nohook.go", pkg.GoFiles)
	}
	if slices.Contains(pkg.GoFiles, "fakemigration_hook.go") {
		t.Errorf("GoFiles %v hold fakemigration_hook.go without the unitlifecycle tag", pkg.GoFiles)
	}
	t.Setenv("MEDIACORE_FAKE_MIGRATION", "12m")
	if got := fakeMigrationSteps(); len(got) != 0 {
		t.Fatalf("the untagged build made %d fake steps, want none", len(got))
	}
}
