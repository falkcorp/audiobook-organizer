// file: core/library/manager_test.go
// version: 1.0.2
// guid: 424ad437-d2db-4df6-a85b-72352e053fb2
// last-edited: 2026-11-11

package library

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

func TestRootFSRefusesDotDotAndSymlinkEscape(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("the Riddler's answers"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.put("books-library", "Gotham Nights/01 - The Bat Signal.m4b", "audio")
	if err := os.Symlink(outside, r.abs("books-library", "escape")); err != nil {
		t.Fatal(err)
	}
	root, _ := r.m.Root("books-library")
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../x", "Gotham Nights/../../x", "/etc/passwd", "a//b", "./a", "a/"} {
		if _, err := fsys.Open(name); !errors.Is(err, ErrBadRel) {
			t.Errorf("Open(%q) = %v, want ErrBadRel", name, err)
		}
		if err := fsys.MkdirAll(name, 0o755); !errors.Is(err, ErrBadRel) {
			t.Errorf("MkdirAll(%q) = %v, want ErrBadRel", name, err)
		}
	}
	for _, name := range []string{"escape/secret.txt", "escape"} {
		f, err := fsys.Open(name)
		if name == "escape" {
			// The symlink itself is a name inside the root; opening it follows it out.
			if err == nil {
				f.Close()
				t.Errorf("Open(%q) followed a symlink out of the root", name)
			}
			continue
		}
		if err == nil {
			f.Close()
			t.Fatalf("Open(%q) read a file outside the root", name)
		}
		if !errors.Is(err, ErrEscapesRoot) {
			t.Errorf("Open(%q) = %v, want ErrEscapesRoot", name, err)
		}
	}
	if _, err := fsys.CreateExclusive("escape/planted.txt", 0o644); !errors.Is(err, ErrEscapesRoot) {
		t.Errorf("CreateExclusive through an escaping symlink = %v, want ErrEscapesRoot", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatal("a file was created outside the root")
	}
	// The root itself is nameable only for reads.
	if err := fsys.Remove("."); err == nil {
		t.Error("Remove(\".\") succeeded")
	}
}

func TestRootFSWalkRefusesDirectorySwappedForSymlink(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.put("books-library", "sub/inside.m4b", "audio")
	root, _ := r.m.Root("books-library")
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	var walkErrs []error
	for e, err := range fsys.Walk(ctxbg(), ".", WalkOptions{}) {
		if err != nil {
			walkErrs = append(walkErrs, err)
			continue
		}
		seen = append(seen, e.Path.Rel)
		if e.Path.Rel == "sub" {
			// A symlink swapped in after the listing and before the descent.
			if err := os.Rename(r.abs("books-library", "sub"), r.abs("books-library", "sub.moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, r.abs("books-library", "sub")); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, s := range seen {
		if s == "sub/secret.txt" {
			t.Fatalf("the walk entered a swapped-in symlink: saw %v", seen)
		}
	}
	if len(walkErrs) == 0 {
		t.Errorf("the swap was not reported; saw %v", seen)
	}
}

// A directory swapped, between the listing and the descent, for something that
// stays INSIDE the root is not entered either. os.Root already refuses a link
// that leaves the root (the test above), so only the comparison of the opened
// directory with the listed one stops these: a link to another directory of the
// root, and a different directory under the same name. Either would otherwise be
// reported under the old name, and a link could lead into a reserved area.
func TestRootFSWalkRefusesADirectorySwappedForAnotherInsideTheRoot(t *testing.T) {
	for name, swap := range map[string]func(t *testing.T, r *rig){
		"a link to another directory of the root": func(t *testing.T, r *rig) {
			if err := os.Rename(r.abs("books-library", "sub"), r.abs("books-library", "sub.moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other", r.abs("books-library", "sub")); err != nil {
				t.Fatal(err)
			}
		},
		"a different directory of the same name": func(t *testing.T, r *rig) {
			if err := os.Rename(r.abs("books-library", "sub"), r.abs("books-library", "sub.moved")); err != nil {
				t.Fatal(err)
			}
			r.put("books-library", "sub/decoy.m4b", "decoy")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary))
			r.put("books-library", "sub/inside.m4b", "audio")
			r.put("books-library", "other/elsewhere.m4b", "audio")
			root, _ := r.m.Root("books-library")
			fsys, err := root.FS(books)
			if err != nil {
				t.Fatal(err)
			}
			var seen []string
			var walkErrs []error
			for e, err := range fsys.Walk(ctxbg(), ".", WalkOptions{}) {
				if err != nil {
					walkErrs = append(walkErrs, err)
					continue
				}
				seen = append(seen, e.Path.Rel)
				if e.Path.Rel == "sub" {
					swap(t, r)
				}
			}
			for _, s := range seen {
				if strings.HasPrefix(s, "sub/") {
					t.Errorf("the walk entered the swapped directory and reported %q (saw %v)", s, seen)
				}
			}
			found := false
			for _, err := range walkErrs {
				found = found || errors.Is(err, ErrEscapesRoot)
			}
			if !found {
				t.Errorf("the swap was not reported as a change during the walk: %v", walkErrs)
			}
			if !slices.Contains(seen, "other/elsewhere.m4b") {
				t.Errorf("the walk stopped short of the untouched sibling: saw %v", seen)
			}
		})
	}
}

func TestRootOverlapAcrossOwnersRefused(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	if err := os.MkdirAll(filepath.Join(lib, "Movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(lib, alias); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(id RootID, owner storage.Owner, p string) RootSpec {
		s := spec(id, owner, RoleLibrary)
		s.Path, s.Sentinel = p, SentinelBootstrap
		return s
	}
	r := newRig(t, newStore(t), rigOpts{},
		mk("books-library", books, lib),
		mk("tv-nested", tv, filepath.Join(lib, "Movies")), // nested under another owner's root
		mk("tv-alias", tv, alias),                         // the same directory through a symlink
		mk("tv-elsewhere", tv, other),                     // independent: fine
	)
	state := func(id RootID) error {
		root, err := r.m.Root(id)
		if err != nil {
			t.Fatalf("root %s: %v", id, err)
		}
		return root.Disabled()
	}
	if err := state("books-library"); err != nil {
		t.Errorf("the first root is disabled: %v", err)
	}
	if err := state("tv-elsewhere"); err != nil {
		t.Errorf("an independent root is disabled: %v", err)
	}
	for _, id := range []RootID{"tv-nested", "tv-alias"} {
		err := state(id)
		if !errors.Is(err, ErrRootOverlap) || !errors.Is(err, ErrRootDisabled) {
			t.Errorf("%s: %v, want a disabled root with ErrRootOverlap", id, err)
		}
	}
	// A disabled root takes no work.
	if _, err := r.m.Transfer(ctxbg(), tv, Path{Root: "books-library", Rel: "x"}, Path{Root: "tv-nested", Rel: "y"}, copyOpts()); !errors.Is(err, ErrRootDisabled) {
		t.Errorf("Transfer into a disabled root = %v, want ErrRootDisabled", err)
	}
}

// The device+inode comparison catches what path comparison cannot: a bind
// mount (one directory under two paths) and a case-folding filesystem (two
// spellings of one path). Neither can be made in a test directory, so the
// comparison is checked on the identities themselves.
func TestRootOverlapByDeviceAndInode(t *testing.T) {
	dir := fileID{1, 100}
	parent := fileID{1, 50}
	mk := func(abs string, id fileID, chain ...fileID) *candidate {
		return &candidate{abs: abs, id: id, chain: append([]fileID{id}, chain...)}
	}
	cases := []struct {
		name string
		a, b *candidate
		want nesting
	}{
		{"bind mount: one directory, two paths", mk("/srv/a", dir, parent), mk("/mnt/bound", dir, fileID{2, 7}), nestSame},
		{"case folded spelling of a nested root", mk("/Data/Books", dir, parent), mk("/data/books/Sub", fileID{1, 101}, dir, parent), nestBInA},
		{"case folded spelling the other way", mk("/data/books/Sub", fileID{1, 101}, dir, parent), mk("/Data/Books", dir, parent), nestAInB},
		{"unrelated", mk("/srv/a", dir, parent), mk("/srv/b", fileID{1, 200}, parent), nestNone},
		{"prefix of a name is not nesting", mk("/srv/lib", dir, parent), mk("/srv/lib-old", fileID{1, 201}, parent), nestNone},
		{"path nesting without inodes", mk("/srv/lib", fileID{}), mk("/srv/lib/Movies", fileID{}), nestBInA},
	}
	for _, c := range cases {
		if got := relate(c.a, c.b); got != c.want {
			t.Errorf("%s: relate = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSameOwnerNestedRootIsExcludedFromTheOuterWalk(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "library")
	inner := filepath.Join(outer, "Imports")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(id RootID, role Role, p string) RootSpec {
		s := spec(id, books, role)
		s.Path = p
		return s
	}
	r := newRig(t, newStore(t), rigOpts{}, mk("books-library", RoleLibrary, outer), mk("import-1", RoleImport, inner))
	r.put("books-library", "Gotham Nights/01.m4b", "audio")
	r.put("import-1", "Arkham/incoming.m4b", "audio")
	for _, id := range []RootID{"books-library", "import-1"} {
		root, _ := r.m.Root(id)
		if err := root.Disabled(); err != nil {
			t.Fatalf("%s is disabled: %v", id, err)
		}
	}
	root, _ := r.m.Root("books-library")
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	for e, err := range fsys.Walk(ctxbg(), ".", WalkOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Path.Rel == "Imports" || len(e.Path.Rel) > 8 && e.Path.Rel[:8] == "Imports/" {
			t.Errorf("the outer root's walk entered the inner root: %s", e.Path.Rel)
		}
	}
}

func TestSentinelBlocksDestructiveOps(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{},
		spec("books-library", books, RoleLibrary), spec("import-1", books, RoleImport))
	src := r.put("import-1", "Arkham/incoming.m4b", "audio")
	r.put("books-library", "Gotham Nights/01.m4b", "audio")
	lib, _ := r.m.Root("books-library")
	if err := lib.Mounted(ctxbg()); err != nil {
		t.Fatalf("a mounted root: %v", err)
	}
	// The share goes away: the sentinel with it.
	if err := os.Remove(r.abs("books-library", DefaultAppDir+"/"+sentinelName)); err != nil {
		t.Fatal(err)
	}
	if err := lib.Mounted(ctxbg()); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("Mounted without a sentinel = %v, want ErrNotMounted", err)
	}
	// Reads are open: an empty-looking tree is harmless to read.
	fsys, _ := lib.FS(books)
	f, err := fsys.Open("Gotham Nights/01.m4b")
	if err != nil {
		t.Fatalf("a read of an unmounted root failed: %v", err)
	}
	f.Close()
	// Acting on it is closed: remove, rename, create, and a transfer into it.
	if err := fsys.Remove("Gotham Nights/01.m4b"); !errors.Is(err, ErrNotMounted) {
		t.Errorf("Remove = %v, want ErrNotMounted", err)
	}
	if err := fsys.RenameNoReplace("Gotham Nights/01.m4b", "Gotham Nights/02.m4b"); !errors.Is(err, ErrNotMounted) {
		t.Errorf("RenameNoReplace = %v, want ErrNotMounted", err)
	}
	if _, err := fsys.CreateExclusive("Gotham Nights/03.m4b", 0o644); !errors.Is(err, ErrNotMounted) {
		t.Errorf("CreateExclusive = %v, want ErrNotMounted", err)
	}
	dst := Path{Root: "books-library", Rel: "Gotham Nights/04.m4b"}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); !errors.Is(err, ErrNotMounted) {
		t.Errorf("Transfer into an unmounted root = %v, want ErrNotMounted", err)
	}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "Gotham Nights/01.m4b"}, "test"); !errors.Is(err, ErrNotMounted) {
		t.Errorf("Quarantine in an unmounted root = %v, want ErrNotMounted", err)
	}
	// Moving out of it is destructive too.
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "books-library", Rel: "Gotham Nights/01.m4b"},
		Path{Root: "import-1", Rel: "back.m4b"}, move()); !errors.Is(err, ErrNotMounted) {
		t.Errorf("Move out of an unmounted root = %v, want ErrNotMounted", err)
	}
	if got, ok := r.read("books-library", "Gotham Nights/01.m4b"); !ok || got != "audio" {
		t.Errorf("the file was touched: %q %v", got, ok)
	}
	// A sentinel naming another install, or another root, is no sentinel.
	for name, sf := range map[string]sentinelFile{
		"another install": {Install: "install-other", Root: "books-library"},
		"another root":    {Install: testInstall, Root: "import-1"},
	} {
		b, _ := json.Marshal(sf)
		if err := os.WriteFile(r.abs("books-library", DefaultAppDir+"/"+sentinelName), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := lib.Mounted(ctxbg()); !errors.Is(err, ErrNotMounted) {
			t.Errorf("sentinel of %s: Mounted = %v, want ErrNotMounted", name, err)
		}
	}
	// WriteSentinel never replaces one that names something else.
	if err := r.m.WriteSentinel(ctxbg(), "books-library"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("WriteSentinel over a different sentinel = %v, want fs.ErrExist", err)
	}
}

