// file: core/kernel/identity_phase_order_test.go
// version: 0.1.0
// guid: d7a4e9c2-6b18-4f35-8c0e-39f5a1b6d724
// last-edited: 2026-11-21

package kernel

import (
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// assertGroupsInOrder fails unless every group has a label among entered and
// no label of a group is entered after the first label of the next group.
func assertGroupsInOrder(t *testing.T, entered []string, groups ...[]string) {
	t.Helper()
	lastOf := func(g []string) (last int, any bool) {
		last = -1
		for i, l := range entered {
			if slices.Contains(g, l) {
				last, any = i, true
			}
		}
		return last, any
	}
	firstOf := func(g []string) (first int, any bool) {
		for i, l := range entered {
			if slices.Contains(g, l) {
				return i, true
			}
		}
		return -1, false
	}
	for i, g := range groups {
		if _, ok := firstOf(g); !ok {
			t.Fatalf("no label of group %d (%v) was entered; entered %v", i, g, entered)
		}
		if i == 0 {
			continue
		}
		prev, _ := lastOf(groups[i-1])
		next, _ := firstOf(g)
		if prev > next {
			t.Errorf("group %d (%v) was entered at %d, before group %d (%v) finished at %d; entered %v", i, g, next, i-1, groups[i-1], prev, entered)
		}
	}
}

// TestIdentityBoot_PhasesRunInTheSpecsOrder: an identity boot runs P4d's steps
// in order and P7 after them: the re-apply's served_boot, then step 4's boot
// writes, then P7's seed commit (06 section 6.5.1's writer table is in this
// order). A restore boot adds the restore line first, then the restore hooks'
// own commits, their completion record, and only then served_boot.
func TestIdentityBoot_PhasesRunInTheSpecsOrder(t *testing.T) {
	t.Run("a fresh boot", func(t *testing.T) {
		h, c := identityHost(t)
		labels := &labelLog{}
		h.ctx = bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Labels: labels})
		h.close(h.mustBoot(c))
		assertGroupsInOrder(t, labels.take(),
			[]string{"kernel.reapply.served_boot"},
			auth.BootWritesLabels(),
			[]string{rbac.LabelCommitSeeds},
		)
	})
	t.Run("a restore boot", func(t *testing.T) {
		h, c, first := restoreHost(t)
		h.close(first)
		h.stage("r-order", restorejournal.KindRevert, h.stagedCopy("r-order", "robin"))
		labels := &labelLog{}
		h.ctx = bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Labels: labels})
		h.close(h.mustBoot(c))
		assertGroupsInOrder(t, labels.take(),
			[]string{labelReapplyRestoreLine},
			[]string{labelHookAuth, labelHookAudit},
			[]string{labelHooksMarkHooked},
			[]string{labelReapplyServedBoot},
			auth.BootWritesLabels(),
			[]string{rbac.LabelCommitSeeds},
		)
	})
}
