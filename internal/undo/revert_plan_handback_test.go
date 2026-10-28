// file: internal/undo/revert_plan_handback_test.go
// version: 1.0.0
// guid: c41e7a9d-6b25-4f03-8d1a-92e5b07f3c68
// last-edited: 2026-10-28

package undo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireRowOf is the book_file_retire row an Apply journals for one file of
// shell (BookID is the holder; OldValue names the shell the row left).
func retireRowOf(id, shell, holder string) *database.OperationChange {
	return &database.OperationChange{ID: id, OperationID: "op-retire", BookID: holder,
		ChangeType: ChangeTypeBookFileRetire, FieldName: "book_file:" + id,
		OldValue: `{"book_id":"` + shell + `","version_id":""}`,
		NewValue: `{"book_id":"` + holder + `","version_id":"rv-` + shell + `"}`}
}

func handBackRows(shell, holder string) (ext, link *database.OperationChange) {
	ext = &database.OperationChange{ID: "ext-" + shell, OperationID: "op-retire", BookID: shell,
		ChangeType: ChangeTypeExternalIDReassign, FieldName: "external_id:asin/B000BATMAN",
		OldValue: shell, NewValue: holder}
	link = &database.OperationChange{ID: "link-" + shell, OperationID: "op-retire", BookID: shell,
		ChangeType: ChangeTypeBookMergedInto, OldValue: "", NewValue: holder}
	return ext, link
}

func orderIDs(p *RevertPlan) []string {
	out := make([]string, 0, len(p.Order))
	for _, c := range p.Order {
		out = append(out, c.ID)
	}
	return out
}

// The fixer journals a shell's retire rows BEFORE its id move and its link, so
// newest-first reverted the hand-back rows first. They run last now, and Gate
// refuses them unless every retire row of their shell was recorded restored: the
// id and the link go back together with the files or not at all.
func TestRevertPlan_HandBackRowsWaitForEveryRetireRowOfTheShell(t *testing.T) {
	r1, r2 := retireRowOf("r1", "robin", "batman"), retireRowOf("r2", "robin", "batman")
	ext, link := handBackRows("robin", "batman")
	other := retireRowOf("o1", "alfred", "batman")
	extOther, _ := handBackRows("alfred", "batman")
	rows := []*database.OperationChange{r1, r2, ext, link, other, extOther}

	plan, err := PlanRevert(rows, nil)
	require.NoError(t, err)
	order := orderIDs(plan)
	require.Equal(t, []string{"o1", "r2"}, order[:2], "retire rows first, newest first: %v", order)
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, hb := range []string{"ext-robin", "link-robin"} {
		require.Greater(t, pos[hb], pos["r1"], "%s after r1: %v", hb, order)
		require.Greater(t, pos[hb], pos["r2"], "%s after r2: %v", hb, order)
	}
	require.Greater(t, pos["ext-alfred"], pos["o1"], "%v", order)

	// Only r2 restored: robin's hand-back is refused, alfred's (all restored) is not.
	plan.Record(other, nil)
	plan.Record(r2, nil)
	plan.Record(r1, &ReferentError{Reason: ReasonChangedSince, Detail: "re-homed since"})
	for _, c := range []*database.OperationChange{ext, link} {
		err := plan.Gate(c)
		require.Equal(t, ReasonDependentNotReverted, RefusalReason(err), "%s: %v", c.ID, err)
		require.Contains(t, err.Error(), "r1")
	}
	require.NoError(t, plan.Gate(extOther))

	// Every retire row restored: the hand-back rows may run.
	plan2, err := PlanRevert(rows, nil)
	require.NoError(t, err)
	for _, c := range []*database.OperationChange{other, r2, r1} {
		plan2.Record(c, nil)
	}
	require.NoError(t, plan2.Gate(ext))
	require.NoError(t, plan2.Gate(link))
}

// A shell the operation holds no (non-voided, readable) retire row for keeps
// the old behaviour: the rows are not held back, not gated, and the preflight
// checks the store (PredictNotRetiredShellReceiver).
func TestRevertPlan_HandBackWithoutARetireRowIsNotGated(t *testing.T) {
	voided := retireRowOf("rv", "robin", "batman")
	voided.Voided = true
	garbled := retireRowOf("rg", "robin", "batman")
	garbled.OldValue = "robin" // not the JSON a retire journals
	ext, link := handBackRows("robin", "batman")
	for name, rows := range map[string][]*database.OperationChange{
		"no retire row": {ext, link}, "voided": {voided, ext, link}, "unparsable": {garbled, ext, link},
	} {
		plan, err := PlanRevert(rows, nil)
		require.NoError(t, err, name)
		require.NoError(t, plan.Gate(ext), name)
		require.NoError(t, plan.Gate(link), name)
		require.Empty(t, plan.shellRetires, name)
	}
}
