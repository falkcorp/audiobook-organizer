// file: internal/server/maintenance_dedupe_test.go
// version: 1.0.0
// guid: 9f2b6c41-7e30-4a58-b1d6-2c8e05f37a94
// last-edited: 2026-08-22

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

// TestMaintenanceDedupe_LegacyOpIDIsTheSoleDiscriminator measures WHY a
// double-clicked maintenance job queues two runs instead of collapsing into one.
//
// EnqueueOp already implements same-params merge: it compares the marshalled
// params of an incoming request against every active op for the same def and
// returns the existing id on a byte-for-byte match. internal/operations/registry
// has ten tests pinning that behaviour. The open question was never whether the
// merge works — it is whether the MAINTENANCE path ever reaches it.
//
// This is a dose-response pair. Both arms enqueue the same def twice, with the
// same job id, into a registry that is never started, so both rows sit "queued"
// and the comparison is decided entirely by params with no dispatch timing in
// play. The arms differ in exactly one byte-range:
//
//	Arm A — params identical            -> the two enqueues MUST collapse to one op
//	Arm B — params differ ONLY in       -> the two enqueues MUST produce two ops
//	        LegacyOpID
//
// A alone would only re-prove the registry's own dedupe. B alone would only show
// that something differs. Together they isolate LegacyOpID as the sole reason a
// real double-click misses the merge: hold it constant and the merge fires; vary
// only it and the merge stops. Nothing else about the request changes.
//
// What this measurement settles for the v1 kill (docs/plans/2026-08-17-...):
// dedupe needs the LegacyOpID stamp gone from the ENQUEUE params, which is one
// line (maintenance_dispatcher.go:189). It does not need the field deleted, the
// dispatcher file deleted, or a consolidator built.
func TestMaintenanceDedupe_LegacyOpIDIsTheSoleDiscriminator(t *testing.T) {
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
		// `def.ConcurrencyKey != ""`. With an empty key both arms would queue two
		// ops and Arm B would "pass" for the wrong reason.
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
		p := maintenanceJobOpParams{LegacyOpID: "legacy-same", JobID: jobID}

		id1, id2 := enqueueTwice(t, p, p)

		require.Equal(t, id1, id2,
			"two byte-identical enqueues of the same maintenance def did not merge; "+
				"EnqueueOp's same-params dedupe is not reached on this path")
	})

	t.Run("differing only in LegacyOpID queues a second op", func(t *testing.T) {
		base := maintenanceJobOpParams{JobID: jobID}
		p1, p2 := base, base
		p1.LegacyOpID = "legacy-1"
		p2.LegacyOpID = "legacy-2"

		id1, id2 := enqueueTwice(t, p1, p2)

		require.NotEqual(t, id1, id2,
			"a differing LegacyOpID no longer defeats the params merge; if the stamp "+
				"was removed from the enqueue path, delete this arm and the dose-response "+
				"claim in this test's doc comment along with it")
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
