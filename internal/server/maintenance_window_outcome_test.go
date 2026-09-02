// file: internal/server/maintenance_window_outcome_test.go
// version: 1.0.1
// guid: 5d81b3a7-2c64-4e19-9f70-6b2a8e15c3df
// last-edited: 2026-09-02

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestClassifyTaskOutcomeNilRowIsAborted is the regression test for the SECOND
// nil dereference in the maintenance window.
//
// The old code was:
//
//	completedOp, _ := store.GetOperationByID(taskOp.ID)
//	if completedOp != nil && completedOp.Status == "failed" {
//	    ...
//	} else {
//	    msg := completedOp.Message   // panics when completedOp is nil
//	}
//
// The nil-check on the `if` is what MADE the `else` reachable with a nil. This
// walks that path deliberately: a nil row must produce a named outcome, never a
// dereference and never a silent "task ok".
func TestClassifyTaskOutcomeNilRowIsAborted(t *testing.T) {
	outcome, detail := classifyTaskOutcome(nil)
	require.Equal(t, taskOutcomeAborted, outcome)
	require.NotEqual(t, taskOutcomeCompleted, outcome, "a nil row must never read as success")
	require.NotEmpty(t, detail)
}

// TestClassifyTaskOutcomeStatusMapping pins the reporting policy against what
// the resume machinery does with each status, not against how the name sounds.
func TestClassifyTaskOutcomeStatusMapping(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		want    taskOutcome
		because string
	}{
		{"completed", "completed", taskOutcomeCompleted, "finished its work"},
		{"failed", "failed", taskOutcomeFailed, "explicit failure"},
		{
			"interrupted_dropped is a failure", "interrupted_dropped", taskOutcomeFailed,
			"ResumeDrop abandons the op on restart (registry/types.go:183) — the work is discarded and never resumed",
		},
		{
			"interrupted_quiesced is not a failure", "interrupted_quiesced", taskOutcomeIncomplete,
			"returned for every ResumePolicy except ResumeDrop and is the NORMAL restart outcome; library.scan ends this way on nearly every run, so calling it a failure would alarm nightly",
		},
		{"canceled", "canceled", taskOutcomeIncomplete, "deliberate stop, not a defect"},
		{"unknown status", "something_new", taskOutcomeIncomplete, "an unrecognised status must not be reported as success"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyTaskOutcome(&database.OperationV2Row{Status: tc.status})
			require.Equal(t, tc.want, got, tc.because)
		})
	}
}

// TestClassifyTaskOutcomeDetailPrefersErrorMessage pins that a failure reports
// the error rather than the last progress line, which is what lands in the
// nightly summary an operator actually reads.
func TestClassifyTaskOutcomeDetailPrefersErrorMessage(t *testing.T) {
	_, detail := classifyTaskOutcome(&database.OperationV2Row{
		Status:          "failed",
		ProgressMessage: "processed 40/100",
		ErrorMessage:    new("disk full"),
	})
	require.Equal(t, "disk full", detail)

	_, detail = classifyTaskOutcome(&database.OperationV2Row{
		Status:          "completed",
		ProgressMessage: "processed 100/100",
		ErrorMessage:    new(""),
	})
	require.Equal(t, "processed 100/100", detail, "an empty error must not blank the detail")
}

// TestOpV2Percent covers the divide-by-zero the v1 Progress field never had:
// an op that has started but not yet sized its work reports total 0.
func TestOpV2Percent(t *testing.T) {
	require.Equal(t, 0, opV2Percent(nil))
	require.Equal(t, 0, opV2Percent(&database.OperationV2Row{ProgressCurrent: 5, ProgressTotal: 0}))
	require.Equal(t, 0, opV2Percent(&database.OperationV2Row{ProgressCurrent: 5, ProgressTotal: -1}))
	require.Equal(t, 50, opV2Percent(&database.OperationV2Row{ProgressCurrent: 5, ProgressTotal: 10}))
	require.Equal(t, 100, opV2Percent(&database.OperationV2Row{ProgressCurrent: 10, ProgressTotal: 10}))
}
