// file: internal/backup/restore_physical_path_test.go
// version: 1.0.1
// guid: 1836ed6a-6e53-4474-bdb6-bfc2a75fc46f
// last-edited: 2026-11-09

package backup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// R1-15 round-8 verification review, B1-r8: landsIn walked the path an entry
// names, one component at a time, and compared each directory on the way
// with the snapshot directories. A symlink on that path to a directory
// strictly under a snapshot directory is followed by the kernel, but the
// walk never reached the snapshot directory itself: it stated the symlink,
// which has the subdirectory's identity, then the entry's own name, which
// did not exist yet, and let the entry through. It now walks the physical
// path: the deepest prefix that exists, resolved with filepath.EvalSymlinks,
// with the names not made yet appended. (CheckPathsApart, the restore
// handler's target check, had the same blind spot and the same fix; it went
// with the in-place restore in R1-25.)

// TestExtractArchive_RefusesASymlinkIntoASnapshotSubdirectory is the
// reviewer's probe and its variants: a symlink in the database directory
// to a directory under the snapshot directory, relative (it resolves from
// its own physical parent) or absolute, reached by an entry directly or
// through directories not made yet, or through a chain of symlinks. Each is
// refused before anything is created for it.
func TestExtractArchive_RefusesASymlinkIntoASnapshotSubdirectory(t *testing.T) {
	for name, tc := range map[string]struct {
		link  func(root string) string // the symlink's target
		chain bool                     // reach it through a second symlink
		entry string
	}{
		"relative, the entry directly under it": {
			link:  func(string) string { return filepath.Join("..", "hnsw", "robin") },
			entry: "audiobooks.pebble/nightwing/hnsw.clean-stop",
		},
		"absolute, the entry directly under it": {
			link:  func(root string) string { return filepath.Join(root, "hnsw", "robin") },
			entry: "audiobooks.pebble/nightwing/hnsw.clean-stop",
		},
		"relative, through directories not made yet": {
			link:  func(string) string { return filepath.Join("..", "hnsw", "robin") },
			entry: "audiobooks.pebble/nightwing/red-hood/batgirl/hnsw.clean-stop",
		},
		"through a chain of symlinks": {
			link:  func(string) string { return filepath.Join("..", "hnsw", "robin") },
			chain: true,
			entry: "audiobooks.pebble/oracle/hnsw.clean-stop",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			snap := filepath.Join(root, "hnsw")
			db := filepath.Join(root, "audiobooks.pebble")
			mustMkdir(t, filepath.Join(snap, "robin"), db)
			mustSymlink(t, tc.link(root), filepath.Join(db, "nightwing"))
			if tc.chain {
				mustSymlink(t, "nightwing", filepath.Join(db, "oracle"))
			}
			archive := filepath.Join(t.TempDir(), "hush.tar")
			writePlainTar(t, archive, [][2]string{{tc.entry, `{"version":1,"model":"bge-m3"}`}})

			if err := extractArchive(archive, root, []string{snap}); !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
				t.Fatalf("extractArchive = %v; want ErrArchiveCarriesHNSWSnapshot", err)
			}
			ents, err := os.ReadDir(filepath.Join(snap, "robin"))
			if err != nil || len(ents) != 0 {
				t.Errorf("the restore wrote inside the snapshot directory (%d entries, %v)", len(ents), err)
			}
		})
	}
}

// TestExtractArchive_RefusesAnUnresolvableSymlinkOnTheWay: a symlink on an
// entry's path that does not resolve, dangling or looping, cannot be
// compared with the snapshot directories, so the entry is refused with
// ErrUnresolvableSymlink, the 409 the handler gives a snapshot directory it
// cannot compare, and the dangling link's target is not made.
func TestExtractArchive_RefusesAnUnresolvableSymlinkOnTheWay(t *testing.T) {
	for name, link := range map[string]string{
		"dangling": filepath.Join("..", "hnsw", "robin"),
		"looping":  "two-face",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			snap := filepath.Join(root, "hnsw")
			db := filepath.Join(root, "audiobooks.pebble")
			mustMkdir(t, snap, db)
			mustSymlink(t, link, filepath.Join(db, "two-face"))
			archive := filepath.Join(t.TempDir(), "hush.tar")
			writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/two-face/harvey/MANIFEST-000001", "batcave"}})

			if err := extractArchive(archive, root, []string{snap}); !errors.Is(err, ErrUnresolvableSymlink) {
				t.Fatalf("extractArchive = %v; want ErrUnresolvableSymlink", err)
			}
			if _, err := os.Lstat(filepath.Join(snap, "robin")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the dangling link's target was made (lstat: %v)", err)
			}
		})
	}
}

// TestExtractArchive_FollowsASymlinkElsewhere: a symlink in the database
// directory to a directory outside the snapshot directories, an archive
// directory moved to another disk say, is followed, as before.
func TestExtractArchive_FollowsASymlinkElsewhere(t *testing.T) {
	root := t.TempDir()
	snap := filepath.Join(root, "hnsw")
	db := filepath.Join(root, "audiobooks.pebble")
	cold := filepath.Join(t.TempDir(), "cold-storage")
	mustMkdir(t, snap, db, cold)
	mustSymlink(t, cold, filepath.Join(db, "archive"))
	archive := filepath.Join(t.TempDir(), "hush.tar")
	writePlainTar(t, archive, [][2]string{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"audiobooks.pebble/archive/000001.sst", "arkham"},
		{"audiobooks.pebble/archive/wal/000002.log", "blackgate"},
	})
	if err := extractArchive(archive, root, []string{snap}); err != nil {
		t.Fatalf("extractArchive = %v", err)
	}
	for p, want := range map[string]string{
		filepath.Join(cold, "000001.sst"):        "arkham",
		filepath.Join(cold, "wal", "000002.log"): "blackgate",
	} {
		if got, err := os.ReadFile(p); err != nil || string(got) != want {
			t.Errorf("%s reads %q (%v); want %q", p, got, err, want)
		}
	}
}

// TestExtractArchive_RefusesAnEntryUnderADirectoryItCannotLookIn: a
// directory on an entry's path that the process cannot search (mode 0)
// hides what is below it, a symlink into a snapshot directory included, so
// the entry cannot be compared with the snapshot directories. It is refused
// with ErrUnresolvableSymlink, as a symlink that does not resolve is, and
// not written. physicalPath's Lstat and landsIn's walk each refuse it, so
// either check alone holds (R1-15 round-8 mutation run).
func TestExtractArchive_RefusesAnEntryUnderADirectoryItCannotLookIn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches a mode-0 directory")
	}
	root := t.TempDir()
	snap := filepath.Join(root, "hnsw")
	locked := filepath.Join(root, "audiobooks.pebble", "arkham")
	mustMkdir(t, snap, locked)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	archive := filepath.Join(t.TempDir(), "hush.tar")
	writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/arkham/joker/000001.sst", "arkham"}})

	if err := extractArchive(archive, root, []string{snap}); !errors.Is(err, ErrUnresolvableSymlink) {
		t.Fatalf("extractArchive = %v; want ErrUnresolvableSymlink", err)
	}
}
