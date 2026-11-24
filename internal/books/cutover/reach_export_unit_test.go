// file: internal/books/cutover/reach_export_unit_test.go
// version: 0.1.0
// guid: 5c0e8f3a-6b27-4d91-a4e5-71d2c3b9f804
// last-edited: 2026-11-22

package cutover

import (
	"slices"
	"testing"
)

// TestRegisterReachDischarge_AppendsToTheRegistry: the exported hook is the only
// way the external test package can claim an obligation, so it must land in the
// slice the obligation check reads, with its covers function intact.
func TestRegisterReachDischarge_AppendsToTheRegistry(t *testing.T) {
	saved := slices.Clone(reachDischarges)
	t.Cleanup(func() { reachDischarges = saved })

	RegisterReachDischarge("F4-07-OWNER-SET", "TestReachRegisteredPath_OwnerSet", func() []string { return []string{"a", "b"} })

	if len(reachDischarges) != len(saved)+1 {
		t.Fatalf("registry holds %d discharges after one registration, want %d", len(reachDischarges), len(saved)+1)
	}
	got := reachDischarges[len(reachDischarges)-1]
	if got.Obligation != "F4-07-OWNER-SET" || got.Test != "TestReachRegisteredPath_OwnerSet" {
		t.Errorf("registered %+v", got)
	}
	if c := got.Covers(); !slices.Equal(c, []string{"a", "b"}) {
		t.Errorf("covers = %v", c)
	}
}
