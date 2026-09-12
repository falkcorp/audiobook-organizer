// file: internal/logger/attrs.go
// version: 1.0.0
// guid: e9fb0153-216e-47b0-84b2-0adaaffc56ec
// last-edited: 2026-09-12

package logger

import (
	"fmt"
	"log/slog"
	"strings"
)

// AttrLogger is implemented by loggers that can write a line carrying
// structured attributes into an operation's own log, where a client can read
// them as fields instead of regexing them back out of message prose.
//
// It is an optional capability rather than a Logger method because most
// loggers (StandardLogger, test fakes) have nowhere structured to put the
// attributes. Call LogWithAttrs instead of asserting for it directly: it falls
// back to appending the attributes to the message, so nothing is dropped.
type AttrLogger interface {
	LogAttrs(level slog.Level, msg string, attrs ...slog.Attr)
}

// LogWithAttrs writes msg with attrs through l. When l implements AttrLogger
// the attributes stay structured; otherwise they are appended to the message
// as key="value" pairs and written at the matching level.
func LogWithAttrs(l Logger, level slog.Level, msg string, attrs ...slog.Attr) {
	if l == nil {
		l = New("")
	}
	if al, ok := l.(AttrLogger); ok {
		al.LogAttrs(level, msg, attrs...)
		return
	}
	LogAtLevel(l, level, "%s", msg+FormatAttrs(attrs))
}

// LogAtLevel dispatches a printf-style line to the LevelLogger method that
// matches an slog level.
func LogAtLevel(l LevelLogger, level slog.Level, format string, args ...any) {
	switch {
	case level >= slog.LevelError:
		l.Error(format, args...)
	case level >= slog.LevelWarn:
		l.Warn(format, args...)
	case level >= slog.LevelInfo:
		l.Info(format, args...)
	default:
		l.Debug(format, args...)
	}
}

// FormatAttrs renders attrs as ` key="value"` pairs for a plain-text log line.
func FormatAttrs(attrs []slog.Attr) string {
	var b strings.Builder
	for _, a := range attrs {
		fmt.Fprintf(&b, " %s=%q", a.Key, a.Value.String())
	}
	return b.String()
}
