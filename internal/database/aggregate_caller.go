// file: internal/database/aggregate_caller.go
// version: 1.0.0
// guid: 3c9e17ab-64d2-4f80-9153-8ad7be2f01c6
// last-edited: 2026-08-12

// Package database — caller attribution for aggregate recomputation.
//
// WHY this file exists:
//
//	A production log sample (2026-08-12) counted 126,928 "RecomputeBookAggregates
//	updated" lines across 5,595 distinct books — worst single book recomputed
//	1,189 times. Because each call re-reads every BookFile of the book, N
//	sequential inserts cost 1+2+...+N reads: 5,430,858 implied reads for 126,928
//	updates, a 42.8x amplification.
//
//	None of those 126,928 lines said WHO issued the write. There are thirteen
//	possible originators — eight packages calling RecomputeBookAggregates
//	directly, plus five BookFile mutators that reach it through
//	notifyBookFileChange — and the obvious suspect was ruled OUT: the scanner
//	writes via BatchUpsertBookFiles, which never calls notifyBookFileChange at
//	all. Without attribution the coalescing fix cannot be aimed, and more
//	importantly cannot be MEASURED afterwards.
//
// WHY a runtime stack walk instead of a caller parameter:
//
//	RecomputeBookAggregates is part of the BookWriter/BookStore/Store interfaces
//	and is mocked in eight generated files. Threading a caller string through the
//	signature would regenerate tens of thousands of lines of mock code for what is
//	purely a diagnostic. The stack already holds the answer.
//
// COST: runtime.Callers over a small fixed frame budget, evaluated only at the
// log sites that actually emit — roughly 1-2us per emitted line. Against the
// 126,928 lines above that totals well under a second across a four-hour scan.
package database

import (
	"runtime"
	"strconv"
	"strings"
)

const (
	// aggregateCallerDepth bounds the stack walk. The deepest real chain is
	// caller -> UpdateBookFile -> notifyBookFileChange -> RecomputeBookAggregates
	// -> log site, so 16 frames leaves generous headroom for wrappers and
	// inlining boundaries without allocating a large array on every call.
	aggregateCallerDepth = 16

	// databasePkgMarker matches any function defined in this package. Function
	// names from runtime look like
	// "github.com/falkcorp/audiobook-organizer/internal/database.(*PebbleStore).UpdateBookFile",
	// so the leading slash keeps this from matching a hypothetical
	// "somethingdatabase." package elsewhere.
	databasePkgMarker = "/internal/database."

	// modulePrefix is trimmed from reported names so the log field stays short
	// and greppable: "internal/scanner.(*Service).Process" rather than the full
	// module path repeated on every one of 126,928 lines.
	modulePrefix = "github.com/falkcorp/audiobook-organizer/"
)

// aggregateCaller returns "package.Func:line" for the nearest stack frame
// OUTSIDE this package — the code that actually asked for the write.
//
// Frames inside internal/database are skipped deliberately: naming
// notifyBookFileChange or UpdateBookFile as the caller would just restate what
// the log line already says. The interesting question is which subsystem drove
// the mutation, and that always lives one package up.
//
// When there is no in-repo caller the walk does NOT stop at the package
// boundary — a goroutine stack never ends there. It continues into whatever sits
// below, so the value degrades gracefully to a runtime frame rather than to
// nothing:
//
//   - a bare `go store.UpdateBookFile(...)` reports "runtime.goexit:0", an
//     honest bucket meaning "no in-repo caller was on the stack";
//   - an in-package test reports a "testing." frame.
//
// The "database-internal" return is therefore a backstop that requires more than
// aggregateCallerDepth consecutive frames in this package, which no current call
// chain produces. It exists so the function can never return an empty string.
// "unknown" is returned only if the stack cannot be walked at all.
func aggregateCaller() string {
	var pcs [aggregateCallerDepth]uintptr
	// Skip 2: runtime.Callers itself and aggregateCaller.
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return "unknown"
	}

	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if frame.Function == "" {
			break
		}
		if !strings.Contains(frame.Function, databasePkgMarker) {
			return shortFuncName(frame.Function) + ":" + strconv.Itoa(frame.Line)
		}
		if !more {
			break
		}
	}
	return "database-internal"
}

// shortFuncName strips the module path prefix from a fully-qualified runtime
// function name, leaving the repo-relative package path intact so the value is
// still unambiguous and still greppable against real source paths.
func shortFuncName(fn string) string {
	return strings.TrimPrefix(fn, modulePrefix)
}
