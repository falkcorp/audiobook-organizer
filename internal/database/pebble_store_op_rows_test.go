// file: internal/database/pebble_store_op_rows_test.go
// version: 1.1.2
// guid: 76703037-5000-42de-b452-f8c43b7fe9e3
// last-edited: 2026-10-27

package database

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

func opRowsOf(n int, tag string) func(i int) (OpRow, error) {
	return func(i int) (OpRow, error) {
		id := fmt.Sprintf("row-%06d", i)
		return OpRow{ID: id, Index: []byte("ix-" + tag + id), Data: []byte("data-" + tag + id)}, nil
	}
}

func scanIDs(t *testing.T, p *PebbleStore, op string) (ids []string, idx map[string]string) {
	t.Helper()
	idx = map[string]string{}
	if err := p.ScanOpRowIndex(op, func(id string, index []byte) error {
		ids = append(ids, id)
		idx[id] = string(index)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ids, idx
}

// TestOpRows_RoundTrip: the index scan returns the rows in ID order with their
// index (never the body), a point read is aligned with its
// ids and nil for a missing one, and a set bigger than one commit batch
// survives intact.
func TestOpRows_RoundTrip(t *testing.T) {
	p := newTimelineStore(t)
	n := opRowBatchRows*2 + 17
	if err := p.ReplaceOpRowsPrefix("op-a", "row-", n, opRowsOf(n, "")); err != nil {
		t.Fatal(err)
	}
	ids, idx := scanIDs(t, p, "op-a")
	if len(ids) != n {
		t.Fatalf("index scan = %d rows, want %d", len(ids), n)
	}
	for i, id := range ids {
		if id != fmt.Sprintf("row-%06d", i) {
			t.Fatalf("index scan out of order at %d: %s", i, id)
		}
	}
	if idx["row-000003"] != "ix-row-000003" {
		t.Fatalf("index entry = %q", idx["row-000003"])
	}
	got, err := p.GetOpRows("op-a", []string{"row-000001", "missing", "row-000000"})
	if err != nil || len(got) != 3 || string(got[0]) != "data-row-000001" || got[1] != nil || string(got[2]) != "data-row-000000" {
		t.Fatalf("GetOpRows = %q, %v", got, err)
	}
	// The caller owns the slice it was handed.
	got[0][0] = 'X'
	again, _ := p.GetOpRows("op-a", []string{"row-000001"})
	if !bytes.HasPrefix(again[0], []byte("data-")) {
		t.Fatalf("a returned slice aliased the store: %q", again[0])
	}
}

// TestOpRows_ReplaceIsExactAndScoped: a second ReplaceOpRowsPrefix leaves exactly the
// new set (no row of the first survives), n == 0 clears, and another operation's
// rows, including one whose id extends this one's, are never touched.
func TestOpRows_ReplaceIsExactAndScoped(t *testing.T) {
	p := newTimelineStore(t)
	for _, op := range []string{"op-a", "op-a2", "op-b"} {
		if err := p.ReplaceOpRowsPrefix(op, "row-", 5, opRowsOf(5, op)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.ReplaceOpRowsPrefix("op-a", "row-", 2, opRowsOf(2, "new")); err != nil {
		t.Fatal(err)
	}
	ids, idx := scanIDs(t, p, "op-a")
	if len(ids) != 2 || idx["row-000000"] != "ix-newrow-000000" {
		t.Fatalf("op-a after replace = %v %v", ids, idx)
	}
	if got, _ := p.GetOpRows("op-a", []string{"row-000004"}); got[0] != nil {
		t.Fatalf("a row of the replaced set survived: %q", got[0])
	}
	if err := p.ReplaceOpRowsPrefix("op-b", "row-", 0, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := scanIDs(t, p, "op-b"); len(ids) != 0 {
		t.Fatalf("op-b not cleared: %v", ids)
	}
	if ids, _ := scanIDs(t, p, "op-a2"); len(ids) != 5 {
		t.Fatalf("op-a2 lost rows when op-a was replaced: %v", ids)
	}
	if err := p.ReplaceOpRowsPrefix("", "row-", 0, nil); err == nil {
		t.Fatal("empty operation id accepted")
	}
	if err := p.ReplaceOpRowsPrefix("op-c", "row-", 3, func(int) (OpRow, error) { return OpRow{}, nil }); err == nil {
		t.Fatal("a row with no id was accepted")
	}
	boom := errors.New("boom")
	if err := p.ReplaceOpRowsPrefix("op-c", "row-", 3, func(int) (OpRow, error) { return OpRow{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("source error = %v", err)
	}
}

func rowKeysOf(t *testing.T, p *PebbleStore, id string) []string {
	t.Helper()
	var out []string
	for _, k := range timelineKeys(t, p, "opv2:row") {
		if strings.Contains(k, ":"+id+":") {
			out = append(out, k)
		}
	}
	return out
}

// TestOpRows_SurviveCompletionAndDieWithTheOperation: a plan op's rows are read
// long after the op completes, so completing it must not touch them; and every
// way of deleting the operation removes both families, with the v3 mirror on
// and with it off (no v3 record: the path whose v3 delete is a no-op).
func TestOpRows_SurviveCompletionAndDieWithTheOperation(t *testing.T) {
	for _, via := range []string{"DeleteOperationV2", "DeleteRunV3", "SweepHollowOperationsV2"} {
		for _, mirror := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/mirror=%v", via, mirror), func(t *testing.T) {
				if via == "DeleteRunV3" && !mirror {
					t.Skip("DeleteRunV3 deletes a v3 record, which a mirror-off run never had")
				}
				p := newTimelineStore(t)
				p.setOpsV2MirrorEnabled(mirror)
				id, other := newRunID(), newRunID()
				now := time.Now().UTC()
				for _, op := range []string{id, other} {
					if err := p.InsertOperationV2(OperationV2Row{ID: op, DefID: "repairs.plan", Status: "running", Priority: 3, QueuedAt: now}); err != nil {
						t.Fatal(err)
					}
					if err := p.ReplaceOpRowsPrefix(op, "row-", 4, opRowsOf(4, "")); err != nil {
						t.Fatal(err)
					}
				}
				for _, op := range []string{id, other} {
					if err := p.UpdateOperationV2Status(op, "completed", &now, &now, nil); err != nil {
						t.Fatal(err)
					}
				}
				if got := rowKeysOf(t, p, id); len(got) != 8 {
					t.Fatalf("completing the op changed its row keys: %v", got)
				}
				switch via {
				case "DeleteRunV3":
					if deleted, err := p.DeleteRunV3(id, []state.V3{state.V3Succeeded}); err != nil || !deleted {
						t.Fatalf("DeleteRunV3: %v %v", deleted, err)
					}
				case "DeleteOperationV2":
					if _, deleted, err := p.DeleteOperationV2(id, []string{"completed"}); err != nil || !deleted {
						t.Fatalf("DeleteOperationV2: %v %v", deleted, err)
					}
				case "SweepHollowOperationsV2":
					// A row that no longer decodes is what the sweep removes.
					if err := p.db.Set(opv2OpKey(id), []byte("not json"), nil); err != nil {
						t.Fatal(err)
					}
					if n, err := p.SweepHollowOperationsV2(); err != nil || n != 1 {
						t.Fatalf("SweepHollowOperationsV2 = %d, %v", n, err)
					}
				}
				if got := rowKeysOf(t, p, id); len(got) != 0 {
					t.Fatalf("row keys survive the delete: %v", got)
				}
				if got := rowKeysOf(t, p, other); len(got) != 8 {
					t.Fatalf("the other operation's rows were touched: %v", got)
				}
			})
		}
	}
}

// recordingDeleter records the ranges stageOpRowsDelete stages.
type recordingDeleter struct{ ranges [][2]string }

func (r *recordingDeleter) DeleteRange(start, end []byte, _ *pebble.WriteOptions) error {
	r.ranges = append(r.ranges, [2]string{string(start), string(end)})
	return nil
}

// TestStageOpRowsDelete: it stages exactly the ranges opv2RowFamilyPrefixes
// names (so a family added there is swept and replaced with it), and refuses an
// id a row set cannot be keyed by without staging anything: an empty one, and
// one with the key separator, whose prefix delete would reach another
// operation's rows.
func TestStageOpRowsDelete(t *testing.T) {
	var ok recordingDeleter
	if err := stageOpRowsDelete(&ok, "01HZ"); err != nil {
		t.Fatal(err)
	}
	want := opv2RowFamilyPrefixes("01HZ")
	if len(ok.ranges) != len(want) || len(want) != 2 {
		t.Fatalf("staged %d ranges, want the %d of opv2RowFamilyPrefixes (2)", len(ok.ranges), len(want))
	}
	for i, pfx := range want {
		if ok.ranges[i] != [2]string{string(pfx), string(prefixEnd(pfx))} {
			t.Errorf("range %d = %q, want %q..%q", i, ok.ranges[i], pfx, prefixEnd(pfx))
		}
	}
	if string(want[0]) != "opv2:row:01HZ:" || string(want[1]) != "opv2:rowix:01HZ:" {
		t.Errorf("row family prefixes = %q", want)
	}

	for _, id := range []string{"", "a:b", ":", "a:"} {
		var bad recordingDeleter
		if err := stageOpRowsDelete(&bad, id); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if len(bad.ranges) != 0 {
			t.Errorf("id %q staged %v before refusing", id, bad.ranges)
		}
	}
}

// TestReplaceOpRowsPrefixRefusesSeparatorIDs: the same ids are refused where a row
// set is written, so none can exist for the sweep to miss.
func TestReplaceOpRowsPrefixRefusesSeparatorIDs(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)
	for _, id := range []string{"a:b", ":"} {
		if err := p.ReplaceOpRowsPrefix(id, "row-", 0, nil); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

// TestSweepSkipsRowRangeOfSeparatorID: a hollow record whose key holds the
// separator is still swept, only its row-range delete is skipped, and the
// sweep does not fail (which would stop the boot that runs it). The point of
// the refusal is what the delete would reach: the prefix of the id "a:b" is
// opv2:row:a:b:, and the row "b:x" of the op "a" is stored at
// opv2:row:a:b:x, so an unguarded sweep of "a:b" deletes a row of "a".
func TestSweepSkipsRowRangeOfSeparatorID(t *testing.T) {
	st, cleanup := setupTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)
	if err := p.ReplaceOpRowsPrefix("a", "b:", 1, func(int) (OpRow, error) {
		return OpRow{ID: "b:x", Index: []byte("i"), Data: []byte("d")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte("opv2:op:a:b"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	n, err := p.SweepHollowOperationsV2()
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1, nil", n, err)
	}
	got, err := p.GetOpRows("a", []string{"b:x"})
	if err != nil || got[0] == nil {
		t.Fatalf("the sweep of \"a:b\" deleted the row \"b:x\" of op \"a\": %q, %v", got, err)
	}
	ids, _ := scanIDs(t, p, "a")
	if len(ids) != 1 {
		t.Fatalf("index entries of op a = %v, want the one for b:x", ids)
	}
}

// TestReplaceOpRowsPrefixRefusesAnEmptyPrefix: an empty prefix would replace
// every namespace of the operation (the wipe ReplaceOpRows used to do), so it is
// refused, and the rows already stored under the operation are left alone.
func TestReplaceOpRowsPrefixRefusesAnEmptyPrefix(t *testing.T) {
	p := newTimelineStore(t)
	if err := p.ReplaceOpRowsPrefix("op-a", "row-", 3, opRowsOf(3, "")); err != nil {
		t.Fatal(err)
	}
	if err := p.ReplaceOpRowsPrefix("op-a", "", 0, nil); err == nil {
		t.Fatal("an empty row prefix was accepted")
	}
	if ids, _ := scanIDs(t, p, "op-a"); len(ids) != 3 {
		t.Fatalf("a refused empty-prefix clear removed rows: %v", ids)
	}
}
