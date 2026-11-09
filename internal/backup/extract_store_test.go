// file: internal/backup/extract_store_test.go
// version: 1.0.0
// guid: 8a4d2f6c-1b7e-4c39-9e05-d3a8b61f2c74
// last-edited: 2026-11-08

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// gothamStore is a Pebble-shaped store directory with two files.
func gothamStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gotham.pebble")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"CURRENT": "MANIFEST-000001\n", "000001.sst": "the long halloween"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// emptyStoreDir is the kernel's staging db/: an empty 0700 directory.
func emptyStoreDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staging", "db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// tarGz writes a gzip tar of the named regular files (name -> body).
func tarGz(t *testing.T, entries map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hand-made.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestExtractStore (storage restore-offline --archive's extractor): a
// backup CreateBackup wrote is verified against its sidecar, its one store
// directory lands in storeDir (which keeps its mode) and the archive's
// SHA-256 is returned; a sidecar that disagrees refuses with nothing
// extracted; an archive with no sidecar is extracted unverified; an archive
// that is not one store directory, a storeDir that is not empty, a relative
// path and a cancelled context are refused.
func TestExtractStore(t *testing.T) {
	ctx := context.Background()
	src := gothamStore(t)
	info, err := CreateBackup(src, "pebble", BackupConfig{BackupDir: t.TempDir(), MaxBackups: 5})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("verified", func(t *testing.T) {
		dst := emptyStoreDir(t)
		sum, err := ExtractStore(ctx, info.Path, dst)
		if err != nil {
			t.Fatal(err)
		}
		if sum != info.Checksum {
			t.Fatalf("archive ID %s, want the archive's SHA-256 %s", sum, info.Checksum)
		}
		for name, want := range map[string]string{"CURRENT": "MANIFEST-000001\n", "000001.sst": "the long halloween"} {
			got, err := os.ReadFile(filepath.Join(dst, name))
			if err != nil || string(got) != want {
				t.Fatalf("%s in the store = %q, %v; want %q", name, got, err, want)
			}
		}
		if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("store directory mode after the extraction: %v, %v; want 0700 kept", fi.Mode(), err)
		}
		if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), ".extract-*")); len(left) != 0 {
			t.Fatalf("extraction directories left behind: %v", left)
		}
	})

	t.Run("sidecar mismatch", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), filepath.Base(info.Path))
		raw, err := os.ReadFile(info.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		bad := "0000000000000000000000000000000000000000000000000000000000000000  " + filepath.Base(archive) + "\n"
		if err := os.WriteFile(checksumSidecarPath(archive), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		dst := emptyStoreDir(t)
		if _, err := ExtractStore(ctx, archive, dst); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("extract with a mismatched sidecar = %v, want ErrChecksumMismatch", err)
		}
		if entries, _ := os.ReadDir(dst); len(entries) != 0 {
			t.Fatalf("a refused archive left %d entries in the store directory", len(entries))
		}
		if err := os.Remove(checksumSidecarPath(archive)); err != nil {
			t.Fatal(err)
		}
		if _, err := ExtractStore(ctx, archive, dst); err != nil {
			t.Fatalf("extract with no sidecar = %v, want it extracted unverified", err)
		}
	})

	refusals := map[string]func(t *testing.T) (string, string, context.Context){
		"several top-level entries": func(t *testing.T) (string, string, context.Context) {
			return tarGz(t, map[string]string{"gotham.pebble/CURRENT": "x", "joker.txt": "ha"}), emptyStoreDir(t), ctx
		},
		"a single file": func(t *testing.T) (string, string, context.Context) {
			return tarGz(t, map[string]string{"gotham.db": "sqlite"}), emptyStoreDir(t), ctx
		},
		"store directory not empty": func(t *testing.T) (string, string, context.Context) {
			dst := emptyStoreDir(t)
			if err := os.WriteFile(filepath.Join(dst, "penguin"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return info.Path, dst, ctx
		},
		"relative archive": func(t *testing.T) (string, string, context.Context) {
			return "backups/gotham.tar.gz", emptyStoreDir(t), ctx
		},
		"cancelled": func(t *testing.T) (string, string, context.Context) {
			c, cancel := context.WithCancel(ctx)
			cancel()
			return info.Path, emptyStoreDir(t), c
		},
	}
	for name, setup := range refusals {
		t.Run(name, func(t *testing.T) {
			archive, dst, c := setup(t)
			before, _ := os.ReadDir(dst)
			if _, err := ExtractStore(c, archive, dst); err == nil {
				t.Fatal("ExtractStore succeeded")
			}
			if after, _ := os.ReadDir(dst); len(after) != len(before) {
				t.Fatalf("a refused extraction changed the store directory: %d entries, was %d", len(after), len(before))
			}
		})
	}
}
