// file: internal/personname/names_credit_test.go
// version: 1.0.0
// guid: 0e1c7d93-eb85-4401-be70-489de22dcca7
// last-edited: 2026-10-06

package personname

import "testing"

func TestNamesCredit(t *testing.T) {
	cases := []struct {
		name, credit string
		want         bool
	}{
		{"Brandon Sanderson", "Brandon Sanderson", true},
		{"brandon  sanderson.", "Brandon Sanderson", true},
		{"Terry Pratchett", "Neil Gaiman, Terry Pratchett", true},
		{"Stormlight Archive", "Brandon Sanderson", false},
		{"Honor Harrington", "David Weber", false},
		{"", "Brandon Sanderson", false},
		{"Brandon Sanderson", "", false},
	}
	for _, c := range cases {
		if got := NamesCredit(c.name, c.credit); got != c.want {
			t.Errorf("NamesCredit(%q, %q) = %v, want %v", c.name, c.credit, got, c.want)
		}
	}
}
