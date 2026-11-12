// file: core/library/guards_test.go
// version: 1.0.0
// guid: 00f78c64-25d1-4cc1-b1f0-153883e12400
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// subtreeFunc is a SubtreeOracle that answers one way for a file and another
// for a directory.
type subtreeFunc struct {
	file, dir func() (protected, loaded bool, err error)
}

func (s subtreeFunc) Protected(context.Context, Path) (bool, bool, error) { return s.file() }
func (s subtreeFunc) ProtectedUnder(context.Context, Path) (bool, bool, error) {
	return s.dir()
}

func answer(protected, loaded bool, err error) func() (bool, bool, error) {
	return func() (bool, bool, error) { return protected, loaded, err }
}

func setProtected(t *testing.T, r *rig, paths ...string) {
	t.Helper()
	set := defaultSettings()
	set.roots = r.specs
	set.protected = paths
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
}

// Moving or quarantining a directory carries everything under it, so a
// protected path INSIDE the source protects the source, not only a source
// inside a protected path.
func TestMoveRefusesADirectoryHoldingAProtectedPath(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	r.put("import-1", "Arkham/Seeding/Gotham.m4b", "seeding bytes")
	r.put("import-1", "Arkham/Done/Gotham.m4b", "finished bytes")
	r.put("import-1", "Elsewhere/One.m4b", "bytes")
	protectedDir := filepath.Join(r.dirs["import-1"], "Arkham", "Seeding")
	// A second name for the same directory: the listed path is a symlink to it.
	alias := filepath.Join(t.TempDir(), "seeding-link")
	if err := os.Symlink(protectedDir, alias); err != nil {
		t.Fatal(err)
	}
	notYet := filepath.Join(r.dirs["import-1"], "Arkham", "Queued") // listed, not created yet
	for name, listed := range map[string]string{
		"a path inside the source":     protectedDir,
		"a symlink to a path inside":   alias,
		"a path that does not exist":   notYet,
		"the source itself":            filepath.Join(r.dirs["import-1"], "Arkham"),
		"a path two levels below":      filepath.Join(protectedDir, "deeper", "still"),
		"a trailing-slash spelling":    protectedDir + string(filepath.Separator),
		"a dot-dot spelling of inside": filepath.Join(protectedDir, "x", ".."),
	} {
		setProtected(t, r, listed)
		src := Path{Root: "import-1", Rel: "Arkham"}
		dst := Path{Root: "books-library", Rel: "Arkham-" + string(rune('a'+len(name)%20))}
		if _, err := r.m.Transfer(ctxbg(), books, src, dst, move()); !errors.Is(err, ErrSourceProtected) {
			t.Errorf("%s: Move of the parent directory = %v, want ErrSourceProtected", name, err)
		}
		if _, err := r.m.Quarantine(ctxbg(), books, src, "x"); !errors.Is(err, ErrSourceProtected) {
			t.Errorf("%s: Quarantine of the parent directory = %v, want ErrSourceProtected", name, err)
		}
		if !r.exists("import-1", "Arkham/Seeding/Gotham.m4b") || r.exists("books-library", dst.Rel) {
			t.Fatalf("%s: a refused Move changed something", name)
		}
	}
	// A directory with no protected path in it still moves, and so does a
	// sibling of a protected directory.
	setProtected(t, r, protectedDir)
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Elsewhere"}, Path{Root: "books-library", Rel: "Elsewhere"}, move()); err != nil {
		t.Errorf("Move of a directory holding no protected path: %v", err)
	}
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Arkham/Done"}, Path{Root: "books-library", Rel: "Done"}, move()); err != nil {
		t.Errorf("Move of a sibling of the protected directory: %v", err)
	}
}

