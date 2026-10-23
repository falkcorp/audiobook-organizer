// file: internal/database/pebble_store_opchange_list_index_test.go
// version: 1.1.0
// guid: 0c6e3a52-81d7-4f09-b3a4-92d5e17f6c48
// last-edited: 2026-10-22

package database

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// listRow is a synthetic carry row: a user_content_follow row of a list field.
func listRow(op, id, book, field string) *OperationChange {
	return &OperationChange{ID: id, OperationID: op, BookID: book, ChangeType: "user_content_follow", FieldName: field}
}

// listEntries lists the "kind:listID" of every opchange_by_list entry of the
// row at (op, id).
func listEntries(t *testing.T, p *PebbleStore, op, id string) []string {
	t.Helper()
	suffix := string(opChangeKey(op, id)[len(opChangeKeyPrefix):])
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(opChangeByListPrefix), UpperBound: prefixEnd([]byte(opChangeByListPrefix))})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	var out []string
	for iter.First(); iter.Valid(); iter.Next() {
		k := string(iter.Key()[len(opChangeByListPrefix):])
		if len(k) > len(suffix) && k[len(k)-len(suffix):] == suffix {
			out = append(out, k[:len(k)-len(suffix)-1])
		}
	}
	return out
}

func listIDs(t *testing.T, p *PebbleStore, kind, list string) []string {
	t.Helper()
	got, err := p.GetListChanges(kind, list)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(got))
	for i, c := range got {
		out[i] = c.OperationID + "/" + c.ID
	}
	return out
}

func TestOpchangeListIndex_OnlyListRowsAreIndexed(t *testing.T) {
	p := newOpChangeTestStore(t)
	rows := []*OperationChange{
		listRow("op1", "a", "S", "collection:L1"),
		listRow("op1", "b", "S", "collection-held:L1"),
		listRow("op1", "c", "S", "collection-took:L1"),
		listRow("op1", "d", "S", "playlist:P1"),
		listRow("op1", "e", "S", "playlist-took:P1"),
		listRow("op1", "f", "S", "playlist-held:P1"),
		listRow("op1", "g", "S", "rating:overall"),                                                            // not a list field
		listRow("op1", "h", "S", "collection:"),                                                               // no list id
		{ID: "i", OperationID: "op1", BookID: "S", ChangeType: "metadata_update", FieldName: "collection:L1"}, // wrong type
	}
	for _, r := range rows {
		if err := p.CreateOperationChange(r); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"a": "collection:L1", "b": "collection:L1", "c": "collection:L1", "d": "playlist:P1", "e": "playlist:P1", "f": "playlist:P1"}
	for _, r := range rows {
		got := listEntries(t, p, "op1", r.ID)
		if w, ok := want[r.ID]; ok {
			if !reflect.DeepEqual(got, []string{w}) {
				t.Fatalf("row %s entries = %v, want [%s]", r.ID, got, w)
			}
		} else if len(got) != 0 {
			t.Fatalf("row %s has entries %v", r.ID, got)
		}
	}
	if got := listIDs(t, p, "collection", "L1"); len(got) != 3 {
		t.Fatalf("collection L1 = %v, want the 3 rows", got)
	}
	if got := listIDs(t, p, "playlist", "P1"); len(got) != 3 {
		t.Fatalf("playlist P1 = %v, want the 3 rows", got)
	}
	if got := listIDs(t, p, "collection", "P1"); len(got) != 0 {
		t.Fatalf("a playlist id read as a collection = %v", got)
	}
}

// The rows of every book come back from one call, across operations, in the
// primary key's order; the by-book read of one survivor sees only its own.
func TestOpchangeListIndex_ReadsEveryBooksRowsOfTheList(t *testing.T) {
	p := newOpChangeTestStore(t)
	for _, r := range []*OperationChange{
		listRow("op2", "x2", "S2", "collection-took:L"),
		listRow("op1", "x1", "S1", "collection-held:L"),
		listRow("op1", "y1", "R1", "collection:L"),
		listRow("op3", "z", "S1", "collection:OTHER"),
	} {
		if err := p.CreateOperationChange(r); err != nil {
			t.Fatal(err)
		}
	}
	got := listIDs(t, p, "collection", "L")
	want := []string{"op1/x1", "op1/y1", "op2/x2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list L = %v, want %v", got, want)
	}
	if got := listIDs(t, p, "collection", "OTHER"); !reflect.DeepEqual(got, []string{"op3/z"}) {
		t.Fatalf("list OTHER = %v", got)
	}
}

