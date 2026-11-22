// file: internal/books/cutover/m4/extend_test.go
// version: 1.0.0
// guid: 8d3b6f12-47ac-4e9d-b5a0-1e72c9f4d386
// last-edited: 2026-11-22

package m4_test

import (
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
)

// TestVerifyAsksForStartTimePerFamilyProbe: M4's Verify is one seek per
// source family, and each seek is a progress unit the runner turns into a
// request for start time (a seek across the tombstones the step just left can
// itself be long): one request before Verify plus one per family range.
func TestVerifyAsksForStartTimePerFamilyProbe(t *testing.T) {
	id := runID(1)
	done := epoch.Add(time.Hour)
	r := newRig(t, withRuns(nil, runSpec{id: id, def: "dedup.book-scan", st: "succeeded", completed: new(done), started: new(epoch.Add(time.Minute)),
		trigger: "scheduled", priority: 2, evts: 1}))
	if _, err := r.run(t); err != nil {
		t.Fatalf("boot: %v\n%s", err, r.logs)
	}
	verify := 0
	for _, s := range r.extended {
		if strings.HasSuffix(s, " verify") {
			verify++
		}
	}
	if want := 1 + len(m4.SourceRanges()); verify != want {
		t.Fatalf("verify requests = %d, want %d (before Verify and one per source family): %q", verify, want, r.extended)
	}
}
