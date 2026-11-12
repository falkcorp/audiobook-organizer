// file: core/app/offline_restore_test.go
// version: 1.0.0
// guid: c27e9a40-6b15-4e83-9d0a-4f1b8e3a7c62
// last-edited: 2026-11-12

package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyTarOf tars the closed store directory under HEAD's audiobooks.pebble/
// top-level directory, gzip-compressed (a legacy-abo/0 archive).
func legacyTarOf(t *testing.T, store, out string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(store, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(store, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/" + filepath.ToSlash(rel), Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(b))}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err := errors.Join(err, tw.Close(), zw.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRestoreOfflineArchiveWithKeyringAndGapFill (F8-01; 05-storage
// §5S.3.12, 05-backup §5B.3.4): an app that sets no R1ArchiveExtract restores
// archives through core's extractor, and storage restore-offline --archive
// brings back the keyring export (--keyring) and the off-host gap-fill file
// (--gap-fill) at the paths the state-file table gives, naming the missing
// gap-fill file when a legacy archive is restored without it.
func TestRestoreOfflineArchiveWithKeyringAndGapFill(t *testing.T) {
	opts := offlineOpts(t)
	keeper := offlineKeeper()
	served(t, opts, keeper)
	store := filepath.Join(opts.StateDir, "storage", "main.pebble")
	archive := filepath.Join(t.TempDir(), "audiobooks_pebble_20261112_020000.tar.gz")
	legacyTarOf(t, store, archive)

	var restore Command
	for _, c := range OfflineCommands(opts, keeper) {
		if c.Name == "storage restore-offline" {
			restore = c
		}
	}
	if restore.Run == nil {
		t.Fatal("no storage restore-offline command")
	}
	var help bytes.Buffer
	if err := restore.Run(t.Context(), []string{"--help"}, &help); err != nil || !strings.Contains(help.String(), "--keyring") || !strings.Contains(help.String(), "--gap-fill") {
		t.Fatalf("--help = %v, output %q", err, help.String())
	}

	dir := t.TempDir()
	keyring := filepath.Join(dir, "gotham.keyring")
	gapFill := filepath.Join(dir, "config.yaml")
	for p, body := range map[string]string{keyring: "exported-keyring", gapFill: "root_dir: /batcave\n"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	liveKeyring := filepath.Join(opts.StateDir, "secrets.keyring")
	liveGap := filepath.Join(filepath.Dir(store), "config.yaml")

	var out bytes.Buffer
	if err := restore.Run(t.Context(), []string{"--archive", archive, "--accept-loss"}, &out); err != nil {
		t.Fatalf("restore without the files = %v (%s)", err, out.String())
	}
	if !strings.Contains(out.String(), "--gap-fill") {
		t.Errorf("a legacy archive restored without --gap-fill printed %q", out.String())
	}
	for _, p := range []string{liveGap} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a restore that was given no such file: %v", p, err)
		}
	}

	out.Reset()
	if err := restore.Run(t.Context(), []string{"--archive", archive, "--keyring", keyring, "--gap-fill", gapFill, "--accept-loss"}, &out); err != nil {
		t.Fatalf("restore with the files = %v (%s)", err, out.String())
	}
	if strings.Contains(out.String(), "--gap-fill") {
		t.Errorf("warned about a gap-fill file that was given: %q", out.String())
	}
	if got := readFile(t, liveKeyring); got != "exported-keyring" {
		t.Errorf("keyring = %q, want the export", got)
	}
	if got := readFile(t, liveGap); got != "root_dir: /batcave\n" {
		t.Errorf("gap-fill file = %q, want the operator's", got)
	}

	// The flags are for an archive: a checkpoint holds its own copies.
	out.Reset()
	err := restore.Run(t.Context(), []string{"--checkpoint", "20261112T000000Z-0badc0de", "--keyring", keyring, "--accept-loss"}, &out)
	if err == nil || !strings.Contains(err.Error(), "go with --archive") {
		t.Fatalf("--checkpoint with --keyring = %v, want the refusal", err)
	}
}
