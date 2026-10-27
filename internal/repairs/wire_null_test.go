// file: internal/repairs/wire_null_test.go
// version: 1.1.4
// guid: 9a4e1d62-3b85-4c07-8d19-f26c0e7a5b43
// last-edited: 2026-10-27

package repairs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/require"
)

// The generated TypeScript types book_ids, rows and the tallies as non-null;
// these tests pin the server side of that promise.

type noBookIDsFixer struct {
	trimFixer
	rows []Row
}

func (f *noBookIDsFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return f.rows, nil
}

func marshalMap(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func TestRunPlan_NilBookIDsAndNoRowsMarshalAsEmptyArrays(t *testing.T) {
	s := newMemStore()
	f := &noBookIDsFixer{s: s, rows: []Row{{RowID: "r1", Title: "t", Reason: "x",
		Risk: RiskLow, Fingerprint: "fp"}}}
	res := planFor(t, s, f)
	// The rows reach the SPA through the rows endpoint (RowsPage).
	page, err := res.Page("op", FilterAll, "", 0, 10)
	require.NoError(t, err)
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(marshalMap(t, page)["rows"], &rows))
	require.JSONEq(t, `[]`, string(rows[0]["book_ids"]), "a row built without BookIDs must not marshal null")

	f.rows = nil
	res = planFor(t, s, f)
	m := marshalMap(t, res)
	require.JSONEq(t, `{}`, string(m["skipped_by_kind"]))
	require.NotContains(t, m, "rows", "the stored summary carries no rows")
	page, err = res.Page("op", FilterAll, "", 0, 10)
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(marshalMap(t, page)["rows"]))
}

func TestPlanResult_NormalizeCoversStoredNulls(t *testing.T) {
	var p PlanResult
	require.NoError(t, json.Unmarshal([]byte(`{"fixer_id":"x","skipped_by_kind":null}`), &p))
	p.Rows = []Row{{RowID: "r"}} // a row with no book_ids
	p.normalize()
	page, err := p.Page("op", FilterAll, "", 0, 10)
	require.NoError(t, err)
	m := marshalMap(t, page)
	require.JSONEq(t, `{}`, string(m["skipped_by_kind"]))
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(m["rows"], &rows))
	require.JSONEq(t, `[]`, string(rows[0]["book_ids"]))
}

func TestRunApply_EarlyReturnsKeepRowsAnArray(t *testing.T) {
	s := newMemStore()
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// No row ids: RunApply returns the result with an error before any row.
	res, err := RunApply(context.Background(), f, plan, "plan-1", nil, true, deps(s, nil), nopReporter{})
	require.Error(t, err)
	m := marshalMap(t, res)
	require.JSONEq(t, `[]`, string(m["rows"]))
	require.JSONEq(t, `{}`, string(m["by_outcome"]))
}

// LoadPlan is the one place a stored plan re-enters the wire path, so a stored
// null must come back as an empty collection: a summary written with null
// collections, and a stored row written with a null book_ids.
func TestLoadPlan_NormalizesStoredNulls(t *testing.T) {
	s := newMemStore()
	data := `{"fixer_id":"fx","row_store":"` + PlanRowStoreV2 + `","total":1,"skipped_by_kind":null}`
	s.ops["plan-1"] = &database.OperationV2Row{ID: "plan-1", DefID: PlanOpID, Status: "completed", ResultData: &data}
	require.NoError(t, s.ReplaceOpRowsPrefix("plan-1", PlanRowPrefix, 1, func(int) (database.OpRow, error) {
		return database.OpRow{ID: PlanRowPrefix + "r", Index: []byte("\x00\x000\x00"), Data: []byte(`{"row_id":"r","book_ids":null}`)}, nil
	}))
	plan, err := LoadPlan(s, s, "plan-1", "fx")
	require.NoError(t, err)
	require.NotNil(t, plan.Rows)
	require.NotNil(t, plan.SkippedByKind)
	page, err := plan.Page("plan-1", FilterAll, "", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	require.NotNil(t, page.Rows[0].BookIDs)
	m := marshalMap(t, plan)
	require.JSONEq(t, `{}`, string(m["skipped_by_kind"]))
	require.NotContains(t, m, "rows", "a plan summary carries no rows")
	pm := marshalMap(t, page)
	require.JSONEq(t, `[]`, string(func() []byte {
		var rows []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(pm["rows"], &rows))
		return rows[0]["book_ids"]
	}()))
}
