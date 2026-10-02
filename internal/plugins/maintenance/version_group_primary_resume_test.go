// file: internal/plugins/maintenance/version_group_primary_resume_test.go
// version: 1.0.0
// guid: 4e9a1c37-5b2d-4f86-a0e3-8c71d2b9f564
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

func runApplyOpResume(t *testing.T, p *Plugin, planOpID string, rowIDs []string, resume *repairs.ApplyCheckpoint) (*repairs.ApplyResult, error) {
	t.Helper()
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: vgPrimaryFixerID, PlanOpID: planOpID, RowIDs: rowIDs,
		DryRun: &no, Resume: resume})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: "op-apply"}
	runErr := p.runRepairsApply(context.Background(), params, rep)
	res, _ := rep.result.(*repairs.ApplyResult)
	return res, runErr
}

func explicitTrue(t *testing.T, f *vgRepairFixture, ids ...string) []string {
	t.Helper()
	var out []string
	for _, id := range ids {
		if f.flag(t, id) == "true" {
			out = append(out, id)
		}
	}
	return out
}

// The lease lapses after the winner is crowned and before B is demoted:
// three renewals succeed (row start, pre-Apply, the winner's Modify) and the
// fourth, B's demotion, is refused. The group is left with two explicit
// primaries. Before the fix Apply stringified the Writer's error, the engine
// saw no ErrStandDownLost, the row settled as `failed` and a resume never
// re-ran it, so the double stayed. Now the row is aborted (not settled) and
// the resumed apply recognises its own half-write and finishes the group.
func TestRepairsOps_LeaseLostAfterWinnerResumesToOnePrimary(t *testing.T) {
	scan := &scriptedScan{renewsLeft: 3}
	f, p, ops := newRepairsFixture(t, scan)
	runPlanOp(t, p, ops, "op-plan")

	res, err := runApplyOpResume(t, p, "op-plan", []string{"vg-double"}, nil)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.NotNil(t, res)
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	require.Equal(t, repairs.OutcomeAborted, row.Outcome, "lease loss must not settle the row: %+v", row)
	require.Contains(t, row.Error, "partially applied", "the winner was written before the refusal")
	require.Equal(t, []string{"A", "B"}, explicitTrue(t, f, "A", "B", "C"), "half-written: A crowned, B not demoted")
	a, err := f.s.GetBookByID("A")
	require.NoError(t, err)
	require.Equal(t, "donor description", *a.Description, "the winner's carry-over landed with the crown")

	// Resume (the checkpoint carries the aborted row, which is not settled).
	scan.mu.Lock()
	scan.renewsLeft = -1
	scan.mu.Unlock()
	res2, err := runApplyOpResume(t, p, "op-plan", []string{"vg-double"}, &repairs.ApplyCheckpoint{Settled: res.Rows})
	require.NoError(t, err)
	require.Equal(t, 1, res2.Applied, "%+v", res2.Rows)
	require.Equal(t, []string{"A"}, explicitTrue(t, f, "A", "B", "C"), "exactly one primary after the resume")
	require.Equal(t, "false", f.flag(t, "B"))
	require.Equal(t, "false", f.flag(t, "C"))
}

// A change the fixer did not make is still refused: B set false by another
// writer (no history row from this fixer) after the half-write is not
// mistaken for this row's own demotion.
func TestRepairsOps_ResumeRefusesAChangeTheFixerDidNotMake(t *testing.T) {
	scan := &scriptedScan{renewsLeft: 3}
	f, p, ops := newRepairsFixture(t, scan)
	runPlanOp(t, p, ops, "op-plan")
	res, err := runApplyOpResume(t, p, "op-plan", []string{"vg-double"}, nil)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	_, err = f.s.ModifyBook("B", func(b *database.Book) error {
		v := false
		b.IsPrimaryVersion = &v
		return nil
	})
	require.NoError(t, err)
	scan.mu.Lock()
	scan.renewsLeft = -1
	scan.mu.Unlock()
	res2, err := runApplyOpResume(t, p, "op-plan", []string{"vg-double"}, &repairs.ApplyCheckpoint{Settled: res.Rows})
	require.NoError(t, err)
	require.Equal(t, 1, res2.ChangedSincePlan, "%+v", res2.Rows)
	require.Equal(t, "nil", f.flag(t, "C"), "nothing more written")
}
