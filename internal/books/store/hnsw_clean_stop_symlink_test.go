// file: internal/books/store/hnsw_clean_stop_symlink_test.go
// version: 1.0.2
// guid: 0fb79c76-6605-4679-9533-bd55a17f694d
// last-edited: 2026-11-09

package store

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
)

// R1-15 round-7 verification review, B1-r7: a symlink at the marker name.
// The marker name is the one path in the snapshot directory a restore can
// still reach without writing in that directory: a symlink there that
// dangles (hnsw/hnsw.clean-stop -> ../audiobooks.pebble/OPTIONS-000099)
// points into the database directory, where an ordinary archive entry may
// write. The take used os.Stat, saw the dangling link as no marker and left
// it; the entry then filled its target with a marker body, and a crash
// before the backstop left a marker the next boot's os.ReadFile followed and
// trusted. A symlink at the marker name is now discarded like any marker,
// never vouches, and is never written through.

const vouchingBody = `{"version":1,"model":"bge-m3","at":"2026-11-08T00:00:00Z"}`

// linkedMarker plants dir/hnsw.clean-stop as a relative symlink to
// ../audiobooks.pebble/<name> and returns the link's target.
func linkedMarker(t *testing.T, base, name string) (dir, target string) {
	t.Helper()
	dir = filepath.Join(base, "hnsw")
	db := filepath.Join(base, "audiobooks.pebble")
	for _, d := range []string{dir, db} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "audiobooks.pebble", name), cleanStopPath(dir)); err != nil {
		t.Skipf("the filesystem does not make symlinks: %v", err)
	}
	return dir, filepath.Join(db, name)
}

// nameGone fails the test unless nothing, not even a symlink, is at p.
func nameGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s is still there (Lstat: %v); want the name removed", p, err)
	}
}

func TestTakeHNSWCleanStop_DiscardsASymlinkAtTheMarkerName(t *testing.T) {
	t.Run("dangling", func(t *testing.T) {
		dir, target := linkedMarker(t, t.TempDir(), "OPTIONS-000099")
		rec, err := takeCleanStop(dir)
		if rec != nil {
			t.Errorf("takeCleanStop vouches for a dangling symlink: %+v", *rec)
		}
		if errors.Is(err, ErrCleanStopNotDiscarded) {
			t.Errorf("takeCleanStop = %v; a removed symlink does not stay", err)
		}
		nameGone(t, cleanStopPath(dir))
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the link's target was made (Lstat: %v)", err)
		}
	})
	t.Run("to a file holding a marker body", func(t *testing.T) {
		dir, target := linkedMarker(t, t.TempDir(), "OPTIONS-000099")
		if err := os.WriteFile(target, []byte(vouchingBody), 0o644); err != nil {
			t.Fatal(err)
		}
		rec, _ := takeCleanStop(dir)
		if rec != nil {
			t.Errorf("takeCleanStop vouches through a symlink: %+v", *rec)
		}
		nameGone(t, cleanStopPath(dir))
		if got, err := os.ReadFile(target); err != nil || string(got) != vouchingBody {
			t.Errorf("the link's target reads %q (%v); the take must remove the link, not touch its target", got, err)
		}
	})
}

