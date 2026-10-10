// file: internal/repairs/engine_cancel_test.go
// version: 1.0.0
// guid: e2567da4-edf5-4121-ae72-9a5d4d295154
// last-edited: 2026-10-10

package repairs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// partitionFixer plans n rows that all share one book, so partitionRows puts
// them in ONE partition and one worker applies them in order. Every row
// retires its own book (a Modify on it). onWrite runs after each write with
// the count so far; the test cancels the apply from it.
type partitionFixer struct {
	n       int
	writes  atomic.Int32
	onWrite func(n int32)
}

func (f *partitionFixer) ID() string          { return "partition-retire" }
func (f *partitionFixer) Title() string       { return "Partition retire" }
func (f *partitionFixer) Description() string { return "test fixer" }

func (f *partitionFixer) row(i int) Row {
	id := fmt.Sprintf("r%02d", i)
	return Row{RowID: id, BookIDs: []string{"shared", "b" + id}, Title: id, Reason: "retire",
		Risk: RiskLow, Fingerprint: "fp:" + id}
}

func (f *partitionFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	rows := make([]Row, f.n)
	for i := range rows {
		rows[i] = f.row(i)
	}
	return rows, nil
}

func (f *partitionFixer) Replan(_ context.Context, _ json.RawMessage, planned Row, _ registry.Reporter) (Row, error) {
	var i int
	_, _ = fmt.Sscanf(planned.RowID, "r%02d", &i)
	return f.row(i), nil
}

func (f *partitionFixer) Apply(_ context.Context, w *Writer, fresh Row) error {
	if _, err := w.Modify("b"+fresh.RowID, func(b *database.Book) error {
		b.Title = "retired"
		return nil
	}); err != nil {
		return err
	}
	n := f.writes.Add(1)
	if f.onWrite != nil {
		f.onWrite(n)
	}
	return nil
}

