// file: core/secrets/open_test.go
// version: 1.0.2
// guid: 5543ef2e-3564-4e6b-a213-df11eb05e442
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// TestNoStoreWriteBeforeCheckpoint_SecretsOpen is the secrets half of
// 05-config's TestNoStoreWriteBeforeCheckpoint: Open on a fresh store writes
// exactly c/secrets/meta (and the keyring file), and a second Open writes
// nothing at all.
func TestNoStoreWriteBeforeCheckpoint_SecretsOpen(t *testing.T) {
	e := newEnv(t)
	if n := len(rows(t, e.ks)); n != 0 {
		t.Fatalf("a fresh store holds %d c/secrets/ rows", n)
	}
	s := e.open(t, nil)
	got := rows(t, e.ks)
	if len(got) != 1 {
		t.Fatalf("Open on a fresh store wrote %d rows, want exactly c/secrets/meta", len(got))
	}
	raw, ok := got[string(metaKey())]
	if !ok {
		t.Fatal("Open did not write c/secrets/meta")
	}
	var m storeMeta
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m.AppID != testAppID || m.KeyringFormat != keyringFormat || m.Created.IsZero() {
		t.Fatalf("c/secrets/meta = %s (%v)", raw, err)
	}
	if other := rows(t, e.other); len(other) != 0 {
		t.Fatalf("Open wrote %d rows outside c/secrets/", len(other))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	kr := fileBytes(t, e.keyringPath())
	e.open(t, nil)
	if again := rows(t, e.ks); len(again) != 1 || again[string(metaKey())] != raw {
		t.Fatalf("a second Open changed c/secrets/: %d rows", len(again))
	}
	if !bytes.Equal(fileBytes(t, e.keyringPath()), kr) {
		t.Fatal("a second Open rewrote the keyring")
	}
}

// TestOpen_FirstRunKeyring: with no keyring and no records, Open creates
// k1 (data) and d1 (derive), both active, in a 0600 file inside a 0700
// directory it creates, with the store's install ID in the header.
func TestOpen_FirstRunKeyring(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, func(o *Options) { o.KeyFile = "keys/secrets.keyring" })
	path := filepath.Join(e.dir, "keys", "secrets.keyring")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("keyring mode %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("keyring directory mode %v, want 0700", di.Mode().Perm())
	}
	f, err := readKeyring(e.root, "keys/secrets.keyring")
	if err != nil {
		t.Fatal(err)
	}
	if f.Format != keyringFormat || f.InstallID != testInstallID || len(f.Keys) != 2 {
		t.Fatalf("keyring header %q/%q with %d keys", f.Format, f.InstallID, len(f.Keys))
	}
	if f.Keys[0].ID != "k1" || f.Keys[0].Use != useData || f.Keys[1].ID != "d1" || f.Keys[1].Use != useDerive {
		t.Fatalf("first-run keys %v", s.KeyIDs())
	}
	if s.ActiveDeriveID() != "d1" {
		t.Errorf("ActiveDeriveID = %q, want d1", s.ActiveDeriveID())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the keyring directory holds %d entries, want only the keyring (no temporary file left)", len(entries))
	}
}

// TestOpen_AppIDMismatchRefuses (05-config §2.9): a store opened as gotham
// refuses to open as arkham, naming both IDs.
func TestOpen_AppIDMismatchRefuses(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	o := e.opts()
	o.AppID = "arkham"
	_, err := Open(context.Background(), o)
	if !errors.Is(err, ErrAppIDMismatch) {
		t.Fatalf("Open as another app = %v, want ErrAppIDMismatch", err)
	}
	if !strings.Contains(err.Error(), `"gotham"`) || !strings.Contains(err.Error(), `"arkham"`) {
		t.Errorf("the error does not name both app IDs: %v", err)
	}
}

