// file: core/library/reserved_race_test.go
// version: 1.2.0
// guid: a09c077a-859d-454e-a263-3867a490db97
// last-edited: 2026-11-11

package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// areaNames lists what the books library root's AppDir holds.
func areaNames(t *testing.T, r *rig) []string {
	t.Helper()
	ents, err := os.ReadDir(r.abs("books-library", DefaultAppDir))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

// The engine drives a non-core caller's transfer through a sealed driver, so the
// reserved-area decision is not only the name check that ran before the locks: a
// directory swapped for a link to the AppDir after that check is caught where the
// operation acts. These are the review probes (the swap lands at the operation,
// after check()).
func TestTransferIntoAnAreaAfterTheCheckIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		swapAt string
		opts   TransferOptions
	}{
		"move into a directory swapped for the AppDir": {"import-1:rename:", withMkdir(move())},
		"copy into a directory swapped for the AppDir": {"books-library:create:", copyOpts()},
		"hardlink into a swapped directory":            {"import-1:link:", withMkdir(NewTransferOptions(Hardlink, PermLibrary, VerifySize))},
	} {
		t.Run(name, func(t *testing.T) {
			f := &faults{}
			r, _ := transferRig(t, rigOpts{faults: f})
			src := r.put("import-1", "Arkham/joke.m4b", "bytes")
			root := r.dirs["books-library"]
			if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
				t.Fatal(err)
			}
			before := areaNames(t, r)
			swapped := false
			f.inject = func(label string) error {
				if !swapped && strings.HasPrefix(label, c.swapAt) {
					swapped = true
					if err := os.Remove(filepath.Join(root, "a")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(DefaultAppDir, filepath.Join(root, "a")); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			_, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "a/planted.m4b"}, c.opts)
			if !errors.Is(err, ErrReservedPath) {
				t.Errorf("Transfer = %v, want ErrReservedPath", err)
			}
			// Nothing was made, so nothing is owed: no recovery is pending and no
			// cleanup went looking for a temp that was never created.
			if errors.Is(err, ErrRecoveryPending) {
				t.Errorf("Transfer = %v: nothing is owed to recovery", err)
			}
			if got := f.labelsOf(":remove:"); len(got) != 0 {
				t.Errorf("a cleanup removed something that was never made: %v", got)
			}
			if got := areaNames(t, r); !slices.Equal(got, before) {
				t.Errorf("the AppDir holds %v, was %v", got, before)
			}
			if !r.exists("import-1", src.Rel) || r.journalRows() != 0 || len(r.strays()) != 0 {
				t.Errorf("source kept %v, rows %d, strays %v", r.exists("import-1", src.Rel), r.journalRows(), r.strays())
			}
		})
	}
}

// A non-core Move cannot take a held file out of the quarantine area by swapping
// its source directory for a link into it after the check.
func TestMoveOutOfQuarantineAfterTheCheckIsRefused(t *testing.T) {
	f := &faults{}
	r, _ := transferRig(t, rigOpts{faults: f})
	root := r.dirs["books-library"]
	victim := r.put("books-library", "Gotham/held.m4b", "held bytes")
	id, err := r.m.Quarantine(ctxbg(), books, victim, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b", "held.m4b"), []byte("decoy"), 0o664); err != nil {
		t.Fatal(err)
	}
	heldDir := filepath.Join(DefaultQuarantine, string(id), "Gotham")
	swapped := false
	f.inject = func(label string) error {
		if !swapped && strings.HasPrefix(label, "books-library:rename:b/held.m4b") {
			swapped = true
			if err := os.Rename(filepath.Join(root, "b"), filepath.Join(root, "b.orig")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(heldDir, filepath.Join(root, "b")); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	_, err = r.m.Transfer(ctxbg(), books, Path{Root: "books-library", Rel: "b/held.m4b"}, Path{Root: "books-library", Rel: "stolen.m4b"}, move())
	if !errors.Is(err, ErrReservedPath) {
		t.Errorf("Transfer = %v, want ErrReservedPath", err)
	}
	if _, ok := r.read("books-library", "stolen.m4b"); ok {
		t.Error("a non-core Move took a quarantined file out of the quarantine area")
	}
	if !r.exists("books-library", filepath.ToSlash(filepath.Join(heldDir, "held.m4b"))) {
		t.Error("the held file moved")
	}
}

// Release and Quarantine act for a caller too: the target a Release names, and the
// source a Quarantine takes, are judged from their directories' descriptors.
func TestQuarantineAndReleaseAfterTheCheckAreRefused(t *testing.T) {
	f := &faults{}
	r, _ := transferRig(t, rigOpts{faults: f})
	root := r.dirs["books-library"]
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	// swap puts a link to the AppDir where the directory a was (a is moved aside
	// by the caller first).
	swap := func() {
		if err := os.Symlink(DefaultAppDir, filepath.Join(root, "a")); err != nil {
			t.Fatal(err)
		}
	}
	// Quarantine of a/root.json after a is swapped for the AppDir would move the
	// sentinel into the quarantine area.
	r.put("books-library", "a/root.json", "a decoy sentinel")
	swapped := false
	f.inject = func(label string) error {
		if !swapped && strings.HasPrefix(label, "books-library:rename:a/root.json") {
			swapped = true
			if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a.orig")); err != nil {
				t.Fatal(err)
			}
			swap()
		}
		return nil
	}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "a/root.json"}, "x"); !errors.Is(err, ErrReservedPath) {
		t.Errorf("Quarantine after the swap = %v, want ErrReservedPath", err)
	}
	if !r.exists("books-library", DefaultAppDir+"/"+sentinelName) {
		t.Fatal("the sentinel was moved into quarantine")
	}
	// Release into a/ after a is swapped for the AppDir.
	if err := os.Remove(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "a.orig"), filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	f.inject = nil
	id, err := r.m.Quarantine(ctxbg(), books, r.put("books-library", "Gotham/held.m4b", "held"), "x")
	if err != nil {
		t.Fatal(err)
	}
	swapped = false
	f.inject = func(label string) error {
		if !swapped && strings.HasPrefix(label, "books-library:rename:") && strings.HasSuffix(label, "->a/back.m4b") {
			swapped = true
			if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a.orig")); err != nil {
				t.Fatal(err)
			}
			swap()
		}
		return nil
	}
	before := areaNames(t, r)
	if _, err := r.m.Release(ctxbg(), books, id, &Path{Root: "books-library", Rel: "a/back.m4b"}); !errors.Is(err, ErrReservedPath) {
		t.Errorf("Release after the swap = %v, want ErrReservedPath", err)
	}
	if got := areaNames(t, r); !slices.Equal(got, before) {
		t.Errorf("the AppDir holds %v, was %v", got, before)
	}
}

