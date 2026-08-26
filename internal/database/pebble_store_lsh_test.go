// file: internal/database/pebble_store_lsh_test.go
// version: 1.0.0
// guid: 4c5d6e7f-8091-a2b3-c4d5-e6f708192a3b
// last-edited: 2026-05-30

package database

import (
	"context"
	"encoding/binary"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func newPebbleStoreForLSH(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "lsh-db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func synthRaw(seed int64, frames int) []byte {
	rng := rand.New(rand.NewSource(seed))
	raw := make([]byte, frames*4)
	for i := range frames {
		binary.LittleEndian.PutUint32(raw[i*4:], rng.Uint32())
	}
	return raw
}

func flipBits(raw []byte, pctBits int, seed int64) []byte {
	flipped := make([]byte, len(raw))
	copy(flipped, raw)
	totalBits := len(flipped) * 8
	toFlip := totalBits * pctBits / 100
	rng := rand.New(rand.NewSource(seed))
	for range toFlip {
		b := rng.Intn(totalBits)
		flipped[b/8] ^= 1 << uint(b%8)
	}
	return flipped
}

func mustInsertBookFile(t *testing.T, store *PebbleStore, id string, fp []byte) {
	t.Helper()
	bf := &BookFile{
		ID:                  id,
		BookID:              "book-" + id,
		FilePath:            "/tmp/" + id + ".mp3",
		AcoustIDFingerprint: fp,
	}
	if err := store.CreateBookFile(bf); err != nil {
		t.Fatalf("CreateBookFile %s: %v", id, err)
	}
}

func TestPebbleStoreLSH_LookupReturnsSelf(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	fp := synthRaw(1, 57600)
	mustInsertBookFile(t, store, "a", fp)

	cands, err := store.LookupAcoustIDCandidates(fp, 50)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(cands) != 1 || cands[0] != "a" {
		t.Fatalf("expected [a], got %v", cands)
	}
}

func TestPebbleStoreLSH_NearDupFoundUnrelatedFiltered(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	fp := synthRaw(42, 57600)
	mustInsertBookFile(t, store, "a", fp)
	mustInsertBookFile(t, store, "near", flipBits(fp, 5, 0xfeed)) // ~5% bit-flip
	mustInsertBookFile(t, store, "far", synthRaw(99, 57600))      // unrelated

	cands, err := store.LookupAcoustIDCandidates(fp, 50)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// Both a (perfect) and near (5% flip) should appear; far should not.
	foundNear := false
	for _, c := range cands {
		if c == "far" {
			t.Fatalf("unrelated fp surfaced as candidate: %v", cands)
		}
		if c == "near" {
			foundNear = true
		}
	}
	if !foundNear {
		t.Fatalf("expected near-dup in candidates, got %v", cands)
	}
}

func TestPebbleStoreLSH_UpdateBookFileSwapsIndex(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	fpOld := synthRaw(1, 57600)
	mustInsertBookFile(t, store, "x", fpOld)

	fpNew := synthRaw(2, 57600)
	updated := &BookFile{
		ID:                  "x",
		BookID:              "book-x",
		FilePath:            "/tmp/x.mp3",
		AcoustIDFingerprint: fpNew,
	}
	if err := store.UpdateBookFile("x", updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Lookup with the new fp finds it; lookup with the old fp doesn't.
	cands, _ := store.LookupAcoustIDCandidates(fpNew, 50)
	if len(cands) != 1 || cands[0] != "x" {
		t.Fatalf("new fp lookup: expected [x], got %v", cands)
	}
	stale, _ := store.LookupAcoustIDCandidates(fpOld, 50)
	for _, c := range stale {
		if c == "x" {
			t.Fatalf("old fp keys not purged: still finds %s", c)
		}
	}
}

func TestPebbleStoreLSH_ClearAllWipesIndex(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	for i := range 3 {
		id := string(rune('a' + i))
		mustInsertBookFile(t, store, id, synthRaw(int64(i+1), 57600))
	}

	cleared, total, err := store.ClearAllAcoustIDFingerprints(context.Background(), 100, nil)
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if cleared != 3 || total != 3 {
		t.Fatalf("expected cleared=3 total=3, got cleared=%d total=%d", cleared, total)
	}

	// Verify the LSH key prefixes are empty.
	for _, prefix := range [][]byte{[]byte(lshKeyPrefix), []byte(lshMetaKeyPrefix)} {
		iter, err := store.db.NewIter(&pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: prefixEnd(prefix),
		})
		if err != nil {
			t.Fatalf("iter %s: %v", prefix, err)
		}
		iter.First()
		if iter.Valid() {
			t.Fatalf("expected %s prefix empty after ClearAll, found key %x", prefix, iter.Key())
		}
		_ = iter.Close()
	}
}

func TestPebbleStoreLSH_HasLSHIndex(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	mustInsertBookFile(t, store, "with", synthRaw(7, 57600))
	mustInsertBookFile(t, store, "without", nil)

	if !store.HasLSHIndex("with") {
		t.Errorf("HasLSHIndex(with) = false, want true")
	}
	if store.HasLSHIndex("without") {
		t.Errorf("HasLSHIndex(without) = true, want false")
	}
}
