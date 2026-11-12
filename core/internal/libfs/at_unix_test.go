// file: core/internal/libfs/at_unix_test.go
// version: 1.4.0
// guid: f659f83a-171b-4adc-996b-b61e175f5807
// last-edited: 2026-11-12

//go:build unix

package libfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The *At operations address one name relative to the directory descriptor. A
// symlink swapped into that name is not followed, so what they create, open or
// remove is in the directory the descriptor is, whatever the names became.
func TestOpenFileAtCreatesExclusivelyAndNeverFollowsALink(t *testing.T) {
	root, dir := openRoot(t)
	d := openDir(t, root, ".")
	f, err := OpenFileAt(d, "a.m4b", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("the bytes"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "a.m4b")); got != "the bytes" {
		t.Errorf("file holds %q", got)
	}
	if _, err := OpenFileAt(d, "a.m4b", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a second exclusive create = %v, want ErrExist", err)
	}
	// A link in the name is refused, for a write as for a create.
	if err := os.Symlink("a.m4b", filepath.Join(dir, "link.m4b")); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []int{os.O_WRONLY, os.O_RDWR | os.O_CREATE} {
		if f, err := OpenFileAt(d, "link.m4b", flag, 0o640); !errors.Is(err, syscall.ELOOP) {
			if err == nil {
				_ = f.Close()
			}
			t.Errorf("open of a symlink with flag %#x = %v, want ELOOP", flag, err)
		}
	}
	if got := read(t, filepath.Join(dir, "a.m4b")); got != "the bytes" {
		t.Errorf("the target changed through the link: %q", got)
	}
}

