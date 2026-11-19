// file: internal/books/cutover/m6/step_test.go
// version: 1.0.0
// guid: 1d6f0a83-5c27-4b9e-8a14-e3b7c92d05f6
// last-edited: 2026-11-19

package m6_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// drained are the key families M6 removes from books' legacy range.
var drained = []string{
	m6.PrefixAIJob, m6.PrefixAIJobBatch, m6.PrefixAIJobPayload, m6.PrefixImportPath,
	m6.PrefixDailyBudget, m6.PrefixThrottle, m6.PrefixDirty, m6.KeyBlob, m6.KeyImportCounter,
}

func isDrained(key string) bool {
	for _, d := range drained {
		if key == d || (strings.HasSuffix(d, ":") || strings.HasSuffix(d, "_")) && strings.HasPrefix(key, d) {
			return true
		}
	}
	return false
}

// TestStepDrainsEveryRetiredClaim: after M6 every key in a retired claim of its
// Source is gone, the step is recorded done, its Verify passes, and every other
// key of the legacy range is byte-identical (I4) apart from the runner's own
// level-step bookkeeping and the rootscan: rows M6 writes.
func TestStepDrainsEveryRetiredClaim(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	before := r.before(t, fsys)
	s, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatalf("M6: %v", err)
	}
	if s.State != "done" {
		t.Fatalf("state %q, want done; logs:\n%s", s.State, r.logs.String())
	}
	if len(s.Verify.Failures) != 0 {
		t.Fatalf("verify failures: %v", s.Verify.Failures)
	}
	for k := range s.Legacy {
		if isDrained(k) {
			t.Errorf("drained key %s is still in the legacy range", k)
		}
	}
	for k, v := range before {
		if isDrained(k) {
			continue
		}
		got, ok := s.Legacy[k]
		if !ok {
			t.Errorf("bystander %s was removed", k)
			continue
		}
		if string(got) != string(v) && !strings.HasPrefix(k, "preference:") && !strings.HasPrefix(k, "counter:preference") {
			t.Errorf("bystander %s changed", k)
		}
	}
	var added []string
	for k := range s.Legacy {
		if _, ok := before[k]; !ok && !strings.HasPrefix(k, "preference:") {
			added = append(added, k)
		}
	}
	slices.Sort(added)
	for _, k := range added {
		if !strings.HasPrefix(k, m6.PrefixRootScan) {
			t.Errorf("M6 added %s to the legacy range; only rootscan: rows may be", k)
		}
	}
}
