// file: core/secrets/offline_test.go
// version: 1.1.0
// guid: a73ea92d-cb22-49ff-8573-95512b562818
// last-edited: 2026-11-19

package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// orderSpy records, in call order, each directory sync (by label) and each
// open of the keyring file, and fails the syncs of failOn.
type orderSpy struct {
	StateFiles
	keyFile string
	failOn  map[string]bool

	mu     sync.Mutex
	events []string
}

func (p *orderSpy) note(ev string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
}

func (p *orderSpy) SyncDir(label, name string) error {
	p.note("sync:" + label)
	if p.failOn[label] {
		return errors.New("no space left on device (Penguin's trap)")
	}
	return p.StateFiles.SyncDir(label, name)
}

func (p *orderSpy) Open(name string) (io.ReadCloser, error) {
	if name == p.keyFile {
		p.note("open:keyring")
	}
	return p.StateFiles.Open(name)
}

// TestExportKeyring_SyncsTheDirectoryBeforeItCopies: the keyring's directory
// is fsynced before export-keyring reads the keyring, and when that sync fails
// nothing is copied and no record is written: an operator never holds a backup
// of a key whose directory entry may be lost with the next power cut.
func TestExportKeyring_SyncsTheDirectoryBeforeItCopies(t *testing.T) {
	e := newEnv(t)
	e.open(t, nil)
	run := func(failOn ...string) (*orderSpy, string, error) {
		spy := &orderSpy{StateFiles: RootStateFiles(e.root), keyFile: defaultKeyFile, failOn: map[string]bool{}}
		for _, l := range failOn {
			spy.failOn[l] = true
		}
		out := filepath.Join(t.TempDir(), "keyring.export")
		_, err := exportKeyring(e.root, spy, "", out, e.clock.now())
		return spy, out, err
	}

	spy, out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if got := spy.events; len(got) < 2 || got[0] != "sync:"+labelExportSync || got[1] != "open:keyring" {
		t.Fatalf("events = %v, want the export-sync before the keyring is opened", got)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("a healthy export wrote no copy: %v", err)
	}

	if err := os.Remove(filepath.Join(e.dir, "secrets.keyring.exported")); err != nil {
		t.Fatal(err)
	}
	spy, out, err = run(labelExportSync)
	if err == nil {
		t.Fatal("the export succeeded though the keyring's directory could not be synced")
	}
	if slices.Contains(spy.events, "open:keyring") {
		t.Errorf("events = %v: the keyring was read after the sync failed", spy.events)
	}
	if _, serr := os.Stat(out); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("a copy exists after the failed sync (stat: %v)", serr)
	}
	if _, serr := os.Stat(filepath.Join(e.dir, "secrets.keyring.exported")); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("an export record exists after the failed sync (stat: %v)", serr)
	}
}

