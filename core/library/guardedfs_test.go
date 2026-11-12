// file: core/library/guardedfs_test.go
// version: 1.0.0
// guid: 3a1d8f5c-92b4-4c7e-8a35-6f0e9b2d41c8
// last-edited: 2026-11-11

package library

import (
	"errors"
	"os"
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

// A symlink swapped in after the name was judged, so that the open lands in the
// AppDir, is refused: the opened descriptor is examined and the name is judged
// again. The swap is made by the test hook between the check and the open.
func TestGuardedFSRefusesALinkSwappedInAfterTheCheck(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	*hook = func(stage, name string) {
		if stage == "checked" && name == "swapme" && !swapped {
			swapped = true
			swapForLink(t, r, "swapme", DefaultAppDir)
		}
	}
	if _, err := g.ReadDir("swapme"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("ReadDir through a link swapped in after the check = %v, want ErrReservedPath", err)
	}
	if !swapped {
		t.Fatal("the hook never ran")
	}
	// The same swap for a file read: the name is judged benign (a file under a
	// plain directory), then the directory becomes a link to the AppDir.
	r.put("books-library", "swapme2/"+sentinelName, "a benign file with the sentinel's name")
	again := false
	*hook = func(stage, name string) {
		if stage == "checked" && name == "swapme2/"+sentinelName && !again {
			again = true
			swapForLink(t, r, "swapme2", DefaultAppDir)
		}
	}
	f, err := g.Open("swapme2/" + sentinelName)
	if err == nil {
		_ = f.Close()
		t.Fatal("Open of the sentinel through a swapped link succeeded")
	}
	if !errors.Is(err, ErrReservedPath) || !again {
		t.Fatalf("Open through a swapped link = %v (hook ran %v), want ErrReservedPath", err, again)
	}
}

// A link swapped in so that the open lands in the AppDir, and swapped back
// before the name is judged again, is refused too: the name no longer reaches
// the descriptor that was opened.
func TestGuardedFSRefusesALinkSwappedBackAfterTheOpen(t *testing.T) {
	r, root, hook := guardedRig(t)
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	*hook = func(stage, name string) {
		if name != "swapme" {
			return
		}
		switch stage {
		case "checked":
			swapForLink(t, r, "swapme", DefaultAppDir) // the open lands in the AppDir
		case "opened":
			if err := os.Remove(r.abs("books-library", "swapme")); err != nil {
				t.Error(err)
			}
			r.put("books-library", "swapme/readme.txt", "a benign directory again") // the name is judged benign
		}
	}
	if _, err := g.ReadDir("swapme"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("ReadDir with the link swapped back after the open = %v, want ErrReservedPath", err)
	}
}

// With nothing swapped, a plain directory reads and the hook sees both stages.
func TestGuardedFSReadsAPlainDirectoryAndRunsBothStages(t *testing.T) {
	_, root, hook := guardedRig(t)
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
	if err != nil || len(ents) != 1 {
		t.Fatalf("ReadDir = %d entries, %v", len(ents), err)
	}
	if len(stages) != 2 || stages[0] != "checked" || stages[1] != "opened" {
		t.Errorf("stages = %v, want [checked opened]", stages)
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
