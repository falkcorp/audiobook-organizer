// file: core/internal/statefs/statefs_test.go
// version: 1.0.0
// guid: 908b41aa-5da6-48aa-8d3f-a31fbba7ef01
// last-edited: 2026-11-19

package statefs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/statefs"
)

// TestOpen_ProductionIsOSRoot: with no seam (or a seam with no filesystem)
// a Dir is an *os.Root over the real directory: a symbolic link is reported
// as one and never followed out of it, a name that leaves the directory is
// refused, the exclusive create refuses an existing name and makes the file
// with the mode asked, the link refuses to replace, and nothing the calls
// write lands anywhere but the real directory.
func TestOpen_ProductionIsOSRoot(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no seams":    t.Context(),
		"seams no FS": bootseam.With(t.Context(), &bootseam.Seams{BuildID: "batcomputer"}),
		"default FS":  bootseam.With(t.Context(), &bootseam.Seams{FS: vfs.Default}),
	} {
		t.Run(name, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "joker"), []byte("ha"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "joker"), filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			d, err := statefs.Open(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := d.Close(); err != nil {
					t.Error(err)
				}
			}()
			if fi, err := d.Lstat("link"); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
				t.Fatalf("Lstat(link) = %v, %v; want a symbolic link", fi, err)
			}
			if _, err := d.Open("link"); err == nil {
				t.Fatal("Open followed a symbolic link out of the directory")
			}
			if err := d.WriteNew("l", "../escape", []byte("x"), 0o600); err == nil {
				t.Fatal("WriteNew wrote outside the directory")
			}
			if err := d.WriteNew("l", "cave", []byte("alfred"), 0o640); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Lstat(filepath.Join(dir, "cave"))
			if err != nil || fi.Mode().Perm() != 0o640 {
				t.Fatalf("the created file is %v, %v; want mode 0640 on the real disk", fi, err)
			}
			if err := d.WriteNew("l", "cave", []byte("bane"), 0o600); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("WriteNew over an existing name = %v, want fs.ErrExist", err)
			}
			if err := d.WriteNew("l", "manor", []byte("bruce"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := d.Link("l", "manor", "cave"); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("Link over an existing name = %v, want fs.ErrExist", err)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "cave")); string(b) != "alfred" {
				t.Fatalf("cave holds %q after a refused link, want alfred", b)
			}
			if err := d.Chmod("l", "cave", 0o600); err != nil {
				t.Fatal(err)
			}
			if fi, _ := os.Lstat(filepath.Join(dir, "cave")); fi.Mode().Perm() != 0o600 {
				t.Fatalf("Chmod left mode %v", fi.Mode().Perm())
			}
			if err := d.MkdirAll("l", "wing/east", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := d.Rename("l", "manor", "wing/east/manor"); err != nil {
				t.Fatal(err)
			}
			if err := d.SyncDir("l", "wing/east"); err != nil {
				t.Fatal(err)
			}
			if b, err := d.ReadFile("wing/east/manor"); err != nil || string(b) != "bruce" {
				t.Fatalf("ReadFile = %q, %v", b, err)
			}
			ents, err := d.ReadDir(".")
			if err != nil {
				t.Fatal(err)
			}
			if got := names(ents); !slices.Equal(got, []string{"cave", "link", "wing"}) {
				t.Fatalf("ReadDir = %q", got)
			}
			if err := d.Remove("l", "cave"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "cave")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Remove left cave: %v", err)
			}
		})
	}
}

