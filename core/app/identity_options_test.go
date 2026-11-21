// file: core/app/identity_options_test.go
// version: 0.1.0
// guid: 3b6f1d84-92c7-4a05-b8e1-5d0a7c4f9e23
// last-edited: 2026-11-21

package app

import (
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only.

// TestKernelConfig_PlumbsResolveAtomAndDisplayName: the two values the identity
// boot reads (stage 13's atom table, the app's name in the modules listing)
// reach kernel.Config, and unset they reach it as nil and "", which is what
// the kernel had before they were plumbed.
func TestKernelConfig_PlumbsResolveAtomAndDisplayName(t *testing.T) {
	want := rbac.Need("batcave:signal")
	var asked []string
	resolve := func(atom string, readSide bool) (rbac.Requirement, bool) {
		asked = append(asked, atom)
		return want, !readSide
	}
	on, err := kernelConfig(Options{AppID: "gotham", DisplayName: "Gotham Watch", ResolveAtom: resolve}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if on.DisplayName != "Gotham Watch" {
		t.Errorf("kernel.Config.DisplayName = %q, want %q", on.DisplayName, "Gotham Watch")
	}
	if on.ResolveAtom == nil {
		t.Fatal("Options.ResolveAtom did not reach kernel.Config")
	}
	if req, ok := on.ResolveAtom("library.view", false); !ok || req.String() != want.String() {
		t.Errorf("the plumbed resolver answered %v, %v, want %v, true", req, ok, want)
	}
	if _, ok := on.ResolveAtom("library.view", true); ok {
		t.Error("the plumbed resolver is not the app's: its read-side answer was lost")
	}
	if len(asked) != 2 || asked[0] != "library.view" {
		t.Errorf("the app's resolver was asked %v, want it called through kernel.Config", asked)
	}

	off, err := kernelConfig(Options{AppID: "gotham"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if off.ResolveAtom != nil || off.DisplayName != "" {
		t.Errorf("unset options reached the kernel as ResolveAtom=%v DisplayName=%q, want nil and empty", off.ResolveAtom != nil, off.DisplayName)
	}
}
