// file: core/library/review_p2_test.go
// version: 1.1.0
// guid: a9d0b21f-80d1-4624-8e39-94f7e4871e5d
// last-edited: 2026-11-11

package library

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// Every fact sourceUnchanged checks has a case in which it is the only one that
// can tell, so none of them can be dropped without a test noticing.
func TestSourceUnchangedChecksEachFact(t *testing.T) {
	const content = "the bytes of the Joke"
	build := func(t *testing.T) (r *rig, root *Root, abs string, src Path, stamp srcStamp) {
		t.Helper()
		r, _ = transferRig(t, rigOpts{})
		src = r.put("import-1", "Arkham/a.m4b", content)
		abs = r.abs("import-1", src.Rel)
		var err error
		if root, err = r.m.Root("import-1"); err != nil {
			t.Fatal(err)
		}
		st, err := os.Lstat(abs)
		if err != nil {
			t.Fatal(err)
		}
		dev, ino, _ := libfs.InodeOf(st)
		return r, root, abs, src, srcStamp{dev: dev, ino: ino, size: st.Size(), mtime: st.ModTime().UnixNano()}
	}
	sha := sum(content)
	check := func(t *testing.T, r *rig, root *Root, src Path, stamp srcStamp, strong bool) (bool, error) {
		t.Helper()
		return r.m.sourceUnchanged(ctxbg(), root.rw, src, stamp, sha, strong)
	}

	t.Run("unchanged", func(t *testing.T) {
		r, root, _, src, stamp := build(t)
		if present, err := check(t, r, root, src, stamp, true); !present || err != nil {
			t.Errorf("= %v, %v", present, err)
		}
	})
	t.Run("gone", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if err := os.Remove(abs); err != nil {
			t.Fatal(err)
		}
		if present, err := check(t, r, root, src, stamp, true); present || err != nil {
			t.Errorf("= %v, %v; want absent without an error", present, err)
		}
	})
	t.Run("a link to a file with the same bytes", func(t *testing.T) {
		// Nothing recorded but the size, which a link's own length can match; the
		// digest, read through the link, matches too. Only "is it a regular file"
		// can say no.
		r, root, abs, src, _ := build(t)
		if err := os.Rename(abs, abs+".real"); err != nil {
			t.Fatal(err)
		}
		target := filepath.Base(abs) + ".real"
		if err := os.Symlink(target, abs); err != nil {
			t.Fatal(err)
		}
		_, err := check(t, r, root, src, srcStamp{size: int64(len(target))}, true)
		if !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged", err)
		}
	})
	t.Run("another file of the same size and time", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if err := os.Remove(abs); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o664); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(0, stamp.mtime)
		if err := os.Chtimes(abs, mt, mt); err != nil {
			t.Fatal(err)
		}
		if _, err := check(t, r, root, src, stamp, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (another inode)", err)
		}
	})
	t.Run("a different size, nothing else recorded", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if err := os.WriteFile(abs, []byte(content+", and a new chapter"), 0o664); err != nil {
			t.Fatal(err)
		}
		if _, err := check(t, r, root, src, srcStamp{size: stamp.size}, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (size)", err)
		}
	})
	t.Run("a different modification time", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		later := time.Unix(0, stamp.mtime).Add(time.Hour)
		if err := os.Chtimes(abs, later, later); err != nil {
			t.Fatal(err)
		}
		if _, err := check(t, r, root, src, stamp, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (mtime)", err)
		}
	})
	t.Run("other bytes of the same size, inode and time", func(t *testing.T) {
		// Written in place and the time put back: only the digest can tell.
		r, root, abs, src, stamp := build(t)
		if err := os.WriteFile(abs, []byte(strings.ToUpper(content)), 0o664); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(0, stamp.mtime)
		if err := os.Chtimes(abs, mt, mt); err != nil {
			t.Fatal(err)
		}
		if present, err := check(t, r, root, src, stamp, false); !present || err != nil {
			t.Fatalf("without the digest the file looks unchanged: %v, %v", present, err)
		}
		if _, err := check(t, r, root, src, stamp, true); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("with the digest = %v, want ErrSourceChanged", err)
		}
	})
}

