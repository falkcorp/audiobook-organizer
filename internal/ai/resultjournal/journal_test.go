// file: internal/ai/resultjournal/journal_test.go
// version: 1.1.0
// guid: 5c28e392-3a6b-4bf2-9949-6f7efc1aedd2
// last-edited: 2026-09-19

package resultjournal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func openPebble(t *testing.T, dir string) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	return s
}

type result struct {
	Text string `json:"text"`
}

func TestComplete_IsDurableBeforeReturn(t *testing.T) {
	dir := t.TempDir()
	s := openPebble(t, dir)
	j, err := New(s, "whisper")
	if err != nil {
		t.Fatal(err)
	}
	key := ContentKey("clip-bytes", "model-a", "v1")
	if err := j.Complete(key, "http://a", "model-a", result{Text: "hello"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Simulated kill: close the store the instant Complete returned and reopen
	// it from disk. The entry must be there.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2 := openPebble(t, dir)
	t.Cleanup(func() { _ = s2.Close() })
	j2, _ := New(s2, "whisper")
	raw, ok, err := j2.Lookup(key)
	if err != nil || !ok {
		t.Fatalf("Lookup after reopen: ok=%v err=%v", ok, err)
	}
	var r result
	if err := json.Unmarshal(raw, &r); err != nil || r.Text != "hello" {
		t.Fatalf("result = %+v (%v), want hello", r, err)
	}
}

// failingKV fails every SetRaw; Complete must surface it, never report success
// for a result that was not persisted.
type failingKV struct{ database.RawKVStore }

func (failingKV) SetRaw(string, []byte) error { return errors.New("disk full") }

func TestComplete_WriteErrorIsReturned(t *testing.T) {
	j, _ := New(failingKV{}, "whisper")
	if err := j.Complete("k", "e", "m", result{Text: "x"}); err == nil {
		t.Fatal("Complete returned nil for a failed write")
	}
}

func TestLookup_MissAndKindIsolation(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	w, _ := New(s, "whisper")
	other, _ := New(s, "llm")
	if _, ok, err := w.Lookup("absent"); ok || err != nil {
		t.Fatalf("miss: ok=%v err=%v", ok, err)
	}
	if err := w.Complete("k", "e", "m", result{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := other.Lookup("k"); ok {
		t.Fatal("an entry of kind whisper was served to kind llm")
	}
}

func TestLookup_CorruptEntryIsAnError(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	j, _ := New(s, "whisper")
	if err := s.SetRaw("aijournal:whisper:bad", []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := j.Lookup("bad"); ok || err == nil {
		t.Fatalf("corrupt entry: ok=%v err=%v, want an error", ok, err)
	}
}

func TestNew_RejectsBadKind(t *testing.T) {
	for _, k := range []string{"", "a:b"} {
		if _, err := New(failingKV{}, k); err == nil {
			t.Errorf("kind %q accepted", k)
		}
	}
	if _, err := New(nil, "whisper"); err == nil {
		t.Error("nil store accepted")
	}
}

func TestContentKey_LengthPrefixed(t *testing.T) {
	if ContentKey("ab", "c") == ContentKey("a", "bc") {
		t.Fatal("part boundaries collide")
	}
	if ContentKey("a", "b") != ContentKey("a", "b") {
		t.Fatal("not deterministic")
	}
}

func TestPrune_DeletesOldAndCorruptKeepsFreshAndOtherKinds(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	j, _ := New(s, "whisper")
	other, _ := New(s, "llm")

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return base }
	other.now = j.now
	_ = j.Complete("old", "e", "m", result{Text: "old"})
	_ = other.Complete("old-other-kind", "e", "m", result{Text: "x"})
	_ = s.SetRaw("aijournal:whisper:corrupt", []byte("nope"))

	j.now = func() time.Time { return base.Add(10 * 24 * time.Hour) }
	_ = j.Complete("fresh", "e", "m", result{Text: "fresh"})

	n, err := j.Prune(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2 (old + corrupt)", n)
	}
	if _, ok, _ := j.Lookup("old"); ok {
		t.Error("old entry survived")
	}
	if _, ok, _ := j.Lookup("fresh"); !ok {
		t.Error("fresh entry pruned")
	}
	if _, ok, _ := other.Lookup("old-other-kind"); !ok {
		t.Error("Prune of kind whisper deleted a kind llm entry")
	}
}

func TestJournal_ConcurrentUse(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	j, _ := New(s, "whisper")
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			k := fmt.Sprintf("k%d", i%8)
			if err := j.Complete(k, "e", "m", result{Text: k}); err != nil {
				t.Error(err)
			}
			if _, ok, err := j.Lookup(k); !ok || err != nil {
				t.Errorf("Lookup %s after own Complete: ok=%v err=%v", k, ok, err)
			}
		})
	}
	wg.Wait()
}

// countingKV wraps a real store and records how Prune drives it, so the
// bounded-memory contract (page size honoured, ScanPrefix never used, deletes
// batched no wider than a page) is asserted rather than assumed.
type countingKV struct {
	*database.PebbleStore
	maxLimit      int
	pages         int
	batches       int
	maxBatch      int
	fullScans     int
	afterEachPage func(page int)
}

func (c *countingKV) ScanPrefix(p string) ([]database.KVPair, error) {
	c.fullScans++
	return c.PebbleStore.ScanPrefix(p)
}

func (c *countingKV) ScanPrefixPage(p, after string, limit int) ([]database.KVPair, string, error) {
	c.pages++
	c.maxLimit = max(c.maxLimit, limit)
	pairs, next, err := c.PebbleStore.ScanPrefixPage(p, after, limit)
	if c.afterEachPage != nil {
		c.afterEachPage(c.pages)
	}
	return pairs, next, err
}

func (c *countingKV) DeleteRawBatch(keys []string) error {
	c.batches++
	c.maxBatch = max(c.maxBatch, len(keys))
	return c.PebbleStore.DeleteRawBatch(keys)
}

// seedPrune writes old (day 0) and fresh (day 10) whisper entries, a corrupt
// whisper entry, and entries of other kinds -- including aijournal:batch:,
// which the batch poller (#3454) owns -- then pins now at day 10.
func seedPrune(t *testing.T, s *database.PebbleStore, oldN, freshN int) *Journal {
	t.Helper()
	j, _ := New(s, "whisper")
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return base }
	for i := range oldN {
		if err := j.Complete(fmt.Sprintf("old%04d", i), "e", "m", result{Text: "o"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"aijournal:whisper:corrupt", "aijournal:batch:b1", "aijournal:llm:x", "aijournal:whisperx:y"} {
		if err := s.SetRaw(k, []byte("not an entry")); err != nil {
			t.Fatal(err)
		}
	}
	j.now = func() time.Time { return base.Add(10 * 24 * time.Hour) }
	for i := range freshN {
		if err := j.Complete(fmt.Sprintf("new%04d", i), "e", "m", result{Text: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func TestPrune_PagesWithBoundedMemoryAndBatchedDeletes(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	seedPrune(t, s, 23, 17)
	c := &countingKV{PebbleStore: s}
	j, _ := New(c, "whisper")
	j.pageSize = 5
	j.now = func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }

	n, err := j.Prune(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 24 {
		t.Fatalf("pruned %d, want 24 (23 old + 1 corrupt)", n)
	}
	if c.fullScans != 0 {
		t.Fatalf("Prune used the unbounded ScanPrefix %d time(s)", c.fullScans)
	}
	if c.maxLimit != 5 {
		t.Fatalf("largest page requested = %d, want the page size 5", c.maxLimit)
	}
	if c.maxBatch > 5 || c.batches == 0 {
		t.Fatalf("delete batches: %d, largest %d; want >0 batches of <=5", c.batches, c.maxBatch)
	}
	if left, _ := s.CountPrefix("aijournal:whisper:"); left != 17 {
		t.Fatalf("%d whisper entries left, want the 17 fresh ones", left)
	}
	for _, k := range []string{"aijournal:batch:b1", "aijournal:llm:x", "aijournal:whisperx:y"} {
		if v, _ := s.GetRaw(k); v == nil {
			t.Errorf("Prune of kind whisper deleted %s, which it does not own", k)
		}
	}
}

// A run of pages that delete nothing must still advance: the cursor is the
// last key SCANNED, not the last key deleted.
func TestPrune_AllFreshTerminates(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	seedPrune(t, s, 0, 12)
	_ = s.DeleteRaw("aijournal:whisper:corrupt")
	c := &countingKV{PebbleStore: s}
	j, _ := New(c, "whisper")
	j.pageSize = 4
	j.now = func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }
	c.afterEachPage = func(page int) {
		if page > 10 {
			t.Fatalf("Prune requested %d pages for 12 entries at page size 4: cursor is not advancing", page)
		}
	}
	n, err := j.Prune(context.Background(), 7*24*time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("Prune = %d, %v; want 0, nil", n, err)
	}
	if c.batches != 0 {
		t.Fatalf("%d delete batches for nothing to delete", c.batches)
	}
}

func TestPrune_ContextCancelStopsBetweenPages(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	seedPrune(t, s, 20, 0)
	ctx, cancel := context.WithCancel(context.Background())
	c := &countingKV{PebbleStore: s, afterEachPage: func(page int) {
		if page == 2 {
			cancel()
		}
	}}
	j, _ := New(c, "whisper")
	j.pageSize = 5
	j.now = func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }

	n, err := j.Prune(ctx, 7*24*time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if c.pages > 2 {
		t.Fatalf("scanned %d pages after cancel at page 2", c.pages)
	}
	if n >= 21 {
		t.Fatalf("deleted %d entries: cancel did not stop the prune", n)
	}
	if left, _ := s.CountPrefix("aijournal:whisper:"); int(left) != 21-n {
		t.Fatalf("reported %d deleted but %d of 21 remain", n, left)
	}
}

func TestPrune_NonPositiveRetentionIsRefused(t *testing.T) {
	s := openPebble(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	j := seedPrune(t, s, 3, 0)
	for _, d := range []time.Duration{0, -time.Hour} {
		if n, err := j.Prune(context.Background(), d); err == nil || n != 0 {
			t.Errorf("Prune(%v) = %d, %v; want 0 and an error", d, n, err)
		}
	}
	if left, _ := s.CountPrefix("aijournal:whisper:"); left != 4 {
		t.Fatalf("%d whisper entries left, want all 4", left)
	}
}
