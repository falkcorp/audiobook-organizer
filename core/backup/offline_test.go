// file: core/backup/offline_test.go
// version: 1.0.0
// guid: 0b7d2c9e-41a8-4d65-8f13-6e5a9c2d7b40
// last-edited: 2026-11-12

package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// repack rewrites the gzip archive at src to dst entry by entry. mutate sees
// each entry and returns its (possibly new) bytes and whether to keep it; extra
// entries are appended. No sidecar is written, so what the extractor refuses
// is refused by the archive's own manifest.
func repack(t *testing.T, src, dst string, mutate func(name string, b []byte) ([]byte, bool), extra map[string][]byte) {
	t.Helper()
	got := entriesOf(t, src)
	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	// The manifest must stay the first entry.
	sort.SliceStable(names, func(i, j int) bool { return names[i] == backup.ManifestName && names[j] != backup.ManifestName })
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	put := func(name string, b []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range names {
		b, keep := mutate(n, []byte(got[n]))
		if keep {
			put(n, b)
		}
	}
	for n, b := range extra {
		put(n, b)
	}
	if err := errors.Join(tw.Close(), zw.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// extracted lists dir's files, relative and slash-separated, with their modes.
func extracted(t *testing.T, dir string) map[string]fs.FileMode {
	t.Helper()
	out := map[string]fs.FileMode{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if rel != "." {
			out[filepath.ToSlash(rel)] = fi.Mode()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func emptyDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExtractStoreCoreArchiveWritesOnlyTheStore(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mediacore.yaml")
	if err := os.WriteFile(cfg, []byte("listen: 192.0.2.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.opts.Config, e.opts.ConfigPath = e.cfg, cfg
	e.rebuild()
	a := e.create("manual")
	entries := entriesOf(t, e.path(a))
	var wantDB []string
	other := 0
	for n := range entries {
		if rest, ok := strings.CutPrefix(n, "db/"); ok {
			wantDB = append(wantDB, rest)
		} else {
			other++
		}
	}
	if len(wantDB) == 0 || other < 2 {
		t.Fatalf("fixture: %d db entries and %d others in %v", len(wantDB), other, keysOf(entries))
	}

	dir := emptyDir(t)
	id, err := backup.ExtractStore(t.Context(), e.path(a), dir)
	if err != nil {
		t.Fatalf("ExtractStore: %v", err)
	}
	if id != string(a.ID) {
		t.Errorf("ID = %q, want the manifest's %q", id, a.ID)
	}
	got := extracted(t, dir)
	for _, rel := range wantDB {
		mode, ok := got[rel]
		if !ok {
			t.Errorf("db/%s was not extracted", rel)
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || string(b) != entries["db/"+rel] {
			t.Errorf("%s differs from the archive's entry (%v)", rel, err)
		}
		if mode.Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", rel, mode.Perm())
		}
	}
	for rel, mode := range got {
		if mode.IsDir() {
			if mode.Perm() != 0o700 {
				t.Errorf("directory %s mode = %v, want 0700", rel, mode.Perm())
			}
			continue
		}
		if !contains(wantDB, rel) {
			t.Errorf("%s was extracted but is not under db/", rel)
		}
	}
	for _, name := range []string{backup.ManifestName, "config", "manifest.json"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s was extracted into the store directory", name)
		}
	}
}

func contains(s []string, v string) bool {
	return slices.Contains(s, v)
}

func TestExtractStoreRefusesWhatTheManifestDoesNotDescribe(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	src := e.path(a)
	entries := entriesOf(t, src)
	// The smallest-named non-empty store entry: a map walk could pick an empty
	// file, which the same-size mutation below would leave unchanged.
	var dbName string
	for _, n := range slices.Sorted(maps.Keys(entries)) {
		if strings.HasPrefix(n, "db/") && len(entries[n]) > 0 {
			dbName = n
			break
		}
	}
	if dbName == "" {
		t.Fatalf("fixture: no non-empty db entry in %v", keysOf(entries))
	}
	for _, tc := range []struct {
		name   string
		mutate func(string, []byte) ([]byte, bool)
		extra  map[string][]byte
	}{
		{"an entry whose bytes changed", func(n string, b []byte) ([]byte, bool) {
			if n == dbName {
				b = append(append([]byte{}, b...), 'x')
			}
			return b, true
		}, nil},
		{"an entry of the same size and other bytes", func(n string, b []byte) ([]byte, bool) {
			if n == dbName && len(b) > 0 {
				b = append([]byte{}, b...)
				b[0] ^= 0xff
			}
			return b, true
		}, nil},
		{"an entry the manifest lists missing", func(n string, b []byte) ([]byte, bool) { return b, n != dbName }, nil},
		{"an entry the manifest does not list", func(_ string, b []byte) ([]byte, bool) { return b, true },
			map[string][]byte{"db/000999.sst": []byte("joker")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "gotham_20261112_030000.tar.gz")
			repack(t, src, bad, tc.mutate, tc.extra)
			dir := emptyDir(t)
			_, err := backup.ExtractStore(t.Context(), bad, dir)
			if !errors.Is(err, backup.ErrManifest) {
				t.Fatalf("ExtractStore = %v, want ErrManifest", err)
			}
		})
	}
}

func TestExtractStoreChecksumSidecar(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	src := e.path(a)
	sidecar := src + backup.ChecksumSidecar
	good, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("Create wrote no sidecar: %v", err)
	}
	t.Run("matching", func(t *testing.T) {
		if _, err := backup.ExtractStore(t.Context(), src, emptyDir(t)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), filepath.Base(src))
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bare, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.ExtractStore(t.Context(), bare, emptyDir(t)); err != nil {
			t.Fatalf("an archive named by hand with no sidecar: %v", err)
		}
	})
	defer func() {
		if err := os.WriteFile(sidecar, good, 0o600); err != nil {
			t.Fatal(err)
		}
	}()
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{"a digest of other bytes", strings.Repeat("0", 64) + "  " + a.Name + "\n", backup.ErrChecksumMismatch},
		{"not a digest", "joker\n", backup.ErrNoChecksum},
		{"empty", "", backup.ErrNoChecksum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(sidecar, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := emptyDir(t)
			_, err := backup.ExtractStore(t.Context(), src, dir)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ExtractStore = %v, want %v", err, tc.want)
			}
			if len(extracted(t, dir)) != 0 {
				t.Errorf("a refused sidecar left %v in the store directory", extracted(t, dir))
			}
		})
	}
}

func TestExtractStoreManifestSidecarMustMatch(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	side := e.path(a) + backup.ManifestSidecar
	if err := os.WriteFile(side, []byte(`{"format":"mediacore-backup/1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := emptyDir(t)
	_, err := backup.ExtractStore(t.Context(), e.path(a), dir)
	if !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("ExtractStore = %v, want ErrManifest", err)
	}
	if len(extracted(t, dir)) != 0 {
		t.Errorf("left %v", extracted(t, dir))
	}
}

func TestExtractStoreRefusesBadPaths(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	src := e.path(a)
	t.Run("relative archive", func(t *testing.T) {
		if _, err := backup.ExtractStore(t.Context(), a.Name, emptyDir(t)); err == nil {
			t.Fatal("a relative archive path was accepted")
		}
	})
	t.Run("relative store", func(t *testing.T) {
		if _, err := backup.ExtractStore(t.Context(), src, "store"); err == nil {
			t.Fatal("a relative store path was accepted")
		}
	})
	t.Run("store with files", func(t *testing.T) {
		dir := emptyDir(t)
		keep := filepath.Join(dir, "LOCK")
		if err := os.WriteFile(keep, []byte("alfred"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.ExtractStore(t.Context(), src, dir); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("ExtractStore into a used directory = %v", err)
		}
		if b, _ := os.ReadFile(keep); string(b) != "alfred" {
			t.Errorf("the existing file was touched: %q", b)
		}
	})
	t.Run("store missing", func(t *testing.T) {
		if _, err := backup.ExtractStore(t.Context(), src, filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("a missing store directory was accepted")
		}
	})
	t.Run("store is a symlink", func(t *testing.T) {
		real := emptyDir(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.ExtractStore(t.Context(), src, link); err == nil {
			t.Fatal("a symlinked store directory was accepted")
		}
		if len(extracted(t, real)) != 0 {
			t.Errorf("wrote through the link: %v", extracted(t, real))
		}
	})
	t.Run("archive is a symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "gotham_20261112_040000.tar.gz")
		if err := os.Symlink(src, link); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.ExtractStore(t.Context(), link, emptyDir(t)); !errors.Is(err, backuparchive.ErrNotRegularFile) {
			t.Fatalf("ExtractStore through a link = %v, want ErrNotRegularFile", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := backup.ExtractStore(ctx, src, emptyDir(t)); !errors.Is(err, context.Canceled) {
			t.Fatalf("ExtractStore = %v, want context.Canceled", err)
		}
	})
}

func TestExtractStoreRefusesAManifestWithoutAStore(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	m, err := backup.ParseManifest([]byte(entriesOf(t, e.path(a))[backup.ManifestName]))
	if err != nil {
		t.Fatal(err)
	}
	keep := m.Entries[:0:0]
	for _, en := range m.Entries {
		if !strings.HasPrefix(en.Path, "db/") {
			keep = append(keep, en)
		}
	}
	m.Entries = keep
	mb, err := backup.MarshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "gotham_20261112_050000.tar.gz")
	repack(t, e.path(a), bad, func(n string, b []byte) ([]byte, bool) {
		switch {
		case n == backup.ManifestName:
			return mb, true
		case strings.HasPrefix(n, "db/"):
			return nil, false
		}
		return b, true
	}, nil)
	dir := emptyDir(t)
	if _, err := backup.ExtractStore(t.Context(), bad, dir); !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("ExtractStore = %v, want ErrManifest", err)
	}
	if len(extracted(t, dir)) != 0 {
		t.Errorf("left %v", extracted(t, dir))
	}
}

func TestExtractStoreLegacyArchive(t *testing.T) {
	for _, codec := range []string{"gzip", "zstd", "none"} {
		for _, sidecar := range []bool{true, false} {
			name := codec + "/no-sidecar"
			if sidecar {
				name = codec + "/sidecar"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				fname := legacyArchive(t, e, codec, sidecar)
				path := filepath.Join(e.backupDir, fname)
				dir := emptyDir(t)
				id, err := backup.ExtractStore(t.Context(), path, dir)
				if err != nil {
					t.Fatalf("ExtractStore: %v", err)
				}
				want := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(fname, ".zst"), ".gz"), ".tar")
				if id != backup.LegacyIDPrefix+want || !backup.IsLegacyID(id) || !backup.ValidArchiveID(id) {
					t.Errorf("ID = %q, want legacy-%s", id, want)
				}
				// The store opens: the very files of the checkpoint it was tarred from.
				got := extracted(t, dir)
				if len(got) == 0 {
					t.Fatal("nothing extracted")
				}
				for rel, mode := range got {
					if mode.IsDir() && mode.Perm() != 0o700 || mode.IsRegular() && mode.Perm() != 0o600 {
						t.Errorf("%s mode = %v", rel, mode)
					}
					if strings.Contains(rel, "audiobooks.pebble") {
						t.Errorf("%s kept the archive's top-level directory", rel)
					}
				}
				manifest := false
				for rel := range got {
					manifest = manifest || strings.HasPrefix(rel, "MANIFEST-")
				}
				if !manifest {
					t.Errorf("no Pebble MANIFEST at the top of the extracted store: %v", got)
				}
			})
		}
	}
}

func TestExtractStoreLegacyRefusals(t *testing.T) {
	e := newEnv(t)
	fname := legacyArchive(t, e, "gzip", true)
	path := filepath.Join(e.backupDir, fname)
	t.Run("sidecar of other bytes", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), fname)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad+backup.ChecksumSidecar, []byte(strings.Repeat("1", 64)+"  "+fname+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := emptyDir(t)
		if _, err := backup.ExtractStore(t.Context(), bad, dir); !errors.Is(err, backup.ErrChecksumMismatch) {
			t.Fatalf("ExtractStore = %v, want ErrChecksumMismatch", err)
		}
		if len(extracted(t, dir)) != 0 {
			t.Errorf("left %v", extracted(t, dir))
		}
	})
	t.Run("not one store directory", func(t *testing.T) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		for _, n := range []string{"README", "alfred/CURRENT"} {
			if err := tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := errors.Join(tw.Close(), zw.Close()); err != nil {
			t.Fatal(err)
		}
		bad := filepath.Join(t.TempDir(), "gotham_20261112_060000.tar.gz")
		if err := os.WriteFile(bad, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := emptyDir(t)
		_, err := backup.ExtractStore(t.Context(), bad, dir)
		if !errors.Is(err, backuparchive.ErrNotAStore) {
			t.Fatalf("ExtractStore = %v, want ErrNotAStore", err)
		}
		if len(extracted(t, dir)) != 0 {
			t.Errorf("a refused archive was partly extracted: %v", extracted(t, dir))
		}
	})
	t.Run("a link entry", func(t *testing.T) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/CURRENT", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(tw.Close(), zw.Close()); err != nil {
			t.Fatal(err)
		}
		bad := filepath.Join(t.TempDir(), "gotham_20261112_070000.tar.gz")
		if err := os.WriteFile(bad, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := emptyDir(t)
		if _, err := backup.ExtractStore(t.Context(), bad, dir); !errors.Is(err, backuparchive.ErrEntryType) {
			t.Fatalf("ExtractStore = %v, want ErrEntryType", err)
		}
		if len(extracted(t, dir)) != 0 {
			t.Errorf("left %v", extracted(t, dir))
		}
	})
	t.Run("a name the catalogue cannot address", func(t *testing.T) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		odd := filepath.Join(t.TempDir(), "audiobooks.2026.v1.tar.gz")
		if err := os.WriteFile(odd, b, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		id, err := backup.ExtractStore(t.Context(), odd, emptyDir(t))
		if err != nil {
			t.Fatal(err)
		}
		if want := backup.LegacyIDPrefix + hex.EncodeToString(sum[:])[:16]; id != want {
			t.Errorf("ID = %q, want the digest's %q", id, want)
		}
	})
}

func TestExtractStoreReadsNoMoreThanItsLimits(t *testing.T) {
	// A header that claims more than any limit allows is refused before the
	// extractor writes or sizes a buffer from it.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/000001.sst", Typeflag: tar.TypeReg, Mode: 0o600, Size: backuparchive.DefaultMaxEntryBytes + 1}); err != nil {
		t.Fatal(err)
	}
	// Close errors (the body is missing) are expected; the header is what matters.
	_ = tw.Flush()
	_ = zw.Close()
	bomb := filepath.Join(t.TempDir(), "gotham_20261112_080000.tar.gz")
	if err := os.WriteFile(bomb, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := emptyDir(t)
	if _, err := backup.ExtractStore(t.Context(), bomb, dir); !errors.Is(err, backuparchive.ErrLimit) {
		t.Fatalf("ExtractStore = %v, want ErrLimit", err)
	}
	if len(extracted(t, dir)) != 0 {
		t.Errorf("left %v", extracted(t, dir))
	}
}
