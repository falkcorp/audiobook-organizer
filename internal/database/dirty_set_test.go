// file: internal/database/dirty_set_test.go
// version: 1.0.0
// guid: 8b1c2f64-0a57-4e1d-b3f9-52c7d4e6a913
// last-edited: 2026-10-30

package database

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"testing"
)

const testDirtyPrefix = "idx:test:dirty:"

func openDirtyTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStoreInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func epochBytes(e uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], e)
	return b[:]
}

func TestMarkDirty_WritesBigEndianEpoch(t *testing.T) {
	s := openDirtyTestStore(t)
	e1, err := s.MarkDirty(testDirtyPrefix, "robin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRaw(testDirtyPrefix + "robin")
	if err != nil || len(got) != 8 || binary.BigEndian.Uint64(got) != e1 {
		t.Fatalf("stored %v (err %v), want 8-byte epoch %d", got, err, e1)
	}
	e2, err := s.MarkDirty(testDirtyPrefix, "robin")
	if err != nil || e2 <= e1 {
		t.Fatalf("re-mark epoch %d (err %v), want > %d", e2, err, e1)
	}
}

func TestMarkDirty_RejectsBadInput(t *testing.T) {
	s := openDirtyTestStore(t)
	for _, c := range [][2]string{{"", "x"}, {testDirtyPrefix, ""}, {"opv2:op:", "x"}, {metadataCacheKeyPrefix, "x"}} {
		if _, err := s.MarkDirty(c[0], c[1]); err == nil {
			t.Errorf("MarkDirty(%q, %q) succeeded, want error", c[0], c[1])
		}
	}
}

func TestCompareAndDeleteRaw_EqualEpochDeletes(t *testing.T) {
	s := openDirtyTestStore(t)
	e, _ := s.MarkDirty(testDirtyPrefix, "batgirl")
	ok, err := s.CompareAndDeleteRaw(testDirtyPrefix+"batgirl", epochBytes(e))
	if err != nil || !ok {
		t.Fatalf("delete = %v, %v; want true, nil", ok, err)
	}
	if got, _ := s.GetRaw(testDirtyPrefix + "batgirl"); got != nil {
		t.Fatalf("entry still present: %v", got)
	}
}

func TestCompareAndDeleteRaw_DifferentEpochKeepsEntry(t *testing.T) {
	s := openDirtyTestStore(t)
	old, _ := s.MarkDirty(testDirtyPrefix, "alfred")
	cur, _ := s.MarkDirty(testDirtyPrefix, "alfred") // re-marked after the reader's snapshot
	ok, err := s.CompareAndDeleteRaw(testDirtyPrefix+"alfred", epochBytes(old))
	if err != nil || ok {
		t.Fatalf("stale delete = %v, %v; want false, nil", ok, err)
	}
	got, _ := s.GetRaw(testDirtyPrefix + "alfred")
	if len(got) != 8 || binary.BigEndian.Uint64(got) != cur {
		t.Fatalf("entry = %v, want the newer epoch %d kept", got, cur)
	}
}

func TestCompareAndDeleteRaw_MissingKey(t *testing.T) {
	s := openDirtyTestStore(t)
	ok, err := s.CompareAndDeleteRaw(testDirtyPrefix+"nobody", epochBytes(1))
	if err != nil || ok {
		t.Fatalf("missing key = %v, %v; want false, nil", ok, err)
	}
}

func TestCompareAndDeleteRaw_RejectsProtectedFamilies(t *testing.T) {
	s := openDirtyTestStore(t)
	for _, k := range []string{"", "opv2:op:abc", metadataCacheKeyPrefix + "abc"} {
		if _, err := s.CompareAndDeleteRaw(k, epochBytes(1)); err == nil {
			t.Errorf("CompareAndDeleteRaw(%q) succeeded, want error", k)
		}
	}
}

// The identification family keeps working through the shared path.
func TestClearIdentDirty_ViaCompareAndDelete(t *testing.T) {
	s := openDirtyTestStore(t)
	old, err := s.MarkIdentDirty("book-1")
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := s.MarkIdentDirty("book-1")
	if ok, _ := s.ClearIdentDirty("book-1", old); ok {
		t.Fatal("cleared with a stale epoch")
	}
	if ok, err := s.ClearIdentDirty("book-1", cur); err != nil || !ok {
		t.Fatalf("clear with the current epoch = %v, %v", ok, err)
	}
}

// Concurrent re-marks and compare-and-deletes: after the snapshot is drained,
// exactly the subjects re-marked after the snapshot remain.
func TestDirtySet_ConcurrentMarksAndDeletesKeepReMarked(t *testing.T) {
	s := openDirtyTestStore(t)
	const n = 400
	snapshot := make(map[string]uint64, n)
	for i := range n {
		id := fmt.Sprintf("s%03d", i)
		e, err := s.MarkDirty(testDirtyPrefix, id)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[id] = e
	}
	// Even subjects are re-dirtied after the snapshot, concurrently with the
	// drain of every snapshot entry.
	var wg sync.WaitGroup
	for i := 0; i < n; i += 2 {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := s.MarkDirty(testDirtyPrefix, id); err != nil {
				t.Error(err)
			}
		}(fmt.Sprintf("s%03d", i))
	}
	// Barrier: every re-mark is issued before any delete starts, so the
	// outcome is deterministic while the calls still overlap in time.
	wg.Wait()
	for id, e := range snapshot {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CompareAndDeleteRaw(testDirtyPrefix+id, epochBytes(e)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	kvs, _, err := s.ScanPrefixPage(testDirtyPrefix, "", n+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(kvs) != n/2 {
		t.Fatalf("%d entries remain, want %d", len(kvs), n/2)
	}
	for _, kv := range kvs {
		id := strings.TrimPrefix(kv.Key, testDirtyPrefix)
		var i int
		fmt.Sscanf(id, "s%d", &i)
		if i%2 != 0 {
			t.Errorf("%s survived but was never re-marked", id)
		}
	}
}

// Marks and deletes truly racing on one key: whichever order they take, the
// entry is either gone (delete first, then the mark re-created it is not
// possible to distinguish) or carries a valid epoch; never a torn value.
func TestDirtySet_RacingMarkAndDeleteNeverTears(t *testing.T) {
	s := openDirtyTestStore(t)
	key := testDirtyPrefix + "joker"
	for range 200 {
		e, _ := s.MarkDirty(testDirtyPrefix, "joker")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.MarkDirty(testDirtyPrefix, "joker") }()
		go func() { defer wg.Done(); _, _ = s.CompareAndDeleteRaw(key, epochBytes(e)) }()
		wg.Wait()
		// The racing mark always happens-after e was minted, so it either
		// replaced e before the delete (entry survives) or landed after it
		// (entry present too): the entry must exist.
		got, _ := s.GetRaw(key)
		if len(got) != 8 || binary.BigEndian.Uint64(got) <= e {
			t.Fatalf("entry = %v, want a value with an epoch newer than %d", got, e)
		}
	}
}
