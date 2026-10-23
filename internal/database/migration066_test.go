// file: internal/database/migration066_test.go
// version: 1.4.1
// guid: 9c16a990-2d0e-4278-b18a-1d20d6bfe3ea
// last-edited: 2026-10-22

package database

import (
	"encoding/json"
	"errors"
	"strings"
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

func resultOf(t *testing.T, p *PebbleStore, id string) string {
	t.Helper()
	row, err := p.GetOperationV2(id)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.ResultData)
	return *row.ResultData
}

// TestMigration066StripsInlineRowsFromLegacyPlans: a plan result that still
// holds its rows loses them and keeps the rest of its summary; the opv2 row
// and the opv3 run record agree; a plan already in the current format (no
// rows), a plan with no result, a malformed result and a NON-plan operation
// whose result has a "rows" member are not touched; and a second run writes
// nothing.
func TestMigration066StripsInlineRowsFromLegacyPlans(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	bigRows := make([]map[string]string, 400)
	for i := range bigRows {
		bigRows[i] = map[string]string{"row_id": strings.Repeat("x", 600)}
	}
	legacy, err := json.Marshal(map[string]any{
		"fixer_id": "retired-versions", "applicable": 3, "skipped": 7, "rows": bigRows,
		"by_outcome": map[string]int{"would_retire": 3},
	})
	require.NoError(t, err)
	// planRowStoreV1 is repairs.PlanRowStoreV1. repairs imports this package, so
	// a test here cannot import it; TestStripLegacyPlanRowsLeavesCurrentPlans in
	// repairs runs the strip over a plan SavePlan really wrote.
	const planRowStoreV1 = "op_rows.v1"
	current := `{"fixer_id":"retired-versions","applicable":3,"row_store":"` + planRowStoreV1 + `"}`
	otherOp := `{"rows":[1,2,3]}`
	malformed := `not json`

	insertPlanLikeOp(t, p, "plan-legacy", LegacyPlanOpDefID, string(legacy))
	insertPlanLikeOp(t, p, "plan-current", LegacyPlanOpDefID, current)
	insertPlanLikeOp(t, p, "plan-broken", LegacyPlanOpDefID, malformed)
	insertPlanLikeOp(t, p, "other-op", "library.scan", otherOp)
	now := time.Now().UTC()
	require.NoError(t, p.InsertOperationV2(OperationV2Row{ID: "plan-noresult", DefID: LegacyPlanOpDefID, Status: "completed", QueuedAt: now, CompletedAt: &now}))

	rep, err := p.StripLegacyPlanRows()
	require.NoError(t, err)
	require.Equal(t, 3, rep.Plans, "legacy, current and broken carry a result; noresult does not")
	require.Equal(t, 1, rep.Stripped)
	require.Equal(t, 1, rep.Unreadable)
	require.EqualValues(t, len(legacy), rep.BytesBefore)
	require.Greater(t, rep.BytesBefore-rep.BytesAfter, int64(200000), "bytes freed")

	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(resultOf(t, p, "plan-legacy")), &got))
	require.NotContains(t, got, "rows")
	require.NotContains(t, got, "row_store", "stripped means legacy: LoadPlan must refuse it")
	require.JSONEq(t, `"retired-versions"`, string(got["fixer_id"]))
	require.JSONEq(t, `3`, string(got["applicable"]))
	require.JSONEq(t, `7`, string(got["skipped"]))
	require.JSONEq(t, `{"would_retire":3}`, string(got["by_outcome"]))

	run, err := p.GetRunV3("plan-legacy")
	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, resultOf(t, p, "plan-legacy"), compactJSON(t, string(run.Result)), "the opv3 run record must carry the stripped result too")

	require.Equal(t, current, resultOf(t, p, "plan-current"))
	require.Equal(t, malformed, resultOf(t, p, "plan-broken"))
	require.Equal(t, otherOp, resultOf(t, p, "other-op"))

	d1, k1 := pebbleDigest(t, p)
	rep2, err := p.StripLegacyPlanRows()
	require.NoError(t, err)
	require.Equal(t, 0, rep2.Stripped, "second run finds nothing to strip")
	d2, k2 := pebbleDigest(t, p)
	require.Equal(t, k1, k2)
	require.Equal(t, d1, d2, "second run must change no byte")

	require.NoError(t, migration066Up(p), "the registered Up runs the same strip")
}

