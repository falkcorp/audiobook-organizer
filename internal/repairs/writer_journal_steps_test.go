// file: internal/repairs/writer_journal_steps_test.go
// version: 1.0.0
// guid: 2c7f4a19-8e63-4b05-9d2a-6f13b8e0c7d4
// last-edited: 2026-10-22

package repairs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// batchJournal is a journal that can write several rows in one commit
// (ChangeBatchJournal), recording each commit and failing the ones asked to.
type batchJournal struct {
	creditFake
	commits   []int // rows per CreateOperationChanges commit
	failBatch map[int]bool
	singles   int
}

func (j *batchJournal) CreateOperationChange(c *database.OperationChange) error {
	j.mu.Lock()
	j.singles++
	j.mu.Unlock()
	return j.creditFake.CreateOperationChange(c)
}

func (j *batchJournal) CreateOperationChanges(cs []*database.OperationChange) error {
	j.mu.Lock()
	n := len(j.commits)
	j.commits = append(j.commits, len(cs))
	fail := j.failBatch[n]
	j.mu.Unlock()
	if fail {
		return errors.New("commit failed")
	}
	for _, c := range cs {
		if err := j.creditFake.CreateOperationChange(c); err != nil {
			return err
		}
	}
	return nil
}

func stepsWriter(j ChangeJournal) *Writer {
	return NewWriter(newMemStore(), newMemStore(), "junk", "bulk_update", "rp-").WithJournal(nil, j, "op-1")
}

func pairSteps(oldV string) []BookStep {
	return []BookStep{
		{BookID: "S", Entry: UndoEntry{ChangeType: "user_content_follow", Field: "collection-held:L", Old: oldV, New: "B"}},
		{BookID: "B", Entry: UndoEntry{ChangeType: "user_content_follow", Field: "collection:L", Old: oldV, New: "S"}},
	}
}

func liveRows(j *creditFake) (n int) {
	for _, r := range j.journal {
		if r.RevertedAt == nil {
			n++
		}
	}
	return n
}

