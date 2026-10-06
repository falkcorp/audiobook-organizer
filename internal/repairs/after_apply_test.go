// file: internal/repairs/after_apply_test.go
// version: 1.0.0
// guid: c0f93abf-8fab-422f-955f-91be26e92bba
// last-edited: 2026-10-06

package repairs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// afterFixer is partialFixer (writes, then reports partially_applied) with
// an AfterApplier that records what it was handed. cancel, when set, is
// called inside Apply: the apply's context is cancelled mid-run.
type afterFixer struct {
	partialFixer
	cancel  context.CancelFunc
	gotIDs  []string
	gotCtx  error
	calls   int
	partial map[string]bool
}

func (f *afterFixer) Apply(ctx context.Context, w *Writer, fresh Row) error {
	if f.partial[fresh.RowID] {
		return f.partialFixer.Apply(ctx, w, fresh)
	}
	err := f.trimFixer.Apply(ctx, w, fresh)
	if f.cancel != nil {
		f.cancel()
	}
	return err
}

func (f *afterFixer) AfterApply(ctx context.Context, bookIDs []string) (string, error) {
	f.calls++
	f.gotIDs = append([]string(nil), bookIDs...)
	f.gotCtx = ctx.Err()
	return "op-follow-up", nil
}

// The follow-up gets the books of applied AND partially applied rows, and
// runs with a live context even when the apply's was cancelled after the
// rows wrote.
func TestRunApply_AfterApplyGetsPartialRowsAndSurvivesCancel(t *testing.T) {
	s := newMemStore()
	seed(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &afterFixer{partialFixer: partialFixer{trimFixer{s: s}}, partial: map[string]bool{"b1": true}}
	plan := planFor(t, s, f)
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Concurrency = 1
	// b2 is applied last and cancels the context as it finishes.
	f.cancel = func() {
		if _, ok := f.applied.Load("b2"); ok {
			cancel()
		}
	}
	res, err := RunApply(ctx, f, plan, "op-plan", []string{"b1", "b2"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Partial)
	require.Equal(t, 1, res.Applied)
	require.Equal(t, 1, f.calls)
	require.Equal(t, []string{"b1", "b2"}, f.gotIDs)
	require.NoError(t, f.gotCtx, "the follow-up must not inherit the cancelled apply context")
	require.Equal(t, "op-follow-up", res.FollowUp)

	// A dry run never calls it.
	f.calls = 0
	_, err = RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, true, d, nopReporter{})
	require.NoError(t, err)
	require.Zero(t, f.calls)
}
