// file: core/backup/verify_test.go
// version: 1.0.0
// guid: 8d3e5a17-6c20-4f9b-b4a8-1f7c0d2e9a55
// last-edited: 2026-11-12

package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/backup"
)

const joker = "THE-JOKER-WAS-HERE-0123456789"

// withJokerArtifact adds a one-file artifact the tests can find in the tar and
// edit without disturbing a Pebble file.
func (e *env) withJokerArtifact() {
	e.t.Helper()
	dir := e.t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scene.txt"), []byte(joker), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.opts.Artifacts = func() []backup.Artifact {
		return []backup.Artifact{{Name: "books.scenes", Owner: "books", Class: backup.SystemOfRecord, Consistency: backup.AppendOnly, Dir: dir}}
	}
	e.rebuild()
}

// refreshSum rewrites the archive's .sha256 sidecar for its current bytes, so a
// tamper is invisible to the checksum level and only the manifest can see it.
func refreshSum(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if err := os.WriteFile(path+backup.ChecksumSidecar, []byte(hex.EncodeToString(h[:])+"  x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManifestMatchesEntries(t *testing.T) {
	e := newEnv(t)
	e.settings.Compression, e.settings.VerifyAfterCreate = "none", false
	e.withJokerArtifact()
	a := e.create("manual")
	ctx := context.Background()
	for _, l := range []backup.VerifyLevel{backup.VerifyChecksum, backup.VerifyEntries, backup.VerifyOpen} {
		if r, err := e.svc.Verify(ctx, a.ID, l); err != nil || !r.OK {
			t.Fatalf("untouched archive at %s: %+v, %v", l, r, err)
		}
	}
	p := e.path(a)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(raw, []byte(joker))
	if i < 0 {
		t.Fatal("the artifact's bytes are not in the plain tar")
	}
	raw[i+3] ^= 0x01 // one flipped bit inside an entry
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Without fixing the sidecar, the outer checksum catches it.
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyChecksum); !errors.Is(err, backup.ErrChecksumMismatch) {
		t.Fatalf("checksum level: %v, want ErrChecksumMismatch", err)
	}
	// With the sidecar fixed up, only the manifest's entry hash can.
	refreshSum(t, p)
	if r, err := e.svc.Verify(ctx, a.ID, backup.VerifyChecksum); err != nil || !r.OK {
		t.Fatalf("checksum level after the sidecar was fixed: %+v, %v", r, err)
	}
	for _, l := range []backup.VerifyLevel{backup.VerifyEntries, backup.VerifyOpen} {
		r, err := e.svc.Verify(ctx, a.ID, l)
		if !errors.Is(err, backup.ErrManifest) || r.OK {
			t.Fatalf("%s: %+v, %v; want ErrManifest and a failed result", l, r, err)
		}
	}
	// The failure is recorded in the sidecar, so the catalogue shows it.
	list, _ := e.svc.List(ctx)
	if list[0].Verified == nil || list[0].Verified.OK {
		t.Fatalf("the failed verification was not recorded: %+v", list[0].Verified)
	}
}

func TestVerifyDetectsAMissingChecksumAndASwappedManifest(t *testing.T) {
	e := newEnv(t)
	e.settings.VerifyAfterCreate = false
	a := e.create("manual")
	b := e.create("manual")
	ctx := context.Background()
	p := e.path(a)
	// A manifest sidecar of another archive: the in-tar manifest disagrees.
	other, err := os.ReadFile(e.path(b) + backup.ManifestSidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+backup.ManifestSidecar, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyEntries); !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("a swapped sidecar manifest: %v, want ErrManifest", err)
	}
	if err := os.Remove(p + backup.ChecksumSidecar); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyChecksum); !errors.Is(err, backup.ErrNoChecksum) {
		t.Fatalf("a missing checksum sidecar: %v, want ErrNoChecksum", err)
	}
	if _, err := e.svc.Verify(ctx, "01ZZZZZZZZZZZZZZZZZZZZZZZZ", backup.VerifyChecksum); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("an unknown ID: %v", err)
	}
	if _, err := e.svc.Verify(ctx, "../etc/passwd", backup.VerifyChecksum); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("a path as an ID: %v", err)
	}
	if _, err := e.svc.Verify(ctx, b.ID, backup.VerifyLevel(9)); err == nil {
		t.Fatal("an unknown level was accepted")
	}
}

// failingInspector reports counts that differ from the manifest's.
type failingInspector struct {
	backup.Inspector
	extra int64
}

func (f failingInspector) Inspect(ctx context.Context, dir string) (backup.StoreInfo, error) {
	info, err := f.Inspector.Inspect(ctx, dir)
	for k := range info.Keys {
		info.Keys[k] += f.extra
	}
	return info, err
}

func TestVerifyOpenCountsKeys(t *testing.T) {
	e := newEnv(t)
	e.settings.VerifyAfterCreate = false
	a := e.create("manual")
	ctx := context.Background()
	if n := a.Manifest.Keyspaces[string(gothamOwner)].Keys; n != 5 {
		t.Fatalf("manifest counts %d keys for %s, want the 5 the fixture wrote", n, gothamOwner)
	}
	if r, err := e.svc.Verify(ctx, a.ID, backup.VerifyOpen); err != nil || !r.OK {
		t.Fatalf("the checkpoint read back with other counts: %+v, %v", r, err)
	}
	// A store that reads back with another count is not the one that was archived.
	real := e.opts.Inspector
	e.opts.Inspector = failingInspector{Inspector: real, extra: 1}
	e.rebuild()
	if r, err := e.svc.Verify(ctx, a.ID, backup.VerifyOpen); !errors.Is(err, backup.ErrManifest) || r.OK {
		t.Fatalf("a count that differs from the manifest passed: %+v, %v", r, err)
	}
	// An inspector that cannot count (no live admin) still checks that the store opens.
	e.opts.Inspector = &backup.StorageInspector{Base: storageBase(e)}
	e.rebuild()
	if r, err := e.svc.Verify(ctx, a.ID, backup.VerifyOpen); err != nil || !r.OK {
		t.Fatalf("open-only inspector: %+v, %v", r, err)
	}
}
