// file: core/library/placeholder_test.go
// version: 1.0.1
// guid: 9017606e-094c-466d-aee1-92253f306dc0
// last-edited: 2026-11-11

package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/falkcorp/mediacore/internal/libfs"
)

const gas = "laughing gas, one canister at a time"

// labelsOf lists the labels of the steps taken, as one string per step.
func (f *faults) labelsOf(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.labels {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}

// placingRows is the Placing of every journal row, in key order.
func (r *rig) placingRows() []*Path {
	r.t.Helper()
	rows, err := r.m.listTransfers(ctxbg())
	if err != nil {
		r.t.Fatal(err)
	}
	var out []*Path
	for _, row := range rows {
		out = append(out, row.Placing)
	}
	return out
}

// A filesystem that makes no hard links publishes by a no-replace rename: the
// temp is consumed, nothing is reserved, and the final name never exists empty.
func TestPublishWithoutHardLinksRenamesTheTemp(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = noHardLinks
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	res, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Did != Copy {
		t.Errorf("Did = %v, want Copy", res.Did)
	}
	if got, _ := r.read(dst.Root, dst.Rel); got != gas {
		t.Errorf("destination holds %q", got)
	}
	if !r.exists(src.Root, src.Rel) || len(r.strays()) != 0 || r.journalRows() != 0 {
		t.Errorf("source kept %v, strays %v, rows %d", r.exists(src.Root, src.Rel), r.strays(), r.journalRows())
	}
	if len(f.labelsOf(":create:Gordon/joke.m4b")) != 0 || len(f.labelsOf(":renameover:")) != 0 {
		t.Errorf("a rename that works needs no placeholder: %v", f.labels)
	}
	if len(f.labelsOf(":rename:")) == 0 {
		t.Errorf("the publish never fell back to a rename: %v", f.labels)
	}
}

// With neither a link nor an atomic rename the name is reserved first: an
// exclusive create at the destination, journaled before it exists, then the temp
// is renamed over it.
func TestPublishWithNeitherReservesTheNameAndJournalsIt(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	var seen []*Path
	f.inject = func(label string) error {
		if label == "books-library:create:Gordon/joke.m4b" {
			seen = r.placingRows()
		}
		return neither(label)
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts())); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] == nil || seen[0].Root != "books-library" || !strings.Contains(seen[0].Rel, tmpPrefix) {
		t.Errorf("the intent was not journaled before the placeholder was created: %v", seen)
	}
	if len(f.labelsOf(":renameover:")) != 1 {
		t.Errorf("want one rename over the placeholder: %v", f.labels)
	}
	if got, ok := r.read(dst.Root, dst.Rel); !ok || got != gas {
		t.Errorf("destination holds %q (readable %v): the placeholder's empty mode was not replaced", got, ok)
	}
	if st, err := os.Lstat(r.abs(dst.Root, dst.Rel)); err != nil || st.Mode().Perm() == 0 {
		t.Errorf("the destination still has the placeholder's mode: %v %v", st, err)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 {
		t.Errorf("strays %v, rows %d", r.strays(), r.journalRows())
	}
}

// Another writer who replaces the placeholder before the rename keeps their file:
// the placeholder's identity is the descriptor that created it.
func TestPlaceholderIsNeverRenamedOverAForeignFile(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	const penguin = "the Penguin got here first"
	dstPath := r.abs("books-library", "Gordon/joke.m4b")
	swapped := false
	f.inject = func(label string) error {
		if label == "close:Gordon/joke.m4b" && !swapped {
			swapped = true
			if err := os.Remove(dstPath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dstPath, []byte(penguin), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return neither(label)
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts()))
	if !swapped {
		t.Fatal("the swap never ran")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("Transfer = %v, want ErrExist", err)
	}
	if got, _ := r.read(dst.Root, dst.Rel); got != penguin {
		t.Errorf("the other writer's file was replaced: %q", got)
	}
	if !r.exists(src.Root, src.Rel) || len(r.strays()) != 0 || r.journalRows() != 0 {
		t.Errorf("source kept %v, strays %v, rows %d", r.exists(src.Root, src.Rel), r.strays(), r.journalRows())
	}
	if len(f.labelsOf(":renameover:")) != 0 {
		t.Error("renamed over a file that is not the placeholder")
	}
}

// A rename over the placeholder that fails takes the placeholder away again.
func TestFailedRenameOverRemovesThePlaceholder(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") {
			return errors.New("the Batcave is on fire")
		}
		return neither(label)
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts())); err == nil {
		t.Fatal("Transfer succeeded")
	}
	if r.exists(dst.Root, dst.Rel) {
		t.Error("the placeholder is still at the destination")
	}
	if !r.exists(src.Root, src.Rel) || len(r.strays()) != 0 || r.journalRows() != 0 {
		t.Errorf("source kept %v, strays %v, rows %d", r.exists(src.Root, src.Rel), r.strays(), r.journalRows())
	}
}

