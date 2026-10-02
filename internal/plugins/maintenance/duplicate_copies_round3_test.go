// file: internal/plugins/maintenance/duplicate_copies_round3_test.go
// version: 1.0.0
// guid: e4ed5d00-b92e-4387-91d0-1b1b72542bb2
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// errDCInjected stands in for a wrapped sentinel a write step returns (the
// engine's repairs.ErrStandDownLost is the one that matters in production).
var errDCInjected = errors.New("injected write failure")

// dcFailMove is a book_file writer whose move fails with errDCInjected.
type dcFailMove struct{ repairs.BookFileWriter }

func (dcFailMove) MoveBookFilesToBook([]string, string, string) error {
	return errDCInjected
}

// TestDuplicateCopies_PartialKeepsTheCause: a step failing after another
// step succeeded is ErrPartiallyApplied AND still the step's own error, so
// the engine can tell a lapsed stand-down (retry) from a real failure.
func TestDuplicateCopies_PartialKeepsTheCause(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	res := d.planFor(t, dcFixerID, "op-plan", nil)
	row := findRow(t, res, dupRowID(s, l))
	require.True(t, row.Applicable(), row.SkipReason)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").
		WithJournal(dcFailMove{d.s}, d.s, "op-partial")
	err := newDuplicateCopiesFixer(d.p).Apply(context.Background(), w, row)
	require.ErrorIs(t, err, repairs.ErrPartiallyApplied)
	require.ErrorIs(t, err, errDCInjected, "the cause survives the partial wrap: %v", err)
}

// TestDuplicateCopies_IndexIncompleteSaysSo: when the hash lookup refuses
// because memdb's index is incomplete, the row's reason tells the owner to
// wait for warmup or restart, not a generic "unreadable".
func TestDuplicateCopies_IndexIncompleteSaysSo(t *testing.T) {
	d, a, _ := ubik(t)
	r := rowOf(t, d.planFor(t, dcFixerID, "op-plan", nil), a)
	require.True(t, r.Applicable(), r.SkipReason)
	d.p.deps = scanDeps{fakeDeps: fakeDeps{store: dcNoHashLookup{d.s}, labels: d.labels}, scan: &scriptedScan{renewsLeft: -1}, ops: d.ops}
	re, err := newDuplicateCopiesFixer(d.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	require.False(t, re.Applicable())
	require.Equal(t, dcSkipIndexIncomplete, re.Skipped)
	require.Contains(t, re.SkipReason, "file index incomplete, restart or wait for warmup")
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{r.RowID})
	require.Zero(t, out.Applied)
	data, err := json.Marshal(out.Rows)
	require.NoError(t, err)
	require.Contains(t, string(data), "file index incomplete")
}
