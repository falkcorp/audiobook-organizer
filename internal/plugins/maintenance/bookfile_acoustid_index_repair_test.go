// file: internal/plugins/maintenance/bookfile_acoustid_index_repair_test.go
// version: 1.0.0
// guid: 3f9a6c42-d718-4e05-b2a3-6e1c80d4f957
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const acoustIDKeyPrefix = "book_file_acoustid:"

// acoustIDRepairFixture is a library whose book_file_acoustid: index holds, as
// the pre-2026-10-30 writers left it:
//
//	seg-riddler  -> a row that exists and stores no segment (orphan)
//	seg-gone     -> a row that was deleted (orphan)
//	seg-joker    -> a legacy row that still stores its segment (live)
//	seg-broken   -> a value that is not a row reference (reported, kept)
//
// It runs the op through a Store-embedding decorator, as production does.
type acoustIDRepairFixture struct {
	p     *Plugin
	inner *database.PebbleStore
}

func newAcoustIDRepairFixture(t *testing.T) *acoustIDRepairFixture {
	t.Helper()
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()

	book, err := inner.CreateBook(&database.Book{Title: "Gotham Central", FilePath: "/lib/gotham-central"})
	if err != nil {
		t.Fatal(err)
	}
	bare := &database.BookFile{BookID: book.ID, FilePath: "/lib/gotham-central/01.mp3"}
	if err := inner.CreateBookFile(bare); err != nil {
		t.Fatal(err)
	}
	legacy := &database.BookFile{ID: "01LEGACYREPAIR0000000000A", BookID: book.ID, FilePath: "/lib/gotham-central/02.mp3", AcoustIDSeg0: "seg-joker"}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	setRaw := func(k string, v []byte) {
		t.Helper()
		if err := inner.SetRaw(k, v); err != nil {
			t.Fatal(err)
		}
	}
	setRaw("book_file:"+book.ID+":"+legacy.ID, raw)
	setRaw(acoustIDKeyPrefix+"seg-riddler", []byte(book.ID+":"+bare.ID))
	setRaw(acoustIDKeyPrefix+"seg-gone", []byte(book.ID+":01GONE000000000000000000A"))
	setRaw(acoustIDKeyPrefix+"seg-joker", []byte(book.ID+":"+legacy.ID))
	setRaw(acoustIDKeyPrefix+"seg-broken", []byte("not-a-ref"))
	return &acoustIDRepairFixture{
		p:     New(fakeDeps{store: &opChangeDecorator{Store: inner}}),
		inner: inner,
	}
}

func (f *acoustIDRepairFixture) has(t *testing.T, seg string) bool {
	t.Helper()
	return keyPresent(t, f.inner, acoustIDKeyPrefix+seg)
}

// Preview is the default: no params, {} and dry_run=true report the orphans and
// delete nothing; only dry_run=false deletes, and it deletes the orphans only.
func TestBookFileAcoustIDIndexRepair_PreviewDeletesNothingAndOnlyDryRunFalseRepairs(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("preview "+raw, func(t *testing.T) {
			f := newAcoustIDRepairFixture(t)
			rep := &p6AttrReporter{}
			if err := f.p.runBookFileAcoustIDIndexRepair(context.Background(), json.RawMessage(raw), rep); err != nil {
				t.Fatalf("preview: %v", err)
			}
			for _, seg := range []string{"seg-riddler", "seg-gone", "seg-joker", "seg-broken"} {
				if !f.has(t, seg) {
					t.Fatalf("params %q deleted %s: a preview must delete nothing", raw, seg)
				}
			}
			joined := joinLines(rep)
			for _, want := range []string{"4 keys", "2 orphans (1 row lacks the segment, 1 row gone)", "1 live legacy", "1 malformed", "preview, nothing deleted"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("preview report missing %q; log:\n%s", want, joined)
				}
			}
		})
	}

	t.Run("dry_run=false deletes the orphans only", func(t *testing.T) {
		f := newAcoustIDRepairFixture(t)
		rep := &p6AttrReporter{}
		if err := f.p.runBookFileAcoustIDIndexRepair(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
			t.Fatalf("repair: %v", err)
		}
		if f.has(t, "seg-riddler") || f.has(t, "seg-gone") {
			t.Fatal("an orphaned key survived the repair")
		}
		if !f.has(t, "seg-joker") {
			t.Fatal("the repair deleted a key whose row still stores its segment")
		}
		if !f.has(t, "seg-broken") {
			t.Fatal("the repair deleted a malformed key it cannot judge")
		}
		if joined := joinLines(rep); !strings.Contains(joined, "2 deleted, 0 kept") {
			t.Fatalf("report must count the deletes; log:\n%s", joined)
		}

		// Idempotent: a second run finds nothing to delete.
		rep = &p6AttrReporter{}
		if err := f.p.runBookFileAcoustIDIndexRepair(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
			t.Fatalf("second repair: %v", err)
		}
		if joined := joinLines(rep); !strings.Contains(joined, "0 orphans") || !strings.Contains(joined, "0 deleted") {
			t.Fatalf("a re-run must find no orphans; log:\n%s", joined)
		}
	})
}

