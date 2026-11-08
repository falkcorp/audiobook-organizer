// file: core/internal/bootseam/bootseamtest/recorder_test.go
// version: 1.0.0
// guid: 7088e36e-83e4-4533-b4d0-a40f7503e603
// last-edited: 2026-11-07

package bootseamtest

import (
	"errors"
	"testing"

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
