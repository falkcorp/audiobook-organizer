// file: internal/logger/sanitize_test.go
// version: 1.0.0
// guid: 9c2e4a17-6b8d-4f31-a2c5-7e0f1b3d6a92
// last-edited: 2026-06-17

package logger

import "testing"

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
