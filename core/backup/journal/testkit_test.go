// file: core/backup/journal/testkit_test.go
// version: 1.0.0
// guid: 6c0a4c84-1d52-4f0c-a1e5-0f4d2b7e9c31
// last-edited: 2026-11-11

package journal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// installID is the store identity every fixture journal names.
const installID = "wayne-manor-install"

// authOwner stands in for core/auth, the revoking subsystem whose batch the
// journal joins.
const authOwner storage.Owner = "core/auth"

// testKeys is a keyring with derive keys by ID (the secrets store's
// DeriveKeyFor, without importing secrets): a retired key stays unknown to the
// journal, as a keyring that dropped it does.
type testKeys struct {
	mu     sync.Mutex
	active string
	secret map[string][]byte
}

func newTestKeys() *testKeys {
	return &testKeys{active: "d1", secret: map[string][]byte{"d1": []byte("alfred-pennyworth-d1")}}
}

func (k *testKeys) ActiveDeriveID() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.active
}

func (k *testKeys) DeriveKeyFor(kid, info string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	s, ok := k.secret[kid]
	if !ok {
		return nil, fmt.Errorf("derive key %q: key unavailable", kid)
	}
	h := hmac.New(sha256.New, s)
	h.Write([]byte(info))
	return h.Sum(nil), nil
}

// rotate makes kid the active derive key.
func (k *testKeys) rotate(kid string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.secret[kid] = []byte("alfred-pennyworth-" + kid)
	k.active = kid
}

// retire drops kid from the keyring.
func (k *testKeys) retire(kid string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.secret, kid)
}

// fixture is a store with c/backup/ and c/auth/, booted the way the kernel's
// P2, P4a and P4b do (as coretest.Store does; coretest is not imported
// because it imports kernel, which will import this package).
type fixture struct {
	db   *storage.DB
	ks   storage.Keyspace // c/backup/
	auth storage.Keyspace // c/auth/
	path string           // the journal file
	keys *testKeys
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(dir, "storage", "main.pebble"), Build: "batcomputer-1", FreeSpaceMargin: 1,
		Owners: []storage.OwnerSpec{
			{Owner: Owner, Spec: StorageSpec(), Enabled: true},
			{Owner: authOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, _ := db.Keyspace(Owner)
	auth, _ := db.Keyspace(authOwner)
	return &fixture{db: db, ks: ks, auth: auth, path: filepath.Join(dir, "backups", "gotham_revocations.journal"), keys: newTestKeys()}
}

// opts is FileOptions for fx's journal.
func (fx *fixture) opts() FileOptions {
	return FileOptions{Path: fx.path, Keyspace: fx.ks, Source: "batcomputer-1", Keys: fx.keys, InstallID: installID, App: "gotham",
		Clock: func() time.Time { return time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC) }}
}

func (fx *fixture) open(t *testing.T) *File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fx.path), 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenFile(context.Background(), fx.opts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

// head reads the committed c/backup/journal_head.
func (fx *fixture) head(t *testing.T) Head {
	t.Helper()
	h, err := ReadHead(context.Background(), fx.ks)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// lines reads the journal file's lines.
func (fx *fixture) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// chain reads and verifies the journal file with fx's keys.
func (fx *fixture) chain(t *testing.T) *Chain {
	t.Helper()
	c, err := ReadFile(fx.path, fx.keys)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// write replaces the journal file's content.
func (fx *fixture) write(t *testing.T, lines ...string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	must(t, os.WriteFile(fx.path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// revoke is a revoking batch of core/auth: the user row and the journal line.
func (fx *fixture) revoke(t *testing.T, j RevocationJournal, user string) *storage.Batch {
	t.Helper()
	b := fx.auth.NewBatch()
	if err := b.Set([]byte("user/"+user), []byte("disabled")); err != nil {
		t.Fatal(err)
	}
	r := Revocation{At: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC), Kind: UserDisabled,
		Subject: entity.Ref{Module: "core", Kind: "user", ID: user}}
	if err := j.Append(context.Background(), b, r); err != nil {
		t.Fatal(err)
	}
	return b
}

// commitRevoke revokes user and commits.
func (fx *fixture) commitRevoke(t *testing.T, j RevocationJournal, user string) {
	t.Helper()
	b := fx.revoke(t, j, user)
	must(t, b.Commit(context.Background(), storage.Sync))
}

// openBroken opens the journal expecting a broken File with an error matching
// want and checks it the way §5B.5 needs it (the app stays open on a broken
// journal): it holds the lock, its Append fails with the open's error and
// stages nothing, and its Gap records journal_gap/<since> so the revocation
// still commits.
func (fx *fixture) openBroken(t *testing.T, what string, want error) {
	t.Helper()
	ctx := context.Background()
	j, err := OpenFile(ctx, fx.opts())
	if !errors.Is(err, want) || j == nil {
		if j != nil {
			_ = j.Close()
		}
		t.Fatalf("%s: OpenFile = %v, %v; want a broken File and %v", what, j, err, want)
	}
	defer func() { must(t, j.Close()) }()
	if j2, err := OpenFile(ctx, fx.opts()); !errors.Is(err, ErrLocked) {
		if j2 != nil {
			_ = j2.Close()
		}
		t.Errorf("%s: a second opener of the broken journal: err = %v, want ErrLocked", what, err)
	}
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/bane"), []byte("disabled")))
	if err := j.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, want) {
		t.Errorf("%s: Append on the broken journal: err = %v, want %v", what, err, want)
	}
	if b.Count() != 1 {
		t.Errorf("%s: the refused Append staged into the batch: count %d", what, b.Count())
	}
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	must(t, j.Gap(b, at))
	must(t, b.Commit(ctx, storage.Sync))
	if v, err := fx.ks.Get(ctx, gapKey(at)); err != nil || !strings.Contains(string(v), `"reason":"append_failed"`) {
		t.Errorf("%s: the gap record: %q, %v", what, v, err)
	}
	clear := fx.ks.NewBatch()
	must(t, clear.Delete(gapKey(at)))
	must(t, clear.Commit(ctx, storage.Sync))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
