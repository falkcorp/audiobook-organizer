// file: core/coretest/crashmatrix/identity_rows_test.go
// version: 0.1.0
// guid: c25adea6-9bcd-4aea-8289-632d6b0705d6
// last-edited: 2026-11-20

package crashmatrix

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
)

// TestIdentityBootLabelsAreUnboundRowsNamingF408: every crash-matrix label the
// identity boot's store commits carry (F4-08 A5: the re-apply, the restore
// hooks' run ID, auth.BootWrites, rbac.CommitSeeds) is in a row of the writer
// table, each such row is Unbound with a reason naming F4-08, and no label is
// in two rows. A label left out of the table is a region the matrix never
// kills; a bound row before F4-08 wires the boot is a cell that can never be
// reached, which Unobserved would report as missing on every run.
func TestIdentityBootLabelsAreUnboundRowsNamingF408(t *testing.T) {
	want := slices.Concat(kernel.ReapplyLabels(), kernel.HookRunLabels(), auth.BootWritesLabels(), []string{rbac.LabelCommitSeeds})
	rowOf := map[string][]Row{}
	for _, r := range Rows() {
		for _, l := range r.Labels {
			rowOf[l] = append(rowOf[l], r)
		}
	}
	for _, l := range want {
		rows := rowOf[l]
		switch {
		case len(rows) == 0:
			t.Errorf("label %q is in no row of the writer table", l)
		case len(rows) > 1:
			t.Errorf("label %q is in %d rows", l, len(rows))
		case rows[0].Unbound == "" || !strings.Contains(rows[0].Unbound, "F4-08"):
			t.Errorf("label %q is in row %q, Unbound %q: want Unbound naming F4-08 until F4-08 wires the boot", l, rows[0].Writer, rows[0].Unbound)
		}
	}
	seen := map[string]bool{}
	for _, l := range want {
		if seen[l] {
			t.Errorf("label %q is declared twice", l)
		}
		seen[l] = true
	}
}