// Every mutation of a module's own view is decided from the descriptor of the
// directory it acts in. The test swaps the directory for a link to the AppDir
// between the name check and the act (the "checked" hook) and the mutation must
// still be refused.
func TestSealedViewMutationsAreJudgedFromTheDescriptor(t *testing.T) {
	type op struct {
		name string
		prep func(r *rig)
		run  func(fsys RootFS) error
	}
	ops := []op{
		{"create", nil, func(f RootFS) error { _, err := f.CreateExclusive("a/planted.m4b", 0o644); return err }},
		{"write open", nil, func(f RootFS) error {
			_, err := f.OpenFile("a/planted.m4b", os.O_WRONLY|os.O_CREATE, 0o644)
			return err
		}},
		{"mkdir", nil, func(f RootFS) error { return f.MkdirAll("a/sub/deeper", 0o755) }},
		{"remove", func(r *rig) {
			if err := os.WriteFile(r.abs("books-library", DefaultAppDir+"/victim.json"), []byte("state"), 0o600); err != nil {
				panic(err)
			}
		}, func(f RootFS) error { return f.Remove("a/victim.json") }},
		{"symlink", nil, func(f RootFS) error { return f.Symlink(Path{Root: "books-library", Rel: "Gotham"}, "a/ln") }},
		{"link into", nil, func(f RootFS) error { return f.Link("Gotham/01.m4b", "a/linked.m4b") }},
		{"link from", func(r *rig) {
			if err := os.WriteFile(r.abs("books-library", DefaultAppDir+"/leak.json"), []byte("state"), 0o600); err != nil {
				panic(err)
			}
		}, func(f RootFS) error { return f.Link("a/leak.json", "leaked.json") }},
		{"rename into", nil, func(f RootFS) error { return f.RenameNoReplace("Gotham/01.m4b", "a/moved.m4b") }},
		{"rename from", func(r *rig) {
			if err := os.WriteFile(r.abs("books-library", DefaultAppDir+"/take.json"), []byte("state"), 0o600); err != nil {
				panic(err)
			}
		}, func(f RootFS) error { return f.RenameNoReplace("a/take.json", "taken.json") }},
	}
	for _, o := range ops {
		t.Run(o.name, func(t *testing.T) {
			var hook func(stage, name string)
			r := newRig(t, newStore(t), rigOpts{seams: seams{fsRace: func(stage, name string) {
				if hook != nil {
					hook(stage, name)
				}
			}}}, spec("books-library", books, RoleLibrary))
			r.put("books-library", "Gotham/01.m4b", "audio")
			if err := os.Mkdir(r.abs("books-library", "a"), 0o755); err != nil {
				t.Fatal(err)
			}
			if o.prep != nil {
				o.prep(r)
			}
			root, err := r.m.Root("books-library")
			if err != nil {
				t.Fatal(err)
			}
			fsys, err := root.FS(books)
			if err != nil {
				t.Fatal(err)
			}
			before := areaNames(t, r)
			swapped := false
			hook = func(stage, name string) {
				if stage == "checked" && !swapped && strings.HasPrefix(name, "a/") {
					swapped = true
					if err := os.RemoveAll(r.abs("books-library", "a")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(DefaultAppDir, r.abs("books-library", "a")); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = o.run(fsys)
			if !swapped {
				t.Fatalf("the hook never ran (%v)", err)
			}
			if !errors.Is(err, ErrReservedPath) {
				t.Errorf("%s through a swapped directory = %v, want ErrReservedPath", o.name, err)
			}
			if got := areaNames(t, r); !slices.Equal(got, before) {
				t.Errorf("the AppDir holds %v, was %v", got, before)
			}
			for _, leak := range []string{"leaked.json", "taken.json"} {
				if r.exists("books-library", leak) {
					t.Errorf("%s now exists: the area's content left the area", leak)
				}
			}
		})
	}
}

// A Move of the area itself by identity: someone with access to the disk renames
// a root's quarantine directory to an innocent name after the name check, and the
// module's rename of that name would carry the whole area away. The leaf is judged
// by identity, and the identity was remembered while the area still had its own
// name. (The AppDir cannot stand in for this: moving it away unmounts the root, a
// different gate, so the test uses a quarantine directory that lies outside it.)
func TestRenameOfAnAreaRenamedAwayIsRefusedByIdentity(t *testing.T) {
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
	// Any earlier check remembers the area's identity.
	if err := fsys.RenameNoReplace("x", "y"); err != nil {
		t.Fatalf("an ordinary rename: %v", err)
	}
	if err := fsys.RenameNoReplace("y", "x"); err != nil {
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace(".failed", "taken"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("rename of the quarantine area by its name = %v, want ErrReservedPath", err)
	}
	swapped := false
	hook = func(stage, name string) {
		if stage == "checked" && !swapped && name == "x" {
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
		t.Fatalf("rename of the renamed-away quarantine area = %v, want ErrReservedPath", err)
	}
	if !swapped {
		t.Fatal("the hook never ran")
	}
	if !r.exists("books-library", "x/held.m4b") || r.exists("books-library", "elsewhere/held.m4b") {
		t.Error("the area moved")
	}
}

// osRootMaxLinks is os.Root's own limit on the links it follows in one lookup
// (os.rootMaxSymlinks): the reads it performs itself give up past it with ELOOP.
const osRootMaxLinks = 8

// A mutation is carried out on the path the resolution expanded, so it is the
// resolution, not os.Root, that must see a long chain end in the AppDir; the
// review probe wrote through a 45-link chain into it. Past maxLinkHops the chain
// is undecided and refused too. The mutation is made through the view directly:
// a Transfer through a chain longer than os.Root's limit fails earlier, at its
// own read of the destination, which would make this test pass without the
// resolution doing anything.
func TestLongLinkChainsToAnAreaAreRefused(t *testing.T) {
	for _, hops := range []int{3, osRootMaxLinks, osRootMaxLinks + 1, 30, 45, 100, maxLinkHops - 1, maxLinkHops, maxLinkHops + 1, 300} {
		t.Run(fmt.Sprintf("hops=%d", hops), func(t *testing.T) {
			r, _ := transferRig(t, rigOpts{})
			src := r.put("import-1", "Arkham/joke.m4b", "bytes")
			root := r.dirs["books-library"]
			if err := os.Symlink(DefaultAppDir, filepath.Join(root, "l0")); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= hops; i++ {
				if err := os.Symlink(fmt.Sprintf("l%d", i-1), filepath.Join(root, fmt.Sprintf("l%d", i))); err != nil {
					t.Fatal(err)
				}
			}
			before := areaNames(t, r)
			rel := fmt.Sprintf("l%d/planted.m4b", hops)

			mr, err := r.m.Root("books-library")
			if err != nil {
				t.Fatal(err)
			}
			fsys, err := mr.FS(books)
			if err != nil {
				t.Fatal(err)
			}
			f, err := fsys.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err == nil {
				err = errors.Join(errors.New("the create succeeded"), f.Close())
			}
			if !errors.Is(err, ErrReservedPath) {
				t.Errorf("create through a %d-link chain to the AppDir = %v, want ErrReservedPath", hops, err)
			}

			// Where os.Root itself can follow the chain, a whole Transfer reaches the
			// check and is refused by it.
			if hops <= osRootMaxLinks {
				dst := Path{Root: "books-library", Rel: rel}
				if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); !errors.Is(err, ErrReservedPath) {
					t.Errorf("Copy through a %d-link chain to the AppDir = %v, want ErrReservedPath", hops, err)
				}
			}
			if got := areaNames(t, r); !slices.Equal(got, before) {
				t.Errorf("the AppDir holds %v, was %v", got, before)
			}
		})
	}
}
