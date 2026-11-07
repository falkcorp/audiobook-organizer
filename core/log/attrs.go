// file: core/log/attrs.go
// version: 1.0.0
// guid: d09f3270-bc43-4dc3-a6d8-45a9bbf9a57e
// last-edited: 2026-11-07

package log

import (
	"context"
	"log/slog"
	"slices"
)

// ctxAttrsKey is the context key of the attributes WithAttrs carries. It is a
// zero-size unexported type, so no other package can read or overwrite the
// value and no package-level variable is needed.
type ctxAttrsKey struct{}

// WithAttrs returns a context carrying attrs after the attributes ctx already
// carries (05-logging §3, "Context-carried attributes"). The pipeline appends
// them to every record logged with that context, before redaction, so a
// credential set here is redacted like any other attribute. They replace
// HEAD's OpContext: tasks, httpapi and realtime set run_id, request_id, route
// and user_id through it (KeyRunID, KeyRequestID, KeyRoute, KeyUserID).
//
// The returned context holds its own slice: the attributes of ctx are copied,
// never shared or appended to in place, so two contexts derived from one
// parent never see each other's attributes. A LogValuer value is resolved when
// a record is logged, not here, so a value that changes later (a run's status)
// shows its state at log time. With no attrs, ctx is returned unchanged.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	prev := ctxAttrs(ctx)
	merged := make([]slog.Attr, 0, len(prev)+len(attrs))
	merged = append(merged, prev...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, ctxAttrsKey{}, merged)
}

// AttrsFrom returns a copy of the attributes ctx carries, oldest first, or nil
// when it carries none (or ctx is nil). The copy is the caller's to modify.
func AttrsFrom(ctx context.Context) []slog.Attr {
	return slices.Clone(ctxAttrs(ctx))
}

// ctxAttrs is AttrsFrom without the copy, for the pipeline and WithAttrs,
// which never modify the slice.
func ctxAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(ctxAttrsKey{}).([]slog.Attr)
	return a
}
