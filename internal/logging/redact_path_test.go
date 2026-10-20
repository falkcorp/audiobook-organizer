// file: internal/logging/redact_path_test.go
// version: 1.0.0
// guid: 232b593d-cfbd-4e4f-9898-8ff6d47e5768
// last-edited: 2026-10-19

package logging

import "testing"

func TestRedactInvitePath(t *testing.T) {
	const tok = "tok-ExampleInviteToken_123"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"spa page", "/invite/" + tok, "/invite/[REDACTED]"},
		{"spa page trailing slash", "/invite/" + tok + "/", "/invite/[REDACTED]/"},
		{"spa page query", "/invite/" + tok + "?ref=mail", "/invite/[REDACTED]?ref=mail"},
		{"public lookup", "/api/v1/auth/invite/" + tok, "/api/v1/auth/invite/[REDACTED]"},
		{"public lookup trailing slash", "/api/v1/auth/invite/" + tok + "/", "/api/v1/auth/invite/[REDACTED]/"},
		{"public lookup query", "/api/v1/auth/invite/" + tok + "?a=b&c=d", "/api/v1/auth/invite/[REDACTED]?a=b&c=d"},
		{"legacy unversioned lookup", "/api/auth/invite/" + tok, "/api/auth/invite/[REDACTED]"},
		{"revoke", "/api/v1/users/invites/" + tok, "/api/v1/users/invites/[REDACTED]"},
		{"revoke trailing slash", "/api/v1/users/invites/" + tok + "/", "/api/v1/users/invites/[REDACTED]/"},
		{"resend", "/api/v1/users/invites/" + tok + "/resend", "/api/v1/users/invites/[REDACTED]/resend"},
		{"resend trailing slash and query", "/api/v1/users/invites/" + tok + "/resend/?x=1", "/api/v1/users/invites/[REDACTED]/resend/?x=1"},
		{"legacy unversioned resend", "/api/users/invites/" + tok + "/resend", "/api/users/invites/[REDACTED]/resend"},
		{"escaped prefix segment", "/%69nvite/" + tok, "/%69nvite/[REDACTED]"},

		// Unrelated paths and the token-less forms stay untouched.
		{"invite list", "/api/v1/users/invites", "/api/v1/users/invites"},
		{"invite list trailing slash", "/api/v1/users/invites/", "/api/v1/users/invites/"},
		{"invite list query", "/api/v1/users/invites?limit=5", "/api/v1/users/invites?limit=5"},
		{"create invite", "/api/v1/users/invite", "/api/v1/users/invite"},
		{"accept invite", "/api/v1/auth/accept-invite", "/api/v1/auth/accept-invite"},
		{"bare invite page", "/invite", "/invite"},
		{"bare invite page trailing slash", "/invite/", "/invite/"},
		{"invite substring not a segment", "/invited/" + tok, "/invited/" + tok},
		{"invite deeper in path", "/books/invite/" + tok, "/books/invite/" + tok},
		{"other users route", "/api/v1/users/42/deactivate", "/api/v1/users/42/deactivate"},
		{"token query on other path", "/api/v1/books?token=abc", "/api/v1/books?token=abc"},
		{"root", "/", "/"},
		{"empty", "", ""},
		{"relative", "invite/" + tok, "invite/" + tok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactInvitePath(tc.in); got != tc.want {
				t.Fatalf("RedactInvitePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
