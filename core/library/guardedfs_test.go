// file: core/library/guardedfs_test.go
// version: 1.1.0
// guid: 3a1d8f5c-92b4-4c7e-8a35-6f0e9b2d41c8
// last-edited: 2026-11-11

package library

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// guardedRig is a books root with one benign directory and a hook the tests
// point at a swap.
func guardedRig(t *testing.T) (*rig, *Root, *func(stage, name string)) {
	t.Helper()
	var hook func(stage, name string)
	r := newRig(t, newStore(t), rigOpts{seams: seams{fsRace: func(stage, name string) {
		if hook != nil {
			hook(stage, name)
		}
	}}}, spec("books-library", books, RoleLibrary))
	r.put("books-library", "Gotham/01.m4b", "audio")
	r.put("books-library", "swapme/readme.txt", "a directory about to be swapped")
	r.put("books-library", "swapme/inner/note.txt", "a directory under the one about to be swapped")
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	return r, root, &hook
}

// swapForLink replaces the directory rel with a symlink to target.
func swapForLink(t *testing.T, r *rig, rel, target string) {
	t.Helper()
	abs := r.abs("books-library", rel)
	if err := os.RemoveAll(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, abs); err != nil {
		t.Fatal(err)
	}
}

// A request-driven read takes the guarded view: the areas are hidden from it
// even though core owns the root, nothing in it is writable, and the owner's and
// core's own views are unchanged.
func TestGuardedFSHidesTheAreasAndWritesNothing(t *testing.T) {
	r, root, _ := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	if ents, err := g.ReadDir("."); err != nil || len(ents) == 0 {
		t.Fatalf("ReadDir of the root = %d entries, %v", len(ents), err)
	}
	if ents, err := g.ReadDir("Gotham"); err != nil || len(ents) != 1 {
		t.Fatalf("ReadDir of a plain directory = %d entries, %v", len(ents), err)
	}
	for name, read := range map[string]func() error{
		"ReadDir of the AppDir":     func() error { _, err := g.ReadDir(DefaultAppDir); return err },
		"ReadDir of the quarantine": func() error { _, err := g.ReadDir(DefaultQuarantine); return err },
		"Open of the sentinel":      func() error { _, err := g.Open(DefaultAppDir + "/" + sentinelName); return err },
		"Stat of the sentinel":      func() error { _, err := g.Stat(DefaultAppDir + "/" + sentinelName); return err },
		"read open of the sentinel": func() error { _, err := g.OpenFile(DefaultAppDir+"/"+sentinelName, os.O_RDONLY, 0); return err },
		"upper-case AppDir":         func() error { _, err := g.ReadDir(".MEDIACORE"); return err },
	} {
		if err := read(); !errors.Is(err, ErrReservedPath) {
			t.Errorf("%s = %v, want ErrReservedPath", name, err)
		}
	}
	if _, err := g.OpenFile("Gotham/x", os.O_WRONLY|os.O_CREATE, 0o644); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("a write open through the guarded view = %v, want ErrReadOnlyRoot", err)
	}
	w, ok := g.(RootFS)
	if !ok {
		t.Fatal("the guarded view is not the local driver")
	}
	if _, err := w.CreateExclusive("Gotham/y", 0o644); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("CreateExclusive through the guarded view = %v, want ErrReadOnlyRoot", err)
	}
	// The unguarded views still read the AppDir: core's own code is trusted.
	for _, caller := range []string{"core", "books"} {
		owner := CoreOwner
		if caller == "books" {
			owner = books
		}
		fsys, err := root.FS(owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.ReadDir(DefaultAppDir); err != nil {
			t.Errorf("%s's own view of the AppDir = %v, want it readable", caller, err)
		}
	}
	_ = r
}

// swapStage runs swap the first time the hook reaches stage for name.
func swapStage(hook *func(stage, name string), stage, name string, swap func()) *bool {
	done := new(bool)
	*hook = func(st, n string) {
		if st == stage && n == name && !*done {
			*done = true
			swap()
		}
	}
	return done
}

