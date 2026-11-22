// file: core/audit/highwater_test.go
// version: 0.1.0
// guid: a4d7e3b1-5c92-4e08-9f16-2b8a0d6c7e35
// last-edited: 2026-11-22

package audit

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
)

// recordSeqs is the seq of every record row in e's keyspace.
func recordSeqs(t *testing.T, e *env) []uint64 {
	t.Helper()
	var out []uint64
	for kv, err := range e.ks.Scan(context.Background(), storage.ScanOptions{Start: recPrefix(), End: prefixEndOf(recPrefix())}) {
		if err != nil {
			t.Fatal(err)
		}
		n, err := seqOfAnyKey(string(kv.Key))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func prefixEndOf(p []byte) []byte {
	end := append([]byte(nil), p...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

// TestStoreHighWaterBoundsRecordsWrittenAfterIt (05-backup §5B.3.5 step 4): the
// records the store writes after HighWater all have a seq above it, and the
// records written before it are at or below it, though the live lease (8)
// still held unissued seqs below the stored mark when it was read. The
// restore hook copies what is above the boundary, so a record a checkpoint
// taken after the read lacks is never below it.
func TestStoreHighWaterBoundsRecordsWrittenAfterIt(t *testing.T) {
	e := bootEnv(t, vfs.NewMem(), t.TempDir()+"/hw.pebble", 8)
	s := newStoreOn(t, e, Options{}, Deps{})
	writeN(t, s, 3)
	before := recordSeqs(t, e)
	if len(before) != 3 {
		t.Fatalf("%d records before, want 3", len(before))
	}
	hw, err := s.HighWater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range before {
		if n > hw {
			t.Fatalf("record %d written before HighWater is above it (%d)", n, hw)
		}
	}
	writeN(t, s, 12)
	all := recordSeqs(t, e)
	if len(all) != 15 {
		t.Fatalf("%d records, want 15", len(all))
	}
	above := 0
	for _, n := range all {
		if n > hw {
			above++
		}
	}
	if above != 12 {
		t.Fatalf("%d records are above the boundary %d, want the 12 written after it (seqs %v)", above, hw, all)
	}
}
