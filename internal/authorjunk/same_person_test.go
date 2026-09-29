// file: internal/authorjunk/same_person_test.go
// version: 1.0.0
// guid: bef40212-5d83-4e21-9ccb-71aa48d11cec
// last-edited: 2026-09-29

package authorjunk

import "testing"

func TestSamePersonName(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Anne Roiphe", "Anne Roiphe", true},
		{"anne  roiphe", "Anne Roiphe", true},
		{"J.R.R. Tolkien", "J. R. R. Tolkien", true},
		{"José Saramago", "José Saramago", true}, // precomposed vs decomposed
		{"Emma Törzs", "Emma Torzs", true},
		{"Roiphe, Anne", "Anne Roiphe", true},
		{"Anne Roiphe", "Roiphe, Anne", true},
		{"Le Guin, Ursula K.", "Ursula K. Le Guin", true},
		{"Anne Roiphe", "Anne Rice", false},
		{"Eldest", "Christopher Paolini", false},
		{"Smith, John, Jr.", "John Smith", false}, // two commas: not a single inversion
		{"", "", false},
		{"", "Anne Roiphe", false},
		{"...", "---", false},
	}
	for _, c := range cases {
		if got := SamePersonName(c.a, c.b); got != c.want {
			t.Errorf("SamePersonName(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