// If the placeholder cannot be taken away either, the row stays, the temp stays
// (it is how recovery tells the placeholder from a user's file), and recovery
// finishes the job.
func TestPlaceholderThatCannotBeRemovedIsLeftToRecovery(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") || label == "books-library:remove:Gordon/joke.m4b" {
			return errors.New("the Batcave is on fire")
		}
		return neither(label)
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts()))
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Transfer = %v, want ErrRecoveryPending", err)
	}
	if !r.exists(dst.Root, dst.Rel) || len(r.strays()) != 1 || r.journalRows() != 1 {
		t.Fatalf("placeholder %v, strays %v, rows %d: all three must remain for recovery", r.exists(dst.Root, dst.Rel), r.strays(), r.journalRows())
	}
	r.m = r.build(rigOpts{})
	rep, rerr := r.m.Recover(ctxbg())
	if rerr != nil || rep.Failed != 0 || rep.Conflicts != 0 || rep.RolledBack != 1 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if r.exists(dst.Root, dst.Rel) || len(r.strays()) != 0 || r.journalRows() != 0 || !r.exists(src.Root, src.Rel) {
		t.Errorf("after recovery: dst %v, strays %v, rows %d, src %v", r.exists(dst.Root, dst.Rel), r.strays(), r.journalRows(), r.exists(src.Root, src.Rel))
	}
}

// Recovery removes a placeholder only when the path being renamed there still
// exists. A foreign non-empty file at the destination is never touched.
func TestRecoveryLeavesAForeignFileWhereAPlaceholderWasJournaled(t *testing.T) {
	dry, df := transferRig(t, rigOpts{})
	df.inject = neither
	dsrc := dry.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	if _, err := dry.m.Transfer(ctxbg(), books, dsrc, dst, withMkdir(copyOpts())); err != nil {
		t.Fatal(err)
	}
	kill := slices.Index(df.labels, "books-library:create:Gordon/joke.m4b") + 1
	if kill == 0 {
		t.Fatalf("no placeholder create in %v", df.labels)
	}
	r, f := transferRig(t, rigOpts{})
	f.inject = neither
	f.killAt = kill // the placeholder is never created: the row says it may be
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts())); err == nil {
		t.Fatal("the kill did not stop the transfer")
	}
	if got := r.placingRows(); len(got) != 1 || got[0] == nil {
		t.Fatalf("the intent was not journaled: %v", got)
	}
	const penguin = "the Penguin's own file"
	if err := os.WriteFile(r.abs(dst.Root, dst.Rel), []byte(penguin), 0o644); err != nil {
		t.Fatal(err)
	}
	r.m = r.build(rigOpts{})
	if rep, err := r.m.Recover(ctxbg()); err != nil || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if got, _ := r.read(dst.Root, dst.Rel); got != penguin {
		t.Errorf("recovery touched a file that is not the placeholder: %q", got)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 || !r.exists(src.Root, src.Rel) {
		t.Errorf("strays %v, rows %d, src %v", r.strays(), r.journalRows(), r.exists(src.Root, src.Rel))
	}
}

