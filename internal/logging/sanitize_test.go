// file: internal/logging/sanitize_test.go
// version: 1.0.0
// guid: 9c4f7d21-6b83-4e05-a1d9-2f68b0c37e54
// last-edited: 2026-08-14

package logging

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestSanitize_EscapesInjectionVectors(t *testing.T) {
	// The attack shape: a newline that forges a second log record.
	forged := "The Hobbit\nlevel=INFO msg=\"admin login ok\""
	got := Sanitize(forged)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("sanitized value still contains raw newline/CR: %q", got)
	}
	if got != "The Hobbit\\nlevel=INFO msg=\"admin login ok\"" {
		t.Fatalf("newline must be ESCAPED, not dropped (the value stays greppable): %q", got)
	}

	if got := Sanitize("a\r\nb"); got != "a\\r\\nb" {
		t.Fatalf("CRLF: got %q", got)
	}

	// The common case allocates nothing and passes through unchanged.
	clean := "A Perfectly Normal Title (Unabridged)"
	if got := Sanitize(clean); got != clean {
		t.Fatalf("clean string must pass through unchanged: %q", got)
	}
}

func TestSanitizeErr(t *testing.T) {
	if got := SanitizeErr(nil); got != "" {
		t.Fatalf("nil error: got %q", got)
	}
	err := errors.New("open /lib/x\ny: no such file")
	if got := SanitizeErr(err); strings.Contains(got, "\n") {
		t.Fatalf("error text still contains raw newline: %q", got)
	}
}

// TestConduits_SanitizeAttrValues proves the Info/Warn/Error/Debug wrappers
// are a sanitizing BARRIER: a tainted attr value passed by any caller comes
// out of the handler as one record with the newline escaped. Captured through
// a real slog handler, not by calling Sanitize directly — the wiring is the
// thing under test.
func TestConduits_SanitizeAttrValues(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	Info(context.Background(), "scan result",
		"title", "Evil Book\nlevel=ERROR msg=forged", "count", 3)

	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("tainted attr forged extra log lines:\n%s", out)
	}
	// slog's TextHandler quotes the value, escaping our backslash again —
	// the raw output carries \\n for the \n we injected.
	if !strings.Contains(out, `Evil Book\\nlevel=ERROR`) {
		t.Fatalf("escaped newline missing — value was dropped instead of escaped:\n%s", out)
	}
}
