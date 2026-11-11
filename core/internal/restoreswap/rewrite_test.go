// file: core/internal/restoreswap/rewrite_test.go
// version: 1.0.1
// guid: d92e319d-69bf-437e-a034-410415eaefa0
// last-edited: 2026-11-11

package restoreswap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const rewriteStepID = "books.rewrite"

// rewriteSwapped is a KindRewrite journal of rewriteStepID for the store at
// dbPath, left at swapped with its in-flight file, as phase 3 leaves it:
// the old store (holding "Two-Face's ledger") moved by step c to the
// set-aside the swap's own rule names. It returns the swapper, the intent
// and that set-aside.
func rewriteSwapped(t *testing.T, dbPath string) (*vfs.MemFS, *Swapper, restorejournal.Intent, string) {
	t.Helper()
	ctx := context.Background()
	mem := vfs.NewMem()
	s, err := New(Options{FS: mem, StateDir: stateDir, BuildID: testBuild, Clock: testNow, Owners: nil, RewriteStep: rewriteStepID})
	if err != nil {
		t.Fatal(err)
	}
	id := "rewrite-19390501T120000.000000000Z"
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindRewrite, DBPath: dbPath,
		Targets:       []restorejournal.Target{{From: dbPath + ".rewrite", To: dbPath}},
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(rewriteExpiry), RequestedBy: "core/storage " + rewriteStepID,
		RequestedAt: testNow(), StepID: rewriteStepID}
	if err := s.mkdirDurable(s.journalDir(id)); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{StepStaged, StepPreviousMoved, StepStagedMoved, StepSidecars, StepSwapped} {
		rec := restorejournal.Record{Step: step}
		if step == StepStaged {
			rec.Note, rec.StepID = string(restorejournal.KindRewrite), rewriteStepID
		}
		if err := s.appendRec(ctx, id, rec); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(mem, s.inflightPath(id), raw, privateFile); err != nil {
		t.Fatal(err)
	}
	aside := restorejournal.SetAsideDest(stateDir, dbPath, id, dbPath)
	writeFile(t, mem, filepath.Join(aside, "000001.sst"), "Two-Face's ledger")
	writeFile(t, mem, filepath.Join(dbPath, "000002.sst"), "the copy")
	return mem, s, in, aside
}

// TestRewriteFinishDeletesTheSetAsideTheMoveUsed: phase 4 deletes the old
// store where step c moved it, by the swap's own SetAsideDest rule, in both
// layouts: a DB under the state directory (the compiled default, whose
// set-aside is <state>/restore/<id>/previous/..., not beside the DB) and a
// DB on its own tree (<db parent>/.mediacore-restore/<id>/previous/...).
// The journal directory stays and records applied; the in-flight file goes;
// the live store is untouched.
func TestRewriteFinishDeletesTheSetAsideTheMoveUsed(t *testing.T) {
	for _, layout := range []string{"state", "outside"} {
		t.Run(layout, func(t *testing.T) {
			db := dbPathOf(layout)
			mem, s, in, aside := rewriteSwapped(t, db)
			_, leaf, root, err := s.rewriteAside(in)
			if err != nil {
				t.Fatal(err)
			}
			wantLeaf := filepath.Join(filepath.Dir(db), restorejournal.AsideDirName, in.ID, "previous")
			if layout == "state" {
				wantLeaf = filepath.Join(s.journalDir(in.ID), "previous")
				if beside := filepath.Join(filepath.Dir(db), restorejournal.AsideDirName, in.ID); strings.HasPrefix(aside, beside) {
					t.Fatalf("fixture: the default layout's set-aside %s lies beside the DB; the case proves nothing", aside)
				}
			}
			if leaf != wantLeaf || !strings.HasPrefix(aside, leaf+"/") {
				t.Fatalf("rewriteAside leaf = %s (root %s), want %s holding %s", leaf, root, wantLeaf, aside)
			}
			if err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db); err != nil {
				t.Fatal(err)
			}
			for _, gone := range []string{aside, leaf, s.inflightPath(in.ID)} {
				if ok, _ := exists(mem, gone); ok {
					t.Errorf("%s survived phase 4", gone)
				}
			}
			if layout == "outside" {
				if ok, _ := exists(mem, root); ok {
					t.Errorf("the swap's own aside root %s survived phase 4", root)
				}
			}
			if ok, _ := exists(mem, filepath.Join(db, "000002.sst")); !ok {
				t.Error("phase 4 touched the live store")
			}
			if last, err := s.last(in.ID); err != nil || last != StepApplied {
				t.Errorf("journal last = %q, %v; want applied", last, err)
			}
			// idempotent: a repeat (a kill before M8's record) is a no-op.
			if err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db); err != nil {
				t.Errorf("repeated phase 4: %v", err)
			}
		})
	}
}

