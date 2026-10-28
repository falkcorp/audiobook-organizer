// file: internal/database/dbtest/memoprows_test.go
// version: 1.0.2
// guid: aed47c42-ea13-41db-a55f-21821dbb3d0c
// last-edited: 2026-10-27

package dbtest

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestOpRowStoresAgree runs one script against MemOpRows and PebbleStore's
// implementation and requires the same answers, so a test double never drifts
// from the store it stands for.
func TestOpRowStoresAgree(t *testing.T) {
	stores := map[string]database.OpRowStore{"mem": NewMemOpRows(), "pebble": NewStore(t)}
	rows := func(n int, tag string) func(int) (database.OpRow, error) {
		return func(i int) (database.OpRow, error) {
			id := fmt.Sprintf("r%03d", n-1-i) // handed over out of order on purpose
			return database.OpRow{ID: id, Index: []byte("ix" + tag + id), Data: []byte("d" + tag + id)}, nil
		}
	}
	dump := func(s database.OpRowStore, op string) string {
		var out string
		if err := s.ScanOpRowIndex(op, func(id string, ix []byte) error {
			out += id + "=" + string(ix) + ";"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOpRows(op, []string{"r000", "nope", "r002"})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s|%q", out, got)
	}
	var want string
	for _, name := range []string{"mem", "pebble"} {
		s := stores[name]
		if err := s.ReplaceOpRowsPrefix("op-a", "r", 4, rows(4, "1")); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceOpRowsPrefix("op-b", "r", 2, rows(2, "b")); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceOpRowsPrefix("op-a", "r", 3, rows(3, "2")); err != nil {
			t.Fatal(err)
		}
		got := dump(s, "op-a") + "#" + dump(s, "op-b")
		if err := s.ReplaceOpRowsPrefix("op-b", "r", 0, nil); err != nil {
			t.Fatal(err)
		}
		got += "#" + dump(s, "op-b")
		if err := s.ReplaceOpRowsPrefix("", "r", 0, nil); err == nil {
			t.Fatalf("%s: empty operation id accepted", name)
		}
		if err := s.ReplaceOpRowsPrefix("op-a", "", 0, nil); err == nil {
			t.Fatalf("%s: an empty row prefix (every namespace) accepted", name)
		}
		if name == "mem" {
			want = got
		} else if got != want {
			t.Fatalf("pebble and mem disagree:\n pebble %s\n mem    %s", got, want)
		}
	}
}

// ReplaceOpRowsPrefix replaces only the rows whose id begins with the prefix:
// the operation's other rows (another kind of row under the same operation id,
// such as a plan's rows beside a result's class lists) and other operations'
// rows survive, a row outside the prefix is refused, and the double and the
// Pebble store answer alike.
func TestOpRowStoresAgree_ReplaceOpRowsPrefixLeavesOtherRowsAlone(t *testing.T) {
	stores := map[string]database.OpRowStore{"mem": NewMemOpRows(), "pebble": NewStore(t)}
	set := func(ids ...string) func(int) (database.OpRow, error) {
		return func(i int) (database.OpRow, error) {
			return database.OpRow{ID: ids[i], Index: []byte("ix-" + ids[i]), Data: []byte("d-" + ids[i])}, nil
		}
	}
	list := func(s database.OpRowStore, op string) []string {
		var out []string
		if err := s.ScanOpRowIndex(op, func(id string, _ []byte) error { out = append(out, id); return nil }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for name, s := range stores {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		must(s.ReplaceOpRowsPrefix("op-a", "plan/", 2, set("plan/1", "plan/2")))
		must(s.ReplaceOpRowsPrefix("op-b", "lists/", 1, set("lists/x/000000")))
		must(s.ReplaceOpRowsPrefix("op-a", "lists/", 2, set("lists/x/000000", "lists/x/000001")))
		must(s.ReplaceOpRowsPrefix("op-a", "lists/", 1, set("lists/y/000000")))
		if got, want := fmt.Sprint(list(s, "op-a")), "[lists/y/000000 plan/1 plan/2]"; got != want {
			t.Fatalf("%s: op-a rows %s, want %s (the plan rows must survive, the first lists must go)", name, got, want)
		}
		if got, want := fmt.Sprint(list(s, "op-b")), "[lists/x/000000]"; got != want {
			t.Fatalf("%s: op-b rows %s, want %s", name, got, want)
		}
		if err := s.ReplaceOpRowsPrefix("op-a", "lists/", 1, set("plan/3")); err == nil {
			t.Fatalf("%s: a row outside the replaced prefix was accepted", name)
		}
		must(s.ReplaceOpRowsPrefix("op-a", "lists/", 0, nil))
		if got, want := fmt.Sprint(list(s, "op-a")), "[plan/1 plan/2]"; got != want {
			t.Fatalf("%s: op-a rows after clearing the prefix %s, want %s", name, got, want)
		}
	}
}
