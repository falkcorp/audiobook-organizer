// file: core/auth/oidc/returnpath.go
// version: 0.1.0
// guid: 8f3d2b61-4c7a-4e09-b5d8-2a6e1f9c0b47
// last-edited: 2026-11-22

package oidc

import (
	"net/url"
	"strings"
)

// maxReturnLen bounds a return path; a longer one is refused, not truncated.
const maxReturnLen = 512

// SanitizeReturn returns ret when it is a same-site absolute path that is safe
// to redirect or navigate to after sign-in, and "" otherwise, so the caller
// falls back to its own default. It is the one Go implementation: the legacy
// OAuth login (internal/server/handlers) calls it too, and the shell's
// sanitizeReturn (core/web/packages/shell/src/auth/safeReturn.ts) applies the
// same rules. testdata/return_payloads.json is the shared payload table.
//
// The checks, in order:
//   - a single leading "/" (so no scheme, no host, no "//host"), at most
//     maxReturnLen bytes;
//   - no backslash (browsers read one as "/", so "/\host" is "//host") and no
//     control character, anywhere in the raw string;
//   - the path, percent-decoded, has no backslash or control character either
//     (so "/%5chost" and "/a%0ab" are refused, and a malformed escape too);
//   - the decoded path with its dot segments removed (RFC 3986 §5.2.4, the
//     resolution a browser applies) does not start with "//". "/.//host",
//     "/%2e//host", "/a/..//host" and "/./%2F/host" all resolve to "//host",
//     which a browser navigating to it reads as scheme-relative: checking the
//     raw input alone would be a parser differential. A "%2e" left in the
//     decoded path (from "%252e" in the raw one) counts as a dot too, as a
//     browser's URL parser counts it, so "/%252e%252e//host" is refused.
//
// An accepted ret is returned byte-for-byte.
func SanitizeReturn(ret string) string {
	if ret == "" || len(ret) > maxReturnLen || ret[0] != '/' {
		return ""
	}
	if len(ret) > 1 && ret[1] == '/' {
		return ""
	}
	if hasUnsafeRune(ret) {
		return ""
	}
	u, err := url.Parse(ret)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil || hasUnsafeRune(decoded) {
		return ""
	}
	if strings.HasPrefix(removeDotSegments(encodedDotsAsDots(decoded)), "//") {
		return ""
	}
	return ret
}

// encodedDotsAsDots maps each percent-encoded dot to "." for dot-segment removal.
func encodedDotsAsDots(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "%2e", "."), "%2E", ".")
}

// hasUnsafeRune reports a backslash or a C0/DEL control character.
func hasUnsafeRune(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r == '\\' || r < 0x20 || r == 0x7f })
}

// removeDotSegments is RFC 3986 §5.2.4's algorithm. Unlike path.Clean it keeps
// empty segments, so "//" survives and "/.//x" becomes "//x" as it does in a
// browser.
func removeDotSegments(in string) string {
	var out strings.Builder
	trimLast := func() {
		s := out.String()
		i := max(strings.LastIndexByte(s, '/'), 0)
		out.Reset()
		out.WriteString(s[:i])
	}
	for in != "" {
		switch {
		case strings.HasPrefix(in, "../"):
			in = in[3:]
		case strings.HasPrefix(in, "./"):
			in = in[2:]
		case strings.HasPrefix(in, "/./"):
			in = in[2:]
		case in == "/.":
			in = "/"
		case strings.HasPrefix(in, "/../"):
			in = in[3:]
			trimLast()
		case in == "/..":
			in = "/"
			trimLast()
		case in == "." || in == "..":
			in = ""
		default:
			end := strings.IndexByte(in[1:], '/') + 1
			if end == 0 {
				end = len(in)
			}
			out.WriteString(in[:end])
			in = in[end:]
		}
	}
	return out.String()
}
