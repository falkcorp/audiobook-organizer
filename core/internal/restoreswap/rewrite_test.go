// file: core/internal/restoreswap/rewrite_test.go
// version: 1.0.0
// guid: d92e319d-69bf-437e-a034-410415eaefa0
// last-edited: 2026-11-10

package restoreswap

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

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
