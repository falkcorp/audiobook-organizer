// file: core/internal/libfs/libfs_test.go
// version: 1.0.2
// guid: 1f6e7a3c-52b8-4a6e-9d0b-3c8f4e5a6b71
// last-edited: 2026-11-11

package libfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openRoot opens a temp directory as an os.Root, closed at cleanup.
func openRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

func openDir(t *testing.T, r *os.Root, rel string) *Dir {
	t.Helper()
	d, err := OpenDir(r, rel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCheckNameRefusesAnythingButOneComponent(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "/abs", "dir/", "x\x00y", "../escape"} {
		if err := checkName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("checkName(%q) = %v, want ErrBadName", bad, err)
		}
	}
	for _, ok := range []string{"a", "01 - Chapter One.m4b", ".mediacore-tmp-1", "...", "a b"} {
		if err := checkName(ok); err != nil {
			t.Errorf("checkName(%q) = %v", ok, err)
		}
	}
}

func TestOpenDirRefusesEscapeAndNonDirectory(t *testing.T) {
	r, dir := openRoot(t)
	write(t, filepath.Join(dir, "robin.txt"), "x")
	if _, err := OpenDir(r, ".."); err == nil {
		t.Error("OpenDir(..) succeeded")
	}
	if _, err := OpenDir(r, "robin.txt"); err == nil {
		t.Error("OpenDir of a file succeeded")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDir(r, "out"); err == nil {
		t.Error("OpenDir through a symlink that leaves the root succeeded")
	}
	d := openDir(t, r, ".")
	if d.File() == nil || d.Name() == "" {
		t.Error("Dir accessors empty")
	}
	var nilDir *Dir
	if err := nilDir.Close(); err != nil {
		t.Errorf("Close on a nil Dir: %v", err)
	}
	if err := SyncDir(nil); err == nil {
		t.Error("SyncDir(nil) succeeded")
	}
}

// renameCases are the destination shapes TestRenameNoReplaceNeverClobbers
// (library) names: file, directory and dangling symlink. Each runs against the
// kernel form and the fallback.
func TestRenameNoReplaceNeverClobbers(t *testing.T) {
	forms := map[string]func(od *Dir, o string, nd *Dir, n string) error{
		"kernel":   RenameNoReplace,
		"fallback": RenameNoReplaceFallback,
	}
	for form, rename := range forms {
		t.Run(form, func(t *testing.T) {
			r, dir := openRoot(t)
			d := openDir(t, r, ".")

			// A free name renames, and the bytes move with it.
			write(t, filepath.Join(dir, "batarang.m4b"), "batarang")
			if err := rename(d, "batarang.m4b", d, "wayne.m4b"); err != nil {
				t.Fatalf("free rename: %v", err)
			}
			if read(t, filepath.Join(dir, "wayne.m4b")) != "batarang" {
				t.Error("renamed file lost its bytes")
			}
			if _, err := os.Lstat(filepath.Join(dir, "batarang.m4b")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("old name remains: %v", err)
			}

			// An existing regular file is never replaced, and neither side changes.
			write(t, filepath.Join(dir, "joker.m4b"), "joker")
			err := rename(d, "joker.m4b", d, "wayne.m4b")
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("rename onto a file: %v, want ErrExist", err)
			}
			if read(t, filepath.Join(dir, "wayne.m4b")) != "batarang" || read(t, filepath.Join(dir, "joker.m4b")) != "joker" {
				t.Error("a refused rename changed a file")
			}

			// An existing directory is refused.
			if err := os.Mkdir(filepath.Join(dir, "arkham"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := rename(d, "joker.m4b", d, "arkham"); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("rename onto a directory: %v, want ErrExist", err)
			}

			// A dangling symlink is an existing name too.
			if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
				t.Fatal(err)
			}
			if err := rename(d, "joker.m4b", d, "dangling"); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("rename onto a dangling symlink: %v, want ErrExist", err)
			}
			if got, err := os.Readlink(filepath.Join(dir, "dangling")); err != nil || got != "nowhere" {
				t.Errorf("dangling symlink changed: %q %v", got, err)
			}

			// A directory source renames to a free name, and a symlink source as the
			// link itself. The fallback cannot make either check atomic (it links and
			// unlinks a regular file): it refuses with ErrNoAtomicRename and changes
			// nothing, rather than check and then rename over a racing creator.
			if err := os.Mkdir(filepath.Join(dir, "gotham"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("wayne.m4b", filepath.Join(dir, "alias")); err != nil {
				t.Fatal(err)
			}
			if form == "fallback" {
				for _, name := range []string{"gotham", "alias"} {
					err := rename(d, name, d, name+"-moved")
					if !errors.Is(err, ErrNoAtomicRename) || !errors.Is(err, errors.ErrUnsupported) {
						t.Errorf("fallback rename of %s = %v, want ErrNoAtomicRename", name, err)
					}
					if _, lerr := os.Lstat(filepath.Join(dir, name+"-moved")); lerr == nil {
						t.Errorf("a refused fallback rename created %s-moved", name)
					}
					if _, lerr := os.Lstat(filepath.Join(dir, name)); lerr != nil {
						t.Errorf("a refused fallback rename lost %s: %v", name, lerr)
					}
				}
			} else {
				if err := rename(d, "gotham", d, "metropolis"); err != nil {
					t.Fatalf("directory rename: %v", err)
				}
				if err := rename(d, "alias", d, "alias2"); err != nil {
					t.Fatalf("symlink rename: %v", err)
				}
				if got, err := os.Readlink(filepath.Join(dir, "alias2")); err != nil || got != "wayne.m4b" {
					t.Errorf("symlink moved wrong: %q %v", got, err)
				}
			}

			// A missing source is ErrNotExist, nothing is created.
			if err := rename(d, "ghost", d, "fresh"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("missing source: %v, want ErrNotExist", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "fresh")); err == nil {
				t.Error("a refused rename created its destination")
			}
		})
	}
}

func TestRenameNoReplaceAcrossDirectories(t *testing.T) {
	r, dir := openRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a", "alfred.m4b"), "alfred")
	from, to := openDir(t, r, "a"), openDir(t, r, "a/b")
	if err := RenameNoReplace(from, "alfred.m4b", to, "alfred.m4b"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "a", "b", "alfred.m4b")) != "alfred" {
		t.Error("file did not arrive")
	}
}