// The rows of one call land in ONE commit, written before the write runs, and the
// writer's index knows them only after it landed.
func TestJournalSteps_AllRowsOfACallAreOneCommit(t *testing.T) {
	j := &batchJournal{}
	w := stepsWriter(j)
	var sawRows int
	err := w.JournalSteps(pairSteps("rec"), func() ([]bool, error) {
		sawRows = len(j.journal)
		return []bool{true, true}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, sawRows, "journal first: both rows exist before the write runs")
	require.Equal(t, []int{2}, j.commits, "one commit for the pair")
	require.Zero(t, j.singles)
	require.Equal(t, 2, w.Journaled())
	require.True(t, w.journaledBook("S"))
	require.True(t, w.journaledBook("B"))
}

// A commit that fails creates none of the rows, never calls write, and leaves the
// writer's index as it was, so the same call works once the journal does.
func TestJournalSteps_AFailedCommitLeavesNoRowAndNoIndexEntry(t *testing.T) {
	j := &batchJournal{failBatch: map[int]bool{0: true}}
	w := stepsWriter(j)
	called := false
	err := w.JournalSteps(pairSteps("rec"), func() ([]bool, error) { called = true; return []bool{true, true}, nil })
	require.ErrorIs(t, err, ErrNotJournaled)
	require.False(t, called, "no write without its rows")
	require.Empty(t, j.journal, "no row of the pair landed")
	require.Zero(t, w.Journaled())
	require.False(t, w.journaledBook("S"))
	require.NoError(t, w.JournalSteps(pairSteps("rec"), func() ([]bool, error) { return []bool{true, true}, nil }))
	require.Len(t, j.journal, 2)
}

// A journal that cannot write several rows in one commit is refused before
// anything is written: one by one, a failure part-way would leave a marker with
// no row.
func TestJournalSteps_AJournalWithoutBatchesIsRefusedUpFront(t *testing.T) {
	j := &creditFake{}
	w := stepsWriter(j)
	called := false
	err := w.JournalSteps(pairSteps("rec"), func() ([]bool, error) { called = true; return nil, nil })
	require.ErrorIs(t, err, ErrNotJournaled)
	require.ErrorIs(t, err, errBatchJournalMissing)
	require.False(t, called)
	require.Empty(t, j.journal)
	// One row needs no batch.
	require.NoError(t, w.JournalSteps(pairSteps("rec")[:1], func() ([]bool, error) { return []bool{true}, nil }))
	require.Len(t, j.journal, 1)
}

// A refused write voids every row of the call in one commit: both stand or both
// are voided.
func TestJournalSteps_ARefusedWriteVoidsThePairInOneCommit(t *testing.T) {
	j := &batchJournal{}
	w := stepsWriter(j)
	err := w.JournalSteps(pairSteps("rec"), func() ([]bool, error) { return nil, ErrChangedSincePlan })
	require.ErrorIs(t, err, ErrChangedSincePlan)
	require.Equal(t, []int{2, 2}, j.commits, "the rows in one commit, the voids in one commit")
	require.Zero(t, liveRows(&j.creditFake))
	for _, r := range j.journal {
		require.True(t, r.Voided)
		require.NotNil(t, r.RevertedAt)
	}
	require.Zero(t, w.Journaled())
	require.False(t, w.journaledBook("S"))
}

// A void that fails leaves BOTH rows standing, not one of them: the writer
// reports it, and the pair is still a pair.
func TestJournalSteps_AFailedVoidLeavesTheWholePairStanding(t *testing.T) {
	j := &batchJournal{failBatch: map[int]bool{1: true}}
	w := stepsWriter(j)
	err := w.JournalSteps(pairSteps("rec"), func() ([]bool, error) { return nil, ErrChangedSincePlan })
	require.ErrorIs(t, err, ErrChangedSincePlan)
	require.Error(t, err)
	require.Equal(t, 2, liveRows(&j.creditFake), "neither row was voided")
	for _, r := range j.journal {
		require.False(t, r.Voided)
	}
}

// A resumed run: rows already in the journal are not written again, only the
// missing ones are created. A marker left alone by an older build gets its row;
// a complete pair gets nothing, and a refused write voids only what this call
// created.
func TestJournalSteps_AResumedRunCreatesOnlyTheMissingRows(t *testing.T) {
	steps := pairSteps("rec")
	// The marker is already there (an older build journaled it and died).
	j := &batchJournal{}
	w := stepsWriter(j)
	require.NoError(t, w.JournalSteps(steps[:1], func() ([]bool, error) { return []bool{true}, nil }))
	require.Len(t, j.journal, 1)
	w2 := stepsWriter(j)
	require.NoError(t, w2.JournalSteps(steps, func() ([]bool, error) { return []bool{true, true}, nil }))
	require.Len(t, j.journal, 2, "the missing row was created, the marker not written again")
	require.Equal(t, 2, j.singles, "the first marker and the one missing row: plain creates, no batch of one")
	require.Empty(t, j.commits)
	require.Equal(t, "S", j.journal[0].BookID)
	require.Equal(t, "B", j.journal[1].BookID)
	// The same call again writes nothing.
	w3 := stepsWriter(j)
	require.NoError(t, w3.JournalSteps(steps, func() ([]bool, error) { return []bool{true, true}, nil }))
	require.Len(t, j.journal, 2)
	// A refusal of a fully resumed call voids nothing: the rows are not this call's.
	w4 := stepsWriter(j)
	require.ErrorIs(t, w4.JournalSteps(steps, func() ([]bool, error) { return nil, ErrChangedSincePlan }), ErrChangedSincePlan)
	require.Equal(t, 2, liveRows(&j.creditFake))
}

// A resumed run whose record changed (the list moved between the runs) journals
// a new pair; the old pair is left as it is, and the carry's history reads only
// the latest marker of a retire (undo.IgnoredMarkers).
func TestJournalSteps_AResumedRunWithAChangedRecordJournalsANewPair(t *testing.T) {
	j := &batchJournal{}
	require.NoError(t, stepsWriter(j).JournalSteps(pairSteps("rec-1"), func() ([]bool, error) { return []bool{true, true}, nil }))
	require.NoError(t, stepsWriter(j).JournalSteps(pairSteps("rec-2"), func() ([]bool, error) { return []bool{true, true}, nil }))
	require.Len(t, j.journal, 4)
	require.Equal(t, []int{2, 2}, j.commits)
}
