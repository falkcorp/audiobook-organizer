// file: internal/logger/sanitize_test.go
// version: 1.1.0
// guid: 9c2e4a17-6b8d-4f31-a2c5-7e0f1b3d6a92
// last-edited: 2026-09-01

package logger

import (
	"strings"
	"testing"
)

func TestSanitizeLogLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"clean", "extracting tags for /books/a.mp3", "extracting tags for /books/a.mp3"},
		{"newline forges line", "book.mp3\n2026 FAKE LOG ENTRY", `book.mp3\n2026 FAKE LOG ENTRY`},
		{"carriage return", "a\rb", `a\rb`},
		{"crlf", "a\r\nb", `a\r\nb`},
		{"ansi escape", "\x1b[31mred\x1b[0m", `\x1b[31mred\x1b[0m`},
		{"nul byte", "a\x00b", `a\x00b`},
		{"del byte", "a\x7fb", `a\x7fb`},
		{"tab preserved", "a\tb", "a\tb"},
		{"unicode preserved", "café déjà", "café déjà"},
		{"unicode then newline", "café\n", `café\n`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeLogLine(c.in); got != c.want {
				t.Errorf("sanitizeLogLine(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// A clean string must be returned without modification (fast path; identity).
func TestSanitizeLogLineCleanIsIdentity(t *testing.T) {
	const clean = "no control chars here 12345 /a/b/c.mp3"
	if got := sanitizeLogLine(clean); got != clean {
		t.Fatalf("expected identity for clean input, got %q", got)
	}
}

// TestSanitizeLogValue_IsTheSameBarrierAsTheInternalOne guards the exported
// wrapper added for call sites that log through log/slog rather than this
// package's Logger.
//
// It matters that this is a thin pass-through with NO guard clause of its own.
// sanitizeLogLine's own doc records that a clean-string fast path placed before
// the ReplaceAll calls left 321 of 322 go/log-injection alerts open, because
// CodeQL's sanitizer barrier is path-sensitive. An exported wrapper that
// short-circuits would reintroduce exactly that, and would do so invisibly —
// the escaping would still look correct in every unit test.
func TestSanitizeLogValue_IsTheSameBarrierAsTheInternalOne(t *testing.T) {
	cases := []string{
		"",
		"plain/path/to/Book.m4b",
		"forged\nWARN attacker-controlled entry",
		"carriage\rreturn",
		"bell\x07and\x1bescape",
		"tab\tpreserved",
	}
	for _, in := range cases {
		if got, want := SanitizeLogValue(in), sanitizeLogLine(in); got != want {
			t.Errorf("SanitizeLogValue(%q) = %q, want %q (must be a pass-through)", in, got, want)
		}
	}

	// Spot-check the property the barrier exists for, so this test still fails
	// if BOTH functions are broken together.
	if got := SanitizeLogValue("a\nb"); strings.Contains(got, "\n") {
		t.Errorf("SanitizeLogValue left a raw newline in %q: a path can forge a log entry", got)
	}
}