func TestRenameAndLinkRefuseSeparatorsInNames(t *testing.T) {
	r, dir := openRoot(t)
	d := openDir(t, r, ".")
	write(t, filepath.Join(dir, "a"), "a")
	for _, name := range []string{"../a", "x/y", "..", ""} {
		if err := RenameNoReplace(d, "a", d, name); !errors.Is(err, ErrBadName) {
			t.Errorf("RenameNoReplace new %q: %v", name, err)
		}
		if err := RenameNoReplace(d, name, d, "b"); !errors.Is(err, ErrBadName) {
			t.Errorf("RenameNoReplace old %q: %v", name, err)
		}
		if err := Link(d, "a", d, name); !errors.Is(err, ErrBadName) {
			t.Errorf("Link new %q: %v", name, err)
		}
		if err := Reflink(d, "a", d, name, 0o644); err == nil {
			t.Errorf("Reflink new %q succeeded", name)
		}
	}
}

func TestLinkNeverClobbersAndSharesTheInode(t *testing.T) {
	r, dir := openRoot(t)
	d := openDir(t, r, ".")
	write(t, filepath.Join(dir, "src"), "bane")
	if err := Link(d, "src", d, "dst"); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(filepath.Join(dir, "src"))
	b, _ := os.Stat(filepath.Join(dir, "dst"))
	if !SameFile(a, b) {
		t.Error("a hard link is a different file")
	}
	write(t, filepath.Join(dir, "other"), "catwoman")
	err := Link(d, "src", d, "other")
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Link onto an existing name: %v, want ErrExist", err)
	}
	if read(t, filepath.Join(dir, "other")) != "catwoman" {
		t.Error("a refused Link changed the destination")
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := Link(d, "src", d, "dangling"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Link onto a dangling symlink: %v, want ErrExist", err)
	}
}

func TestReflinkRefusesAnExistingDestination(t *testing.T) {
	r, dir := openRoot(t)
	d := openDir(t, r, ".")
	write(t, filepath.Join(dir, "src"), "scarecrow")
	write(t, filepath.Join(dir, "dst"), "riddler")
	err := Reflink(d, "src", d, "dst", LibraryFileMode)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Reflink onto an existing file: %v, want ErrExist", err)
	}
	if errors.Is(err, ErrReflinkUnsupported) {
		t.Error("an existing destination reported as unsupported: a caller would fall back and clobber")
	}
	if read(t, filepath.Join(dir, "dst")) != "riddler" {
		t.Error("Reflink truncated an existing destination")
	}
}

