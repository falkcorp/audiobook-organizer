// file: core/secrets/helpers_test.go
// version: 1.0.0
// guid: b12ecc65-7f52-400c-bdf2-3a5b4947b278
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
)

// The fixtures: Batman names and values only (public repo).
const (
	testAppID     = "gotham"
	testInstallID = "batcave-0001"

	nameGate   Name = "core.http.basic_gate.password"
	nameModule Name = "modules.batcomputer.api_key"
	nameOracle Name = "modules.batcomputer.oracle_token"
	nameInteg  Name = "integrations.wayne.lucius.token"

	valueVengeance = "i-am-vengeance-i-am-the-night"
	valueSerious   = "why-so-serious-hahaha"
	valueClock     = "oracle-clocktower-uplink"

	byAlfred = "user:alfred"
)

// secretsOwner is the owner of c/secrets/ as the kernel declares it
// (kernel.secretsOwnerSpec): a keyspace, Serialized as a whole.
const secretsOwner storage.Owner = "core/secrets"

// otherOwner is a second keyspace, for batches that start elsewhere.
const otherOwner storage.Owner = "core/config"

// env is a booted store with the c/secrets/ keyspace and a state directory.
type env struct {
	db    *storage.DB
	ks    storage.Keyspace
	other storage.Keyspace
	dir   string
	root  *os.Root
	clock *clock
	logs  *logBuffer
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// logBuffer collects the store's log lines, so tests can assert that no
// secret value reaches one.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// newEnv boots a fresh store in memory and a fresh state directory. It boots
// as coretest.Store does (storage.Open, Prepare, Migrate); coretest reaches
// the kernel, which imports secrets.
func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: otherOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(secretsOwner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", secretsOwner)
	}
	other, ok := db.Keyspace(otherOwner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", otherOwner)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &env{
		db: db, ks: ks, other: other, dir: dir, root: root,
		clock: &clock{t: time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)},
		logs:  &logBuffer{},
	}
}

// opts is the Options the kernel would pass for e.
func (e *env) opts() Options {
	return Options{
		StateDir:  e.root,
		Keyspace:  e.ks,
		AppID:     testAppID,
		InstallID: testInstallID,
		Logger:    slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:       e.clock.now,
	}
}

// open opens the store with e's options changed by mut, fatal on error, and
// closes it at cleanup.
func (e *env) open(t testing.TB, mut func(*Options)) *Store {
	t.Helper()
	o := e.opts()
	if mut != nil {
		mut(&o)
	}
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// keyringPath is the default keyring's path.
func (e *env) keyringPath() string { return filepath.Join(e.dir, defaultKeyFile) }

// readKeyringFile decodes the default keyring file.
func (e *env) readKeyringFile(t testing.TB) *keyringFile {
	t.Helper()
	f, err := readKeyring(e.root, defaultKeyFile)
	if err != nil {
		t.Fatalf("read the keyring: %v", err)
	}
	return f
}

// fileBytes is the content of path, fatal on error.
func fileBytes(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rows is every key and value of ks.
func rows(t testing.TB, ks storage.Keyspace) map[string]string {
	t.Helper()
	out := map[string]string{}
	for kv, err := range ks.Scan(context.Background(), storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		out[string(kv.Key)] = string(kv.Value)
	}
	return out
}

// rawSet writes value at key of ks directly, with the Expect c/secrets/
// needs: a test's stand-in for a tampering or a restore.
func rawSet(t testing.TB, ks storage.Keyspace, key, value []byte) {
	t.Helper()
	ctx := context.Background()
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := b.GetForUpdate(ctx, ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if err := b.Set(key, value); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// rawGet reads key of ks, nil when absent.
func rawGet(t testing.TB, ks storage.Keyspace, key []byte) []byte {
	t.Helper()
	v, err := ks.Get(context.Background(), key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// mustPut puts value under n, fatal on error.
func mustPut(t testing.TB, s *Store, n Name, value string) Meta {
	t.Helper()
	m, err := s.Put(context.Background(), n, NewValue(value), byAlfred)
	if err != nil {
		t.Fatalf("Put %s: %v", n, err)
	}
	return m
}

// mustGet reads n and reports whether it holds want (never printing either).
func mustGet(t testing.TB, s *Store, n Name, want string) Meta {
	t.Helper()
	v, m, err := s.Get(context.Background(), n)
	if err != nil {
		t.Fatalf("Get %s: %v", n, err)
	}
	if v.Reveal() != want {
		t.Fatalf("Get %s returned another value than the one put", n)
	}
	return m
}

// assertNoPlaintext fails when any fixture value appears in text.
func assertNoPlaintext(t testing.TB, where, text string) {
	t.Helper()
	for _, v := range []string{valueVengeance, valueSerious, valueClock} {
		if bytes.Contains([]byte(text), []byte(v)) {
			t.Errorf("%s holds a secret value in plaintext", where)
		}
	}
}
