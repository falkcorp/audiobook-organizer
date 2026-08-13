// file: internal/database/export_aggregate_caller_test.go
// version: 1.0.0
// guid: 5a70c9d3-1e46-4b28-8f95-c2d0e6a3417b
// last-edited: 2026-08-12

package database

import (
	"strings"
	"testing"
)

// AggCallerExportedForTest exposes the unexported aggregateCaller to the
// EXTERNAL database_test package.
//
// WHY the indirection is necessary rather than merely convenient: aggregateCaller
// skips every frame belonging to this package. A test written in `package
// database` is itself such a frame, so calling it in-package always yields
// "database-internal" — the test would pass while proving nothing about how real
// callers are attributed. Only a test in `package database_test` (whose frames
// read ".../internal/database_test.", which the marker deliberately does not
// match) can observe the real behaviour.
func AggCallerExportedForTest() string {
	return aggregateCaller()
}

// AggCallerViaStoreFrameForTest calls aggregateCaller through an EXTRA frame in
// this package, mimicking the real notifyBookFileChange -> RecomputeBookAggregates
// chain. The external test asserts the answer is unchanged, which is what proves
// intervening database frames are skipped rather than reported.
func AggCallerViaStoreFrameForTest() string {
	return aggCallerInnerStoreFrameForTest()
}

func aggCallerInnerStoreFrameForTest() string {
	return aggregateCaller()
}

// TestAggregateCallerWalksPastDatabaseIntoTheRuntime pins what happens when
// there is no in-repo caller: the walk does NOT stop at the package boundary, it
// keeps going into whatever sits below.
//
// This test was originally written asserting "database-internal" and it FAILED,
// returning "testing.tRunner:2036". That result is correct and worth pinning: a
// goroutine stack never ends at a package boundary, so the "every frame in the
// budget is database" fallback is effectively unreachable. The realistic
// degenerate case in production is a bare `go store.UpdateBookFile(...)`, which
// reports "runtime.goexit:0" — an honest bucket meaning "no in-repo caller on
// the stack" rather than a filtered-away blank.
func TestAggregateCallerWalksPastDatabaseIntoTheRuntime(t *testing.T) {
	got := aggregateCaller()

	if strings.Contains(got, databasePkgMarker) {
		t.Fatalf("aggregateCaller() = %q, want no frame from package database itself", got)
	}
	// The nearest non-database frame above an in-package test is the testing
	// harness. Matching on the package rather than the exact function keeps this
	// from breaking if the Go runtime renames tRunner.
	if !strings.HasPrefix(got, "testing.") {
		t.Fatalf("aggregateCaller() from inside package database = %q, want a "+
			"\"testing.\" frame — the walk continues past this package into whatever "+
			"called it, and for an in-package test that is the test harness", got)
	}
}

// TestShortFuncNameTrimsModulePrefix guards the log-field formatting. If the
// module path ever changes, the prefix stops matching and every one of the
// 126,928 lines/scan grows by the full module path; this fails loudly instead.
func TestShortFuncNameTrimsModulePrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "method on a store type in another package",
			in:   "github.com/falkcorp/audiobook-organizer/internal/scanner.(*Service).ProcessBook",
			want: "internal/scanner.(*Service).ProcessBook",
		},
		{
			name: "plain function in a maintenance job",
			in:   "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.RunRecompute",
			want: "internal/maintenance/jobs.RunRecompute",
		},
		{
			name: "third-party frame keeps its full path",
			in:   "github.com/cockroachdb/pebble.(*DB).Set",
			want: "github.com/cockroachdb/pebble.(*DB).Set",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortFuncName(tc.in); got != tc.want {
				t.Fatalf("shortFuncName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
