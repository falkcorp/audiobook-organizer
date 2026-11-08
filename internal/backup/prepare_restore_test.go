// file: internal/backup/prepare_restore_test.go
// version: 1.0.0
// guid: 29ed21ba-737f-4de2-844d-5681aec413d7
// last-edited: 2026-11-08

package backup

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writePlainTar writes an uncompressed tar archive of regular files.
func writePlainTar(t *testing.T, path string, entries [][2]string) {
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

// R1-15 round-3 review (B1-r3): the restore handler marks the HNSW snapshot
// diverged between PrepareRestoreIn and Extract, so the split must put every
// refusal in the first step and every write in the second. PrepareRestoreIn
// takes no target, so it cannot write into one; these tests pin which errors
// it returns and that Extract is where a restore can fail after writing.

func TestPrepareRestoreIn_Refusals(t *testing.T) {
	backupDir := t.TempDir()
	writePlainTar(t, filepath.Join(backupDir, "hush.tar"), [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})
	if err := os.WriteFile(filepath.Join(backupDir, "hush.tar.sha256"), []byte("00  hush.tar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlainTar(t, filepath.Join(backupDir, "knightfall.tar"), [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})

	cases := []struct {
		name      string
		backupDir string
		filename  string
		verify    bool
		wantIs    []error
	}{
		{"not found", backupDir, "year-one.tar", false, []error{ErrBackupNotFound, os.ErrNotExist}},
		{"verification unsupported", backupDir, "knightfall.tar", true, []error{ErrVerificationUnsupported}},
		{"checksum mismatch", backupDir, "hush.tar", true, []error{ErrChecksumMismatch}},
		{"the backup directory cannot be read", filepath.Join(backupDir, "arkham"), "knightfall.tar", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := PrepareRestoreIn(tc.backupDir, tc.filename, tc.verify)
			if err == nil || r != nil {
				t.Fatalf("PrepareRestoreIn = %v, %v; want a refusal and no prepared restore", r, err)
			}
			for _, want := range tc.wantIs {
				if !errors.Is(err, want) {
					t.Errorf("error %v does not match %v", err, want)
				}
			}
		})
	}
}

func TestPreparedRestore_ExtractCanFailAfterWriting(t *testing.T) {
	backupDir := t.TempDir()
	writePlainTar(t, filepath.Join(backupDir, "knightfall.tar"), [][2]string{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"../bane", "venom"},
	})
	r, err := PrepareRestoreIn(backupDir, "knightfall.tar", false)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := r.Extract(target); err == nil {
		t.Fatal("Extract succeeded; want the escaping entry refused")
	}
	if _, err := os.Stat(filepath.Join(target, "audiobooks.pebble", "MANIFEST-000001")); err != nil {
		t.Errorf("the entry before the failure was not written (%v); this case must fail after a write", err)
	}
}

// TestPreparedRestore_ExtractErrorCanMatchNotExist: an Extract error can match
// os.ErrNotExist (here the archive went away after the prepare), so no caller
// may read a not-exist error as a restore that wrote nothing. The round-2
// RefusedBeforeWriting kept os.ErrNotExist off its list for this reason; the
// split replaced it, because the step that failed now says whether anything
// could have been written.
func TestPreparedRestore_ExtractErrorCanMatchNotExist(t *testing.T) {
	backupDir := t.TempDir()
	archive := filepath.Join(backupDir, "the-cult.tar")
	writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})
	r, err := PrepareRestoreIn(backupDir, "the-cult.tar", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := r.Extract(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Extract after the archive went away = %v; want an error matching os.ErrNotExist", err)
	}
}
