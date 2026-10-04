// file: internal/operations/registry/reporter_progress_shape_internal_test.go
// version: 1.0.0
// guid: 1c83eead-be8d-4ff8-a4e4-b82ead2a022e
// last-edited: 2026-10-04

package registry

import "testing"

func TestProgressShape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"12", "#"},
		{"Books 3/76994", "Books #/#"},
		{"1.5 GB", "#.# GB"},
		{"x9y99z", "x#y#z"},
		{"Books 3/76994 (scanned 4, skipped 12)", "Books #/# (scanned #, skipped #)"},
	}
	for _, c := range cases {
		if got := progressShape(c.in); got != c.want {
			t.Errorf("progressShape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
