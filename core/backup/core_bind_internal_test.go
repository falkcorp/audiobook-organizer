// file: core/backup/core_bind_internal_test.go
// version: 0.1.0
// guid: 0e7c3b58-91a4-4f26-8d1b-5c2a6e9f4b17
// last-edited: 2026-11-20

package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// tinyCore writes a small mediacore-backup/1 archive whose manifest carries id
// into dir, with a sha256sum-format sidecar when sidecar is set, and returns
// its path and the file's SHA-256. Two IDs give two archives of other bytes.
func tinyCore(t *testing.T, dir string, id ArchiveID, sidecar bool) (path, sum string) {
	t.Helper()
	gz, err := backuparchive.Resolve("gzip")
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := spec(t, gz)
	m := &Manifest{Format: FormatMediacore, ArchiveID: id, App: "gotham", Entries: append([]Entry(nil), sp.Entries...)}
	if sp.Manifest, err = MarshalManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeArchive(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "gotham_"+string(id)+".tar.gz")
	if err := os.Rename(sp.Partial, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	sum = hex.EncodeToString(h[:])
	if sidecar {
		if err := os.WriteFile(path+ChecksumSidecar, []byte(sum+"  "+filepath.Base(path)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path, sum
}

// TestVerifyVouchesForTheEntriesPassOfAMediacoreArchive: the entries level of a
// mediacore-backup/1 archive vouches for the digest of the bytes its entries
// pass read, with a sidecar and without one. Without that digest, a restore of
// a foreign archive that has no sidecar would have nothing to bind its staging
// to (StageRestore). A checksum-only level with no sidecar reads nothing and
// vouches for nothing.
func TestVerifyVouchesForTheEntriesPassOfAMediacoreArchive(t *testing.T) {
	ctx := context.Background()
	s := &Svc{}
	for _, tc := range []struct {
		name    string
		sidecar bool
		level   VerifyLevel
		want    bool // the file's digest, else ""
	}{
		{"sidecar/entries", true, VerifyEntries, true},
		{"no-sidecar/entries", false, VerifyEntries, true},
		{"no-sidecar/checksum", false, VerifyChecksum, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, sum := tinyCore(t, t.TempDir(), "01JCZ8Y5W2B3C4D5E6F7G8H9JK", tc.sidecar)
			_, got, err := s.verifyWith(ctx, path, "gotham-x", tc.level, !tc.sidecar)
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.want {
				want = sum
			}
			if got != want {
				t.Fatalf("vouched for %q, want %q", got, want)
			}
		})
	}
}

// TestVerifyBindsTheEntriesPassToTheBytesTheSidecarCheckRead: a mediacore
// archive renamed over between the sidecar check's read and the entries pass's
// read is refused. The sidecar check hashed the original through its open
// descriptor and passed; the entries pass read the replacement, a whole sound
// archive of its own, so only the comparison of the two digests catches it, and
// the verification then vouches for nothing.
func TestVerifyBindsTheEntriesPassToTheBytesTheSidecarCheckRead(t *testing.T) {
	path, _ := tinyCore(t, t.TempDir(), "01JCZ8Y5W2B3C4D5E6F7G8H9JK", true)
	other, _ := tinyCore(t, t.TempDir(), "01JCZ8Y5W2B3C4D5E6F7G8H9JM", false)
	var swapErr error
	ctx := &swapOnErr{Context: context.Background(), swap: func() { swapErr = os.Rename(other, path) }}
	_, got, err := (&Svc{}).verifyWith(ctx, path, "gotham-x", VerifyEntries, false)
	if swapErr != nil || ctx.calls < 2 {
		t.Fatalf("fixture: the archive was not replaced (%d Err calls): %v", ctx.calls, swapErr)
	}
	if !errors.Is(err, backuparchive.ErrChanged) || !errors.Is(err, ErrChecksumMismatch) || got != "" {
		t.Fatalf("an archive replaced between the sidecar check and the entries pass: %q, %v; want backuparchive.ErrChanged and no digest", got, err)
	}
}

// TestFileDigestNeedsTheWholeArchive: an opened archive gives no digest until
// consume has read it to its end, because a prefix's digest is not the file's.
func TestFileDigestNeedsTheWholeArchive(t *testing.T) {
	path, sum := tinyCore(t, t.TempDir(), "01JCZ8Y5W2B3C4D5E6F7G8H9JK", false)
	a, err := openArchive(path, backuparchive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if got, err := a.fileSHA256(); err == nil || got != "" {
		t.Fatalf("a digest before the archive was read: %q, %v", got, err)
	}
	if err := a.consume(context.Background(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := a.fileSHA256(); err != nil || got != sum {
		t.Fatalf("digest after consume = %q, %v; want the file's %s", got, err, sum)
	}
}
