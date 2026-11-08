// file: internal/backup/refused_before_writing_test.go
// version: 1.0.0
// guid: 39180581-ca32-4526-82b3-79f9de417013
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

// TestRefusedBeforeWriting (R1-15 round-2 review, B1): the restore handler
// skips distrusting the HNSW snapshot only for the errors RestoreBackupIn
// returns before it writes, so each of those must report true and a failure
// after a write must report false.
func TestRefusedBeforeWriting(t *testing.T) {
	backupDir := t.TempDir()
	writePlainTar(t, filepath.Join(backupDir, "hush.tar"), [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})
	if err := os.WriteFile(filepath.Join(backupDir, "hush.tar.sha256"), []byte("00  hush.tar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlainTar(t, filepath.Join(backupDir, "knightfall.tar"), [][2]string{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"../bane", "venom"},
	})

	cases := []struct {
		name     string
		filename string
		verify   bool
		want     bool
		wantIs   error
	}{
		{"not found", "year-one.tar", false, true, ErrBackupNotFound},
		{"verification unsupported", "knightfall.tar", true, true, ErrVerificationUnsupported},
		{"checksum mismatch", "hush.tar", true, true, ErrChecksumMismatch},
		{"the extraction fails after a write", "knightfall.tar", false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			err := RestoreBackupIn(backupDir, tc.filename, target, tc.verify)
			if err == nil {
				t.Fatal("the restore succeeded; want an error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("error %v does not match %v", err, tc.wantIs)
			}
			if got := RefusedBeforeWriting(err); got != tc.want {
				t.Errorf("RefusedBeforeWriting(%v) = %v, want %v", err, got, tc.want)
			}
			_, statErr := os.Stat(filepath.Join(target, "audiobooks.pebble", "MANIFEST-000001"))
			if wrote := statErr == nil; wrote == tc.want {
				t.Errorf("wrote into the target = %v, but RefusedBeforeWriting = %v", wrote, tc.want)
			}
		})
	}

	t.Run("a not-found error still matches os.ErrNotExist", func(t *testing.T) {
		err := RestoreBackupIn(backupDir, "year-one.tar", t.TempDir(), false)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error %v does not match os.ErrNotExist", err)
		}
	})
}
