// file: core/internal/restoreswap/swapback_test.go
// version: 1.0.0
// guid: 7f76dc4a-b3aa-4424-86dc-05da90363f27
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// truncateManifests cuts every MANIFEST of the store at db to half its
// length: a store that no longer opens.
func truncateManifests(t testing.TB, fs vfs.FS, db string) {
	t.Helper()
	names, err := fs.List(db)
	if err != nil {
		t.Fatal(err)
	}
	cut := 0
	for _, n := range names {
		if !strings.HasPrefix(n, "MANIFEST-") {
			continue
		}
		p := filepath.Join(db, n)
		f, err := fs.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(f)
		if err := errors.Join(err, f.Close()); err != nil {
			t.Fatal(err)
		}
		w, err := fs.Create(p, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		_, werr := w.Write(b[:len(b)/2])
		if err := errors.Join(werr, w.Sync(), w.Close()); err != nil {
			t.Fatal(err)
		}
		cut++
	}
	if cut == 0 {
		t.Fatalf("no MANIFEST under %s", db)
	}
}

// TestSwapBackOverUnopenableOriginal (05-backup §5B.3.4 step x; R1-21 review
// M2, probe 2): a restore whose restored store fails after it opened, over
// an original that cannot be opened (an empty directory, a truncated
// MANIFEST), still swaps back in that boot: x5 cannot rewrite the original's
// sidecars from its stamps, so the sidecars x4 moved back with it stand, the
// error is logged and recorded in swapped_back's note, and the in-flight
// file goes. The next P0a has nothing to resume, where every boot used to
// halt in x5.
func TestSwapBackOverUnopenableOriginal(t *testing.T) {
	for _, original := range []string{"empty", "corrupt"} {
		t.Run(original, func(t *testing.T) {
			mem := vfs.NewCrashableMem()
			db := dbPathOf("outside")
			id := "r-" + original
			switch original {
			case "empty":
				if err := mkdirAllSynced(mem, db); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				makeStore(t, mem, db, 0, "live")
				truncateManifests(t, mem, db)
			}
			live := treeHash(t, mem, db)
			staging := filepath.Join(restorejournal.StagingDir(db, id), "main.pebble")
			makeStore(t, mem, staging, 1, "staged")
			staged := treeHash(t, mem, staging)
			if err := mkdirAllSynced(mem, stateDir); err != nil {
				t.Fatal(err)
			}
			in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, DBPath: db,
				Targets:       []restorejournal.Target{{From: staging, To: db}},
				StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
			s := newSwapper(t, mem, testBuild, testNow)
			ctx := context.Background()
			if err := s.Stage(ctx, in); err != nil {
				t.Fatal(err)
			}
			out, err := s.RunP0a(ctx)
			if err != nil || out.Boot != BootRestore {
				t.Fatalf("P0a = %+v, %v; want a restore boot", out, err)
			}
			if err := s.Opened(ctx, id); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("P2 refused the restored store")
			if err := s.SwapBack(ctx, id, cause); !errors.Is(err, ErrSwappedBack) || !errors.Is(err, cause) {
				t.Fatalf("SwapBack = %v, want ErrSwappedBack wrapping the cause", err)
			}
			recs, err := s.Journal(id)
			if err != nil {
				t.Fatal(err)
			}
			last := recs[len(recs)-1]
			if last.Step != StepSwappedBack || !strings.Contains(last.Note, cause.Error()) || !strings.Contains(last.Note, "sidecars") {
				t.Fatalf("last record %+v, want swapped_back naming the cause and the sidecars x5 could not rewrite", last)
			}
			if _, ok, err := InFlight(mem, stateDir); err != nil || ok {
				t.Fatalf("in flight = %v, %v; want the in-flight file gone", ok, err)
			}
			if got := treeHash(t, mem, db); got != live {
				t.Fatal("the original is not back at the DB path as it was")
			}
			if got := treeHash(t, mem, restorejournal.FailedDest(stateDir, db, id, db)); got != staged {
				t.Fatal("the restored store is not in failed/")
			}
			if out, err := newSwapper(t, mem, testBuild, testNow).RunP0a(ctx); err != nil || out.Boot != BootPlain {
				t.Fatalf("the next P0a = %+v, %v; want a plain boot", out, err)
			}
		})
	}
}