// TestOpen_MissingKeyringWithRecordsRefuses (05-config §2.9): with sealed
// records and no keyring, Open refuses with ErrKeyMissing naming the file,
// the exported copy and the override, and writes nothing. A malformed
// record counts as sealed (it may be a value Open cannot parse, and a new
// keyring would orphan it). Cleared and unreadable markers need no keyring.
func TestOpen_MissingKeyringWithRecordsRefuses(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.keyringPath()); err != nil {
		t.Fatal(err)
	}
	before := rows(t, e.ks)
	_, err := Open(context.Background(), e.opts())
	if !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("Open = %v, want ErrKeyMissing", err)
	}
	for _, want := range []string{defaultKeyFile, "export-keyring", "core.secrets.allow_key_regen"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the ErrKeyMissing message does not name %q: %v", want, err)
		}
	}
	if _, serr := os.Stat(e.keyringPath()); !errors.Is(serr, os.ErrNotExist) {
		t.Error("a refused Open created a keyring")
	}
	if after := rows(t, e.ks); len(after) != len(before) {
		t.Error("a refused Open changed c/secrets/")
	}

	// Only a malformed record: still refused.
	g := newEnv(t)
	if err := g.open(t, nil).Close(); err != nil {
		t.Fatal(err)
	}
	rawSet(t, g.ks, recordKey(nameGate), []byte(`{"v":1,"kid":"k1","nonce":`))
	if err := os.Remove(g.keyringPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), g.opts()); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("Open with only a malformed record = %v, want ErrKeyMissing", err)
	}
	if _, serr := os.Stat(g.keyringPath()); !errors.Is(serr, os.ErrNotExist) {
		t.Error("a refused Open created a keyring over a malformed record")
	}

	// Only markers: no keyring is needed, so a new one is generated.
	m := newEnv(t)
	ms := m.open(t, nil)
	mustPut(t, ms, nameGate, valueVengeance)
	if err := ms.Delete(context.Background(), nameGate, byAlfred); err != nil {
		t.Fatal(err)
	}
	b := m.ks.NewBatch()
	if err := ms.StageUnreadable(context.Background(), b, nameModule, reasonKey, "cutover:m2"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := ms.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.keyringPath()); err != nil {
		t.Fatal(err)
	}
	m.open(t, nil)
}

// TestOpen_RegenKeepsOrphans (05-config §2.9): with allow_key_regen, Open
// generates new keys under new IDs and keeps every record: they read as
// ErrKeyUnavailable until the old keyring is put back, which restores them.
// Only c/secrets/meta changes: its max_key_id rises to the new keys' number.
func TestOpen_RegenKeepsOrphans(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	mustPut(t, s, nameModule, valueSerious)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	old := fileBytes(t, e.keyringPath())
	if err := os.Remove(e.keyringPath()); err != nil {
		t.Fatal(err)
	}
	before := rows(t, e.ks)

	r := e.open(t, func(o *Options) { o.AllowKeyRegen = true })
	if ids := strings.Join(r.KeyIDs(), ","); ids != "k2,d2" {
		t.Errorf("regenerated key IDs %s, want k2,d2 (never reusing k1)", ids)
	}
	after := rows(t, e.ks)
	if len(after) != len(before) {
		t.Fatalf("regen changed the row count: %d -> %d", len(before), len(after))
	}
	for k, v := range before {
		if k != string(metaKey()) && after[k] != v {
			t.Fatal("regen rewrote a record")
		}
	}
	if m, err := decodeStoreMeta([]byte(after[string(metaKey())])); err != nil || m.MaxKeyID != 2 || m.AppID != "gotham" {
		t.Fatalf("c/secrets/meta after regen = %+v, %v; want max_key_id 2", m, err)
	}
	for _, n := range []Name{nameGate, nameModule} {
		if _, m, err := r.Get(context.Background(), n); !errors.Is(err, ErrKeyUnavailable) || !errors.Is(m.Err, ErrKeyUnavailable) || !m.Set {
			t.Errorf("Get %s after regen = %v (Meta.Err %v), want ErrKeyUnavailable", n, err, m.Err)
		}
	}
	if !strings.Contains(e.logs.String(), "allow_key_regen") {
		t.Error("regen logged no warning")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(e.keyringPath(), old, 0o600); err != nil {
		t.Fatal(err)
	}
	back := e.open(t, nil)
	mustGet(t, back, nameGate, valueVengeance)
	mustGet(t, back, nameModule, valueSerious)
}

// TestOpen_KeyringPermissions (05-config §2.9): a 0644 keyring is set back
// to 0600; a keyring path that is not a regular file is refused, and a
// keyring that cannot be read is refused and never replaced.
func TestOpen_KeyringPermissions(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.keyringPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	e.open(t, nil)
	fi, err := os.Stat(e.keyringPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("keyring mode %v after Open, want 0600", fi.Mode().Perm())
	}
	if !strings.Contains(e.logs.String(), "0600") {
		t.Error("fixing the mode logged nothing")
	}

	d := newEnv(t)
	if err := os.Mkdir(d.keyringPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), d.opts()); err == nil {
		t.Error("Open accepted a directory as the keyring")
	}

	g := newEnv(t)
	garbage := []byte(`{"format":"mediacore-keyring/1","keys":[`)
	if err := os.WriteFile(g.keyringPath(), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), g.opts()); err == nil {
		t.Error("Open accepted a keyring that does not decode")
	}
	if !bytes.Equal(fileBytes(t, g.keyringPath()), garbage) {
		t.Error("Open replaced a keyring it could not read")
	}
	if n := len(rows(t, g.ks)); n != 0 {
		t.Error("a refused Open wrote c/secrets/meta")
	}
}