// More keys than one page, judged on the worker pool: every orphan on every
// page is found and deleted, and the cursor neither skips nor repeats a key.
func TestBookFileAcoustIDIndexRepair_PagesAndWorkerPool(t *testing.T) {
	f := newAcoustIDRepairFixture(t)
	book, err := f.inner.CreateBook(&database.Book{Title: "Arkham Knight", FilePath: "/lib/arkham-knight"})
	if err != nil {
		t.Fatal(err)
	}
	bare := &database.BookFile{BookID: book.ID, FilePath: "/lib/arkham-knight/01.mp3"}
	if err := f.inner.CreateBookFile(bare); err != nil {
		t.Fatal(err)
	}
	const extra = acoustIDRepairPage + 150
	for i := range extra {
		if err := f.inner.SetRaw(fmt.Sprintf("%sseg-bulk-%05d", acoustIDKeyPrefix, i), []byte(book.ID+":"+bare.ID)); err != nil {
			t.Fatal(err)
		}
	}

	rep := &p6AttrReporter{}
	if err := f.p.runBookFileAcoustIDIndexRepair(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
		t.Fatalf("repair: %v", err)
	}
	pairs, err := f.inner.ScanPrefix(acoustIDKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, kv := range pairs {
		left = append(left, strings.TrimPrefix(kv.Key, acoustIDKeyPrefix))
	}
	if len(left) != 2 || !f.has(t, "seg-joker") || !f.has(t, "seg-broken") {
		t.Fatalf("keys left = %v, want only the live legacy and the malformed one", left)
	}
	want := fmt.Sprintf("%d deleted", extra+2)
	if joined := joinLines(rep); !strings.Contains(joined, want) {
		t.Fatalf("report must say %q; log:\n%s", want, joined)
	}
}

// A cancelled run stops without error from a half-finished delete and leaves
// the index as it was or partly repaired, never worse.
func TestBookFileAcoustIDIndexRepair_CancelledRunDeletesNothingMore(t *testing.T) {
	f := newAcoustIDRepairFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := &p6AttrReporter{}
	if err := f.p.runBookFileAcoustIDIndexRepair(ctx, json.RawMessage(`{"dry_run":false}`), rep); err == nil {
		t.Fatal("a cancelled run must return the context error")
	}
	for _, seg := range []string{"seg-riddler", "seg-gone", "seg-joker", "seg-broken"} {
		if !f.has(t, seg) {
			t.Fatalf("a run cancelled before it started deleted %s", seg)
		}
	}
}

func TestBookFileAcoustIDIndexRepair_DeclaresAPreviewDefaultOp(t *testing.T) {
	def := (&Plugin{}).bookFileAcoustIDIndexRepairDef()
	if def.ID != "maintenance.bookfile-acoustid-index-repair" {
		t.Fatalf("op id = %q", def.ID)
	}
	if def.Run == nil || def.Preview.Params == nil {
		t.Fatal("the op must declare Run and a Preview params type")
	}
}
