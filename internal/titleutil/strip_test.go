// file: internal/titleutil/strip_test.go
// version: 1.1.0
// guid: 8f3b2c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d

package titleutil_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/titleutil"
)

func TestStripChapterPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Fraction form
		{"(76/85) Tarkin: Star Wars (Unabridged)", "Tarkin: Star Wars (Unabridged)"},
		{"(76 of 85) Tarkin: Star Wars", "Tarkin: Star Wars"},
		{"(1-2) Some Title", "Some Title"},
		{"(1_2) Some Title", "Some Title"},

		// Chapter prefix
		{"Chapter 03 - The Storm", "The Storm"},
		{"Chapter 03: The Storm", "The Storm"},
		{"chapter 3 The Storm", "The Storm"},
		{"CHAPTER 12 - Finale", "Finale"},

		// Track prefix
		{"Track 12 - Foo", "Foo"},
		{"track 1: Bar", "Bar"},

		// Part prefix
		{"Part 4 - Bar", "Bar"},
		{"Part 4 of 8 - Bar", "Bar"},
		{"PART 2: Intro", "Intro"},

		// Bare number with delimiter
		{"03 - Foo", "Foo"},
		{"002. Title Here", "Title Here"},
		{"1: Something", "Something"},

		// Clean titles — must be untouched
		{"The Hobbit", "The Hobbit"},
		{"Tarkin: Star Wars (Unabridged)", "Tarkin: Star Wars (Unabridged)"},
		{"A Tale of Two Cities", "A Tale of Two Cities"},

		// Edge cases
		{"", ""},
		{"   ", ""},
		{"  (1/2) Padded  ", "Padded"},
	}

	for _, tc := range cases {
		got := titleutil.StripChapterPrefix(tc.in)
		if got != tc.want {
			t.Errorf("StripChapterPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripChapterSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// The reported case: trailing "N/M" after an en-dash / hyphen.
		{"At All Costs – 11/23", "At All Costs"},
		{"At All Costs - 13/23", "At All Costs"},
		{"At All Costs – 23/23", "At All Costs"},
		{"At All Costs : 1/23", "At All Costs"},

		// Trailing parenthesised forms.
		{"At All Costs (11/23)", "At All Costs"},
		{"At All Costs (11 of 23)", "At All Costs"},
		{"At All Costs (1-2)", "At All Costs"},

		// Trailing "N of M" / bare "N/M" without a delimiter.
		{"At All Costs 11 of 23", "At All Costs"},
		{"At All Costs 11/23", "At All Costs"},

		// Bare keyword+number part markers WITHOUT an N/M fraction — the iTunes
		// chapter-file convention that previously fragmented one book into one
		// book per chapter (e.g. "Aces Abroad - Part 19").
		{"Aces Abroad - Part 19", "Aces Abroad"},
		{"Aces Abroad - Part 01", "Aces Abroad"},
		{"Aces Abroad – Part 7", "Aces Abroad"},
		{"Aces Abroad-Part19", "Aces Abroad"},
		{"The Storm - Chapter 3", "The Storm"},
		{"The Storm : Chapter 12", "The Storm"},
		{"Foo - CD 2", "Foo"},
		{"Foo - Disc 1", "Foo"},
		{"Foo - Disk 4", "Foo"},
		{"Foo - Pt 4", "Foo"},
		{"Foo - Track 8", "Foo"},
		{"Foo - Section 5", "Foo"},
		{"Foo - Episode 2", "Foo"},

		// Clean titles — must be untouched.
		{"The Hobbit", "The Hobbit"},
		{"Tarkin: Star Wars (Unabridged)", "Tarkin: Star Wars (Unabridged)"},
		{"A Tale of Two Cities", "A Tale of Two Cities"},
		// A trailing year or lone number is NOT a part marker — keep it.
		{"1984", "1984"},
		{"Catch 22", "Catch 22"},
		// "Book N" is a SERIES VOLUME, not a chapter — must survive (78 legit
		// titles on prod, e.g. "...Full Metal Superhero, Book 8").
		{"Arsenal Reloaded Full Metal Superhero, Book 8", "Arsenal Reloaded Full Metal Superhero, Book 8"},
		{"Renegade Star Publisher's Pack 6, Book 11-12", "Renegade Star Publisher's Pack 6, Book 11-12"},
		{"Traction: Eternal Dominion, Book 2", "Traction: Eternal Dominion, Book 2"},
		// "Volume N" is also series-level — keep it.
		{"Saga Volume 2", "Saga Volume 2"},
		// A keyword mid-word must not be stripped.
		{"Apartment 16", "Apartment 16"},

		// Edge cases.
		{"", ""},
		{"   ", ""},
		{"  At All Costs – 11/23  ", "At All Costs"},
		// Idempotent.
		{"At All Costs", "At All Costs"},
	}

	for _, tc := range cases {
		got := titleutil.StripChapterSuffix(tc.in)
		if got != tc.want {
			t.Errorf("StripChapterSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
