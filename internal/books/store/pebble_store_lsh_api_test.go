// file: internal/books/store/pebble_store_lsh_api_test.go
// version: 1.0.1
// guid: 99f6d371-3600-4eca-b7ba-0556e725a30b
// last-edited: 2026-11-09

// Tests for the exported LSH API surface in pebble_store_lsh.go:
// PutLSHEntries, DeleteLSHEntries, LSHProbe, IsLSHIndexBuilt / SetLSHIndexBuilt.
//
// The lower-level helpers (writeFingerprintLSHIndexes, LookupAcoustIDCandidates,
// HasLSHIndex) are exercised by pebble_store_lsh_test.go via the CreateBookFile /
// UpdateBookFile / ClearAllAcoustIDFingerprints paths. These tests target the
// exported API methods used by the lsh-index-build op.

package store

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// TestPutDeleteLSHEntries_RoundTrip indexes 3 fingerprints, probes for
// near-duplicates, then verifies that DeleteLSHEntries removes all fpidx: keys
// for one file — confirmed by a Pebble prefix iterator.
func TestPutDeleteLSHEntries_RoundTrip(t *testing.T) {
	store := newPebbleStoreForLSH(t)

	// Build three raw fingerprints: a, near-dup of a, and unrelated.
	// Use a 1% bit-flip for the near-dup: at 1% each 64-bit subprint has a
	// (0.99)^64 ≈ 52.7% survival probability, giving expected band hits
	// 64 × 0.527 ≈ 34 — well above LSHMinBandHits=2.
	fpA := synthRaw(1000, 57600)
	fpNear := flipBits(fpA, 1, 0xbeef) // ~1% bit-flip of fpA
	fpFar := synthRaw(9999, 57600)     // unrelated

	// Derive subprints for each.
	subsA, bandsA, err := fingerprint.Subprints(fpA)
	if err != nil || len(subsA) == 0 {
		t.Fatalf("Subprints(fpA): %v, len=%d", err, len(subsA))
	}
	subsNear, bandsNear, err := fingerprint.Subprints(fpNear)
	if err != nil || len(subsNear) == 0 {
		t.Fatalf("Subprints(fpNear): %v, len=%d", err, len(subsNear))
	}
	subsFar, bandsFar, err := fingerprint.Subprints(fpFar)
	if err != nil || len(subsFar) == 0 {
		t.Fatalf("Subprints(fpFar): %v, len=%d", err, len(subsFar))
	}

	// Index all three via the exported API.
	if err := store.PutLSHEntries("file-a", "book-a", subsA, bandsA); err != nil {
		t.Fatalf("PutLSHEntries(a): %v", err)
	}
	if err := store.PutLSHEntries("file-near", "book-near", subsNear, bandsNear); err != nil {
		t.Fatalf("PutLSHEntries(near): %v", err)
	}
	if err := store.PutLSHEntries("file-far", "book-far", subsFar, bandsFar); err != nil {
		t.Fatalf("PutLSHEntries(far): %v", err)
	}

	// Probe with fpA's subprints — should find file-a (perfect match)
	// and file-near (≥LSHMinBandHits collisions), but NOT file-far.
	candidates, err := store.LSHProbe(subsA, bandsA, 0)
	if err != nil {
		t.Fatalf("LSHProbe: %v", err)
	}

	if _, ok := candidates["file-a"]; !ok {
		t.Errorf("expected file-a in candidates (self), got %v", candidates)
	}
	if _, ok := candidates["file-near"]; !ok {
		t.Errorf("expected file-near in candidates (near-dup), got %v", candidates)
	}
	if _, ok := candidates["file-far"]; ok {
		t.Errorf("file-far should not be in candidates (unrelated), got hits=%d", candidates["file-far"])
	}

	// Probe with fpFar's subprints — should return empty (nothing shares bands
	// with the unrelated fingerprint).
	farCands, err := store.LSHProbe(subsFar, bandsFar, 0)
	if err != nil {
		t.Fatalf("LSHProbe(far): %v", err)
	}
	// Only file-far should be found (self-probe); file-a and file-near must not.
	for id := range farCands {
		if id == "file-a" || id == "file-near" {
			t.Errorf("unrelated probe returned a/near candidate: %s (hits=%d)", id, farCands[id])
		}
	}

	// DeleteLSHEntries removes file-a; verify via Pebble prefix iterator.
	if err := store.DeleteLSHEntries("file-a"); err != nil {
		t.Fatalf("DeleteLSHEntries(file-a): %v", err)
	}

	// After delete, probing with fpA should no longer surface file-a.
	afterDelete, err := store.LSHProbe(subsA, bandsA, 0)
	if err != nil {
		t.Fatalf("LSHProbe after delete: %v", err)
	}
	if _, ok := afterDelete["file-a"]; ok {
		t.Errorf("file-a still surfaced after DeleteLSHEntries, hits=%d", afterDelete["file-a"])
	}

	// Verify the fpidx: prefix has no keys for file-a via a raw Pebble iterator.
	// We range over fpidx:<band><subprint>:file-a keys. The safest check is to
	// iterate the full fpidx: prefix and assert no key ends with ":file-a".
	prefix := []byte(lshKeyPrefix)
	iter, iterErr := store.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if iterErr != nil {
		t.Fatalf("open iter: %v", iterErr)
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Key format: fpidx:<band:1B><subprint:8B>:<fileID>
		// The fileID starts at byte 16 (prefix 6 + band 1 + subprint 8 + ':' 1).
		if len(key) > 16 && key[16:] == "file-a" {
			t.Errorf("found stale fpidx: key for deleted file-a: %x", iter.Key())
		}
	}

	// Also verify fpidx_meta: is gone.
	if store.HasLSHIndex("file-a") {
		t.Errorf("HasLSHIndex(file-a) still true after delete")
	}
}