func TestOpchangeListIndex_RewriteMovesOrRemovesTheEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	r := listRow("op1", "r", "S", "collection:L1")
	if err := p.CreateOperationChange(r); err != nil {
		t.Fatal(err)
	}
	// Same list: still one entry.
	again := listRow("op1", "r", "S", "collection:L1")
	if err := p.CreateOperationChange(again); err != nil {
		t.Fatal(err)
	}
	if got := listEntries(t, p, "op1", "r"); !reflect.DeepEqual(got, []string{"collection:L1"}) {
		t.Fatalf("entries after a same-list rewrite = %v", got)
	}
	// Another list: the old entry goes.
	if err := p.CreateOperationChange(listRow("op1", "r", "S", "playlist-took:P9")); err != nil {
		t.Fatal(err)
	}
	if got := listEntries(t, p, "op1", "r"); !reflect.DeepEqual(got, []string{"playlist:P9"}) {
		t.Fatalf("entries after a move = %v, want [playlist:P9]", got)
	}
	if got := listIDs(t, p, "collection", "L1"); len(got) != 0 {
		t.Fatalf("the old list still reads the row: %v", got)
	}
	// Out of lists entirely.
	if err := p.CreateOperationChange(&OperationChange{ID: "r", OperationID: "op1", BookID: "S", ChangeType: "metadata_update"}); err != nil {
		t.Fatal(err)
	}
	if got := listEntries(t, p, "op1", "r"); len(got) != 0 {
		t.Fatalf("entries after leaving every list = %v", got)
	}
}

