// file: internal/logging/redact_path.go
// version: 1.4.0
// guid: 03986f4e-57ff-4667-bcfb-da4deb8f283b
// last-edited: 2026-10-22

package logging

import (
	"net/url"
	pathpkg "path"
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

// maxDecodeRounds bounds how many times RedactInvitePath percent-decodes a path
// looking for an invite route (%252F is a slash after two rounds).
const maxDecodeRounds = 3

// resendSegment is the one fixed word that may follow an invite ID on the resend
// route and stays visible.
const resendSegment = "resend"

// RedactInvitePath returns pathWithQuery with everything after an invite route
// prefix replaced by InviteTokenRedacted, one per non-empty path segment. The
// prefix, empty segments (doubled or trailing slashes) and any query string are
// preserved, a trailing "resend" is kept when it is the last segment of the
// invites route (POST /api/v1/users/invites/:id/resend), and every path that is
// not an invite route (including the invite list endpoint /api/v1/users/invites,
// which has no token) is returned unchanged.
//
// Everything after the prefix is redacted, not just the next segment, because a
// decoy segment must not hide the token: a lenient front end or proxy can still
// send /invite/./TOK, /invite%2F%zz/TOK or /invite/%2F/TOK to the invite page,
// and the real token then sits behind the decoy.
//
// A segment of the prefix matches after percent-decoding and without regard to
// case, and empty segments are skipped, because the request line, or a URL a
// browser reports, may spell the route as /%69nvite/TOK, /INVITE/TOK or
// //invite/TOK. The path is read in several forms, each matched anchored at its
// start, and the first match decides: the raw path as written, then cleaned
// (path.Clean resolves ".", ".." and "//"); then each of up to maxDecodeRounds
// percent-decodings of the whole path, cleaned first and as decoded second (an
// encoded slash such as /api/v1/auth/invite%2FTOK, or a double encoding such as
// %252F, moves a segment boundary; the uncleaned form matters because a dot
// segment AFTER the token, /invite%2FTOK/.., would clean the route away).
// Decoding never fails: an invalid escape is kept as the literal text it is, so
// a malformed escape cannot turn redaction off. A path that is an invite route
// only once decoded or cleaned is logged in that form, re-escaped segment by
// segment, so what was decoded never reaches the log as a raw control character.
//
// Two rules fail closed, logging "/[REDACTED]" and keeping the query, when no
// form matched: (1) some form has a "." or ".." segment and some form has an
// invite prefix followed by a segment at any position, because a walked path
// means no positional match can be trusted (/x/../invite/TOK/..); (2) the
// escaping is nested deeper than maxDecodeRounds (the last form would still
// decode and has "invite" in it or in its next decoding). Neither fires for
// /books/invite/TOK or /books/../x, which have no invite route at the start of
// any form and no dot walk over one.
func RedactInvitePath(pathWithQuery string) string {
	path, query, hasQuery := strings.Cut(pathWithQuery, "?")
	// An absolute path splits to ["", seg1, seg2, ...].
	if !strings.HasPrefix(path, "/") {
		return pathWithQuery
	}
	// Every form: the raw path and up to maxDecodeRounds decodings of it.
	forms := []string{path}
	for cur := path; len(forms) <= maxDecodeRounds; {
		dec := unescapeLenient(cur)
		if dec == cur {
			break
		}
		cur = dec
		forms = append(forms, cur)
	}
	// (a) Anchored match, each form both as written and cleaned.
	var out string
	ok := false
	for i, f := range forms {
		cands := []string{cleanKeepingTrailingSlash(f), f}
		if i == 0 {
			cands = []string{f, cleanKeepingTrailingSlash(f)}
		}
		for _, cand := range cands {
			if out, ok = redactInviteSegments(cand, i > 0); ok {
				break
			}
		}
		if ok {
			break
		}
	}
	// (b) Fail closed: a form with a dot segment walks the path, so an invite
	// prefix anywhere in any form means no positional redaction can be trusted.
	if !ok && anyDotSegment(forms) && anyInvitePrefix(forms) {
		out, ok = "/"+InviteTokenRedacted, true
	}
	// Also fail closed on nesting deeper than maxDecodeRounds: if the last form
	// would still decode and the text "invite" shows up in it or in its next
	// decoding, a further layer of escaping may hide a route. Not decoded until
	// stable: each round of nested escapes shrinks the text by only two bytes, so
	// looping to a fixed point would be quadratic in the length of the path.
	if !ok && len(forms) > maxDecodeRounds && hidesInviteBeyondRounds(forms[len(forms)-1]) {
		out, ok = "/"+InviteTokenRedacted, true
	}
	if !ok {
		return pathWithQuery
	}
	if hasQuery {
		out += "?" + query
	}
	return out
}

// cleanKeepingTrailingSlash is path.Clean that keeps a trailing slash.
func cleanKeepingTrailingSlash(p string) string {
	c := pathpkg.Clean(p)
	if strings.HasSuffix(p, "/") && !strings.HasSuffix(c, "/") {
		c += "/"
	}
	return c
}

// redactInviteSegments splits path, and when it is an invite route replaces every
// non-empty segment after the prefix with InviteTokenRedacted (keeping a trailing
// "resend" on the invites route) and joins the result. reencode is set when path
// was decoded: the other segments are then escaped again.
func redactInviteSegments(path string, reencode bool) (string, bool) {
	segs := strings.Split(path, "/")
	if len(segs) < 2 || segs[0] != "" {
		return path, false
	}
	idx, invitesRoute := inviteTokenIndex(segs[1:])
	if idx < 0 {
		return path, false
	}
	idx++ // position in segs
	last := -1
	for i := len(segs) - 1; i >= idx; i-- {
		if segs[i] != "" {
			last = i
			break
		}
	}
	for i := range segs {
		switch {
		case i >= idx && segs[i] != "":
			if invitesRoute && i == last && i > idx && segs[i] == resendSegment {
				continue
			}
			segs[i] = InviteTokenRedacted
		case reencode:
			segs[i] = url.PathEscape(segs[i])
		}
	}
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
// when segs is not an invite route or has no token segment, and whether the
// route is the invites one (the one with a resend action).
func inviteTokenIndex(segs []string) (idx int, invitesRoute bool) {
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
			return nonEmpty[n], prefix[len(prefix)-1] == "invites"
		}
	}
	return -1, false
}

func decodeSegment(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	return s
}
func anyDotSegment(forms []string) bool {
	for _, f := range forms {
		for _, s := range strings.Split(f, "/") {
			if s == "." || s == ".." {
				return true
			}
		}
	}
	return false
}

func anyInvitePrefix(forms []string) bool {
	for _, f := range forms {
		segs := strings.Split(f, "/")
		for i := range segs {
			if idx, _ := inviteTokenIndex(segs[i:]); idx >= 0 {
				return true
			}
		}
	}
	return false
}

// hidesInviteBeyondRounds reports whether last, the final form RedactInvitePath
// read, would still decode and has "invite" (any case) in it or in its next
// decoding.
func hidesInviteBeyondRounds(last string) bool {
	next := unescapeLenient(last)
	if next == last {
		return false
	}
	return strings.Contains(strings.ToLower(last), "invite") || strings.Contains(strings.ToLower(next), "invite")
}