// Recovery removes a cross-device Move's source only after reading it again: a
// file rewritten in place, with its size and time put back, is not the file that
// was copied.
func TestRecoveryKeepsASourceRewrittenInPlace(t *testing.T) {
	r, _, _ := crossDeviceMoveKilledBeforeSourceRemoval(t)
	abs := r.abs("import-1", "Arkham/a.m4b")
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	const rewritten = "THE ORIGINAL BYTES" // the same 18 bytes
	if int64(len(rewritten)) != st.Size() {
		t.Fatalf("fixture: %d bytes, want %d", len(rewritten), st.Size())
	}
	if err := os.WriteFile(abs, []byte(rewritten), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abs, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	r.m = r.build(rigOpts{})
	rep, rerr := r.m.Recover(ctxbg())
	if rerr != nil || rep.Conflicts != 1 {
		t.Fatalf("Recover = %v, %v; want one conflict", rep, rerr)
	}
	if got, _ := r.read("import-1", "Arkham/a.m4b"); got != rewritten {
		t.Errorf("the rewritten source was removed or changed: %q", got)
	}
	if got, _ := r.read("books-library", "a.m4b"); got != "the original bytes" {
		t.Errorf("destination = %q", got)
	}
}

// A temp the cleanup could not remove is still owed: the row stays, says so, and
// recovery removes it.
func TestTempThatCannotBeRemovedKeepsTheRow(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		switch {
		case strings.Contains(label, ":link:"):
			return errors.New("the Batcave link failed")
		case strings.Contains(label, ":remove:") && strings.Contains(label, tmpPrefix):
			return errors.New("the Batcave is on fire")
		}
		return nil
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Moore/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts()))
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Transfer = %v, want ErrRecoveryPending", err)
	}
	if r.journalRows() != 1 || len(r.strays()) != 1 {
		t.Fatalf("rows %d, strays %v: the row must stay while a temp does", r.journalRows(), r.strays())
	}
	r.m = r.build(rigOpts{})
	if rep, rerr := r.m.Recover(ctxbg()); rerr != nil || rep.RolledBack != 1 || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 || r.exists(dst.Root, dst.Rel) || !r.exists(src.Root, src.Rel) {
		t.Errorf("after recovery: strays %v, rows %d, dst %v, src %v", r.strays(), r.journalRows(), r.exists(dst.Root, dst.Rel), r.exists(src.Root, src.Rel))
	}
}

// A Move has no temp, so a placeholder it cannot take away is the only thing the
// row is kept for.
func TestMoveWhosePlaceholderCannotBeRemovedKeepsTheRow(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") || label == "books-library:remove:Moore/joke.m4b" {
			return errors.New("the Batcave is on fire")
		}
		return noAtomicRename(label)
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Moore/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(move()))
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Transfer = %v, want ErrRecoveryPending", err)
	}
	if got := r.placingRows(); len(got) != 1 || got[0] == nil || !r.exists(dst.Root, dst.Rel) || !r.exists(src.Root, src.Rel) {
		t.Fatalf("placing %v, placeholder %v, source %v", got, r.exists(dst.Root, dst.Rel), r.exists(src.Root, src.Rel))
	}
	r.m = r.build(rigOpts{})
	if rep, rerr := r.m.Recover(ctxbg()); rerr != nil || rep.RolledBack != 1 || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if r.exists(dst.Root, dst.Rel) || !r.exists(src.Root, src.Rel) || r.journalRows() != 0 {
		t.Errorf("after recovery: placeholder %v, source %v, rows %d", r.exists(dst.Root, dst.Rel), r.exists(src.Root, src.Rel), r.journalRows())
	}
}

// A Replace whose publish fails puts the old file back, on a filesystem where even
// that restore has to reserve the name first.
func TestReplaceRestoresThroughAPlaceholder(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") && strings.Contains(label, tmpPrefix) {
			return errors.New("the Batcave is on fire") // the new bytes cannot be put in place
		}
		return neither(label)
	}
	const old = "the old edition of the Joke"
	r.put("books-library", "Moore/joke.m4b", old)
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Moore/joke.m4b"}
	ro := withMkdir(copyOpts())
	ro.Replace = &ReplaceSpec{ExpectSHA256: sum(old)}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, ro); err == nil {
		t.Fatal("Transfer succeeded")
	}
	if got, _ := r.read(dst.Root, dst.Rel); got != old {
		t.Errorf("the old file was not put back: %q", got)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 || !r.exists(src.Root, src.Rel) {
		t.Errorf("strays %v, rows %d, source %v", r.strays(), r.journalRows(), r.exists(src.Root, src.Rel))
	}
}

