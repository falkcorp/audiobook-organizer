// file: core/internal/restoreswap/invalid_test.go
// version: 1.0.0
// guid: 157b49e8-b962-49c8-b5e3-a5c74ce2c8b0
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// rejectedFiles lists restore/rejected-*.json with their contents.
func rejectedFiles(t *testing.T, fs vfs.FS) map[string]string {
	t.Helper()
	names, err := fs.List(restorejournal.Dir(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, n := range names {
		if strings.HasPrefix(n, "rejected-") {
			out[n], _ = readFile(t, fs, filepath.Join(restorejournal.Dir(stateDir), n))
		}
	}
	return out
}

// TestP0aRejectsAnUnusableIntent (R1-21 review m3): a pending.json that does
// not decode, or has no usable id or db_path, no longer halts every boot.
// Nothing has moved, so P0a moves it to restore/rejected-<time>.json with
// its bytes intact, logs, and boots plain, degraded restore_rejected; a
// second one at the same time takes a new name and keeps the first.
func TestP0aRejectsAnUnusableIntent(t *testing.T) {
	db := dbPathOf("outside")
	for name, body := range map[string]string{
		"undecodable":     `{"id": "batarang", "kind": `,
		"bad-id":          `{"id": "../joker", "kind": "archive", "db_path": "` + db + `"}`,
		"dot-id":          `{"id": "..", "kind": "archive", "db_path": "` + db + `"}`,
		"relative-db":     `{"id": "r-riddler", "kind": "archive", "db_path": "db/main.pebble"}`,
		"empty-file":      ``,
		"json-not-intent": `["penguin"]`,
	} {
		t.Run(name, func(t *testing.T) {
			mem := vfs.NewCrashableMem()
			makeStore(t, mem, db, 0, "live")
			live := treeHash(t, mem, db)
			writeFile(t, mem, restorejournal.PendingPath(stateDir), body)
			out, err := newSwapper(t, mem, testBuild, testNow).RunP0a(context.Background())
			if err != nil || out.Boot != BootPlain || out.Degraded != DegradedRestoreRejected {
				t.Fatalf("P0a = %+v, %v; want a plain boot, degraded %s", out, err, DegradedRestoreRejected)
			}
			if ok, _ := exists(mem, restorejournal.PendingPath(stateDir)); ok {
				t.Fatal("the unusable intent is still in the slot")
			}
			if got := rejectedFiles(t, mem); len(got) != 1 || got["rejected-19390501T120000Z.json"] != body {
				t.Fatalf("rejected files %v, want the intent's bytes in rejected-19390501T120000Z.json", got)
			}
			if got := treeHash(t, mem, db); got != live {
				t.Fatal("the store changed")
			}
			// A second unusable intent at the same clock keeps the first.
			writeFile(t, mem, restorejournal.PendingPath(stateDir), "second")
			if out, err := newSwapper(t, mem, testBuild, testNow).RunP0a(context.Background()); err != nil || out.Degraded != DegradedRestoreRejected {
				t.Fatalf("second P0a = %+v, %v", out, err)
			}
			if got := rejectedFiles(t, mem); len(got) != 2 || got["rejected-19390501T120000Z.json"] != body || got["rejected-19390501T120000Z-1.json"] != "second" {
				t.Fatalf("rejected files %v, want both kept", got)
			}
		})
	}
}

// TestP0aMovesAnUnusableIntentBesideAnInflight (R1-21 review m3): beside an
// in-flight file a pending.json that is no usable intent is not the swap's
// own slot file; it is moved aside and the swap resumes.
func TestP0aMovesAnUnusableIntentBesideAnInflight(t *testing.T) {
	f := restoreFixture(t, restorejournal.KindArchive, "outside", false)
	inflight := filepath.Join(restorejournal.JournalDir(stateDir, f.in.ID), restorejournal.InflightName)
	if err := f.mem.Rename(restorejournal.PendingPath(stateDir), inflight); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.mem, restorejournal.PendingPath(stateDir), `{"id": `)
	out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(context.Background())
	if err != nil || out.Boot != BootRestore || out.ID != f.in.ID {
		t.Fatalf("P0a = %+v, %v; want the in-flight restore resumed", out, err)
	}
	if got := rejectedFiles(t, f.mem); len(got) != 1 {
		t.Fatalf("rejected files %v, want the unusable intent moved aside", got)
	}
}

// TestP0aHaltsWhenPendingCannotBeRead (R1-21 review m3): an error reading
// pending.json is no verdict on its content: boot halts and the intent
// stays in the slot for the next boot.
func TestP0aHaltsWhenPendingCannotBeRead(t *testing.T) {
	f := restoreFixture(t, restorejournal.KindArchive, "outside", false)
	pending := restorejournal.PendingPath(stateDir)
	// The Recorder sees durable operations only; a read fault is wrapped
	// directly.
	fs := errorfs.Wrap(f.mem, errorfs.InjectorFunc(func(op errorfs.Op) error {
		if (op.Kind == errorfs.OpFileRead || op.Kind == errorfs.OpFileReadAt) && filepath.Clean(op.Path) == pending {
			return syscall.EIO
		}
		return nil
	}))
	if _, err := newSwapper(t, fs, testBuild, testNow).RunP0a(context.Background()); err == nil {
		t.Fatal("P0a booted over an intent it could not read")
	}
	if ok, _ := exists(f.mem, pending); !ok {
		t.Fatal("an intent that could not be read was moved out of the slot")
	}
	if got := rejectedFiles(t, f.mem); len(got) != 0 {
		t.Fatalf("rejected files %v after a read error", got)
	}
}