// TestRewriteFinishFailsClosedOnAMissingSetAside: the journal says step c
// moved the old store, no deletion began, and the set-aside is not there:
// phase 4 refuses with ErrJournalCorrupt naming the path, journals nothing
// and keeps the in-flight file (the boot halts; nothing claims the old store
// gone). With the deletion mark present (a kill after it), the same state is
// the deletion's own progress and phase 4 completes.
func TestRewriteFinishFailsClosedOnAMissingSetAside(t *testing.T) {
	for _, layout := range []string{"state", "outside"} {
		t.Run(layout, func(t *testing.T) {
			db := dbPathOf(layout)
			mem, s, in, aside := rewriteSwapped(t, db)
			if err := mem.RemoveAll(aside); err != nil {
				t.Fatal(err)
			}
			err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db)
			if !errors.Is(err, ErrJournalCorrupt) || !strings.Contains(err.Error(), aside) {
				t.Fatalf("finish with the set-aside missing = %v, want ErrJournalCorrupt naming %s", err, aside)
			}
			if last, _ := s.last(in.ID); last != StepSwapped {
				t.Errorf("journal last = %q after the refusal, want swapped", last)
			}
			if ok, _ := exists(mem, s.inflightPath(in.ID)); !ok {
				t.Error("the refusal removed the in-flight file")
			}
			if err := writeFileAtomic(mem, s.markPath(in.ID, rewriteDeleteStarted), nil, privateFile); err != nil {
				t.Fatal(err)
			}
			if err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db); err != nil {
				t.Errorf("finish after a kill inside the deletion = %v, want nil", err)
			}
		})
	}
}

// TestRewriteFinishRefusesAnotherIntent: the in-flight intent must be this
// M8's KindRewrite intent for the store this boot opened; any other is a
// corrupt journal and deletes nothing.
func TestRewriteFinishRefusesAnotherIntent(t *testing.T) {
	db := dbPathOf("state")
	mem, s, in, aside := rewriteSwapped(t, db)
	for name, call := range map[string]func() error{
		"other step": func() error { return s.finishRewriteSwap(context.Background(), in.ID, "joker.rewrite", db) },
		"other store": func() error {
			return s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, "/batcave/elsewhere/main.pebble")
		},
	} {
		if err := call(); !errors.Is(err, ErrJournalCorrupt) {
			t.Errorf("%s: finish = %v, want ErrJournalCorrupt", name, err)
		}
		if ok, _ := exists(mem, aside); !ok {
			t.Fatalf("%s: the refusal deleted the set-aside", name)
		}
	}
}

