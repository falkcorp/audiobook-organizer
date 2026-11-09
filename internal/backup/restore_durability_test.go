// file: internal/backup/restore_durability_test.go
// version: 1.0.0
// guid: d1096f1c-7cfc-4fb7-8cfd-6a0f7085706c
// last-edited: 2026-11-08

package backup

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeDurabilityTar writes an archive with a file in the database
// directory, one in a directory under it, and a directory entry two levels
// down, so the extraction makes directories as well as renaming files.
func writeDurabilityTar(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, name := range []string{"audiobooks.pebble/MANIFEST-000001", "audiobooks.pebble/archive/000005.sst"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 7, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("batcave")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/a/b/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return 3
}

// recordSyncs replaces syncDir for the test. Each call records its
// directory and how many entries the extraction had finished by then
// (afterEntryHook), and fails with errFor's error for that directory.
func recordSyncs(t *testing.T, errFor func(dir string) error) (synced *[]string, entriesAt *[]int) {
	t.Helper()
	var dirs []string
	var at []int
	done := 0
	afterEntryHook = func(string) { done++ }
	prev := syncDir
	syncDir = func(dir string) error {
		dirs = append(dirs, dir)
		at = append(at, done)
		if errFor != nil {
			return errFor(dir)
		}
		return nil
	}
	t.Cleanup(func() { syncDir, afterEntryHook = prev, nil })
	return &dirs, &at
}

// TestExtractArchive_SyncsEachChangedDirectory: a rename is durable only
// once the directory holding the new name is synced, and a directory the
// extraction made is durable only once its parent is. So the extraction
// syncs, after its last entry and before it reports success, each
// directory an entry was renamed into or made in, and each of their
// ancestors up to the nearest directory that existed before it began:
// every directory whose entries it changed, and none other.
func TestExtractArchive_SyncsEachChangedDirectory(t *testing.T) {
	for _, tc := range []struct {
		name         string
		targetExists bool
	}{
		{"a target the restore makes", false},
		{"a target that exists", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "gotham")
			if tc.targetExists {
				mustMkdir(t, target)
			}
			archive := filepath.Join(base, "the-dark-knight-returns.tar")
			entries := writeDurabilityTar(t, archive)
			synced, entriesAt := recordSyncs(t, nil)

			if err := extractArchive(archive, target, nil); err != nil {
				t.Fatalf("extractArchive = %v", err)
			}

			db := filepath.Join(target, "audiobooks.pebble")
			want := []string{filepath.Join(db, "archive"), filepath.Join(db, "a"), db, target}
			if !tc.targetExists {
				want = append(want, base)
			}
			got := slices.Clone(*synced)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("synced %q; want each directory the extraction changed, once:\n%q", got, want)
			}
			for i, n := range *entriesAt {
				if n != entries {
					t.Errorf("%s was synced after %d of %d entries; want after the last", (*synced)[i], n, entries)
				}
			}
		})
	}
}

// TestExtractArchive_FailsWhenADirectoryDoesNotSync: a restore whose renames
// may not survive a crash has not succeeded, so a directory that does not
// sync fails the extraction.
func TestExtractArchive_FailsWhenADirectoryDoesNotSync(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "gotham")
	archive := filepath.Join(base, "a-death-in-the-family.tar")
	writeDurabilityTar(t, archive)
	db := filepath.Join(target, "audiobooks.pebble")
	errDisk := errors.New("the batcomputer's disk is full")
	recordSyncs(t, func(dir string) error {
		if dir == db {
			return errDisk
		}
		return nil
	})

	err := extractArchive(archive, target, nil)
	if !errors.Is(err, errDisk) || !strings.Contains(err.Error(), db) {
		t.Fatalf("extractArchive = %v; want the sync error, naming %s", err, db)
	}
}

// TestSyncDir_ReportsADirectoryItCannotOpen: syncDir is a real fsync off
// Windows, so a directory that is not there is an error, not a silent
// success.
func TestSyncDir_ReportsADirectoryItCannotOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syncDir does nothing on Windows")
	}
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir on a directory = %v; want nil", err)
	}
	if err := syncDir(filepath.Join(t.TempDir(), "arkham")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("syncDir on a missing directory = %v; want os.ErrNotExist", err)
	}
}
