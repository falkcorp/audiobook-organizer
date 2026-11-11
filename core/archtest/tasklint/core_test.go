// file: core/archtest/tasklint/core_test.go
// version: 1.0.2
// guid: 033d7131-0d90-4946-aecb-74c2016a00b0
// last-edited: 2026-11-10

package tasklint_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest/guards"
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
	guards.RunItemsDeclareParallelism(t, core+"/...")
	guards.ResumeContinueCheckpoints(t, nil, core+"/...") // core allows no from-zero kind
}

// TestTasklint_TestSupportOnly: no non-test core package imports
// archtest/tasklint or archtest/guards (03 §3.5 R4), and golang.org/x/tools
// is outside the graph of the runtime packages and of archtest and coretest,
// which every module's tests link.
func TestTasklint_TestSupportOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", core+"/tasks", core+"/kernel", core+"/app",
		core+"/archtest", core+"/coretest").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 20 {
		t.Fatalf("go list -deps returned %d packages; the listing is broken", len(deps))
	}
	for _, d := range deps {
		if d == core+"/archtest/tasklint" || d == core+"/archtest/guards" || strings.HasPrefix(d, "golang.org/x/tools/") {
			t.Errorf("%s is in the dependency graph of tasks, kernel, app, archtest or coretest; tasklint, guards and x/tools are test-support only and never linked by coretest", d)
		}
	}
}
