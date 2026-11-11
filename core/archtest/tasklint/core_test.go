// file: core/archtest/tasklint/core_test.go
// version: 1.0.0
// guid: 033d7131-0d90-4946-aecb-74c2016a00b0
// last-edited: 2026-11-10

package tasklint_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/archtest/tasklint"
)

const core = "github.com/falkcorp/mediacore"

// TestTasklint_Core: the six rules hold over core's own packages from F2, with
// no exemptions (05-tasks §5T.8.4; core has no exemption list, so a finding
// here is a defect of the row that introduced it).
func TestTasklint_Core(t *testing.T) {
	tasklint.Run(t, tasklint.Options{Packages: []string{core + "/..."}, ModulePath: core})
}

// TestTasklint_CoreGuards runs the two type-aware guards over core (05-tasks
// §5T.3.2, §5T.3.6; 03 R7).
func TestTasklint_CoreGuards(t *testing.T) {
	archtest.RunItemsDeclareParallelism(t, core+"/...")
	archtest.ResumeContinueCheckpoints(t, core+"/...")
}

// TestTasklint_TestSupportOnly: no non-test core package imports
// archtest/tasklint (03 §3.5 R4).
func TestTasklint_TestSupportOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", core+"/tasks", core+"/kernel", core+"/app").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 20 {
		t.Fatalf("go list -deps returned %d packages; the listing is broken", len(deps))
	}
	for _, d := range deps {
		if d == core+"/archtest/tasklint" || strings.HasPrefix(d, "golang.org/x/tools/") {
			t.Errorf("%s is in the dependency graph of tasks, kernel or app; tasklint and x/tools are test-support only", d)
		}
	}
}