func TestReadCleanStop_VouchesOnlyForARegularFile(t *testing.T) {
	dir, target := linkedMarker(t, t.TempDir(), "OPTIONS-000099")
	if err := os.WriteFile(target, []byte(vouchingBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec, err := readCleanStop(cleanStopPath(dir)); rec != nil || err == nil {
		t.Errorf("readCleanStop through a symlink = %+v, %v; want no record and an error", rec, err)
	}
	if err := os.Remove(cleanStopPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cleanStopPath(dir), []byte(vouchingBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec, err := readCleanStop(cleanStopPath(dir)); rec == nil || err != nil {
		t.Errorf("readCleanStop of a regular marker = %+v, %v; want it to vouch", rec, err)
	}
}

// TestDiscardCleanStop_DoesNotWriteThroughASymlink: when the name cannot be
// removed, the fallback empties the marker; it must not empty the file a
// symlink there points at.
func TestDiscardCleanStop_DoesNotWriteThroughASymlink(t *testing.T) {
	dir, target := linkedMarker(t, t.TempDir(), "OPTIONS-000099")
	if err := os.WriteFile(target, []byte("the riddler's ledger"), 0o644); err != nil {
		t.Fatal(err)
	}
	markerRemove = func(string) error { return errors.New("simulated: the directory refuses the unlink") }
	t.Cleanup(func() { markerRemove = os.Remove })
	if err := discardCleanStop(cleanStopPath(dir)); err == nil {
		t.Error("discardCleanStop = nil; a symlink that could not be removed was not discarded")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "the riddler's ledger" {
		t.Errorf("the link's target reads %q (%v); the fallback wrote through the symlink", got, err)
	}
}

// TestWriteFileSynced_RefusesASymlink: every marker write goes through
// writeFileSynced (MarkCleanStop's, the discard fallback's), so it refuses
// a symlink at the name, dangling or not, rather than writing where it
// points.
func TestWriteFileSynced_RefusesASymlink(t *testing.T) {
	t.Run("to a file", func(t *testing.T) {
		dir, target := linkedMarker(t, t.TempDir(), "OPTIONS-000099")
		if err := os.WriteFile(target, []byte("the riddler's ledger"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeFileSynced(cleanStopPath(dir), []byte(vouchingBody)); !errors.Is(err, errNotRegularFile) {
			t.Errorf("writeFileSynced = %v; want errNotRegularFile", err)
		}
		if got, _ := os.ReadFile(target); string(got) != "the riddler's ledger" {
			t.Errorf("the link's target reads %q; it was written through", got)
		}
	})
	t.Run("dangling", func(t *testing.T) {
		dir, target := linkedMarker(t, t.TempDir(), "OPTIONS-000099")
		if err := writeFileSynced(cleanStopPath(dir), []byte(vouchingBody)); !errors.Is(err, errNotRegularFile) {
			t.Errorf("writeFileSynced = %v; want errNotRegularFile", err)
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the link's target was made (Lstat: %v)", err)
		}
	})
	t.Run("a regular file is replaced, and a new one made", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "hnsw.clean-stop")
		for _, body := range []string{"a longer first body", vouchingBody} {
			if err := writeFileSynced(p, []byte(body)); err != nil {
				t.Fatalf("writeFileSynced = %v", err)
			}
			if got, err := os.ReadFile(p); err != nil || string(got) != body {
				t.Errorf("the file reads %q (%v); want %q", got, err, body)
			}
		}
	})
}

// TestHNSWCleanStop_ARestoreCannotFillAMarkerSymlink is the reviewer's
// probe, through the staged restore's steps (R1-25): the stager's discard
// before it stages (DiscardHNSWCleanStop), then the swap putting the
// restored store's files in place, here ExtractStore into the database
// directory, whose ordinary entry is the dangling link's target. The boot
// after the swap takes the marker; it must find nothing that vouches,
// whether or not the discard ran first.
func TestHNSWCleanStop_ARestoreCannotFillAMarkerSymlink(t *testing.T) {
	for _, tc := range []struct {
		name           string
		discardFirst   bool
		wantLinkBefore bool
	}{
		{"with the stager's discard before the swap", true, false},
		{"without it: the boot's read refuses the link", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dir, target := linkedMarker(t, base, "OPTIONS-000099")
			archive := filepath.Join(t.TempDir(), "joker.tar")
			writeRestoreTar(t, archive, [][2]string{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{"audiobooks.pebble/OPTIONS-000099", vouchingBody},
			})
			if tc.discardFirst {
				if err := DiscardHNSWCleanStop(filepath.Join(base, "audiobooks.pebble")); err != nil {
					t.Fatalf("DiscardHNSWCleanStop = %v", err)
				}
				nameGone(t, cleanStopPath(dir))
			}
			if _, err := backup.ExtractStore(context.Background(), archive, filepath.Dir(target)); err != nil {
				t.Fatalf("ExtractStore = %v", err)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != vouchingBody {
				t.Fatalf("the entry reads %q (%v); want it written", got, err)
			}
			_, lerr := os.Lstat(cleanStopPath(dir))
			if (lerr == nil) != tc.wantLinkBefore {
				t.Fatalf("the marker name before the boot: Lstat = %v", lerr)
			}
			if rec, err := takeCleanStop(dir); rec != nil {
				t.Errorf("the next boot's take vouches: %+v (%v)", *rec, err)
			}
			nameGone(t, cleanStopPath(dir))
		})
	}
}

// TestExtractStore_RefusesAnEntryNamedForTheSnapshotDirectory: the archive
// extractor refuses an entry with a path component named for the HNSW
// snapshot directory, which no backup holds (backup's hnswSnapshotDirName,
// a copy of HNSWSnapshotDir's name: the entry is built from this function,
// so the two cannot drift apart unnoticed).
func TestExtractStore_RefusesAnEntryNamedForTheSnapshotDirectory(t *testing.T) {
	snap := filepath.Base(HNSWSnapshotDir(filepath.Join("gotham", "audiobooks.pebble")))
	archive := filepath.Join(t.TempDir(), "the-court-of-owls.tar")
	writeRestoreTar(t, archive, [][2]string{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"audiobooks.pebble/" + snap + "/" + hnswCleanStopFile, vouchingBody},
	})
	store := filepath.Join(t.TempDir(), "staging", "db")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.ExtractStore(context.Background(), archive, store); !errors.Is(err, backup.ErrArchiveCarriesHNSWSnapshot) {
		t.Fatalf("ExtractStore = %v; want backup.ErrArchiveCarriesHNSWSnapshot", err)
	}
}

// writeRestoreTar writes an uncompressed archive of regular files.
func writeRestoreTar(t *testing.T, path string, entries [][2]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
