// file: internal/operations/registry/legacy_op_status_internal_test.go
// version: 1.1.0
// guid: 8e35b0d7-4a91-4c26-bf08-73d9a15c6e42
// last-edited: 2026-08-16

package registry

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// legacyUpdate records one UpdateOperationStatus call.
type legacyUpdate struct {
	id       string
	status   string
	progress int
	total    int
	message  string
}

// legacyFakeStore implements the OpsV2Store surface by embedding the interface
// — every method this test does not exercise is nil and would panic loudly if
// something started calling it, which is the intent — plus the two v1 methods
// that make it a legacyOpStore.
type legacyFakeStore struct {
	database.OpsV2Store
	v2      map[string]database.OperationV2Row
	legacy  map[string]*database.Operation
	updates []legacyUpdate
}

func newLegacyFakeStore() *legacyFakeStore {
	return &legacyFakeStore{
		v2:     map[string]database.OperationV2Row{},
		legacy: map[string]*database.Operation{},
	}
}

func (s *legacyFakeStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	row, ok := s.v2[id]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (s *legacyFakeStore) GetOperationByID(id string) (*database.Operation, error) {
	return s.legacy[id], nil
}

func (s *legacyFakeStore) UpdateOperationStatus(id, status string, progress, total int, message string) error {
	s.updates = append(s.updates, legacyUpdate{id, status, progress, total, message})
	if op, ok := s.legacy[id]; ok {
		op.Status, op.Progress, op.Total, op.Message = status, progress, total, message
	}
	return nil
}

// v2OnlyStore has NO v1 methods. It stands in for the several registry test
// fakes (and any future store) that implement OpsV2Store and nothing else —
// the reason the v1 surface is discovered by type assertion rather than added
// to OpsV2Store.
type v2OnlyStore struct{ database.OpsV2Store }

func newTestRegistryWithStore(store database.OpsV2Store) *Registry {
	return &Registry{store: store, logger: slog.Default()}
}

// The defect: jobs dispatched via maintenance.job / the scheduler create a v1
// operations row and enqueue a v2 op carrying its id, and nothing ever wrote
// the v1 row again. The ops UI reads v1, so on 2026-08-14 every maintenance-job
// row of the day sat at "pending" — including jobs that had completed with
// journalled summaries.
func TestPropagateLegacyOpStatus(t *testing.T) {
	t.Run("terminal status reaches the legacy row", func(t *testing.T) {
		s := newLegacyFakeStore()
		s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"legacy_op_id":"leg-1"}`}
		s.legacy["leg-1"] = &database.Operation{ID: "leg-1", Status: "pending", Progress: 7, Total: 9, Message: "working"}

		newTestRegistryWithStore(s).propagateLegacyOpStatus("v2-1", "completed")

		require.Len(t, s.updates, 1)
		assert.Equal(t, "leg-1", s.updates[0].id)
		assert.Equal(t, "completed", s.updates[0].status)
		assert.Equal(t, 7, s.updates[0].progress, "existing counters must be preserved")
		assert.Equal(t, 9, s.updates[0].total)
	})

	t.Run("a completed row with no counters is not left at zero percent", func(t *testing.T) {
		s := newLegacyFakeStore()
		s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"legacy_op_id":"leg-1"}`}
		s.legacy["leg-1"] = &database.Operation{ID: "leg-1", Status: "pending"}

		newTestRegistryWithStore(s).propagateLegacyOpStatus("v2-1", "completed")

		require.Len(t, s.updates, 1)
		assert.Equal(t, 1, s.updates[0].progress,
			"trading 'stuck at pending' for 'finished at 0%%' would be no more honest")
		assert.Equal(t, 1, s.updates[0].total)
	})

	t.Run("failed and canceled pass through; interrupted variants collapse", func(t *testing.T) {
		for v2Status, want := range map[string]string{
			"failed":              "failed",
			"canceled":            "canceled",
			"interrupted_ask":     "interrupted",
			"interrupted_dropped": "interrupted",
		} {
			s := newLegacyFakeStore()
			s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"legacy_op_id":"leg-1"}`}
			s.legacy["leg-1"] = &database.Operation{ID: "leg-1", Status: "running"}

			newTestRegistryWithStore(s).propagateLegacyOpStatus("v2-1", v2Status)

			require.Len(t, s.updates, 1, "v2 status %q", v2Status)
			assert.Equal(t, want, s.updates[0].status, "v2 status %q", v2Status)
		}
	})

	t.Run("non-terminal statuses are not mirrored", func(t *testing.T) {
		for _, v2Status := range []string{"queued", "running", ""} {
			s := newLegacyFakeStore()
			s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"legacy_op_id":"leg-1"}`}
			s.legacy["leg-1"] = &database.Operation{ID: "leg-1", Status: "pending"}

			newTestRegistryWithStore(s).propagateLegacyOpStatus("v2-1", v2Status)

			assert.Empty(t, s.updates, "v2 status %q must not write a terminal row", v2Status)
		}
	})

	t.Run("an op with no legacy twin writes nothing", func(t *testing.T) {
		s := newLegacyFakeStore()
		s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"some_other":"field"}`}

		newTestRegistryWithStore(s).propagateLegacyOpStatus("v2-1", "completed")

		assert.Empty(t, s.updates, "the vast majority of v2 ops have no legacy twin")
	})

	t.Run("unparseable or absent params write nothing", func(t *testing.T) {
		s := newLegacyFakeStore()
		s.v2["a"] = database.OperationV2Row{ID: "a", Params: `{not json`}
		s.v2["b"] = database.OperationV2Row{ID: "b", Params: ``}
		r := newTestRegistryWithStore(s)

		r.propagateLegacyOpStatus("a", "completed")
		r.propagateLegacyOpStatus("b", "completed")
		r.propagateLegacyOpStatus("missing-entirely", "completed")

		assert.Empty(t, s.updates)
	})

	t.Run("a row already at the target status is not rewritten", func(t *testing.T) {
		s := newLegacyFakeStore()
		s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"legacy_op_id":"leg-1"}`}
		s.legacy["leg-1"] = &database.Operation{ID: "leg-1", Status: "completed", Progress: 3, Total: 3}

		newTestRegistryWithStore(s).propagateLegacyOpStatus("v2-1", "completed")

		assert.Empty(t, s.updates)
	})

	t.Run("a store without the v1 surface is a no-op, not a panic", func(t *testing.T) {
		r := newTestRegistryWithStore(&v2OnlyStore{})
		assert.NotPanics(t, func() { r.propagateLegacyOpStatus("v2-1", "completed") })
	})
}

