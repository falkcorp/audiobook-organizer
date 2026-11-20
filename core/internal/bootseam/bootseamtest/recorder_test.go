// file: core/internal/bootseam/bootseamtest/recorder_test.go
// version: 1.1.0
// guid: 7088e36e-83e4-4533-b4d0-a40f7503e603
// last-edited: 2026-11-19

package bootseamtest

import (
	"errors"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"
)

func create(t *testing.T, fs vfs.FS, path string) error {
	t.Helper()
	f, err := fs.Create(path, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("robin")); err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// TestRecorderLabels: the outer region's label wins, an operation outside
// every region is LabelPebble on Pebble's own files (but a WAL write is
// not) and LabelNone otherwise, and ordinals count per label.
func TestRecorderLabels(t *testing.T) {
	r := New(vfs.NewCrashableMem(), Options{})
	fs := r.FS()
	if err := fs.MkdirAll("/cave", 0o700); err != nil {
		t.Fatal(err)
	}
	exitOuter := r.Enter("swap.outer")
	exitInner := r.Enter("swap.inner")
	if err := create(t, fs, "/cave/a"); err != nil {
		t.Fatal(err)
	}
	exitInner()
	exitInner() // idempotent
	if err := create(t, fs, "/cave/b"); err != nil {
		t.Fatal(err)
	}
	exitOuter()
	for _, p := range []string{"/cave/c", "/cave/MANIFEST-000001", "/cave/000002.log"} {
		if err := create(t, fs, p); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string][]string{ // path → labels of create, write, sync
		"/cave/a":               {"swap.outer", "swap.outer", "swap.outer"},
		"/cave/b":               {"swap.outer", "swap.outer", "swap.outer"},
		"/cave/c":               {LabelNone, LabelNone, LabelNone},
		"/cave/MANIFEST-000001": {LabelPebble, LabelPebble, LabelPebble},
		"/cave/000002.log":      {LabelPebble, LabelNone, LabelNone},
	}
	got := map[string][]string{}
	for _, ev := range r.Events() {
		if ev.Kind == errorfs.OpMkdirAll {
			continue
		}
		got[ev.Path] = append(got[ev.Path], ev.Label)
	}
	for p, w := range want {
		if len(got[p]) != len(w) {
			t.Fatalf("%s: labels %v, want %v", p, got[p], w)
		}
		for i := range w {
			if got[p][i] != w[i] {
				t.Fatalf("%s: labels %v, want %v", p, got[p], w)
			}
		}
	}
	last := -1
	for _, ev := range r.Events() {
		if ev.Label == "swap.outer" {
			if ev.Ordinal != last+1 {
				t.Fatalf("ordinal %d after %d", ev.Ordinal, last)
			}
			last = ev.Ordinal
		}
	}
}

// TestRecorderClonesAndFails: a clone is taken before each kill point (so
// the clone before a create lacks the file) and Fail's error is returned by
// the chosen operation, which then does not run.
func TestRecorderClonesAndFails(t *testing.T) {
	boom := errors.New("injected")
	r := New(vfs.NewCrashableMem(), Options{Clones: true, Fail: func(ev Event) error {
		if ev.Path == "/cave/joker" && ev.Kind == errorfs.OpCreate {
			return boom
		}
		return nil
	}})
	fs := r.FS()
	if err := fs.MkdirAll("/cave", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := create(t, fs, "/cave/bat"); err != nil {
		t.Fatal(err)
	}
	if err := create(t, fs, "/cave/joker"); !errors.Is(err, boom) {
		t.Fatalf("create: %v, want the injected error", err)
	}
	if _, err := r.Mem().Stat("/cave/joker"); err == nil {
		t.Fatal("the failed create ran")
	}
	var sawCreate bool
	for _, c := range r.Clones() {
		if c.Event.Path == "/cave/bat" && c.Event.Kind == errorfs.OpCreate {
			sawCreate = true
			if _, err := c.FS.Stat("/cave/bat"); err == nil {
				t.Fatal("the clone before the create has the file")
			}
		}
		if !c.Event.Kill() {
			t.Fatalf("a clone before a non-kill operation %v", c.Event.Kind)
		}
	}
	if !sawCreate {
		t.Fatal("no clone before the create")
	}
}

// TestRecorderCloneIf (R1-21 review M6): without Clones, CloneIf takes a
// clone before each kill point it accepts and before no other operation.
func TestRecorderCloneIf(t *testing.T) {
	r := New(vfs.NewCrashableMem(), Options{CloneIf: func(ev Event) bool { return ev.Label == "swap.back.mv" }})
	fs := r.FS()
	if err := fs.MkdirAll("/cave", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := create(t, fs, "/cave/bat"); err != nil {
		t.Fatal(err)
	}
	exit := r.Enter("swap.back.mv")
	if err := create(t, fs, "/cave/robin"); err != nil {
		t.Fatal(err)
	}
	exit()
	var got []errorfs.OpKind
	for _, c := range r.Clones() {
		if c.Event.Label != "swap.back.mv" || c.Event.Path != "/cave/robin" {
			t.Fatalf("a clone before %s %v, which CloneIf refuses", c.Event.Path, c.Event.Kind)
		}
		got = append(got, c.Event.Kind)
	}
	if want := []errorfs.OpKind{errorfs.OpCreate, errorfs.OpFileSync}; !slices.Equal(got, want) {
		t.Fatalf("clones before %v, want one before each kill point of the accepted region %v", got, want)
	}
}

// TestRecorderFlagsPebblesWALRotation: an operation Pebble issues inside its
// WAL rotation (a Flush's, here, on a store in a labelled region) is flagged
// PebbleFatal, because Pebble panics on an I/O error there, and no other
// operation of the region is: not an ordinary WAL commit and not a plain file
// operation of the region's own, whose errors their callers handle. The flag
// does not hide the rotation's operations from kill mode: a clone is taken
// before each of them. The flag must not grow: if it covered a plain
// operation, the matrix would stop injecting errors into the region's own
// writes (run.go's injectable).
func TestRecorderFlagsPebblesWALRotation(t *testing.T) {
	r := New(vfs.NewCrashableMem(), Options{Clones: true})
	fs := r.FS()
	if err := fs.MkdirAll("/cave", 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := pebble.Open("/cave/db", &pebble.Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	exit := r.Enter("storage.rewrite.copy")
	before := len(r.Events())
	if err := db.Set([]byte("robin"), []byte("nightwing"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := create(t, fs, "/cave/batarang"); err != nil {
		t.Fatal(err)
	}
	ordinary := r.Events()[before:]
	before = len(r.Events())
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	flush := r.Events()[before:]
	exit()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var plain int
	for _, ev := range ordinary {
		if ev.Fatal() {
			t.Errorf("an operation outside a WAL rotation is flagged fatal: %+v", ev)
		}
		if ev.Path == "/cave/batarang" && !ev.Engine && ev.Label == "storage.rewrite.copy" {
			plain++
		}
	}
	if plain == 0 {
		t.Fatalf("the region's plain file operations were not recorded: %+v", ordinary)
	}
	var fatal []Event
	for _, ev := range flush {
		if ev.Fatal() {
			fatal = append(fatal, ev)
		}
	}
	if len(fatal) == 0 {
		t.Fatalf("a Flush's WAL rotation issued no operation flagged fatal: %+v", flush)
	}
	cloned := map[int]bool{}
	for _, c := range r.Clones() {
		if c.Event.Fatal() {
			cloned[c.Event.Seq] = true
		}
	}
	kills := 0
	for _, ev := range fatal {
		if ev.Kill() {
			kills++
			if !cloned[ev.Seq] {
				t.Errorf("kill mode took no clone before the fatal kill point %+v", ev)
			}
		}
	}
	if kills == 0 {
		t.Fatalf("the rotation has no fatal kill point to clone: %+v", fatal)
	}
}
