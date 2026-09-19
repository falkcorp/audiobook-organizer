// file: internal/lifecycle/shutdown.go
// version: 1.0.0
// guid: 59608b55-5c49-4ca4-9443-a025758bf746
// last-edited: 2026-09-19

// Package lifecycle holds process-lifecycle sentinels shared by layers that
// must not import each other (the operations registry and the subsystems its
// ops run).
package lifecycle

import (
	"context"
	"errors"
	"fmt"
)

// ErrShutdown is the cancellation cause the operations registry gives a
// running op's context when the server is shutting down. The op is marked
// interrupted and, if it declares ResumeRestart, re-run after the restart.
//
// ctx.Err() is context.Canceled for a shutdown and an operator cancel alike;
// context.Cause is what tells them apart. An op holding paid work outside the
// process (an OpenAI batch) must leave that work running on a shutdown and
// cancel it only on a real cancel.
//
// It wraps context.Canceled so every existing errors.Is(err, context.Canceled)
// check — including code that returns context.Cause(ctx) instead of ctx.Err(),
// like the scan stand-down checkpoint and the organizer — still sees a cancel.
var ErrShutdown = fmt.Errorf("server shutting down: %w", context.Canceled)

// IsShutdown reports whether ctx was canceled because the server is shutting
// down, as opposed to by an operator cancel or a timeout.
func IsShutdown(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrShutdown)
}
