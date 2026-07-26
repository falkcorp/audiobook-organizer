// file: internal/server/handlers/oauth_login_sanitize_test.go
// version: 1.0.0
// guid: 7b2e0c94-5a18-4d63-8f07-1a6c5b2e9d34

package handlers

import "testing"

func TestSanitizeReturn(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/library", "/library"},
		{"/", "/"},
		{"", ""},
		{"//evil.com", ""},        // protocol-relative
		{"/\\evil.com", ""},       // backslash → browser normalizes to //
		{"/path\\x", ""},          // any backslash rejected
		{"https://evil.com", ""},  // absolute URL
		{"evil.com", ""},          // no leading slash
	}
	for _, c := range cases {
		if got := sanitizeReturn(c.in); got != c.want {
			t.Errorf("sanitizeReturn(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}
