// file: core/internal/backuparchive/reader_test.go
// version: 1.0.0
// guid: 74c0a451-3c59-4bed-9f7c-df423afd22fa
// last-edited: 2026-11-12

package backuparchive_test

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

func TestCodecsRoundTripAndSniff(t *testing.T) {
	for _, codec := range []string{backuparchive.Gzip, backuparchive.Zstd, backuparchive.None} {
		t.Run(codec, func(t *testing.T) {
			p := buildTar(t, codec, ent{name: "db/MANIFEST-000001", body: "the Batcave"}, ent{name: "config/config.yaml", body: "x"})
			names, err := readAll(t, p, backuparchive.Limits{}, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != 2 || names[0] != "db/MANIFEST-000001" {
				t.Fatalf("entries = %v", names)
			}
			// The extension is a hint: the magic bytes decide.
			renamed := filepath.Join(filepath.Dir(p), "renamed.bin")
			if err := os.Rename(p, renamed); err != nil {
				t.Fatal(err)
			}
			if _, err := readAll(t, renamed, backuparchive.Limits{}, false); err != nil {
				t.Fatalf("a renamed %s archive does not read: %v", codec, err)
			}
		})
	}
}

func TestResolveRefusesUnknownAndEmptyCodec(t *testing.T) {
	for _, name := range []string{"", "lz4", "age"} {
		if _, err := backuparchive.Resolve(name); !errors.Is(err, backuparchive.ErrUnknownCodec) {
			t.Errorf("Resolve(%q) = %v, want ErrUnknownCodec", name, err)
		}
	}
	gz, _ := backuparchive.Resolve("gzip")
	if err := backuparchive.ValidateLevel(gz, 42); err == nil {
		t.Error("gzip level 42 was accepted")
	}
	if err := backuparchive.ValidateLevel(gz, backuparchive.LevelDefault); err != nil {
		t.Errorf("the default level was refused: %v", err)
	}
}

func TestReaderRefusesUnsafeEntries(t *testing.T) {
	cases := []struct {
		name string
		e    ent
		want error
	}{
		{"absolute", ent{name: "/etc/passwd", body: "x"}, backuparchive.ErrEntryName},
		{"dotdot", ent{name: "db/../../escape", body: "x"}, backuparchive.ErrEntryName},
		{"leading dotdot", ent{name: "../escape", body: "x"}, backuparchive.ErrEntryName},
		{"doubled slash", ent{name: "db//f", body: "x"}, backuparchive.ErrEntryName},
		{"backslash", ent{name: `db\f`, body: "x"}, backuparchive.ErrEntryName},
		{"dot", ent{name: "./f", body: "x"}, backuparchive.ErrEntryName},
		{"too long", ent{name: strings.Repeat("a", backuparchive.MaxNameBytes+1), body: "x"}, backuparchive.ErrEntryName},
		{"symlink", ent{name: "db/link", typ: tar.TypeSymlink, link: "/etc/passwd"}, backuparchive.ErrEntryType},
		{"hardlink", ent{name: "db/hard", typ: tar.TypeLink, link: "db/other"}, backuparchive.ErrEntryType},
		{"fifo", ent{name: "db/pipe", typ: tar.TypeFifo}, backuparchive.ErrEntryType},
		{"char device", ent{name: "db/tty", typ: tar.TypeChar}, backuparchive.ErrEntryType},
		{"directory in a plain archive", ent{name: "db", typ: tar.TypeDir}, backuparchive.ErrEntryType},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := buildTar(t, backuparchive.Gzip, ent{name: "ok", body: "fine"}, c.e)
			if _, err := readAll(t, p, backuparchive.Limits{}, false); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestReaderRefusesDuplicateAndOversize(t *testing.T) {
	dup := buildTar(t, backuparchive.None, ent{name: "db/a", body: "1"}, ent{name: "db/a", body: "2"})
	if _, err := readAll(t, dup, backuparchive.Limits{}, false); !errors.Is(err, backuparchive.ErrDuplicateEntry) {
		t.Errorf("duplicate: %v", err)
	}
	many := buildTar(t, backuparchive.None, ent{name: "a", body: "1"}, ent{name: "b", body: "1"}, ent{name: "c", body: "1"})
	if _, err := readAll(t, many, backuparchive.Limits{MaxEntries: 2}, false); !errors.Is(err, backuparchive.ErrLimit) {
		t.Errorf("entry count: %v", err)
	}
	big := buildTar(t, backuparchive.None, ent{name: "a", body: strings.Repeat("x", 100)})
	if _, err := readAll(t, big, backuparchive.Limits{MaxEntryBytes: 99}, false); !errors.Is(err, backuparchive.ErrLimit) {
		t.Errorf("entry size: %v", err)
	}
	sum := buildTar(t, backuparchive.None, ent{name: "a", body: strings.Repeat("x", 60)}, ent{name: "b", body: strings.Repeat("y", 60)})
	if _, err := readAll(t, sum, backuparchive.Limits{MaxTotalBytes: 100}, false); !errors.Is(err, backuparchive.ErrLimit) {
		t.Errorf("total size: %v", err)
	}
	// The archive's own size is checked before it is read.
	if _, err := readAll(t, sum, backuparchive.Limits{MaxArchiveBytes: 10}, false); err != nil {
		// readAll opens with no cap; the cap is OpenRegular's.
		t.Fatal(err)
	}
	if _, _, err := backuparchive.OpenRegular(sum, 10); !errors.Is(err, backuparchive.ErrLimit) {
		t.Errorf("archive size: %v", err)
	}
}

func TestReaderBodyNeverExceedsItsHeader(t *testing.T) {
	// A header that lies about its size (tar.Writer refuses to write more than
	// the header says, so the lie is a short body): the read ends in an error,
	// never in a longer entry.
	p := buildTar(t, backuparchive.None, ent{name: "db/a", body: "short"})
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the header and three bytes of the five-byte body.
	if err := os.WriteFile(p, raw[:515], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, p, backuparchive.Limits{}, false); err == nil {
		t.Fatal("a truncated archive read to the end without an error")
	}
}

func TestReaderRefusesTrailingData(t *testing.T) {
	p := buildTar(t, backuparchive.None, ent{name: "db/a", body: "x"})
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("planted bytes after the end of the archive"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, p, backuparchive.Limits{}, false); !errors.Is(err, backuparchive.ErrTrailingData) {
		t.Fatalf("trailing data: %v", err)
	}
}

func TestOpenRegularRefusesLinksAndDirectories(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	for _, p := range []string{link, dir} {
		if f, _, err := backuparchive.OpenRegular(p, 0); !errors.Is(err, backuparchive.ErrNotRegularFile) {
			if f != nil {
				f.Close()
			}
			t.Errorf("OpenRegular(%s) = %v, want ErrNotRegularFile", filepath.Base(p), err)
		}
	}
	f, _, err := backuparchive.OpenRegular(real, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
