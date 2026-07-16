// file: internal/reconcile/reconcile_hash_test.go
// version: 1.0.0
// guid: 5a9c2e74-8b13-4d6f-9027-1e4c8a35d9b2
// last-edited: 2026-07-16

package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// hashFilesConcurrent must index each hashable file, drop the highest-indexed
// file on a content collision (preserving the old sequential last-wins map
// build), and skip files that fail to hash. Run with -race to exercise the pool.
func TestHashFilesConcurrent(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.m4b", "content-A")
	b := writeFile(t, dir, "b.m4b", "content-B")
	// Two files with identical content hash to the same value → collision.
	c1 := writeFile(t, dir, "c1.m4b", "same-content")
	c2 := writeFile(t, dir, "c2.m4b", "same-content")
	missing := filepath.Join(dir, "does-not-exist.m4b") // hash error → skipped

	files := []string{a, b, c1, c2, missing}
	idx := hashFilesConcurrent(files, nil)

	ha, _ := scanner.ComputeSegmentFileHash(a)
	hb, _ := scanner.ComputeSegmentFileHash(b)
	hc, _ := scanner.ComputeSegmentFileHash(c1)

	if idx[ha] != a {
		t.Errorf("hash of a → %q, want %q", idx[ha], a)
	}
	if idx[hb] != b {
		t.Errorf("hash of b → %q, want %q", idx[hb], b)
	}
	// Collision: c1 and c2 share a hash; the higher index (c2) must win, matching
	// the former sequential `hashIndex[h] = fp` in index order.
	if idx[hc] != c2 {
		t.Errorf("collision winner = %q, want highest-index %q", idx[hc], c2)
	}
	// Three distinct hashes (a, b, shared-c); the missing file contributed none.
	if len(idx) != 3 {
		t.Errorf("index size = %d, want 3 (missing file skipped)", len(idx))
	}
}

func TestHashFilesConcurrent_Empty(t *testing.T) {
	if idx := hashFilesConcurrent(nil, nil); len(idx) != 0 {
		t.Fatalf("empty input → %d entries, want 0", len(idx))
	}
}

// The progress callback fires (at the ~every-100 cadence) and never races.
func TestHashFilesConcurrent_ProgressReported(t *testing.T) {
	dir := t.TempDir()
	files := make([]string, 0, 250)
	for i := range 250 {
		files = append(files, writeFile(t, dir, fmt.Sprintf("f%03d.m4b", i), fmt.Sprintf("content-%d", i)))
	}
	var maxDone int64
	idx := hashFilesConcurrent(files, func(done int) {
		for {
			cur := atomic.LoadInt64(&maxDone)
			if int64(done) <= cur || atomic.CompareAndSwapInt64(&maxDone, cur, int64(done)) {
				break
			}
		}
	})
	if len(idx) != 250 {
		t.Errorf("index size = %d, want 250", len(idx))
	}
	if atomic.LoadInt64(&maxDone) < 100 {
		t.Errorf("progress high-water = %d, want >= 100", maxDone)
	}
}
