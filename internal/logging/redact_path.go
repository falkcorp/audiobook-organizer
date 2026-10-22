// file: internal/logging/redact_path.go
// version: 1.0.2
// guid: 03986f4e-57ff-4667-bcfb-da4deb8f283b
// last-edited: 2026-10-21

package logging

import (
	"net/url"
	"strings"
)

// InviteTokenRedacted replaces an invite token path segment in logs and spans.
const InviteTokenRedacted = "[REDACTED]"

// inviteTokenPrefixes are the leading path segments (after the root) that are
// immediately followed by an invite token. An invite token is a bearer secret
// (the public lookup confirms a live one and names its username), so it must
// not reach the access log, the panic-recovery dump, error logs or a span.
//
// The un-versioned /api/... forms are not registered routes: the router
// 301-redirects them to /api/v1/..., but the request line still carries the
// token and is logged before the redirect is issued.
//
//	GET    /invite/:token                          SPA page (NoRoute fallback)
//	GET    /api/v1/auth/invite/:token              public lookup
//	DELETE /api/v1/users/invites/:id               revoke
//	POST   /api/v1/users/invites/:id/resend        resend
//
// Revoke and resend now take the invite's non-secret ID, not its token. The
// segment is still redacted: an older client (or a hand-typed request) can
// still put a token there, and it is logged before the route answers 404.
var inviteTokenPrefixes = [][]string{
	{"invite"},
	{"api", "auth", "invite"},
	{"api", "v1", "auth", "invite"},
	{"api", "users", "invites"},
	{"api", "v1", "users", "invites"},
}

// RedactInvitePath returns pathWithQuery with the invite token segment replaced
// by InviteTokenRedacted. It matches whole path segments, so only the token is
// replaced: the rest of the path, a trailing slash and any query string are
// preserved, and every other path (including the invite list endpoint
// /api/v1/users/invites, which has no token) is returned unchanged.
//
// Segments are compared after percent-decoding, because the request line (as
// opposed to the decoded URL.Path gin routes on) may spell "invite" with
// escapes and still reach the same route.
func RedactInvitePath(pathWithQuery string) string {
	path, query, hasQuery := strings.Cut(pathWithQuery, "?")
	segs := strings.Split(path, "/")
	// An absolute path splits to ["", seg1, seg2, ...].
	if len(segs) < 2 || segs[0] != "" {
		return pathWithQuery
	}
	idx := inviteTokenIndex(segs[1:])
	if idx < 0 {
		return pathWithQuery
	}
	segs[idx+1] = InviteTokenRedacted
	out := strings.Join(segs, "/")
	if hasQuery {
		out += "?" + query
	}
	return out
}

// inviteTokenIndex returns the index in segs of the invite token segment, or -1
// when segs is not an invite route or the token segment is empty.
func inviteTokenIndex(segs []string) int {
	for _, prefix := range inviteTokenPrefixes {
		n := len(prefix)
		if len(segs) <= n || segs[n] == "" {
			continue
		}
		match := true
		for i, want := range prefix {
			if decodeSegment(segs[i]) != want {
				match = false
				break
			}
		}
		if match {
			return n
		}
	}
	return -1
}

func decodeSegment(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	return s
}