// TestPublishOpTerminal_PropagatesToLegacyRow is the wiring test. The cases
// above prove propagateLegacyOpStatus is correct, not that anything CALLS it —
// and every terminal path in the registry reaches the legacy row only by way of
// publishOpTerminal. Deleting that one call would leave the whole block above
// green while the defect returned intact.
func TestPublishOpTerminal_PropagatesToLegacyRow(t *testing.T) {
	s := newLegacyFakeStore()
	s.v2["v2-1"] = database.OperationV2Row{ID: "v2-1", Params: `{"legacy_op_id":"leg-1"}`}
	s.legacy["leg-1"] = &database.Operation{ID: "leg-1", Status: "pending", Progress: 4, Total: 4}

	// bus is nil: publishOpTerminal returns early on that, so this also pins
	// that the legacy write happens BEFORE the nil-bus bail-out.
	newTestRegistryWithStore(s).publishOpTerminal("v2-1", "maintenance.job", "completed")

	require.Len(t, s.updates, 1, "publishOpTerminal must mirror the terminal status onto the legacy row")
	assert.Equal(t, "completed", s.legacy["leg-1"].Status)
}

// TestLegacyStatusFor_MapsEveryInterruptedVariantTheRegistryCanMint is the
// regression test for a silent early return.
//
// legacyStatusFor used to enumerate the interrupted variants it knew:
// interrupted_ask, interrupted_dropped, interrupted. The side that MINTS them
// had moved on. interruptedStatus returns "interrupted_quiesced" for every
// ResumePolicy except ResumeDrop — three of the four legal policies — and
// worker.go publishes "interrupted_restart". Neither matched, so both fell to
// the default, returned "", and propagateLegacyOpStatus returned before writing
// anything. The legacy row stayed "pending" forever with nothing logged, which
// is byte-for-byte indistinguishable from an op that has no legacy twin at all.
//
// The cases are DERIVED, not listed: every ResumePolicy the type declares is fed
// through the real interruptedStatus, so adding a sixth policy puts its status
// in this test automatically. Listing the variants by hand is the exact mistake
// being fixed, and a hand-written list here would have been just as stale as the
// one in legacyStatusFor.
func TestLegacyStatusFor_MapsEveryInterruptedVariantTheRegistryCanMint(t *testing.T) {
	// Every policy from the first legal value through the last, read off the
	// const block in types.go. ResumeUnspecified is excluded deliberately:
	// RegisterOp refuses it, so no op can ever reach a terminal state under it.
	for p := ResumeRestart; p <= ResumeAsk; p++ {
		minted := interruptedStatus(p)
		t.Run(minted, func(t *testing.T) {
			assert.Equal(t, "interrupted", legacyStatusFor(minted),
				"ResumePolicy %d mints %q, which legacyStatusFor must map — "+
					"returning \"\" strands the legacy row at pending forever, silently", p, minted)
		})
	}

	// The variants published as literals rather than via interruptedStatus.
	// worker.go treats interrupted_restart as a resumable state; server_lifecycle
	// writes bare "interrupted" when it sweeps rows on restart.
	for _, s := range []string{"interrupted", "interrupted_restart", "interrupted_ask", "interrupted_dropped"} {
		t.Run(s, func(t *testing.T) {
			assert.Equal(t, "interrupted", legacyStatusFor(s))
		})
	}

	// The non-interrupted terminals pass through unchanged...
	for _, s := range []string{"completed", "failed", "canceled"} {
		assert.Equal(t, s, legacyStatusFor(s), "terminal status %q must map to itself", s)
	}

	// ...and non-terminal statuses must still map to "", or an op would have its
	// legacy row closed while it was still running. This is the half that keeps
	// the prefix match honest: it is a prefix, not a catch-all.
	for _, s := range []string{"", "queued", "running", "pending", "paused", "interrupting"} {
		assert.Empty(t, legacyStatusFor(s),
			"%q is not terminal; mirroring it would close a live op's legacy row", s)
	}
}
