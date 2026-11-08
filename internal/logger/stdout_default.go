// file: internal/logger/stdout_default.go
// version: 1.0.0
// guid: 4911a492-f35a-4720-a75a-e18ab1bd1dfa
// last-edited: 2026-11-08

package logger

import (
	"log/slog"
	"os"
	"strings"
)

// InstallStdoutDefault makes slog.Default a text handler on standard output at
// level (log_level / --log-level; "" or a level slog does not know is info),
// and returns the call that puts the previous default back.
//
// It is for commands that have no App and so no log pipeline (scan, organize):
// slog's stock default would print their lines to standard error, while every
// other line of those commands goes to standard output. serve does not use it:
// its App installs the pipeline as the default.
func InstallStdoutDefault(level string) (restore func()) {
	lv := slog.LevelInfo
	if l := strings.TrimSpace(level); l != "" {
		if err := lv.UnmarshalText([]byte(l)); err != nil {
			lv = slog.LevelInfo
		}
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv})))
	return func() { slog.SetDefault(prev) }
}
