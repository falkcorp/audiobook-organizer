// file: core/internal/backuparchive/extract_test.go
// version: 1.0.0
// guid: 712b8e10-ab0e-4774-a71b-de7e8906e286
// last-edited: 2026-11-12

package backuparchive_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestExtractorModesAndConfinement(t *testing.T) {
	dst := t.TempDir()
	ex, err := backuparchive.NewExtractor(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if _, err := ex.WriteFile("db/sub/000001.sst", strings.NewReader("Arkham"), &backuparchive.Expect{Size: 6, SHA256: sum("Arkham")}); err != nil {
		t.Fatal(err)
	}
	if err := ex.Sync(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "db", "sub", "000001.sst"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Join(dst, "db", "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	// A name is never an overwrite.
	if _, err := ex.WriteFile("db/sub/000001.sst", strings.NewReader("Joker"), nil); err == nil {
		t.Error("an existing file was overwritten")
	}
	got, _ := os.ReadFile(filepath.Join(dst, "db", "sub", "000001.sst"))
	if string(got) != "Arkham" {
		t.Errorf("content = %q after a refused overwrite", got)
	}
	// Names that escape are refused before anything is made.
	for _, bad := range []string{"../escape", "/abs", "db/../../escape"} {
		if _, err := ex.WriteFile(bad, strings.NewReader("x"), nil); !errors.Is(err, backuparchive.ErrEntryName) {
			t.Errorf("WriteFile(%q) = %v", bad, err)
		}
	}
}

func TestExtractorRefusesToFollowASymlinkedDirectory(t *testing.T) {
	dst, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dst, "db")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	ex, err := backuparchive.NewExtractor(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if _, err := ex.WriteFile("db/planted", strings.NewReader("x"), nil); err == nil {
		t.Fatal("a write went through a symlinked directory")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("the extractor wrote %d entries outside its root", len(entries))
	}
}

func TestExtractorMismatchRemovesTheFile(t *testing.T) {
	dst := t.TempDir()
	ex, err := backuparchive.NewExtractor(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	for name, want := range map[string]backuparchive.Expect{
		"wrong-digest": {Size: 3, SHA256: sum("abc")},
		"wrong-size":   {Size: 99, SHA256: sum("xyz")},
	} {
		_, err := ex.WriteFile(name, strings.NewReader("xyz"), &want)
		if !errors.Is(err, backuparchive.ErrEntryMismatch) {
			t.Errorf("%s: err = %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dst, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the mismatched file was left behind", name)
		}
	}
}

func TestNewExtractorRefusesNonDirectories(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backuparchive.NewExtractor(f); err == nil {
		t.Error("a file was accepted as an extraction directory")
	}
	if _, err := backuparchive.NewExtractor("relative/dir"); err == nil {
		t.Error("a relative path was accepted")
	}
}

func TestLegacyScanAndExtract(t *testing.T) {
	for _, codec := range []string{backuparchive.Gzip, backuparchive.Zstd, backuparchive.None} {
		t.Run(codec, func(t *testing.T) {
			p := buildTar(t, codec,
				ent{name: "audiobooks.pebble", typ: tar.TypeDir},
				ent{name: "audiobooks.pebble/000001.sst", body: "Alfred"},
				ent{name: "audiobooks.pebble/sub", typ: tar.TypeDir},
				ent{name: "audiobooks.pebble/sub/MANIFEST", body: "Lucius"})
			info, err := backuparchive.ScanLegacy(context.Background(), p, backuparchive.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if info.Root != "audiobooks.pebble" || info.Files != 2 || info.Codec != codec || len(info.SHA256) != 64 {
				t.Fatalf("info = %+v", info)
			}
			store := t.TempDir()
			if err := backuparchive.ExtractLegacy(context.Background(), p, info, store, backuparchive.Limits{}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(store, "sub", "MANIFEST"))
			if err != nil || string(got) != "Lucius" {
				t.Fatalf("extracted = %q, %v", got, err)
			}
			if fi, _ := os.Stat(filepath.Join(store, "000001.sst")); fi.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v", fi.Mode().Perm())
			}
		})
	}
}

func TestLegacyRefusesWhatIsNotOneStoreDirectory(t *testing.T) {
	cases := map[string][]ent{
		"top-level file":      {{name: "stray.txt", body: "x"}},
		"two roots":           {{name: "a/f", body: "x"}, {name: "b/f", body: "x"}},
		"only directories":    {{name: "audiobooks.pebble", typ: tar.TypeDir}},
		"a symlink in a tree": {{name: "audiobooks.pebble/f", body: "x"}, {name: "audiobooks.pebble/l", typ: tar.TypeSymlink, link: "/etc"}},
		"a traversal":         {{name: "audiobooks.pebble/f", body: "x"}, {name: "audiobooks.pebble/../x", body: "x"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			p := buildTar(t, backuparchive.Gzip, entries...)
			if _, err := backuparchive.ScanLegacy(context.Background(), p, backuparchive.Limits{}); err == nil {
				t.Fatal("the archive was accepted")
			}
		})
	}
	empty := buildTar(t, backuparchive.None)
	if _, err := backuparchive.ScanLegacy(context.Background(), empty, backuparchive.Limits{}); !errors.Is(err, backuparchive.ErrEmptyArchive) {
		t.Errorf("empty: %v", err)
	}
}

func TestLegacyExtractRefusesAnArchiveChangedAfterTheScan(t *testing.T) {
	p := buildTar(t, backuparchive.None, ent{name: "audiobooks.pebble/f", body: "Dick Grayson"})
	info, err := backuparchive.ScanLegacy(context.Background(), p, backuparchive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	other := buildTar(t, backuparchive.None, ent{name: "audiobooks.pebble/f", body: "Jason Todd, longer"})
	if err := os.Rename(other, p); err != nil {
		t.Fatal(err)
	}
	if err := backuparchive.ExtractLegacy(context.Background(), p, info, t.TempDir(), backuparchive.Limits{}); !errors.Is(err, backuparchive.ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	// Same size, different bytes: caught by the digest.
	same := buildTar(t, backuparchive.None, ent{name: "audiobooks.pebble/f", body: "Dick Graysoo"})
	data, _ := os.ReadFile(same)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	orig := buildTar(t, backuparchive.None, ent{name: "audiobooks.pebble/f", body: "Dick Grayson"})
	info2, err := backuparchive.ScanLegacy(context.Background(), orig, backuparchive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := backuparchive.ExtractLegacy(context.Background(), p, info2, t.TempDir(), backuparchive.Limits{}); !errors.Is(err, backuparchive.ErrChanged) {
		t.Fatalf("same-size swap: err = %v, want ErrChanged", err)
	}
}

func TestLegacyExtractNeedsAnEmptyDirectory(t *testing.T) {
	p := buildTar(t, backuparchive.None, ent{name: "audiobooks.pebble/f", body: "x"})
	info, err := backuparchive.ScanLegacy(context.Background(), p, backuparchive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "old"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backuparchive.ExtractLegacy(context.Background(), p, info, store, backuparchive.Limits{}); err == nil {
		t.Fatal("extraction into a non-empty directory")
	}
}
