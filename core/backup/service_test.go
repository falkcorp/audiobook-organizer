// file: core/backup/service_test.go
// version: 1.0.1
// guid: b2c9d4e6-5a17-4e38-8f20-3d6b1a9c7e45
// last-edited: 2026-11-12

package backup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
)

func TestRewrapBeforeDeriveKeyRetired(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.keys.bruceKeys = newBruceKeys("d1", "d1", "d2")
	e.opts.Keyring, e.opts.Inspector = e.keys, &backup.StorageInspector{Base: storageBase(e), Admin: e.st.DB().Admin(), Secrets: e.keys, SecretsOwner: gothamOwner}
	e.rebuild()
	old := e.create("manual")
	if old.Manifest.Provenance.KeyID != "d1" {
		t.Fatalf("created under %q", old.Manifest.Provenance.KeyID)
	}
	// Already under the active key: returned as it is, nothing written.
	same, err := e.svc.Rewrap(ctx, old.ID)
	if err != nil || same.ID != old.ID {
		t.Fatalf("Rewrap under the active key = %+v, %v", same, err)
	}
	// The derivation key moves on; d1 is now in the verify state.
	e.keys.active = "d2"
	re, err := e.svc.Rewrap(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if re.ID == old.ID || re.Manifest.Provenance.KeyID != "d2" || re.Provenance != backup.Own || re.Verified == nil || !re.Verified.OK {
		t.Fatalf("rewrapped = %+v", re)
	}
	list, _ := e.svc.List(ctx)
	if len(list) != 1 || list[0].ID != re.ID {
		t.Fatalf("catalogue = %d archives; the original must go once the rewrap is the newest verified", len(list))
	}
	if len(re.Manifest.Entries) != len(old.Manifest.Entries) {
		t.Fatalf("the rewrap changed the entry set")
	}
	// d1 retires: the rewrapped archive is still ours, an un-rewrapped one is not.
	e.keys.active = "d1"
	late := e.create("manual") // under d1
	e.keys.active = "d2"
	delete(e.keys.keys, "d1")
	list, _ = e.svc.List(ctx)
	got := map[backup.ArchiveID]backup.ProvenanceClass{}
	for _, a := range list {
		got[a.ID] = a.Provenance
	}
	if got[re.ID] != backup.Own || got[late.ID] != backup.Foreign {
		t.Fatalf("after d1 retired: %v (rewrapped %s, late %s)", got, re.ID, late.ID)
	}
	if _, err := e.svc.Rewrap(ctx, late.ID); !errors.Is(err, backup.ErrForeignArchive) {
		t.Fatalf("Rewrap of an archive that is no longer Own: %v", err)
	}
}

func TestRewrapKeepsAFloorProtectedOriginal(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.keys.bruceKeys = newBruceKeys("d1", "d1", "d2")
	e.rebuild()
	old := e.create("manual")
	if _, err := e.svc.StageRestore(ctx, old.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	e.keys.active = "d2"
	re, err := e.svc.Rewrap(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := e.svc.List(ctx)
	if len(list) != 2 {
		t.Fatalf("catalogue = %d archives, want the original kept for the staged restore plus the rewrap (%s)", len(list), re.ID)
	}
}

func TestEnsure(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a, created, err := e.svc.Ensure(ctx, time.Hour, "pre:books.organize")
	if err != nil || !created || a.Manifest.Reason != "pre:books.organize" {
		t.Fatalf("first Ensure = %+v, %v, %v", a.Manifest, created, err)
	}
	b, created, err := e.svc.Ensure(ctx, time.Hour, "pre:books.organize")
	if err != nil || created || b.ID != a.ID {
		t.Fatalf("second Ensure = %s created=%v err=%v, want the first archive", b.ID, created, err)
	}
	// Too old for the caller: a new one.
	c, created, err := e.svc.Ensure(ctx, time.Second, "pre:books.organize")
	if err != nil || !created || c.ID == a.ID {
		t.Fatalf("Ensure with a short maxAge = %s created=%v err=%v", c.ID, created, err)
	}
	// An archive nobody verified is not a safety net.
	e2 := newEnv(t)
	e2.settings.VerifyAfterCreate = false
	u := e2.create("manual")
	v, created, err := e2.svc.Ensure(ctx, time.Hour, "pre:books.organize")
	if err != nil || !created || v.ID == u.ID {
		t.Fatalf("Ensure over an unverified archive = %s created=%v err=%v", v.ID, created, err)
	}
	// A failing Create is reported, with created false.
	e3 := newEnv(t)
	e3.settings.Compression = "lz4"
	if _, created, err := e3.svc.Ensure(ctx, time.Hour, "x"); err == nil || created {
		t.Fatalf("Ensure with a broken configuration = created=%v err=%v", created, err)
	}
}

func TestImportRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	src := newEnv(t)
	a := src.create("manual")
	good, err := os.ReadFile(src.path(a))
	if err != nil {
		t.Fatal(err)
	}
	leftovers := func() []string {
		var out []string
		for _, n := range e.names() {
			if strings.Contains(n, ".partial") || strings.Contains(n, "import-") {
				out = append(out, n)
			}
		}
		return out
	}
	if _, err := e.svc.Import(ctx, "", strings.NewReader("this is not an archive")); err == nil {
		t.Fatal("garbage was imported")
	}
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)/2] ^= 0x55
	if _, err := e.svc.Import(ctx, "", bytes.NewReader(flipped)); err == nil {
		t.Fatal("a corrupted archive was imported")
	}
	if l := leftovers(); len(l) != 0 {
		t.Fatalf("refused imports left %v", l)
	}
	// A path-like or foreign-pattern name hint is never used either.
	for _, hint := range []string{"../escape.tar.gz", "/etc/gotham_20261112_000000.tar.gz", "other_20261112_000000.tar.gz", "gotham_x.tar.gz"} {
		imp, err := e.svc.Import(ctx, hint, bytes.NewReader(good))
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if strings.ContainsAny(imp.Name, `/\`) || !strings.HasPrefix(imp.Name, "gotham_") || imp.Name == hint {
			t.Fatalf("hint %q produced the name %q", hint, imp.Name)
		}
		if _, err := os.Stat(filepath.Join(e.dbParent, "escape.tar.gz")); err == nil {
			t.Fatal("an import escaped the backup directory")
		}
		if err := e.svc.Delete(ctx, imp.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Not even a conformant, free hint names the stored file (the name is the
	// service's, never text that arrived with the request); the same archive
	// twice is refused.
	const conformant = "gotham_20250101_000000.tar.gz"
	imp, err := e.svc.Import(ctx, conformant, bytes.NewReader(good))
	if err != nil || imp.Name == conformant || !strings.HasPrefix(imp.Name, "gotham_") {
		t.Fatalf("Import = %+v, %v; the hint must not name the stored file", imp, err)
	}
	if _, err := os.Stat(filepath.Join(e.backupDir, conformant)); err == nil {
		t.Fatal("a conformant hint named a stored file")
	}
	if _, err := e.svc.Import(ctx, "", bytes.NewReader(good)); !errors.Is(err, backup.ErrManifest) {
		t.Fatalf("a second import of the same archive: %v, want ErrManifest (duplicate ID)", err)
	}
	if l := leftovers(); len(l) != 0 {
		t.Fatalf("left %v", l)
	}
	info, err := os.Stat(filepath.Join(e.backupDir, imp.Name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("imported archive = %v, %v; want 0600", info, err)
	}
	// An upload over the limit is refused without being stored.
	e.opts.Limits = backuparchive.Limits{MaxArchiveBytes: int64(len(good) - 1)}
	e.rebuild()
	if _, err := e.svc.Import(ctx, "", bytes.NewReader(good)); !errors.Is(err, backuparchive.ErrLimit) {
		t.Fatalf("an oversize upload: %v, want ErrLimit", err)
	}
	if l := leftovers(); len(l) != 0 {
		t.Fatalf("left %v", l)
	}
}

func TestVerifyEnforcesEntryAndSizeLimits(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	n := len(a.Manifest.Entries)
	for name, lim := range map[string]backuparchive.Limits{
		"entry count": {MaxEntries: n - 1},
		"entry size":  {MaxEntryBytes: 8},
		"total size":  {MaxTotalBytes: 16},
	} {
		e.opts.Limits = lim
		e.rebuild()
		if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyEntries); err == nil {
			t.Errorf("%s: an archive over the limit verified", name)
		}
	}
}

func TestHashPoolSizesAllProduceVerifiableArchives(t *testing.T) {
	ctx := context.Background()
	for _, workers := range []int{1, 2, 8} {
		e := newEnv(t)
		e.settings.HashWorkers = workers
		a, err := e.svc.Create(ctx, backup.CreateOptions{})
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		if len(a.Manifest.Entries) == 0 {
			t.Fatalf("workers=%d: no entries", workers)
		}
		for _, en := range a.Manifest.Entries {
			if len(en.SHA256) != 64 || en.Size < 0 {
				t.Fatalf("workers=%d: entry %+v", workers, en)
			}
		}
		// Verification re-hashes with the same pool: every digest the writer
		// recorded must be the one the reader computes.
		if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyEntries); err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
	}
}
