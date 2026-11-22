// file: internal/books/cutover/m5/extend_test.go
// version: 1.0.0
// guid: 3f9a07c5-b216-4d8e-9c41-6a5e2d80b7f3
// last-edited: 2026-11-22

package m5_test

import (
	"strings"
	"testing"
)

// TestVerifyAsksForStartTimeBeforeItsProbe: M5's Verify is one seek over the
// syslog: range, a progress unit the runner turns into a request for start
// time: one request before Verify plus one for the probe.
func TestVerifyAsksForStartTimeBeforeItsProbe(t *testing.T) {
	r := newRig(t, "stage1")
	if _, err := r.run(t); err != nil {
		t.Fatalf("boot: %v\n%s", err, r.logs)
	}
	verify := 0
	for _, s := range r.extended {
		if strings.HasSuffix(s, " verify") {
			verify++
		}
	}
	if verify != 2 {
		t.Fatalf("verify requests = %d, want 2 (before Verify and for its one probe): %q", verify, r.extended)
	}
}
