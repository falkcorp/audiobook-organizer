// file: internal/server/handlers/abs/ignore_prefix_test.go
// version: 1.0.0
// guid: 5e9b2d47-8a1c-4f36-b7d0-2c4e6a9f1b83
// last-edited: 2026-09-28

package abs

import (
	"sort"
	"testing"
)

func TestIgnorePrefix_NumberedBooksAndArticles(t *testing.T) {
	cases := []struct{ in, want string }{
		{"I Corinthians", "Corinthians 1"},
		{"1 Corinthians", "Corinthians 1"},
		{"First Corinthians", "Corinthians 1"},
		{"l Corinthians", "Corinthians 1"},
		{"II Kings 3", "Kings 2 3"},
		{"The Odyssey", "Odyssey, The"},
		{"A Game of Thrones", "Game of Thrones, A"},
		{"I, Robot", "I, Robot"},
		{"l'Étranger", "l'Étranger"},
		{"V for Vendetta", "V for Vendetta"},
	}
	for _, c := range cases {
		if got := ignorePrefix(c.in); got != c.want {
			t.Errorf("ignorePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A client sorting by titleIgnorePrefix files the spellings together,
// between "Colossians" and "Daniel", not under I, 1, F and l.
func TestIgnorePrefix_SortsNumberedBooksUnderTheirName(t *testing.T) {
	titles := []string{"l Corinthians", "Daniel", "II Corinthians", "Colossians", "Isaiah"}
	sort.SliceStable(titles, func(i, j int) bool { return ignorePrefix(titles[i]) < ignorePrefix(titles[j]) })
	want := []string{"Colossians", "l Corinthians", "II Corinthians", "Daniel", "Isaiah"}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("order = %v, want %v", titles, want)
		}
	}
}
