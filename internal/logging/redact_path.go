// file: internal/logging/redact_path.go
// version: 1.2.0
// guid: 03986f4e-57ff-4667-bcfb-da4deb8f283b
// last-edited: 2026-10-22

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
// A segment matches after percent-decoding and without regard to case, and empty
// segments (a doubled slash) are skipped, because the request line, or a URL a
// browser reports, may spell the route as /%69nvite/TOK, /INVITE/TOK or
// //invite/TOK and a lenient front end or proxy can still send it to the same
// page. The token is the next non-empty segment after the prefix. An encoded
// slash (/api/v1/auth/invite%2FTOK) is treated as the slash it decodes to: when
// the raw segments do not match, the whole path is decoded before it is split.
func RedactInvitePath(pathWithQuery string) string {
	path, query, hasQuery := strings.Cut(pathWithQuery, "?")
	// An absolute path splits to ["", seg1, seg2, ...].
	if !strings.HasPrefix(path, "/") {
		return pathWithQuery
	}
	out, ok := redactInviteSegments(path, strings.Split(path, "/"), false)
	if !ok && strings.Contains(path, "%") {
		// An encoded slash moves a segment boundary (/api/v1/auth/invite%2FTOK is one
		// raw segment but, to a lenient front end, two). Decode the WHOLE path
		// before splitting. Decoding never fails here: an invalid escape is kept as
		// the literal text it is, so a malformed escape elsewhere in the path cannot
		// turn the redaction off.
		if dec := unescapeLenient(path); dec != path {
			if red, found := redactInviteSegments(dec, strings.Split(dec, "/"), true); found {
				out, ok = red, true
			}
		}
	}
	if !ok {
		return pathWithQuery
	}
	if hasQuery {
		out += "?" + query
	}
	return out
}

// redactInviteSegments replaces the invite token segment of segs (the split of
// path) and joins the result. reencode is set when segs came from a decoded path:
// the joined result is then escaped again, so the other segments are logged in
// their escaped form (a decoded control character never reaches the log).
func redactInviteSegments(path string, segs []string, reencode bool) (string, bool) {
	if len(segs) < 2 || segs[0] != "" {
		return path, false
	}
	idx := inviteTokenIndex(segs[1:])
	if idx < 0 {
		return path, false
	}
	if reencode {
		for i := range segs {
			segs[i] = url.PathEscape(segs[i])
		}
	}
	segs[idx+1] = InviteTokenRedacted
	return strings.Join(segs, "/"), true
}

// unescapeLenient is url.PathUnescape that never fails: a % that does not start a
// valid two-digit escape is kept as it is.
func unescapeLenient(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// inviteTokenIndex returns the index in segs of the invite token segment, or -1
// when segs is not an invite route or has no token segment.
func inviteTokenIndex(segs []string) int {
	// The non-empty segments, with their positions in segs.
	nonEmpty := make([]int, 0, len(segs))
	for i, s := range segs {
		if s != "" {
			nonEmpty = append(nonEmpty, i)
		}
	}
	for _, prefix := range inviteTokenPrefixes {
		n := len(prefix)
		if len(nonEmpty) <= n {
			continue
		}
		match := true
		for i, want := range prefix {
			if !strings.EqualFold(decodeSegment(segs[nonEmpty[i]]), want) {
				match = false
				break
			}
		}
		if match {
			return nonEmpty[n]
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
