// file: internal/maintenance/result.go
// version: 1.0.0
// guid: 016e4969-dfb6-49b0-88b8-c2c9683ff71a
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"errors"
)

// resultSetterKey carries the run's result writer. It is a context value, like
// WithRawParams, because MaintenanceJob.Run's signature has no route for it and
// widening that signature would touch every registered job for a concern only
// the jobs that produce a structured payload have.
type resultSetterKey struct{}

// ErrNoResultSetter is returned by SetResult when the run was started without a
// result writer (a direct Run call in a test, or a runner that never wired one).
var ErrNoResultSetter = errors.New("maintenance: this run has no result writer; its structured result cannot be persisted")

// WithResultSetter returns a context whose SetResult persists through fn. The
// maintenance op bridge wires fn to the v2 operation row, which is what
// GET /operations/:id/result serves.
func WithResultSetter(ctx context.Context, fn func(v any) error) context.Context {
	return context.WithValue(ctx, resultSetterKey{}, fn)
}

// SetResult persists v as the run's structured result payload.
//
// It fails loudly (ErrNoResultSetter) rather than dropping the payload, for the
// same reason registry.ReporterSetResult does: the payload IS the output a
// caller reads back, and a silently missing one looks like "no result" rather
// than "lost result".
func SetResult(ctx context.Context, v any) error {
	fn, _ := ctx.Value(resultSetterKey{}).(func(v any) error)
	if fn == nil {
		return ErrNoResultSetter
	}
	return fn(v)
}
