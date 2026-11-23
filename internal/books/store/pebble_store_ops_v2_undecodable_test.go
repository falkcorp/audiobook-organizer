// file: internal/books/store/pebble_store_ops_v2_undecodable_test.go
// version: 0.1.0
// guid: 9212043b-c39e-4768-8eab-f5715259ba66
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

func insertPlanLikeOp(t *testing.T, p *PebbleStore, id, defID, result string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, p.InsertOperationV2(OperationV2Row{
		ID: id, DefID: defID, Status: "completed", QueuedAt: now, CompletedAt: &now, ResultData: &result,
	}))
}

// TestGetOperationV2ClassifiesUndecodableRecords: the sentinel is how a caller
// tells a damaged record from a failed read. The message and the decode error
// under it are unchanged, a record with no id is still (nil, nil), and a good
// record is unaffected.
func TestGetOperationV2ClassifiesUndecodableRecords(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	require.NoError(t, p.db.Set(opv2OpKey("bad-time"), []byte(`{"ID":"bad-time","QueuedAt":"garbage"}`), pebble.Sync))
	require.NoError(t, p.db.Set(opv2OpKey("bad-json"), []byte(`{not json`), pebble.Sync))
	require.NoError(t, p.db.Set(opv2OpKey("shell"), []byte(`{}`), pebble.Sync))
	insertPlanLikeOp(t, p, "good", "x.y", `{}`)

	row, err := p.GetOperationV2("bad-time")
	require.Nil(t, row)
	require.ErrorIs(t, err, ErrOpRecordUndecodable)
	var perr *time.ParseError
	require.ErrorAs(t, err, &perr, "the decode error under the sentinel is still reachable")
	var want OperationV2Row
	require.EqualError(t, err, json.Unmarshal([]byte(`{"ID":"bad-time","QueuedAt":"garbage"}`), &want).Error(), "message unchanged")

	_, err = p.GetOperationV2("bad-json")
	require.ErrorIs(t, err, ErrOpRecordUndecodable)
	var serr *json.SyntaxError
	require.ErrorAs(t, err, &serr)

	row, err = p.GetOperationV2("shell")
	require.NoError(t, err)
	require.Nil(t, row, "a record with no id stays (nil, nil)")
	row, err = p.GetOperationV2("missing")
	require.NoError(t, err)
	require.Nil(t, row)
	row, err = p.GetOperationV2("good")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.False(t, errors.Is(err, ErrOpRecordUndecodable))
}
