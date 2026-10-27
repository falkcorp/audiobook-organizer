// file: internal/plugins/maintenance/credits_backfill_class_lists_test.go
// version: 1.0.0
// guid: b4d81e07-93c2-4a5f-8e16-5f0c2a7d9e38
// last-edited: 2026-10-27

package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The class id lists of a full-library result are 5-12 MB. They are stored as
// op rows beside the operation, and the operation's own result (read whole by
// GetOperationV2, the /system/status poll, the boot scan and the diagnostics
// bundle) keeps the summary, the counts and the samples. Fixtures: Batman
// characters and storylines (seedCBCensusFixture), nothing from a real library.

// failingResultReporter is a cbReporter whose result write fails.
type failingResultReporter struct{ cbReporter }

func (r *failingResultReporter) SetResult(any) error { return errors.New("result store unavailable") }

func opRowIDs(t *testing.T, rs database.OpRowStore, opID string) []string {
	t.Helper()
	var ids []string
	if err := rs.ScanOpRowIndex(opID, func(id string, _ []byte) error { ids = append(ids, id); return nil }); err != nil {
		t.Fatal(err)
	}
	return ids
}

// The stored result has every class's count and samples and no ids; each
// class's ids come back from the rows, in the scan's sorted order, equal to
// what the scan returned.
func TestCreditsBackfill_StoredResultKeepsCountsAndPagesIDsFromOpRows(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)
	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	scanned := len(res.Classes)
	rep := &cbReporter{id: cbTestOpID}
	if err := storeCreditsBackfillResult(rep, f.s, cbTestOpID, res); err != nil {
		t.Fatal(err)
	}
	stored, ok := rep.result.(*creditsBackfillResult)
	if !ok {
		t.Fatalf("stored result is %T", rep.result)
	}
	if len(stored.Classes) != scanned || !stored.ClassLists.Valid() {
		t.Fatalf("stored %d classes (scan %d), summary %+v", len(stored.Classes), scanned, stored.ClassLists)
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Classes []map[string]json.RawMessage `json:"classes"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, c := range decoded.Classes {
		if _, has := c["book_ids"]; has {
			t.Fatalf("the stored result still carries book_ids: %s", c["key"])
		}
	}
	// The scan's own result is left whole (a caller of the scan, and the tests,
	// read the ids from it).
	if cbClassOf(res, cbNarratorFillFromColumn) == nil || len(cbClassIDs(res, cbNarratorFillFromColumn)) == 0 {
		t.Fatal("storing the result emptied the scan's own class lists")
	}

	withIDs := 0
	for _, c := range res.Classes {
		if stored.ClassLists.Counts[c.Key] != c.Count || c.Count != len(c.BookIDs) {
			t.Errorf("%s: stored count %d, class count %d, ids %d", c.Key, stored.ClassLists.Counts[c.Key], c.Count, len(c.BookIDs))
		}
		got, err := stored.ClassLists.Page(f.s, cbTestOpID, c.Key, 0, max(c.Count, 1))
		if err != nil {
			t.Fatalf("%s: %v", c.Key, err)
		}
		if !slices.Equal(got, append([]string{}, c.BookIDs...)) {
			t.Errorf("%s: paged %v, scanned %v", c.Key, got, c.BookIDs)
		}
		if c.Count > 0 {
			withIDs++
		}
	}
	if withIDs < 5 {
		t.Fatalf("only %d classes had ids: the fixture proves little", withIDs)
	}
}

// At library scale the stored result is a few tens of kilobytes however many
// ids the classes hold, and every id is still there in the rows.
func TestCreditsBackfill_StoredResultSizeDoesNotGrowWithTheIDLists(t *testing.T) {
	f := newCBFixture(t)
	const n = 150000
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("01J0000000000000000%07d", i)
	}
	res := &creditsBackfillResult{
		Counts: map[string]int{cbNarratorFillFromColumn: n, cbHeldITunes: 3},
		Classes: []cbClass{
			{Key: cbNarratorFillFromColumn, Kind: cbKindWrite, Definition: "d", Count: n, BookIDs: ids,
				Samples: []*cbSample{{BookID: ids[0]}}},
			{Key: cbHeldITunes, Kind: cbKindHeld, Definition: "d", Count: 3, BookIDs: ids[:3]},
		},
	}
	full, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	rep := &cbReporter{id: cbTestOpID}
	if err := storeCreditsBackfillResult(rep, f.s, cbTestOpID, res); err != nil {
		t.Fatal(err)
	}
	small, err := json.Marshal(rep.result)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 4_000_000 || len(small) > 20_000 {
		t.Fatalf("whole result %d bytes, stored result %d bytes: want the stored one to be tens of kilobytes", len(full), len(small))
	}
	stored := rep.result.(*creditsBackfillResult)
	last, err := stored.ClassLists.Page(f.s, cbTestOpID, cbNarratorFillFromColumn, n-3, 100)
	if err != nil || !slices.Equal(last, ids[n-3:]) {
		t.Fatalf("last page: %v %v", last, err)
	}
	if rows := opRowIDs(t, f.s, cbTestOpID); len(rows) != n/database.ClassListChunk+1 {
		t.Fatalf("%d rows stored, want %d", len(rows), n/database.ClassListChunk+1)
	}
}

// A result that cannot be stored takes its rows back, so no operation keeps an
// orphaned set; a second store replaces the first (a resumed run).
func TestCreditsBackfill_FailedResultWriteClearsTheClassListRows(t *testing.T) {
	f := newCBFixture(t)
	res := &creditsBackfillResult{Classes: []cbClass{
		{Key: cbHeldITunes, Kind: cbKindHeld, Definition: "d", Count: 2, BookIDs: []string{"b1", "b2"}},
	}}
	rep := &failingResultReporter{cbReporter{id: cbTestOpID}}
	if err := storeCreditsBackfillResult(rep, f.s, cbTestOpID, res); err == nil {
		t.Fatal("a failing result write was reported as stored")
	}
	if rows := opRowIDs(t, f.s, cbTestOpID); len(rows) != 0 {
		t.Fatalf("rows left behind a result that was never stored: %v", rows)
	}

	good := &cbReporter{id: cbTestOpID}
	if err := storeCreditsBackfillResult(good, f.s, cbTestOpID, res); err != nil {
		t.Fatal(err)
	}
	res2 := &creditsBackfillResult{Classes: []cbClass{
		{Key: cbHeldOwnerManual, Kind: cbKindHeld, Definition: "d", Count: 1, BookIDs: []string{"b9"}},
	}}
	again := &cbReporter{id: cbTestOpID}
	if err := storeCreditsBackfillResult(again, f.s, cbTestOpID, res2); err != nil {
		t.Fatal(err)
	}
	if rows := opRowIDs(t, f.s, cbTestOpID); !slices.Equal(rows, []string{"held_owner_manual/000000"}) {
		t.Fatalf("rows after a second store = %v, want only the second run's", rows)
	}

	if err := storeCreditsBackfillResult(good, nil, cbTestOpID, res); err == nil {
		t.Fatal("no row store was accepted")
	}
	if err := storeCreditsBackfillResult(good, f.s, "", res); err == nil {
		t.Fatal("no operation id was accepted")
	}
}
