// file: internal/logger/ctx.go
// version: 1.0.3
// guid: 941538ef-bccb-4899-ab37-a24fe681513d
// last-edited: 2026-11-07

package logger

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	corelog "github.com/falkcorp/mediacore/log"
)

// InfoCtx, WarnCtx, ErrorCtx and DebugCtx are the books-side context-aware
// log helpers. They replace core log's deleted package-level Info, Warn,
// Error and Debug (05-logging §11 item 2): args are slog's alternating
// key/value pairs (or slog.Attr values), and the attributes ctx carries
// (corelog.WithAttrs, e.g. the operation attributes internal/server sets) are
// written before them, as HEAD's helpers wrote the operation attributes.
// internal/activity's line parser matches attribute keys exactly, so the
// order no longer decides which op_id or component an activity row gets.
//
// They log through slog.Default(), the books logger path logToStdout uses,
// and keep the sanitizing barrier the deleted helpers had: the message and
// every string value (context attributes included, resolved first) pass
// through sanitizeLogLine, so a CR, LF or other control character cannot
// forge a record whatever handler slog.Default() holds.
//
// Until R1-15's app.InstallProcessDefaults points slog.Default() at the App
// pipeline, the default handler does not read corelog.AttrsFrom, so these
// helpers add the context's attributes themselves. Once it does, the
// pipeline appends them as well and this must go (R1-15 / R1-18), or each
// attribute appears twice.
func InfoCtx(ctx context.Context, msg string, args ...any) {
	logCtx(slog.Default(), ctx, slog.LevelInfo, msg, args)
}

// WarnCtx is InfoCtx at warn level.
func WarnCtx(ctx context.Context, msg string, args ...any) {
	logCtx(slog.Default(), ctx, slog.LevelWarn, msg, args)
}

// ErrorCtx is InfoCtx at error level.
func ErrorCtx(ctx context.Context, msg string, args ...any) {
	logCtx(slog.Default(), ctx, slog.LevelError, msg, args)
}

// DebugCtx is InfoCtx at debug level.
func DebugCtx(ctx context.Context, msg string, args ...any) {
	logCtx(slog.Default(), ctx, slog.LevelDebug, msg, args)
}

// logCtx builds one record the way slog.Logger does (its source position is
// the helper's caller), sanitises it and hands it to l's handler.
func logCtx(l *slog.Logger, ctx context.Context, level slog.Level, msg string, args []any) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !l.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:]) // skip runtime.Callers, logCtx and the XxxCtx helper
	// Let slog turn args into attrs (string keys, slog.Attr values, !BADKEY),
	// then copy them sanitised into the record that is written.
	in := slog.NewRecord(time.Now(), level, msg, pcs[0])
	in.Add(args...)
	out := slog.NewRecord(in.Time, level, sanitizeLogLine(msg), in.PC)
	for _, a := range corelog.AttrsFrom(ctx) {
		out.AddAttrs(sanitizeAttr(a))
	}
	in.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(sanitizeAttr(a))
		return true
	})
	// slog.Logger discards a handler error too: logging never fails its caller.
	_ = l.Handler().Handle(ctx, out)
}

// sanitizeAttr resolves a's value (a LogValuer such as internal/server's
// operation context becomes its group) and passes every string in it through
// sanitizeLogLine, descending into groups. Keys are program constants and are
// left alone.
func sanitizeAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, sanitizeLogLine(v.String()))
	case slog.KindGroup:
		in := v.Group()
		out := make([]slog.Attr, len(in))
		for i, c := range in {
			out[i] = sanitizeAttr(c)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// Sanitizing returns a logger over l's handler that passes the message and
// every string value (resolved, groups included) through sanitizeLogLine
// before l's handler sees them. It is for a books-side *slog.Logger handed to
// code that builds messages from outside text and relies on the handler to
// quote them (core/health's flight recorder): until R1-15 points
// slog.Default() at the core pipeline, the default handler may be slog's
// built-in one, which writes the message unquoted (for example when
// setupFileLogging fails and nothing replaced it). A nil l yields nil.
func Sanitizing(l *slog.Logger) *slog.Logger {
	if l == nil {
		return nil
	}
	return slog.New(sanitizingHandler{next: l.Handler()})
}

// sanitizingHandler is Sanitizing's handler.
type sanitizingHandler struct{ next slog.Handler }

func (h sanitizingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h sanitizingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, sanitizeLogLine(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(sanitizeAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h sanitizingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = sanitizeAttr(a)
	}
	return sanitizingHandler{next: h.next.WithAttrs(clean)}
}

func (h sanitizingHandler) WithGroup(name string) slog.Handler {
	return sanitizingHandler{next: h.next.WithGroup(name)}
}

// SanitizeLogErr renders err for logging through SanitizeLogValue's barrier
// (CR, LF and other control characters escaped), or "" for a nil error. It
// replaces core log's deleted SanitizeErr for books-side call sites that log
// error text straight to log/slog: error text routinely wraps user-controlled
// strings (file names, tag values).
func SanitizeLogErr(err error) string {
	if err == nil {
		return ""
	}
	return SanitizeLogValue(err.Error())
}
