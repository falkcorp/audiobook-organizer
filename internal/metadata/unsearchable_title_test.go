// file: internal/metadata/unsearchable_title_test.go
// version: 1.1.0
// guid: 6da04422-761b-4fbf-a25d-fd032c6f3457
// last-edited: 2026-09-28

package metadata

import "testing"

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
		{"03", true},
		{"98", true},
		{"Disc 2", true},
		{"06 Chapter 6", true},
		{"01 - Track 1", true},
		{"Chapter One", true},
		{"Part One", true},
		{"Part II", true},
		{"Book 1", true},
		{"Vol. 2", true},
		{"Volume 2", true},
		{"Prologue", true},
		{"Introduction", true},
		{"Opening Credits", true},
		{"  opening   credits ", true},
		{"Episode Three", true},
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
