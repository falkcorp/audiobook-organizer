// file: internal/seriesdenumber/run_test.go
// version: 1.0.0
// guid: 6d03b8a4-e1f7-4c59-8b20-95a7c4e1d368
// last-edited: 2026-11-17

package seriesdenumber

import (
	"strings"
	"testing"
)

// one is a member of a run: one series holding one book with a title.
func one(id, n int, title string) RunSeries {
	return RunSeries{ID: id, N: n, Books: []Facts{{ID: "b" + strings.Repeat("x", id), Title: title}}}
}

func TestTitleKey(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Hush", "hush"},
		{"Hush Part 1", "hush"},
		{"Hush Part 2", "hush"},
		{"Hush 03", "hush"},
		{"Hush, Book One", "hush"},
		{"Hush - Disc 2 of 3", "hush"},
		{"Hush (Unabridged)", "hush"},
		{"Batman: Hush", "batman hush"},
		{"A Death in the Family", "a death in the family"},
		{"Under the Red Hood", "under the red hood"},
		{"Part 1", ""},
		{"Chapter 12", ""},
		{"03", ""},
		{"", ""},
		{"Don't Look Up Part 2", "don't look up"},
		{"HUSH   PART   3", "hush"},
	}
	for _, tc := range cases {
		if got := TitleKey(tc.title); got != tc.want {
			t.Errorf("TitleKey(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestCheckRun_NoRun(t *testing.T) {
	cases := []struct {
		name string
		self RunSeries
		sib  []RunSeries
	}{
		{"no siblings", one(1, 5, "A Death in the Family"), nil},
		{"one sibling", one(1, 2, "A Death in the Family"), []RunSeries{one(2, 3, "Under the Red Hood")}},
		{"two unrelated numbers", one(1, 66, "A Death in the Family"), []RunSeries{one(2, 89, "Under the Red Hood")}},
		{"n joins none of two siblings", one(1, 66, "A Death in the Family"), []RunSeries{one(2, 89, "Under the Red Hood"), one(3, 90, "Hush")}},
		{"a gap breaks the run", one(1, 1, "A Death in the Family"), []RunSeries{one(2, 2, "Under the Red Hood"), one(3, 4, "Hush")}},
		{"the run is two long", one(1, 5, "A Death in the Family"), []RunSeries{one(2, 6, "Under the Red Hood"), one(3, 9, "Hush")}},
	}
	for _, tc := range cases {
		if v := CheckRun("Batman", tc.self, tc.sib); v.State != RunNone {
			t.Errorf("%s: state %v (%s), want RunNone", tc.name, v.State, v.Reason)
		}
	}
}

func TestCheckRun_ADistinctRunIsConfirmed(t *testing.T) {
	titles := []string{"A Death in the Family", "A Lonely Place of Dying", "Under the Red Hood", "Hush", "Heart of Hush"}
	for _, selfAt := range []int{0, 1, 2} {
		var all []RunSeries
		for i := range 3 {
			all = append(all, one(i+1, i+4, titles[i]))
		}
		self := all[selfAt]
		var sib []RunSeries
		for i, s := range all {
			if i != selfAt {
				sib = append(sib, s)
			}
		}
		if v := CheckRun("Batman", self, sib); v.State != RunConfirmed {
			t.Errorf("self at %d: state %v (%s), want RunConfirmed", selfAt, v.State, v.Reason)
		}
	}
	// A long run is read whole, and the order the siblings arrive in does not matter.
	var all []RunSeries
	for i := range 5 {
		all = append(all, one(i+1, i+1, titles[i]))
	}
	if v := CheckRun("Batman", all[2], []RunSeries{all[4], all[0], all[3], all[1]}); v.State != RunConfirmed {
		t.Errorf("long run: %v (%s)", v.State, v.Reason)
	}
}

// B2(d): runs that are the parts of one shattered book, or one mixed series, are
// refused as a RUN, whatever the numbers look like.
func TestCheckRun_ARunOfOneShatteredBookIsRejected(t *testing.T) {
	asin := func(a, title string) []Facts { return []Facts{{ID: "b" + title, Title: title, ASIN: a}} }
	cases := []struct {
		name    string
		members []RunSeries
		want    string
	}{
		{"one title", []RunSeries{one(1, 1, "Hush"), one(2, 2, "Hush"), one(3, 3, "Hush")}, "same title"},
		{"the same title in other letters", []RunSeries{one(1, 1, "HUSH"), one(2, 2, "hush  "), one(3, 3, "Under the Red Hood")}, "same title"},
		{"part numbers only differ", []RunSeries{one(1, 1, "Hush Part 1"), one(2, 2, "Hush Part 2"), one(3, 3, "Hush Part 3")}, "same title"},
		{"the digits only differ", []RunSeries{one(1, 1, "Hush 01"), one(2, 2, "Hush 02"), one(3, 3, "Hush 03")}, "same title"},
		{"the title is the base", []RunSeries{one(1, 1, "Batman"), one(2, 2, "Hush"), one(3, 3, "Under the Red Hood")}, "base name itself"},
		{"the base with a number is the title", []RunSeries{one(1, 1, "Batman 1"), one(2, 2, "Hush"), one(3, 3, "Under the Red Hood")}, "base name itself"},
		{"a title that is nothing but part words", []RunSeries{one(1, 1, "Part 1"), one(2, 2, "Hush"), one(3, 3, "Under the Red Hood")}, "no title"},
		{"an empty title", []RunSeries{one(1, 1, ""), one(2, 2, "Hush"), one(3, 3, "Under the Red Hood")}, "no title"},
		{"one ASIN", []RunSeries{
			{ID: 1, N: 1, Books: asin("B00BANE001", "A Death in the Family")}, {ID: 2, N: 2, Books: asin("b00bane001", "Hush")}, one(3, 3, "Under the Red Hood")}, "share the identifier"},
		{"one ISBN, hyphenated or not", []RunSeries{
			{ID: 1, N: 1, Books: []Facts{{ID: "a", Title: "A Death in the Family", ISBN13: "978-0-7851-1234-5"}}},
			{ID: 2, N: 2, Books: []Facts{{ID: "b", Title: "Hush", ISBN13: "9780785112345"}}},
			one(3, 3, "Under the Red Hood")}, "share the identifier"},
		{"an ISBN-10 shared", []RunSeries{
			{ID: 1, N: 1, Books: []Facts{{ID: "a", Title: "A Death in the Family", ISBN10: "0785112340"}}},
			{ID: 2, N: 2, Books: []Facts{{ID: "b", Title: "Hush", ISBN10: "0-7851-1234-0"}}},
			one(3, 3, "Under the Red Hood")}, "share the identifier"},
		{"a member that holds two distinct books", []RunSeries{
			one(1, 1, "A Death in the Family"),
			{ID: 2, N: 2, Books: []Facts{{ID: "a", Title: "Hush"}, {ID: "b", Title: "Heart of Hush"}}},
			one(3, 3, "Under the Red Hood")}, "distinct books"},
		{"a member that holds no book", []RunSeries{one(1, 1, "A Death in the Family"), {ID: 2, N: 2}, one(3, 3, "Under the Red Hood")}, "holds no book"},
		{"two series carry one number", []RunSeries{one(1, 1, "A Death in the Family"), one(2, 2, "Hush"), one(4, 2, "Heart of Hush"), one(3, 3, "Under the Red Hood")}, "carry the number 2"},
	}
	for _, tc := range cases {
		// Every member takes a turn as the series being decided: the verdict about
		// the run must not depend on which one asks.
		for i, self := range tc.members {
			var sib []RunSeries
			for j, s := range tc.members {
				if j != i {
					sib = append(sib, s)
				}
			}
			v := CheckRun("Batman", self, sib)
			if v.State != RunRejected || !strings.Contains(v.Reason, tc.want) {
				t.Errorf("%s (member %d asks): state %v reason %q, want RunRejected mentioning %q", tc.name, i, v.State, v.Reason, tc.want)
			}
		}
	}
}

// Versions of one book are one book: they may share a title and an identifier.
func TestCheckRun_VersionsOfOneBookAreOneBook(t *testing.T) {
	members := []RunSeries{
		{ID: 1, N: 1, Books: []Facts{
			{ID: "a", Identity: "g1", Title: "A Death in the Family", ASIN: "B00ABC"},
			{ID: "a2", Identity: "g1", Title: "A Death in the Family", ASIN: "B00ABC"},
		}},
		one(2, 2, "Hush"), one(3, 3, "Under the Red Hood"),
	}
	if v := CheckRun("Batman", members[0], members[1:]); v.State != RunConfirmed {
		t.Fatalf("state %v (%s), want RunConfirmed", v.State, v.Reason)
	}
}

// A run is strict: one failing member holds the run, for every member that asks.
func TestCheckRun_OneBadMemberHoldsTheWholeRun(t *testing.T) {
	members := []RunSeries{one(1, 1, "A Death in the Family"), one(2, 2, "Hush"), one(3, 3, "Under the Red Hood"), one(4, 4, "Hush")}
	for i, self := range members {
		var sib []RunSeries
		for j, s := range members {
			if j != i {
				sib = append(sib, s)
			}
		}
		if v := CheckRun("Batman", self, sib); v.State != RunRejected {
			t.Errorf("member %d: state %v, want RunRejected", i, v.State)
		}
	}
}