func TestMkdirAtRemoveAtSymlinkAtIdentityAt(t *testing.T) {
	root, dir := openRoot(t)
	d := openDir(t, root, ".")
	if err := MkdirAt(d, "Gotham", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAt(d, "Gotham", 0o755); !errors.Is(err, fs.ErrExist) {
		t.Errorf("MkdirAt of an existing name = %v, want ErrExist", err)
	}
	id, isDir, err := IdentityAt(d, "Gotham")
	st, serr := os.Stat(filepath.Join(dir, "Gotham"))
	if err != nil || serr != nil || !isDir {
		t.Fatalf("IdentityAt = %v, %v, %v (%v)", id, isDir, err, serr)
	}
	if dev, ino, ok := InodeOf(st); !ok || (ID{Dev: dev, Ino: ino}) != id {
		t.Errorf("IdentityAt %v is not the directory's inode %v %v", id, dev, ino)
	}
	if err := SymlinkAt("Gotham", d, "Arkham"); err != nil {
		t.Fatal(err)
	}
	if err := SymlinkAt("Gotham", d, "Arkham"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("SymlinkAt of an existing name = %v, want ErrExist", err)
	}
	// IdentityAt does not follow: the link has its own inode and is no directory.
	lid, lisDir, err := IdentityAt(d, "Arkham")
	if err != nil || lisDir || lid == id {
		t.Errorf("IdentityAt of a link = %v, %v, %v", lid, lisDir, err)
	}
	if _, _, err := IdentityAt(d, "nowhere"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("IdentityAt of a missing name = %v, want ErrNotExist", err)
	}
	write(t, filepath.Join(dir, "Gotham", "x"), "x")
	if err := RemoveAt(d, "Gotham"); err == nil {
		t.Error("RemoveAt of a non-empty directory succeeded")
	}
	if err := RemoveAt(d, "Arkham"); err != nil {
		t.Errorf("RemoveAt of a link = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Gotham", "x")); err != nil {
		t.Errorf("RemoveAt of a link removed the target's content: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "Gotham", "x")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAt(d, "Gotham"); err != nil {
		t.Errorf("RemoveAt of an empty directory = %v", err)
	}
	write(t, filepath.Join(dir, "f"), "f")
	if err := RemoveAt(d, "f"); err != nil {
		t.Errorf("RemoveAt of a file = %v", err)
	}
	if err := RemoveAt(d, "f"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveAt of a missing name = %v, want ErrNotExist", err)
	}
	for _, bad := range []string{"", "a/b", ".."} {
		if _, err := OpenFileAt(d, bad, os.O_RDONLY, 0); err == nil {
			t.Errorf("OpenFileAt accepted %q", bad)
		}
		if err := MkdirAt(d, bad, 0o755); err == nil {
			t.Errorf("MkdirAt accepted %q", bad)
		}
	}
}

// RenameOver is the one rename that replaces: the caller made the placeholder it
// lands on. It carries a file over a file and a directory over an empty one.
func TestRenameOverReplacesOnlyWhatItLandsOn(t *testing.T) {
	r, dir := openRoot(t)
	d := openDir(t, r, ".")
	write(t, filepath.Join(dir, "tmp"), "bane")
	write(t, filepath.Join(dir, "placeholder"), "")
	if err := RenameOver(d, "tmp", d, "placeholder"); err != nil {
		t.Fatalf("file over a placeholder: %v", err)
	}
	if read(t, filepath.Join(dir, "placeholder")) != "bane" {
		t.Error("the bytes did not arrive")
	}
	if _, err := os.Lstat(filepath.Join(dir, "tmp")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the old name remains: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "full"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "reserved"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "full", "a.m4b"), "a")
	if err := RenameOver(d, "full", d, "reserved"); err != nil {
		t.Fatalf("directory over an empty directory: %v", err)
	}
	if read(t, filepath.Join(dir, "reserved", "a.m4b")) != "a" {
		t.Error("the directory's content did not arrive")
	}
	for _, name := range []string{"../a", "x/y", "..", ""} {
		if err := RenameOver(d, "placeholder", d, name); !errors.Is(err, ErrBadName) {
			t.Errorf("RenameOver new %q: %v", name, err)
		}
	}
}

// The errnos that mean "this filesystem has no hard links" are classified, so a
// caller can publish by rename; a link that fails for any other reason is not.
func TestNoHardLinksClassification(t *testing.T) {
	for _, e := range []unix.Errno{unix.EPERM, unix.ENOTSUP, unix.EOPNOTSUPP, unix.ENOSYS, unix.EMLINK} {
		if !noHardLinks(e) {
			t.Errorf("%v is not classified as no hard links", e)
		}
	}
	for _, e := range []unix.Errno{unix.EEXIST, unix.EXDEV, unix.EACCES, unix.ENOENT, unix.ENOSPC} {
		if noHardLinks(e) {
			t.Errorf("%v is classified as no hard links", e)
		}
	}
	if !errors.Is(ErrNoHardLinks, errors.ErrUnsupported) || !errors.Is(ErrNoAtomicRename, errors.ErrUnsupported) {
		t.Error("the filesystem-support sentinels must be errors.ErrUnsupported")
	}
}

// On a real filesystem without hard links (exFAT, FAT, an SMB share) a link is
// ErrNoHardLinks, a no-replace rename is either atomic or ErrNoAtomicRename (never
// a check-then-rename), and an existing name is still refused. Skipped unless
// MEDIACORE_TEST_NOLINK_DIR names a directory on such a volume.
func TestOnARealFilesystemWithoutHardLinks(t *testing.T) {
	base := os.Getenv("MEDIACORE_TEST_NOLINK_DIR")
	if base == "" {
		t.Skip("MEDIACORE_TEST_NOLINK_DIR is not set")
	}
	dir, err := os.MkdirTemp(base, "libfs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	d := openDir(t, root, ".")
	write(t, filepath.Join(dir, "a"), "a")
	if err := os.Link(filepath.Join(dir, "a"), filepath.Join(dir, "probe")); err == nil {
		t.Skip("this filesystem has hard links")
	}
	if err := Link(d, "a", d, "b"); !errors.Is(err, ErrNoHardLinks) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Link = %v, want ErrNoHardLinks", err)
	}
	if err := RenameNoReplace(d, "a", d, "c"); err != nil && !errors.Is(err, ErrNoAtomicRename) {
		t.Errorf("RenameNoReplace of a file = %v, want success or ErrNoAtomicRename", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(d, "dir", d, "dir2"); err != nil && !errors.Is(err, ErrNoAtomicRename) {
		t.Errorf("RenameNoReplace of a directory = %v, want success or ErrNoAtomicRename", err)
	}
	write(t, filepath.Join(dir, "x"), "x")
	write(t, filepath.Join(dir, "y"), "y")
	if err := RenameNoReplace(d, "x", d, "y"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("RenameNoReplace onto an existing name = %v, want ErrExist", err)
	}
	if read(t, filepath.Join(dir, "y")) != "y" {
		t.Error("an existing file was replaced")
	}
}

// The errno of a failed link decides what a caller does next, so each class is
// pinned without needing a filesystem that produces it: a missing hard link
// facility is classified (and the errno stays in the chain), a device boundary
// and an existing name are not, and the fallback rename answers
// ErrNoAtomicRename only where no atomic way to refuse an existing name is left.
func TestLinkFailureClassification(t *testing.T) {
	noLinks := []unix.Errno{unix.EPERM, unix.ENOTSUP, unix.EOPNOTSUPP, unix.ENOSYS, unix.EMLINK}
	for _, e := range noLinks {
		err := linkFailure("a", "b", e)
		if !errors.Is(err, ErrNoHardLinks) || !errors.Is(err, errors.ErrUnsupported) || !errors.Is(err, e) {
			t.Errorf("link failure %v = %v, want ErrNoHardLinks wrapping the errno", e, err)
		}
		if errors.Is(err, ErrNoAtomicRename) || errors.Is(err, ErrCrossDevice) {
			t.Errorf("link failure %v is mistaken for another class: %v", e, err)
		}
		ferr := fallbackLinkFailure("a", "b", e)
		if !errors.Is(ferr, ErrNoAtomicRename) || !errors.Is(ferr, errors.ErrUnsupported) || !errors.Is(ferr, e) {
			t.Errorf("fallback link failure %v = %v, want ErrNoAtomicRename wrapping the errno", e, ferr)
		}
		if errors.Is(ferr, ErrNoHardLinks) {
			t.Errorf("the fallback's failure %v is reported as a link failure: %v", e, ferr)
		}
	}
	for name, c := range map[string]struct {
		errno unix.Errno
		is    error
	}{
		"an existing name":  {unix.EEXIST, fs.ErrExist},
		"a device boundary": {unix.EXDEV, ErrCrossDevice},
		"a missing source":  {unix.ENOENT, fs.ErrNotExist},
		"no permission":     {unix.EACCES, fs.ErrPermission},
		"no space":          {unix.ENOSPC, unix.ENOSPC},
	} {
		for form, err := range map[string]error{
			"link":     linkFailure("a", "b", c.errno),
			"fallback": fallbackLinkFailure("a", "b", c.errno),
		} {
			if !errors.Is(err, c.is) {
				t.Errorf("%s of %s = %v, want %v in the chain", form, name, err, c.is)
			}
			if errors.Is(err, ErrNoHardLinks) || errors.Is(err, ErrNoAtomicRename) {
				t.Errorf("%s of %s is classified as missing hard links: %v", form, name, err)
			}
		}
	}
	if err := linkFailure("a", "b", nil); err != nil {
		t.Errorf("linkFailure(nil) = %v", err)
	}
}

// openat refuses a name that is not local to the directory before it reaches the
// system call, whatever checkName at the public entry already said.
func TestOpenatRefusesANameThatIsNotLocal(t *testing.T) {
	for _, name := range []string{"", "..", "../Batcave", "/etc/passwd"} {
		if nfd, err := openat(-1, name, unix.O_RDONLY, 0); !errors.Is(err, ErrBadName) || nfd != -1 {
			t.Errorf("openat(%q) = %d, %v; want -1 and ErrBadName", name, nfd, err)
		}
	}
}
