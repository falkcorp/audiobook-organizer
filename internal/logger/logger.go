// file: internal/logger/logger.go
// version: 1.3.0
// guid: f47ac10b-58cc-4372-a567-0e02b2c3d479
// last-edited: 2026-06-17

package logger

import (
	"context"
	"fmt"
	"log/slog"
)

// Level represents a log severity level.
type Level int

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the level name for display.
func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "trace"
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

// ParseLevel converts a string to a Level.
func ParseLevel(s string) Level {
	switch s {
	case "trace":
		return LevelTrace
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Change represents a tracked change during an operation.
type Change struct {
	BookID     string
	ChangeType string // "book_create", "book_update", "file_move", "metadata_update"
	Field      string // optional: specific field name
	OldValue   string // optional
	NewValue   string // optional
	Summary    string // human-readable
}

// Logger is the central interface for logging, progress, and change tracking.
type Logger interface {
	Trace(msg string, args ...any)
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)

	// Progress reporting (operations only; no-op on StandardLogger)
	UpdateProgress(current, total int, message string)

	// Change tracking
	RecordChange(change Change)

	// Get accumulated change counters (e.g., {"book_create": 150})
	ChangeCounters() map[string]int

	// Operation awareness
	IsCanceled() bool

	// Create child logger with subsystem prefix
	With(subsystem string) Logger
}

// Compile-time assertions that both concrete types fully implement Logger.
var _ Logger = (*StandardLogger)(nil)
var _ Logger = (*OperationLogger)(nil)

// logToStdout formats and prints a log line to stdout using slog.
func logToStdout(subsystem string, level Level, msg string, args ...any) {
	formatted := sanitizeLogLine(fmt.Sprintf(msg, args...))
	l := slog.Default()
	msgWithSubsystem := formatted
	if subsystem != "" {
		msgWithSubsystem = fmt.Sprintf("%s: %s", subsystem, formatted)
	}
	switch level {
	case LevelTrace, LevelDebug:
		l.Debug(msgWithSubsystem)
	case LevelInfo:
		l.Info(msgWithSubsystem)
	case LevelWarn:
		l.Warn(msgWithSubsystem)
	case LevelError:
		l.Error(msgWithSubsystem)
	default:
		l.Info(msgWithSubsystem)
	}
}

// contextKey is a private type used as the key for storing a slog.Logger in a context.
type contextKey struct{}

// WithOperation returns a context carrying a slog.Logger that has the op_id attribute set.
// Callers can retrieve the logger with FromContext(ctx) and use it for structured logging
// so that log lines emitted within an operation are tagged with the operation id.
func WithOperation(ctx context.Context, opID string) context.Context {
	l := slog.Default().With("op_id", opID)
	return context.WithValue(ctx, contextKey{}, l)
}

// FromContext returns the slog.Logger stored in ctx, or slog.Default() if none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
