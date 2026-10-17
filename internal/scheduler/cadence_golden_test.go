// file: internal/scheduler/cadence_golden_test.go
// version: 1.0.0
// guid: 3b6e1d52-8f47-4c09-b2a1-5d7c90e4f813
// last-edited: 2026-10-17

package scheduler

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// cadenceGoldenPath is the committed record of every registered
// TaskDefinition's timer. Port PRs (05-PR8 onward) update it only for the op
// they port, so the diff shows exactly the cadence they copied.
const cadenceGoldenPath = "testdata/cadence.golden"

// cadenceLines renders one line per registered task, in registration order:
// name|interval|daily_at|run_on_start|in_window|window_index|enabled_default.
func cadenceLines(ts *TaskScheduler) []string {
	var out []string
	for _, name := range ts.order {
		t := ts.tasks[name]
		inWindow := t.RunInMaintenanceWindow != nil && t.RunInMaintenanceWindow()
		out = append(out, fmt.Sprintf("%s|%s|%s|%t|%t|%d|%t",
			name, t.GetInterval(), t.DailyAt,
			t.RunOnStart != nil && t.RunOnStart(), inWindow,
			slices.Index(ts.maintenanceOrder, name), t.IsEnabled()))
	}
	return out
}

// TestCadenceGolden proves no existing TaskDefinition's timer changed. Set
// UPDATE_CADENCE_GOLDEN=1 to regenerate after a deliberate cadence change.
func TestCadenceGolden(t *testing.T) {
	got := strings.Join(cadenceLines(NewTaskScheduler(reachabilityTestDeps())), "\n") + "\n"
	if os.Getenv("UPDATE_CADENCE_GOLDEN") == "1" {
		if err := os.WriteFile(cadenceGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(cadenceGoldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_CADENCE_GOLDEN=1)", err)
	}
	if string(want) != got {
		t.Errorf("cadence changed.\n--- golden\n%s--- got\n%s", want, got)
	}
}
