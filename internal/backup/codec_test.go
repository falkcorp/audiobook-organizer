// file: internal/backup/codec_test.go
// version: 1.0.0
// guid: 9d41f6a2-58bc-4e07-b3d9-1a6c082e4f75
// last-edited: 2026-08-29

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveCodecEmptyMeansGzip is the regression test for the zero-value
// trap. Config is persisted as a full-struct marshal with no `omitempty`, so
// this field arrives as "" in every config written before it existed. If ""
// ever resolves to anything but gzip, upgrading silently changes the archive
// format -- or, if it resolved to "none", silently stops compressing.
func TestResolveCodecEmptyMeansGzip(t *testing.T) {
	c, err := ResolveCodec("")
	if err != nil {
		t.Fatalf("empty compression must resolve, got error: %v", err)
	}
	if c.Name() != CompressionGzip {
		t.Fatalf("empty compression must mean %q, got %q", CompressionGzip, c.Name())
	}
	if c.Extension() != ".tar.gz" {
		t.Fatalf("empty compression must keep the historical extension, got %q", c.Extension())
	}
}

// TestGzipLevelZeroIsNotNoCompression guards the other half of the same trap.
// gzip.NoCompression is 0, so passing an unset level straight through would
// write uncompressed archives with no error and no log line.
func TestGzipLevelZeroIsNotNoCompression(t *testing.T) {
	payload := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 4096)

	var atDefault bytes.Buffer
	w, err := gzipCodec{}.NewWriter(&atDefault, LevelDefault)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if atDefault.Len() >= len(payload) {
		t.Fatalf("LevelDefault produced %d bytes from %d bytes of highly compressible input -- "+
			"level 0 was passed through as gzip.NoCompression", atDefault.Len(), len(payload))
	}
}

func TestResolveCodecUnknownIsAnError(t *testing.T) {
	if _, err := ResolveCodec("lz4"); err == nil {
		t.Fatal("an unknown compression name must be rejected, not silently defaulted")
	}
}