// m8Store is a store on fs at db (wayne at level 1, tagged "live") holding
// rows rows of Batman storylines, for M8's end-to-end runs.
func m8Store(t *testing.T, fs vfs.FS, db string, rows int) {
	t.Helper()
	makeStore(t, fs, db, 1, "live")
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: fs})
	op, err := storage.Open(ctx, storage.OpenOptions{Path: db, Build: testBuild, Owners: []storage.OwnerSpec{wayne(1)}, FreeSpaceMargin: 1})
	if err != nil {
		t.Fatal(err)
	}
	sdb, err := op.Prepare(ctx)
	if err == nil {
		err = sdb.Migrate(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	ks, _ := sdb.Keyspace("wayne")
	b := ks.NewBatch()
	for n := range rows {
		if err := b.Set(fmt.Appendf(nil, "story/%04d", n), fmt.Appendf(nil, "Hush, chapter %d", n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(b.Commit(ctx, storage.Sync), sdb.Close()); err != nil {
		t.Fatal(err)
	}
}

// m8Boot boots the store at db on fs with M8 (wayne.rewrite) as its one
// data step, run by a Swapper over fs; before is called inside the step
// before RunRewrite. It returns the boot's error, M8's runs and the store's
// epoch generation and tag at the end of a successful boot.
type m8Result struct {
	err        error
	runs       int
	genBefore  uint64
	genAfter   uint64
	tag        string
	rows       int
	checkpoint string // the checkpoint area's tree hash as M8 started
}

func m8Boot(t *testing.T, fs vfs.FS, db string) m8Result {
	t.Helper()
	return m8BootWith(t, fs, db, nil)
}

// m8BootWith is m8Boot with before (nil: none) called inside the step
// before RunRewrite.
func m8BootWith(t *testing.T, fs vfs.FS, db string, before func(ctx context.Context, sdb *storage.DB) error) m8Result {
	t.Helper()
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: fs, BuildID: testBuild, Clock: testNow})
	s, err := New(Options{FS: fs, StateDir: stateDir, BuildID: testBuild, Clock: testNow, Owners: []storage.OwnerSpec{wayne(1)}, RewriteStep: "wayne.rewrite"})
	if err != nil {
		t.Fatal(err)
	}
	var res m8Result
	if _, err := s.RunP0a(ctx); err != nil {
		res.err = err
		return res
	}
	step := storage.NewRewriteStep("wayne", "wayne.rewrite")
	ds := storage.DataStep{Owner: "wayne", ID: step.ID, Cutover: true, Rewrite: true, Run: func(ctx context.Context, sdb *storage.DB) error {
		res.runs++
		res.genBefore = epochOf(t, sdb)
		res.checkpoint = treeHash(t, fs, filepath.Join(filepath.Dir(db), ".mediacore-checkpoints"))
		if before != nil {
			if err := before(ctx, sdb); err != nil {
				return err
			}
		}
		return s.RunRewrite(ctx, sdb, step)
	}}
	op, err := storage.Open(ctx, storage.OpenOptions{Path: db, Build: testBuild, Owners: []storage.OwnerSpec{wayne(1)}, FreeSpaceMargin: 1,
		DataSteps: []storage.DataStep{ds}})
	if err != nil {
		res.err = err
		return res
	}
	sdb, err := op.Prepare(ctx)
	if err != nil {
		res.err = errors.Join(err, op.Close())
		return res
	}
	if err := sdb.Migrate(ctx); err != nil {
		res.err = errors.Join(err, sdb.Close())
		return res
	}
	res.genAfter = epochOf(t, sdb)
	ks, _ := sdb.Keyspace("wayne")
	tag, _ := ks.Get(ctx, []byte("tag"))
	res.tag = string(tag)
	for _, err := range ks.Scan(ctx, storage.ScanOptions{Start: []byte("story/"), End: []byte("story0"), KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		res.rows++
	}
	if err := sdb.Close(); err != nil {
		t.Fatal(err)
	}
	return res
}

// epochOf is the store generation in c/storage/epoch.
func epochOf(t *testing.T, db *storage.DB) uint64 {
	t.Helper()
	ks, _ := db.Keyspace("core/storage")
	raw, err := ks.Get(context.Background(), keys.Join(keys.Str("epoch")))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Epoch uint64 `json:"epoch"`
	}
	if err := json.Unmarshal(raw[bytes.IndexByte(raw, '{'):], &rec); err != nil {
		t.Fatalf("epoch record %q: %v", raw, err)
	}
	return rec.Epoch
}

// engineFiles is the content of each Pebble table and MANIFEST under dir.
func engineFiles(t *testing.T, fs vfs.FS, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	names, err := fs.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".sst") && !strings.HasPrefix(n, "MANIFEST-") {
			continue
		}
		out[n] = readAll(t, fs, filepath.Join(dir, n))
	}
	return out
}