// TestMigration066_IsRegisteredAndStrips: the migrations list (the one
// RunMigrations applies on boot) has a version 66 whose Up strips a legacy
// plan's rows, so the cleanup actually runs on deploy and not only when the
// helper is called directly.
func TestMigration066_IsRegisteredAndStrips(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	var m *Migration
	for i := range migrations {
		if migrations[i].Version == 66 {
			m = &migrations[i]
		}
	}
	require.NotNil(t, m, "migration 66 is not in the migrations list")
	require.NotNil(t, m.Up)

	insertPlanLikeOp(t, p, "plan-legacy-2", LegacyPlanOpDefID, `{"fixer_id":"retired-versions","rows":[{"row_id":"a"},{"row_id":"b"}]}`)
	require.NoError(t, m.Up(p))
	require.JSONEq(t, `{"fixer_id":"retired-versions"}`, resultOf(t, p, "plan-legacy-2"))
}

func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var v json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return string(v)
}

// TestMigration066SkipsUndecodableOperationRecords: migration 66 runs at boot,
// and an error out of it refuses to start the server on every restart. One
// operation record whose bytes are not an operation (a torn or hand-edited
// value) used to be exactly such an error. It is counted, logged and skipped,
// the legacy plan beside it is still stripped, and the migration succeeds.
func TestMigration066SkipsUndecodableOperationRecords(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	legacy := `{"fixer_id":"retired-versions","applicable":3,"rows":[{"row_id":"a"}]}`
	insertPlanLikeOp(t, p, "plan-legacy", LegacyPlanOpDefID, legacy)
	require.NoError(t, p.db.Set(opv2OpKey("junk"), []byte("{not json"), pebble.Sync))
	require.NoError(t, p.db.Set(opv2OpKey("wrong-shape"), []byte(`{"id":["not","a","string"]}`), pebble.Sync))
	// A time.ParseError, not a json syntax or type error: the classification
	// is by where the failure came from, not by which error type it is.
	require.NoError(t, p.db.Set(opv2OpKey("bad-time"), []byte(`{"ID":"bad-time","QueuedAt":"garbage"}`), pebble.Sync))

	rep, err := p.StripLegacyPlanRows()
	require.NoError(t, err, "an undecodable operation record must not fail the strip")
	require.Equal(t, 3, rep.UnreadableOps, "the syntax-error, wrong-type and bad-timestamp records")
	require.Equal(t, 1, rep.Stripped, "the plan next to them is still stripped")
	require.NotContains(t, resultOf(t, p, "plan-legacy"), `"rows"`)

	require.NoError(t, migration066Up(p), "the registered Up must succeed over the same store")
}

// TestMigration066CountsLegacyPlanWithUndecodableTimestamp: a legacy plan whose
// record also has a timestamp that does not parse cannot be rewritten (the
// rewrite decodes the row whole and re-indexes it by those times). It is
// counted and left exactly as it is, the boot goes on, and a legacy plan
// beside it is still stripped.
func TestMigration066CountsLegacyPlanWithUndecodableTimestamp(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	insertPlanLikeOp(t, p, "plan-ok", LegacyPlanOpDefID, `{"fixer_id":"x","rows":[{"row_id":"a"}]}`)
	torn := `{"ID":"plan-torn","DefID":"` + LegacyPlanOpDefID + `","QueuedAt":"garbage","ResultData":"{\"rows\":[1]}"}`
	require.NoError(t, p.db.Set(opv2OpKey("plan-torn"), []byte(torn), pebble.Sync))

	rep, err := p.StripLegacyPlanRows()
	require.NoError(t, err)
	require.Equal(t, 1, rep.UnreadableOps)
	require.Equal(t, 1, rep.Stripped)
	require.NotContains(t, resultOf(t, p, "plan-ok"), `"rows"`)
	raw, closer, err := p.db.Get(opv2OpKey("plan-torn"))
	require.NoError(t, err)
	require.Equal(t, torn, string(raw), "the undecodable record is left byte for byte as it was")
	require.NoError(t, closer.Close())
}

