// file: internal/database/op_class_lists_test.go
// version: 1.0.0
// guid: 8e2a4c71-5d03-4b96-a1f8-6c3d9b7e0f24
// last-edited: 2026-10-27

package database

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// classListIDs makes n book ids in sorted order.
func classListIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%06d", prefix, i)
	}
	return ids
}

// A class bigger than one stored row is read back in full through pages that
// start and end inside a row, across a row boundary and past the end, and a
// class with no ids has no rows and an empty page.
func TestClassLists_PagesAcrossChunkBoundaries(t *testing.T) {
	p := newTimelineStore(t)
	lists := map[string][]string{
		"narrator_fill_from_column": classListIDs("bk", ClassListChunk*2+345),
		"filing_author_changes":     classListIDs("fa", 7),
		"held_itunes":               nil,
	}
	sum, err := SaveClassLists(p, "op-a", lists)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Valid() || sum.Counts["narrator_fill_from_column"] != ClassListChunk*2+345 ||
		sum.Counts["filing_author_changes"] != 7 || sum.Counts["held_itunes"] != 0 {
		t.Fatalf("summary %+v", sum)
	}

	big := lists["narrator_fill_from_column"]
	for _, c := range []struct{ off, limit int }{
		{0, 10}, {ClassListChunk - 3, 6}, {ClassListChunk, ClassListChunk}, {5, len(big)}, {len(big) - 2, 50},
	} {
		got, err := sum.Page(p, "op-a", "narrator_fill_from_column", c.off, c.limit)
		if err != nil {
			t.Fatalf("page %+v: %v", c, err)
		}
		want := big[c.off:min(c.off+c.limit, len(big))]
		if !slices.Equal(got, want) {
			t.Fatalf("page %+v: got %d ids starting %v, want %d starting %v", c, len(got), head(got), len(want), head(want))
		}
	}
	if got, err := sum.Page(p, "op-a", "filing_author_changes", 0, 100); err != nil || !slices.Equal(got, lists["filing_author_changes"]) {
		t.Fatalf("small class: %v %v", got, err)
	}
	if got, err := sum.Page(p, "op-a", "held_itunes", 0, 100); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty class: %v %v, want an empty non-nil page", got, err)
	}
	if got, err := sum.Page(p, "op-a", "filing_author_changes", 7, 100); err != nil || len(got) != 0 {
		t.Fatalf("offset at the count: %v %v, want an empty page", got, err)
	}
	if _, err := sum.Page(p, "op-a", "no_such_class", 0, 10); !errors.Is(err, ErrClassListUnknown) {
		t.Fatalf("unknown class: %v", err)
	}
	if _, err := sum.Page(p, "op-a", "held_itunes", -1, 10); err == nil {
		t.Fatal("a negative offset was accepted")
	}
}

func head(ids []string) []string { return ids[:min(2, len(ids))] }

// Saving again under the same operation id (a resumed run) leaves exactly the
// new set, and another operation's set is untouched.
func TestClassLists_SaveReplacesTheOperationsSetOnly(t *testing.T) {
	p := newTimelineStore(t)
	if _, err := SaveClassLists(p, "op-other", map[string][]string{"x": classListIDs("o", 3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveClassLists(p, "op-a", map[string][]string{"x": classListIDs("old", ClassListChunk+5), "gone": classListIDs("g", 2)}); err != nil {
		t.Fatal(err)
	}
	sum, err := SaveClassLists(p, "op-a", map[string][]string{"x": classListIDs("new", 4)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sum.Page(p, "op-a", "x", 0, 100)
	if err != nil || !slices.Equal(got, classListIDs("new", 4)) {
		t.Fatalf("after the second save: %v %v", got, err)
	}
	if _, err := sum.Page(p, "op-a", "gone", 0, 10); !errors.Is(err, ErrClassListUnknown) {
		t.Fatalf("a class the second save left out: %v", err)
	}
	var rows []string
	if err := p.ScanOpRowIndex("op-a", func(id string, _ []byte) error { rows = append(rows, id); return nil }); err != nil {
		t.Fatal(err)
	}
	if want := []string{classListRowID("x", 0)}; !slices.Equal(rows, want) {
		t.Fatalf("op-a rows %v, want only %v (the first save's rows must be gone)", rows, want)
	}
	other := &ClassListSummary{Store: ClassListsStoreV1, Chunk: ClassListChunk, Counts: map[string]int{"x": 3}}
	if got, err := other.Page(p, "op-other", "x", 0, 10); err != nil || !slices.Equal(got, classListIDs("o", 3)) {
		t.Fatalf("the other operation's set: %v %v", got, err)
	}
}

// Rows that are missing, or do not hold what the summary counts, are reported
// as an incomplete set (the remedy is a new run), never served short.
func TestClassLists_IncompleteSetIsReportedNotServedShort(t *testing.T) {
	p := newTimelineStore(t)
	sum, err := SaveClassLists(p, "op-a", map[string][]string{"x": classListIDs("bk", ClassListChunk+10)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ClearClassLists(p, "op-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := sum.Page(p, "op-a", "x", 0, 10); !errors.Is(err, ErrClassListIncomplete) {
		t.Fatalf("rows deleted: %v, want ErrClassListIncomplete", err)
	}

	// A set cut short by a summary that counts more than was written.
	if _, err := SaveClassLists(p, "op-a", map[string][]string{"x": classListIDs("bk", 5)}); err != nil {
		t.Fatal(err)
	}
	liar := &ClassListSummary{Store: ClassListsStoreV1, Chunk: ClassListChunk, Counts: map[string]int{"x": 9}}
	if _, err := liar.Page(p, "op-a", "x", 0, 10); !errors.Is(err, ErrClassListIncomplete) {
		t.Fatalf("summary counts more than the row holds: %v, want ErrClassListIncomplete", err)
	}
	var none *ClassListSummary
	if _, err := none.Page(p, "op-a", "x", 0, 10); !errors.Is(err, ErrClassListIncomplete) {
		t.Fatalf("no summary: %v, want ErrClassListIncomplete", err)
	}
	if none.Valid() || (&ClassListSummary{Store: "something.else", Chunk: 1, Counts: map[string]int{}}).Valid() {
		t.Fatal("Valid accepted a missing or foreign summary")
	}
}
