// file: internal/operations/registry/legacy_backfill_internal_test.go
// version: 1.0.0
// guid: 5b0e83d1-72af-46c9-a1e4-8f6c20d95347
// last-edited: 2026-08-22

package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// backfillFakeStore extends legacyFakeStore with the two listing methods the
// backfill needs. Ordered slices rather than maps so tests can pin iteration
// behaviour that depends on order.
type backfillFakeStore struct {
	*legacyFakeStore
	v1List  []database.Operation
	v2List  []database.OperationV2Row
	listErr error
}

func newBackfillFakeStore() *backfillFakeStore {
	return &backfillFakeStore{legacyFakeStore: newLegacyFakeStore()}
}

func (s *backfillFakeStore) ListOperations(limit, offset int) ([]database.Operation, int, error) {
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	return s.v1List, len(s.v1List), nil
}

func (s *backfillFakeStore) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	return s.v2List, nil
}

func at(day int) time.Time {
	return time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC)
}

// TestNonTerminalLegacyStatusIncludesPending is the guard on the whole point of
// this backfill. The stranded rows sit at "pending", and the two pre-existing
// predicates disagree about it: isStaleOperationStatus excludes pending (so
// GET /operations/stale reported count:0 while stranded rows existed), while
// ClearStaleOperations includes it. Excluding it here would make the backfill a
// silent no-op against the exact population it targets.
func TestNonTerminalLegacyStatusIncludesPending(t *testing.T) {
	for _, s := range []string{"pending", "running", "queued", "in_progress"} {
		assert.True(t, nonTerminalLegacyStatus(s), "%q should be non-terminal", s)
	}
	for _, s := range []string{"completed", "failed", "canceled", "interrupted", ""} {
		assert.False(t, nonTerminalLegacyStatus(s), "%q should be terminal", s)
	}
}

func TestBuildV2LegacyIndex(t *testing.T) {
	rows := []database.OperationV2Row{
		{ID: "v2-a", Params: `{"legacy_op_id":"leg-1","job_id":"x"}`, QueuedAt: at(14)},
		{ID: "v2-b", Params: `{"job_id":"y"}`, QueuedAt: at(15)},           // no legacy id
		{ID: "v2-c", Params: ``, QueuedAt: at(15)},                         // no params
		{ID: "v2-d", Params: `not json`, QueuedAt: at(15)},                 // unparseable
		{ID: "v2-e", Params: `{"legacy_op_id":"leg-1"}`, QueuedAt: at(16)}, // same id, newer
	}
	idx := buildV2LegacyIndex(rows)

	require.Len(t, idx, 1)
	// Newest wins: if a legacy row was ever re-enqueued, the later run's outcome
	// is the current one.
	assert.Equal(t, "v2-e", idx["leg-1"].ID)
}

// TestBackfillDryRunWritesNothing is the safety property. The op defaults to a
// dry run precisely so an operator who triggers it with no body gets a plan.
func TestBackfillDryRunWritesNothing(t *testing.T) {
	s := newBackfillFakeStore()
	s.v1List = []database.Operation{
		{ID: "leg-1", Type: "maintenance:fix-file-modes", Status: "pending", CreatedAt: at(14)},
	}
	s.v2List = []database.OperationV2Row{
		{ID: "v2-1", Status: "completed", Params: `{"legacy_op_id":"leg-1"}`, QueuedAt: at(14)},
	}

	rep, err := newTestRegistryWithStore(s).BackfillLegacyOpStatus(context.Background(), true)
	require.NoError(t, err)

	assert.True(t, rep.DryRun)
	assert.Equal(t, 1, rep.NonTerminal)
	assert.Equal(t, 1, rep.ResolvedFromV2)
	assert.Equal(t, 0, rep.Applied)
	assert.Empty(t, s.updates, "a dry run must not write")
	// Positive control: the plan is non-empty, so "wrote nothing" cannot pass by
	// virtue of having decided nothing.
	require.Len(t, rep.Decisions, 1)
	assert.Equal(t, "completed", rep.Decisions[0].NewStatus)
}

