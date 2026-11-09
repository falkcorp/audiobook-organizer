// file: internal/database/standalone_swap_test.go
// version: 1.0.1
// guid: 96b69961-40ee-480a-8fb1-be5e87dbe976
// last-edited: 2026-11-09

package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// treeState is every entry under root with its size and modification time,
// sorted: what a refused open must leave exactly as it found it.
func treeState(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		size := fi.Size()
		if d.IsDir() {
			size = 0
		}
		out = append(out, fmt.Sprintf("%s %d %d", rel, size, fi.ModTime().UnixNano()))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// TestStandaloneOpenerHonoursInflightSwap (R1-23 review S5; R1-25 step 7):
// the standalone opener has no P0a, so with a swap in flight
// (restore/<id>/inflight.json) or staged (restore/pending.json) under the
// state directory it refuses with restorejournal.ErrSwapPending, naming the
// token and the fix, before it opens, checkpoints or migrates anything: the
// store's directory and its parent are byte-for-byte as they were. A slot
// it cannot read refuses too (fail closed), and an empty slot opens.
func TestStandaloneOpenerHonoursInflightSwap(t *testing.T) {
	// stage writes an archive restore's intent for the store at dbPath into
	// stateDir's slot, and moves it in flight when inflight is set, as the
	// swap's own slot move does.
	stage := func(t *testing.T, stateDir, dbPath string, inflight bool) {
		t.Helper()
		in := restorejournal.Intent{
			ID: "20261108T000000Z-batcave1", Kind: restorejournal.KindArchive, DBPath: dbPath,
			Targets: []restorejournal.Target{{From: restorejournal.StagingDir(dbPath, "20261108T000000Z-batcave1") + "/db", To: dbPath}},
		}
		if err := restorejournal.CreateIntent(stateDir, in); err != nil {
			t.Fatal(err)
		}
		if !inflight {
			return
		}
		dir := restorejournal.JournalDir(stateDir, in.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(restorejournal.PendingPath(stateDir), filepath.Join(dir, "inflight.json")); err != nil {
			t.Fatal(err)
		}
	}
	cells := []struct {
		name    string
		slot    func(t *testing.T, stateDir, dbPath string)
		token   string // in the refusal; "" for a refusal that is not ErrSwapPending
		refused bool
	}{
		{"in flight", func(t *testing.T, s, db string) { stage(t, s, db, true) }, restorejournal.TokenRestoreInflight, true},
		{"staged", func(t *testing.T, s, db string) { stage(t, s, db, false) }, restorejournal.TokenRestorePending, true},
		{"unreadable slot", func(t *testing.T, s, _ string) {
			// pending.json that cannot be read as a file.
			if err := os.MkdirAll(restorejournal.PendingPath(s), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "", true},
		{"empty slot", func(*testing.T, string, string) {}, "", false},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			dbPath := filepath.Join(parent, "gotham.pebble")
			first, err := NewPebbleStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.CreateAuthor("Selina Kyle"); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			stateDir := t.TempDir()
			c.slot(t, stateDir, dbPath)
			before := treeState(t, parent)

			s, err := OpenStandalone(dbPath, StandaloneOptions{StateDir: stateDir})
			if !c.refused {
				if err != nil {
					t.Fatalf("open with an empty slot: %v", err)
				}
				if a, err := s.GetAuthorByName("Selina Kyle"); err != nil || a == nil {
					t.Fatalf("the store's row after the open: %v, %v", a, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				if cerr := s.Close(); cerr != nil {
					t.Log(cerr)
				}
				t.Fatal("OpenStandalone opened the store with the swap slot busy or unreadable")
			}
			if c.token != "" {
				if !errors.Is(err, restorejournal.ErrSwapPending) {
					t.Fatalf("refusal = %v, want restorejournal.ErrSwapPending", err)
				}
				for _, want := range []string{c.token, "20261108T000000Z-batcave1", "boot"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q does not name %q", err, want)
					}
				}
			}
			if after := treeState(t, parent); !slices.Equal(before, after) {
				t.Fatalf("a refused open changed the store's directory:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// TestVerifyStoreOpens (05-backup's VerifyOpen, R1's form; the restore
// stager's check of an extracted archive): a store books' guard admits opens
// read-only and nothing under it changes; an empty directory and a missing
// one are refused, and the missing one is not created.
func TestVerifyStoreOpens(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "gotham.pebble")
	s, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAuthor("Harvey Dent"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Pebble's directory lock is taken read-only too, which touches LOCK's
	// modification time; nothing else may change.
	unlocked := func(s []string) []string {
		return slices.DeleteFunc(s, func(e string) bool { return strings.HasPrefix(e, "LOCK ") })
	}
	before := unlocked(treeState(t, dir))
	if err := VerifyStoreOpens(ctx, dir); err != nil {
		t.Fatalf("a served store: %v", err)
	}
	if after := unlocked(treeState(t, dir)); !slices.Equal(before, after) {
		t.Fatalf("the check wrote to the store:\nbefore %v\nafter  %v", before, after)
	}
	empty := t.TempDir()
	if err := VerifyStoreOpens(ctx, empty); err == nil {
		t.Fatal("an empty directory passed")
	}
	missing := filepath.Join(t.TempDir(), "arkham.pebble")
	if err := VerifyStoreOpens(ctx, missing); err == nil {
		t.Fatal("a missing directory passed")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the check created %s (%v)", missing, err)
	}
}