// An oracle that is not a SubtreeOracle cannot vouch for a directory, so a
// directory Move or Quarantine is refused while one is registered; a file is
// not affected. A SubtreeOracle answers for the subtree, unknown and error
// included.
func TestDirectoryMoveNeedsAnOracleThatAnswersForSubtrees(t *testing.T) {
	free := oracleFunc(func(context.Context, Path) (bool, bool, error) { return false, true, nil })
	for name, c := range map[string]struct {
		oracle  ProtectionOracle
		refused bool
	}{
		"a file-only oracle":            {free, true},
		"a subtree oracle, all free":    {subtreeFunc{answer(false, true, nil), answer(false, true, nil)}, false},
		"a subtree oracle, protected":   {subtreeFunc{answer(false, true, nil), answer(true, true, nil)}, true},
		"a subtree oracle, unknown":     {subtreeFunc{answer(false, true, nil), answer(false, false, nil)}, true},
		"a subtree oracle, error":       {subtreeFunc{answer(false, true, nil), answer(false, true, errors.New("deluge unreachable"))}, true},
		"a subtree oracle, file marked": {subtreeFunc{answer(true, true, nil), answer(false, true, nil)}, false}, // a directory asks ProtectedUnder only
	} {
		r, _ := transferRig(t, rigOpts{oracles: []ProtectionOracle{c.oracle}})
		r.put("import-1", "Arkham/Gotham.m4b", "bytes")
		dir := Path{Root: "import-1", Rel: "Arkham"}
		_, err := r.m.Transfer(ctxbg(), books, dir, Path{Root: "books-library", Rel: "Arkham"}, move())
		if refused := errors.Is(err, ErrSourceProtected); refused != c.refused || (!c.refused && err != nil) {
			t.Errorf("%s: Move of a directory = %v (refused want %v)", name, err, c.refused)
		}
		_, qerr := r.m.Quarantine(ctxbg(), books, dir, "x")
		if refused := errors.Is(qerr, ErrSourceProtected); refused != c.refused || (!c.refused && qerr != nil && !r.exists("books-library", "Arkham")) {
			t.Errorf("%s: Quarantine of a directory = %v (refused want %v)", name, qerr, c.refused)
		}
	}
	// A file is asked about as a file: the file-only oracle is enough.
	r, _ := transferRig(t, rigOpts{oracles: []ProtectionOracle{free}})
	src := r.put("import-1", "Gotham.m4b", "bytes")
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "Gotham.m4b"}, move()); err != nil {
		t.Errorf("Move of a file with a file-only oracle: %v", err)
	}
}

// A source that is a file when the early check looks and a directory when the
// transfer runs under its locks is asked about again, as a directory.
func TestDirectorySwappedInAfterTheEarlyProtectionCheck(t *testing.T) {
	var r *rig
	calls := 0
	swap := oracleFunc(func(_ context.Context, p Path) (bool, bool, error) {
		calls++
		if calls == 1 {
			abs := r.abs(p.Root, p.Rel)
			if err := os.Remove(abs); err != nil {
				t.Error(err)
			}
			if err := os.MkdirAll(filepath.Join(abs, "Seeding"), 0o755); err != nil {
				t.Error(err)
			}
		}
		return false, true, nil
	})
	r, _ = transferRig(t, rigOpts{oracles: []ProtectionOracle{swap}})
	src := r.put("import-1", "Arkham", "a file for now")
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "Arkham"}, move()); !errors.Is(err, ErrSourceProtected) {
		t.Fatalf("Move of a source swapped for a directory = %v, want ErrSourceProtected", err)
	}
	if !r.exists("import-1", "Arkham/Seeding") || r.exists("books-library", "Arkham") {
		t.Error("the directory moved")
	}
}

// reservedRig is a books library root with the default areas and one whose
// areas are nested under a state directory, plus a core-owned download root.
func reservedRig(t *testing.T) *rig {
	t.Helper()
	nested := spec("books-nested", books, RoleLibrary)
	nested.AppDir, nested.Quarantine = "state/app", "state/held"
	caps := func(o storage.Owner, c string) bool { return c == CapabilityDownload && o == tv }
	return newRig(t, newStore(t), rigOpts{caps: caps},
		spec("books-library", books, RoleLibrary), nested, spec("dl-1", CoreOwner, RoleDownload), spec("tv-library", tv, RoleLibrary))
}

