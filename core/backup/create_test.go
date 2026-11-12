// file: core/backup/create_test.go
// version: 1.0.0
// guid: 3f9a1d58-c2e4-4b70-95a6-7d8e0b1c2f34
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// entriesOf reads every entry of the archive at path: name -> content.
func entriesOf(t *testing.T, path string) map[string]string {
	t.Helper()
	f, _, err := backuparchive.OpenRegular(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd, err := backuparchive.NewReader(f, backuparchive.Limits{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	out := map[string]string{}
	for {
		h, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rd)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = string(b)
	}
	return out
}

func TestCreateWritesAVerifiedOwnArchive(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	if a.Format != backup.FormatMediacore || a.Provenance != backup.Own || a.Manifest == nil {
		t.Fatalf("archive = %+v", a)
	}
	if a.Verified == nil || !a.Verified.OK || a.Verified.Level != backup.VerifyOpen {
		t.Fatalf("verified = %+v", a.Verified)
	}
	for _, suf := range []string{"", backup.ChecksumSidecar, backup.ManifestSidecar, backup.VerifySidecar} {
		fi, err := os.Lstat(e.path(a) + suf)
		if err != nil {
			t.Fatalf("%q: %v", suf, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s%s mode = %v, want 0600", a.Name, suf, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(e.backupDir); fi.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode = %v, want 0700", fi.Mode().Perm())
	}
	for _, n := range e.names() {
		if strings.HasSuffix(n, ".partial") || strings.HasSuffix(n, ".tmp") {
			t.Errorf("leftover %s", n)
		}
	}
	m := a.Manifest
	if m.App != "gotham" || m.Reason != "manual" || m.JournalSeq != 7 || m.AuditHighWater != 11 || m.InstallID == "" {
		t.Errorf("manifest = %+v", m)
	}
	if m.ServedBoot == nil || m.ServedBoot.N != 4 {
		t.Errorf("manifest carries served_boot %+v, want the pair read before the checkpoint (n=4)", m.ServedBoot)
	}
	if m.Keyspaces[string(gothamOwner)].Keys != 5 {
		t.Errorf("keyspace counts = %+v", m.Keyspaces)
	}
	if m.Provenance.KeyID != "d1" || m.Provenance.MAC == "" {
		t.Errorf("provenance = %+v", m.Provenance)
	}
	hasDB := false
	for _, en := range m.Entries {
		hasDB = hasDB || strings.HasPrefix(en.Path, "db/")
		if en.Class != backup.SystemOfRecord {
			t.Errorf("entry %s class = %v", en.Path, en.Class)
		}
	}
	if !hasDB {
		t.Error("the archive holds no db/ entry")
	}
	list, err := e.svc.List(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Provenance != backup.Own {
		t.Fatalf("List = %+v, %v", list, err)
	}
	// The staging directory is gone.
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".backup-staging")); len(ents) != 0 {
		t.Errorf("staging left %d entries", len(ents))
	}
}

// TestInPlaceRestoreOfLastNightsCoreArchive_Replaced is the Create half of
// SEC6-6: the boundaries and the content marker pair P are read before the
// checkpoint, the new pair P' is written right after it, and the archive
// carries P, so it is older than the live store's pair.
func TestInPlaceRestoreOfLastNightsCoreArchive_Replaced(t *testing.T) {
	e := newEnv(t)
	a := e.create("schedule")
	r, c, adv := e.log.index("markers.read"), e.log.index("checkpoint"), e.log.index("markers.advance")
	if !(r >= 0 && r < c && c < adv) {
		t.Fatalf("order = %v, want markers.read < checkpoint < markers.advance", e.log.list())
	}
	if len(e.markers.advances) != 1 || e.markers.advances[0].N != 5 {
		t.Fatalf("advances = %+v", e.markers.advances)
	}
	if a.Manifest.ServedBoot.N != 4 || a.Manifest.ServedBoot.N >= e.markers.sb.N {
		t.Fatalf("archive pair n=%d is not older than the live pair n=%d", a.Manifest.ServedBoot.N, e.markers.sb.N)
	}
	// A failed advance fails the archive: nothing is left behind.
	e2 := newEnv(t)
	e2.markers.failAdvance = errors.New("store refused the write")
	if _, err := e2.svc.Create(context.Background(), backup.CreateOptions{}); err == nil {
		t.Fatal("Create succeeded though the content marker could not be advanced")
	}
	if n := e2.names(); len(n) != 0 {
		t.Fatalf("a failed Create left %v", n)
	}
}

func TestCheckpointCrossDeviceRefused(t *testing.T) {
	e := newEnv(t)
	e.opts.Disk = backup.Disk{Device: func(p string) (uint64, error) {
		if strings.Contains(p, ".backup-staging") {
			return 2, nil
		}
		return 1, nil
	}}
	e.rebuild()
	_, err := e.svc.Create(context.Background(), backup.CreateOptions{})
	if !errors.Is(err, backup.ErrCrossDevice) {
		t.Fatalf("err = %v, want ErrCrossDevice", err)
	}
	if e.log.index("checkpoint") != -1 {
		t.Fatal("a checkpoint was attempted across devices: it would have been a silent copy")
	}
	if len(e.names()) != 0 {
		t.Fatalf("left %v", e.names())
	}
}

func TestNoKeyMaterialInArchives(t *testing.T) {
	keyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(keyDir, "keyring.json"), []byte(canaryKey), 0o600); err != nil {
		t.Fatal(err)
	}
	goodDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(goodDir, "playlist.json"), []byte("the long halloween"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.opts.Artifacts = func() []backup.Artifact {
		return []backup.Artifact{
			{Name: "core.keyring", Owner: "core/secrets", Class: backup.Secret, Dir: keyDir},
			{Name: "books.playlists", Owner: "books", Class: backup.SystemOfRecord, Consistency: backup.AppendOnly, Dir: goodDir},
		}
	}
	e.rebuild()
	a := e.create("manual")
	got := entriesOf(t, e.path(a))
	if got["artifacts/books/books.playlists/playlist.json"] != "the long halloween" {
		t.Fatalf("the system-of-record artifact is missing: %v", keysOf(got))
	}
	for name, body := range got {
		if strings.Contains(name, "keyring") || strings.Contains(body, canaryKey) {
			t.Errorf("entry %s carries key material", name)
		}
	}
	for _, en := range a.Manifest.Entries {
		if en.Class == backup.Secret {
			t.Errorf("entry %s has the secret class", en.Path)
		}
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPlainArchiveHasNoConfigSecrets(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mediacore.yaml")
	boot := filepath.Join(filepath.Dir(cfg), "config.boot.json")
	if err := os.WriteFile(cfg, []byte("openai_key: "+canaryKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boot, []byte(`{"storage":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, withSecrets := range []bool{true, false} {
		e := newEnv(t)
		e.cfg.secrets, e.cfg.redacted = withSecrets, []byte("openai_key: <redacted>\n")
		e.opts.Config, e.opts.ConfigPath, e.opts.BootRecordPath = e.cfg, cfg, boot
		e.rebuild()
		// HEAD's gap-fill file sits beside the DB: it is never archived.
		if err := os.WriteFile(filepath.Join(e.dbParent, "config.yaml"), []byte(canaryKey), 0o600); err != nil {
			t.Fatal(err)
		}
		a := e.create("manual")
		got := entriesOf(t, e.path(a))
		body, ok := got["config/mediacore.yaml"]
		if !ok {
			t.Fatalf("withSecrets=%v: no config entry in %v", withSecrets, keysOf(got))
		}
		if a.Manifest.ConfigRedacted != withSecrets {
			t.Errorf("withSecrets=%v: ConfigRedacted = %v", withSecrets, a.Manifest.ConfigRedacted)
		}
		if withSecrets && (strings.Contains(body, canaryKey) || !strings.Contains(body, "<redacted>")) {
			t.Errorf("a config with secrets was archived as %q", body)
		}
		if !withSecrets && !strings.Contains(body, canaryKey) {
			t.Errorf("a config without secrets was not archived as itself: %q", body)
		}
		if got["config/config.boot.json"] != `{"storage":"x"}` {
			t.Errorf("boot record = %q", got["config/config.boot.json"])
		}
		for name, b := range got {
			if name == "config/mediacore.yaml" && !withSecrets {
				continue
			}
			if strings.Contains(b, canaryKey) {
				t.Errorf("entry %s carries the canary", name)
			}
			if name == "config/config.yaml" {
				t.Errorf("the gap-fill config.yaml was archived")
			}
		}
	}
}

func TestCreateRefusesBadCodecBeforeAnyWork(t *testing.T) {
	e := newEnv(t)
	e.settings.Compression = "lz4"
	_, err := e.svc.Create(context.Background(), backup.CreateOptions{})
	if !errors.Is(err, backuparchive.ErrUnknownCodec) {
		t.Fatalf("err = %v", err)
	}
	if e.log.index("markers.read") != -1 || e.log.index("checkpoint") != -1 {
		t.Fatalf("work began before the configuration error: %v", e.log.list())
	}
	if _, err := os.Stat(e.backupDir); err == nil {
		t.Error("the backup directory was created before the configuration error")
	}
}
