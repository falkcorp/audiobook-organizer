// file: internal/books/store/hnsw_marker_durability_test.go
// version: 1.0.2
// guid: 4e5bb1cc-11e8-4ef8-b8b5-1f68f38a572f
// last-edited: 2026-11-09

package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// errSimulatedSync is the failure the tests' directory-sync hook returns.
var errSimulatedSync = errors.New("simulated: the disk refuses the directory sync")

// recordDirSyncs installs a directory-sync hook for the test that records
// each directory fsyncDir is given and fails the ones fail names.
func recordDirSyncs(t *testing.T, fail func(dir string) error) *[]string {
	t.Helper()
	var dirs []string
	t.Cleanup(SetDirSyncHookForTest(func(dir string) error {
		dirs = append(dirs, dir)
		if fail != nil {
			return fail(dir)
		}
		return nil
	}))
	return &dirs
}

// recordFileSyncs installs a file-sync hook for the test that records the
// name of each file syncFile is given.
func recordFileSyncs(t *testing.T) *[]string {
	t.Helper()
	var names []string
	h := func(path string) error {
		names = append(names, path)
		return nil
	}
	prev := fileSyncHook.Swap(&h)
	t.Cleanup(func() { fileSyncHook.Store(prev) })
	return &names
}

// TestTakeCleanStop_SyncsTheDirectoryAfterTheRemoval (R1-15 round-8
// verification review, SF1): the removal is durable only once the directory
// is synced, and nothing saw whether it was.
func TestTakeCleanStop_SyncsTheDirectoryAfterTheRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hnsw")
	writeMarker(t, dir)
	synced := recordDirSyncs(t, nil)
	rec, err := takeCleanStop(dir)
	if err != nil || rec == nil {
		t.Fatalf("takeCleanStop = %+v, %v; want the marker, vouching", rec, err)
	}
	if !slices.Contains(*synced, dir) {
		t.Errorf("the directory was not synced after the marker's removal (synced: %q)", *synced)
	}
	if markerExists(t, dir) {
		t.Error("the marker is still on disk")
	}

	t.Run("a sync that fails leaves the marker possibly vouching", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "hnsw")
		writeMarker(t, dir)
		recordDirSyncs(t, func(string) error { return errSimulatedSync })
		if _, err := takeCleanStop(dir); !errors.Is(err, ErrCleanStopNotDiscarded) {
			t.Errorf("takeCleanStop = %v; want ErrCleanStopNotDiscarded", err)
		}
	})
	t.Run("a removal that works on the retry", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "hnsw")
		writeMarker(t, dir)
		calls := 0
		markerRemove = func(path string) error {
			calls++
			if calls == 1 {
				return errors.New("simulated transient failure")
			}
			return os.Remove(path)
		}
		t.Cleanup(func() { markerRemove = os.Remove })
		synced := recordDirSyncs(t, nil)
		if _, err := takeCleanStop(dir); err != nil {
			t.Fatalf("takeCleanStop = %v", err)
		}
		if calls != 2 {
			t.Fatalf("removal attempts = %d, want 2", calls)
		}
		if !slices.Contains(*synced, dir) {
			t.Errorf("the directory was not synced after the retried removal (synced: %q)", *synced)
		}
	})
}

