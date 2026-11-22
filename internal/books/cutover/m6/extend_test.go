// file: internal/books/cutover/m6/extend_test.go
// version: 1.0.0
// guid: b17e4d93-0c58-4a2f-8e36-d9f1a5c70e24
// last-edited: 2026-11-22

package m6_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// TestVerifyAsksForStartTimePerFamilyProbe: M6's Verify is one seek per
// drained family range, and each is a progress unit the runner turns into a
// request for start time: one request before Verify plus one per range.
func TestVerifyAsksForStartTimePerFamilyProbe(t *testing.T) {
	r := newRig(t, fromCorpus("stage1"), afterM2)
	if _, err := r.run(t); err != nil {
		t.Fatalf("boot: %v\n%s", err, r.logs.String())
	}
	verify := 0
	for _, s := range r.extended {
		if strings.HasSuffix(s, " verify") {
			verify++
		}
	}
	if want := 1 + len(m6.SourceRanges()); verify != want {
		t.Fatalf("verify requests = %d, want %d (before Verify and one per drained range): %q", verify, want, r.extended)
	}
}
