// file: internal/plugins/acoustid/worker_reason_test.go
// version: 1.0.0
// guid: 8d3f6b02-71a5-4c94-bd28-4e0a97c15f63
// last-edited: 2026-09-21

package acoustid

import (
	"strings"
	"testing"
)

// TestNormalizeWorkerReason_CoversEveryRejectionSite pins one bounded suffix
// per reason the worker can actually refuse a job with. The six literals below
// are copied from internal/fingerprint/workerclient/worker.go; if a new refusal
// is added there without a case here it lands in ":other", which is a bucket,
// not a diagnosis.
func TestNormalizeWorkerReason_CoversEveryRejectionSite(t *testing.T) {
	cases := map[string]string{
		`path: root books is not configured on this worker`: ":root_not_configured",
		`path: rel_b64 is not valid base64`:                 ":bad_rel_b64",
		`job has no windows`:                                ":no_windows",
		`path: not a regular file`:                          ":not_regular_file",
		`window tools missing: fpcalc`:                      ":tools_missing",
		`fingerprint too short`:                             ":too_short",
		``:                                                  "",
	}
	for in, want := range cases {
		if got := normalizeWorkerReason(in); got != want {
			t.Errorf("normalizeWorkerReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeWorkerReason_IsBounded is the property that matters: this value
// becomes a KEY in the deferral tally, so it must never carry the variable part
// of an error. Feeding it a thousand distinct paths must not produce a thousand
// distinct keys — that would give the map (and the progress line it prints)
// unbounded cardinality.
func TestNormalizeWorkerReason_IsBounded(t *testing.T) {
	seen := map[string]bool{}
	for i := range 1000 {
		seen[normalizeWorkerReason("path: /mnt/bigdata/books/x"+strings.Repeat("y", i%37)+"/f.m4b: no such file")] = true
		seen[normalizeWorkerReason("some unrecognised failure number "+strings.Repeat("z", i%29))] = true
	}
	if len(seen) > 4 {
		t.Errorf("reason keys must stay bounded, got %d distinct: %v", len(seen), seen)
	}
	for k := range seen {
		if len(k) > 32 {
			t.Errorf("reason suffix too long to sit in a progress line: %q", k)
		}
	}
}