// TestTakeCleanStop_SyncsTheDirectoryWhenNoMarkerIsThere (R1-15 round-8
// verification review, SF2): a marker another process removed, without a
// directory sync yet, is absent by Lstat and can come back after a crash.
// A take that finds no marker syncs the directory too, so the absence it
// reports is durable before the caller writes anything that relies on it.
// A directory that does not exist (a fresh custom restore target's) has
// nothing to sync and is not an error; any other sync failure means a
// marker may come back, and says so.
func TestTakeCleanStop_SyncsTheDirectoryWhenNoMarkerIsThere(t *testing.T) {
	t.Run("the directory exists", func(t *testing.T) {
		dir := t.TempDir()
		synced := recordDirSyncs(t, nil)
		if rec, err := takeCleanStop(dir); rec != nil || err != nil {
			t.Fatalf("takeCleanStop = %+v, %v; want nil, nil", rec, err)
		}
		if !slices.Contains(*synced, dir) {
			t.Errorf("a take that found no marker did not sync the directory (synced: %q)", *synced)
		}
	})
	t.Run("the directory does not exist", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "hnsw")
		if rec, err := takeCleanStop(dir); rec != nil || err != nil {
			t.Errorf("takeCleanStop = %+v, %v; want nil, nil", rec, err)
		}
	})
	t.Run("the sync fails", func(t *testing.T) {
		dir := t.TempDir()
		recordDirSyncs(t, func(string) error { return errSimulatedSync })
		_, err := takeCleanStop(dir)
		if !errors.Is(err, ErrCleanStopNotDiscarded) {
			t.Fatalf("takeCleanStop = %v; want ErrCleanStopNotDiscarded", err)
		}
		// The remedy, as the discard variant gives one (R1-25 follow-up N-4).
		for _, want := range []string{"could not be synced", "the user the service runs as", "run again"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("takeCleanStop = %v; want it to say %q", err, want)
			}
		}
	})
	t.Run("the directory goes between the look and the sync", func(t *testing.T) {
		dir := t.TempDir()
		recordDirSyncs(t, func(d string) error { return fmt.Errorf("open %s for fsync: %w", d, fs.ErrNotExist) })
		if rec, err := takeCleanStop(dir); rec != nil || err != nil {
			t.Errorf("takeCleanStop = %+v, %v; want nil, nil", rec, err)
		}
	})
}

// TestWriteFileSynced_DoesNotWriteThroughAHardLink (R1-15 round-8
// verification review, N3): a regular file at the name passes the
// regular-file check whatever else links to it, so writing into it wrote
// the other names' inode too: a hard link at an export's temporary name to
// a Pebble file was overwritten with the graph. writeFileSynced removes the
// name first and creates a new file, so the other names keep their bytes.
func TestWriteFileSynced_DoesNotWriteThroughAHardLink(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "000001.sst")
	if err := os.WriteFile(outside, []byte("arkham-sst-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(base, "hnsw-book.bin.tmp")
	if err := os.Link(outside, tmp); err != nil {
		t.Skipf("the filesystem does not make hard links: %v", err)
	}
	if err := writeFileSynced(tmp, []byte("graph")); err != nil {
		t.Fatalf("writeFileSynced = %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "arkham-sst-bytes" {
		t.Errorf("the other name reads %q (%v); the write went through the hard link", got, err)
	}
	if got, err := os.ReadFile(tmp); err != nil || string(got) != "graph" {
		t.Errorf("the name reads %q (%v); want the new bytes", got, err)
	}
}

// TestWriteFileSynced_RefusesADirectory: removing the name first never
// removes a directory there, empty or not.
func TestWriteFileSynced_RefusesADirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hnsw-book.bin.tmp")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileSynced(p, []byte("graph")); !errors.Is(err, errNotRegularFile) {
		t.Errorf("writeFileSynced = %v; want errNotRegularFile", err)
	}
	if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
		t.Errorf("the directory is gone (lstat: %v)", err)
	}
}

// TestMarkerFiles_AreSyncedBeforeTheyCount (R1-15 round-8 mutation run): a
// marker emptied because its name cannot be removed vouches for nothing
// after a crash only once the emptying is on disk, and a file
// writeFileSynced wrote, a marker or a snapshot file, is whole only once its
// bytes are. Neither sync had a test: removing either left every test
// green.
func TestMarkerFiles_AreSyncedBeforeTheyCount(t *testing.T) {
	t.Run("a marker emptied in place", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "hnsw")
		writeMarker(t, dir)
		markerRemove = func(string) error { return errors.New("simulated: the directory refuses the unlink") }
		t.Cleanup(func() { markerRemove = os.Remove })
		synced := recordFileSyncs(t)
		if _, err := takeCleanStop(dir); err != nil {
			t.Fatalf("takeCleanStop = %v", err)
		}
		if !slices.Contains(*synced, cleanStopPath(dir)) {
			t.Errorf("the emptied marker was not synced (synced: %q)", *synced)
		}
	})
	t.Run("a file writeFileSynced wrote", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "hnsw-book.bin.tmp")
		synced := recordFileSyncs(t)
		if err := writeFileSynced(p, []byte("graph")); err != nil {
			t.Fatalf("writeFileSynced = %v", err)
		}
		if !slices.Contains(*synced, p) {
			t.Errorf("the file was not synced (synced: %q)", *synced)
		}
	})
}
