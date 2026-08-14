// file: internal/logger/sanitize.go
// version: 1.1.0
// guid: 1a7f3c92-5e6b-4d08-9c2a-3b8e0f1d6a47
// last-edited: 2026-08-14

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
// Clean strings (the common case) are returned unchanged with no allocation.
//
// The explicit `strings.ReplaceAll` of "\r" and "\n" below is what CodeQL
// recognizes as a log-injection sanitizer barrier. A previous revision of this
// function claimed that in a comment while the code only had the builder loop —
// which CodeQL does NOT model — so every call site downstream of this sanitizer
// stayed flagged (322 open go/log-injection alerts on 2026-08-14, many of them
// through here). The ReplaceAll calls are semantically redundant with the loop;
// they exist to be machine-recognizable. Do not "simplify" them away again.
func sanitizeLogLine(s string) string {
	if !strings.ContainsFunc(s, isLogControl) {
		return s
	}
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
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
