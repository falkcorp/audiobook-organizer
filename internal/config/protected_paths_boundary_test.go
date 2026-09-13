// file: internal/config/protected_paths_boundary_test.go
// version: 1.0.0
// guid: 9b3f2a61-d4c8-47e0-a519-6e2d8c0b7f34
// last-edited: 2026-09-12

package config

import "testing"

// pathCoveredByProtected must match on a separator boundary: a protected
// "/lib" does not cover the sibling "/lib2/...". Windows-style entries behave
// the same after the backslash normalisation.
func TestPathCoveredByProtected_SeparatorBoundary(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		protected []string
		want      bool
	}{
		{"root itself", "/lib", []string{"/lib"}, true},
		{"under root", "/lib/x/a.itl", []string{"/lib"}, true},
		{"trailing sep root", "/lib/x", []string{"/lib/"}, true},
		{"sibling", "/lib2/x/a.itl", []string{"/lib"}, false},
		{"sibling, second entry matches", "/lib2/x", []string{"/lib", "/lib2"}, true},
		{"windows under", `C:\lib\x\a.itl`, []string{`C:\lib`}, true},
		{"windows sibling", `C:\lib2\x\a.itl`, []string{`C:\lib`}, false},
		{"empty entry ignored", "/lib2/x", []string{""}, false},
		{"empty path", "", []string{"/lib"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathCoveredByProtected(tc.path, tc.protected); got != tc.want {
				t.Fatalf("pathCoveredByProtected(%q, %q) = %v, want %v", tc.path, tc.protected, got, tc.want)
			}
		})
	}
}
