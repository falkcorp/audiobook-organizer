// file: internal/database/aggregate_caller_ext_test.go
// version: 1.0.0
// guid: 8f31d62c-0b74-4e95-a1d8-73c6f905e2b4
// last-edited: 2026-08-12

// Package database_test holds the caller-attribution tests that CANNOT live in
// package database — see the comment on AggCallerExportedForTest for why an
// in-package test cannot observe the behaviour under test.
package database_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestAggregateCallerNamesTheExternalCaller is the core assertion: a caller
// outside package database is reported by name and line.
func TestAggregateCallerNamesTheExternalCaller(t *testing.T) {
	got := database.AggCallerExportedForTest()

	const wantFunc = "internal/database_test.TestAggregateCallerNamesTheExternalCaller"
	if !strings.HasPrefix(got, wantFunc+":") {
		t.Fatalf("aggregateCaller() = %q, want it to start with %q — the nearest "+
			"frame outside package database is this test function", got, wantFunc+":")
	}
	// A line number must be present and non-zero, otherwise the value is useless
	// for locating the originator in source.
	line := strings.TrimPrefix(got, wantFunc+":")
	if line == "" || line == "0" {
		t.Fatalf("aggregateCaller() = %q, want a non-zero source line after the colon", got)
	}
}

// TestAggregateCallerSkipsIntermediateDatabaseFrames is the DISCRIMINATING case.
//
// The previous test would still pass if aggregateCaller simply reported
// runtime.Caller(1) with no skip logic at all, because the test calls straight
// into the package. This one routes through an extra in-package frame — the same
// shape as the real notifyBookFileChange -> RecomputeBookAggregates chain — and
// requires the answer to be UNCHANGED. Naive caller-reporting fails here.
func TestAggregateCallerSkipsIntermediateDatabaseFrames(t *testing.T) {
	got := database.AggCallerViaStoreFrameForTest()

	const wantFunc = "internal/database_test.TestAggregateCallerSkipsIntermediateDatabaseFrames"
	if !strings.HasPrefix(got, wantFunc+":") {
		t.Fatalf("aggregateCaller() through an intermediate database frame = %q, "+
			"want it to start with %q — every frame inside package database must be "+
			"skipped so the reported caller is the subsystem that drove the write, "+
			"not the store's own plumbing", got, wantFunc+":")
	}
	if strings.Contains(got, "/internal/database.") {
		t.Fatalf("aggregateCaller() = %q, want no frame from package database itself", got)
	}
}

// TestAggregateCallerReportsRepoRelativePackagePath pins the log-field shape.
// The module prefix must be stripped (it repeats on every emitted line) but the
// repo-relative package path must survive, or the value stops being greppable
// against real source paths.
func TestAggregateCallerReportsRepoRelativePackagePath(t *testing.T) {
	got := database.AggCallerExportedForTest()

	if strings.Contains(got, "github.com/falkcorp/audiobook-organizer/") {
		t.Fatalf("aggregateCaller() = %q, want the module prefix trimmed", got)
	}
	if !strings.HasPrefix(got, "internal/") {
		t.Fatalf("aggregateCaller() = %q, want a repo-relative package path "+
			"beginning with \"internal/\"", got)
	}
}