// Only core names a root's AppDir or Quarantine area as a transfer endpoint, a
// quarantine source or a release target.
func TestReservedAreasAreReachableByCoreOnly(t *testing.T) {
	r := reservedRig(t)
	r.put("books-library", "Gotham/01.m4b", "audio")
	r.put("books-library", "statement/notes.txt", "a sibling that only shares a prefix")
	r.put("books-nested", "Gotham/01.m4b", "audio")
	r.put("books-nested", "statement/notes.txt", "a sibling that only shares a prefix")
	good := Path{Root: "books-library", Rel: "Gotham/01.m4b"}
	sentinel := Path{Root: "books-library", Rel: DefaultAppDir + "/root.json"}
	if !r.exists("books-library", DefaultAppDir+"/root.json") {
		t.Fatal("setup: no sentinel")
	}
	// A symlink inside the root that leads into the AppDir.
	if err := os.Symlink(DefaultAppDir, r.abs("books-library", "backdoor")); err != nil {
		t.Fatal(err)
	}
	cp := NewTransferOptions(Copy, PermLibrary, VerifySize)
	cp.MkdirAll = true
	for name, c := range map[string]struct {
		src, dst Path
		opts     TransferOptions
	}{
		"copy the sentinel out":              {sentinel, Path{Root: "books-library", Rel: "root-copy.json"}, cp},
		"move the sentinel out":              {sentinel, Path{Root: "books-library", Rel: "root-copy.json"}, move()},
		"write into the AppDir":              {good, Path{Root: "books-library", Rel: DefaultAppDir + "/planted.json"}, cp},
		"write into the AppDir, upper case":  {good, Path{Root: "books-library", Rel: ".MEDIACORE/planted.json"}, cp},
		"write into the quarantine area":     {good, Path{Root: "books-library", Rel: DefaultQuarantine + "/01H/planted.m4b"}, cp},
		"read from the quarantine area":      {Path{Root: "books-library", Rel: DefaultQuarantine + "/x"}, Path{Root: "books-library", Rel: "x"}, cp},
		"write through a symlink to AppDir":  {good, Path{Root: "books-library", Rel: "backdoor/planted.json"}, cp},
		"read through a symlink to AppDir":   {Path{Root: "books-library", Rel: "backdoor/root.json"}, Path{Root: "books-library", Rel: "x.json"}, cp},
		"replace the sentinel":               {good, sentinel, func() TransferOptions { o := cp; o.Replace = &ReplaceSpec{ExpectSHA256: sum("x")}; return o }()},
		"hardlink the sentinel":              {sentinel, Path{Root: "books-library", Rel: "link.json"}, NewTransferOptions(Hardlink, PermLibrary, VerifySize)},
		"symlink the sentinel":               {sentinel, Path{Root: "books-library", Rel: "sym.json"}, NewTransferOptions(Symlink, PermLibrary, VerifySize)},
		"move a directory holding the areas": {Path{Root: "books-nested", Rel: "state"}, Path{Root: "books-nested", Rel: "state-moved"}, move()},
		"write into a nested AppDir":         {Path{Root: "books-nested", Rel: "Gotham/01.m4b"}, Path{Root: "books-nested", Rel: "state/app/planted"}, cp},
		"write into a nested quarantine":     {Path{Root: "books-nested", Rel: "Gotham/01.m4b"}, Path{Root: "books-nested", Rel: "state/held/planted"}, cp},
	} {
		if _, err := r.m.Transfer(ctxbg(), books, c.src, c.dst, c.opts); !errors.Is(err, ErrReservedPath) {
			t.Errorf("%s: = %v, want ErrReservedPath", name, err)
		}
	}
	if r.exists("books-library", "root-copy.json") || r.exists("books-library", DefaultAppDir+"/planted.json") || r.exists("books-nested", "state-moved") {
		t.Fatal("a refused transfer changed something")
	}
	if !r.exists("books-library", DefaultAppDir+"/root.json") || !r.exists("books-nested", "state/app/root.json") {
		t.Fatal("a sentinel is gone")
	}
	// A directory that only shares a prefix with the areas moves as usual.
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "books-nested", Rel: "statement"}, Path{Root: "books-nested", Rel: "statement-moved"}, move()); err != nil {
		t.Errorf("a prefix-sharing directory: %v", err)
	}
	// Core is not refused: it reads the sentinel of its own download root.
	dlSentinel := Path{Root: "dl-1", Rel: DefaultAppDir + "/root.json"}
	if _, err := r.m.Transfer(ctxbg(), CoreOwner, dlSentinel, Path{Root: "dl-1", Rel: "sentinel-copy.json"}, cp); err != nil {
		t.Errorf("core copying out of its own AppDir: %v", err)
	}
	// Quarantine: the areas, a directory holding them, and the sentinel.
	for name, p := range map[string]Path{
		"the AppDir":                {Root: "books-library", Rel: DefaultAppDir},
		"the sentinel":              {Root: "books-library", Rel: DefaultAppDir + "/root.json"},
		"the quarantine area":       {Root: "books-library", Rel: DefaultQuarantine},
		"a directory holding both":  {Root: "books-nested", Rel: "state"},
		"through a symlink to both": {Root: "books-library", Rel: "backdoor"},
	} {
		if _, err := r.m.Quarantine(ctxbg(), books, p, "x"); !errors.Is(err, ErrReservedPath) {
			t.Errorf("Quarantine of %s = %v, want ErrReservedPath", name, err)
		}
	}
	// Release may not land in the areas, and the record stays held.
	id, err := r.m.Quarantine(ctxbg(), books, good, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{DefaultAppDir + "/released", DefaultQuarantine + "/again", "backdoor/released"} {
		if _, err := r.m.Release(ctxbg(), books, id, &Path{Root: "books-library", Rel: rel}); !errors.Is(err, ErrReservedPath) {
			t.Errorf("Release into %s = %v, want ErrReservedPath", rel, err)
		}
	}
	if held, _ := r.m.Quarantined(ctxbg(), books); len(held) != 1 {
		t.Fatalf("%d records held after refused releases, want 1", len(held))
	}
	if _, err := r.m.Release(ctxbg(), books, id, nil); err != nil || !r.exists("books-library", good.Rel) {
		t.Errorf("Release to the original path = %v", err)
	}
}