// TestMigration066SkipsRecordStoredUnderAnotherKey: a record whose own id is
// not the key it sits under is counted as unreadable and skipped. The rewrite
// writes by id, so a legacy plan found under "plan-x" whose JSON says "other"
// must neither be stripped nor cause anything to be written under "other".
func TestMigration066SkipsRecordStoredUnderAnotherKey(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	result := `{"fixer_id":"x","rows":[{"row_id":"a"}]}`
	now := time.Now().UTC()
	data, err := json.Marshal(OperationV2Row{ID: "other", DefID: LegacyPlanOpDefID, Status: "completed",
		QueuedAt: now, CompletedAt: &now, ResultData: &result})
	require.NoError(t, err)
	require.NoError(t, p.db.Set(opv2OpKey("plan-x"), data, pebble.Sync))

	rep, err := p.StripLegacyPlanRows()
	require.NoError(t, err)
	require.Equal(t, 1, rep.UnreadableOps)
	require.Zero(t, rep.Stripped)
	require.Zero(t, rep.Plans, "a record under the wrong key is not counted as a plan")

	raw, closer, err := p.db.Get(opv2OpKey("plan-x"))
	require.NoError(t, err)
	require.Equal(t, string(data), string(raw), "the mismatched record is left byte for byte as it was")
	require.NoError(t, closer.Close())
	_, _, err = p.db.Get(opv2OpKey("other"))
	require.ErrorIs(t, err, pebble.ErrNotFound, "nothing may be written under the id the record claims")
}

// TestMigration066CountsHollowRecordsApart: a stored record that decodes but has
// no id is a hollow shell, counted in HollowOps, not in UnreadableOps (which
// is Warn-logged per record); a damaged record beside it still is.
func TestMigration066CountsHollowRecordsApart(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)

	require.NoError(t, p.db.Set(opv2OpKey("shell-1"), []byte(`{}`), pebble.Sync))
	require.NoError(t, p.db.Set(opv2OpKey("shell-2"), []byte(`{"Status":"queued"}`), pebble.Sync))
	require.NoError(t, p.db.Set(opv2OpKey("junk"), []byte("{not json"), pebble.Sync))

	rep, err := p.StripLegacyPlanRows()
	require.NoError(t, err)
	require.Equal(t, 2, rep.HollowOps)
	require.Equal(t, 1, rep.UnreadableOps)
}

// TestMigration066ReadFailureStaysFatal: only a record whose bytes do not
// decode is skipped. A failed read (an I/O error, a closed store) says nothing
// about the record, so the strip stops and returns it, and boot reports the
// failure instead of carrying on with a migration it only half did.
func TestMigration066ReadFailureStaysFatal(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)
	insertPlanLikeOp(t, p, "plan-legacy", LegacyPlanOpDefID, `{"rows":[1]}`)

	// legacyPlanStripGetHook is a package-level variable: this test must not
	// use t.Parallel(), and neither may any other test that sets the hook.
	boom := errors.New("injected read failure")
	legacyPlanStripGetHook = func(string) (*OperationV2Row, []byte, error) { return nil, nil, boom }
	t.Cleanup(func() { legacyPlanStripGetHook = nil })

	rep, err := p.StripLegacyPlanRows()
	require.ErrorIs(t, err, boom)
	require.Zero(t, rep.UnreadableOps, "a failed read is not counted as an undecodable record")
	require.Error(t, migration066Up(p), "the registered Up must fail, not skip, on a read failure")
	legacyPlanStripGetHook = nil
	require.Contains(t, resultOf(t, p, "plan-legacy"), `"rows"`, "nothing was rewritten while the read was failing")
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
