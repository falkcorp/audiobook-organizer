// file: internal/logger/sanitize.go
// version: 1.3.0
// guid: 1a7f3c92-5e6b-4d08-9c2a-3b8e0f1d6a47
// last-edited: 2026-09-01

package logger

import "strings"

// sanitizeLogLine neutralizes control characters in a formatted log line so that
// user-controlled values (file paths, embedded audiobook tags, error strings)
// cannot forge additional log entries via embedded CR/LF or inject terminal
// escape sequences. This is the sink-side barrier for go/log-injection.
//
//   - "\n" -> `\n`, "\r" -> `\r` (kept visible, single line preserved)
//   - other C0 control bytes and DEL -> `\xNN`
//   - "\t" is preserved (intended, safe in logs)
//
// Clean strings (the common case) are returned unchanged with no allocation:
// ReplaceAll returns its input untouched when there is nothing to replace.
//
// The explicit `strings.ReplaceAll` of "\r" and "\n" below is what CodeQL
// recognizes as a log-injection sanitizer barrier, and it must run on EVERY
// path through this function. Two prior revisions broke that invisibly:
// one had only the builder loop (which CodeQL does not model), and the next
// put a clean-string fast-path (`if !ContainsFunc { return s }`) BEFORE the
// ReplaceAll calls — CodeQL's barrier is path-sensitive, so the early return
// read as taint bypassing the sanitizer and 321 of 322 go/log-injection
// alerts stayed open. No guard clause before the ReplaceAll calls, ever.
// SanitizeLogValue is the exported barrier, for call sites that log
// user-controlled values through log/slog (or any other logger) instead of
// this package's Logger.
//
// The unexported sanitizeLogLine is applied automatically by logger.Logger,
// logger.OperationLogger and the standard-logger bridge — but ONLY by those.
// A package that reaches for log/slog directly gets no barrier at all, which
// is how internal/versions came to hold two open go/log-injection alerts while
// the rest of the codebase was clean. Prefer this package's Logger; where a
// function has no logger to hand, wrap the tainted value with this.
func SanitizeLogValue(s string) string {
	return sanitizeLogLine(s)
}

func sanitizeLogLine(s string) string {
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	if !strings.ContainsFunc(s, isLogControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	const hex = "0123456789abcdef"
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteByte('\t')
		case isLogControl(r):
			b.WriteString(`\x`)
			b.WriteByte(hex[(r>>4)&0xf])
			b.WriteByte(hex[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isLogControl reports whether r is a control character that must be escaped in
// log output: C0 controls (< 0x20) and DEL (0x7f). Tab is treated as a control
// here for detection but is preserved verbatim by sanitizeLogLine.
func isLogControl(r rune) bool {
	return r < 0x20 || r == 0x7f
}
