// file: internal/seriesdenumber/select.go
// version: 1.1.0
// guid: 41d8a2f7-93be-4c05-8e16-b07a5c3d9e28
// last-edited: 2026-11-17

package seriesdenumber

import (
	"sort"
	"strings"
)

// Item is one plan that is allowed to apply, as Select needs it.
type Item struct {
	IntoName string
	Position int
	// Books is how many books the plan would move, after the position checks.
	Books int
}

// Selection scopes an apply. The zero value selects everything that moves a
// book.
type Selection struct {
	// MinBooks keeps only plans that move at least this many books. Values below
	// 1 are read as 1: a plan that moves nothing is never selected, so it can
	// neither create a base series nor spend a canary slot.
	MinBooks int
	// BaseName, when set, keeps only plans folding into this base name, under any
	// author (compared the way the store compares series names: ignoring case and
	// folding runs of space).
	BaseName string
	// Limit caps how many plans are selected; 0 means no cap. It counts plans
	// that move books, because every selected plan does.
	Limit int
}

// Select returns the indices of the items to apply, in apply order: plans
// carrying books only, by base name then position, so the first Limit plans of
// a canary are the first plans that actually change something and a dry run
// and the apply after it agree.
func Select(items []Item, sel Selection) []int {
	min := max(sel.MinBooks, 1)
	want := foldName(sel.BaseName)

	idx := make([]int, 0, len(items))
	for i, it := range items {
		if it.Books < min {
			continue
		}
		if want != "" && foldName(it.IntoName) != want {
			continue
		}
		idx = append(idx, i)
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := items[idx[a]], items[idx[b]]
		if x.IntoName != y.IntoName {
			return x.IntoName < y.IntoName
		}
		return x.Position < y.Position
	})
	if sel.Limit > 0 && len(idx) > sel.Limit {
		idx = idx[:sel.Limit]
	}
	return idx
}

// foldName is the store's series-name key: lower case, runs of space folded to one.
func foldName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
