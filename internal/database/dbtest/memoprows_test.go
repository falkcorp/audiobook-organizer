// file: internal/database/dbtest/memoprows_test.go
// version: 1.0.0
// guid: aed47c42-ea13-41db-a55f-21821dbb3d0c
// last-edited: 2026-10-22

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
		if err := s.ReplaceOpRows("op-a", 4, rows(4, "1")); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceOpRows("op-b", 2, rows(2, "b")); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceOpRows("op-a", 3, rows(3, "2")); err != nil {
			t.Fatal(err)
		}
		got := dump(s, "op-a") + "#" + dump(s, "op-b")
		if err := s.ReplaceOpRows("op-b", 0, nil); err != nil {
			t.Fatal(err)
		}
		got += "#" + dump(s, "op-b")
		if err := s.ReplaceOpRows("", 0, nil); err == nil {
			t.Fatalf("%s: empty operation id accepted", name)
		}
		if name == "mem" {
			want = got
		} else if got != want {
			t.Fatalf("pebble and mem disagree:\n pebble %s\n mem    %s", got, want)
		}
	}
}
