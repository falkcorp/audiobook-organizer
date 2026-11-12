// file: core/backup/verify_test.go
// version: 1.0.1
// guid: 8d3e5a17-6c20-4f9b-b4a8-1f7c0d2e9a55
// last-edited: 2026-11-12

package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
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

// A failed verification is shown as a code from a closed set; its error text
// (host paths, internal errors) stays in the sidecar and the operator's log.
func TestVerifyFailureCarriesAReasonCode(t *testing.T) {
	e := newEnv(t)
	e.settings.Compression, e.settings.VerifyAfterCreate = "none", false
	var logs bytes.Buffer
	e.opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	e.withJokerArtifact()
	ctx := context.Background()
	readSidecar := func(a backup.Archive) string {
		t.Helper()
		b, err := os.ReadFile(e.path(a) + backup.VerifySidecar)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// A pass has no reason, in the result and in the sidecar.
	ok := e.create("manual")
	r, err := e.svc.Verify(ctx, ok.ID, backup.VerifyEntries)
	if err != nil || !r.OK || r.Reason != backup.VerifyReasonNone || strings.Contains(readSidecar(ok), `"reason"`) {
		t.Fatalf("a good archive: %+v, %v, sidecar %s", r, err, readSidecar(ok))
	}
	if strings.Contains(logs.String(), "failed verification") {
		t.Errorf("a pass was logged as a failure: %s", logs.String())
	}

	// One flipped bit: the outer checksum, then (sidecar fixed up) the manifest.
	a := e.create("manual")
	p := e.path(a)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(raw, []byte(joker))
	if i < 0 {
		t.Fatal("the artifact's bytes are not in the plain tar")
	}
	raw[i+3] ^= 0x01
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = e.svc.Verify(ctx, a.ID, backup.VerifyChecksum)
	if !errors.Is(err, backup.ErrChecksumMismatch) || r.Reason != backup.VerifyReasonChecksumMismatch {
		t.Fatalf("checksum level: %+v, %v", r, err)
	}
	refreshSum(t, p)
	r, err = e.svc.Verify(ctx, a.ID, backup.VerifyEntries)
	if !errors.Is(err, backup.ErrManifest) || r.Reason != backup.VerifyReasonManifestMismatch {
		t.Fatalf("entries level: %+v, %v", r, err)
	}
	if r.Detail == "" {
		t.Error("the operator's Detail was dropped from the result")
	}
	// The catalogue shows the code; the sidecar keeps both.
	list, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var listed *backup.VerifyResult
	for _, x := range list {
		if x.ID == a.ID {
			listed = x.Verified
		}
	}
	if listed == nil || listed.OK || listed.Reason != backup.VerifyReasonManifestMismatch {
		t.Fatalf("listed verification = %+v", listed)
	}
	if side := readSidecar(a); !strings.Contains(side, `"reason":"manifest_mismatch"`) || !strings.Contains(side, `"detail":"`) {
		t.Errorf("sidecar = %s", side)
	}
	// The error text went to the operator's log, with the code.
	out := logs.String()
	if !strings.Contains(out, "an archive failed verification") || !strings.Contains(out, "reason=manifest_mismatch") ||
		!strings.Contains(out, "does not match its recorded digest") {
		t.Errorf("the failure was not logged with its reason and text: %s", out)
	}

	// A missing checksum sidecar.
	if err := os.Remove(p + backup.ChecksumSidecar); err != nil {
		t.Fatal(err)
	}
	if r, err = e.svc.Verify(ctx, a.ID, backup.VerifyChecksum); !errors.Is(err, backup.ErrNoChecksum) || r.Reason != backup.VerifyReasonNoChecksum {
		t.Fatalf("no checksum: %+v, %v", r, err)
	}

	// A tar cut off inside an entry (the sidecar fixed up, so only the stream can say).
	cut := e.create("manual")
	cp := e.path(cut)
	raw, err = os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	i = bytes.Index(raw, []byte(joker))
	if i < 0 {
		t.Fatal("the artifact's bytes are not in the plain tar")
	}
	if err := os.WriteFile(cp, raw[:i+3], 0o600); err != nil {
		t.Fatal(err)
	}
	refreshSum(t, cp)
	if r, err = e.svc.Verify(ctx, cut.ID, backup.VerifyEntries); err == nil || r.Reason != backup.VerifyReasonMalformed {
		t.Fatalf("a truncated tar: %+v, %v", r, err)
	}

	// An archive past a limit the build was started with.
	e.opts.Limits = backuparchive.Limits{MaxEntries: 1}
	e.rebuild()
	if r, err = e.svc.Verify(ctx, ok.ID, backup.VerifyEntries); !errors.Is(err, backuparchive.ErrLimit) || r.Reason != backup.VerifyReasonLimitExceeded {
		t.Fatalf("over the entry limit: %+v, %v", r, err)
	}
}

// The sidecar lives where an operator or a network share can write, so what it
// says about a reason is read as untrusted: only a code this build knows
// comes back, and a pass has none.
func TestVerifySidecarReasonIsReadAsUntrusted(t *testing.T) {
	e := newEnv(t)
	e.settings.VerifyAfterCreate = false
	a := e.create("manual")
	ctx := context.Background()
	side := e.path(a) + backup.VerifySidecar
	for _, c := range []struct {
		name, body string
		ok         bool
		want       backup.VerifyReason
	}{
		{"known code", `{"at":"2026-11-12T01:00:00Z","level":"entries","ok":false,"detail":"x","reason":"limit_exceeded"}`, false, backup.VerifyReasonLimitExceeded},
		{"a sidecar from before reason codes", `{"at":"2026-11-12T01:00:00Z","level":"entries","ok":false,"detail":"/srv/gotham: permission denied"}`, false, backup.VerifyReasonFailed},
		{"made-up code", `{"at":"2026-11-12T01:00:00Z","level":"entries","ok":false,"detail":"x","reason":"/srv/gotham: permission denied"}`, false, backup.VerifyReasonFailed},
		{"a code on a pass", `{"at":"2026-11-12T01:00:00Z","level":"entries","ok":true,"detail":"","reason":"checksum_mismatch"}`, true, backup.VerifyReasonNone},
	} {
		if err := os.WriteFile(side, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		list, err := e.svc.List(ctx)
		if err != nil || len(list) != 1 || list[0].Verified == nil {
			t.Fatalf("%s: %v %+v", c.name, err, list)
		}
		if v := list[0].Verified; v.OK != c.ok || v.Reason != c.want {
			t.Errorf("%s: read as %+v, want ok=%v reason %q", c.name, v, c.ok, c.want)
		}
	}
}
