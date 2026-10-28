// file: internal/plugins/maintenance/retired_versions_swap_hold_test.go
// version: 1.0.0
// guid: e27b94c1-3d58-4a60-b9f2-1c8d05a7e346
// last-edited: 2026-10-28

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// stickSwap leaves a version of the named book in status, as a crashed swap does.
func (rf *retiredFixture) stickSwap(name, status string) *database.BookVersion {
	rf.t.Helper()
	v, err := rf.st.CreateBookVersion(&database.BookVersion{ID: "stuck-" + name, BookID: rf.ids[name], Status: status})
	require.NoError(rf.t, err)
	return v
}

// S1 of the review of #4236: neither the fixer nor RetireBookFiles looked for a
// version stuck in swapping_in/swapping_out (TODO-SWAP-STUCK-RECOVERY). A shell
// whose swap did not finish is planned held (retry_later), under the fingerprint
// it would have without the hold, so the same plan applies once the swap is
// finished; nothing is written meanwhile.
func TestRetiredVersions_ShellWithAnUnfinishedSwapIsHeldNotRetired(t *testing.T) {
	for _, status := range []string{database.BookVersionStatusSwappingIn, database.BookVersionStatusSwappingOut} {
		t.Run(status, func(t *testing.T) {
			rf := newRetiredFixture(t)
			rf.enableApply()
			rf.seedApplyLibrary()
			shell := rf.ids["would-retire"]

			// Planned clean, then a swap of the shell stops half way: the re-plan holds it.
			clean := rf.planAll()
			require.True(t, rf.retiredPlanRow(clean, "would-retire").Applicable())
			stuck := rf.stickSwap("would-retire", status)
			out := rf.applyRows(rf.f, clean, []string{shell}, false, "op-held")
			got := retiredOutcomeOf(out, shell)
			require.Equal(t, repairs.OutcomeRetryLater, got.Outcome, "%+v", got)
			require.Equal(t, retCatSwapUnfinished, got.Skipped)
			require.Zero(t, out.Applied)
			require.Zero(t, rf.journalByType("op-held")[undo.ChangeTypeBookFileRetire], "nothing written")
			held, err := rf.st.GetRetiredBookFiles(rf.ids["primary"])
			require.NoError(t, err)
			require.Empty(t, held)

			// A plan made while it is stuck lists the row held, not applicable.
			row := rf.retiredPlanRow(rf.planAll(), "would-retire")
			require.False(t, row.Applicable())
			require.Equal(t, retCatSwapUnfinished, row.Skipped)
			require.True(t, row.RetryLater)
			require.Contains(t, row.SkipReason, "stuck-would-retire")

			// The swap finishes: the plan made before the hold applies.
			stuck.Status = database.BookVersionStatusAlt
			require.NoError(t, rf.st.UpdateBookVersion(stuck))
			out = rf.applyRows(rf.f, clean, []string{shell}, false, "op-after")
			require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
			held, err = rf.st.GetRetiredBookFiles(rf.ids["primary"])
			require.NoError(t, err)
			require.NotEmpty(t, held)
		})
	}
}