// TestOpen_ResetBootRewritesInstallID (05-config §2.9, DATA7-17): without
// ResetBoot an existing header install_id is never changed (an absent one is
// written); with it the header takes the fresh store's install ID and the
// keys stay the same.
func TestOpen_ResetBootRewritesInstallID(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, func(o *Options) { o.InstallID = "" })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f := e.readKeyringFile(t); f.InstallID != "" {
		t.Fatalf("header install_id %q with no install ID given", f.InstallID)
	}
	s = e.open(t, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f := e.readKeyringFile(t); f.InstallID != testInstallID {
		t.Fatalf("an absent header install_id was not written: %q", f.InstallID)
	}
	keysBefore := e.readKeyringFile(t).Keys

	s = e.open(t, func(o *Options) { o.InstallID = "batcave-0002" })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f := e.readKeyringFile(t); f.InstallID != testInstallID {
		t.Fatalf("an Open without ResetBoot changed the header install_id to %q", f.InstallID)
	}
	if !strings.Contains(e.logs.String(), "another install") {
		t.Error("a header naming another install logged no warning")
	}

	s = e.open(t, func(o *Options) { o.InstallID, o.ResetBoot = "batcave-0002", true })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f := e.readKeyringFile(t)
	if f.InstallID != "batcave-0002" {
		t.Fatalf("a reset boot left the header install_id at %q", f.InstallID)
	}
	if len(f.Keys) != len(keysBefore) {
		t.Fatal("a reset boot changed the keys")
	}
	for i := range f.Keys {
		if f.Keys[i].ID != keysBefore[i].ID || !bytes.Equal(f.Keys[i].Material, keysBefore[i].Material) || f.Keys[i].Status != keysBefore[i].Status {
			t.Fatalf("a reset boot changed key %s", f.Keys[i].ID)
		}
	}

	if _, err := Open(context.Background(), Options{StateDir: e.root, Keyspace: e.ks, AppID: testAppID, ResetBoot: true}); err == nil {
		t.Error("a reset boot without the install ID was accepted")
	}
}

// TestOpen_RefusesBadOptions: Open validates its options before it touches
// anything.
func TestOpen_RefusesBadOptions(t *testing.T) {
	e := newEnv(t)
	for name, mut := range map[string]func(*Options){
		"no state dir":   func(o *Options) { o.StateDir = nil },
		"bad app id":     func(o *Options) { o.AppID = "Gotham" },
		"short app id":   func(o *Options) { o.AppID = "g" },
		"no keyspace":    func(o *Options) { o.Keyspace = e.ks.Sub("") },
		"escaping file":  func(o *Options) { o.KeyFile = "../secrets.keyring" },
		"absolute file":  func(o *Options) { o.KeyFile = "/secrets.keyring" },
		"bad install id": func(o *Options) { o.InstallID = "wayne manor" },
		"reset, no id":   func(o *Options) { o.InstallID, o.ResetBoot = "", true },
	} {
		o := e.opts()
		mut(&o)
		if s, err := Open(context.Background(), o); err == nil {
			_ = s.Close()
			t.Errorf("%s: Open accepted it", name)
		}
	}
	if n := len(rows(t, e.ks)); n != 0 {
		t.Errorf("refused Opens wrote %d rows", n)
	}
	if _, err := os.Stat(e.keyringPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused Open created a keyring")
	}
}

