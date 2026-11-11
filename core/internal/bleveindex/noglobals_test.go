// file: core/internal/bleveindex/noglobals_test.go
// version: 1.0.0
// guid: e42d27cb-35e6-4f35-a6b2-b217d0a02a5c
// last-edited: 2026-11-11

package bleveindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestNoGlobals (03 R5): the index packages hold no init function and no
// package-level mutable variable, bar the one allowlisted entry below.
func TestNoGlobals(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	archtest.NoInit(t, root, "./internal/bleveindex/...")
	archtest.NoInit(t, root, "./storage/index/...")
}

// TestNoGlobals_TheScorchRegistryIsTheOnlyAllowlistedEntry: the package has
// exactly one //archtest:allow-var, the mutex that guards scorch's callback
// registry, and no non-test file but callback.go names the registry (the
// app-level TestNoGlobals scan bans it elsewhere and exempts only
// asyncCallback.register and unregister).
func TestNoGlobals_TheScorchRegistryIsTheOnlyAllowlistedEntry(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	allow := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		allow += strings.Count(src, "//archtest:allow-var ")
		if strings.Contains(src, "RegistryAsyncErrorCallbacks") && f != "callback.go" {
			t.Errorf("%s names scorch's callback registry; only callback.go may", f)
		}
	}
	if allow != 1 {
		t.Fatalf("%d //archtest:allow-var entries, want exactly the one for scorchRegistryMu", allow)
	}
}
