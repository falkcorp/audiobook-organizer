// file: internal/logger/standard.go
// version: 2.0.2
// guid: 3f2504e0-4f89-11d3-9a0c-0305e82c3301
// last-edited: 2026-11-08

package logger

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	corelog "github.com/falkcorp/mediacore/log"
)

// ActivityLogWriter is an optional interface for writing to a system activity log.
type ActivityLogWriter interface {
	AddSystemActivityLog(source, level, message string) error
}

// StandardLogger is the books-side Logger over log/slog (05-logging §11 item
// 4, stage 2). The printf-style methods format their message, sanitise it
// (sanitizeLogLine) and write one slog record whose "subsystem" is an
// attribute of the logger, so the App's pipeline can level and label it per
// subsystem (core.log.levels) instead of finding the name baked into the
// text. Progress, change tracking and cancellation are no-ops.
type StandardLogger struct {
	subsystem      string
	minStdout      Level
	activityWriter ActivityLogWriter

	// base is the slog logger the records go to. nil (New, NewWithActivityLog)
	// means the process default, looked up at each call, because loggers are
	// package variables built before app.InstallProcessDefaults installs the
	// App's pipeline as the default.
	base *slog.Logger
	// scoped caches the logger derived from the last default seen, so a call
	// does not rebuild the subsystem's handler each time.
	scoped atomic.Pointer[scopedLogger]
}

// scopedLogger is a slog logger paired with the logger it was derived from.
type scopedLogger struct{ from, l *slog.Logger }

// FromSlog returns a StandardLogger that writes to l; a nil l is the process
// default, looked up at each call. It is the one constructor: New and
// NewWithActivityLog are FromSlog over the default. The result is the
// concrete type (assignable to Logger) so New can build on it.
func FromSlog(l *slog.Logger) *StandardLogger {
	return &StandardLogger{minStdout: LevelDebug, base: l}
}

// New creates a StandardLogger for the given subsystem, over the process
// default.
func New(subsystem string) *StandardLogger {
	l := FromSlog(nil)
	l.subsystem = subsystem
	return l
}

// NewWithActivityLog creates a StandardLogger that also writes INFO+ to the system activity log.
func NewWithActivityLog(subsystem string, writer ActivityLogWriter) *StandardLogger {
	l := New(subsystem)
	l.activityWriter = writer
	return l
}

// slogger is the slog logger a record goes to: base, or the default, either
// with the subsystem attribute.
func (l *StandardLogger) slogger() *slog.Logger {
	from := l.base
	if from == nil {
		from = slog.Default()
	}
	if l.subsystem == "" {
		return from
	}
	if c := l.scoped.Load(); c != nil && c.from == from {
		return c.l
	}
	c := &scopedLogger{from: from, l: from.With("subsystem", l.subsystem)}
	l.scoped.Store(c)
	return c.l
}

// slogLevel maps a Level to the pipeline's levels; core has a trace level
// below slog's debug.
func slogLevel(level Level) slog.Level {
	switch level {
	case LevelTrace:
		return corelog.LevelTrace
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		// LevelInfo itself, and any Level this package does not define: an
		// unknown value is logged at info rather than dropped or promoted.
		return slog.LevelInfo
	}
}

func (l *StandardLogger) log(level Level, msg string, args ...any) {
	if level >= l.minStdout {
		l.emit(level, msg, args)
	}
	if l.activityWriter != nil && level >= LevelInfo {
		formatted := msg
		if len(args) > 0 {
			formatted = fmt.Sprintf(msg, args...)
		}
		formatted = sanitizeLogLine(formatted)
		_ = l.activityWriter.AddSystemActivityLog(l.subsystem, level.String(), formatted)
	}
}

// emit formats msg and writes it as one record at level, with the position of
// the caller of the Trace/Debug/Info/Warn/Error method.
func (l *StandardLogger) emit(level Level, msg string, args []any) {
	sl := l.slogger()
	ctx := context.Background()
	lv := slogLevel(level)
	if !sl.Enabled(ctx, lv) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(4, pcs[:]) // skip runtime.Callers, emit, log and the level method
	rec := slog.NewRecord(time.Now(), lv, sanitizeLogLine(fmt.Sprintf(msg, args...)), pcs[0])
	// slog.Logger discards a handler error too: logging never fails its caller.
	_ = sl.Handler().Handle(ctx, rec)
}

func (l *StandardLogger) Trace(msg string, args ...any) { l.log(LevelTrace, msg, args...) }
func (l *StandardLogger) Debug(msg string, args ...any) { l.log(LevelDebug, msg, args...) }
func (l *StandardLogger) Info(msg string, args ...any)  { l.log(LevelInfo, msg, args...) }
func (l *StandardLogger) Warn(msg string, args ...any)  { l.log(LevelWarn, msg, args...) }
func (l *StandardLogger) Error(msg string, args ...any) { l.log(LevelError, msg, args...) }

func (l *StandardLogger) UpdateProgress(current, total int, message string) {}
func (l *StandardLogger) RecordChange(change Change)                        {}
func (l *StandardLogger) ChangeCounters() map[string]int                    { return nil }
func (l *StandardLogger) IsCanceled() bool                                  { return false }

func (l *StandardLogger) With(subsystem string) Logger {
	prefix := subsystem
	if l.subsystem != "" {
		prefix = l.subsystem + "." + subsystem
	}
	return &StandardLogger{
		subsystem:      prefix,
		minStdout:      l.minStdout,
		activityWriter: l.activityWriter,
		base:           l.base,
	}
}