func TestBackfillResolvesFromV2Twin(t *testing.T) {
	s := newBackfillFakeStore()
	s.v1List = []database.Operation{
		{ID: "leg-done", Status: "pending", Type: "maintenance:a", CreatedAt: at(14)},
		{ID: "leg-failed", Status: "pending", Type: "maintenance:b", CreatedAt: at(14)},
		{ID: "leg-quiesced", Status: "running", Type: "maintenance:c", CreatedAt: at(14)},
		{ID: "leg-orphan", Status: "pending", Type: "maintenance:d", CreatedAt: at(13)},
		{ID: "leg-fine", Status: "completed", Type: "maintenance:e", CreatedAt: at(14)},
	}
	s.v2List = []database.OperationV2Row{
		{ID: "v2-1", Status: "completed", Params: `{"legacy_op_id":"leg-done"}`},
		{ID: "v2-2", Status: "failed", Params: `{"legacy_op_id":"leg-failed"}`},
		// interrupted_quiesced must map through legacyStatusFor's PREFIX match,
		// not an enumerated list — that list had already drifted once.
		{ID: "v2-3", Status: "interrupted_quiesced", Params: `{"legacy_op_id":"leg-quiesced"}`},
	}

	rep, err := newTestRegistryWithStore(s).BackfillLegacyOpStatus(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, 5, rep.TotalV1Rows)
	assert.Equal(t, 4, rep.NonTerminal, "the already-completed row must be skipped")
	assert.Equal(t, 3, rep.ResolvedFromV2)
	assert.Equal(t, 1, rep.MarkedInterrupted)
	assert.Equal(t, 4, rep.Applied)
	assert.Equal(t, 0, rep.ApplyErrors)

	got := map[string]string{}
	for _, u := range s.updates {
		got[u.id] = u.status
	}
	assert.Equal(t, map[string]string{
		"leg-done":     "completed",
		"leg-failed":   "failed",
		"leg-quiesced": "interrupted",
		"leg-orphan":   "interrupted",
	}, got)
	assert.NotContains(t, got, "leg-fine", "a terminal row must never be rewritten")
}

// A twin that is itself non-terminal is evidence the work did not finish, not
// evidence to copy verbatim — copying would leave the v1 row still lying.
func TestBackfillNonTerminalTwinBecomesInterrupted(t *testing.T) {
	s := newBackfillFakeStore()
	s.v1List = []database.Operation{{ID: "leg-1", Status: "pending", CreatedAt: at(14)}}
	s.v2List = []database.OperationV2Row{
		{ID: "v2-1", Status: "running", Params: `{"legacy_op_id":"leg-1"}`},
	}

	rep, err := newTestRegistryWithStore(s).BackfillLegacyOpStatus(context.Background(), true)
	require.NoError(t, err)

	require.Len(t, rep.Decisions, 1)
	assert.Equal(t, "interrupted", rep.Decisions[0].NewStatus)
	assert.Equal(t, "v2 twin exists but is non-terminal", rep.Decisions[0].Evidence)
	assert.Equal(t, 1, rep.MarkedInterrupted)
}

// Idempotence is what makes ResumeDrop safe on this op: a half-applied pass can
// be abandoned and re-run rather than resumed.
func TestBackfillIsIdempotent(t *testing.T) {
	s := newBackfillFakeStore()
	s.v1List = []database.Operation{{ID: "leg-1", Status: "pending", CreatedAt: at(14)}}
	reg := newTestRegistryWithStore(s)

	_, err := reg.BackfillLegacyOpStatus(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, s.updates, 1)

	// Simulate the write having landed, then run again.
	s.v1List[0].Status = "interrupted"
	rep, err := reg.BackfillLegacyOpStatus(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, 0, rep.NonTerminal)
	assert.Len(t, s.updates, 1, "second pass must write nothing")
}

// The store surface is optional and discovered by type assertion, so a store
// that cannot supply it must fail loudly rather than silently report zero rows.
func TestBackfillRejectsStoreWithoutLegacyAccess(t *testing.T) {
	_, err := newTestRegistryWithStore(v2OnlyStore{}).
		BackfillLegacyOpStatus(context.Background(), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support legacy operation access")
}