func TestDropPlaceholderShapes(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	fsys := root.rw
	touch := func(rel, body string) {
		t.Helper()
		r.put("books-library", rel, body)
	}
	cases := []struct {
		name      string
		setup     func()
		wantFreed bool
		wantThere bool
	}{
		{"an empty file, the path still exists", func() { touch("from", gas); touch("to", "") }, true, false},
		{"an empty directory, the path still exists", func() {
			touch("from", gas)
			if err := os.Mkdir(r.abs("books-library", "to"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"a non-empty file is not a placeholder", func() { touch("from", gas); touch("to", "the Riddler's file") }, false, true},
		{"a non-empty directory is not removed", func() { touch("from", gas); touch("to/clue.m4b", "?") }, false, true},
		{"the path is gone: the rename finished", func() { touch("to", "") }, false, true},
		{"the same file under both names", func() {
			touch("from", "")
			if err := os.Link(r.abs("books-library", "from"), r.abs("books-library", "to")); err != nil {
				t.Fatal(err)
			}
		}, false, true},
		{"nothing at the destination", func() { touch("from", gas) }, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, n := range []string{"from", "to"} {
				if err := os.RemoveAll(r.abs("books-library", n)); err != nil {
					t.Fatal(err)
				}
			}
			c.setup()
			freed, err := r.m.dropPlaceholder(fsys, "from", fsys, "to")
			if err != nil || freed != c.wantFreed {
				t.Errorf("dropPlaceholder = %v, %v; want freed=%v", freed, err, c.wantFreed)
			}
			if got := r.exists("books-library", "to"); got != c.wantThere {
				t.Errorf("the destination exists = %v, want %v", got, c.wantThere)
			}
		})
	}
}

// A directory Move on a filesystem without an atomic rename reserves the name
// with a directory and renames over it. Killed at every step, recovery leaves
// the tree whole at one end or the other and no empty directory behind.
func TestDirectoryMoveThroughAPlaceholderIsCrashSafe(t *testing.T) {
	src := Path{Root: "import-1", Rel: "Arkham"}
	dst := Path{Root: "books-library", Rel: "Gotham"}
	opts := withMkdir(move())
	build := func(f *faults) *rig {
		r, _ := transferRig(t, rigOpts{faults: f})
		f.inject = noAtomicRename
		r.put("import-1", "Arkham/joke.m4b", gas)
		r.put("import-1", "Arkham/Cells/two-face.m4b", "heads")
		return r
	}
	whole := func(r *rig, id RootID, rel string) bool {
		a, _ := r.read(id, rel+"/joke.m4b")
		b, _ := r.read(id, rel+"/Cells/two-face.m4b")
		return a == gas && b == "heads"
	}
	dry := &faults{}
	dr := build(dry)
	if _, err := dr.m.Transfer(ctxbg(), books, src, dst, opts); err != nil {
		t.Fatal(err)
	}
	if !whole(dr, dst.Root, dst.Rel) || dr.exists(src.Root, src.Rel) || len(dry.labelsOf(":mkdirx:Gotham")) != 1 {
		t.Fatalf("dry run: moved %v, source left %v, labels %v", whole(dr, dst.Root, dst.Rel), dr.exists(src.Root, src.Rel), dry.labels)
	}
	for k := 1; k <= dry.steps(); k++ {
		t.Run(fmt.Sprintf("kill-%02d", k), func(t *testing.T) {
			f := &faults{killAt: k}
			r := build(f)
			_, _ = r.m.Transfer(ctxbg(), books, src, dst, opts)
			label := fmt.Sprintf("killed at step %d (%s)", k, f.labels[len(f.labels)-1])
			if !whole(r, src.Root, src.Rel) && !whole(r, dst.Root, dst.Rel) {
				t.Errorf("%s: the tree is whole at neither end", label)
			}
			r.m = r.build(rigOpts{})
			if rep, err := r.m.Recover(ctxbg()); err != nil || rep.Failed != 0 || rep.Conflicts != 0 || rep.Blocked != 0 {
				t.Fatalf("%s: Recover = %v, %v", label, rep, err)
			}
			atSrc, atDst := whole(r, src.Root, src.Rel), whole(r, dst.Root, dst.Rel)
			if atSrc == atDst {
				t.Errorf("%s: after recovery the tree is at the source %v and at the destination %v", label, atSrc, atDst)
			}
			if atSrc && r.exists(dst.Root, dst.Rel) {
				t.Errorf("%s: a rolled back Move left an entry at the destination (a placeholder?)", label)
			}
			if r.journalRows() != 0 {
				t.Errorf("%s: %d rows left", label, r.journalRows())
			}
		})
	}
}

// Quarantine's holding path is a fresh ULID name, so a filesystem without an
// atomic rename moves into it after checking it is absent. A Release back to a
// name the caller chose reserves it, and the quarantine record says so: killed
// before the rename, recovery removes the placeholder and the item is still held.
func TestQuarantineAndReleaseWithoutAnAtomicRename(t *testing.T) {
	build := func() (*rig, *faults) {
		r, f := transferRig(t, rigOpts{})
		f.inject = noAtomicRename
		return r, f
	}
	r, f := build()
	orig := r.put("books-library", "Gotham/held.m4b", gas)
	id, err := r.m.Quarantine(ctxbg(), books, orig, "damaged")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.labelsOf(":renameover:")) != 1 || len(f.labelsOf(":create:")) != 0 {
		t.Errorf("a fresh name takes a checked rename over, no placeholder: %v", f.labels)
	}
	base := f.steps()
	held := heldRel(t, r, id)
	to := Path{Root: "books-library", Rel: "Gotham/back.m4b"}
	if _, err := r.m.Release(ctxbg(), books, id, &to); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.read(to.Root, to.Rel); got != gas || len(f.labelsOf(":create:Gotham/back.m4b")) != 1 {
		t.Errorf("Release put back %q; labels %v", got, f.labels)
	}
	cut := slices.Index(f.labels[base:], "books-library:renameover:"+held+"->Gotham/back.m4b")
	if cut < 0 {
		t.Fatalf("no rename over in %v", f.labels[base:])
	}

	// The same Release, killed at the rename over: the placeholder is there.
	k, kf := build()
	orig = k.put("books-library", "Gotham/held.m4b", gas)
	kid, err := k.m.Quarantine(ctxbg(), books, orig, "damaged")
	if err != nil {
		t.Fatal(err)
	}
	kf.killAt = kf.steps() + cut + 1
	if _, err := k.m.Release(ctxbg(), books, kid, &to); err == nil {
		t.Fatal("the kill did not stop the Release")
	}
	if !k.exists(to.Root, to.Rel) {
		t.Fatal("the placeholder was never made")
	}
	k.m = k.build(rigOpts{})
	if rep, err := k.m.Recover(ctxbg()); err != nil || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if k.exists(to.Root, to.Rel) {
		t.Error("recovery left the placeholder at the release target")
	}
	if !k.exists("books-library", heldRel(t, k, kid)) {
		t.Error("the held file is gone")
	}
	if _, err := k.m.Release(ctxbg(), books, kid, &to); err != nil {
		t.Fatalf("a second Release: %v", err)
	}
	if got, _ := k.read(to.Root, to.Rel); got != gas {
		t.Errorf("the second Release put back %q", got)
	}
}

// heldRel is the root-relative path of a held item's file.
func heldRel(t *testing.T, r *rig, id QuarantineID) string {
	t.Helper()
	row, err := r.m.loadQuarantine(ctxbg(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Held.Rel
}

// A platform without directory-relative syscalls refuses every mutation before
// it starts: nothing is journaled and nothing is created.
func TestNoHandlesRefusesBeforeAnythingHappens(t *testing.T) {
	r, _ := transferRig(t, rigOpts{seams: seams{noHandles: true}})
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	for _, m := range []TransferMode{Copy, Move, Hardlink, Symlink, Auto, Reflink} {
		o := withMkdir(NewTransferOptions(m, PermLibrary, VerifySize))
		if _, err := r.m.Transfer(ctxbg(), books, src, dst, o); !errors.Is(err, ErrNotSupported) {
			t.Errorf("%v: Transfer = %v, want ErrNotSupported", m, err)
		}
	}
	if _, err := r.m.Quarantine(ctxbg(), books, r.put("books-library", "Gotham/a.m4b", "a"), "x"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Quarantine = %v, want ErrNotSupported", err)
	}
	if _, err := r.m.Release(ctxbg(), books, QuarantineID(newULID()), nil); !errors.Is(err, ErrNotSupported) && !errors.Is(err, ErrUnknownQuarantine) {
		t.Errorf("Release = %v", err)
	}
	if r.exists(dst.Root, filepath.Dir(dst.Rel)) || r.journalRows() != 0 || !r.exists(src.Root, src.Rel) {
		t.Errorf("something happened: dir %v, rows %d", r.exists(dst.Root, filepath.Dir(dst.Rel)), r.journalRows())
	}
}

// A Hardlink asked for on a filesystem without hard links is ErrNotSupported, not
// a raw EPERM, and leaves nothing behind.
func TestHardlinkWithoutHardLinksIsNotSupported(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = noHardLinks
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(NewTransferOptions(Hardlink, PermLibrary, VerifySize)))
	if !errors.Is(err, ErrNotSupported) || !errors.Is(err, libfs.ErrNoHardLinks) || !errors.Is(err, syscall.EPERM) {
		t.Errorf("Transfer = %v, want ErrNotSupported wrapping the cause", err)
	}
	if r.exists(dst.Root, dst.Rel) || r.journalRows() != 0 || len(r.strays()) != 0 {
		t.Errorf("dst %v, rows %d, strays %v", r.exists(dst.Root, dst.Rel), r.journalRows(), r.strays())
	}
}

// noLinkDirEnv names a directory on a filesystem with no hard links (and often
// no atomic rename): exFAT, FAT, an SMB share. The test below runs the real
// engine there; it is skipped when the variable is unset, since CI has no such
// volume. To try it on macOS: make an exFAT image (diskutil image create blank
// --fs ExFAT ...), mount it, and point the variable at a directory on it.
const noLinkDirEnv = "MEDIACORE_TEST_NOLINK_DIR"

func TestOnARealFilesystemWithoutHardLinks(t *testing.T) {
	base := os.Getenv(noLinkDirEnv)
	if base == "" {
		t.Skipf("%s is not set", noLinkDirEnv)
	}
	work, err := os.MkdirTemp(base, "f7-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	mk := func(name string) string {
		p := filepath.Join(work, name)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	lib, imp := spec("books-library", books, RoleLibrary), spec("import-1", books, RoleImport)
	lib.Path, imp.Path = mk("lib"), mk("imp")
	r := newRig(t, newStore(t), rigOpts{}, lib, imp)
	probe := filepath.Join(work, "probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lerr := os.Link(probe, probe+".link"); lerr == nil {
		t.Skipf("%s has hard links; this test is for a filesystem without them", work)
	}

	// Copy, then Auto (clone and link both unavailable), publish without a link.
	for _, mode := range []TransferMode{Copy, Auto} {
		src := r.put("import-1", fmt.Sprintf("Arkham/%v.m4b", mode), gas)
		dst := Path{Root: "books-library", Rel: fmt.Sprintf("Gordon/%v.m4b", mode)}
		if _, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(NewTransferOptions(mode, PermLibrary, VerifySHA256))); err != nil {
			t.Fatalf("%v: %v", mode, err)
		}
		if got, ok := r.read(dst.Root, dst.Rel); !ok || got != gas {
			t.Errorf("%v: destination holds %q (readable %v)", mode, got, ok)
		}
	}
	// A Move on one volume and a Move of a directory.
	msrc := r.put("import-1", "Arkham/move.m4b", gas)
	if _, err := r.m.Transfer(ctxbg(), books, msrc, Path{Root: "books-library", Rel: "Gordon/move.m4b"}, withMkdir(move())); err != nil {
		t.Fatalf("Move: %v", err)
	}
	r.put("import-1", "Wing/Cells/two-face.m4b", "heads")
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Wing"}, Path{Root: "books-library", Rel: "Wing"}, withMkdir(move())); err != nil {
		t.Fatalf("directory Move: %v", err)
	}
	if got, _ := r.read("books-library", "Wing/Cells/two-face.m4b"); got != "heads" {
		t.Errorf("the moved directory holds %q", got)
	}
	// Replace.
	r.put("books-library", "Gordon/old.m4b", "the old edition")
	ro := withMkdir(copyOpts())
	ro.Replace = &ReplaceSpec{ExpectSHA256: sum("the old edition")}
	if _, err := r.m.Transfer(ctxbg(), books, r.put("import-1", "Arkham/new.m4b", gas), Path{Root: "books-library", Rel: "Gordon/old.m4b"}, ro); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got, _ := r.read("books-library", "Gordon/old.m4b"); got != gas {
		t.Errorf("Replace left %q", got)
	}
	// Quarantine and Release.
	held := r.put("books-library", "Gotham/held.m4b", gas)
	id, err := r.m.Quarantine(ctxbg(), books, held, "damaged")
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if _, err := r.m.Release(ctxbg(), books, id, nil); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got, _ := r.read("books-library", "Gotham/held.m4b"); got != gas {
		t.Errorf("Release put back %q", got)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 {
		t.Errorf("strays %v, rows %d", r.strays(), r.journalRows())
	}
	// An existing destination is still refused.
	_, err = r.m.Transfer(ctxbg(), books, r.put("import-1", "Arkham/again.m4b", "two"), Path{Root: "books-library", Rel: "Gordon/Copy.m4b"}, withMkdir(copyOpts()))
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("a transfer onto an existing name = %v, want ErrExist", err)
	}
	if got, _ := r.read("books-library", "Gordon/Copy.m4b"); got != gas {
		t.Errorf("an existing file was changed: %q", got)
	}
}
