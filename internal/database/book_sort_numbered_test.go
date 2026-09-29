// file: internal/database/book_sort_numbered_test.go
// version: 1.0.0
// guid: 1c7f4a83-6e2d-4b95-a0c8-9d3e5b7f2a61
// last-edited: 2026-09-28

package database

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// The materialised title sort and the memdb title index use one key, so the
// spellings of a numbered book file together under its name in both.
func TestTitleSort_NumberedBooksShareOneKey(t *testing.T) {
	want := TitleSortKey("1 Corinthians", nil)
	if want != "corinthians 1" {
		t.Fatalf("TitleSortKey(1 Corinthians) = %q", want)
	}
	for _, title := range []string{"I Corinthians", "First Corinthians", "l Corinthians"} {
		if got := TitleSortKey(title, nil); got != want {
			t.Errorf("TitleSortKey(%q) = %q, want %q", title, got, want)
		}
		_, idx, err := titleSortIndex{}.FromObject(&Book{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		if string(idx[:len(idx)-1]) != want {
			t.Errorf("titleSortIndex(%q) = %q, want %q", title, idx, want)
		}
	}
	args, err := titleSortIndex{}.FromArgs("First Corinthians")
	if err != nil {
		t.Fatal(err)
	}
	if string(args[:len(args)-1]) != util.TitleSortKey("I Corinthians") {
		t.Errorf("FromArgs key %q drifts from FromObject", args)
	}
	// Every other title keeps its plain normalised key.
	if got := TitleSortKey("  I, Robot ", nil); got != "i, robot" {
		t.Errorf("TitleSortKey(I, Robot) = %q", got)
	}
	if c := CompareSortStrings(TitleSortKey("II Corinthians", nil), TitleSortKey("Daniel", nil)); c >= 0 {
		t.Errorf("II Corinthians should sort before Daniel, compare = %d", c)
	}
}