// TestOpen_InMemorySeam: under a seam over an in-memory filesystem every
// call goes to that filesystem at the same absolute path, none to the real
// disk; every mutating call is recorded under the label it was given;
// regular files report 0600 and directories 0700 (the filesystem keeps no
// modes); the exclusive create and the link refuse an existing name; a name
// that leaves the directory is refused; and the caller's buffer is not the
// one handed to the filesystem.
func TestOpen_InMemorySeam(t *testing.T) {
	dir := t.TempDir() // exists on disk too: nothing may land there
	mem := vfs.NewCrashableMem()
	if err := mem.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{})
	ctx := bootseam.With(t.Context(), rec.Seams())
	d, err := statefs.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("oracle")
	if err := d.WriteNew("bat.write", "tmp", data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("oracle")) {
		t.Fatalf("WriteNew changed the caller's buffer to %q", data)
	}
	if err := d.WriteNew("bat.write", "tmp", data, 0o600); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteNew over an existing name = %v, want fs.ErrExist", err)
	}
	if err := d.Link("bat.link", "tmp", "key"); err != nil {
		t.Fatal(err)
	}
	if err := d.Link("bat.link", "tmp", "key"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Link over an existing name = %v, want fs.ErrExist", err)
	}
	if err := d.Remove("bat.remove", "tmp"); err != nil {
		t.Fatal(err)
	}
	if err := d.MkdirAll("bat.mkdir", "sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.Rename("bat.rename", "key", "sub/key"); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncDir("bat.sync", "sub"); err != nil {
		t.Fatal(err)
	}
	if err := d.Chmod("bat.chmod", "sub/key", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Chmod("bat.chmod", "absent", 0o600); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Chmod of an absent name = %v, want fs.ErrNotExist", err)
	}
	for _, bad := range []string{"../x", "/etc/passwd"} {
		if err := d.WriteNew("bat.write", bad, data, 0o600); err == nil {
			t.Fatalf("WriteNew(%q) left the directory", bad)
		}
	}
	if fi, err := d.Lstat("sub/key"); err != nil || fi.Mode() != 0o600 || fi.Name() != "key" || fi.Size() != 6 {
		t.Fatalf("Lstat(sub/key) = %v, %v; want key, 6 bytes, mode 0600", fi, err)
	}
	if fi, err := d.Lstat("sub"); err != nil || fi.Mode() != fs.ModeDir|0o700 || !fi.IsDir() {
		t.Fatalf("Lstat(sub) = %v, %v; want a 0700 directory", fi, err)
	}
	if b, err := d.ReadFile("sub/key"); err != nil || string(b) != "oracle" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	r, err := d.Open("sub/key")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(r); err != nil || string(b) != "oracle" || r.Close() != nil {
		t.Fatalf("Open read %q, %v", b, err)
	}
	ents, err := d.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(ents); !slices.Equal(got, []string{"sub"}) || !ents[0].IsDir() {
		t.Fatalf("ReadDir = %q", got)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
		t.Fatalf("the real directory holds %d entries (%v); the seam wrote past the boot filesystem", len(ents), err)
	}
	want := map[string]bool{"bat.write": true, "bat.link": true, "bat.remove": true, "bat.mkdir": true, "bat.rename": true, "bat.sync": true}
	got := map[string]bool{}
	for _, ev := range rec.Events() {
		if ev.Label == bootseamtest.LabelNone {
			t.Errorf("unlabelled operation %v on %s", ev.Kind, ev.Path)
		}
		got[ev.Label] = true
	}
	for l := range want {
		if !got[l] {
			t.Errorf("no recorded operation carries %s", l)
		}
	}
}

// TestOpen_Refusals: a seam filesystem that is neither in memory nor the
// real one unwrapped is refused, since a write through it could keep
// neither os.Root's refusals nor the kit's record; an in-memory directory
// that does not exist, or is a file, and a relative path are refused.
func TestOpen_Refusals(t *testing.T) {
	wrapped := errorfs.Wrap(vfs.Default, errorfs.InjectorFunc(func(errorfs.Op) error { return nil }))
	ctx := bootseam.With(t.Context(), &bootseam.Seams{FS: wrapped})
	if _, err := statefs.Open(ctx, t.TempDir()); err == nil {
		t.Fatal("Open accepted a wrapped real filesystem")
	}
	if err := statefs.MkdirAll(ctx, "l", filepath.Join(t.TempDir(), "x"), 0o700); err == nil {
		t.Fatal("MkdirAll accepted a wrapped real filesystem")
	}
	mem := vfs.NewMem()
	mctx := bootseam.With(t.Context(), &bootseam.Seams{FS: mem})
	if _, err := statefs.Open(mctx, "/gotham"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open of an absent in-memory directory = %v, want fs.ErrNotExist", err)
	}
	f, err := mem.Create("/arkham", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := statefs.Open(mctx, "/arkham"); err == nil {
		t.Fatal("Open accepted a file as the state directory")
	}
	if _, err := statefs.Open(mctx, "relative"); err == nil {
		t.Fatal("Open accepted a relative in-memory path")
	}
	if _, err := statefs.Open(t.Context(), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("Open of an absent real directory succeeded")
	}
}

// TestMkdirAll_FollowsTheSeam: MkdirAll creates on the boot's filesystem
// under its label, and on the real disk without a seam.
func TestMkdirAll_FollowsTheSeam(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "deep")
	mem := vfs.NewMem()
	rec := bootseamtest.New(mem, bootseamtest.Options{})
	if err := statefs.MkdirAll(bootseam.With(t.Context(), rec.Seams()), "config.statedir.mkdir", dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if fi, err := mem.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("in memory: %v, %v", fi, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MkdirAll under an in-memory seam created %s on disk: %v", dir, err)
	}
	evs := rec.Events()
	if len(evs) != 1 || evs[0].Label != "config.statedir.mkdir" || evs[0].Kind != errorfs.OpMkdirAll {
		t.Fatalf("events = %+v, want one labelled mkdir", evs)
	}
	if err := statefs.MkdirAll(t.Context(), "l", dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("on disk: %v, %v", fi, err)
	}
}

// TestFromRoot_LeavesTheRootOpen: a Dir over a caller's *os.Root reads and
// writes through it, and Close leaves it to the caller; Open's Dir closes
// the root it opened.
func TestFromRoot_LeavesTheRootOpen(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	d := statefs.FromRoot(root)
	if d.Name() != dir {
		t.Fatalf("Name = %q, want %q", d.Name(), dir)
	}
	if err := d.WriteNew("l", "f", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("f"); err != nil {
		t.Fatalf("FromRoot's Close closed the caller's root: %v", err)
	}
	o, err := statefs.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Lstat("f"); err == nil {
		t.Fatal("Open's Close left its root open")
	}
}

func names(ents []fs.DirEntry) []string {
	out := make([]string, len(ents))
	for i, e := range ents {
		out[i] = e.Name()
	}
	return out
}