func TestOpchangeListIndex_MarkRevertedKeepsTheEntryAndShowsTheStamp(t *testing.T) {
	p := newOpChangeTestStore(t)
	for _, r := range []*OperationChange{listRow("op1", "a", "S", "collection:L"), listRow("op1", "b", "S", "collection-held:L")} {
		if err := p.CreateOperationChange(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.MarkOperationChangesReverted("op1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetListChanges("collection", "L")
	if err != nil || len(got) != 2 {
		t.Fatalf("GetListChanges = %v, %v", got, err)
	}
	for _, c := range got {
		if (c.ID == "a") != (c.RevertedAt != nil) {
			t.Fatalf("row %s RevertedAt = %v", c.ID, c.RevertedAt)
		}
	}
	if e := listEntries(t, p, "op1", "a"); !reflect.DeepEqual(e, []string{"collection:L"}) {
		t.Fatalf("entries after a mark = %v", e)
	}
}

// A mark rewrites the row, and re-Sets its list entry: an entry lost some other
// way (a crash between an older build's two writes, a partial restore) is back
// after the mark, so a row the journal marks is never missing from its list.
func TestOpchangeListIndex_MarkRevertedRestoresAMissingEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.CreateOperationChange(listRow("op1", "a", "S", "collection:L")); err != nil {
		t.Fatal(err)
	}
	key := append(opChangeByListListPrefix("collection", "L"), "op1:a"...)
	if err := p.db.Delete(key, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, p, "collection", "L"); len(got) != 0 {
		t.Fatalf("entry deleted, yet the list reads %v", got)
	}
	if err := p.MarkOperationChangesReverted("op1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetListChanges("collection", "L")
	if err != nil || len(got) != 1 || got[0].RevertedAt == nil {
		t.Fatalf("after the mark the list reads %v, %v", got, err)
	}
}

// A voided row (written with Voided set) is still in its list's journal.
func TestOpchangeListIndex_VoidedRowsAreIndexed(t *testing.T) {
	p := newOpChangeTestStore(t)
	now := time.Now()
	v := listRow("op1", "v", "S", "collection:L")
	v.Voided, v.RevertedAt = true, &now
	if err := p.CreateOperationChange(v); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetListChanges("collection", "L")
	if err != nil || len(got) != 1 || !got[0].Voided {
		t.Fatalf("GetListChanges = %v, %v; want the voided row", got, err)
	}
}

// Prune parity: a pruned row takes its list entry with it. user_content_follow
// is in OpChangeTypesKeptByPrune and never pruned in practice, so the test
// takes it out of the set for its own duration (not parallel; the set is a
// package variable).
func TestOpchangeListIndex_PruneDeletesTheEntry(t *testing.T) {
	was, had := OpChangeTypesKeptByPrune["user_content_follow"]
	delete(OpChangeTypesKeptByPrune, "user_content_follow")
	t.Cleanup(func() {
		if had {
			OpChangeTypesKeptByPrune["user_content_follow"] = was
		}
	})
	p := newOpChangeTestStore(t)
	if err := p.CreateOperationChange(listRow("op1", "old", "S", "collection:L")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	cut := time.Now()
	time.Sleep(2 * time.Millisecond)
	if err := p.CreateOperationChange(listRow("op2", "new", "S", "collection:L")); err != nil {
		t.Fatal(err)
	}
	n, err := p.PruneOperationChanges(context.Background(), cut, nil)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	if e := listEntries(t, p, "op1", "old"); len(e) != 0 {
		t.Fatalf("pruned row still has list entries %v", e)
	}
	if got := listIDs(t, p, "collection", "L"); !reflect.DeepEqual(got, []string{"op2/new"}) {
		t.Fatalf("list after prune = %v", got)
	}
}

// And with the real set, the carry rows survive an age-based prune.
func TestOpchangeListIndex_PruneKeepsCarryRows(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.CreateOperationChange(listRow("op1", "old", "S", "collection:L")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if n, err := p.PruneOperationChanges(context.Background(), time.Now(), nil); err != nil || n != 0 {
		t.Fatalf("prune = %d, %v; want 0", n, err)
	}
	if got := listIDs(t, p, "collection", "L"); len(got) != 1 {
		t.Fatalf("list = %v, want the carry row kept", got)
	}
}

// A list id with ':' cannot be keyed without colliding with another list, so it
// has no entries and GetListChanges scans for it; the rows still come back, and
// the list "a" is not confused with "a:b".
func TestOpchangeListIndex_ColonListIDIsScanned(t *testing.T) {
	p := newOpChangeTestStore(t)
	for _, r := range []*OperationChange{
		listRow("op1", "c1", "S", "collection:a:b"),
		listRow("op1", "c2", "S", "collection:a"),
	} {
		if err := p.CreateOperationChange(r); err != nil {
			t.Fatal(err)
		}
	}
	if e := listEntries(t, p, "op1", "c1"); len(e) != 0 {
		t.Fatalf("colon list has entries %v", e)
	}
	if got := listIDs(t, p, "collection", "a:b"); !reflect.DeepEqual(got, []string{"op1/c1"}) {
		t.Fatalf("a:b = %v", got)
	}
	if got := listIDs(t, p, "collection", "a"); !reflect.DeepEqual(got, []string{"op1/c2"}) {
		t.Fatalf("a = %v", got)
	}
}

func TestOpchangeListIndex_DanglingEntriesAreSkipped(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.CreateOperationChange(listRow("op1", "a", "S", "collection:L")); err != nil {
		t.Fatal(err)
	}
	// An entry with no row, and an entry whose row names another list.
	if err := p.db.Set([]byte(opChangeByListPrefix+"collection:L:op9:ghost"), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.CreateOperationChange(listRow("op1", "m", "S", "collection:OTHER")); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte(opChangeByListPrefix+"collection:L:op1:m"), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, p, "collection", "L"); !reflect.DeepEqual(got, []string{"op1/a"}) {
		t.Fatalf("list = %v, want only the real row", got)
	}
}

func TestOpchangeListIndex_UnknownKindAndEmptyIDAreErrors(t *testing.T) {
	p := newOpChangeTestStore(t)
	if _, err := p.GetListChanges("series", "L"); err == nil {
		t.Fatal("an unknown kind must be an error")
	}
	if _, err := p.GetListChanges("collection", ""); err == nil {
		t.Fatal("an empty list id must be an error")
	}
}

// CreateOperationChanges is one batch: a failing row (here, an unmarshalable
// one cannot be built, so the batch is checked through its observable parts)
// lands every row with its entries, or none.
func TestOpchangeListIndex_CreateOperationChangesIsOneBatch(t *testing.T) {
	p := newOpChangeTestStore(t)
	rows := []*OperationChange{
		listRow("op1", "m", "S", "collection-held:L"),
		listRow("op1", "r", "R", "collection:L"),
	}
	if err := p.CreateOperationChanges(rows); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, p, "collection", "L"); !reflect.DeepEqual(got, []string{"op1/m", "op1/r"}) {
		t.Fatalf("list = %v", got)
	}
	for _, b := range []string{"S", "R"} {
		if got, err := p.GetBookChanges(b); err != nil || len(got) != 1 {
			t.Fatalf("book %s = %v, %v", b, changeIDs(got), err)
		}
	}
	if !rows[0].CreatedAt.Before(rows[1].CreatedAt) {
		t.Fatalf("rows are stamped in order: %v then %v", rows[0].CreatedAt, rows[1].CreatedAt)
	}
	if err := p.CreateOperationChanges(nil); err != nil {
		t.Fatalf("an empty batch is a no-op: %v", err)
	}
}

// A batch that fails before its commit leaves nothing behind: no row, no book
// or list index entry, no clock key.
func TestOpchangeListIndex_CreateOperationChangesFailsAsAWhole(t *testing.T) {
	p := newOpChangeTestStore(t)
	journalAfterStamp = func() error { return errors.New("injected failure before the commit") }
	defer func() { journalAfterStamp = nil }()
	err := p.CreateOperationChanges([]*OperationChange{
		listRow("op1", "m", "S", "collection-held:L"),
		listRow("op1", "r", "R", "collection:L"),
	})
	if err == nil {
		t.Fatal("the injected failure must surface")
	}
	journalAfterStamp = nil
	if got := listIDs(t, p, "collection", "L"); len(got) != 0 {
		t.Fatalf("list entries landed: %v", got)
	}
	if all := scanAllChanges(t, p); len(all) != 0 {
		t.Fatalf("rows landed: %v", changeIDs(all))
	}
	for _, b := range []string{"S", "R"} {
		if got, err := p.GetBookChanges(b); err != nil || len(got) != 0 {
			t.Fatalf("book %s index landed: %v, %v", b, changeIDs(got), err)
		}
	}
	if v, ok := persistedClock(t, p); ok {
		t.Fatalf("the clock key landed: %d", v)
	}
}

// A repeated (operation, id) in one batch is refused up front: the second row
// would silently replace the first.
func TestOpchangeListIndex_CreateOperationChangesRejectsDuplicateIDs(t *testing.T) {
	p := newOpChangeTestStore(t)
	err := p.CreateOperationChanges([]*OperationChange{
		listRow("op1", "m", "S", "collection-held:L"),
		listRow("op1", "m", "R", "collection:L"),
	})
	if err == nil {
		t.Fatal("a repeated id must be an error")
	}
	if all := scanAllChanges(t, p); len(all) != 0 {
		t.Fatalf("nothing may be written: %v", changeIDs(all))
	}
	// The same id under another operation is a different row.
	if err := p.CreateOperationChanges([]*OperationChange{
		listRow("op1", "m", "S", "collection-held:L"),
		listRow("op2", "m", "R", "collection:L"),
	}); err != nil {
		t.Fatalf("same id, different operation: %v", err)
	}
}

func scanAllChanges(t *testing.T, p *PebbleStore) []*OperationChange {
	t.Helper()
	var all []*OperationChange
	if err := p.ScanOperationChanges(func(c *OperationChange) error {
		all = append(all, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return all
}

// The property: after random creates, rewrites (the same id again, possibly in
// another list or operation) and marks, the list read equals a scan of the whole
// journal, for every list.
func TestOpchangeListIndex_PropertyMatchesScan(t *testing.T) {
	fields := []string{"collection:L1", "collection-held:L1", "collection-took:L2", "playlist:P1", "playlist-took:P1", "rating:overall", "collection:a:b"}
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			p := newOpChangeTestStore(t)
			opOf := map[string]string{} // a row id keeps its operation: the key has it
			for step := 0; step < 400; step++ {
				id := fmt.Sprintf("c%d", rng.Intn(60))
				op, ok := opOf[id]
				if !ok {
					op = fmt.Sprintf("op%d", rng.Intn(3))
					opOf[id] = op
				}
				if rng.Intn(4) < 3 {
					c := listRow(op, id, fmt.Sprintf("b%d", rng.Intn(6)), fields[rng.Intn(len(fields))])
					if err := p.CreateOperationChange(c); err != nil {
						t.Fatal(err)
					}
				} else if err := p.MarkOperationChangesReverted(op, []string{id}); err != nil {
					t.Fatal(err)
				}
			}
			all := scanAllChanges(t, p)
			for _, kl := range [][2]string{{"collection", "L1"}, {"collection", "L2"}, {"playlist", "P1"}, {"collection", "a:b"}, {"collection", "none"}} {
				var want []string
				for _, c := range all {
					if k, id, ok := opChangeListRef(c); ok && k == kl[0] && id == kl[1] {
						want = append(want, c.OperationID+"/"+c.ID)
					}
				}
				got := listIDs(t, p, kl[0], kl[1])
				sort.Strings(want)
				sort.Strings(got)
				if len(want) != len(got) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
					t.Fatalf("%v: index read %v, scan %v", kl, got, want)
				}
			}
		})
	}
}