// TestExportKeyring_CopiesWithoutTheStore (a test name coined by F3-03; the
// running-server half is the kernel's TestExportKeyring_OpensNoStore): the
// export is the keyring file byte for byte at 0600, the record names every
// key ID and holds no material, and no c/secrets/ row changes.
func TestExportKeyring_CopiesWithoutTheStore(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	before := rows(t, e.ks)
	out := filepath.Join(t.TempDir(), "oracle", "keyring.export")
	if err := os.Mkdir(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := ExportKeyring(e.root, "", out, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBytes(t, out), fileBytes(t, e.keyringPath())) {
		t.Fatal("the export is not the keyring file")
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("export mode %v, want 0600", fi.Mode().Perm())
	}
	if r.InstallID != testInstallID || !slices.Equal(r.KeyIDs, []string{"d1", "k1"}) || r.Out != out || r.Record != "secrets.keyring.exported" {
		t.Fatalf("ExportResult = %+v", r)
	}
	raw := fileBytes(t, filepath.Join(e.dir, "secrets.keyring.exported"))
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec["format"] != exportFormat || len(rec) != 3 {
		t.Fatalf("export record %s", raw)
	}
	f := e.readKeyringFile(t)
	for _, k := range f.Keys {
		enc, _ := json.Marshal([]byte(k.Material))
		if bytes.Contains(raw, bytes.Trim(enc, `"`)) {
			t.Fatal("the export record holds key material")
		}
	}
	if missing, err := s.UnexportedKeys(); err != nil || len(missing) != 0 {
		t.Fatalf("UnexportedKeys after the export = %v, %v", missing, err)
	}
	after := rows(t, e.ks)
	if len(after) != len(before) {
		t.Fatal("export-keyring changed c/secrets/")
	}

	// An existing 0600 export is replaced.
	if _, err := ExportKeyring(e.root, "", out, e.clock.now()); err != nil {
		t.Fatalf("re-export over a 0600 file: %v", err)
	}
}

// TestExportKeyring_RefusesLooseOrOwnTargets (a test name coined by F3-03):
// a destination looser than 0600, a non-regular one (a symbolic link or a
// directory), the keyring itself and its record are refused, and a missing
// keyring has nothing to export.
func TestExportKeyring_RefusesLooseOrOwnTargets(t *testing.T) {
	e := newEnv(t)
	if _, err := ExportKeyring(e.root, "", filepath.Join(t.TempDir(), "x"), e.clock.now()); err == nil {
		t.Fatal("export of a missing keyring succeeded")
	}
	e.open(t, nil)
	kr := fileBytes(t, e.keyringPath())
	dir := t.TempDir()

	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("arkham"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(e.keyringPath(), hard); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"0644 file":     loose,
		"symbolic link": link,
		"directory":     sub,
		"the keyring":   e.keyringPath(),
		"a hard link":   hard,
		"the record":    filepath.Join(e.dir, "secrets.keyring.exported"),
		"empty":         "",
	} {
		if _, err := ExportKeyring(e.root, "", out, e.clock.now()); err == nil {
			t.Errorf("%s: export accepted it", name)
		}
	}
	if !bytes.Equal(fileBytes(t, loose), []byte("arkham")) {
		t.Error("a refused export changed the loose file")
	}
	if !bytes.Equal(fileBytes(t, e.keyringPath()), kr) {
		t.Error("a refused export changed the keyring")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "secrets.keyring.exported")); !os.IsNotExist(err) {
		t.Error("a refused export wrote the record")
	}
}

// TestSetInstallID_RewritesOnlyTheHeader (the secrets half of 05-config's
// TestSetInstallID_RestoresLineageOffline; the kernel half checks the lock
// and the store's marker): only the header's install_id changes, the keys and
// the export record stay byte for byte, the old value is returned, and a
// missing keyring or an invalid ID is refused.
func TestSetInstallID_RewritesOnlyTheHeader(t *testing.T) {
	e := newEnv(t)
	if _, err := SetInstallID(e.root, "", "batcave-0001"); err == nil {
		t.Fatal("SetInstallID accepted a missing keyring")
	}
	e.open(t, nil).Close()
	if _, err := ExportKeyring(e.root, "", filepath.Join(t.TempDir(), "k"), e.clock.now()); err != nil {
		t.Fatal(err)
	}
	record := fileBytes(t, filepath.Join(e.dir, "secrets.keyring.exported"))
	keys := e.readKeyringFile(t).Keys

	old, err := SetInstallID(e.root, "", "batcave-0000")
	if err != nil {
		t.Fatal(err)
	}
	if old != testInstallID {
		t.Fatalf("old install ID %q, want %q", old, testInstallID)
	}
	f := e.readKeyringFile(t)
	if f.InstallID != "batcave-0000" {
		t.Fatalf("header install_id %q", f.InstallID)
	}
	for i := range keys {
		if f.Keys[i].ID != keys[i].ID || !bytes.Equal(f.Keys[i].Material, keys[i].Material) || f.Keys[i].Status != keys[i].Status {
			t.Fatalf("SetInstallID changed key %s", keys[i].ID)
		}
	}
	if !bytes.Equal(fileBytes(t, filepath.Join(e.dir, "secrets.keyring.exported")), record) {
		t.Fatal("SetInstallID changed the export record")
	}
	fi, err := os.Stat(e.keyringPath())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("keyring mode after SetInstallID: %v %v", fi.Mode().Perm(), err)
	}
	for _, bad := range []string{"", "wayne manor", string(bytes.Repeat([]byte("b"), 129))} {
		if _, err := SetInstallID(e.root, "", bad); err == nil {
			t.Errorf("SetInstallID accepted %q", bad)
		}
	}
}
