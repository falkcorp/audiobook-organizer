// file: internal/maintenance/jobs/recompute_book_aggregates_sentinel_test.go
// version: 1.1.0
// guid: 0a5c7e39-4b18-4d26-91f7-6e2a8b3c5d40
// last-edited: 2026-08-29

package jobs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// This pins the short-circuit whose absence made the job redo the entire
// 40k-book backfill on every production run.
//
// The sentinel lives on the concrete Pebble store, so before the capability
// conversion a bare assertion failed through the indexedStore decorator, the
// job fell into runViaInterface, and IsBookAggregatesBackfillDone was never
// consulted. Resolution is now covered by
// aggregates_marker_capability_test.go; what was still untested is whether the
// job actually HONOURS the sentinel once it can read it.
//
// package jobs, not jobs_test: aggregatesBackfillMarker is unexported, so the
// generated mock and this test have to live inside the package. That is also
// why this file carries its own reporter rather than reusing noopReporter from
// testhelpers_test.go, which is in the external jobs_test package.

// sentinelReporter is a maintenance.ProgressReporter that records log messages.
type sentinelReporter struct{ logs []string }

func (r *sentinelReporter) SetTotal(_ int)                      {}
func (r *sentinelReporter) Increment()                          {}
func (r *sentinelReporter) Log(_ string, msg string, _ *string) { r.logs = append(r.logs, msg) }

// sentinelStore is a JobStore that also carries the backfill-marker capability,
// so resolveAggregatesBackfillMarker finds the mock. ListBookIDs records being
// reached: that call is the first thing past the short-circuit, so it is the
// discriminating signal for whether the job returned early.
type sentinelStore struct {
	maintenance.JobStore
	*mockAggregatesBackfillMarker
	listCalled bool
}

func (s *sentinelStore) ListBookIDs() ([]string, error) {
	s.listCalled = true
	return nil, nil
}

// SaveOperationSummaryLog is reached only on the completing path. Implemented as
// a no-op because the embedded JobStore is nil: leaving it unimplemented made
// the negative test panic instead of fail, which is how it was found.
func (s *sentinelStore) SaveOperationSummaryLog(_ *database.OperationSummaryLog) error {
	return nil
}

// TestRecomputeHonoursBackfillSentinel: sentinel set + not a dry run => return
// immediately, without enumerating books.
func TestRecomputeHonoursBackfillSentinel(t *testing.T) {
	m := newMockAggregatesBackfillMarker(t)
	m.EXPECT().IsBookAggregatesBackfillDone().Return(true).Once()

	store := &sentinelStore{mockAggregatesBackfillMarker: m}
	rep := &sentinelReporter{}
	j := &recomputeBookAggregatesJob{}

	if err := j.Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run returned err %v, want nil", err)
	}
	if store.listCalled {
		t.Error("ListBookIDs was called despite the sentinel being set — the job would redo the whole 40k-book backfill")
	}
}

// TestRecomputeProceedsWhenSentinelUnset is the discriminating negative: without
// it, a Run that always returned early would pass the test above.
func TestRecomputeProceedsWhenSentinelUnset(t *testing.T) {
	m := newMockAggregatesBackfillMarker(t)
	m.EXPECT().IsBookAggregatesBackfillDone().Return(false).Once()
	// Discovered by running this test: on completion the job SETS the sentinel,
	// which is what makes the short-circuit above reachable on the next run. The
	// pair is the whole mechanism, so assert both halves rather than only the
	// read.
	m.EXPECT().MarkBookAggregatesBackfillDone().Return(nil).Once()

	store := &sentinelStore{mockAggregatesBackfillMarker: m}
	rep := &sentinelReporter{}
	j := &recomputeBookAggregatesJob{}

	if err := j.Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run returned err %v, want nil", err)
	}
	if !store.listCalled {
		t.Error("ListBookIDs was NOT called with the sentinel unset — the job skipped work it should have done")
	}
}

