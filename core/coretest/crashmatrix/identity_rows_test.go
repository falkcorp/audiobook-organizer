// file: core/coretest/crashmatrix/identity_rows_test.go
// version: 0.1.0
// guid: c25adea6-9bcd-4aea-8289-632d6b0705d6
// last-edited: 2026-11-20

package crashmatrix

import (
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
)

// TestIdentityBootLabelsAreBoundRowsAllowedUntilAFixtureDeclaresIdentity: every
// crash-matrix label the identity boot's store commits carry (F4-08 A5: the
// re-apply, the restore hooks' run ID, auth.BootWrites, rbac.CommitSeeds) is in
// exactly one row of the writer table, the row is bound (F4-08 B3 wired the
// boot, so no row is Unbound), and IdentityUnreachedLabels allows the label
// until a fixture declares the flip. A label left out of the table is a region
// the matrix never kills; one left out of the allow set fails every fixture's
// coverage test.
func TestIdentityBootLabelsAreBoundRowsAllowedUntilAFixtureDeclaresIdentity(t *testing.T) {
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
		case rows[0].Unbound != "":
			t.Errorf("label %q is in row %q, Unbound %q: the boot is wired, so the row is bound", l, rows[0].Writer, rows[0].Unbound)
		}
		if _, ok := IdentityUnreachedLabels()[l]; !ok {
			t.Errorf("label %q is not in IdentityUnreachedLabels, so every fixture that does not declare identity fails its coverage test", l)
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
