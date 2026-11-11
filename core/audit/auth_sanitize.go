// file: core/audit/auth_sanitize.go
// version: 1.0.1
// guid: 5d7ab1b1-eb09-4602-8324-5a3f06413949
// last-edited: 2026-11-10

package audit

import "strings"

// sanitizeLogValue escapes CR, LF and the other C0 controls and DEL in a value
// an auth attempt record logs, so a submitted username or user agent cannot
// forge a log line (go/log-injection). It is ABO's internal/logger
// SanitizeLogValue, which the auth attempt record used before it moved here
// (F4-02); tab is kept verbatim.
func sanitizeLogValue(s string) string {
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

// isLogControl reports a C0 control (< 0x20) or DEL.
func isLogControl(r rune) bool { return r < 0x20 || r == 0x7f }
