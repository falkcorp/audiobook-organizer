// file: internal/logger/stdout_default_test.go
// version: 1.0.1
// guid: 60428bc3-966c-4616-abec-120ad1d76457
// last-edited: 2026-11-08

package logger

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	corelog "github.com/falkcorp/mediacore/log"
)

// TestParseLevelOrInfo: core's parser decides, so trace (which slog's own
// parser rejects) is a level; "" is info silently; an unknown level is info
// with a warning that quotes what was given.
func TestParseLevelOrInfo(t *testing.T) {
	for _, tc := range []struct {
		in       string
		want     slog.Level
		wantWarn bool
	}{
		{"", slog.LevelInfo, false},
		{"  ", slog.LevelInfo, false},
		{"trace", corelog.LevelTrace, false},
		{" DEBUG ", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"loud", slog.LevelInfo, true},
	} {
		got, warn := ParseLevelOrInfo(tc.in)
		if got != tc.want || (warn != "") != tc.wantWarn {
			t.Errorf("ParseLevelOrInfo(%q) = %v, %q; want %v, warn=%v", tc.in, got, warn, tc.want, tc.wantWarn)
		}
	}
}

// TestInstallStdoutDefault_WarnsOnlyWhenAsked: an unknown level is written to
// warnTo as a "Warning:" line; a nil warnTo is accepted; the previous default
// comes back on restore.
func TestInstallStdoutDefault_WarnsOnlyWhenAsked(t *testing.T) {
	prev := slog.Default()
	var warned bytes.Buffer
	restore := InstallStdoutDefault("loud", &warned)
	if slog.Default() == prev {
		t.Error("the default was not replaced")
	}
	restore()
	if slog.Default() != prev {
		t.Error("restore did not put the previous default back")
	}
	if got := warned.String(); got == "" || got[:9] != "Warning: " {
		t.Errorf("warning = %q, want a Warning: line", got)
	}

	// A nil warnTo reports nothing and must not panic.
	InstallStdoutDefault("loud", nil)()
	if slog.Default() != prev {
		t.Error("restore after a nil warnTo did not put the previous default back")
	}
}

// TestInstallStdoutDefault_CarriesContextAttributes: the stdout default is not
// the App's pipeline, which is the handler that appends a context's
// attributes, and InfoCtx no longer adds them itself; so this default does,
// once, after the record's own attributes. Without it an operation's
// identifiers would vanish from the scan and organize commands' lines and from
// serve's lines before app.New. A forged line in an attribute value stays one
// quoted line. Not parallel: it swaps os.Stdout and slog's default.
func TestInstallStdoutDefault_CarriesContextAttributes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	restoreSlog := InstallStdoutDefault("info", nil)
	t.Cleanup(func() { os.Stdout = realStdout })

	ctx := corelog.WithAttrs(context.Background(), slog.String("op_id", "op-gotham"), slog.String("wing", "east\nINFO forged"))
	InfoCtx(ctx, "the signal is lit", "err", context.Canceled)
	slog.Default().With("k", "v").InfoContext(ctx, "derived")

	restoreSlog()
	os.Stdout = realStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if strings.Count(l, "op_id=op-gotham") != 1 || strings.Count(l, "wing=") != 1 {
			t.Errorf("line carries the context attributes %d/%d times, want once each: %q",
				strings.Count(l, "op_id=op-gotham"), strings.Count(l, "wing="), l)
		}
	}
	if !strings.Contains(lines[0], " err=") || strings.Index(lines[0], "op_id=") < strings.Index(lines[0], " err=") {
		t.Errorf("context attributes are written after the record's own: %q", lines[0])
	}
	if !strings.Contains(lines[1], "k=v") {
		t.Errorf("a derived logger lost its own attribute: %q", lines[1])
	}
}
