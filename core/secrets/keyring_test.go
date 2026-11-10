// file: core/secrets/keyring_test.go
// version: 1.0.0
// guid: 211fe806-6c88-41f7-8232-e85cc488f922
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// exportTo exports e's keyring to a scratch file, fatal on error.
func exportTo(t *testing.T, e *env) {
	t.Helper()
	if _, err := ExportKeyring(e.root, "", filepath.Join(t.TempDir(), "keyring.export"), e.clock.now()); err != nil {
		t.Fatal(err)
	}
}

// b64 is s in standard base64, the keyring file's encoding of material.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// metaOf decodes e's c/secrets/meta.
func metaOf(t *testing.T, e *env) *storeMeta {
	t.Helper()
	raw, err := e.ks.Get(context.Background(), metaKey())
	if err != nil {
		t.Fatal(err)
	}
	m, err := decodeStoreMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestExportRecord_VouchesOnlyForItsMaterial (a test name coined by F3-03;
// review probe A): an export record names each key by ID and material
// digest, never the material. After the keyring is lost and a new one is
// made, the old record does not vouch for it: the new keys are unexported
// (and numbered past the old ones), and a record naming the right IDs with
// other digests covers nothing.
func TestExportRecord_VouchesOnlyForItsMaterial(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	material := []string{string(s.ring.data.Material), string(s.ring.derive.Material)}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	exportTo(t, e)
	rec := fileBytes(t, filepath.Join(e.dir, "secrets.keyring.exported"))
	for _, m := range material {
		if bytes.Contains(rec, []byte(m)) || strings.Contains(string(rec), b64(m)) {
			t.Fatal("the export record holds key material")
		}
	}
	r, err := readExportRecord(e.root, "secrets.keyring.exported")
	if err != nil || len(r.Keys) != 2 || r.Keys[0].ID != "d1" || r.Keys[1].ID != "k1" || len(r.Keys[0].SHA256) != 64 {
		t.Fatalf("export record = %+v, %v; want d1 and k1 with SHA-256 digests", r, err)
	}

	if err := os.Remove(e.keyringPath()); err != nil {
		t.Fatal(err)
	}
	s2 := e.open(t, nil)
	if ids := strings.Join(s2.KeyIDs(), ","); ids != "k2,d2" {
		t.Errorf("the new keyring's IDs are %s, want k2,d2 (past the exported k1 and d1)", ids)
	}
	missing, err := s2.UnexportedKeys()
	if err != nil || !slices.Equal(missing, []string{"k2", "d2"}) {
		t.Fatalf("UnexportedKeys after the keyring was replaced = %v, %v; want k2 and d2", missing, err)
	}
	if !strings.Contains(e.logs.String(), "the keyring was missing") {
		t.Error("replacing a keyring that existed was not logged as a loss")
	}

	// A record with the new keys' IDs but other digests covers nothing.
	forged := []byte(`{"format":"mediacore-keyring-export/1","keys":[` +
		`{"id":"d2","sha256":"` + strings.Repeat("0", 64) + `"},` +
		`{"id":"k2","sha256":"` + strings.Repeat("0", 64) + `"}],"at":"2039-04-01T22:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(e.dir, "secrets.keyring.exported"), forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if missing, err := s2.UnexportedKeys(); err != nil || len(missing) != 2 {
		t.Fatalf("UnexportedKeys with matching IDs and other digests = %v, %v; want both keys", missing, err)
	}
	exportTo(t, e)
	if missing, err := s2.UnexportedKeys(); err != nil || len(missing) != 0 {
		t.Fatalf("UnexportedKeys after exporting the new keyring = %v, %v", missing, err)
	}
}

// TestOpen_NumbersPastKnownKeys (a test name coined by F3-03; review probe
// B): a regenerated keyring never gives an old key's ID to new material. It
// numbers past the export record (rotate-derive added d2, the export holds
// it, no boot saw it) and past c/secrets/meta's max_key_id, which every Open
// raises to its keyring's highest key number (here with no export at all).
func TestOpen_NumbersPastKnownKeys(t *testing.T) {
	regen := func(o *Options) { o.AllowKeyRegen = true }

	// Through the export record.
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rotate(t, e)
	exportTo(t, e)
	if metaOf(t, e).MaxKeyID != 1 {
		t.Fatal("c/secrets/meta moved without an Open")
	}
	if err := os.Remove(e.keyringPath()); err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(e.open(t, regen).KeyIDs(), ","); ids != "k3,d3" {
		t.Errorf("regenerated after an exported d2: %s, want k3,d3", ids)
	}
	if metaOf(t, e).MaxKeyID != 3 {
		t.Errorf("c/secrets/meta max_key_id = %d after the regen, want 3", metaOf(t, e).MaxKeyID)
	}

	// Through c/secrets/meta: a boot saw d2, nothing was exported.
	m := newEnv(t)
	ms := m.open(t, nil)
	mustPut(t, ms, nameGate, valueVengeance)
	if err := ms.Close(); err != nil {
		t.Fatal(err)
	}
	rotate(t, m)
	if err := m.open(t, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if got := metaOf(t, m).MaxKeyID; got != 2 {
		t.Fatalf("c/secrets/meta max_key_id after a boot with d2 = %d, want 2", got)
	}
	if err := os.Remove(m.keyringPath()); err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(m.open(t, regen).KeyIDs(), ","); ids != "k3,d3" {
		t.Errorf("regenerated after a boot with d2: %s, want k3,d3", ids)
	}

	// No records, no export, but meta knows k1/d1: still k2/d2.
	n := newEnv(t)
	if err := n.open(t, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(n.keyringPath()); err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(n.open(t, nil).KeyIDs(), ","); ids != "k2,d2" {
		t.Errorf("a keyring replaced with no records: %s, want k2,d2", ids)
	}
}

// TestReadExportRecord_RegularAndBounded (a test name coined by F3-03): the
// export record is read only as a regular file of at most maxKeyringBytes;
// a link to a valid record and a record one byte over the bound fail, and
// so read as "not exported".
func TestReadExportRecord_RegularAndBounded(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	exportTo(t, e)
	rec := filepath.Join(e.dir, "secrets.keyring.exported")
	valid := fileBytes(t, rec)
	elsewhere := filepath.Join(t.TempDir(), "record")
	if err := os.WriteFile(elsewhere, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := readExportRecord(e.root, "secrets.keyring.exported"); err == nil {
		t.Error("a symbolic link to a valid record was read")
	}
	if missing, err := s.UnexportedKeys(); err == nil || len(missing) != 2 {
		t.Errorf("UnexportedKeys through a linked record = %v, %v; want both keys and an error", missing, err)
	}
	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	big := append(bytes.TrimRight(valid, "\n}"), []byte(`,"pad":"`+strings.Repeat("x", maxKeyringBytes)+`"}`)...)
	if err := os.WriteFile(rec, big[:maxKeyringBytes+1], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExportRecord(e.root, "secrets.keyring.exported"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a record one byte over the bound = %v, want refused as too large", err)
	}
}

// TestExportKeyring_RefusesSymlinkedKeyring (a test name coined by F3-03):
// export-keyring copies only a regular keyring file, never one reached
// through a symbolic link, even one whose target is inside the state
// directory (which os.Root would follow; a target outside it os.Root
// refuses on its own).
func TestExportKeyring_RefusesSymlinkedKeyring(t *testing.T) {
	e := newEnv(t)
	e.open(t, nil)
	target := filepath.Join(e.dir, "keyring.real")
	if err := os.Rename(e.keyringPath(), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("keyring.real", e.keyringPath()); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "keyring.export")
	if _, err := ExportKeyring(e.root, "", out, e.clock.now()); err == nil {
		t.Fatal("export-keyring copied a keyring reached through a symbolic link")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a refused export wrote --out")
	}
}

// TestWriteKeyringNew_NeverReplaces (a test name coined by F3-03): a first
// or regenerated keyring is created exclusively; one that exists is left
// byte for byte, and no temporary file stays behind.
func TestWriteKeyringNew_NeverReplaces(t *testing.T) {
	e := newEnv(t)
	f, err := newKeyringFile(1, testInstallID, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyringNew(e.root, defaultKeyFile, f); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(e.keyringPath()); err != nil || fi.Mode().Perm() != keyringMode {
		t.Fatalf("the new keyring: %v, %v; want mode 0600", fi, err)
	}
	before := fileBytes(t, e.keyringPath())
	g, err := newKeyringFile(2, testInstallID, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyringNew(e.root, defaultKeyFile, g); err == nil {
		t.Fatal("writeKeyringNew replaced an existing keyring")
	}
	if !bytes.Equal(fileBytes(t, e.keyringPath()), before) {
		t.Fatal("a refused writeKeyringNew changed the keyring")
	}
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range entries {
		if strings.Contains(d.Name(), ".tmp-") {
			t.Errorf("a temporary file stayed behind: %s", d.Name())
		}
	}
}

// TestOpen_RemovesStaleKeyringTemps (a test name coined by F3-03): Open
// removes the keyring's leftover temporary files (regular files named
// <key file>.tmp-<12 hex>) and nothing else: not a link of that name, not a
// near miss, not the export record's temporary file (export-keyring runs
// without the store lock).
func TestOpen_RemovesStaleKeyringTemps(t *testing.T) {
	e := newEnv(t)
	if err := e.open(t, nil).Close(); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(e.dir, "secrets.keyring.tmp-0123456789ab")
	keep := []string{
		filepath.Join(e.dir, "secrets.keyring.tmp-batcave"),
		filepath.Join(e.dir, "secrets.keyring.exported.tmp-0123456789ab"),
		filepath.Join(e.dir, "secrets.keyring.tmp-0123456789abc"),
	}
	for _, p := range append([]string{stale}, keep...) {
		if err := os.WriteFile(p, []byte("arkham"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(e.dir, "secrets.keyring.tmp-aaaaaaaaaaaa")
	if err := os.Symlink(filepath.Join(e.dir, "secrets.keyring.tmp-batcave"), link); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(time.Minute)
	e.open(t, nil)
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale keyring temporary file is still there: %v", err)
	}
	for _, p := range append(keep, link) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("Open removed %s: %v", filepath.Base(p), err)
		}
	}
	if !strings.Contains(e.logs.String(), "removed a stale keyring temporary file") {
		t.Error("the removal was not logged")
	}
}
