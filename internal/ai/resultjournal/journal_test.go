// file: internal/ai/resultjournal/journal_test.go
// version: 1.0.0
// guid: 5c28e392-3a6b-4bf2-9949-6f7efc1aedd2
// last-edited: 2026-09-19

package resultjournal

import (
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

	n, err := j.Prune(7 * 24 * time.Hour)
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
