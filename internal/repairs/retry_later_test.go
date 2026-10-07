// file: internal/repairs/retry_later_test.go
// version: 1.0.0
// guid: 5c1d8e27-9a43-4b6f-8e12-7f0a3c9d4b61
// last-edited: 2026-10-06

package repairs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// holdFixer is a trimFixer whose re-plan of the rows in hold comes back held
// on a transient condition (Row.RetryLater). The held row's Fingerprint
// differs from the planned one (the hold is an input); its RetryFingerprint
// is the planned one unless shiftRetry says another input moved too. Rows in
// noSkip come back with RetryLater set but no Skipped.
type holdFixer struct {
	trimFixer
	mu         sync.Mutex
	hold       map[string]bool
	shiftRetry map[string]bool
	noSkip     map[string]bool
}

func (f *holdFixer) set(m *map[string]bool, id string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if *m == nil {
		*m = map[string]bool{}
	}
	(*m)[id] = on
}

func (f *holdFixer) Replan(ctx context.Context, p json.RawMessage, planned Row, rep registry.Reporter) (Row, error) {
	r, err := f.trimFixer.Replan(ctx, p, planned, rep)
	if err != nil {
		return r, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hold[r.RowID] {
		return r, nil
	}
	retryFP := r.Fingerprint
	if f.shiftRetry[r.RowID] {
		retryFP += " (record refreshed)"
	}
	r.RetryLater, r.RetryFingerprint = true, retryFP
	r.Fingerprint = "held:" + r.Fingerprint
	if !f.noSkip[r.RowID] {
		r.Skipped, r.SkipReason = "index_not_built", "the synthetic index is not built yet"
	}
	return r, nil
}

// (a) A re-plan held on a transient condition, with nothing else changed, is
// retry_later: not written, checkpointed, and not settled -- a run resumed
// from that checkpoint re-plans and applies the row once the hold clears.
func TestRunApply_RetryLaterReplanIsRetriedOnResume(t *testing.T) {
	old := checkpointEvery
	checkpointEvery = 1
	t.Cleanup(func() { checkpointEvery = old })

	s := newMemStore()
	seed(s)
	f := &holdFixer{trimFixer: trimFixer{s: s}}
	plan := planFor(t, s, f)
	f.set(&f.hold, "b1", true)

	var mu sync.Mutex
	var last ApplyCheckpoint
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Checkpoint = func(cp ApplyCheckpoint) error {
		mu.Lock()
		defer mu.Unlock()
		last = cp
		return nil
	}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.RetryLater, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Zero(t, res.ChangedSincePlan)
	require.Equal(t, OutcomeRetryLater, res.Rows[0].Outcome)
	require.Equal(t, "index_not_built", res.Rows[0].Skipped)
	require.Equal(t, "the synthetic index is not built yet", res.Rows[0].Error)
	require.Equal(t, " One ", s.title("b1"), "a held row is not written")
	require.Len(t, last.Settled, 1)
	require.Equal(t, OutcomeRetryLater, last.Settled[0].Outcome)

	// The hold clears; the run resumes from the checkpoint.
	f.set(&f.hold, "b1", false)
	d2 := deps(s, &fakeStandDown{renewsLeft: -1})
	cp := last
	d2.Resumed = &cp
	res, err = RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d2, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied, "a retry_later row is re-run on resume, not reported as settled: %+v", res.Rows)
	require.Zero(t, res.RetryLater)
	require.Equal(t, "One", s.title("b1"))
}

// (b) RetryLater without Skipped is not a hold: the engine ignores the flag
// and the fingerprint check decides (here: changed_since_plan).
func TestRunApply_RetryLaterWithoutSkipIsFingerprintChecked(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &holdFixer{trimFixer: trimFixer{s: s}}
	plan := planFor(t, s, f)
	f.set(&f.hold, "b1", true)
	f.set(&f.noSkip, "b1", true)

	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.ChangedSincePlan, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Zero(t, res.RetryLater)
	require.Equal(t, OutcomeChangedSincePlan, res.Rows[0].Outcome)
	require.Equal(t, " One ", s.title("b1"))
}

// (c) A held row whose other inputs changed too (its RetryFingerprint no
// longer equals the plan's fingerprint) is changed_since_plan: the transient
// hold never masks a real change.
func TestRunApply_RetryLaterWithChangedInputsIsChangedSincePlan(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &holdFixer{trimFixer: trimFixer{s: s}}
	plan := planFor(t, s, f)
	f.set(&f.hold, "b1", true)
	f.set(&f.shiftRetry, "b1", true)

	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.ChangedSincePlan, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Zero(t, res.RetryLater)
	require.Equal(t, OutcomeChangedSincePlan, res.Rows[0].Outcome)
	require.Equal(t, "index_not_built", res.Rows[0].Skipped)
	require.Equal(t, " One ", s.title("b1"))
}