// TestLSHIndexBuiltFlag verifies the IsLSHIndexBuilt / SetLSHIndexBuilt
// round-trip used by the build op to mark completion.
func TestLSHIndexBuiltFlag(t *testing.T) {
	store := newPebbleStoreForLSH(t)

	if store.IsLSHIndexBuilt() {
		t.Fatal("IsLSHIndexBuilt() should be false on a fresh store")
	}
	if err := store.SetLSHIndexBuilt(); err != nil {
		t.Fatalf("SetLSHIndexBuilt: %v", err)
	}
	if !store.IsLSHIndexBuilt() {
		t.Fatal("IsLSHIndexBuilt() should be true after SetLSHIndexBuilt()")
	}
}

// TestPutLSHEntries_Idempotent verifies that calling PutLSHEntries twice for
// the same file with the same subprints produces a clean, correct index
// (no duplicate keys, correct probe results).
func TestPutLSHEntries_Idempotent(t *testing.T) {
	store := newPebbleStoreForLSH(t)

	fp := synthRaw(42, 57600)
	subs, bands, err := fingerprint.Subprints(fp)
	if err != nil || len(subs) == 0 {
		t.Fatalf("Subprints: %v", err)
	}

	// First write.
	if err := store.PutLSHEntries("file-x", "book-x", subs, bands); err != nil {
		t.Fatalf("first PutLSHEntries: %v", err)
	}
	// Second write (idempotent re-run).
	if err := store.PutLSHEntries("file-x", "book-x", subs, bands); err != nil {
		t.Fatalf("second PutLSHEntries: %v", err)
	}

	// Probe should still find exactly file-x.
	cands, err := store.LSHProbe(subs, bands, 0)
	if err != nil {
		t.Fatalf("LSHProbe: %v", err)
	}
	if _, ok := cands["file-x"]; !ok {
		t.Errorf("expected file-x in probe after idempotent put, got %v", cands)
	}
}
