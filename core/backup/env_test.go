// file: core/backup/env_test.go
// version: 1.0.2
// guid: e5b0c7d2-8a41-4f93-b6d7-2a9c1e4f8b60
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const (
	gothamOwner storage.Owner = "core/auth" // a keyspace the fixture fills
	canaryKey                 = "ROBIN-KEYRING-CANARY-0d3f"
)

// eventLog records the order of the seams' calls.
type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(s string) { l.mu.Lock(); l.ev = append(l.ev, s); l.mu.Unlock() }
func (l *eventLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}
func (l *eventLog) index(s string) int {
	for i, e := range l.list() {
		if e == s {
			return i
		}
	}
	return -1
}

// recStore is a backup.Store over the fixture's admin, logging the checkpoint.
type recStore struct {
	admin *storage.Admin
	log   *eventLog
}

func (r recStore) Checkpoint(ctx context.Context, dir string) error {
	r.log.add("checkpoint")
	return r.admin.Checkpoint(ctx, dir)
}
func (r recStore) InstallID() string { return r.admin.InstallID() }

// fakeMarkers stands in for the journal component and the audit store.
type fakeMarkers struct {
	log         *eventLog
	head, audit uint64
	sb          journal.ServedBoot
	advances    []journal.ServedBoot
	failAdvance error
}

func (m *fakeMarkers) Read(context.Context) (backup.Boundaries, error) {
	m.log.add("markers.read")
	sb := m.sb
	return backup.Boundaries{JournalSeq: m.head, AuditHighWater: m.audit, ServedBoot: &sb}, nil
}

func (m *fakeMarkers) Advance(context.Context) (journal.ServedBoot, error) {
	m.log.add("markers.advance")
	if m.failAdvance != nil {
		return journal.ServedBoot{}, m.failAdvance
	}
	m.sb = journal.ServedBoot{N: m.sb.N + 1, Nonce: "nonce-" + string(rune('a'+m.sb.N+1))}
	m.advances = append(m.advances, m.sb)
	return m.sb, nil
}

// fakeStager records the intents it is given, and enforces the one slot the way
// restorejournal.CreateIntent does.
type fakeStager struct {
	stateDir string
	staged   []restorejournal.Intent
	err      error
}

func (f *fakeStager) Stage(_ context.Context, in restorejournal.Intent) error {
	if f.err != nil {
		return f.err
	}
	if err := restorejournal.CreateIntent(f.stateDir, in); err != nil {
		return err
	}
	f.staged = append(f.staged, in)
	return nil
}

// fakeConfig is 05-config's view of the config file.
type fakeConfig struct {
	secrets  bool
	redacted []byte
}

func (c *fakeConfig) ConfigFileHasSecrets() bool          { return c.secrets }
func (c *fakeConfig) RedactedConfigFile() ([]byte, error) { return c.redacted, nil }

// secretKeys is the keyring: derivation keys plus the key IDs a restored
// store's secrets need.
type secretKeys struct {
	*bruceKeys
	referenced []string
}

func (k *secretKeys) KeyIDsReferenced(context.Context, storage.Reader) ([]string, error) {
	return k.referenced, nil
}

// env is a real store, a backup directory, a state directory and a Service over
// them.
type env struct {
	t        *testing.T
	st       *coretest.Store
	owners   []storage.OwnerSpec
	keys     *secretKeys
	log      *eventLog
	markers  *fakeMarkers
	stager   *fakeStager
	cfg      *fakeConfig
	settings backup.Settings
	opts     backup.Options
	svc      *backup.Svc

	dbParent, backupDir, stateDir, confDir string
	clock                                  time.Time
	mu                                     sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	owners := []storage.OwnerSpec{
		{Owner: journal.Owner, Spec: journal.StorageSpec(), Enabled: true},
		{Owner: gothamOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
	}
	e := &env{
		t: t, owners: owners, log: &eventLog{}, cfg: &fakeConfig{},
		keys:  &secretKeys{bruceKeys: newBruceKeys("d1", "d1")},
		clock: time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC),
	}
	e.st = coretest.NewStore(t, owners...)
	e.dbParent = filepath.Dir(e.st.Path())
	e.backupDir = filepath.Join(e.dbParent, "backups")
	e.stateDir = t.TempDir()
	e.confDir = t.TempDir()
	e.markers = &fakeMarkers{log: e.log, head: 7, audit: 11, sb: journal.ServedBoot{N: 4, Nonce: "nonce-d"}}
	e.stager = &fakeStager{stateDir: e.stateDir}
	e.settings = backup.DefaultSettings()
	e.settings.HashWorkers, e.settings.SpaceMargin = 2, 1
	e.settings.Compression = "gzip"
	ks := e.st.Keyspace(gothamOwner)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := ks.Set(context.Background(), []byte("rogue/"+k), []byte("arkham-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	e.build()
	return e
}

// build (re)creates the Service from the env's current seams and settings.
func (e *env) build() {
	admin := e.st.DB().Admin()
	e.opts = backup.Options{
		AppID: "gotham", AppVersion: "1.0.0", CoreVersion: "0.9.0", BuildID: "build-1", Prefix: "gotham_",
		Dir: e.backupDir, StateDir: e.stateDir, DBPath: e.st.Path(),
		Settings: func() backup.Settings { return e.settings },
		Owners:   e.owners, Modules: map[string]string{"books": "1.0.0"},
		Store: recStore{admin: admin, log: e.log}, Keyring: e.keys, Markers: e.markers, Stager: e.stager,
		Inspector: &backup.StorageInspector{
			Base:  storage.OpenOptions{Owners: e.owners, Build: "test-build", FreeSpaceMargin: 1},
			Admin: admin, Secrets: e.keys, SecretsOwner: gothamOwner,
		},
		Clock: e.now,
	}
	e.rebuild()
}

func (e *env) rebuild() {
	e.t.Helper()
	svc, err := backup.New(e.opts)
	if err != nil {
		e.t.Fatal(err)
	}
	e.svc = svc
}

// now is a clock that moves two minutes per call, so every archive and every
// verification has its own time.
func (e *env) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = e.clock.Add(2 * time.Minute)
	return e.clock
}

func (e *env) create(reason string) backup.Archive {
	e.t.Helper()
	a, err := e.svc.Create(context.Background(), backup.CreateOptions{Reason: reason})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return a
}

// path is an archive's file.
func (e *env) path(a backup.Archive) string { return filepath.Join(e.backupDir, a.Name) }

// names lists the backup directory's file names, sorted.
func (e *env) names() []string {
	ents, err := os.ReadDir(e.backupDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range ents {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out
}

func storageBase(e *env) storage.OpenOptions {
	return storage.OpenOptions{Owners: e.owners, Build: "test-build", FreeSpaceMargin: 1}
}
