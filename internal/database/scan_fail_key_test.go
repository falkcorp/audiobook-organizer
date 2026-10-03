// file: internal/database/scan_fail_key_test.go
// version: 1.0.0
// guid: 658c85c6-b9a7-44ba-92b4-452d9acc3868
// last-edited: 2026-10-03

package database

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// TestScanFailKeyIsStable pins the derivation. Every scan-fail counter already
// stored in production is keyed this way, so a change here would orphan them
// all: the old counters stop resetting and the new ones start from zero.
func TestScanFailKeyIsStable(t *testing.T) {
	const path = "/library/Author/Book/01.mp3"
	sum := sha256.Sum256([]byte(path))
	want := fmt.Sprintf("%x", sum[:8]) // the expression both former copies used
	if got := ScanFailKey(path); got != want {
		t.Fatalf("ScanFailKey(%q) = %q, want %q", path, got, want)
	}
	if len(want) != 16 {
		t.Fatalf("key length %d, want 16 hex chars", len(want))
	}
}
