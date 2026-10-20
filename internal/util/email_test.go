// file: internal/util/email_test.go
// version: 1.0.0
// guid: bb606c60-2e28-48c8-900c-950f0c43fb1e
// last-edited: 2026-10-20

package util

import "testing"

func TestNormalizeEmail(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  Guest@Example.COM ", "guest@example.com"},
		{"admin@examplK.com", "admin@examplk.com"}, // Kelvin sign
		{"Kate@example.com", "kate@example.com"},   // Kelvin sign, leading
		{"roſe@example.com", "rose@example.com"},   // long s
		{"ａｄｍｉｎ@example.com", "admin@example.com"}, // fullwidth
		{" x@example.com　", "x@example.com"},       // other spaces
		{"", ""},
		{"   ", ""},
	} {
		if got := NormalizeEmail(tc.in); got != tc.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := NormalizeEmail(NormalizeEmail(tc.in)); got != NormalizeEmail(tc.in) {
			t.Errorf("NormalizeEmail is not idempotent on %q: %q", tc.in, got)
		}
	}
}

func TestSameEmail(t *testing.T) {
	if !SameEmail("Kate@Example.com", "Kate@example.com") {
		t.Error("the Kelvin-sign spelling must be the same address")
	}
	if SameEmail("", "") || SameEmail("a@example.com", "") {
		t.Error("an empty address matches nothing")
	}
	if SameEmail("a@example.com", "b@example.com") {
		t.Error("different addresses")
	}
}
