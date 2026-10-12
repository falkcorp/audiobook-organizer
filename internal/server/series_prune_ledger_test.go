// file: internal/server/series_prune_ledger_test.go
// version: 1.0.0
// guid: 2d9e7a51-c06b-4f38-9b14-8a3f6e2c75d9
// last-edited: 2026-10-11

package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The golden library ends every run with one refusal (a merged-away copy a
// trashed book still names); the tests here are about the ledger, so that
// error is expected and checked to be the only one.
func spRunPrune(t *testing.T, st *spLedgerStore, prog *seriesPruneRecordingProgress, opID string) {
	t.Helper()
	err := newSeriesPruneServer(t).executeSeriesPrune(context.Background(), st, prog, opID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "series prune recorded 1 error(s)")
}

// spLedgerStore counts (and optionally refuses) the op's ledger rows; every
// other call, capabilities included, reaches the Pebble store it embeds.
type spLedgerStore struct {
	*database.PebbleStore
	fail  bool
	calls atomic.Int32
}

func (s *spLedgerStore) CreateOperationChange(c *database.OperationChange) error {
	s.calls.Add(1)
	if s.fail {
		return errors.New("ledger down")
	}
	return s.PebbleStore.CreateOperationChange(c)
}

// A run with no operation writes no ledger row at all (the inline loop
// guarded every row on operationID), and still prunes.
func TestExecuteSeriesPrune_NoOperationWritesNoLedgerRows(t *testing.T) {
	st := &spLedgerStore{PebbleStore: newSeriesPruneGoldenLibrary(t)}
	before, err := st.GetAllSeries()
	require.NoError(t, err)

	spRunPrune(t, st, &seriesPruneRecordingProgress{}, "")
	assert.Zero(t, st.calls.Load(), "no operation, no ledger rows")
	after, err := st.GetAllSeries()
	require.NoError(t, err)
	assert.Less(t, len(after), len(before), "precondition: the run removed series, so it had rows to record")
}

// Ledger rows the store refuses are counted in one warn line, and the writes
// they describe stand.
func TestExecuteSeriesPrune_UnrecordedRowsAreReportedInOneWarnLine(t *testing.T) {
	ok := &spLedgerStore{PebbleStore: newSeriesPruneGoldenLibrary(t)}
	spRunPrune(t, ok, &seriesPruneRecordingProgress{}, "op-ok")
	want := ok.calls.Load()
	require.Positive(t, want)

	bad := &spLedgerStore{PebbleStore: newSeriesPruneGoldenLibrary(t), fail: true}
	prog := &seriesPruneRecordingProgress{}
	spRunPrune(t, bad, prog, "op-bad")
	require.Equal(t, want, bad.calls.Load(), "the same rows were attempted")
	assert.Contains(t, prog.lines,
		fmt.Sprintf("warn %d change row(s) of this run could not be recorded; the changes themselves were made", want))
	okSeries, err := ok.GetAllSeries()
	require.NoError(t, err)
	badSeries, err := bad.GetAllSeries()
	require.NoError(t, err)
	assert.Len(t, badSeries, len(okSeries), "an unrecorded row never undoes or stops its write")
}
