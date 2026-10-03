// file: internal/database/pebble_store_opchange_scan_test.go
// version: 1.0.0
// guid: fabd968d-8245-4447-a90e-81e613cad611
// last-edited: 2026-10-03

package database

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

func newOpChangeScanStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}

// TestScanOperationChanges_VisitsExactlyTheOpChangeRows: every opchange:
// row is visited once, in key order, and nothing under a neighbouring
// prefix ("opchange" bare, "opchange;", "opchanges:", "opchang:") is.
func TestScanOperationChanges_VisitsExactlyTheOpChangeRows(t *testing.T) {
	s := newOpChangeScanStore(t)
	for _, c := range []*OperationChange{
		{ID: "c1", OperationID: "op-a", BookID: "b1", ChangeType: "t", Source: "fixer-x"},
		{ID: "c2", OperationID: "op-a", BookID: "b2", ChangeType: "t"},
		{ID: "c3", OperationID: "op-b", BookID: "b1", ChangeType: "t"},
	} {
		require.NoError(t, s.CreateOperationChange(c))
	}
	// Neighbours a loose bound would sweep in. Their values are not JSON, so
	// visiting one would also fail the scan.
	for _, k := range []string{"opchange", "opchange;x", "opchanges:op:x", "opchang:op:x", "opchange\xff"} {
		require.NoError(t, s.db.Set([]byte(k), []byte("not json"), pebble.Sync))
	}
	var seen []string
	require.NoError(t, s.ScanOperationChanges(func(c *OperationChange) error {
		seen = append(seen, c.OperationID+"/"+c.ID)
		return nil
	}))
	require.Equal(t, []string{"op-a/c1", "op-a/c2", "op-b/c3"}, seen)
	require.True(t, sort.StringsAreSorted(seen), "key order")
}

// TestScanOperationChanges_DecodeErrorPropagates: a row that does not decode
// stops the scan with the decode error.
func TestScanOperationChanges_DecodeErrorPropagates(t *testing.T) {
	s := newOpChangeScanStore(t)
	require.NoError(t, s.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op-a", BookID: "b1"}))
	require.NoError(t, s.db.Set([]byte("opchange:op-b:bad"), []byte("{not json"), pebble.Sync))
	n := 0
	err := s.ScanOperationChanges(func(*OperationChange) error { n++; return nil })
	require.Error(t, err)
	var syn *json.SyntaxError
	require.ErrorAs(t, err, &syn)
	require.Equal(t, 1, n, "the row before the bad one was visited")
}

// TestScanOperationChanges_CallbackErrorStops: fn's first error ends the scan
// and is returned as is.
func TestScanOperationChanges_CallbackErrorStops(t *testing.T) {
	s := newOpChangeScanStore(t)
	for _, id := range []string{"c1", "c2", "c3"} {
		require.NoError(t, s.CreateOperationChange(&OperationChange{ID: id, OperationID: "op-a", BookID: "b1"}))
	}
	stop := errors.New("stop")
	calls := 0
	err := s.ScanOperationChanges(func(*OperationChange) error {
		calls++
		return stop
	})
	require.ErrorIs(t, err, stop)
	require.Equal(t, 1, calls)
}

// TestOperationChange_SourceRoundTrips: Source survives the store, and a row
// stored before the field existed (no "source" key) decodes with it empty.
func TestOperationChange_SourceRoundTrips(t *testing.T) {
	s := newOpChangeScanStore(t)
	require.NoError(t, s.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op-a", BookID: "b1", Source: "fragment-consolidation"}))
	legacy := `{"id":"c0","operation_id":"op-a","book_id":"b1","change_type":"t","field_name":"f","old_value":"","new_value":"","created_at":"2026-01-01T00:00:00Z"}`
	require.NoError(t, s.db.Set([]byte("opchange:op-a:c0"), []byte(legacy), pebble.Sync))
	cs, err := s.GetOperationChanges("op-a")
	require.NoError(t, err)
	require.Len(t, cs, 2)
	got := map[string]string{}
	for _, c := range cs {
		got[c.ID] = c.Source
	}
	require.Equal(t, map[string]string{"c0": "", "c1": "fragment-consolidation"}, got)
	raw, err := json.Marshal(&OperationChange{ID: "x"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"source"`, "omitempty: an unstamped row writes no key")
}
