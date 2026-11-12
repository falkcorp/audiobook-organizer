// file: core/backup/internal_test.go
// version: 1.0.0
// guid: 8d3e5b17-6a02-4f9c-b1d4-0e7a9c2f5b36
// last-edited: 2026-11-12

package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage"
)

func compatSvc() *Svc {
	return &Svc{o: Options{
		AppID:        "gotham",
		PebbleFormat: 13,
		Modules:      map[string]string{"books": "1.0.0"},
		Owners: []storage.OwnerSpec{
			{Owner: "books", Enabled: true, Spec: storage.Spec{Level: 2, UpgradeFloor: &storage.UpgradeFloor{Owner: "books", Version: 3, InstallFirst: "v0.9.0"}}},
			{Owner: "core/auth", Enabled: true, Spec: storage.Spec{Keyspace: true, Level: 1}},
		},
	}}
}

func compatManifest() *Manifest {
	return &Manifest{
		Format: FormatMediacore, App: "gotham", PebbleFormat: 13,
		Levels:    map[string]int{"books": 2, "core": 1},
		Keyspaces: map[string]KSInfo{"books": {Version: 3}},
		Modules:   map[string]string{"books": "1.0.0"},
	}
}

func TestCompatAcceptsWhatTheBuildServes(t *testing.T) {
	w, err := compatSvc().compat(compatManifest())
	if err != nil || len(w) != 0 {
		t.Fatalf("compat = %v, %v", w, err)
	}
	// An older level and an older Pebble format are fine: the next boot migrates.
	m := compatManifest()
	m.Levels["books"], m.PebbleFormat = 1, 12
	if _, err := compatSvc().compat(m); err != nil {
		t.Fatalf("an older archive was refused: %v", err)
	}
}

func TestRestoreRefusesTooNewArchive(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"owner level above the build's": func(m *Manifest) { m.Levels["books"] = 3 },
		"Pebble format above the build": func(m *Manifest) { m.PebbleFormat = 14 },
	} {
		m := compatManifest()
		mutate(m)
		if _, err := compatSvc().compat(m); !errors.Is(err, ErrBackupTooNew) {
			t.Errorf("%s: err = %v, want ErrBackupTooNew", name, err)
		}
	}
	m := compatManifest()
	m.App = "arkham"
	if _, err := compatSvc().compat(m); !errors.Is(err, ErrWrongApp) {
		t.Errorf("wrong app: err = %v, want ErrWrongApp", err)
	}
	// A build that does not report its Pebble format does not compare it.
	s := compatSvc()
	s.o.PebbleFormat = 0
	m = compatManifest()
	m.PebbleFormat = 99
	if _, err := s.compat(m); err != nil {
		t.Errorf("an unreported build format was compared: %v", err)
	}
}

func TestCompatUpgradeFloor(t *testing.T) {
	m := compatManifest()
	m.Keyspaces["books"] = KSInfo{Version: 2}
	_, err := compatSvc().compat(m)
	var uf *storage.ErrUpgradeFloor
	if !errors.As(err, &uf) || uf.Owner != "books" || uf.Have != 2 || uf.Floor != 3 || uf.InstallFirst != "v0.9.0" {
		t.Fatalf("err = %#v, want *storage.ErrUpgradeFloor{books, have 2, floor 3, v0.9.0}", err)
	}
	// An owner absent from the archive has applied nothing: below any floor.
	m = compatManifest()
	delete(m.Keyspaces, "books")
	if _, err := compatSvc().compat(m); !errors.As(err, &uf) || uf.Have != 0 {
		t.Fatalf("a missing keyspace record passed the floor: %v", err)
	}
}

func TestCompatDormantModulesWarnAndAreKept(t *testing.T) {
	m := compatManifest()
	m.Levels["tv"] = 5 // an owner this build lacks: restored dormant, whatever its level
	m.Modules["tv"] = "2.0.0"
	w, err := compatSvc().compat(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) == 0 || !strings.Contains(strings.Join(w, "|"), "module tv") || !strings.Contains(strings.Join(w, "|"), "dormant") {
		t.Fatalf("warnings = %v", w)
	}
}

// cancelCodec cancels the run when the archive's first bytes reach the codec.
type cancelCodec struct {
	backuparchive.Codec
	cancel context.CancelFunc
}

func (c cancelCodec) NewWriter(w io.Writer, level int) (io.WriteCloser, error) {
	zw, err := c.Codec.NewWriter(w, level)
	return &cancelWriter{WriteCloser: zw, cancel: c.cancel}, err
}

type cancelWriter struct {
	io.WriteCloser
	cancel context.CancelFunc
}

func (c *cancelWriter) Write(p []byte) (int, error) { c.cancel(); return c.WriteCloser.Write(p) }

func spec(t *testing.T, codec backuparchive.Codec) (archiveSpec, string) {
	t.Helper()
	run, out := t.TempDir(), t.TempDir()
	var ents []Entry
	for _, n := range []string{"db/one", "db/two", "config/boot"} {
		body := []byte("robin-" + n)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(run, n)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(run, n), body, 0o600); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(body)
		ents = append(ents, Entry{Path: n, Size: int64(len(body)), SHA256: hex.EncodeToString(h[:]), Class: SystemOfRecord})
	}
	return archiveSpec{Partial: filepath.Join(out, "a.tar.gz.partial"), Codec: codec, Manifest: []byte(`{}`),
		Created: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), Entries: ents, RunDir: run}, out
}

func TestWriteArchiveCancelLeavesNoPartial(t *testing.T) {
	gz, err := backuparchive.Resolve("gzip")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sp, out := spec(t, cancelCodec{Codec: gz, cancel: cancel})
	_, _, err = writeArchive(ctx, sp)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("a canceled archive left %d files", len(ents))
	}
	// And the same archive, uncanceled, is written whole and 0600.
	sp, out = spec(t, gz)
	sum, size, err := writeArchive(context.Background(), sp)
	if err != nil || size == 0 || len(sum) != 64 {
		t.Fatalf("writeArchive = %q, %d, %v", sum, size, err)
	}
	fi, err := os.Stat(sp.Partial)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("partial = %v, %v", fi, err)
	}
}

func TestWriteArchiveRefusesAnInputThatChanged(t *testing.T) {
	gz, _ := backuparchive.Resolve("gzip")
	sp, out := spec(t, gz)
	// Same size, other bytes: the hash pass is no longer true of the file.
	if err := os.WriteFile(filepath.Join(sp.RunDir, "db/two"), []byte("joker-db/two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeArchive(context.Background(), sp); !errors.Is(err, ErrManifest) {
		t.Fatalf("err = %v, want ErrManifest", err)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("a failed archive left %d files", len(ents))
	}
	// A symlink in the run directory is not a regular file.
	sp, out = spec(t, gz)
	if err := os.Remove(filepath.Join(sp.RunDir, "db/one")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(sp.RunDir, "db/one")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeArchive(context.Background(), sp); !errors.Is(err, backuparchive.ErrNotRegularFile) {
		t.Fatalf("err = %v, want ErrNotRegularFile", err)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("left %d files", len(ents))
	}
	// An existing partial is never overwritten or followed.
	sp, _ = spec(t, gz)
	if err := os.WriteFile(sp.Partial, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeArchive(context.Background(), sp); err == nil {
		t.Fatal("an existing .partial was reused")
	}
	if b, _ := os.ReadFile(sp.Partial); string(b) != "x" {
		t.Fatal("an existing .partial was removed or rewritten")
	}
}
