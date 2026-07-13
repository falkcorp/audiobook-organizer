// file: internal/boilerplate/patterns_test.go
// version: 1.0.0
// guid: b2f4a6d8-1c30-4e75-9f82-6a3d5c7e0b41
// last-edited: 2026-07-12

package boilerplate

import "testing"

func TestIsBoilerplateTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Intro", true},
		{"intro", true},
		{"  Big Finish Ident  ", true}, // whitespace-normalized default
		{"BIG FINISH IDENT", true},
		{"Credits", true},
		{"This Is Audible presenting the show", true}, // prefix pattern
		{"Introduction to Algorithms", false},         // real book, prefix must not over-match
		{"Foundation", false},
		{"The Last Hunter", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsBoilerplateTitle(c.title); got != c.want {
			t.Errorf("IsBoilerplateTitle(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}
