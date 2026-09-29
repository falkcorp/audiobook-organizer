// file: internal/metadata/unsearchable_title_test.go
// version: 1.3.0
// guid: 6da04422-761b-4fbf-a25d-fd032c6f3457
// last-edited: 2026-09-28

package metadata

import "testing"

// IsUnsearchableTitle is unconditional, so it refuses only what no book is
// titled: placeholders, chapter numbers, chapter fragments and labelled
// digit positions. Headings in words or roman numerals and front-matter
// names are real titles too ("Act One", "Epilogue") and pass here.
func TestIsUnsearchableTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"", true},
		{"   ", true},
		{"Unknown Title", true},
		{"unknown author", true},
		{"read by narrator", true},
		{"Chapter 3", true},
		{"Part 12", true},
		{"03", true},
		{"98", true},
		{"Disc 2", true},
		{"06 Chapter 6", true},
		{"01 - Track 1", true},
		{"Track 29", true},
		{"Book 1", true},
		{"Vol. 2", true},
		{"Volume 2", true},
		{"Episode 3 of 12", true},
		// Real titles as well as headings: never refused unconditionally.
		{"Act One", false},
		{"Book Two", false},
		{"Book X", false},
		{"Chapter One", false},
		{"Part II", false},
		{"Forward", false},
		{"Epilogue", false},
		{"Dedication", false},
		{"Interlude", false},
		{"Introduction", false},
		{"Credits", false},
		{"Contents", false},
		{"Prologue", false},
		{"2001", false},
		{"Book of the New Sun", false},
		{"The Prologue of Doom", false},
		{"Volume Control", false},
		{"Chapterhouse: Dune", false},
		{"Epic", false},
		{"Chill", false},
		{"Act of War", false},
		{"Eldest", false},
		{"1984", false},
		{"Metro 2034", false},
		{"Part of Your World", false},
		{"Marvel's Planet Hulk", false},
	}
	for _, tc := range cases {
		if got := IsUnsearchableTitle(tc.title); got != tc.want {
			t.Errorf("IsUnsearchableTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// IsSectionHeadingTitle names the headings that need the book's files to
// corroborate them; its roman class is valid numerals only, so words made of
// numeral letters ("Ill", "Civil") are not headings.
func TestIsSectionHeadingTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Chapter One", true},
		{"Chapter Two", true},
		{"Part Two", true},
		{"Part II", true},
		{"Book Two", true},
		{"Book X", true},
		{"Volume II", true},
		{"Episode IV", true},
		{"Act One", true},
		{"Episode Final", true},
		{"Vol. Last", true},
		{"Prologue", true},
		{"Epilogue", true},
		{"Introduction", true},
		{"Intro", true},
		{"Forward", true},
		{"Dedication", true},
		{"Interlude", true},
		{"Credits", true},
		{"Contents", true},
		{"Copyright", true},
		{"Afterword", true},
		{"  opening   credits ", true},
		{"Book Ill", false},
		{"CD Civil", false},
		{"Book of the Dead", false},
		{"Part of the Problem", false},
		{"Chapter House", false},
		{"Prologue to Murder", false},
		{"The Prologue", false},
		{"Act of Will", false},
		{"Part the Third", false},
		{"Epic", false},
		{"Chill", false},
		{"1984", false},
		{"Chapter 3", false}, // unconditional: IsUnsearchableTitle
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSectionHeadingTitle(tc.title); got != tc.want {
			t.Errorf("IsSectionHeadingTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
		if got := MayBeUnsearchableTitle(tc.title); tc.want && !got {
			t.Errorf("MayBeUnsearchableTitle(%q) = false for a heading", tc.title)
		}
	}
}