// A symlink swapped in after the name was judged, so that the open would land
// in the AppDir, is refused: the decision comes from the descriptors.
func TestGuardedFSRefusesALinkSwappedInAfterTheCheck(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	done := swapStage(hook, "checked", "swapme", func() { swapForLink(t, r, "swapme", DefaultAppDir) })
	if _, err := g.ReadDir("swapme"); !errors.Is(err, ErrReservedPath) || !*done {
		t.Fatalf("ReadDir through a link swapped in after the check = %v (swapped %v), want ErrReservedPath", err, *done)
	}
	// The same swap for a file read: a benign file under a plain directory, then
	// the directory becomes a link to the AppDir.
	r.put("books-library", "swapme2/"+sentinelName, "a benign file with the sentinel's name")
	done = swapStage(hook, "checked", "swapme2/"+sentinelName, func() { swapForLink(t, r, "swapme2", DefaultAppDir) })
	f, err := g.Open("swapme2/" + sentinelName)
	if err == nil {
		_ = f.Close()
		t.Fatal("Open of the sentinel through a swapped link succeeded")
	}
	if !errors.Is(err, ErrReservedPath) || !*done {
		t.Fatalf("Open through a swapped link = %v (swapped %v), want ErrReservedPath", err, *done)
	}
	// And through OpenFile, the read-only open a caller may also use.
	r.put("books-library", "swapme3/"+sentinelName, "another benign file with the sentinel's name")
	done = swapStage(hook, "checked", "swapme3/"+sentinelName, func() { swapForLink(t, r, "swapme3", DefaultAppDir) })
	of, err := g.OpenFile("swapme3/"+sentinelName, os.O_RDONLY, 0)
	if err == nil {
		_ = of.Close()
		t.Fatal("OpenFile of the sentinel through a swapped link succeeded")
	}
	if !errors.Is(err, ErrReservedPath) || !*done {
		t.Fatalf("OpenFile through a swapped link = %v (swapped %v), want ErrReservedPath", err, *done)
	}
}

// A symlink swapped in after the name was resolved, at every place it can
// stand: the last component (the open refuses to follow it), a directory above
// it (the directory that is opened is walked up and found in the area), and a
// directory below an area, not only the area itself.
func TestGuardedFSRefusesALinkSwappedInAfterTheNameWasResolved(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(r.abs("books-library", DefaultQuarantine), "01HELD", "inner")
	if err := os.MkdirAll(held, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		read   func() error
		target string
	}{
		"the last component, a directory": {read: func() error { _, err := g.ReadDir("swapme"); return err }, target: DefaultAppDir},
		"the last component, a file": {read: func() error {
			f, err := g.Open("swapme/readme.txt")
			if f != nil {
				_ = f.Close()
			}
			return err
		}, target: DefaultAppDir},
		"a directory above, into the AppDir": {read: func() error { _, err := g.ReadDir("swapme/inner"); return err },
			target: DefaultAppDir},
		"a directory above, into a directory below the quarantine area": {read: func() error { _, err := g.ReadDir("swapme/inner"); return err },
			target: DefaultQuarantine + "/01HELD"},
	} {
		r.put("books-library", "swapme/readme.txt", "a directory about to be swapped")
		r.put("books-library", "swapme/inner/note.txt", "a directory under the one about to be swapped")
		stage, key := "resolved", "swapme"
		if strings.Contains(name, "above") {
			key = "swapme/inner"
		}
		if strings.HasPrefix(name, "the last component, a file") {
			key = "swapme/readme.txt"
		}
		done := swapStage(hook, stage, key, func() { swapForLink(t, r, "swapme", c.target) })
		if err := c.read(); !errors.Is(err, ErrReservedPath) || !*done {
			t.Errorf("%s: read = %v (swapped %v), want ErrReservedPath", name, err, *done)
		}
		if err := os.RemoveAll(r.abs("books-library", "swapme")); err != nil {
			t.Fatal(err)
		}
	}
}

// The decision is the descriptor's: a link swapped in so that the directory is
// opened inside the AppDir, and swapped back before anything looks at a name,
// is still refused. A check that resolved the name again would find a plain
// directory and let the read through.
func TestGuardedFSDecidesFromTheDescriptorNotTheName(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	*hook = func(stage, name string) {
		if name != "swapme/inner" {
			return
		}
		switch stage {
		case "resolved":
			swapForLink(t, r, "swapme", DefaultAppDir) // the directory is opened inside the AppDir
		case "parent":
			if err := os.Remove(r.abs("books-library", "swapme")); err != nil { // the name is a plain directory again
				t.Error(err)
			}
			r.put("books-library", "swapme/inner/note.txt", "a benign directory again")
			swapped = true
		}
	}
	if _, err := g.ReadDir("swapme/inner"); !errors.Is(err, ErrReservedPath) || !swapped {
		t.Fatalf("ReadDir with the link swapped back = %v (swapped back %v), want ErrReservedPath", err, swapped)
	}
}

