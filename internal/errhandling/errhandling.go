// file: internal/errhandling/errhandling.go
// version: 1.0.0
// guid: 525ad74d-d188-4d2f-9a1a-7806ac7599fc
// last-edited: 2026-08-11

// Package errhandling provides the two primitives the silent-failure sweep
// needs, so that waves 4-13 do not each invent their own.
//
// The problem this package exists to solve is documented in
// docs/audits/2026-08-11-silent-failure-error-discards.md: the backend has
// ~1,125 statement-position error discards, and the ones that matter are
// indistinguishable from the ones that don't because BOTH look like `_ = f()`.
//
// The two primitives:
//
//   - [MustLog] — "we looked at this error and chose to continue." One call
//     instead of five lines, and crucially it leaves a WARN in the log, so the
//     discard is auditable after the fact instead of invisible.
//
//   - [SkipCounter] — for bucket (d): loops that `continue` on error with no
//     counter and no log, so the loop reports "processed 4,812" and the
//     denominator is silently wrong. A SkipCounter makes the skipped count a
//     first-class number and emits exactly one summary line at loop exit.
//
// # Choosing between them
//
// Use [MustLog] for a one-off discard. Use [SkipCounter] inside a loop — one
// MustLog per iteration turns a 300-item skip into 300 log lines, which is its
// own kind of invisible.
//
// # What this package is NOT for
//
// It is not a way to keep discarding errors with a clearer conscience. If the
// error should abort the operation, return it. MustLog is for the case where
// continuing is genuinely the right call and the only defect is that nobody
// can tell it happened.
package errhandling

import (
	"context"
	"log/slog"
	"sync"
)

// logger holds the slog.Logger used by this package. It is nil until
// [SetLogger] is called, in which case [activeLogger] falls back to
// slog.Default().
//
// This indirection exists so tests can observe what was logged. A helper whose
// output cannot be observed cannot be tested — a "does not panic" test would
// pass with the body deleted.
var (
	loggerMu sync.RWMutex
	logger   *slog.Logger
)

// SetLogger overrides the logger used by this package and returns a function
// that restores the previous one. It is intended for tests and for wiring at
// startup; production code should normally leave it alone and let the package
// use slog.Default().
//
// The returned restore function makes it safe to use with defer:
//
//	defer errhandling.SetLogger(testLogger)()
func SetLogger(l *slog.Logger) (restore func()) {
	loggerMu.Lock()
	prev := logger
	logger = l
	loggerMu.Unlock()
	return func() {
		loggerMu.Lock()
		logger = prev
		loggerMu.Unlock()
	}
}

// activeLogger returns the logger to use for this call.
func activeLogger() *slog.Logger {
	loggerMu.RLock()
	l := logger
	loggerMu.RUnlock()
	if l == nil {
		return slog.Default()
	}
	return l
}

// MustLog records an error that the caller has deliberately chosen not to
// propagate. It is a no-op when err is nil, so it is safe to call
// unconditionally:
//
//	errhandling.MustLog(store.SaveCheckpoint(ctx, cp), "checkpoint not saved",
//	    "op_id", opID)
//
// The message should say what the consequence is ("checkpoint not saved"), not
// what the call was ("SaveCheckpoint failed") — the error already carries the
// latter. kv is a slog-style alternating key/value list.
//
// Logged at WARN. A discard that deserves ERROR is probably not a discard;
// return the error instead.
func MustLog(err error, msg string, kv ...any) {
	MustLogContext(context.Background(), err, msg, kv...)
}

// MustLogContext is [MustLog] with a context, so the record participates in
// whatever the active slog handler extracts from it. Prefer this inside code
// that already has a ctx in scope.
func MustLogContext(ctx context.Context, err error, msg string, kv ...any) {
	if err == nil {
		return
	}
	// Parse the caller's key/value list FIRST, then append the error.
	// Order matters: if the error attr were appended before parsing, a caller
	// who passed an odd number of kv args would have their dangling key
	// consume the error as its value — losing the error inside the helper
	// whose entire job is to not lose the error.
	attrs := append(toAttrs(kv), slog.Any("error", err))
	activeLogger().LogAttrs(ctx, slog.LevelWarn, msg, attrs...)
}

// toAttrs converts a slog-style alternating key/value list into []slog.Attr.
// A trailing key with no value is preserved as a "!BADKEY" attr rather than
// dropped, matching slog's own behaviour — losing the operand would hide the
// very information the caller was trying to record.
func toAttrs(args []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(args))
	for i := 0; i < len(args); {
		switch v := args[i].(type) {
		case slog.Attr:
			attrs = append(attrs, v)
			i++
		case string:
			if i+1 < len(args) {
				attrs = append(attrs, slog.Any(v, args[i+1]))
				i += 2
			} else {
				attrs = append(attrs, slog.String("!BADKEY", v))
				i++
			}
		default:
			attrs = append(attrs, slog.Any("!BADKEY", v))
			i++
		}
	}
	return attrs
}
