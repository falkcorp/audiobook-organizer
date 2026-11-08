// file: core/internal/restoreswap/failclosed_unix_test.go
// version: 1.0.0
// guid: ee200a1f-b05c-472e-bac6-f461798b596c
// last-edited: 2026-11-08

//go:build unix

package restoreswap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// otherFS is a filesystem that is neither the real one nor in memory as
// vfs.Root sees it: it writes real files through vfs.Default, and the
// Unwrap it promotes from vfs.Default returns nil, so vfs.Root stops at the
// wrapper. Nothing in it is trusted the way either known root is.
type otherFS struct{ vfs.FS }

func newOtherFS(t *testing.T) vfs.FS {
	t.Helper()
	w := otherFS{vfs.Default}
	if root := vfs.Root(w); root == vfs.Default {
		t.Fatal("vfs.Root sees through otherFS to vfs.Default; the fixture tests nothing")
	} else if _, mem := root.(*vfs.MemFS); mem {
		t.Fatal("vfs.Root sees otherFS as in memory; the fixture tests nothing")
	}
	return w
}

// TestOtherFSNeverProvesATwin (R1-21 verification review): mv's twin proof
// is the caller's ghost only on the in-memory filesystem and inode identity
// only on the real one. On any other it is refused: two names of one file
// (a twin on the real filesystem) and two different files with a caller
// proof both halt with ErrSwapAmbiguous, and nothing is deleted.
func TestOtherFSNeverProvesATwin(t *testing.T) {
	w := newOtherFS(t)
	yes := func() (bool, error) { return true, nil }
	dir := t.TempDir()
	for _, d := range []string{"a", "b", "c", "d"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	linkSrc, linkDst := filepath.Join(dir, "a", "robin"), filepath.Join(dir, "b", "robin")
	if err := os.WriteFile(linkSrc, []byte("dick"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(linkSrc, linkDst); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(dir, "c", "robin"), filepath.Join(dir, "d", "robin")
	for p, body := range map[string]string{src: "jason", dst: "tim"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := mv(w, linkSrc, linkDst, memGhost); !errors.Is(err, ErrSwapAmbiguous) {
		t.Fatalf("a hard link's two names: %v, want ErrSwapAmbiguous", err)
	}
	if err := mv(w, src, dst, yes); !errors.Is(err, ErrSwapAmbiguous) {
		t.Fatalf("two different files with a caller proof: %v, want ErrSwapAmbiguous", err)
	}
	for p, want := range map[string]string{linkSrc: "dick", linkDst: "dick", src: "jason", dst: "tim"} {
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v; want %q kept", p, b, err, want)
		}
	}
}

// TestOtherFSRefusesToWriteWithoutAMode (SEC-9; R1-21 verification review):
// vfs.FS has no chmod, so a file created on a filesystem that is neither
// the real one nor in memory cannot be made 0600. Every create of the swap
// and the slot fails there before it writes a byte, and removes what it
// created: the journal's first append, its rewrite, an atomic write, and
// the intent's create.
func TestOtherFSRefusesToWriteWithoutAMode(t *testing.T) {
	w := newOtherFS(t)
	if err := restorejournal.ChmodFS(w, filepath.Join(t.TempDir(), "absent"), privateFile); err == nil {
		t.Fatal("ChmodFS on another filesystem succeeded")
	}
	h := newOSHost(t.TempDir())
	const id = "r-other"
	dir := restorejournal.JournalDir(h.state, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{FS: w, StateDir: h.state, BuildID: testBuild, Clock: testNow})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	journal := filepath.Join(dir, restorejournal.JournalName)
	if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepStaged}); err == nil {
		t.Fatal("the journal's first append succeeded without a mode")
	}
	wantAbsent(t, journal)
	// A journal with a torn tail is rewritten through a temp: refused before
	// the temp holds a byte, the temp removed, the journal as it was.
	torn := `{"step":"staged"}` + "\n" + `{"step":"rech`
	if err := os.WriteFile(journal, []byte(torn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepRechecked}); err == nil {
		t.Fatal("the journal's rewrite succeeded without a mode")
	}
	wantAbsent(t, filepath.Join(dir, "."+restorejournal.JournalName+".tmp"))
	if b, err := os.ReadFile(journal); err != nil || string(b) != torn {
		t.Fatalf("journal after the refused rewrite = %q, %v; want it as it was", b, err)
	}
	if err := writeFileAtomic(w, filepath.Join(dir, backStarted), nil, privateFile); err == nil {
		t.Fatal("an atomic write succeeded without a mode")
	}
	wantAbsent(t, filepath.Join(dir, backStarted))
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, DBPath: h.db,
		Targets:       []restorejournal.Target{{From: filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble"), To: h.db}},
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
	if err := restorejournal.CreateIntentFS(w, h.state, in); err == nil {
		t.Fatal("the intent's create succeeded without a mode")
	}
	wantAbsent(t, restorejournal.PendingPath(h.state))
	// Nothing but the journal (as it was) and the journal's directory is left
	// in restore/: no temp from any of the refused creates.
	for _, d := range []string{restorejournal.Dir(h.state), dir} {
		ents, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if n := e.Name(); n != id && n != restorejournal.JournalName {
				t.Fatalf("%s is left in %s after the refused creates", n, d)
			}
		}
	}
}

// TestRealFSAliasIsNeverATwin (R1-21 verification review): two paths that
// name one directory entry (a symlinked parent; a case-insensitive
// spelling) are the same inode to Lstat, but they are one link, not two:
// discarding "the second name" deletes the only one. A twin on the real
// filesystem is two links of one file in two directories; a file or a
// directory reached through a symlinked parent halts with ErrSwapAmbiguous
// and nothing is deleted. (twinProven also refuses any directory, for a
// bind mount's second parent; making one needs root, so no test here.)
func TestRealFSAliasIsNeverATwin(t *testing.T) {
	dir := t.TempDir()
	cave := filepath.Join(dir, "batcave")
	if err := os.MkdirAll(filepath.Join(cave, "vault"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cave, "cowl"), []byte("bruce"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "manor")
	if err := os.Symlink(cave, alias); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cowl", "vault"} {
		if err := mv(vfs.Default, filepath.Join(cave, name), filepath.Join(alias, name), nil); !errors.Is(err, ErrSwapAmbiguous) {
			t.Fatalf("%s through a symlinked parent: %v, want ErrSwapAmbiguous", name, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(cave, "cowl")); err != nil || string(b) != "bruce" {
		t.Fatalf("the file named twice = %q, %v; want it kept", b, err)
	}
	if fi, err := os.Stat(filepath.Join(cave, "vault")); err != nil || !fi.IsDir() {
		t.Fatalf("the directory named twice: %v; want it kept", err)
	}
	// Two links of one file in one directory: no swap rename makes them (each
	// moves an item to another directory), so they are not proven either.
	if err := os.Link(filepath.Join(cave, "cowl"), filepath.Join(cave, "mask")); err != nil {
		t.Fatal(err)
	}
	if err := mv(vfs.Default, filepath.Join(cave, "cowl"), filepath.Join(cave, "mask"), nil); !errors.Is(err, ErrSwapAmbiguous) {
		t.Fatalf("two links in one directory: %v, want ErrSwapAmbiguous", err)
	}
	for _, n := range []string{"cowl", "mask"} {
		if b, err := os.ReadFile(filepath.Join(cave, n)); err != nil || string(b) != "bruce" {
			t.Fatalf("%s = %q, %v; want it kept", n, b, err)
		}
	}
}

// wantAbsent fails t unless path does not exist.
func wantAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: %v, want it absent", path, err)
	}
}
