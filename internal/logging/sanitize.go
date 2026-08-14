// file: internal/logging/sanitize.go
// version: 1.0.0
// guid: 6e2a8b40-1d5f-4c93-b7e8-3a09d6c15f82
// last-edited: 2026-08-14

package logging

import "strings"

// Sanitize neutralizes log injection (CodeQL go/log-injection, CA12): a
// user-controlled string containing "\n" or "\r" can otherwise forge entire
// fake log lines — an attacker-supplied title of
// "x\nlevel=INFO msg=\"admin login ok\"" prints as two records. Newlines and
// carriage returns are ESCAPED (\n -> \\n), not dropped, so the logged value
// remains a faithful, greppable representation of what the user actually sent.
//
// Use it on any string that originates outside the process — request params,
// file paths from disk scans, tag values read from media files, titles,
// external API payloads — at the point it is passed to slog/fmt logging.
// Values the program itself computed (op IDs, enum states, counts) need no
// wrapping.
func Sanitize(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// SanitizeErr renders an error for logging with the same newline escaping as
// Sanitize. Errors routinely wrap user-controlled strings (file names, tag
// values), which makes err.Error() exactly as forgeable as the raw input.
// Returns "" for a nil error.
func SanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	return Sanitize(err.Error())
}
