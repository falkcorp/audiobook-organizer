// file: internal/server/maintenance_dedupe_test.go
// version: 2.0.0
// guid: 9f2b6c41-7e30-4a58-b1d6-2c8e05f37a94
// last-edited: 2026-08-23

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/require"
)

// TestMaintenanceDedupe pins what a double-clicked maintenance job does.
//
// #2717 first used this test to MEASURE the bug: it showed that two enqueues
// differing only in LegacyOpID produced two ops, isolating that field as the
// sole reason the merge never fired. LegacyOpID is per-request bookkeeping, not
// work identity, so it is now excluded from the comparison
// (registry.sameParamsIgnoringLegacyID) and that arm asserts the opposite.
//
// The arms are kept as a set because each one alone proves very little:
//
//	identical params           -> merge   (re-proves the registry's own dedupe)
//	differ ONLY in LegacyOpID  -> merge   (the fix; was two ops before)
//	differ in dry_run          -> TWO ops (the over-merge guard)
//
// The third arm is the load-bearing one. A comparison that stripped too much
// would pass both merge arms and still be badly wrong: for cleanup-series, whose
// first phase deletes every single-book series, absorbing an operator's real
// apply into an already-running preview would silently discard the apply. The
// merge arms cannot detect that; only the dry_run arm can.
//
// All three enqueue into a registry that is never started, so both rows sit
// "queued" and the outcome is decided entirely by params, with no dispatch
// timing in play.
func TestMaintenanceDedupe(t *testing.T) {
	const jobID = "dedupe-probe-job"

	// enqueueTwice returns the two op ids produced by enqueueing p1 then p2
	// against a fresh registry. The registry is deliberately NOT started: a
	// queued row is active for dedupe purposes, so nothing has to run and the
	// result cannot depend on scheduling.
	enqueueTwice := func(t *testing.T, p1, p2 maintenanceJobOpParams) (string, string) {
		t.Helper()
		ctx := context.Background()

		store := newOpsFake(t)
		reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 4, nil)

		src := maintReg(t)
		require.NoError(t, (&Server{}).registerMaintenanceJobOp(src, &fakeDedupeJob{id: jobID}))
		def, ok := src.Def(maintenanceOpID(jobID))
		require.True(t, ok)

		// Precondition, not decoration: EnqueueOp's whole dedupe block is behind
		// `def.ConcurrencyKey != ""`. With an empty key every arm would queue two
		// ops — silently turning both merge arms red and letting the dry_run arm
		// pass for entirely the wrong reason.
		require.NotEmpty(t, def.ConcurrencyKey,
			"precondition: an empty ConcurrencyKey skips the dedupe block entirely")

		def.Run = func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil }
		require.NoError(t, reg.RegisterOp(def))

		id1, err := reg.EnqueueOp(ctx, def.ID, p1)
		require.NoError(t, err)
		id2, err := reg.EnqueueOp(ctx, def.ID, p2)
		require.NoError(t, err)

		// Positive control. If EnqueueOp started returning empty ids, "id1 == id2"
		// in Arm A would hold vacuously — two nothings compare equal.
		require.NotEmpty(t, id1, "first enqueue returned no op id")
		require.NotEmpty(t, id2, "second enqueue returned no op id")

		return id1, id2
	}

	t.Run("identical params collapse to one op", func(t *testing.T) {
		p := maintenanceJobOpParams{JobID: jobID}

		id1, id2 := enqueueTwice(t, p, p)

		require.Equal(t, id1, id2,
			"two byte-identical enqueues of the same maintenance def did not merge; "+
				"EnqueueOp's same-params dedupe is not reached on this path")
	})

	// The arm that used to sit here varied LegacyOpID and asserted the two
	// requests merged anyway. maintenanceJobOpParams no longer has the field, so
	// there is nothing left to vary: two requests for the same job with the same
	// dry_run are now byte-identical, which is Arm A above. What that arm proved
	// about the registry -- that a per-request v1 id is bookkeeping rather than
	// work identity -- is still proved, by TestSameParamsIgnoringLegacyID in
	// internal/operations/registry, alongside the enqueue sites that still stamp
	// one.
	//
	// This is a strengthening, not a coverage loss: maintenance dedupe now rests
	// on the plain byte-equality rule rather than on the key-wise exception.

	t.Run("differing in dry_run queues a second op", func(t *testing.T) {
		base := maintenanceJobOpParams{JobID: jobID}
		preview, apply := base, base
		preview.DryRun = true
		apply.DryRun = false

		id1, id2 := enqueueTwice(t, preview, apply)

		require.NotEqual(t, id1, id2,
			"a real apply was absorbed into an active dry run. These are different "+
				"work: the preview changes nothing and the apply mutates rows, so "+
				"merging them silently discards what the operator asked for")
	})
}

// fakeDedupeJob is a minimal maintenance job used to register a real def via the
// production registration path. A purpose-built job keeps the measurement from
// depending on whichever job maintenance.All() happens to return first, and on
// that job's advertised dry-run default.
type fakeDedupeJob struct{ id string }

func (j *fakeDedupeJob) ID() string          { return j.id }
func (j *fakeDedupeJob) Name() string        { return "Dedupe Probe Job" }
func (j *fakeDedupeJob) Description() string { return "test double for the enqueue-dedupe measurement" }

func (j *fakeDedupeJob) Policy() maintenance.ExecutionPolicy { return maintenance.DefaultPolicy() }

func (j *fakeDedupeJob) Category() string   { return "test" }
func (j *fakeDedupeJob) DefaultParams() any { return struct{}{} }
func (j *fakeDedupeJob) CanResume() bool    { return false }

func (j *fakeDedupeJob) Run(context.Context, maintenance.JobStore, maintenance.ProgressReporter, bool) error {
	return nil
}
