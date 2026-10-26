// file: internal/plugins/maintenance/book_hash_index_repair_test.go
// version: 1.0.0
// guid: 2b7d9e41-6c35-4a08-9f1e-5d3a8c0b7e62
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// hashIndexKey is the key the store keeps for (hash, book) in the file-hash
// index (internal/database/pebble_store_book_hash_index.go).
func hashIndexKey(hash, id string) string { return "book_hash:" + hash + "\x00" + id }

// hashIndexFixture is a library of two books whose index keys the test then
// damages: "gone" lost its key for hash HG (the 2026-10-21 purge shape), "kept"
// is intact. It runs the op through a Store-embedding decorator, as production
// does.
func hashIndexFixture(t *testing.T) (*Plugin, *database.PebbleStore, string) {
	t.Helper()
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()
	hg, hk := "HG", "HK"
	if _, err := inner.CreateBook(&database.Book{ID: "gone", Title: "Gone", FilePath: "/lib/gone", FileHash: &hg}); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.CreateBook(&database.Book{ID: "kept", Title: "Kept", FilePath: "/lib/kept", FileHash: &hk}); err != nil {
		t.Fatal(err)
	}
	if err := inner.DeleteRaw(hashIndexKey("HG", "gone")); err != nil {
		t.Fatal(err)
	}
	if got, _ := inner.GetBookByFileHash("HG"); got != nil {
		t.Fatalf("fixture: HG must be unfindable before the op, found %s", got.ID)
	}
	return New(fakeDeps{store: &opChangeDecorator{Store: inner}}), inner, hashIndexKey("HG", "gone")
}

func keyPresent(t *testing.T, s *database.PebbleStore, key string) bool {
	t.Helper()
	v, err := s.GetRaw(key)
	if err != nil {
		t.Fatal(err)
	}
	return v != nil
}

// Preview is the default: no params, {} and dry_run=true report the missing key
// and write nothing; only dry_run=false repairs it. (A preview that wrote would
// be the one-line mutant that turns a report into a mutation.)
func TestBookHashIndexRepair_PreviewWritesNothingAndOnlyDryRunFalseRepairs(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("preview "+raw, func(t *testing.T) {
			p, inner, key := hashIndexFixture(t)
			rep := &p6AttrReporter{}
			if err := p.runBookHashIndexRepair(context.Background(), json.RawMessage(raw), rep); err != nil {
				t.Fatalf("preview: %v", err)
			}
			if keyPresent(t, inner, key) {
				t.Fatalf("params %q wrote the missing key: a preview must write nothing", raw)
			}
			if got, _ := inner.GetBookByFileHash("HG"); got != nil {
				t.Fatalf("params %q repaired the lookup", raw)
			}
			joined := joinLines(rep)
			if !strings.Contains(joined, "1 missing keys") || !strings.Contains(joined, "preview, nothing written") {
				t.Fatalf("the preview must report the missing key; log:\n%s", joined)
			}
		})
	}

	t.Run("dry_run=false repairs", func(t *testing.T) {
		p, inner, key := hashIndexFixture(t)
		rep := &p6AttrReporter{}
		if err := p.runBookHashIndexRepair(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
			t.Fatalf("repair: %v", err)
		}
		if !keyPresent(t, inner, key) {
			t.Fatal("dry_run=false did not write the missing key")
		}
		got, err := inner.GetBookByFileHash("HG")
		if err != nil || got == nil || got.ID != "gone" {
			t.Fatalf("GetBookByFileHash(HG) = %v, %v; want the repaired book", got, err)
		}
		if !strings.Contains(joinLines(rep), "index complete=true") {
			t.Fatalf("a clean repair marks the index complete; log:\n%s", joinLines(rep))
		}
	})
}

// Boot's {"ensure": true} repairs without dry_run (which would make it a
// preview), is a tagged no-op once nothing is left to do, and is refused beside
// dry_run.
func TestBookHashIndexRepair_EnsureRepairsThenIsATaggedNoOp(t *testing.T) {
	p, inner, key := hashIndexFixture(t)
	rep := &p6AttrReporter{}
	if err := p.runBookHashIndexRepair(context.Background(), json.RawMessage(`{"ensure":true}`), rep); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !keyPresent(t, inner, key) {
		t.Fatal("ensure did not repair")
	}
	if rep.noOpLines() != 0 {
		t.Fatal("a run that wrote keys was tagged no-op")
	}

	rep = &p6AttrReporter{}
	if err := p.runBookHashIndexRepair(context.Background(), json.RawMessage(`{"ensure":true}`), rep); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if rep.noOpLines() != 1 {
		t.Fatalf("the next boot found nothing to do and must say so, tagged: %+v", rep.lines)
	}

	for _, raw := range []string{`{"ensure":true,"dry_run":true}`, `{"ensure":true,"dry_run":false}`, `{"ensure":true,"dryRun":false}`} {
		if err := p.runBookHashIndexRepair(context.Background(), json.RawMessage(raw), &p6AttrReporter{}); err == nil {
			t.Fatalf("%s must be refused", raw)
		}
	}
}

// The API start check refuses ensure, which only the server's own boot enqueue
// may send.
func TestBookHashIndexRepair_StartCheckRefusesEnsure(t *testing.T) {
	def := (&Plugin{}).bookHashIndexRepairDef()
	if def.Preview.StartCheck == nil {
		t.Fatal("no StartCheck")
	}
	if err := def.Preview.StartCheck(map[string]json.RawMessage{"ensure": json.RawMessage(`true`)}, false); err == nil {
		t.Fatal("ensure=true must be refused at start")
	}
	if err := def.Preview.StartCheck(map[string]json.RawMessage{"ensure": json.RawMessage(`false`)}, false); err != nil {
		t.Fatalf("ensure=false is a plain run: %v", err)
	}
	if err := def.Preview.StartCheck(map[string]json.RawMessage{}, false); err != nil {
		t.Fatal(err)
	}
}

func joinLines(r *p6AttrReporter) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, l := range r.lines {
		b.WriteString(l.msg)
		b.WriteByte('\n')
	}
	return b.String()
}
