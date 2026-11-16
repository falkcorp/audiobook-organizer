// file: core/health/diagnostics/safekeys.go
// version: 1.0.0
// guid: 2f6a9d13-c8e4-4b70-9a15-d3e7b0c41f68
// last-edited: 2026-11-16

package diagnostics

import (
	"strings"
	"time"
)

// safeKey is the validator of a JSON member name whose string value is
// structured, not free text: a value that passes its key's validator is kept as
// written (after the log redactor, which has already run), anything else goes
// through Sanitizer.Text. It is what keeps a route template ("GET
// /api/v1/books/{id}") and a reason token ("no_roots:read") that Text would wipe
// ("/" and "x:" are triggers). The grammars are hand-written, not regular
// expressions (core forbids package-level regexps, 03 R5); the specs are in the
// comments.
//
// A key is matched exactly (case-sensitive). The allowlist is a promise about
// the producer of the field: `route` and `pattern` hold a router template, never
// a raw URL, and the validator only bounds its shape (see validRoute).
func safeKey(key string) (func(string) bool, bool) {
	switch key {
	case "level":
		return validLevel, true
	case "time":
		return validTime, true
	case "component", "module", "kind", "task", "state", "status", "outcome", "reason":
		return validToken, true
	case "method":
		return validMethod, true
	case "route", "pattern":
		return validRoute, true
	case "run_id", "id":
		return validID, true
	case "version":
		return validVersion, true
	}
	return nil, false
}

// validLevel is the slog and common level names, in either case.
func validLevel(s string) bool {
	switch strings.ToUpper(s) {
	case "TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "FATAL", "PANIC":
		return true
	}
	return false
}

// validTime is an RFC 3339 timestamp.
func validTime(s string) bool {
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// validToken is ^[a-z0-9_.:-]{1,64}$.
func validToken(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// validMethod is an HTTP method name.
func validMethod(s string) bool {
	switch s {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return true
	}
	return false
}

// validRoute is a router pattern: an optional "METHOD " prefix and a path that
// starts "/", whose segments (at most 16, none empty but a trailing one, none
// "." or "..") are a name of [A-Za-z0-9._-] or a wildcard, "{name}", "{name...}"
// or "{$}" (name of [A-Za-z0-9_]). The first segment is a mount the router
// owns, "api" or "public" (with at least one segment after it), or the whole
// path is "/livez", "/readyz" or "/metrics": "/Users/bruce/x" is not a route.
// What it cannot tell is a raw request path that spells a filesystem path under
// "/api/": a producer of `route` or `pattern` must give the template.
func validRoute(s string) bool {
	if len(s) > 256 {
		return false
	}
	if m, rest, ok := strings.Cut(s, " "); ok {
		if !validMethod(m) {
			return false
		}
		s = rest
	}
	switch s {
	case "/livez", "/readyz", "/metrics":
		return true
	}
	if !strings.HasPrefix(s, "/") {
		return false
	}
	segs := strings.Split(s[1:], "/")
	if len(segs) > 16 || len(segs) < 2 || (segs[0] != "api" && segs[0] != "public") {
		return false
	}
	for i, seg := range segs {
		if seg == "" {
			if i == len(segs)-1 && i > 1 {
				continue
			}
			return false
		}
		if seg == "." || seg == ".." || !validSegment(seg) {
			return false
		}
	}
	return true
}

// validSegment is a literal name or a wildcard of a route.
func validSegment(seg string) bool {
	if seg[0] == '{' {
		if seg[len(seg)-1] != '}' {
			return false
		}
		name := seg[1 : len(seg)-1]
		if name == "$" {
			return true
		}
		name = strings.TrimSuffix(name, "...")
		if name == "" {
			return false
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validID is a ULID (26 characters of Crockford base32, the first at most 7) or
// a UUID (8-4-4-4-12 hex digits).
func validID(s string) bool {
	switch len(s) {
	case 26:
		if s[0] > '7' {
			return false
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
			if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' && c != 'I' && c != 'L' && c != 'O' && c != 'U') {
				return false
			}
		}
		return true
	case 36:
		for i := 0; i < len(s); i++ {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if s[i] != '-' {
					return false
				}
				continue
			}
			if !isHexDigit(s[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// validVersion is a semantic version ("v1.2.3", "1.2.3-rc.1+build.5", a Go
// pseudo-version) or a VCS revision (7 to 40 lower-case hex digits).
func validVersion(s string) bool {
	if n := len(s); n >= 7 && n <= 40 && isHexString(s) {
		return true
	}
	s = strings.TrimPrefix(s, "v")
	core, rest := s, ""
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		core, rest = s[:i], s[i:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 10 {
			return false
		}
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
		}
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+') {
			return false
		}
	}
	return len(rest) <= 128
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
