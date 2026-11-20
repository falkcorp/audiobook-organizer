// file: core/backup/legacy_bind_internal_test.go
// version: 0.1.0
// guid: 6b2f9e47-1c3a-4d85-9e0f-7a4c2b8d1e63
// last-edited: 2026-11-20

package backup

import (
	"archive/tar"
	"compress/gzip"
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

// tinyLegacy writes a one-directory legacy-abo/0 archive (HEAD's layout) into
// dir, with a sha256sum-format sidecar when sidecar is set, and returns its path
// and the file's SHA-256.
func tinyLegacy(t *testing.T, dir, body string, sidecar bool) (path, sum string) {
	t.Helper()
	path = filepath.Join(dir, "gotham_20261112_020000.tar.gz")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/000001.sst", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tw.Close(), zw.Close(), f.Close()); err != nil {
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
	} else {
		_ = os.Remove(path + ChecksumSidecar)
	}
	return path, sum
}

// TestVerifyVouchesForTheArchiveDigest: verification hands back the archive
// file's SHA-256 it vouched for, the anchor a legacy restore's staging is bound
// to. With a sidecar every level vouches for the sidecar-checked digest; without
// one, a level that scans vouches for the scan's, and a checksum-only level reads
// no digest and vouches for none. A failed verification vouches for nothing.
func TestVerifyVouchesForTheArchiveDigest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &Svc{}
	for _, tc := range []struct {
		name    string
		sidecar bool
		skip    bool
		level   VerifyLevel
		want    bool // the file's digest, else ""
	}{
		{"sidecar/checksum", true, false, VerifyChecksum, true},
		{"sidecar/entries", true, false, VerifyEntries, true},
		{"no-sidecar/entries", false, true, VerifyEntries, true},
		{"no-sidecar/checksum", false, true, VerifyChecksum, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, sum := tinyLegacy(t, dir, "Alfred", tc.sidecar)
			_, got, err := s.verifyWith(ctx, path, "legacy-x", tc.level, tc.skip)
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
	// A sidecar that does not match: refused, and no digest.
	path, _ := tinyLegacy(t, dir, "Alfred", false)
	if err := os.WriteFile(path+ChecksumSidecar, []byte(strings.Repeat("0", 64)+"  x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got, err := s.verifyWith(ctx, path, "legacy-x", VerifyEntries, false); !errors.Is(err, ErrChecksumMismatch) || got != "" {
		t.Fatalf("mismatched sidecar: %q, %v", got, err)
	}
}

// TestVerifyLegacyRefusesAScanOfOtherBytes: the scan must read the bytes the
// sidecar check accepted; an archive replaced between the two is refused.
func TestVerifyLegacyRefusesAScanOfOtherBytes(t *testing.T) {
	ctx := context.Background()
	path, sum := tinyLegacy(t, t.TempDir(), "Alfred", true)
	s := &Svc{}
	got, err := s.verifyLegacy(ctx, path, VerifyEntries, ErrManifest, sum)
	if err != nil || got != sum {
		t.Fatalf("the checked archive: %q, %v", got, err)
	}
	other := strings.Repeat("f", 64)
	if got, err := s.verifyLegacy(ctx, path, VerifyEntries, ErrManifest, other); !errors.Is(err, backuparchive.ErrChanged) || got != "" {
		t.Fatalf("a scan of other bytes than the checked ones: %q, %v; want backuparchive.ErrChanged", got, err)
	}
}

// swapOnErr is a context whose Err rename-replaces the archive once, on its
// second call. checkSumFile reads through ctxReader, which asks Err before
// every Read, so the second call falls inside the sidecar check's copy: after
// that check opened the archive and before ScanLegacy opens the path again.
type swapOnErr struct {
	context.Context
	calls int
	swap  func()
}

func (c *swapOnErr) Err() error {
	c.calls++
	if c.calls == 2 {
		c.swap()
	}
	return c.Context.Err()
}

// TestVerifyBindsTheScanToTheBytesTheSidecarCheckRead: an archive renamed over
// between the sidecar check's read and the legacy scan's read is refused. The
// sidecar check hashed the original through its open descriptor and passed;
// the scan read the replacement, and runVerify hands verifyLegacy the checked
// digest to compare against, so verification fails and vouches for nothing.
func TestVerifyBindsTheScanToTheBytesTheSidecarCheckRead(t *testing.T) {
	path, _ := tinyLegacy(t, t.TempDir(), "Alfred", true)
	other, _ := tinyLegacy(t, t.TempDir(), "Bane", false)
	var swapErr error
	ctx := &swapOnErr{Context: context.Background(), swap: func() { swapErr = os.Rename(other, path) }}
	_, got, err := (&Svc{}).verifyWith(ctx, path, "legacy-x", VerifyEntries, false)
	if swapErr != nil || ctx.calls < 2 {
		t.Fatalf("fixture: the archive was not replaced (%d Err calls): %v", ctx.calls, swapErr)
	}
	if !errors.Is(err, backuparchive.ErrChanged) || got != "" {
		t.Fatalf("an archive replaced between the sidecar check and the scan: %q, %v; want backuparchive.ErrChanged and no digest", got, err)
	}
}

// TestExtractLegacyRefusesAnArchiveItDidNotVerify: staging extracts a legacy
// archive only if it still has the digest the verification vouched for, and
// writes nothing otherwise; with no digest vouched for there is nothing to bind.
func TestExtractLegacyRefusesAnArchiveItDidNotVerify(t *testing.T) {
	ctx := context.Background()
	path, sum := tinyLegacy(t, t.TempDir(), "Alfred", true)
	s := &Svc{o: Options{Disk: oneDisk(1 << 40)}}
	staging := t.TempDir()
	if _, err := s.extractLegacy(ctx, path, staging, strings.Repeat("e", 64)); !errors.Is(err, backuparchive.ErrChanged) {
		t.Fatalf("an archive other than the verified one: %v, want backuparchive.ErrChanged", err)
	}
	if ents, _ := os.ReadDir(staging); len(ents) != 0 {
		t.Fatalf("the refused archive wrote into staging: %v", ents)
	}
	for name, verified := range map[string]string{"verified": sum, "nothing vouched": ""} {
		staging := t.TempDir()
		if _, err := s.extractLegacy(ctx, path, staging, verified); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b, err := os.ReadFile(filepath.Join(staging, "db", "000001.sst")); err != nil || string(b) != "Alfred" {
			t.Fatalf("%s: staged %q, %v", name, b, err)
		}
	}
}

// rogueInspector reports a store whose levels no manifest here records.
type rogueInspector struct{}

func (rogueInspector) Inspect(context.Context, string) (StoreInfo, error) {
	return StoreInfo{Levels: map[string]int{"books": 9}}, nil
}

// TestFailedMediacoreVerifyVouchesForNothing: a mediacore archive whose checksum
// passes but whose entries or store do not match its manifest fails
// verification, and the failure carries no digest, at the entries level and at
// the open level alike.
func TestFailedMediacoreVerifyVouchesForNothing(t *testing.T) {
	ctx := context.Background()
	gz, err := backuparchive.Resolve("gzip")
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := spec(t, gz)
	sp.Entries = sp.Entries[:2] // the store only: db/one, db/two
	// The manifest records another digest for db/two than the bytes archived.
	m := &Manifest{Format: FormatMediacore, ArchiveID: "01JCZ8Y5W2B3C4D5E6F7G8H9JK", App: "gotham", Entries: append([]Entry(nil), sp.Entries...)}
	m.Entries[1].SHA256 = strings.Repeat("b", 64)
	if sp.Manifest, err = MarshalManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeArchive(ctx, sp); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSuffix(sp.Partial, ".partial")
	if err := os.Rename(sp.Partial, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if err := os.WriteFile(path+ChecksumSidecar, []byte(hex.EncodeToString(h[:])+"  a.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Svc{o: Options{Disk: oneDisk(1 << 40), DBPath: filepath.Join(t.TempDir(), "db"), Inspector: rogueInspector{}}}
	if _, got, err := s.verifyWith(ctx, path, "gotham-x", VerifyEntries, false); err == nil || !strings.Contains(err.Error(), "does not match its recorded digest") || got != "" {
		t.Fatalf("entries level over a wrong digest: %q, %v; want the digest refusal and no digest", got, err)
	}
	// The same archive with a true manifest passes the entries level, and the
	// open level fails on the store's levels: again no digest.
	m.Entries[1] = sp.Entries[1]
	sp2, _ := spec(t, gz)
	sp2.Entries = sp2.Entries[:2]
	if sp2.Manifest, err = MarshalManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeArchive(ctx, sp2); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sp2.Partial, path); err != nil {
		t.Fatal(err)
	}
	if b, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	h = sha256.Sum256(b)
	if err := os.WriteFile(path+ChecksumSidecar, []byte(hex.EncodeToString(h[:])+"  a.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got, err := s.verifyWith(ctx, path, "gotham-x", VerifyEntries, false); err != nil || got != hex.EncodeToString(h[:]) {
		t.Fatalf("entries level over a true manifest: %q, %v", got, err)
	}
	if _, got, err := s.verifyWith(ctx, path, "gotham-x", VerifyOpen, false); !errors.Is(err, ErrManifest) || !strings.Contains(err.Error(), "levels") || got != "" {
		t.Fatalf("open level over a mismatched store: %q, %v; want the levels refusal and no digest", got, err)
	}
}
