// file: internal/seriesdenumber/select_test.go
// version: 1.0.0
// guid: 5e9b2d64-18c7-4a03-9f5e-a4d176c20b83
// last-edited: 2026-11-16

package seriesdenumber

import (
	"slices"
	"testing"
)

func TestSelect(t *testing.T) {
	items := []Item{
		{IntoName: "Knightfall", Position: 2, Books: 1},
		{IntoName: "Arkham", Position: 1, Books: 0}, // moves nothing
		{IntoName: "Hush", Position: 3, Books: 4},
		{IntoName: "Knightfall", Position: 1, Books: 2},
		{IntoName: "Arkham", Position: 5, Books: 1},
	}
	cases := []struct {
		name string
		sel  Selection
		want []int
	}{
		{"zero value: plans that move books, by base then position", Selection{}, []int{4, 2, 3, 0}},
		{"a plan that moves nothing is never selected, even at MinBooks 0", Selection{MinBooks: 0}, []int{4, 2, 3, 0}},
		{"min books", Selection{MinBooks: 2}, []int{2, 3}},
		{"limit counts book-carrying plans, so the empty one costs no slot", Selection{Limit: 2}, []int{4, 2}},
		{"limit larger than the set", Selection{Limit: 50}, []int{4, 2, 3, 0}},
		{"exact base, case blind", Selection{BaseName: "  knightfall "}, []int{3, 0}},
		{"base is exact, not a prefix", Selection{BaseName: "Knight"}, []int{}},
		{"base and limit", Selection{BaseName: "Knightfall", Limit: 1}, []int{3}},
		{"base and min books", Selection{BaseName: "Knightfall", MinBooks: 2}, []int{3}},
		{"nothing matches", Selection{BaseName: "Hush", MinBooks: 5}, []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Select(items, tc.sel)
			if got == nil {
				got = []int{}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Select = %v, want %v", got, tc.want)
			}
		})
	}
}

// Select must not reorder or mutate the caller's slice: the op keeps indexing
// plans by the original positions.
func TestSelect_DoesNotMutateItems(t *testing.T) {
	items := []Item{{IntoName: "Hush", Books: 1}, {IntoName: "Arkham", Books: 1}}
	_ = Select(items, Selection{})
	if items[0].IntoName != "Hush" || items[1].IntoName != "Arkham" {
		t.Fatalf("items reordered: %+v", items)
	}
}