// TestKeyring_Invariants (a test name coined by F3-03): the keyring decoder
// enforces exactly one active key per use, unique well-formed IDs, 32-byte
// material, retire_after on verify derive keys only, a known format and no
// unknown fields or trailing data.
func TestKeyring_Invariants(t *testing.T) {
	now := time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)
	ra := now.Add(time.Hour)
	mk := func(id, use, status string) keyringKey {
		m := make(keyMaterial, keyBytes)
		if _, err := rand.Read(m); err != nil {
			t.Fatal(err)
		}
		return keyringKey{ID: id, Use: use, Created: now, Status: status, Material: m}
	}
	valid := func() *keyringFile {
		return &keyringFile{Format: keyringFormat, InstallID: testInstallID, Keys: []keyringKey{mk("k1", useData, statusActive), mk("d1", useDerive, statusActive)}}
	}
	if err := valid().validate(); err != nil {
		t.Fatalf("a valid keyring fails: %v", err)
	}
	for name, mut := range map[string]func(f *keyringFile){
		"two active data":   func(f *keyringFile) { f.Keys = append(f.Keys, mk("k2", useData, statusActive)) },
		"two active derive": func(f *keyringFile) { f.Keys = append(f.Keys, mk("d2", useDerive, statusActive)) },
		"no active data":    func(f *keyringFile) { f.Keys[0].Status = statusRetired },
		"no active derive":  func(f *keyringFile) { f.Keys = f.Keys[:1] },
		"duplicate id":      func(f *keyringFile) { f.Keys[1].ID = "k1" },
		"bad id":            func(f *keyringFile) { f.Keys[0].ID = "K1" },
		"bad use":           func(f *keyringFile) { f.Keys[0].Use = "batarang" },
		"bad status":        func(f *keyringFile) { f.Keys[0].Status = "lost" },
		"short material":    func(f *keyringFile) { f.Keys[0].Material = f.Keys[0].Material[:16] },
		"no created":        func(f *keyringFile) { f.Keys[0].Created = time.Time{} },
		"verify, no retire": func(f *keyringFile) { f.Keys = append(f.Keys, mk("d0", useDerive, statusVerify)) },
		"retire on active":  func(f *keyringFile) { f.Keys[1].RetireAfter = &ra },
		"verify data key": func(f *keyringFile) {
			k := mk("k0", useData, statusVerify)
			k.RetireAfter = &ra
			f.Keys = append(f.Keys, k)
		},
		"bad format":     func(f *keyringFile) { f.Format = "mediacore-keyring/2" },
		"bad install id": func(f *keyringFile) { f.InstallID = "wayne manor" },
	} {
		f := valid()
		mut(f)
		if err := f.validate(); err == nil {
			t.Errorf("%s: validate accepted it", name)
		} else if strings.Contains(err.Error(), "material\":") {
			t.Errorf("%s: the error quotes material", name)
		}
	}
	raw, err := json.Marshal(valid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeKeyring(raw); err != nil {
		t.Fatalf("decode of a valid keyring: %v", err)
	}
	if _, err := decodeKeyring(append(append([]byte(nil), raw...), []byte(` {}`)...)); err == nil {
		t.Error("decode accepted trailing data")
	}
	unknown := bytes.Replace(raw, []byte(`"format"`), []byte(`"utility_belt":1,"format"`), 1)
	if _, err := decodeKeyring(unknown); err == nil {
		t.Error("decode accepted an unknown field")
	}
	// writeKeyring never writes an invalid keyring.
	e := newEnv(t)
	bad := valid()
	bad.Keys = append(bad.Keys, mk("k2", useData, statusActive))
	if err := writeKeyring(e.root, defaultKeyFile, bad); err == nil {
		t.Error("writeKeyring wrote a keyring with two active data keys")
	}
}