// A filesystem view hands out the areas to core alone: the owner reads them
// but cannot change them, a reader that is not the owner cannot see them.
func TestFilesystemViewsSealTheReservedAreas(t *testing.T) {
	r := reservedRig(t)
	r.put("books-library", "Gotham/01.m4b", "audio")
	r.put("books-nested", "Gotham/01.m4b", "audio")
	r.put("dl-1", "complete/Gotham.m4b", "audio")
	owner := func(id RootID, o storage.Owner) RootFS {
		root, err := r.m.Root(id)
		if err != nil {
			t.Fatal(err)
		}
		fsys, err := root.FS(o)
		if err != nil {
			t.Fatal(err)
		}
		return fsys
	}
	sealed := func(err error) bool { return errors.Is(err, ErrReservedPath) }
	own := owner("books-library", books)
	if f, err := own.Open(DefaultAppDir + "/root.json"); err != nil {
		t.Errorf("the owner reading its sentinel: %v", err)
	} else {
		f.Close()
	}
	if _, err := own.OpenFile("Gotham/01.m4b", os.O_RDONLY, 0); err != nil {
		t.Errorf("the owner reading a book: %v", err)
	}
	if err := own.Remove(DefaultAppDir + "/root.json"); !sealed(err) {
		t.Errorf("the owner removing its sentinel = %v, want ErrReservedPath", err)
	}
	if _, err := own.CreateExclusive(DefaultAppDir+"/planted", 0o644); !sealed(err) {
		t.Errorf("the owner creating in the AppDir = %v, want ErrReservedPath", err)
	}
	if err := own.MkdirAll(DefaultQuarantine+"/01H", 0o755); !sealed(err) {
		t.Errorf("the owner making a quarantine directory = %v, want ErrReservedPath", err)
	}
	if err := own.RenameNoReplace(DefaultAppDir+"/root.json", "gone.json"); !sealed(err) {
		t.Errorf("the owner renaming the sentinel out = %v, want ErrReservedPath", err)
	}
	if err := own.RenameNoReplace("Gotham/01.m4b", DefaultAppDir+"/in.m4b"); !sealed(err) {
		t.Errorf("the owner renaming into the AppDir = %v, want ErrReservedPath", err)
	}
	if err := own.Link(DefaultAppDir+"/root.json", "link.json"); !sealed(err) {
		t.Errorf("the owner hard-linking the sentinel = %v, want ErrReservedPath", err)
	}
	if _, err := own.OpenFile(DefaultAppDir+"/root.json", os.O_WRONLY|os.O_TRUNC, 0); !sealed(err) {
		t.Errorf("the owner truncating the sentinel = %v, want ErrReservedPath", err)
	}
	if err := owner("books-nested", books).RenameNoReplace("state", "state-moved"); !sealed(err) {
		t.Errorf("the owner renaming a directory that holds the areas = %v, want ErrReservedPath", err)
	}
	if err := owner("books-nested", books).MkdirAll("state/other", 0o755); err != nil {
		t.Errorf("a sibling of the areas under their parent: %v", err)
	}
	if !r.exists("books-library", DefaultAppDir+"/root.json") || !r.exists("books-nested", "state/app/root.json") {
		t.Fatal("a sentinel changed")
	}
	// A reader that is not the owner (the download capability) sees neither
	// area, and reads the rest.
	dl := owner("dl-1", tv)
	if _, err := dl.Open("complete/Gotham.m4b"); err != nil {
		t.Errorf("a download reader reading a download: %v", err)
	}
	for name, op := range map[string]func() error{
		"Open":    func() error { _, err := dl.Open(DefaultAppDir + "/root.json"); return err },
		"Stat":    func() error { _, err := dl.Stat(DefaultAppDir + "/root.json"); return err },
		"Lstat":   func() error { _, err := dl.Lstat(DefaultQuarantine); return err },
		"ReadDir": func() error { _, err := dl.ReadDir(DefaultAppDir); return err },
	} {
		if err := op(); !sealed(err) {
			t.Errorf("a download reader's %s of a reserved name = %v, want ErrReservedPath", name, err)
		}
	}
	// The same reader's other reads: digest, host path, resolving the host path.
	tvScope, err := NewScope(r.m, tv)
	if err != nil {
		t.Fatal(err)
	}
	reserved := Path{Root: "dl-1", Rel: DefaultAppDir + "/root.json"}
	if _, err := tvScope.Hash(ctxbg(), reserved); !sealed(err) {
		t.Errorf("a download reader hashing the sentinel = %v, want ErrReservedPath", err)
	}
	if _, err := tvScope.Abs(reserved); !sealed(err) {
		t.Errorf("a download reader asking for the sentinel's host path = %v, want ErrReservedPath", err)
	}
	abs, _ := r.m.Abs(reserved)
	if _, err := tvScope.Resolve(abs); !sealed(err) {
		t.Errorf("a download reader resolving the sentinel's host path = %v, want ErrReservedPath", err)
	}
	if _, err := tvScope.Hash(ctxbg(), Path{Root: "dl-1", Rel: "complete/Gotham.m4b"}); err != nil {
		t.Errorf("a download reader hashing a download: %v", err)
	}
	// Core's own view is open: it keeps its state there.
	core := owner("dl-1", CoreOwner)
	f, err := core.CreateExclusive(DefaultAppDir+"/core-state", 0o644)
	if err != nil {
		t.Fatalf("core creating in its AppDir: %v", err)
	}
	f.Close()
	if err := core.Remove(DefaultAppDir + "/core-state"); err != nil {
		t.Errorf("core removing from its AppDir: %v", err)
	}
}
