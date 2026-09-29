// file: internal/util/title_sort_test.go
// version: 1.0.0
// guid: 6a3d9e52-1c8f-4b70-a4e6-3f2b8d0c5e19
// last-edited: 2026-09-28

package util

import "testing"

func TestTitleSortKey_NumberedBooksSortTogether(t *testing.T) {
	want := "corinthians 1"
	for _, in := range []string{"I Corinthians", "1 Corinthians", "First Corinthians", "l Corinthians",
		"1st Corinthians", "i corinthians", "I. Corinthians"} {
		if got := TitleSortKey(in); got != want {
			t.Errorf("TitleSortKey(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"II Corinthians", "2 Corinthians", "Second Corinthians", "ll Corinthians", "lI Corinthians"} {
		if got := TitleSortKey(in); got != "corinthians 2" {
			t.Errorf("TitleSortKey(%q) = %q, want corinthians 2", in, got)
		}
	}
}

func TestTitleSortKey_Table(t *testing.T) {
	cases := []struct{ in, want string }{
		{"I Corinthians 13", "corinthians 1 13"},
		{"II Kings - Chapter 3", "kings 2 - chapter 3"},
		{"III John", "john 3"},
		{"First Samuel chapter 4", "samuel 1 chapter 4"},
		{"IV Maccabees", "maccabees 4"},
		// ---- untouched: plain NormalizeTitle ----
		{"The Odyssey", "the odyssey"},
		{"I, Robot", "i, robot"},
		{"V for Vendetta", "v for vendetta"},
		{"l'Étranger", "l'étranger"},
		{"First Kings of England", "first kings of england"},
		{"I John Smith", "i john smith"},
		{"1984", "1984"},
		{"  Dune  ", "dune"},
		{"Corinthians", "corinthians"},
		{"Fifth Corinthians", "fifth corinthians"},
		{"lll", "lll"},
	}
	for _, c := range cases {
		if got := TitleSortKey(c.in); got != c.want {
			t.Errorf("TitleSortKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeLetterLOrdinal(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"l Corinthians", "1 Corinthians", true},
		{"ll Corinthians 4", "2 Corinthians 4", true},
		{"lI Timothy", "2 Timothy", true},
		{"lll John", "3 John", true},
		{"l. Peter", "1 Peter", true},
		{"I Corinthians", "I Corinthians", false}, // correct roman spelling
		{"1 Corinthians", "1 Corinthians", false},
		{"l'Étranger", "l'Étranger", false},
		{"l Am Legend", "l Am Legend", false},      // not a numbered book
		{"Ill Corinthians", "3 Corinthians", true}, // "III" with two l glyphs
	}
	for _, c := range cases {
		got, ok := NormalizeLetterLOrdinal(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeLetterLOrdinal(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
