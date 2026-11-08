// file: internal/logger/stdout_default_test.go
// version: 1.0.0
// guid: 60428bc3-966c-4616-abec-120ad1d76457
// last-edited: 2026-11-08

package logger

import (
	"bytes"
	"log/slog"
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