// A Release that fails and cannot remove its placeholder leaves the record
// "releasing" with the placeholder named, so recovery removes it; a record put
// back to "held" would leave the empty file at the target for good.
func TestReleaseWhosePlaceholderCannotBeRemovedStaysReleasing(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = noAtomicRename
	id, err := r.m.Quarantine(ctxbg(), books, r.put("books-library", "Gotham/held.m4b", gas), "damaged")
	if err != nil {
		t.Fatal(err)
	}
	to := Path{Root: "books-library", Rel: "Gotham/back.m4b"}
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") || label == "books-library:remove:Gotham/back.m4b" {
			return errors.New("the Batcave is on fire")
		}
		return noAtomicRename(label)
	}
	if _, err := r.m.Release(ctxbg(), books, id, &to); err == nil {
		t.Fatal("Release succeeded")
	}
	row, err := r.m.loadQuarantine(ctxbg(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != qReleasing || row.Placing == nil || !r.exists(to.Root, to.Rel) {
		t.Fatalf("record %s placing %v, placeholder %v: want releasing with the placeholder named", row.State, row.Placing, r.exists(to.Root, to.Rel))
	}
	r.m = r.build(rigOpts{})
	if rep, rerr := r.m.Recover(ctxbg()); rerr != nil || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if r.exists(to.Root, to.Rel) {
		t.Error("recovery left the placeholder at the release target")
	}
	row, err = r.m.loadQuarantine(ctxbg(), id)
	if err != nil || row.State != qHeld || row.Placing != nil {
		t.Errorf("after recovery: %v, %v; want held", row, err)
	}
}

// The source leaf of a rename is judged by identity from its directory's
// descriptor, after the name check: a directory swapped for a remembered area
// between the two is refused. The swap happens when the destination is checked,
// which is after the source's name check and before the act.
func TestRenameSourceSwappedAfterItsNameCheckIsRefusedByDescriptor(t *testing.T) {
	var hook func(stage, name string)
	s := spec("books-library", books, RoleLibrary)
	s.Quarantine = ".failed"
	r := newRig(t, newStore(t), rigOpts{seams: seams{fsRace: func(stage, name string) {
		if hook != nil {
			hook(stage, name)
		}
	}}}, s)
	r.put("books-library", ".failed/held.m4b", "held bytes")
	if err := os.Mkdir(r.abs("books-library", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("x", "y"); err != nil { // remembers the area's identity
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("y", "x"); err != nil {
		t.Fatal(err)
	}
	swapped := false
	hook = func(stage, name string) {
		if stage == "checked" && !swapped && name == "elsewhere" {
			swapped = true
			if err := os.Remove(r.abs("books-library", "x")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(r.abs("books-library", ".failed"), r.abs("books-library", "x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := fsys.RenameNoReplace("x", "elsewhere"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("rename of a source swapped for the quarantine area = %v, want ErrReservedPath", err)
	}
	if !swapped {
		t.Fatal("the hook never ran")
	}
	if !r.exists("books-library", "x/held.m4b") || r.exists("books-library", "elsewhere") {
		t.Error("the area moved")
	}
}

// A directory that HOLDS an area is carried along by a rename, so the leaf of a
// rename is refused by identity at the act as well as by name before it: a name
// swapped for the area-holding directory after the name check. The identity is
// faked (a second name for the directory a case-insensitive volume or a bind
// mount would give) only once the name check has passed, so only the act's own
// check can see it.
func TestRenameLeafHoldingAnAreaIsRefusedByIdentityAtTheAct(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	leaf := filepath.Join(dir, "x")
	seam := func(i fs.FileInfo) (uint64, uint64, bool) {
		if armed.Load() && i.Name() == "state" {
			// "state" now answers with the identity of x: x is a second name for it.
			st, err := os.Lstat(leaf)
			if err != nil {
				return 0, 0, false
			}
			return libfs.InodeOf(st)
		}
		return libfs.InodeOf(i)
	}
	s := spec("books-library", books, RoleLibrary)
	s.Path, s.AppDir = dir, "state/app"
	r := newRig(t, newStore(t), rigOpts{seams: seams{inodeOf: seam, fsRace: func(stage, name string) {
		if stage == "checked" && name == "elsewhere" {
			armed.Store(true)
		}
	}}}, s)
	for _, d := range []string{"x", "y"} {
		if err := os.Mkdir(r.abs("books-library", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("y", "z"); err != nil { // an ordinary directory moves
		t.Fatalf("rename of an ordinary directory = %v", err)
	}
	if err := fsys.RenameNoReplace("x", "elsewhere"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("rename of a leaf that is a second name for the area's holder = %v, want ErrReservedPath", err)
	}
	if !armed.Load() {
		t.Fatal("the hook never ran")
	}
	if !r.exists("books-library", "x") || r.exists("books-library", "elsewhere") {
		t.Error("the directory moved")
	}
}

// renameFresh is for a name nobody else can be making (a ULID names it), and
// where the filesystem has no atomic no-replace rename it checks the name is
// absent: it never replaces a name that is there.
func TestRenameFreshNeverReplacesAnExistingName(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = noAtomicRename
	r.put("books-library", "Gotham/held.m4b", "the one being moved")
	r.put("books-library", "Gotham/taken.m4b", "a name that must keep its bytes")
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	err = r.m.renameFresh(root.rw, "Gotham/held.m4b", root.rw, "Gotham/taken.m4b")
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("renameFresh onto an existing name = %v, want fs.ErrExist", err)
	}
	if got, _ := r.read("books-library", "Gotham/taken.m4b"); got != "a name that must keep its bytes" {
		t.Errorf("the existing name was replaced: %q", got)
	}
	if !r.exists("books-library", "Gotham/held.m4b") {
		t.Error("the source was moved")
	}
	// Where the name is free it is a plain rename, with no placeholder made.
	if err := r.m.renameFresh(root.rw, "Gotham/held.m4b", root.rw, "Gotham/free.m4b"); err != nil {
		t.Fatalf("renameFresh onto a free name = %v", err)
	}
	if got, _ := r.read("books-library", "Gotham/free.m4b"); got != "the one being moved" || r.exists("books-library", "Gotham/held.m4b") {
		t.Errorf("free name holds %q, source still there %v", got, r.exists("books-library", "Gotham/held.m4b"))
	}
}
