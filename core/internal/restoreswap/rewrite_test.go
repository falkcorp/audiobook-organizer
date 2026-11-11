// file: core/internal/restoreswap/rewrite_test.go
// version: 1.0.3
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
	"slices"
	"strings"
	"sync"
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
			rec = stagedRecord(in)
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
	return m8BootIn(t, fs, m8Env{}, db, before)
}

// m8Env is where an M8 boot runs: its state directory ("" = stateDir) and
// its free-space margin (0 = 1 byte).
type m8Env struct {
	state  string
	margin uint64
}

// m8BootIn is m8BootWith in env.
func m8BootIn(t *testing.T, fs vfs.FS, env m8Env, db string, before func(ctx context.Context, sdb *storage.DB) error) m8Result {
	t.Helper()
	state, margin := env.state, env.margin
	if state == "" {
		state = stateDir
	}
	if margin == 0 {
		margin = 1
	}
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: fs, BuildID: testBuild, Clock: testNow})
	s, err := New(Options{FS: fs, StateDir: state, BuildID: testBuild, Clock: testNow, Owners: []storage.OwnerSpec{wayne(1)}, RewriteStep: "wayne.rewrite"})
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
	op, err := storage.Open(ctx, storage.OpenOptions{Path: db, Build: testBuild, Owners: []storage.OwnerSpec{wayne(1)}, FreeSpaceMargin: margin,
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

// checkpointPointer is the name <db>.migration-checkpoint holds.
func checkpointPointer(t *testing.T, fs vfs.FS, db string) string {
	t.Helper()
	f, err := fs.Open(db + restorejournal.CheckpointSuffix)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(f)
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Name
}

// checkpointRecords lists the checkpoint area's <name>.json records.
func checkpointRecords(t *testing.T, fs vfs.FS, db string) []string {
	t.Helper()
	names, err := fs.List(filepath.Join(filepath.Dir(db), ".mediacore-checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range names {
		if strings.HasSuffix(n, ".json") {
			out = append(out, strings.TrimSuffix(n, ".json"))
		}
	}
	return out
}

// TestRewriteRebootAfterSwapReusesTheCheckpoint (05-storage §5S.3.13 phase
// 3, §5S.3.12 "Sources" and "Free space"), on the real filesystem, where the
// copy moved into place has a new inode (MemFS reports every directory as
// 0/0, so no in-memory test can see this): a boot killed after M8's swap,
// in phase 4 or before the checkpoint record is promoted to the copy's
// identity, is rebooted with a free-space margin no disk has, so a new
// checkpoint would be refused. The reboot reuses the attempt's checkpoint
// (one record, the pointer still naming it), M8 finishes, the store keeps
// the generation it had before M8, and neither the copy nor the swap's
// in-flight file is left.
func TestRewriteRebootAfterSwapReusesTheCheckpoint(t *testing.T) {
	kills := map[string]func(db string) func(errorfs.Op) bool{
		"in phase 4": func(string) func(errorfs.Op) bool {
			return func(op errorfs.Op) bool {
				return op.Kind == errorfs.OpCreate && strings.HasPrefix(filepath.Base(op.Path), "."+rewriteDeleteStarted+".tmp-")
			}
		},
		"before the promotion": func(db string) func(errorfs.Op) bool {
			cp := filepath.Join(filepath.Dir(db), ".mediacore-checkpoints")
			var mu sync.Mutex
			swapped := false
			return func(op errorfs.Op) bool {
				mu.Lock()
				defer mu.Unlock()
				if op.Kind == errorfs.OpRename && filepath.Clean(op.Path) == storage.RewriteDir(db) {
					swapped = true
					return false
				}
				return swapped && op.Kind == errorfs.OpCreate && filepath.Dir(op.Path) == cp
			}
		},
	}
	for name, kill := range kills {
		t.Run(name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			db := filepath.Join(state, "main.pebble")
			m8Store(t, vfs.Default, db, 60)
			fs, hit := failOnce(vfs.Default, kill(db))
			first := m8BootIn(t, fs, m8Env{state: state}, db, nil)
			if !hit() || first.err == nil {
				t.Fatalf("the killed boot = %v (injected %t), want it to fail after the swap", first.err, hit())
			}
			cp := checkpointPointer(t, vfs.Default, db)
			next := m8BootIn(t, vfs.Default, m8Env{state: state, margin: 1 << 62}, db, nil)
			if next.err != nil {
				t.Fatalf("the reboot after the swap: %v", next.err)
			}
			if next.runs != 1 || next.tag != "live" || next.rows != 60 {
				t.Errorf("the reboot = %+v, want M8 finished over the copy with every row", next)
			}
			if first.genBefore == 0 || next.genAfter != first.genBefore {
				t.Errorf("generation %d before M8, %d after the reboot; a KindRewrite swap keeps it", first.genBefore, next.genAfter)
			}
			if got := checkpointPointer(t, vfs.Default, db); got != cp {
				t.Errorf("the pointer names %s after the reboot, want the attempt's %s", got, cp)
			}
			if recs := checkpointRecords(t, vfs.Default, db); len(recs) != 1 || recs[0] != cp {
				t.Errorf("checkpoint records = %v, want only the attempt's %s", recs, cp)
			}
			if ok, err := exists(vfs.Default, storage.RewriteDir(db)); err != nil || ok {
				t.Errorf("the copy's directory is still beside the store (%v)", err)
			}
			if _, ok, err := InFlight(vfs.Default, state); err != nil || ok {
				t.Errorf("a swap is still in flight (%v)", err)
			}
		})
	}
}

// TestRewriteCopyThatWillNotOpenWritableIsSwappedBack (05-storage
// §5S.3.13 phase 3, 05-backup §5B.3.4 KindRewrite rules): a copy moved into
// place, which the swap's sidecar step opened, but which the engine cannot
// reopen writable (its LOCK fails: the one write a Pebble open makes before
// the first flush) is swapped back at once and the original
// reopened, so the boot fails with ErrSwappedBack, the journal ends at
// swapped_back, and the next boot finds the original with every row and
// M8's record pending, and finishes M8.
func TestRewriteCopyThatWillNotOpenWritableIsSwappedBack(t *testing.T) {
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	db := dbPathOf("state")
	m8Store(t, mem, db, 40)
	// After the copy moves into place the swap's sidecar step opens it (its
	// first LOCK); the engine's reopen is the second.
	var mu sync.Mutex
	placed, locks := false, 0
	fs, hit := failOnce(mem, func(op errorfs.Op) bool {
		mu.Lock()
		defer mu.Unlock()
		if op.Kind == errorfs.OpRename && filepath.Clean(op.Path) == storage.RewriteDir(db) {
			placed = true
			return false
		}
		if !placed || op.Kind != errorfs.OpLock || filepath.Dir(filepath.Clean(op.Path)) != db {
			return false
		}
		locks++
		return locks == 2
	})
	res := m8Boot(t, fs, db)
	if !hit() || !errors.Is(res.err, ErrSwappedBack) || !strings.Contains(res.err.Error(), "reopen the copy") {
		t.Fatalf("the boot whose copy will not open writable = %v (injected %t), want ErrSwappedBack caused by the reopen", res.err, hit())
	}
	js, err := newSwapper(t, mem, testBuild, testNow).rewriteJournals("wayne.rewrite", db)
	if err != nil || len(js) != 1 || js[0].last != StepSwappedBack {
		t.Fatalf("journals = %+v, %v; want one ending at swapped_back", js, err)
	}
	next := m8Boot(t, mem, db)
	if next.err != nil || next.runs != 1 || next.tag != "live" || next.rows != 40 || next.genAfter != next.genBefore {
		t.Fatalf("the boot after the swap-back = %+v, want the original served and M8 finished", next)
	}
}

// TestRewriteNewAttemptCountsNoEarlierSwapBack (§5S.3.13 "M8 never
// loops"): M8 counts only its attempt's swap-backs. Two swap-backs refuse
// the next boot of the same attempt; once that attempt's checkpoint is
// gone (pruned), the next boot takes a new one, a new attempt, and M8 runs
// once more and finishes.
func TestRewriteNewAttemptCountsNoEarlierSwapBack(t *testing.T) {
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	db := dbPathOf("state")
	m8Store(t, mem, db, 30)
	for i := range 2 {
		fs, hit := failPlace(mem, db)
		if res := m8Boot(t, fs, db); !hit() || !errors.Is(res.err, ErrSwappedBack) {
			t.Fatalf("swap %d = %v (injected %t), want ErrSwappedBack", i+1, res.err, hit())
		}
	}
	if res := m8Boot(t, mem, db); !errors.Is(res.err, storage.ErrRewriteSwappedBack) {
		t.Fatalf("the same attempt's next boot = %v, want storage.ErrRewriteSwappedBack", res.err)
	}
	attempt := checkpointPointer(t, mem, db)
	if err := errors.Join(mem.RemoveAll(filepath.Join(filepath.Dir(db), ".mediacore-checkpoints")),
		mem.Remove(db+restorejournal.CheckpointSuffix)); err != nil {
		t.Fatal(err)
	}
	next := m8Boot(t, mem, db)
	if next.err != nil || next.runs != 1 || next.rows != 30 || next.tag != "live" {
		t.Fatalf("the new attempt's boot = %+v, want M8 run once more and finished", next)
	}
	if got := checkpointPointer(t, mem, db); got == attempt {
		t.Errorf("the boot reused the pruned attempt's checkpoint name %s", got)
	}
}

// TestRewriteJournalsOfAReplacedStore (§5S.3.13 phases): M8's journals
// say what moved, and M8's record on the live store says whether that
// store is the copy. A finished M8's journal (applied) does not finish the
// M8 of a store that replaced it (restored by any path): that store reads
// pending, so M8 copies and swaps it again. A journal at swapped over a
// store that is not the copy halts M8 (ErrJournalCorrupt) rather than
// delete the set-aside and mark a store M8 never rewrote done.
func TestRewriteJournalsOfAReplacedStore(t *testing.T) {
	newStore := func(t *testing.T) (*vfs.MemFS, string) {
		mem := vfs.NewMem()
		mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
		db := dbPathOf("state")
		m8Store(t, mem, db, 25)
		return mem, db
	}

	t.Run("applied", func(t *testing.T) {
		mem, db := newStore(t)
		if res := m8Boot(t, mem, db); res.err != nil || res.runs != 1 {
			t.Fatalf("the first M8 = %+v", res)
		}
		if err := mem.RemoveAll(db); err != nil {
			t.Fatal(err)
		}
		m8Store(t, mem, db, 25) // a store from before M8, in its place
		res := m8Boot(t, mem, db)
		if res.err != nil || res.runs != 1 || res.rows != 25 {
			t.Fatalf("the replaced store's M8 = %+v", res)
		}
		js, err := newSwapper(t, mem, testBuild, testNow).rewriteJournals("wayne.rewrite", db)
		if err != nil || len(js) != 2 || js[0].last != StepApplied || js[1].last != StepApplied {
			t.Errorf("journals = %+v, %v; want the replaced store copied and swapped again", js, err)
		}
	})

	t.Run("swapped over a store that is not the copy", func(t *testing.T) {
		mem, db := newStore(t)
		s, err := New(Options{FS: mem, StateDir: stateDir, BuildID: testBuild, Clock: testNow, Owners: []storage.OwnerSpec{wayne(1)}, RewriteStep: "wayne.rewrite"})
		if err != nil {
			t.Fatal(err)
		}
		in := restorejournal.Intent{ID: "rewrite-19390501T120000.000000000Z", Kind: restorejournal.KindRewrite, DBPath: db,
			Targets:       []restorejournal.Target{{From: storage.RewriteDir(db), To: db}},
			StagedByBuild: testBuild, ExpiresAt: testNow().Add(rewriteExpiry), RequestedBy: "core/storage wayne.rewrite",
			RequestedAt: testNow(), StepID: "wayne.rewrite"}
		journalOf(t, s, in, StepPreviousMoved, StepStagedMoved, StepSidecars, StepSwapped)
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(mem, s.inflightPath(in.ID), raw, privateFile); err != nil {
			t.Fatal(err)
		}
		if res := m8Boot(t, mem, db); !errors.Is(res.err, ErrJournalCorrupt) || res.runs != 1 {
			t.Fatalf("M8 over a store the swapped journal does not describe = %+v, want ErrJournalCorrupt", res)
		}
	})
}

// journalOf writes journal id under the swapper's state directory: its
// staged record (for intent in) and then steps.
func journalOf(t *testing.T, s *Swapper, in restorejournal.Intent, steps ...string) {
	t.Helper()
	ctx := context.Background()
	if err := s.mkdirDurable(s.journalDir(in.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.appendRec(ctx, in.ID, stagedRecord(in)); err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: step}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRetireRewriteJournals (§5S.3.13 "M8 never loops", §5S.3.12 "In
// place"): restore-offline retires every KindRewrite journal of the store
// it replaced that ended (applied or swapped_back), whatever its step ID,
// and no other: another store's, another kind's, a rejected one. M8 then
// reads none of them. A journal that does not read is skipped when its
// first record proves it is not M8's of this store, and halts M8 when it
// may be.
func TestRetireRewriteJournals(t *testing.T) {
	ctx := context.Background()
	db := dbPathOf("state")
	other := filepath.Join(filepath.Dir(db), "arkham.pebble")
	rw := func(id, dbPath, step string) restorejournal.Intent {
		return restorejournal.Intent{ID: id, Kind: restorejournal.KindRewrite, DBPath: dbPath, StepID: step, Attempt: "19390501T120000Z-0000beef"}
	}
	setup := func(t *testing.T) (*vfs.MemFS, *Swapper) {
		mem := vfs.NewMem()
		s := newSwapper(t, mem, testBuild, testNow)
		journalOf(t, s, rw("rewrite-a", db, "wayne.rewrite"), StepSwappedBack)
		journalOf(t, s, rw("rewrite-b", db, "wayne.rewrite"), StepSwapped, StepOpened, StepApplied)
		journalOf(t, s, rw("rewrite-c", db, "robin.rewrite"), StepSwappedBack)
		journalOf(t, s, rw("rewrite-d", other, "wayne.rewrite"), StepSwappedBack)
		journalOf(t, s, rw("rewrite-e", db, "wayne.rewrite"), StepRejected)
		journalOf(t, s, restorejournal.Intent{ID: "restore-f", Kind: restorejournal.KindArchive, DBPath: db}, StepApplied)
		return mem, s
	}
	last := func(t *testing.T, mem vfs.FS, id string) string {
		t.Helper()
		recs, err := ReadJournal(mem, stateDir, id)
		if err != nil {
			t.Fatal(err)
		}
		return lastStep(recs)
	}
	// corrupt appends an undecodable line and then a good one to id's
	// journal, so ReadJournal refuses it (ErrJournalCorrupt).
	corrupt := func(t *testing.T, mem vfs.FS, id string) {
		t.Helper()
		p := journalPath(stateDir, id)
		raw := readAll(t, mem, p) + "{the riddler was here\n" + `{"step":"swapped_back"}` + "\n"
		if err := writeFileAtomic(mem, p, []byte(raw), privateFile); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("retires this store's ended journals", func(t *testing.T) {
		mem, s := setup(t)
		if err := s.retireRewriteJournals(ctx, db, "restore-x"); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{
			"rewrite-a": StepRetired, "rewrite-b": StepRetired, "rewrite-c": StepRetired,
			"rewrite-d": StepSwappedBack, "rewrite-e": StepRejected, "restore-f": StepApplied,
		} {
			if got := last(t, mem, id); got != want {
				t.Errorf("%s ends %q after the retire, want %q", id, got, want)
			}
		}
		if js, err := s.rewriteJournals("wayne.rewrite", db); err != nil || len(js) != 0 {
			t.Errorf("M8 reads %+v, %v after the retire, want nothing", js, err)
		}
		if js, err := s.rewriteJournals("wayne.rewrite", other); err != nil || len(js) != 1 || js[0].id != "rewrite-d" {
			t.Errorf("the other store's M8 reads %+v, %v, want its own journal", js, err)
		}
	})

	t.Run("reads only this step and store", func(t *testing.T) {
		_, s := setup(t)
		js, err := s.rewriteJournals("wayne.rewrite", db+"/")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, j := range js {
			ids = append(ids, j.id)
			if j.attempt != "19390501T120000Z-0000beef" {
				t.Errorf("%s's attempt = %q", j.id, j.attempt)
			}
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"rewrite-a", "rewrite-b"}) {
			t.Errorf("M8 wayne.rewrite of %s reads %v, want rewrite-a and rewrite-b", db, ids)
		}
	})

	t.Run("an unreadable journal proven another's is skipped", func(t *testing.T) {
		mem, s := setup(t)
		for _, id := range []string{"rewrite-c", "rewrite-d", "restore-f"} {
			corrupt(t, mem, id)
		}
		if js, err := s.rewriteJournals("wayne.rewrite", db); err != nil || len(js) != 2 {
			t.Errorf("M8 reads %+v, %v beside unreadable journals of other steps, stores and kinds", js, err)
		}
	})

	t.Run("an unreadable journal that may be M8's halts it", func(t *testing.T) {
		mem, s := setup(t)
		corrupt(t, mem, "rewrite-a")
		if _, err := s.rewriteJournals("wayne.rewrite", db); !errors.Is(err, ErrJournalCorrupt) {
			t.Errorf("M8 beside its own unreadable journal = %v, want ErrJournalCorrupt", err)
		}
		if err := s.retireRewriteJournals(ctx, db, "restore-x"); !errors.Is(err, ErrJournalCorrupt) {
			t.Errorf("the retire beside an unreadable journal of this store = %v, want ErrJournalCorrupt", err)
		}
	})
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
			js, err := s.rewriteJournals("wayne.rewrite", db)
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
		js, err := newSwapper(t, mem, testBuild, testNow).rewriteJournals("wayne.rewrite", db)
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
		js, _ := newSwapper(t, mem, testBuild, testNow).rewriteJournals("wayne.rewrite", db)
		if len(js) != 2 {
			t.Errorf("the refusal journaled: %+v", js)
		}
		_ = before
	})
}
