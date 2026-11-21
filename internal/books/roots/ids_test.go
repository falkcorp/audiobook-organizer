// file: internal/books/roots/ids_test.go
// version: 0.1.0
// guid: 664662dc-903f-46b9-9d44-a69850387c5a
// last-edited: 2026-11-20

package roots_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

func TestImportPathID_RoundTripAndRefusals(t *testing.T) {
	for _, n := range []int{0, 1, 7, 42, 1000} {
		id := roots.RootIDFor(n)
		got, ok := roots.ImportPathID(id)
		if !ok || got != n {
			t.Errorf("ImportPathID(%q) = %d, %v; want %d, true", id, got, ok, n)
		}
	}
	for _, id := range []library.RootID{"books-library", "import-", "import-007", "import--1", "import-1a", "dl-qbit-1", "", "import-1-2"} {
		if n, ok := roots.ImportPathID(id); ok {
			t.Errorf("ImportPathID(%q) = %d, true; want false", id, n)
		}
	}
}

func TestNextImportN(t *testing.T) {
	cases := []struct {
		name string
		ids  []library.RootID
		want int
	}{
		{"none", nil, 1},
		{"books-library only", []library.RootID{"books-library"}, 1},
		{"above the highest", []library.RootID{"import-1", "import-9", "import-3"}, 10},
		{"foreign ids ignored", []library.RootID{"dl-qbit-1", "import-2", "import-x"}, 3},
	}
	for _, c := range cases {
		if got := roots.NextImportN(c.ids...); got != c.want {
			t.Errorf("%s: NextImportN = %d; want %d", c.name, got, c.want)
		}
	}
}

// The shapes here are the ones M6 writes; a drift between the two is a
// migrated row the live code cannot read.
func TestRootscanShapePinnedToM6(t *testing.T) {
	if roots.PrefixRootScan != m6.PrefixRootScan {
		t.Errorf("prefix %q != m6 %q", roots.PrefixRootScan, m6.PrefixRootScan)
	}
	if roots.BooksLibraryRoot != m6.BooksLibraryRoot {
		t.Errorf("books-library root %q != m6 %q", roots.BooksLibraryRoot, m6.BooksLibraryRoot)
	}
	for _, n := range []int{0, 1, 12, 345} {
		if roots.RootIDFor(n) != m6.RootID(n) {
			t.Errorf("RootIDFor(%d) = %q; m6 %q", n, roots.RootIDFor(n), m6.RootID(n))
		}
	}
	for _, id := range []library.RootID{"import-5", "import-05", "books-library", "import-"} {
		gn, gok := roots.ImportPathID(id)
		wn, wok := m6.ImportPathID(id)
		if gn != wn || gok != wok {
			t.Errorf("ImportPathID(%q) = %d,%v; m6 %d,%v", id, gn, gok, wn, wok)
		}
	}
	at := time.Date(2026, 11, 20, 3, 4, 5, 0, time.UTC)
	last := at.Add(time.Hour)
	live := roots.ScanState{Enabled: true, CreatedAt: at, LastScan: &last, BookCount: 12}
	frozen := m6.RootScan{Enabled: true, CreatedAt: at, LastScan: &last, BookCount: 12}
	a, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("row bytes differ:\n live   %s\n m6     %s", a, b)
	}
	// And the no-LastScan form (omitempty).
	live.LastScan, frozen.LastScan = nil, nil
	a, _ = json.Marshal(live)
	b, _ = json.Marshal(frozen)
	if string(a) != string(b) {
		t.Errorf("row bytes differ without LastScan:\n live   %s\n m6     %s", a, b)
	}
}