func TestReflinkEitherClonesOrLeavesNothing(t *testing.T) {
	r, dir := openRoot(t)
	d := openDir(t, r, ".")
	write(t, filepath.Join(dir, "src"), strings.Repeat("penguin", 1000))
	err := Reflink(d, "src", d, "clone", LibraryFileMode)
	switch {
	case err == nil:
		if read(t, filepath.Join(dir, "clone")) != read(t, filepath.Join(dir, "src")) {
			t.Error("clone differs from its source")
		}
	case errors.Is(err, ErrReflinkUnsupported):
		if _, serr := os.Lstat(filepath.Join(dir, "clone")); !errors.Is(serr, fs.ErrNotExist) {
			t.Errorf("an unsupported reflink left its destination behind: %v", serr)
		}
	default:
		t.Fatalf("Reflink: %v", err)
	}
	if err := Reflink(d, "missing", d, "x", LibraryFileMode); err == nil {
		t.Error("Reflink of a missing source succeeded")
	}
}

func TestSyncDirSucceedsOnADirectory(t *testing.T) {
	r, _ := openRoot(t)
	if err := SyncDir(openDir(t, r, ".")); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceAndInodeOf(t *testing.T) {
	r, dir := openRoot(t)
	write(t, filepath.Join(dir, "f"), "x")
	fi, _ := os.Lstat(filepath.Join(dir, "f"))
	di, _ := os.Lstat(dir)
	dev1, ino1, ok1 := InodeOf(fi)
	dev2, ino2, ok2 := InodeOf(di)
	if ok1 != ok2 {
		t.Fatalf("InodeOf availability differs: %v %v", ok1, ok2)
	}
	if ok1 && dev1 != dev2 {
		t.Errorf("a file and its directory report different devices: %d %d", dev1, dev2)
	}
	if ok1 && ino1 == ino2 {
		t.Error("a file and its directory share an inode")
	}
	_ = r
}

func TestCopyHashesWhatItReads(t *testing.T) {
	data := bytes.Repeat([]byte("two-face"), 300_000) // 2.4 MB: three chunks
	h := sha256.New()
	var dst bytes.Buffer
	n, err := Copy(&dst, bytes.NewReader(data), CopyOptions{Hash: h})
	if err != nil || n != int64(len(data)) {
		t.Fatalf("Copy = %d, %v", n, err)
	}
	want := sha256.Sum256(data)
	if hex.EncodeToString(h.Sum(nil)) != hex.EncodeToString(want[:]) {
		t.Error("digest does not describe the bytes read")
	}
	if !bytes.Equal(dst.Bytes(), data) {
		t.Error("destination differs from the source")
	}
}

func TestCopyStopsAtAChunkBoundaryWhenChecked(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 3*ChunkSize)
	stop := errors.New("stopped")
	calls := 0
	var dst bytes.Buffer
	n, err := Copy(&dst, bytes.NewReader(data), CopyOptions{Check: func() error {
		calls++
		if calls == 2 {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatalf("Copy error = %v, want the check's", err)
	}
	if n != ChunkSize {
		t.Errorf("copied %d bytes before the check fired, want one chunk (%d)", n, ChunkSize)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestCopyReportsShortWritesReadErrorsAndThrottle(t *testing.T) {
	if _, err := Copy(shortWriter{}, strings.NewReader("harley"), CopyOptions{}); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short write: %v", err)
	}
	if _, err := Copy(io.Discard, errReader{}, CopyOptions{}); err == nil {
		t.Error("a read error was swallowed")
	}
	throttled := 0
	if _, err := Copy(io.Discard, strings.NewReader("quinn"), CopyOptions{Throttle: func(n int) error { throttled += n; return nil }}); err != nil || throttled != 5 {
		t.Errorf("throttle saw %d bytes, err %v", throttled, err)
	}
	stop := errors.New("slow down")
	if _, err := Copy(io.Discard, strings.NewReader("quinn"), CopyOptions{Throttle: func(int) error { return stop }}); !errors.Is(err, stop) {
		t.Errorf("throttle error not returned: %v", err)
	}
}
