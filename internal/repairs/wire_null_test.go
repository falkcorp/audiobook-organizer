// file: internal/repairs/wire_null_test.go
// version: 1.0.0
// guid: 9a4e1d62-3b85-4c07-8d19-f26c0e7a5b43
// last-edited: 2026-10-18

package repairs

import (
	"context"
	"encoding/json"
	"testing"

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
	f := &noBookIDsFixer{trimFixer: trimFixer{s: s}, rows: []Row{{RowID: "r1", Title: "t", Reason: "x",
		Risk: RiskLow, Fingerprint: "fp"}}}
	res := planFor(t, s, f)
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(marshalMap(t, res)["rows"], &rows))
	require.JSONEq(t, `[]`, string(rows[0]["book_ids"]), "a row built without BookIDs must not marshal null")

	f.rows = nil
	res = planFor(t, s, f)
	m := marshalMap(t, res)
	require.JSONEq(t, `[]`, string(m["rows"]))
	require.JSONEq(t, `{}`, string(m["skipped_by_kind"]))
}

func TestPlanResult_NormalizeCoversStoredNulls(t *testing.T) {
	var p PlanResult
	require.NoError(t, json.Unmarshal([]byte(`{"fixer_id":"x","skipped_by_kind":null,"rows":[{"row_id":"r","book_ids":null}]}`), &p))
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