func TestSentinelBootstrapMigratedRoot(t *testing.T) {
	boot := func(id RootID) RootSpec {
		s := spec(id, books, RoleLibrary)
		s.Sentinel = SentinelBootstrap
		return s
	}
	r := newRig(t, newStore(t), rigOpts{}, boot("books-library"), boot("books-empty"))
	r.put("books-library", "Gotham Nights/01.m4b", "audio")
	lib, _ := r.m.Root("books-library")
	// The heuristic passes (the root holds non-dot content) and the sentinel is written.
	if err := lib.Mounted(ctxbg()); err != nil {
		t.Fatalf("a migrated root with content: %v", err)
	}
	b, err := os.ReadFile(r.abs("books-library", DefaultAppDir+"/"+sentinelName))
	if err != nil {
		t.Fatalf("the sentinel was not created: %v", err)
	}
	var sf sentinelFile
	if err := json.Unmarshal(b, &sf); err != nil || sf.Install != testInstall || sf.Root != "books-library" {
		t.Fatalf("sentinel = %s (%v)", b, err)
	}
	// An empty mount: no content, no sentinel: refused, and nothing is created.
	empty, _ := r.m.Root("books-empty")
	if err := empty.Mounted(ctxbg()); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("an empty bootstrap root = %v, want ErrNotMounted", err)
	}
	if r.exists("books-empty", DefaultAppDir) {
		t.Error("a sentinel (or its directory) was created in an empty root")
	}
	// Dot content does not count (HEAD's rule).
	if err := os.WriteFile(r.abs("books-empty", ".DS_Store"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := empty.Mounted(ctxbg()); !errors.Is(err, ErrNotMounted) {
		t.Errorf("a root with only dot entries = %v, want ErrNotMounted", err)
	}
	// Once the sentinel exists the heuristic no longer decides: removing the
	// content (an empty share mounted over it) leaves a root that names this
	// install, which stays mounted; a sentinel naming another install does not.
	if err := os.WriteFile(r.abs("books-library", DefaultAppDir+"/"+sentinelName), []byte(`{"install":"install-other","root":"books-library"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lib.Mounted(ctxbg()); !errors.Is(err, ErrNotMounted) {
		t.Errorf("a present sentinel naming another install = %v, want ErrNotMounted (the heuristic must not override it)", err)
	}
}

// A mount that appears over a root's directory after the root was opened is not
// seen through the old handle: destructive operations refuse until a reload.
func TestMountAppearingAfterOpenFailsClosed(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary))
	lib, _ := r.m.Root("books-library")
	if err := lib.Mounted(ctxbg()); err != nil {
		t.Fatal(err)
	}
	dir := r.dirs["books-library"]
	moved := dir + ".detached"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil { // a new directory under the same path, as a mount would be
		t.Fatal(err)
	}
	if err := lib.Mounted(ctxbg()); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("Mounted after the directory was replaced = %v, want ErrNotMounted", err)
	}
}

func TestDisabledModuleRootsInert(t *testing.T) {
	enabled := func(o storage.Owner) bool { return o != books }
	r := newRig(t, newStore(t), rigOpts{enabled: enabled},
		spec("books-library", books, RoleLibrary), spec("tv-library", tv, RoleLibrary))
	r.put("books-library", "Gotham Nights/01.m4b", "audio")
	r.put("tv-library", "Batman/S01E01.mkv", "video")
	src := Path{Root: "books-library", Rel: "Gotham Nights/01.m4b"}
	// Reads stay open.
	if d, err := r.m.Hash(ctxbg(), Path{Root: "books-library", Rel: "Gotham Nights/01.m4b"}); err != nil || d.Size != 5 {
		t.Errorf("Hash on a disabled owner's root = %+v, %v", d, err)
	}
	root, _ := r.m.Root("books-library")
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Open("Gotham Nights/01.m4b"); err != nil {
		t.Errorf("a read of a disabled owner's root: %v", err)
	}
	if _, err := fsys.CreateExclusive("x", 0o644); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("a write through a disabled owner's FS = %v, want ErrReadOnlyRoot", err)
	}
	// No transfers, quarantine or watches.
	dst := Path{Root: "books-library", Rel: "Gotham Nights/02.m4b"}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("Transfer into a disabled owner's root = %v, want ErrOwnerDisabled", err)
	}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "Gotham Nights/01.m4b"}, "x"); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("Quarantine = %v, want ErrOwnerDisabled", err)
	}
	if _, err := r.m.Watch(ctxbg(), "books-library", WatchOptions{}); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("Watch = %v, want ErrOwnerDisabled", err)
	}
	// Moving a file out of a disabled owner's root is a write there.
	if _, err := r.m.Transfer(ctxbg(), tv, Path{Root: "books-library", Rel: "Gotham Nights/01.m4b"},
		Path{Root: "tv-library", Rel: "stolen.m4b"}, move()); err == nil {
		t.Error("another owner moved a file out of a disabled owner's root")
	}
	if !r.exists("books-library", "Gotham Nights/01.m4b") {
		t.Error("the disabled owner's file was touched")
	}
}

func TestScopeCannotActAsAnotherOwner(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary), spec("tv-library", tv, RoleLibrary))
	bookFile := r.put("books-library", "Gotham Nights/01.m4b", "audio")
	tvFile := r.put("tv-library", "Batman/S01E01.mkv", "video")
	tvScope, err := NewScope(r.m, tv)
	if err != nil {
		t.Fatal(err)
	}
	if tvScope.Owner() != tv {
		t.Fatalf("Owner = %s", tvScope.Owner())
	}
	// It cannot write the other owner's root, in any mode.
	for _, o := range []TransferOptions{copyOpts(), move(), NewTransferOptions(Auto, PermLibrary, VerifySize)} {
		if _, err := tvScope.Transfer(ctxbg(), tvFile, Path{Root: "books-library", Rel: "planted.mkv"}, o); !errors.Is(err, ErrReadOnlyRoot) {
			t.Errorf("%s into another owner's root = %v, want ErrReadOnlyRoot", o.Mode, err)
		}
	}
	if r.exists("books-library", "planted.mkv") {
		t.Fatal("a file was written into the other owner's root")
	}
	// It cannot read, hash, map, watch or move out of it, nor quarantine its files.
	if _, err := tvScope.Transfer(ctxbg(), bookFile, Path{Root: "tv-library", Rel: "x.m4b"}, copyOpts()); !errors.Is(err, ErrNotReadable) {
		t.Errorf("a transfer out of an unreadable root = %v, want ErrNotReadable", err)
	}
	if _, err := tvScope.Hash(ctxbg(), bookFile); !errors.Is(err, ErrNotReadable) {
		t.Errorf("Hash = %v, want ErrNotReadable", err)
	}
	if _, err := tvScope.FS("books-library"); !errors.Is(err, ErrNotReadable) {
		t.Errorf("FS = %v, want ErrNotReadable", err)
	}
	if _, err := tvScope.Quarantine(ctxbg(), bookFile, "mine now"); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("Quarantine of another owner's file = %v, want ErrReadOnlyRoot", err)
	}
	if _, err := tvScope.Watch(ctxbg(), "books-library", WatchOptions{}); !errors.Is(err, ErrNotReadable) {
		t.Errorf("Watch = %v, want ErrNotReadable", err)
	}
	if _, err := tvScope.Abs(bookFile); !errors.Is(err, ErrNotReadable) {
		t.Errorf("Abs = %v, want ErrNotReadable", err)
	}
	if _, err := tvScope.Resolve(r.abs("books-library", "Gotham Nights/01.m4b")); !errors.Is(err, ErrNotReadable) {
		t.Errorf("Resolve = %v, want ErrNotReadable", err)
	}
	roots := tvScope.Roots(RootFilter{})
	if len(roots) != 1 || roots[0].ID() != "tv-library" {
		t.Errorf("Roots lists %d roots, want only tv-library", len(roots))
	}
	// Another owner's quarantine record cannot be released through this scope.
	id, err := r.m.Quarantine(ctxbg(), books, bookFile, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tvScope.Release(ctxbg(), id, nil); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("Release of another owner's record = %v, want ErrReadOnlyRoot", err)
	}
	if !r.exists("books-library", DefaultQuarantine+"/"+string(id)+"/Gotham Nights/01.m4b") {
		t.Error("the held file moved")
	}
	// Its own root works.
	if _, err := tvScope.Hash(ctxbg(), tvFile); err != nil {
		t.Errorf("Hash of its own file: %v", err)
	}
	if _, err := NewScope(r.m, ""); !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("NewScope with no owner = %v, want ErrInvalidOptions", err)
	}
}

func TestDownloadRootSharedMoveOutOnly(t *testing.T) {
	dl := spec("dl-qbit-1", CoreOwner, RoleDownload)
	caps := func(o storage.Owner, c string) bool { return c == CapabilityDownload && (o == books || o == tv) }
	r := newRig(t, newStore(t), rigOpts{caps: caps}, dl, spec("books-library", books, RoleLibrary), spec("tv-library", tv, RoleLibrary))
	for _, o := range []struct {
		owner storage.Owner
		file  string
		dst   RootID
	}{{books, "Batman Begins.m4b", "books-library"}, {tv, "Batman S01E02.mkv", "tv-library"}} {
		src := r.put("dl-qbit-1", "complete/"+o.file, "bytes of "+o.file)
		sc, err := NewScope(r.m, o.owner)
		if err != nil {
			t.Fatal(err)
		}
		// Both modules read the shared root and move OUT of it.
		if _, err := sc.Hash(ctxbg(), src); err != nil {
			t.Errorf("%s: read of the download root: %v", o.owner, err)
		}
		res, err := sc.Transfer(ctxbg(), src, Path{Root: o.dst, Rel: o.file}, NewTransferOptions(Move, PermLibrary, VerifySize))
		if err != nil {
			t.Fatalf("%s: move out of the download root: %v", o.owner, err)
		}
		if res.Did != Move {
			t.Errorf("%s: Did = %s", o.owner, res.Did)
		}
		if r.exists("dl-qbit-1", "complete/"+o.file) {
			t.Errorf("%s: the source stayed", o.owner)
		}
		// Neither writes in, by transfer or through the filesystem.
		if _, err := sc.Transfer(ctxbg(), Path{Root: o.dst, Rel: o.file}, Path{Root: "dl-qbit-1", Rel: "back/" + o.file}, copyOpts()); !errors.Is(err, ErrReadOnlyRoot) {
			t.Errorf("%s: a transfer into the download root = %v, want ErrReadOnlyRoot", o.owner, err)
		}
		fsys, err := sc.FS("dl-qbit-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.CreateExclusive("planted", 0o644); !errors.Is(err, ErrReadOnlyRoot) {
			t.Errorf("%s: create in the download root = %v, want ErrReadOnlyRoot", o.owner, err)
		}
		if err := fsys.Remove("complete"); !errors.Is(err, ErrReadOnlyRoot) {
			t.Errorf("%s: remove in the download root = %v, want ErrReadOnlyRoot", o.owner, err)
		}
	}
	// A module without the capability reads nothing there.
	other, err := NewScope(r.m, "podcasts")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.FS("dl-qbit-1"); !errors.Is(err, ErrNotReadable) {
		t.Errorf("a module without the capability: FS = %v, want ErrNotReadable", err)
	}
	// A download root is core-owned: a module-owned one is not a valid entry.
	bad := spec("dl-bad", books, RoleDownload)
	bad.Path = t.TempDir()
	if err := bad.normalized().validate(); !errors.Is(err, ErrBadRootSpec) {
		t.Errorf("a module-owned download root = %v, want ErrBadRootSpec", err)
	}
}

func TestResolveAndAbs(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	old := filepath.Join(base, "lib-old")
	nested := filepath.Join(lib, "Series")
	for _, d := range []string{lib, old, nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(lib, "out")); err != nil {
		t.Fatal(err)
	}
	mk := func(id RootID, p string, role Role) RootSpec {
		s := spec(id, books, role)
		s.Path, s.Sentinel = p, SentinelBootstrap
		return s
	}
	r := newRig(t, newStore(t), rigOpts{}, mk("books-library", lib, RoleLibrary), mk("books-series", nested, RoleImport), mk("books-old", old, RoleImport))
	canon, err := filepath.EvalSymlinks(lib)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		abs  string
		want Path
		err  error
	}{
		{filepath.Join(lib, "Gotham", "01.m4b"), Path{"books-library", "Gotham/01.m4b"}, nil},
		{lib, Path{Root: "books-library"}, nil},
		{filepath.Join(nested, "A", "b.m4b"), Path{"books-series", "A/b.m4b"}, nil}, // the most specific root wins
		{filepath.Join(old, "x"), Path{"books-old", "x"}, nil},                      // separator boundary: lib vs lib-old
		{filepath.Join(base, "lib2", "x"), Path{}, ErrNotUnderRoot},
		{"relative/path", Path{}, ErrNotUnderRoot},
		{filepath.Join(lib, "out", "secret"), Path{}, ErrNotUnderRoot}, // resolves outside every root
	}
	for _, c := range cases {
		got, err := r.m.Resolve(c.abs)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("Resolve(%s) = %v, %v; want %v, %v", c.abs, got, err, c.want, c.err)
		}
	}
	abs, err := r.m.Abs(Path{Root: "books-library", Rel: "Gotham/01.m4b"})
	if err != nil || abs != filepath.Join(canon, "Gotham", "01.m4b") {
		t.Errorf("Abs = %q, %v", abs, err)
	}
	if _, err := r.m.Abs(Path{Root: "books-library", Rel: "out/secret"}); !errors.Is(err, ErrEscapesRoot) {
		t.Errorf("Abs through an escaping symlink = %v, want ErrEscapesRoot", err)
	}
	if _, err := r.m.Abs(Path{Root: "books-library", Rel: "../x"}); !errors.Is(err, ErrBadRel) {
		t.Errorf("Abs with .. = %v, want ErrBadRel", err)
	}
	if _, err := r.m.Abs(Path{Root: "nope", Rel: "x"}); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("Abs of an unknown root = %v, want ErrUnknownRoot", err)
	}
}

func TestReloadKeepsUnchangedRootsAndRetiresTheRest(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary), spec("tv-library", tv, RoleLibrary))
	a1, _ := r.m.Root("books-library")
	b1, _ := r.m.Root("tv-library")
	set := defaultSettings()
	changed := r.specs[1]
	changed.Name = "Gotham Late Night"
	set.roots = []RootSpec{r.specs[0], changed}
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	a2, _ := r.m.Root("books-library")
	b2, _ := r.m.Root("tv-library")
	if a1 != a2 {
		t.Error("an unchanged root was replaced")
	}
	if b1 == b2 || !b1.isRetired() {
		t.Error("a changed root was kept or not retired")
	}
	if b2.Spec().Name != "Gotham Late Night" {
		t.Errorf("name = %q", b2.Spec().Name)
	}
	set.roots = set.roots[:1]
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Root("tv-library"); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("a removed root: %v", err)
	}
	if !b2.isRetired() {
		t.Error("a removed root was not retired")
	}
	if _, err := b2.FS(tv); !errors.Is(err, ErrRootDisabled) {
		t.Errorf("FS of a retired root = %v, want ErrRootDisabled", err)
	}
}

func TestRootSpecJSONMinFree(t *testing.T) {
	var s RootSpec
	if err := json.Unmarshal([]byte(`{"id":"books-library","path":"/x","role":"library","owner":"books"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.MinFree != MinFreeDefault {
		t.Errorf("omitted min_free = %d, want the default marker", s.MinFree)
	}
	if err := json.Unmarshal([]byte(`{"id":"books-library","path":"/x","role":"library","owner":"books","min_free":0}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.MinFree != 0 {
		t.Errorf("explicit min_free 0 = %d", s.MinFree)
	}
	b, err := json.Marshal(RootSpec{ID: "books-library", Path: "/x", Role: RoleLibrary, Owner: books, MinFree: 7, Watch: WatchPoll, Sentinel: SentinelBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	var back RootSpec
	if err := json.Unmarshal(b, &back); err != nil || back.MinFree != 7 || back.Watch != WatchPoll || back.Sentinel != SentinelBootstrap || back.Role != RoleLibrary {
		t.Errorf("round trip = %+v, %v (%s)", back, err, b)
	}
	if err := json.Unmarshal([]byte(`{"id":"books-library","path":"/x","role":"sideways","owner":"books"}`), &s); err == nil {
		t.Error("an unknown role decoded")
	}
}

// CreateExclusive is O_EXCL: an existing name is fs.ErrExist and the file that
// holds it keeps its bytes. The transfer's no-clobber guarantee for its temp
// file rests on this.
func TestCreateExclusiveRefusesAnExistingName(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	r.put("books-library", "Gotham/Court of Owls.txt", "the first writer's bytes")
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fsys.CreateExclusive("Gotham/Court of Owls.txt", 0o644)
	if err == nil {
		f.Close()
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateExclusive of an existing name = %v, want fs.ErrExist", err)
	}
	if got, _ := r.read("books-library", "Gotham/Court of Owls.txt"); got != "the first writer's bytes" {
		t.Errorf("the existing file = %q", got)
	}
	// A free name is created, once.
	g, err := fsys.CreateExclusive("Gotham/Talons.txt", 0o644)
	if err != nil {
		t.Fatalf("CreateExclusive of a free name: %v", err)
	}
	g.Close()
	if h, err := fsys.CreateExclusive("Gotham/Talons.txt", 0o644); !errors.Is(err, fs.ErrExist) {
		if err == nil {
			h.Close()
		}
		t.Errorf("the second CreateExclusive of the same name = %v, want fs.ErrExist", err)
	}
}