// The 2026-10-01 shape: an apply kept retiring books after Cancel because the
// partition loop only noticed the cancel between partitions. A 10-row
// partition canceled during row 3 must stop after the row in flight: at most
// that row's write lands, and every later row is reported canceled_skipped,
// written by nobody and not checkpointed (a resume re-checks it).
//
// Red on origin/main (before 05-PR3): all 10 rows were applied.
func TestApply_CancelDuringPartitionStopsAfterInFlightRow(t *testing.T) {
	s := newMemStore()
	s.add("shared", "Shared", "/lib/A/shared/x.m4b", nil)
	f := &partitionFixer{n: 10}
	for i := range f.n {
		id := fmt.Sprintf("br%02d", i)
		s.add(id, id, "/lib/A/"+id+"/x.m4b", nil)
	}
	plan := planFor(t, s, f)
	require.Len(t, plan.Rows, 10)
	ids := make([]string, 0, 10)
	for _, r := range plan.Rows {
		ids = append(ids, r.RowID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onWrite = func(n int32) {
		if n == 3 {
			cancel()
		}
	}
	var checkpointed []RowResult
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Concurrency = 1
	d.Checkpoint = func(cp ApplyCheckpoint) error {
		checkpointed = cp.Settled
		return nil
	}
	res, _ := RunApply(ctx, f, plan, "op-plan", ids, false, d, nopReporter{})

	require.LessOrEqual(t, f.writes.Load(), int32(4), "rows kept being applied after the cancel")
	require.LessOrEqual(t, res.Applied, 4, "%+v", res.Rows)
	skipped := 0
	for _, r := range res.Rows {
		if r.Outcome == OutcomeCanceledSkip {
			skipped++
			require.False(t, settledForResume(r.Outcome), "a canceled-skip row must be retried on resume")
		}
	}
	require.Equal(t, 10-res.Applied, skipped, "every row after the cancel is canceled_skipped: %+v", res.Rows)
	require.Equal(t, skipped, res.ByOutcome[OutcomeCanceledSkip])
	for _, cp := range checkpointed {
		require.NotEqual(t, OutcomeCanceledSkip, cp.Outcome, "a canceled-skip row was checkpointed")
	}
}

// A Writer whose fence is revoked refuses Modify with the fence's error and
// never reaches ModifyBook, even with no stand-down lease installed.
func TestWriter_FencedRefusesModify(t *testing.T) {
	s := newMemStore()
	s.add("b1", "One", "/lib/A/One/one.m4b", nil)
	w := NewWriter(s, s, "t", "bulk_update", "rp-")
	w.setFence(func() error { return registry.ErrFenced })
	called := false
	_, err := w.Modify("b1", func(*database.Book) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, registry.ErrFenced)
	require.False(t, called, "ModifyBook ran for a fenced write")
	require.Equal(t, "One", s.title("b1"))
	require.Zero(t, w.Writes())

	// Cleared, the same Writer writes again.
	w.setFence(nil)
	_, err = w.Modify("b1", func(b *database.Book) error {
		b.Title = "Two"
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "Two", s.title("b1"))
}

// The engine installs the run's fence on the Writer for the whole apply,
// including a NoScanStandDown-free apply with no lease, and clears it when the
// apply returns. (The registry's own tests cover revocation; this pins the
// install.)
func TestApply_InstallsAndClearsTheRunFence(t *testing.T) {
	s := newMemStore()
	s.add("shared", "Shared", "/lib/A/shared/x.m4b", nil)
	s.add("br00", "br00", "/lib/A/br00/x.m4b", nil)
	f := &partitionFixer{n: 1}
	plan := planFor(t, s, f)
	d := deps(s, nil) // no stand-down: no lease, the fence still applies
	var sawFence atomic.Bool
	f.onWrite = func(int32) { sawFence.Store(d.Writer.fence != nil) }
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"r00"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied, "%+v", res.Rows)
	require.True(t, sawFence.Load(), "the fence was not installed during the apply")
	require.Nil(t, d.Writer.fence, "the fence was not cleared after the apply")
}

// fencedFixer's Apply writes one book, then reports the fence refusing its
// second write, as a fixer sees it from the Writer mid-row.
type fencedFixer struct{ partitionFixer }

func (f *fencedFixer) Apply(_ context.Context, w *Writer, fresh Row) error {
	if _, err := w.Modify("b"+fresh.RowID, func(b *database.Book) error {
		b.Title = "half"
		return nil
	}); err != nil {
		return err
	}
	return fmt.Errorf("%w: after 1 step: book b%s not written: %w", ErrPartiallyApplied, fresh.RowID, registry.ErrFenced)
}

// A row whose write the fence refused mid-row is canceled_skipped, not
// partially_applied or failed: it is not settled and not checkpointed, so a
// resumed apply re-runs it (and the fixer skips the step already written).
func TestApply_FencedWriteMidRowIsNotSettled(t *testing.T) {
	s := newMemStore()
	s.add("shared", "Shared", "/lib/A/shared/x.m4b", nil)
	s.add("br00", "br00", "/lib/A/br00/x.m4b", nil)
	f := &fencedFixer{partitionFixer{n: 1}}
	plan := planFor(t, s, f)
	var checkpointed []RowResult
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Checkpoint = func(cp ApplyCheckpoint) error {
		checkpointed = append(checkpointed, cp.Settled...)
		return nil
	}
	res, _ := RunApply(context.Background(), f, plan, "op-plan", []string{"r00"}, false, d, nopReporter{})
	require.Len(t, res.Rows, 1)
	require.Equal(t, OutcomeCanceledSkip, res.Rows[0].Outcome, "%+v", res.Rows)
	require.Contains(t, res.Rows[0].Error, "after 1 step", "the row keeps how far it got")
	require.Zero(t, res.Partial)
	require.Empty(t, checkpointed)
}

// When the registry revoked the fence because the stand-down lease was lost,
// a refused write still reports ErrStandDownLost (callers stop on it), as
// well as the fence's error.
func TestWriter_FenceAfterLostLeaseKeepsStandDownLost(t *testing.T) {
	s := newMemStore()
	s.add("b1", "One", "/lib/A/One/one.m4b", nil)
	w := NewWriter(s, s, "t", "bulk_update", "rp-")
	w.setLease(func() bool { return false })
	w.setFence(func() error { return registry.ErrFenced })
	_, err := w.Modify("b1", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, ErrStandDownLost)
	require.ErrorIs(t, err, registry.ErrFenced)

	// A fence revoked by a plain cancel, with the lease still held, is
	// ErrFenced alone.
	w2 := NewWriter(s, s, "t", "bulk_update", "rp-")
	w2.setLease(func() bool { return true })
	w2.setFence(func() error { return registry.ErrFenced })
	_, err = w2.Modify("b1", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, registry.ErrFenced)
	require.NotErrorIs(t, err, ErrStandDownLost)
	require.Equal(t, "One", s.title("b1"))
}
