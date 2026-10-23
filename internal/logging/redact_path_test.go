// file: internal/logging/redact_path_test.go
// version: 1.5.0
// guid: 232b593d-cfbd-4e4f-9898-8ff6d47e5768
// last-edited: 2026-10-22

package logging

import (
	"strings"
	"testing"
	"time"
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
		// A dot segment AFTER the token must not walk the route away before it is
		// recognised (the cleaned form of /invite/TOK/.. is /invite).
		{"dot after token: encoded slash then dot-dot", "/invite%2F" + tok + "/..", "/invite/[REDACTED]/[REDACTED]"},
		{"dot after token: both slashes encoded", "/invite%2F" + tok + "%2F..", "/invite/[REDACTED]/[REDACTED]"},
		{"dot after token: encoded dot-dot", "/invite%2F" + tok + "%2F%2E%2E", "/invite/[REDACTED]/[REDACTED]"},
		{"dot after token: mixed case", "/Invite%2F" + tok + "/..", "/Invite/[REDACTED]/[REDACTED]"},
		{"dot after token: api prefix", "/api/v1/auth/invite%2F" + tok + "%2F..", "/api/v1/auth/invite/[REDACTED]/[REDACTED]"},
		{"dot after token: invites route, two dot-dots", "/api/v1/users/invites%2F" + tok + "%2F..%2F..", "/api/v1/users/invites/[REDACTED]/[REDACTED]/[REDACTED]"},
		{"dot after token: dot-dots then a real segment", "/invite%2F" + tok + "%2F..%2F..%2Fbooks", "/invite/[REDACTED]/[REDACTED]/[REDACTED]/[REDACTED]"},
		{"dot after token: double encoded", "/invite%252F" + tok + "/..", "/invite/[REDACTED]/[REDACTED]"},
		{"dot after token: double encoded both slashes", "/invite%252F" + tok + "%252F..", "/invite/[REDACTED]/[REDACTED]"},
		{"dot after token: query kept", "/invite%2F" + tok + "/..?a=b", "/invite/[REDACTED]/[REDACTED]?a=b"},
		// A walked path with an invite prefix that no anchored match can trust:
		// fail closed, query kept.
		{"fail closed: dot walk before and after an invite route", "/x/../invite/" + tok + "/..", "/[REDACTED]"},
		{"fail closed: encoded prefix mid path with dot walks", "/a%2Finvite%2F" + tok + "/../x/invite/y/..", "/[REDACTED]"},
		{"fail closed: query kept", "/x/../invite/" + tok + "/..?a=b", "/[REDACTED]?a=b"},
		// Nested deeper than the decode rounds, with the route name inside.
		{"fail closed: four rounds of encoding", "/invite%2525252F" + tok, "/[REDACTED]"},
		{"fail closed: four rounds, the route name itself escaped", "/%2569nvite%2525252F" + tok, "/[REDACTED]"},
		{"fail closed: the route name only shows in the decoding after the last round", "/%25252569nvite%2525252F" + tok, "/[REDACTED]"},
		// Not invite routes, so unchanged: no walk of a route, no deep nesting.
		{"unchanged: invite is not the first segment", "/books/invite/" + tok, "/books/invite/" + tok},
		{"unchanged: dot walk without an invite route", "/books/../x", "/books/../x"},
		{"unchanged: deep escaping without the route name", "/a/%2541%25252Fb", "/a/%2541%25252Fb"},
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
		"/invite%2FTOK/..", "/invite%2FTOK%2F..", "/invite%2FTOK%2F%2E%2E", "/Invite%2FTOK/..",
		"/api/v1/auth/invite%2FTOK%2F..", "/api/v1/users/invites%2FTOK%2F..%2F..", "/invite%2FTOK%2F..%2F..%2Fbooks",
		"/invite%252FTOK/..", "/invite%252FTOK%252F..", "/x/../invite/TOK/..", "/a%2Finvite%2FTOK/../x/invite/y/..",
		"/invite%2525252FTOK", "/x/../invite/TOK/..?a=b", "/%25252569nvite%2525252FTOK",
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

// A path over the cap is not read at all: it fails closed, query kept, whatever
// it holds (it cannot be shown not to carry a token), and one at the cap is read
// as usual.
func TestRedactInvitePath_OverCapFailsClosed(t *testing.T) {
	long := strings.Repeat("a", maxRedactPathBytes)
	for name, tc := range map[string]struct{ in, want string }{
		"invite route":              {"/invite/" + long, "/[REDACTED]"},
		"not an invite route":       {"/books/" + long, "/[REDACTED]"},
		"query kept":                {"/books/" + long + "?a=b&token=x", "/[REDACTED]?a=b&token=x"},
		"many segments":             {strings.Repeat("/a", maxRedactPathBytes/2+1), "/[REDACTED]"},
		"encoded slashes":           {"/invite" + strings.Repeat("%2F", maxRedactPathBytes/3+1) + "x", "/[REDACTED]"},
		"exactly at the cap, other": {"/" + long[:maxRedactPathBytes-1], "/" + long[:maxRedactPathBytes-1]},
		"exactly at the cap, route": {"/invite/" + long[:maxRedactPathBytes-8], "/invite/[REDACTED]"},
	} {
		if got := RedactInvitePath(tc.in); got != tc.want {
			t.Errorf("%s: RedactInvitePath(%d bytes) = %.60q, want %.60q", name, len(tc.in), got, tc.want)
		}
	}
}

// The work is linear in the path. A 100k-segment path and a 1 MB path are
// answered by the cap at once; the segment scan under it (anyInvitePrefix, the
// part that was quadratic) is run directly on 100k and 400k segments, where a
// quadratic scan would take minutes.
func TestRedactInvitePath_LinearTime(t *testing.T) {
	const budget = 2 * time.Second
	start := time.Now()
	for _, in := range []string{
		strings.Repeat("/a", 100_000),
		"/x/../invite/" + strings.Repeat("/a", 100_000),
		"/" + strings.Repeat("a", 1<<20),
		"/invite" + strings.Repeat("%2F..", 200_000),
	} {
		if got := RedactInvitePath(in); got != "/[REDACTED]" {
			t.Fatalf("%d byte path not failed closed: %.40q", len(in), got)
		}
	}
	if d := time.Since(start); d > budget {
		t.Fatalf("over-cap paths took %v, want the cap to answer at once", d)
	}
	for _, n := range []int{100_000, 400_000} {
		form := strings.Repeat("/a", n) + "/invite/TOK"
		start := time.Now()
		if !anyInvitePrefix([]string{form}) {
			t.Fatalf("n=%d: the invite prefix at the end was not found", n)
		}
		if anyInvitePrefix([]string{strings.Repeat("/a", n)}) {
			t.Fatalf("n=%d: found an invite prefix in a path without one", n)
		}
		if d := time.Since(start); d > budget {
			t.Fatalf("anyInvitePrefix over %d segments took %v: not linear", n, d)
		}
	}
}

func BenchmarkRedactInvitePath_AtTheCap(b *testing.B) {
	in := "/x/../invite" + strings.Repeat("/a", maxRedactPathBytes/2-8)
	for range b.N {
		RedactInvitePath(in)
	}
}
