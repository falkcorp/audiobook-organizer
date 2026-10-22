// file: internal/database/migration066_test.go
// version: 1.1.0
// guid: 9c16a990-2d0e-4278-b18a-1d20d6bfe3ea
// last-edited: 2026-10-22

package database

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	current := `{"fixer_id":"retired-versions","applicable":3,"row_store":"v1"}`
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

// TestMigration066_IsRegisteredAndStrips: the entry in the migrations list
// (the one RunMigrations applies on boot) is version 66, is the last one, and
// its Up strips a legacy plan's rows; so the cleanup actually runs on deploy,
// not only when the helper is called directly.
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
	require.Equal(t, 66, migrations[len(migrations)-1].Version, "66 is no longer the last migration; keep this test pointing at the plan-row strip")
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
