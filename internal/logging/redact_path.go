// file: internal/logging/redact_path.go
// version: 1.6.0
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

// maxRedactPathBytes bounds the path RedactInvitePath examines. The path comes
// from an unauthenticated request line, and every form it builds (the raw path
// and its decodings, which are never longer) is split and scanned; the scans are
// linear, and this keeps the constant small too. A longer path is not a real
// URL of this service: it is logged as "/[REDACTED]" (query kept) instead of
// being read, since it cannot be shown not to carry a token.
const maxRedactPathBytes = 8 << 10

// resendSegment is the one fixed word that may follow an invite ID on the resend
// route and stays visible.
const resendSegment = "resend"

// RedactInvitePath returns path, a URL path with no query string, with everything
// after an invite route prefix replaced by InviteTokenRedacted, one per non-empty
// path segment. The prefix and empty segments (doubled or trailing slashes) are
// preserved, a trailing "resend" is kept when it is the last segment of the
// invites route (POST /api/v1/users/invites/:id/resend), and every path that is
// not an invite route (including the invite list endpoint /api/v1/users/invites,
// which has no token) is returned unchanged.
//
// A "?" in path is path text, never a query delimiter. That matters for a path
// that has already been decoded once, such as net/http's URL.Path: for the
// request line /invite/%3FTOK it is "/invite/?TOK", and cutting it at the "?"
// would log TOK verbatim. A caller holding a string with a real query appended
// (a RequestURI) uses RedactInvitePathWithQuery.
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
// Cost: everything here is linear in the path. A path with no "%" and no
// "invite" in it (any case) cannot be an invite route and is returned at once.
// The path is capped at maxRedactPathBytes, at most maxDecodeRounds+1 forms are
// built, each is split and scanned a constant number of times, and the
// any-position check reads each segment once. path.Clean and percent-decoding
// are linear.
//
// Fail closed, logging "/[REDACTED]": (0) the path is longer than
// maxRedactPathBytes; and, when no form matched, (1) some form has a "." or ".."
// segment and some form has an "invite" or "invites" segment followed by another
// segment at any position, because a walked path means no positional match can
// be trusted (/x/../invite/TOK/.., /api/v1/users/q/../invites/TOK/..); (2) the
// escaping is nested deeper than maxDecodeRounds (the last form would still
// decode). (1) does not fire for /books/invite/TOK or /books/../x, which have no
// dot walk over an invite route.
func RedactInvitePath(path string) string {
	// An absolute path splits to ["", seg1, seg2, ...].
	if !strings.HasPrefix(path, "/") {
		return path
	}
	// Over the cap nothing is read: fail closed.
	if len(path) > maxRedactPathBytes {
		return "/" + InviteTokenRedacted
	}
	// The ordinary case: no escape to decode and no route name to match.
	if strings.IndexByte(path, '%') < 0 && !containsFoldInvite(path) {
		return path
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
	// (a) Anchored match, each form both as written and cleaned (skipping the
	// cleaned candidate when it is the form itself).
	for i, f := range forms {
		cands := [2]string{f, cleanKeepingTrailingSlash(f)}
		if i > 0 {
			cands[0], cands[1] = cands[1], cands[0]
		}
		for j, cand := range cands {
			if j == 1 && cand == cands[0] {
				break
			}
			if out, ok := redactInviteSegments(cand, i > 0); ok {
				return out
			}
		}
	}
	// (b) Fail closed: a form with a dot segment walks the path, so an invite
	// route name anywhere in any form means no positional redaction can be trusted.
	if anyDotSegment(forms) && anyInvitePrefix(forms) {
		return "/" + InviteTokenRedacted
	}
	// Also fail closed on nesting deeper than maxDecodeRounds: if the last form
	// would still decode, a further layer of escaping may hide a route. Not
	// decoded until stable: each round of nested escapes shrinks the text by only
	// two bytes, so looping to a fixed point would be quadratic in the length of
	// the path.
	if last := forms[len(forms)-1]; len(forms) > maxDecodeRounds && unescapeLenient(last) != last {
		return "/" + InviteTokenRedacted
	}
	return path
}

// RedactInvitePathWithQuery is RedactInvitePath for a string that carries a real
// query, such as a RequestURI: the text from the first "?" is the query and is
// kept as it is (RedactQueryCredentials is its own step).
func RedactInvitePathWithQuery(pathWithQuery string) string {
	path, query, hasQuery := strings.Cut(pathWithQuery, "?")
	out := RedactInvitePath(path)
	if hasQuery {
		out += "?" + query
	}
	return out
}

// containsFoldInvite reports whether s contains "invite" in any ASCII case. A
// route segment that matches an invite word starts with those six letters (the
// one non-ASCII folding, U+017F for the s of "invites", comes after them), so a
// path without them cannot be an invite route.
func containsFoldInvite(s string) bool {
	const word = "invite"
	for i := 0; i+len(word) <= len(s); i++ {
		j := 0
		for ; j < len(word); j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != word[j] {
				break
			}
		}
		if j == len(word) {
			return true
		}
	}
	return false
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
	return invitePrefixAt(segs, nonEmptyPositions(segs), 0)
}

// nonEmptyPositions is the positions in segs of its non-empty segments.
func nonEmptyPositions(segs []string) []int {
	nonEmpty := make([]int, 0, len(segs))
	for i, s := range segs {
		if s != "" {
			nonEmpty = append(nonEmpty, i)
		}
	}
	return nonEmpty
}

// invitePrefixAt reports whether the non-empty segments starting at the k-th one
// (nonEmpty is nonEmptyPositions(segs)) are an invite route prefix followed by at
// least one more non-empty segment, and if so the position in segs of that
// segment (the token) and whether the route is the invites one. It compares at
// most len(prefix) segments per prefix, so a caller can try every start in
// O(len(segs)) in all.
func invitePrefixAt(segs []string, nonEmpty []int, k int) (idx int, invitesRoute bool) {
	for _, prefix := range inviteTokenPrefixes {
		n := len(prefix)
		if len(nonEmpty)-k <= n {
			continue
		}
		match := true
		for i, want := range prefix {
			if !strings.EqualFold(decodeSegment(segs[nonEmpty[k+i]]), want) {
				match = false
				break
			}
		}
		if match {
			return nonEmpty[k+n], prefix[n-1] == "invites"
		}
	}
	return -1, false
}

// decodeSegment is s percent-decoded once, or s itself when it has an invalid
// escape (so a prefix word cannot be hidden by one, nor invented from one).
func decodeSegment(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	return s
}

// anyDotSegment reports whether any form has a "." or ".." path segment.
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

// anyInvitePrefix reports whether any form has an "invite" or "invites" segment
// (percent-decoded once, any case) followed by another non-empty segment, at any
// position. This is deliberately wider than the routes: a dot walk can split the
// route itself (/api/v1/users/q/../invites/TOK/..), so no more than the route's
// last word is relied on. One pass over each form, one decoding per segment.
func anyInvitePrefix(forms []string) bool {
	for _, f := range forms {
		prev := false
		for _, seg := range strings.Split(f, "/") {
			if seg == "" {
				continue
			}
			if prev {
				return true
			}
			d := decodeSegment(seg)
			prev = strings.EqualFold(d, "invite") || strings.EqualFold(d, "invites")
		}
	}
	return false
}
