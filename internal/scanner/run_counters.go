// file: internal/scanner/run_counters.go
// version: 1.0.0
// guid: 9a4e1c7b-2d58-4f63-b8e0-5c1a7d3f9b26
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"sync/atomic"
)

// scanRunCounters counts, for ONE ProcessBooksParallel or ScanDirectoryParallel
// call, the outcomes its end-of-run summary reports. They ride on the ctx
// rather than living in package globals read as "value now minus value at
// start": two scans running at once (a scan plus an import, two roots) would
// each report the other's refusals, and a summary a concurrent run can inflate
// is not a count of this run.
type scanRunCounters struct {
	// ownershipLookupErr: scanned books not imported because a store read
	// failed while checking who owns their files.
	ownershipLookupErr atomic.Int64
	// ceilingRefused: books createBookFilesForBook gave no rows because they
	// claimed more than maxDirectoryBookFiles files.
	ceilingRefused atomic.Int64
	// stagedAppended / appendFailed: files appended to an already-imported
	// book (a staged download), and append attempts that failed.
	stagedAppended atomic.Int64
	appendFailed   atomic.Int64
	// oversizedGroups / oversizedFiles: same-title groups (and their files) in
	// oversized directories that were refused rather than imported.
	oversizedGroups atomic.Int64
	oversizedFiles  atomic.Int64
}

type scanRunCountersKey struct{}

// withScanRunCounters attaches a fresh set of counters to ctx for one run.
func withScanRunCounters(ctx context.Context) (context.Context, *scanRunCounters) {
	c := &scanRunCounters{}
	return context.WithValue(ctx, scanRunCountersKey{}, c), c
}

// scanRunCountersFrom returns the run's counters, or a throwaway set when the
// caller is not inside a run (a unit test calling a helper directly): the
// increments then go nowhere, which is what "no run to summarize" means.
func scanRunCountersFrom(ctx context.Context) *scanRunCounters {
	if ctx != nil {
		if c, ok := ctx.Value(scanRunCountersKey{}).(*scanRunCounters); ok && c != nil {
			return c
		}
	}
	return &scanRunCounters{}
}
