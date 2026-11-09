// file: core/internal/restoreswap/confined_test.go
// version: 1.0.0
// guid: 3ebca9d2-9bb5-458b-bbb0-de88e94132a8
// last-edited: 2026-11-08

package restoreswap

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestConfinedFilesBounds (05-storage §5S.3.15 "Hook confinement", R10-6,
// COR10-4; rule (4)): the reset hook's filesystem follows a path only
// inside the state directory, <db parent>, the intent's delete entries and
// its hook_paths. A relative path or one outside them is ErrConfined and
// touches nothing; a kept path (the state directory, <db parent>, a
// set-aside destination, the live store) is ErrKeptPath, reported to
// onKept, and stays; an entry inside a hook path is removed, and removing
// it again is success.
func TestConfinedFilesBounds(t *testing.T) {
	mem := vfs.NewMem()
	db := dbPathOf("outside")
	s := newSwapper(t, mem, "batcomputer-r1", testNow)
	const id = "20261108T000000Z-confined"
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: restorejournal.ResetFactory, DBPath: db,
		Targets:   ResetTargets(stateDir, db, id, nil, nil),
		HookPaths: []string{"/arkham/openlibrary-dumps"}}
	dump := "/arkham/openlibrary-dumps/authors.jsonl"
	outside := "/arkham/library/Hush.m4b"
	for _, p := range []string{dump, outside, filepath.Join(stateDir, "riddle.txt"), filepath.Join(db, "MANIFEST-000001")} {
		writeFile(t, mem, p, "bat-signal")
	}
	var kept []string
	files := s.Confined(in, func(p string) { kept = append(kept, p) })

	for _, p := range []string{outside, "/arkham/library", "relative/Hush.m4b", "/"} {
		if err := files.RemoveAll(p); !errors.Is(err, storage.ErrConfined) {
			t.Errorf("RemoveAll(%s) = %v, want ErrConfined", p, err)
		}
		if _, err := files.ReadDir(p); !errors.Is(err, storage.ErrConfined) {
			t.Errorf("ReadDir(%s) = %v, want ErrConfined", p, err)
		}
	}
	if _, ok := readFile(t, mem, outside); !ok {
		t.Fatal("a refused path was removed")
	}

	keptPaths := []string{stateDir, filepath.Dir(db), db, restorejournal.SetAsideDest(stateDir, db, id, db)}
	for _, p := range keptPaths {
		if err := files.RemoveAll(p); !errors.Is(err, storage.ErrKeptPath) {
			t.Errorf("RemoveAll(%s) = %v, want ErrKeptPath", p, err)
		}
	}
	if !slices.Equal(kept, keptPaths) {
		t.Fatalf("onKept saw %v, want %v", kept, keptPaths)
	}
	if _, ok := readFile(t, mem, filepath.Join(db, "MANIFEST-000001")); !ok {
		t.Fatal("the live store was removed")
	}

	if err := files.RemoveAll(dump); err != nil {
		t.Fatalf("RemoveAll(%s) = %v", dump, err)
	}
	if _, ok := readFile(t, mem, dump); ok {
		t.Fatal("an entry inside a hook path survived")
	}
	if err := files.RemoveAll(dump); err != nil {
		t.Fatalf("removing what is gone = %v, want success", err)
	}
	if err := files.RemoveAll(filepath.Join(stateDir, "riddle.txt")); err != nil {
		t.Fatalf("an entry inside the state directory = %v", err)
	}
}

// TestConfinedFilesSymlinkEscape (05-storage §5S.3.15 "Hook confinement"):
// on the real filesystem a path inside a hook path whose directory is a
// symbolic link to a directory outside every root is ErrConfined, and the
// file it names stays.
func TestConfinedFilesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	db := filepath.Join(base, "db", "main.pebble")
	dump := filepath.Join(base, "openlibrary-dumps")
	library := filepath.Join(base, "library")
	for _, d := range []string{state, filepath.Dir(db), dump, library} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(library, "Hush.m4b")
	if err := os.WriteFile(victim, []byte("hush"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(library, filepath.Join(dump, "ladder")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{FS: vfs.Default, StateDir: state, BuildID: "batcomputer-r1", Clock: testNow, Owners: []storage.OwnerSpec{wayne(1)}})
	if err != nil {
		t.Fatal(err)
	}
	in := restorejournal.Intent{ID: "r-ladder", Kind: restorejournal.KindReset, Reset: restorejournal.ResetFactory, DBPath: db,
		HookPaths: []string{dump}}
	files := s.Confined(in, nil)
	if err := files.RemoveAll(filepath.Join(dump, "ladder", "Hush.m4b")); !errors.Is(err, storage.ErrConfined) {
		t.Fatalf("removing through a link out of the hook path = %v, want ErrConfined", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the file behind the link: %v", err)
	}
	if err := files.RemoveAll(filepath.Join(dump, "ladder")); err != nil {
		t.Fatalf("removing the link itself = %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("removing the link removed what it named: %v", err)
	}
}

// TestPlanKeptMatchesConfinement (05-storage §5S.3.15 rule (4)): PlanKept,
// the rule the reset hook's Plan counts its wipe with before the intent
// exists, answers as restorejournal.Kept does over the staged intent the
// confined filesystem applies at Run, so the count the operator confirms is
// what Run can remove. The cells include the private directory beside the
// config boot record in the state directory, which only the targets name.
func TestPlanKeptMatchesConfinement(t *testing.T) {
	mem := vfs.NewMem()
	db := dbPathOf("outside")
	s := newSwapper(t, mem, "batcomputer-r1", testNow)
	const id = "20261108T000000Z-plankept"
	staged := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: restorejournal.ResetFactory, DBPath: db,
		Targets: ResetTargets(stateDir, db, id, s.legacySuffixes(), nil)}
	kept := s.PlanKept(db, nil)
	// The private directory beside the config boot record, a target in the
	// state directory: kept only because the targets name the record.
	aside := filepath.Join(filepath.Dir(filepath.Join(stateDir, configBootRecord)), restorejournal.AsideDirName)
	cells := map[string]bool{
		db: true, filepath.Dir(db): true, stateDir: true, restorejournal.Dir(stateDir): true,
		db + restorejournal.CoreSidecarSuffixes()[0]: true,
		filepath.Join(aside, "ledger"):               true,
		filepath.Join(stateDir, "riddle.txt"):        false,
		"/arkham/library/Hush.m4b":                   false,
		"/arkham/openlibrary-dumps":                  false,
	}
	for p, want := range cells {
		if got := kept(p); got != want {
			t.Errorf("PlanKept(%s) = %v, want %v", p, got, want)
		}
		if run := restorejournal.Kept(stateDir, staged, s.legacySuffixes(), p); run != kept(p) {
			t.Errorf("%s: PlanKept %v, the staged intent's rule %v", p, kept(p), run)
		}
	}
}