func readAll(t *testing.T, fs vfs.FS, p string) string {
	t.Helper()
	f, err := fs.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// anyFileHolds walks fs from root, outside the checkpoint area, for a file
// whose content is one of contents; it returns the first such path.
func anyFileHolds(t *testing.T, fs vfs.FS, root string, contents map[string]string) string {
	t.Helper()
	want := map[string]bool{}
	for _, c := range contents {
		want[c] = true
	}
	var found string
	var walk func(p string)
	walk = func(p string) {
		if found != "" || filepath.Base(p) == ".mediacore-checkpoints" {
			return
		}
		fi, err := fs.Stat(p)
		if err != nil {
			return
		}
		if !fi.IsDir() {
			if want[readAll(t, fs, p)] {
				found = p
			}
			return
		}
		names, _ := fs.List(p)
		for _, n := range names {
			walk(filepath.Join(p, n))
		}
	}
	walk(root)
	return found
}

// TestCutoverRewriteLeavesNoOldFile (05-storage §5S.3.13 "M8: the
// rewrite"): M8 compacts, copies the store into a new directory and swaps it
// in as KindRewrite; afterwards no table or MANIFEST of the old directory is
// anywhere outside the checkpoint area (phase 4 deleted the set-aside), the
// generation is unchanged, every row is there, the journal reads applied
// with no in-flight file, and the attempt's checkpoint is still in place
// for a restore. The next boot does not run M8 again.
func TestCutoverRewriteLeavesNoOldFile(t *testing.T) {
	for _, layout := range []string{"state", "outside"} {
		t.Run(layout, func(t *testing.T) {
			mem := vfs.NewMem()
			mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
			db := dbPathOf(layout)
			m8Store(t, mem, db, 300)
			old := engineFiles(t, mem, db)
			if len(old) == 0 {
				t.Fatal("fixture: the old store has no engine files")
			}
			res := m8Boot(t, mem, db)
			if res.err != nil {
				t.Fatalf("M8 boot: %v", res.err)
			}
			if res.runs != 1 || res.genAfter != res.genBefore || res.genBefore == 0 || res.tag != "live" || res.rows != 300 {
				t.Fatalf("M8 boot = %+v, want one run, the generation kept and every row", res)
			}
			if p := anyFileHolds(t, mem, "/", old); p != "" {
				t.Errorf("%s holds a file of the old directory after M8", p)
			}
			if ok, _ := exists(mem, storage.RewriteDir(db)); ok {
				t.Error("the copy's directory survived the swap")
			}
			s := newSwapper(t, mem, testBuild, testNow)
			js, err := s.rewriteJournals("wayne.rewrite")
			if err != nil || len(js) != 1 || js[0].last != StepApplied {
				t.Fatalf("M8 journals = %+v, %v; want one applied", js, err)
			}
			if ok, _ := exists(mem, s.inflightPath(js[0].id)); ok {
				t.Error("the in-flight file survived phase 4")
			}
			cps, err := mem.List(filepath.Join(filepath.Dir(db), ".mediacore-checkpoints"))
			if err != nil || len(cps) == 0 {
				t.Errorf("checkpoint area = %v, %v; want the attempt's checkpoint", cps, err)
			}
			again := m8Boot(t, mem, db)
			if again.err != nil || again.runs != 0 || again.tag != "live" || again.rows != 300 {
				t.Errorf("the next boot = %+v, want M8 done and the store intact", again)
			}
		})
	}
}

// failPlace is fs over mem failing the first rename of the copy into the
// store's path (step d of the KindRewrite swap), so the swap swaps back.
func failPlace(mem *vfs.MemFS, db string) (vfs.FS, func() bool) {
	return failOnce(mem, func(op errorfs.Op) bool {
		return op.Kind == errorfs.OpRename && filepath.Clean(op.Path) == storage.RewriteDir(db)
	})
}

// TestRewriteSwapBackMovesNoCheckpoint (05-backup §5B.3.4 KindRewrite rules;
// §5S.3.13 "M8 never loops"): a swap that fails swaps back, the store is
// the original with every row, and the checkpoint area is exactly as M8
// found it (the swap-back moves no checkpoint and starts no clock). The
// next boot restarts M8 once from the compaction and finishes, so a row
// written to the original after the refused copy was taken is in the store
// it swaps in (the refused copy, still beside the store when the failure was
// its own rename into place, is never reused); a second consecutive
// swap-back refuses the boot with storage.ErrRewriteSwappedBack and stages
// nothing.
func TestRewriteSwapBackMovesNoCheckpoint(t *testing.T) {
	newStore := func(t *testing.T) (*vfs.MemFS, string) {
		mem := vfs.NewMem()
		mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
		db := dbPathOf("state")
		m8Store(t, mem, db, 120)
		return mem, db
	}
	cpArea := func(db string) string { return filepath.Join(filepath.Dir(db), ".mediacore-checkpoints") }

	t.Run("once, then finished", func(t *testing.T) {
		mem, db := newStore(t)
		fs, hit := failPlace(mem, db)
		res := m8Boot(t, fs, db)
		if !hit() || !errors.Is(res.err, ErrSwappedBack) {
			t.Fatalf("the failing swap = %v (injected %t), want ErrSwappedBack", res.err, hit())
		}
		if got := treeHash(t, mem, cpArea(db)); got != res.checkpoint {
			t.Error("the swap-back changed the checkpoint area")
		}
		// A row the restarting boot writes before M8 (an earlier data step's,
		// say) is not in the refused copy: the restart must copy again.
		late := func(ctx context.Context, sdb *storage.DB) error {
			ks, _ := sdb.Keyspace("wayne")
			b := ks.NewBatch()
			if err := b.Set([]byte("story/late"), []byte("Hush, epilogue")); err != nil {
				return errors.Join(err, b.Close())
			}
			return b.Commit(ctx, storage.Sync)
		}
		next := m8BootWith(t, mem, db, late)
		if next.err != nil || next.runs != 1 || next.tag != "live" || next.rows != 121 || next.genAfter != next.genBefore {
			t.Fatalf("the boot after one swap-back = %+v, want M8 restarted from the compaction and finished with the late row", next)
		}
		js, err := newSwapper(t, mem, testBuild, testNow).rewriteJournals("wayne.rewrite")
		if err != nil || len(js) != 2 || js[0].last != StepApplied || js[1].last != StepSwappedBack {
			t.Errorf("journals = %+v, %v; want applied after swapped_back", js, err)
		}
	})

	t.Run("twice, then refused", func(t *testing.T) {
		mem, db := newStore(t)
		for i := range 2 {
			fs, hit := failPlace(mem, db)
			if res := m8Boot(t, fs, db); !hit() || !errors.Is(res.err, ErrSwappedBack) {
				t.Fatalf("swap %d = %v (injected %t), want ErrSwappedBack", i+1, res.err, hit())
			}
		}
		before := treeHash(t, mem, stateDir)
		res := m8Boot(t, mem, db)
		if !errors.Is(res.err, storage.ErrRewriteSwappedBack) {
			t.Fatalf("the boot after two swap-backs = %v, want storage.ErrRewriteSwappedBack", res.err)
		}
		if ok, _ := exists(mem, restorejournal.PendingPath(stateDir)); ok {
			t.Error("the refusal staged an intent")
		}
		js, _ := newSwapper(t, mem, testBuild, testNow).rewriteJournals("wayne.rewrite")
		if len(js) != 2 {
			t.Errorf("the refusal journaled: %+v", js)
		}
		_ = before
	})
}
