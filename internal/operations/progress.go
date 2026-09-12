// file: internal/operations/progress.go
// version: 1.2.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-09-12

// Package operations provides shared types for async operation execution.
// This file holds ProgressReporter, OperationFunc, and LoggerFromReporter —
// extracted from the deleted queue.go during BridgeQueue elimination.

package operations

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// OperationFunc is the signature for all async operation implementations.
type OperationFunc func(ctx context.Context, progress ProgressReporter) error

// ProgressReporter allows operations to report progress and check cancellation.
type ProgressReporter interface {
	UpdateProgress(current, total int, message string) error
	Log(level, message string, details *string) error
	IsCanceled() bool
}

// LoggerFromReporter adapts a ProgressReporter to the logger.Logger interface
// that the long-running services (scanner, organizer, reconcile, iTunes import)
// take, so their UpdateProgress calls reach the operations registry.
//
// This carries the ops registry's liveness signal. The watchdog
// (internal/operations/registry/watchdog.go) cancels any op that goes
// ProgressTimeout — 5 minutes by default — without a progress stamp, and when an
// op has NEVER stamped one it measures from StartedAt instead. An operation that
// cannot report progress is therefore not merely un-observable: it is killed at
// the five-minute mark however healthy it is, and its own Timeout (4h for
// library.scan) never gets to apply.
//
// From the BridgeQueue elimination (2026-05-11) until 2026-08-16 this function
// was a stub that discarded its argument:
//
//	func LoggerFromReporter(_ ProgressReporter) logger.Logger {
//	    return logger.New("operation")
//	}
//
// The signature still satisfied every caller, so nothing failed to compile and
// nothing failed loudly. StandardLogger.UpdateProgress has an empty body, so the
// scanner's three progress calls (internal/scanner/service.go:324, :366, :465)
// ran, cost nothing and reported nothing. All eight ops built on this helper —
// library.scan, library.import, library.organize, itunes.import, itunes.sync,
// reconcile preview, folder autoscan and maintenance reconcile — were blind, and
// each one that ran longer than five minutes was cancelled mid-flight. Two were
// diagnosed as problems with the op itself and worked around locally
// (internal/plugins/maintenance/dedupe_book_file_rows.go raised ProgressTimeout
// to 30m; internal/organizer/service.go stopped re-taking a 20-minute backup);
// both comments describe an operation that was, in their words, "in fact working
// the whole time". Those workarounds stay — they are independently worthwhile —
// but they are now belt-and-braces rather than load-bearing.
//
// A nil reporter yields a plain logger, so callers that log before they have a
// reporter keep working.
func LoggerFromReporter(r ProgressReporter) logger.Logger {
	return &reporterLogger{Logger: logger.New("operation"), reporter: r}
}

// reporterLogger is a logger.Logger that forwards progress to a
// ProgressReporter and delegates everything else to the logger it wraps.
//
// It embeds the logger.Logger INTERFACE rather than *logger.StandardLogger so
// that With delegates to the wrapped logger's own implementation, preserving its
// subsystem prefix, minimum stdout level and activity-log writer instead of
// reconstructing them here.
type reporterLogger struct {
	logger.Logger
	reporter ProgressReporter
}

// UpdateProgress forwards to the reporter, which stamps the registry's liveness
// clock. A reporter error is logged rather than returned: logger.Logger's
// UpdateProgress has no error result, and a failed progress write must never
// abort the work whose progress it describes.
func (l *reporterLogger) UpdateProgress(current, total int, message string) {
	if l.reporter == nil {
		return
	}
	if err := l.reporter.UpdateProgress(current, total, message); err != nil {
		l.Logger.Warn("progress update failed: %v", err)
	}
}

// With returns a child logger that KEEPS the reporter.
//
// Without this override the promoted method would return the wrapped logger's
// child — a plain logger with no reporter — and progress would silently stop at
// the first .With() call. That is not a theoretical concern: the scanner hands
// log.With("scanner") to ScanDirectoryParallel and ProcessBooksParallel
// (internal/scanner/service.go:338, :389), which is the longest-running stretch
// of a scan and exactly the window in which the watchdog fires.
func (l *reporterLogger) With(subsystem string) logger.Logger {
	return &reporterLogger{Logger: l.Logger.With(subsystem), reporter: l.reporter}
}

// IsCanceled deliberately still delegates to the wrapped logger, which answers
// false, rather than to the reporter.
//
// Forwarding it is a separate behavioural change from restoring progress:
// internal/scanner/service.go:190, internal/organizer/service.go:897 and :1082
// and internal/reconcile/reconcile.go:597 all guard on it, and those branches
// have been unreachable for three months. Cancellation already works through
// ctx, which every one of these services honours, so nothing is broken by
// leaving this alone — but turning four never-exercised early-return paths on in
// the same change that unblocks production scanning would make a bad first run
// impossible to bisect. Tracked in todo.d/20260816-logger-iscanceled-forwarding.md.

// AttrReporter is implemented by ProgressReporters that can persist structured
// attributes on a log line. The v2 registry adapter does (registry.Reporter.Log
// takes slog attrs); ProgressReporter.Log itself only carries a details string.
type AttrReporter interface {
	LogAttrs(level slog.Level, message string, attrs ...slog.Attr) error
}

// LogAttrs implements logger.AttrLogger, so a service handed this logger can
// write a line whose attributes land in the operation's own log as fields --
// the scanner's per-file failure lines (file_path / stage / reason) are the
// reason it exists. Plain Warn/Error on this logger reach stdout only.
//
// The line is always written to stdout as well. A reporter without LogAttrs
// gets the attributes as a JSON details payload rather than losing them, and a
// failed write is reported on stdout, never dropped silently.
func (l *reporterLogger) LogAttrs(level slog.Level, msg string, attrs ...slog.Attr) {
	logger.LogAtLevel(l.Logger, level, "%s", msg+logger.FormatAttrs(attrs))
	if l.reporter == nil {
		return
	}
	var err error
	if ar, ok := l.reporter.(AttrReporter); ok {
		err = ar.LogAttrs(level, msg, attrs...)
	} else {
		details := attrsDetailsJSON(attrs)
		err = l.reporter.Log(reporterLevelName(level), msg, &details)
	}
	if err != nil {
		l.Logger.Warn("operation log write failed for %q: %v", msg, err)
	}
}

// reporterLevelName maps an slog level onto ProgressReporter.Log's level names.
func reporterLevelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	case level >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

func attrsDetailsJSON(attrs []slog.Attr) string {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value.Any()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return logger.FormatAttrs(attrs)
	}
	return string(b)
}
