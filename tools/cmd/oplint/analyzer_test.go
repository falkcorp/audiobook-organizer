// file: tools/cmd/oplint/analyzer_test.go
// version: 1.0.0
// guid: e8def934-7124-4f87-a67e-9f206a103d30
// last-edited: 2026-10-16

package main

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// One fixture package per rule under testdata/src/.../internal/oplintcases
// (inside the module path, so they may import the internal/database stub),
// each with // want annotations; clean must produce nothing. The stubs of
// pkg/ops, internal/database and internal/opscatalog live under their real
// import paths in the same GOPATH-style tree.
func TestAnalyzer(t *testing.T) {
	const cases = "github.com/falkcorp/audiobook-organizer/internal/oplintcases/"
	for _, pkg := range []string{
		cases + "unbundled_def",
		cases + "unbundled_def_nobundle",
		cases + "unregistered_bundle",
		cases + "writer_bypass",
		cases + "store_in_deps",
		cases + "store_in_ops_param",
		cases + "sequential_empty_reason",
		cases + "deletes_owner_review",
		cases + "missing_help",
		cases + "clean",
		"github.com/falkcorp/audiobook-organizer/internal/opscatalog",
		"github.com/falkcorp/audiobook-organizer/pkg/ops/opstest",
	} {
		t.Run(pkg, func(t *testing.T) {
			analysistest.Run(t, analysistest.TestData(), Analyzer, pkg)
		})
	}
}

// The v2 probe extends writer-bypass to registry.OperationDef Run funcs; it
// is off unless asked for.
func TestProbeV2IsOffByDefault(t *testing.T) {
	if probeV2 {
		t.Fatal("-probe-v2 is on by default; make oplint must not apply the informational probe")
	}
}

// The driver exits 1 on a failing finding and 0 when only warnings remain.
func TestDriverExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("loads packages with go list")
	}
	t.Setenv("GOPATH", analysistest.TestData())
	t.Setenv("GO111MODULE", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	t.Chdir(analysistest.TestData() + "/src")

	var out, errw bytes.Buffer
	if code := run([]string{"github.com/falkcorp/audiobook-organizer/internal/oplintcases/missing_help"}, &out, &errw); code != 0 {
		t.Fatalf("warnings only: exit %d, want 0\n%s%s", code, out.String(), errw.String())
	}
	if !strings.Contains(errw.String(), "missing-help: 2") {
		t.Fatalf("summary does not count the warnings:\n%s", errw.String())
	}
	out.Reset()
	errw.Reset()
	if code := run([]string{"github.com/falkcorp/audiobook-organizer/internal/oplintcases/sequential_empty_reason"}, &out, &errw); code != 1 {
		t.Fatalf("a failing finding: exit %d, want 1\n%s%s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "[sequential-no-reason]") {
		t.Fatalf("finding not printed with its category:\n%s", out.String())
	}
}