// TestRoundTripEveryCodec writes a tar through each codec and reads it back
// through SniffCodec -- i.e. the real restore path, which never consults
// configuration.
func TestRoundTripEveryCodec(t *testing.T) {
	for _, name := range SupportedCompression() {
		t.Run(name, func(t *testing.T) {
			codec, err := ResolveCodec(name)
			if err != nil {
				t.Fatalf("ResolveCodec(%q): %v", name, err)
			}

			path := filepath.Join(t.TempDir(), "archive"+codec.Extension())
			f, err := os.Create(path)
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			cw, err := codec.NewWriter(f, LevelDefault)
			if err != nil {
				t.Fatalf("NewWriter: %v", err)
			}
			tw := tar.NewWriter(cw)
			body := []byte("payload for " + name)
			if err := tw.WriteHeader(&tar.Header{
				Name: "db/file.dat", Mode: 0o600, Size: int64(len(body)),
			}); err != nil {
				t.Fatalf("WriteHeader: %v", err)
			}
			if _, err := tw.Write(body); err != nil {
				t.Fatalf("tar write: %v", err)
			}
			if err := tw.Close(); err != nil {
				t.Fatalf("tar close: %v", err)
			}
			if err := cw.Close(); err != nil {
				t.Fatalf("codec close: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("file close: %v", err)
			}

			rf, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer rf.Close()

			sniffed, err := SniffCodec(rf)
			if err != nil {
				t.Fatalf("SniffCodec: %v", err)
			}
			if sniffed.Name() != codec.Name() {
				t.Fatalf("sniffed %q, wrote %q", sniffed.Name(), codec.Name())
			}

			cr, err := sniffed.NewReader(rf)
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			defer cr.Close()

			tr := tar.NewReader(cr)
			hdr, err := tr.Next()
			if err != nil {
				t.Fatalf("tar next: %v", err)
			}
			if hdr.Name != "db/file.dat" {
				t.Fatalf("header name %q", hdr.Name)
			}
			got, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != string(body) {
				t.Fatalf("body round-trip mismatch: %q != %q", got, body)
			}
		})
	}
}

// TestSniffIgnoresTheFilename is the point of sniffing: an archive renamed to
// the wrong extension must still restore. Restoring by extension would turn
// this into "gzip: invalid header".
func TestSniffIgnoresTheFilename(t *testing.T) {
	dir := t.TempDir()
	// A zstd archive deliberately named .tar.gz.
	path := filepath.Join(dir, "audiobooks_pebble_20260829_010203.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cw, err := zstdCodec{}.NewWriter(f, LevelDefault)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := cw.Write([]byte("not actually gzip")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	f.Close()

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rf.Close()

	c, err := SniffCodec(rf)
	if err != nil {
		t.Fatalf("SniffCodec: %v", err)
	}
	if c.Name() != CompressionZstd {
		t.Fatalf("magic bytes must win over the extension; got %q", c.Name())
	}
}

// TestListBackupsSeesEveryFormat is the retention guard.
//
// enforceRetention and the organizer's freshness check both call ListBackups,
// so a listing predicate that recognised only .tar.gz would make zstd archives
// invisible to retention: nothing would ever be pruned and the backup directory
// would grow without bound. That is the 2026-08-29 outage.
func TestListBackupsSeesEveryFormat(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"audiobooks_pebble_20260829_010203.tar.gz",
		"audiobooks_pebble_20260829_010204.tar.zst",
		"audiobooks_pebble_20260829_010205.tar",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	// Not an archive; must not be listed, and must never be a retention target.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	got, err := ListBackups(dir)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(got) != len(names) {
		var listed []string
		for _, b := range got {
			listed = append(listed, b.Filename)
		}
		t.Fatalf("expected %d archives, got %d (%s) -- an unlisted archive is an archive "+
			"retention will never prune", len(names), len(got), strings.Join(listed, ", "))
	}
	for _, b := range got {
		if b.Filename == "notes.txt" {
			t.Fatal("a non-archive was listed as a backup; retention could delete it")
		}
	}
}

// TestCreateBackupHonoursCompression checks the setting reaches the filename
// and the bytes, through the real CreateBackup path.
func TestCreateBackupHonoursCompression(t *testing.T) {
	for _, tc := range []struct {
		compression string
		wantExt     string
	}{
		{CompressionGzip, ".tar.gz"},
		{CompressionZstd, ".tar.zst"},
		{CompressionNone, ".tar"},
		{"", ".tar.gz"}, // unset must stay gzip
	} {
		t.Run("compression="+tc.compression, func(t *testing.T) {
			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "data.dat"),
				bytes.Repeat([]byte("compressible "), 2048), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}

			cfg := DefaultBackupConfig()
			cfg.BackupDir = t.TempDir()
			cfg.Compression = tc.compression
			cfg.CompressionLevel = LevelDefault

			info, err := CreateBackup(src, "pebble", cfg)
			if err != nil {
				t.Fatalf("CreateBackup: %v", err)
			}
			if !strings.HasSuffix(info.Filename, tc.wantExt) {
				t.Fatalf("filename %q does not end in %q", info.Filename, tc.wantExt)
			}

			f, err := os.Open(info.Path)
			if err != nil {
				t.Fatalf("open archive: %v", err)
			}
			defer f.Close()
			c, err := SniffCodec(f)
			if err != nil {
				t.Fatalf("SniffCodec: %v", err)
			}
			want := tc.compression
			if want == "" {
				want = CompressionGzip
			}
			if c.Name() != want {
				t.Fatalf("archive bytes are %s, config asked for %s", c.Name(), want)
			}
		})
	}
}

// TestCreateBackupRejectsUnknownCompressionWithoutLeavingAFile makes sure a
// typo fails cleanly. A zero-byte archive left behind would be listed as a real
// backup and could be restored from.
func TestCreateBackupRejectsUnknownCompressionWithoutLeavingAFile(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.dat"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dest := t.TempDir()

	cfg := DefaultBackupConfig()
	cfg.BackupDir = dest
	cfg.Compression = "lz4"

	if _, err := CreateBackup(src, "pebble", cfg); err == nil {
		t.Fatal("an unknown compression name must fail the backup")
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if hasArchiveExtension(e.Name()) {
			t.Fatalf("a failed backup left an archive behind: %s", e.Name())
		}
	}
}

// TestRestoreReadsAGzipArchiveWrittenBeforeThisChange is the backward-
// compatibility guard: archives already on disk in production are gzip, and
// they must restore no matter what the current setting says.
func TestRestoreReadsAGzipArchiveWrittenBeforeThisChange(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "legacy.tar.gz")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gw := gzip.NewWriter(f) // written exactly as the old code wrote it
	tw := tar.NewWriter(gw)
	body := []byte("legacy contents")
	if err := tw.WriteHeader(&tar.Header{Name: "pebble/CURRENT", Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	tw.Close()
	gw.Close()
	f.Close()

	target := t.TempDir()
	if err := RestoreBackup(archive, target, false); err != nil {
		t.Fatalf("a pre-existing gzip archive must still restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "pebble", "CURRENT"))
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("restored contents %q != %q", got, body)
	}
}