// TestRecomputeForceOverridesBackfillSentinel pins the escape hatch the job has
// advertised in two operator-facing strings since it was written.
//
// Force was declared in DefaultParams and read nowhere, so once the sentinel was
// set this job could never run again — and the message telling the operator how
// to override said so over a flag that could not be submitted. That mattered
// beyond the job: notifyBookFileChange swallows recompute errors on the grounds
// that this backfill is the safety net.
//
// The mock carries NO IsBookAggregatesBackfillDone expectation on purpose. force
// is evaluated before the sentinel read, so a forced run must not consult it at
// all; mockery fails an unexpected call, which makes that ordering an assertion
// rather than a comment.
func TestRecomputeForceOverridesBackfillSentinel(t *testing.T) {
	m := newMockAggregatesBackfillMarker(t)
	// A clean forced run rewrites the marker — the backfill HAS just completed
	// again — so this is expected, not incidental.
	m.EXPECT().MarkBookAggregatesBackfillDone().Return(nil).Once()

	store := &sentinelStore{mockAggregatesBackfillMarker: m}
	rep := &sentinelReporter{}
	j := &recomputeBookAggregatesJob{}

	// The params blob the dispatcher persists on the v2 row and the op Run
	// closure hands to the job. Built as raw JSON rather than from a struct so
	// this asserts the wire shape an operator actually POSTs.
	ctx := maintenance.WithRawParams(context.Background(),
		json.RawMessage(`{"job_id":"recompute-book-aggregates","dry_run":false,"force":true}`))

	if err := j.Run(ctx, store, rep, false); err != nil {
		t.Fatalf("Run returned err %v, want nil", err)
	}
	if !store.listCalled {
		t.Error("ListBookIDs was NOT called with force=true — the Force flag is still inert")
	}
}

// TestRecomputeForceFalseStillHonoursSentinel is the discriminating negative for
// the test above: without it, a gate that ignored the sentinel unconditionally
// (or a forceFromCtx that returned true for any params blob) would pass.
func TestRecomputeForceFalseStillHonoursSentinel(t *testing.T) {
	m := newMockAggregatesBackfillMarker(t)
	m.EXPECT().IsBookAggregatesBackfillDone().Return(true).Once()

	store := &sentinelStore{mockAggregatesBackfillMarker: m}
	rep := &sentinelReporter{}
	j := &recomputeBookAggregatesJob{}

	ctx := maintenance.WithRawParams(context.Background(),
		json.RawMessage(`{"job_id":"recompute-book-aggregates","dry_run":false,"force":false}`))

	if err := j.Run(ctx, store, rep, false); err != nil {
		t.Fatalf("Run returned err %v, want nil", err)
	}
	if store.listCalled {
		t.Error("ListBookIDs was called with force=false and the sentinel set — the short-circuit is gone")
	}
}

// TestRecomputeForceIgnoresUnparseableParams pins the fail-safe direction of the
// decode. A params blob the job cannot read must NOT be treated as an override:
// force is an explicit operator action, and inferring it from a decode failure
// would turn a malformed request into a full 40k-book rewrite.
func TestRecomputeForceIgnoresUnparseableParams(t *testing.T) {
	m := newMockAggregatesBackfillMarker(t)
	m.EXPECT().IsBookAggregatesBackfillDone().Return(true).Once()

	store := &sentinelStore{mockAggregatesBackfillMarker: m}
	rep := &sentinelReporter{}
	j := &recomputeBookAggregatesJob{}

	ctx := maintenance.WithRawParams(context.Background(), json.RawMessage(`{"force":`))

	if err := j.Run(ctx, store, rep, false); err != nil {
		t.Fatalf("Run returned err %v, want nil", err)
	}
	if store.listCalled {
		t.Error("ListBookIDs was called on an undecodable params blob — force defaulted to true")
	}
}
