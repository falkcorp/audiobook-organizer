// file: internal/logging/redact_path_test.go
// version: 1.3.0
// guid: 232b593d-cfbd-4e4f-9898-8ff6d47e5768
// last-edited: 2026-10-22

package logging

import (
	"strings"
	"testing"
)

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
		{"doubled leading slash", "//invite/" + tok, "//invite/[REDACTED]"},
		{"doubled slash before the token", "/invite//" + tok, "/invite//[REDACTED]"},
		{"doubled slash inside an api prefix", "/api//v1/auth/invite/" + tok, "/api//v1/auth/invite/[REDACTED]"},
		{"upper case", "/INVITE/" + tok, "/INVITE/[REDACTED]"},
		{"mixed case", "/Invite/" + tok, "/Invite/[REDACTED]"},
		{"mixed case api prefix", "/API/V1/Auth/Invite/" + tok, "/API/V1/Auth/Invite/[REDACTED]"},
		{"doubled slash and case", "//Invite//" + tok + "/", "//Invite//[REDACTED]/"},
		{"bare invite with doubled slashes", "//invite//", "//invite//"},

		// An encoded slash is the slash it decodes to (the request line is logged
		// as RequestURI, which keeps %2F escaped).
		{"encoded slash before the token", "/api/v1/auth/invite%2F" + tok, "/api/v1/auth/invite/[REDACTED]"},
		{"encoded slash, lower-case hex", "/api/v1/auth/invite%2f" + tok, "/api/v1/auth/invite/[REDACTED]"},
		{"encoded slash on the spa page", "/invite%2F" + tok, "/invite/[REDACTED]"},
		{"encoded slash, query kept", "/invite%2F" + tok + "?a=1", "/invite/[REDACTED]?a=1"},
		{"encoded slash then resend", "/api/v1/users/invites%2F" + tok + "/resend", "/api/v1/users/invites/[REDACTED]/resend"},
		{"every slash encoded", "/api%2Fv1%2Fauth%2Finvite%2F" + tok, "/api/v1/auth/invite/[REDACTED]"},
		{"encoded slash and a malformed escape after it", "/invite%2F" + tok + "%zz", "/invite/[REDACTED]"},
		{"malformed escape before the encoded slash", "/%zz/../invite%2F" + tok, "/invite/[REDACTED]"},
		{"encoded slash on an unrelated path", "/books%2Finvite", "/books%2Finvite"},

		// A decoy segment must not hide the token: everything after the prefix is
		// redacted, the path is cleaned, and the decoding is repeated.
		{"decoy: malformed escape inside an encoded slash segment", "/invite%2F%zz/" + tok, "/invite/[REDACTED]/[REDACTED]"},
		{"decoy: empty encoded segment", "/invite/%2F/" + tok, "/invite/[REDACTED]/[REDACTED]"},
		{"decoy: encoded dot segment", "/invite%2F%2E%2F" + tok, "/invite/[REDACTED]"},
		{"decoy: dot segment", "/invite/./" + tok, "/invite/[REDACTED]/[REDACTED]"},
		{"decoy: double encoded slash", "/invite%252F" + tok, "/invite/[REDACTED]"},
		{"decoy: triple encoded slash", "/invite%25252F" + tok, "/invite/[REDACTED]"},
		{"decoy: dot-dot segment", "/invite/../" + tok, "/invite/[REDACTED]/[REDACTED]"},
		{"decoy: a path that is an invite route only once cleaned", "/books/../invite/" + tok, "/invite/[REDACTED]"},
		{"decoy: encoded dot-dot walks onto the route", "/x/%2e%2e/api/v1/auth/invite/" + tok, "/api/v1/auth/invite/[REDACTED]"},
		{"decoy: segment after the token", "/api/v1/auth/invite/" + tok + "/more/" + tok, "/api/v1/auth/invite/[REDACTED]/[REDACTED]/[REDACTED]"},
		{"decoy: trailing slash kept on a cleaned form", "/books/../invite/" + tok + "/", "/invite/[REDACTED]/"},
		{"decoy: encoded query delimiter stays in the token", "/invite%2F" + tok + "%3Fx=1", "/invite/[REDACTED]"},
		{"a decoded control character never reaches the output", "/%69nvite%2F" + tok + "/%0d%0a", "/invite/[REDACTED]/[REDACTED]"},

		// "resend" stays only as the last segment of the invites route.
		{"resend is kept only after an invite ID on the invites route", "/api/v1/users/invites/" + tok + "/resend", "/api/v1/users/invites/[REDACTED]/resend"},
		{"resend with a decoy before it", "/api/v1/users/invites/" + tok + "/x/resend", "/api/v1/users/invites/[REDACTED]/[REDACTED]/resend"},
		{"resend that is not last", "/api/v1/users/invites/" + tok + "/resend/" + tok, "/api/v1/users/invites/[REDACTED]/[REDACTED]/[REDACTED]"},
		{"resend on the lookup route is not kept", "/api/v1/auth/invite/" + tok + "/resend", "/api/v1/auth/invite/[REDACTED]/[REDACTED]"},
		{"resend as the token itself", "/api/v1/users/invites/resend", "/api/v1/users/invites/[REDACTED]"},
		{"already redacted stays redacted", "/invite/[REDACTED]", "/invite/[REDACTED]"},

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

// Redaction is idempotent: a path that was already redacted (a second layer of
// logging) comes through unchanged.
func TestRedactInvitePath_Idempotent(t *testing.T) {
	for _, in := range []string{
		"/invite%2F%zz/TOK", "/invite/./TOK", "/invite%252FTOK", "/api/v1/users/invites/TOK/resend?x=1",
		"/books/../invite/TOK/", "//Invite//TOK/",
	} {
		once := RedactInvitePath(in)
		if twice := RedactInvitePath(once); twice != once {
			t.Errorf("RedactInvitePath not idempotent for %q: %q then %q", in, once, twice)
		}
		if strings.Contains(once, "TOK") {
			t.Errorf("RedactInvitePath(%q) = %q still carries the token", in, once)
		}
	}
}