// When the place of the directory cannot be established the read is refused:
// here the directory is moved out of the root after it was opened, so the walk
// up from it never meets the root.
func TestGuardedFSRefusesWhenTheWalkUpFails(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	done := swapStage(hook, "parent", "swapme/inner", func() {
		if err := os.Rename(r.abs("books-library", "swapme"), filepath.Join(outside, "swapme")); err != nil {
			t.Fatal(err)
		}
	})
	_, err = g.ReadDir("swapme/inner")
	if !errors.Is(err, ErrReservedPath) || !strings.Contains(err.Error(), "cannot tell where the directory lies") || !*done {
		t.Fatalf("ReadDir of a directory moved out of the root = %v (moved %v), want a refusal that says the place is unknown", err, *done)
	}
}

// The checks on the descriptors stand on their own, whatever the spelling of the
// name: guardedOpen is called directly, past the name check, with the areas'
// own names, as a second name for an area (a bind mount) would present them. The
// leaf's identity and the identity of every directory above it are compared
// with the areas.
func TestGuardedOpenJudgesDescriptorsWithoutTheSpelling(t *testing.T) {
	r, root, _ := guardedRig(t)
	if err := os.MkdirAll(filepath.Join(r.abs("books-library", DefaultQuarantine), "01HELD", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := root.base.readOnlyView()
	c.sealed, c.hidden = true, true
	for _, n := range []string{
		DefaultAppDir,                       // the leaf is an area
		DefaultQuarantine,                   // the leaf is the nested area
		DefaultAppDir + "/" + sentinelName,  // a file whose parent is an area
		DefaultQuarantine + "/01HELD",       // a leaf below an area
		DefaultQuarantine + "/01HELD/inner", // two levels below an area
	} {
		f, err := c.guardedOpen("open", n, n)
		if err == nil {
			_ = f.Close()
			t.Errorf("guardedOpen(%q) succeeded", n)
			continue
		}
		if !errors.Is(err, ErrReservedPath) {
			t.Errorf("guardedOpen(%q) = %v, want ErrReservedPath", n, err)
		}
	}
	for _, n := range []string{".", "Gotham", "Gotham/01.m4b", "swapme/inner", "swapme/inner/note.txt"} {
		f, err := c.guardedOpen("open", n, n)
		if err != nil {
			t.Errorf("guardedOpen(%q) = %v, want it opened", n, err)
			continue
		}
		_ = f.Close()
	}
}

// With nothing swapped, plain names read, benign symlinks are followed, and the
// hook sees the four stages in order.
func TestGuardedFSReadsPlainNamesAndFollowsBenignLinks(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	*hook = func(stage, name string) {
		if name == "swapme" {
			stages = append(stages, stage)
		}
	}
	ents, err := g.ReadDir("swapme")
	if err != nil || len(ents) != 2 {
		t.Fatalf("ReadDir = %d entries, %v", len(ents), err)
	}
	if want := []string{"checked", "resolved", "parent", "opened"}; !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	*hook = nil
	// A directory link and a file link that stay in the plain part of the root.
	if err := os.Symlink("swapme", r.abs("books-library", "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("swapme/readme.txt", r.abs("books-library", "flink")); err != nil {
		t.Fatal(err)
	}
	if ents, err := g.ReadDir("alias/inner"); err != nil || len(ents) != 1 {
		t.Errorf("ReadDir through a directory link = %d entries, %v", len(ents), err)
	}
	for _, n := range []string{"alias/readme.txt", "flink"} {
		f, err := g.Open(n)
		if err != nil {
			t.Errorf("Open(%q) = %v", n, err)
			continue
		}
		buf := make([]byte, 64)
		if k, _ := f.Read(buf); !strings.Contains(string(buf[:k]), "about to be swapped") {
			t.Errorf("Open(%q) read %q", n, buf[:k])
		}
		_ = f.Close()
	}
	// A missing name is not found, not refused.
	if _, err := g.ReadDir("swapme/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadDir of a missing directory = %v, want not-exist", err)
	}
	// A link that leaves the root is refused as escaping, not followed.
	if err := os.Symlink(t.TempDir(), r.abs("books-library", "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReadDir("out"); err == nil {
		t.Error("ReadDir through a link that leaves the root succeeded")
	}
}

// A disabled or replaced root has no guarded view.
func TestGuardedFSRefusesAnUnusableRoot(t *testing.T) {
	r, root, _ := guardedRig(t)
	if _, err := root.GuardedFS(); err != nil {
		t.Fatal(err)
	}
	set := defaultSettings()
	set.roots = nil
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	if _, err := root.GuardedFS(); err == nil {
		t.Error("a retired root handed out a guarded view")
	}
}
