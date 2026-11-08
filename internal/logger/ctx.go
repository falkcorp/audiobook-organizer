// file: internal/logger/ctx.go
// version: 1.1.0
// guid: 941538ef-bccb-4899-ab37-a24fe681513d
// last-edited: 2026-11-08

package logger

import (
	"context"
	"log/slog"
	"runtime"
	"time"
)

// InfoCtx, WarnCtx, ErrorCtx and DebugCtx are the books-side context-aware
// log helpers. They replace core log's deleted package-level Info, Warn,
// Error and Debug (05-logging §11 item 2): args are slog's alternating
// key/value pairs (or slog.Attr values), and the attributes ctx carries
// (corelog.WithAttrs, e.g. the operation attributes internal/server sets) are
// the pipeline's to add: the App's handler, which is slog.Default() once
// app.InstallProcessDefaults has run, appends corelog.AttrsFrom(ctx) to the
// record itself, after the record's own attributes. The helpers must not add
// them as well, or each appears twice. internal/activity's line parser
// matches attribute keys exactly, so the order does not decide which op_id or
// component an activity row gets.
//
// They log through slog.Default(), the books logger path the StandardLogger
// shim uses, and keep the sanitizing barrier the deleted helpers had: the
// message and every string value the call passes pass through
// sanitizeLogLine, so a CR, LF or other control character cannot forge a
// record whatever handler slog.Default() holds. Context attributes are
// resolved and quoted by the handler that appends them.
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
// the helper's caller), sanitises its message and attributes and hands it to
// l's handler with ctx, which is where the handler finds the context's
// attributes.
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
	in.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(sanitizeAttr(a))
		return true
	})
	// slog.Logger discards a handler error too: logging never fails its caller.
	_ = l.Handler().Handle(ctx, out)
}

// sanitizeAttr resolves a's value (a LogValuer becomes its group) and passes every string in it through
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
