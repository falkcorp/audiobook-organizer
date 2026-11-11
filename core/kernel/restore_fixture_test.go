// file: core/kernel/restore_fixture_test.go
// version: 1.0.0
// guid: 4c9a7e31-b258-4d06-93f1-6e0d2a8b7c15
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// rxApp is the archive prefix every fixture journal carries.
const rxApp = "gotham"

// rxKeys is a keyring with derive keys by ID, the secrets store's DeriveKeyFor
// without the secrets store: a retired key stays unknown to the journal.
type rxKeys struct {
	mu     sync.Mutex
	active string
	secret map[string][]byte
}

func newRxKeys() *rxKeys {
	return &rxKeys{active: "d1", secret: map[string][]byte{"d1": []byte("alfred-pennyworth-d1")}}
}

func (k *rxKeys) ActiveDeriveID() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.active
}

func (k *rxKeys) DeriveKeyFor(kid, info string) ([]byte, error) {
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

// rxClock is a clock that moves one second per reading, so every line and
// record has its own time.
type rxClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *rxClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// rxFx is one install: a store over a MemFS (the journal and state directory
// are real, so rename, flock and mode behave as in production), the journal
// component bound to it and the auth, rbac and audit handles the restore
// hooks drive. The kernel's own boot is not run (coretest cannot be imported
// here, and F4-08 owns that wiring); the tests call reapplyRevocations and
// runRestoreHooks with inputs the way F4-08 will.
type rxFx struct {
	t      *testing.T
	ctx    context.Context
	vfs    vfs.FS
	fs     storage.FS
	secret bool // the c/secrets/ owner is registered (a real keyring)
	db     *storage.DB
	dbPath string
	state  string
	backup string
	keys   journal.Keys
	clock  *rxClock

	install string
	comp    *journal.Component
	auth    *authstore.Store
	roles   *rbacstore.Store
	aud     *audit.Store
}

// newRxFx boots a store at /gotham/main.pebble with every owner step 2 and 3
// touch, and binds the journal component to it. keys nil is the fixture keyring.
func newRxFx(t *testing.T, keys journal.Keys) *rxFx {
	t.Helper()
	if keys == nil {
		keys = newRxKeys()
	}
	mem := vfs.NewMem()
	fx := &rxFx{
		t: t, ctx: context.Background(), vfs: mem, fs: storage.FSOf(mem), dbPath: "/gotham/main.pebble",
		state: t.TempDir(), backup: filepath.Join(t.TempDir(), "backups"), keys: keys,
		clock: &rxClock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)},
	}
	if err := os.MkdirAll(fx.backup, 0o700); err != nil {
		t.Fatal(err)
	}
	fx.attach(fx.openDB(fx.dbPath), fx.dbPath)
	return fx
}

func (fx *rxFx) owners() []storage.OwnerSpec {
	o := rxOwners()
	if fx.secret {
		o = append(o, secretsOwnerSpec())
	}
	return o
}

func rxOwners() []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: journal.Owner, Spec: journal.StorageSpec(), Enabled: true},
		{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
		{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		auditOwnerSpec(),
	}
}

// openDB boots the store at path the way P2, P4a and P4b do.
func (fx *rxFx) openDB(path string) *storage.DB {
	fx.t.Helper()
	o, err := storage.Open(fx.ctx, storage.OpenOptions{Path: path, Owners: fx.owners(), Build: "batcomputer-1", FreeSpaceMargin: 1, FS: fx.fs})
	if err != nil {
		fx.t.Fatalf("open %s: %v", path, err)
	}
	db, err := o.Prepare(fx.ctx)
	if err == nil {
		err = db.Migrate(fx.ctx)
	}
	if err != nil {
		_ = o.Close()
		fx.t.Fatalf("boot %s: %v", path, err)
	}
	fx.t.Cleanup(func() { _ = db.Close() })
	return db
}

// attach makes db the store the fixture serves: a new journal component, as
// after a process restart, and fresh handles.
func (fx *rxFx) attach(db *storage.DB, path string) {
	fx.t.Helper()
	if fx.comp != nil {
		_ = fx.comp.Close(fx.ctx)
	}
	fx.db, fx.dbPath = db, path
	fx.install = db.Admin().InstallID()
	ks, ok := db.Keyspace(journal.Owner)
	if !ok {
		fx.t.Fatal("no c/backup/ keyspace")
	}
	comp, err := journal.NewComponent(journal.ComponentOptions{
		StateDir: fx.state, DBPath: path, Keyspace: ks, Keys: fx.keys, InstallID: fx.install, App: rxApp,
		Clock: fx.clock.now, Tick: time.Hour, Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.comp = comp
	fx.t.Cleanup(func() { _ = comp.Close(fx.ctx) })
	aks, _ := db.Keyspace(authstore.Owner)
	if fx.auth, err = authstore.New(aks, authstore.Options{Now: fx.clock.now}); err != nil {
		fx.t.Fatal(err)
	}
	rks, _ := db.Keyspace(rbacstore.Owner)
	if fx.roles, err = rbacstore.New(rks, rbacstore.Options{Now: fx.clock.now}); err != nil {
		fx.t.Fatal(err)
	}
	if fx.aud, err = buildAudit(fx.ctx, db, slog.New(slog.DiscardHandler), fx.clock.now); err != nil {
		fx.t.Fatal(err)
	}
}

// restart is a process restart over the same store: the component is closed
// (it releases the journal's lock and writes the anchor) and a new one built.
func (fx *rxFx) restart() { fx.attach(fx.db, fx.dbPath) }

func (fx *rxFx) bks() storage.Keyspace {
	ks, _ := fx.db.Keyspace(journal.Owner)
	return ks
}

func (fx *rxFx) journalPath() string { return filepath.Join(fx.backup, "gotham_revocations.journal") }

func (fx *rxFx) stores() hookStores {
	return hookStores{Auth: fx.auth, Roles: fx.roles, Audit: fx.aud, Journal: fx.comp, Clock: fx.clock.now, Log: slog.New(slog.DiscardHandler)}
}

// input is step 2's input for this boot; mut adjusts it.
func (fx *rxFx) input(mut func(*reapplyInput)) reapplyInput {
	in := reapplyInput{
		Journal: fx.comp, KS: fx.bks(), Keys: fx.keys, Overrides: true, Install: fx.install, App: rxApp,
		Identity: fx.db.Admin().StoreIdentity(), DBPath: fx.dbPath, StateDir: fx.state, BackupDir: fx.backup,
		JournalPath: fx.journalPath(), Apply: reapplyApplyFor(fx.stores()), Clock: fx.clock.now,
		Log: slog.New(slog.DiscardHandler),
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

// boot runs P4d steps 2 and 3 for this boot (rb nil is a plain boot) and
// returns both results; a failure is the test's.
func (fx *rxFx) boot(rb *restoreBoot, mut func(*reapplyInput)) (reapplyResult, restoreHooksResult) {
	fx.t.Helper()
	res, hres, err := fx.tryBoot(rb, mut, nil)
	if err != nil {
		fx.t.Fatalf("boot: %v", err)
	}
	return res, hres
}

// tryBoot is boot that returns the error; hooks, when non-nil, replaces the
// hook list.
func (fx *rxFx) tryBoot(rb *restoreBoot, mut func(*reapplyInput), hooks func(env hookEnv) ([]journal.RestoreHook, error)) (reapplyResult, restoreHooksResult, error) {
	fx.t.Helper()
	in := fx.input(mut)
	in.Restore = rb
	res, err := reapplyRevocations(fx.ctx, in)
	if err != nil {
		return res, restoreHooksResult{}, err
	}
	if hooks == nil {
		hooks = restoreHookList(fx.stores())
	}
	hres, err := runRestoreHooks(fx.ctx, restoreHooksInput{
		Journal: fx.comp, KS: fx.bks(), Install: fx.install, Identity: in.Identity, Result: res, Restore: rb,
		SwappedBack: in.SwappedBack, OpenPrevious: fx.openPrevious, Hooks: hooks, Clock: fx.clock.now,
		Log: slog.New(slog.DiscardHandler),
	})
	return res, hres, err
}

// openPrevious opens a pre-swap store directory read-only through the live store.
func (fx *rxFx) openPrevious(ctx context.Context, dir string) (previousView, error) {
	return fx.db.Admin().OpenReadOnly(ctx, dir)
}

// ---------------------------------------------------------------------------
// Seeding and reading rows.

func rxUserRef(id string) entity.Ref { return entity.Ref{Module: "core", Kind: "user", ID: id} }
func rxKeyRef(id string) entity.Ref  { return entity.Ref{Module: "core", Kind: "apikey", ID: id} }

// user stores an active user at version 1 and returns its ID.
func (fx *rxFx) user(name string, roles ...rbac.RoleName) string {
	fx.t.Helper()
	id := "u-" + name
	u := &auth.User{ID: auth.UserID(id), Username: name, Roles: roles, Status: auth.StatusActive,
		CreatedAt: fx.clock.now(), UpdatedAt: fx.clock.now(), Version: 1}
	fx.commit(func(b *storage.Batch) error { return fx.auth.PutUser(b, u) })
	return id
}

// key stores an API key under an explicit ID.
func (fx *rxFx) key(id, owner string, st auth.KeyStatus) {
	fx.t.Helper()
	fx.commit(func(b *storage.Batch) error {
		return fx.auth.ImportAPIKey(b, &auth.APIKey{ID: id, Owner: auth.UserID(owner), Name: "utility belt " + id,
			Scopes: []rbac.Permission{"gotham:patrol"}, Status: st, CreatedAt: fx.clock.now(),
			ExpiresAt: fx.clock.now().Add(48 * time.Hour), Imported: true}, auth.HashCredential(auth.Token("abk_"+id)))
	})
}

func (fx *rxFx) commit(stage func(b *storage.Batch) error) {
	fx.t.Helper()
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	if err := stage(b); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *rxFx) keyStatus(id string) auth.KeyStatus {
	fx.t.Helper()
	k, err := fx.auth.APIKeyByID(fx.ctx, id)
	if err != nil {
		fx.t.Fatalf("APIKeyByID %s: %v", id, err)
	}
	return k.Status
}

func (fx *rxFx) userRow(id string) *auth.User {
	fx.t.Helper()
	u, err := fx.auth.GetUser(fx.ctx, auth.UserID(id))
	if err != nil {
		fx.t.Fatalf("GetUser %s: %v", id, err)
	}
	return u
}

// revokeKey is what auth does for a key revoke: the journal line first, then
// the batch that changes the key and stages the head, committed. It returns the
// line's seq.
func (fx *rxFx) revokeKey(id string) uint64 {
	fx.t.Helper()
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	k, err := fx.auth.APIKeyByID(fx.ctx, id)
	if err != nil {
		fx.t.Fatal(err)
	}
	k.Status, k.RevokedAt, k.Version = auth.KeyRevoked, fx.clock.now(), k.Version+1
	if err := fx.auth.PutAPIKey(b, k); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.comp.Append(fx.ctx, b, journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef(id), At: fx.clock.now()}); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
	return fx.head().Seq
}

// journalOnly is a crash after the line's fsync and before the revoking batch
// commits: the line is in the journal and nothing reached the store, head
// included. It returns the line's seq.
func (fx *rxFx) journalOnly(r journal.Revocation) uint64 {
	fx.t.Helper()
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }() // never committed
	r.At = fx.clock.now()
	if err := fx.comp.Append(fx.ctx, b, r); err != nil {
		fx.t.Fatal(err)
	}
	return fx.comp.Tail().Seq
}

func (fx *rxFx) head() journal.Head {
	fx.t.Helper()
	h, err := journal.ReadHead(fx.ctx, fx.bks())
	if err != nil {
		fx.t.Fatal(err)
	}
	return h
}

func (fx *rxFx) pair() journal.ServedBoot {
	fx.t.Helper()
	p, err := journal.ReadServedBoot(fx.ctx, fx.bks())
	if err != nil {
		fx.t.Fatal(err)
	}
	return p
}

// chain reads the journal file as the next boot would.
func (fx *rxFx) chain() *journal.Chain {
	fx.t.Helper()
	c, err := journal.ReadFile(fx.journalPath(), fx.keys)
	if err != nil {
		fx.t.Fatal(err)
	}
	return c
}

// kinds lists the kinds of the verified lines after the header.
func (fx *rxFx) kinds() []journal.Kind {
	fx.t.Helper()
	var out []journal.Kind
	for _, l := range fx.chain().Lines {
		out = append(out, l.Kind)
	}
	return out
}

func (fx *rxFx) location() journal.Location {
	fx.t.Helper()
	l, ok, err := journal.ReadLocation(fx.state)
	if err != nil || !ok {
		fx.t.Fatalf("ReadLocation: %v, present %v", err, ok)
	}
	return l
}

func (fx *rxFx) hooked(seq uint64) bool {
	fx.t.Helper()
	ok, err := journal.Hooked(fx.ctx, fx.bks(), seq)
	if err != nil {
		fx.t.Fatal(err)
	}
	return ok
}

// health lists the component's tokens.
func (fx *rxFx) health() []string {
	var out []string
	for _, h := range fx.comp.Health() {
		out = append(out, h.Token)
	}
	slices.Sort(out)
	return out
}

func (fx *rxFx) hasHealth(token string) bool { return slices.Contains(fx.health(), token) }

// restoreLines are the restore lines of the journal as the next boot reads them.
func (fx *rxFx) restoreLines() []journal.Line {
	fx.t.Helper()
	var out []journal.Line
	for _, l := range fx.chain().Lines {
		if l.Kind == journal.Restore {
			out = append(out, l)
		}
	}
	return out
}

// rxSteps is a restoreJournal that records the edges walked, in order, and
// reads the journal's markers at each so a test can assert the order of the
// hooks, served_boot and the location file against the restore journal.
type rxSteps struct {
	mu     sync.Mutex
	events []string
	on     func(event string)
	fail   map[string]error
}

func (s *rxSteps) record(e string) error {
	s.mu.Lock()
	s.events = append(s.events, e)
	fail := s.fail[e]
	on := s.on
	s.mu.Unlock()
	if on != nil {
		on(e)
	}
	return fail
}

func (s *rxSteps) RestoreLine(_ context.Context, _ string, seq uint64) error {
	return s.record(fmt.Sprintf("restore_line:%d", seq))
}
func (s *rxSteps) HooksDone(context.Context, string) error     { return s.record("hooks_done") }
func (s *rxSteps) MarkerWritten(context.Context, string) error { return s.record("marker_written") }
func (s *rxSteps) HooksApplied(context.Context, string) error  { return s.record("applied") }

func (s *rxSteps) log() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.events, ",")
}

// archiveBoot is a staged archive restore of this install.
func archiveBoot(id, archive, previous string, steps restoreJournal) *restoreBoot {
	return &restoreBoot{ID: id, Kind: journal.KindArchive, ArchiveID: archive, PreSwapDir: previous, Steps: steps}
}

// snapshot checkpoints the live store into /snap/<name> and returns the
// directory: an archive or checkpoint of the store as it is now, or the
// pre-swap store of a restore about to happen.
func (fx *rxFx) snapshot(name string) string {
	fx.t.Helper()
	dir := "/snap/" + name
	if err := fx.vfs.MkdirAll("/snap", 0o700); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.db.Admin().Checkpoint(fx.ctx, dir); err != nil {
		fx.t.Fatalf("checkpoint %s: %v", dir, err)
	}
	return dir
}

// swapIn replaces the live store with the snapshot dir, in place at the same
// path: a staged restore's swap, or a file copy of an old store over the live
// one. The component is closed and a new one bound to the reopened store, as a
// new process would.
func (fx *rxFx) swapIn(snap string) {
	fx.t.Helper()
	if fx.comp != nil {
		_ = fx.comp.Close(fx.ctx)
	}
	if err := fx.db.Close(); err != nil {
		fx.t.Fatalf("close the live store: %v", err)
	}
	if err := fx.vfs.RemoveAll(fx.dbPath); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.vfs.Rename(snap, fx.dbPath); err != nil {
		fx.t.Fatalf("swap %s in: %v", snap, err)
	}
	fx.attach(fx.openDB(fx.dbPath), fx.dbPath)
}

// setLocation rewrites backup.location.json, as an older run of the kernel (or
// an anchor tick since) left it.
func (fx *rxFx) setLocation(mut func(*journal.Location)) {
	fx.t.Helper()
	loc := fx.location()
	mut(&loc)
	if err := journal.WriteLocation(fx.state, loc); err != nil {
		fx.t.Fatal(err)
	}
}

// newRxFxSecrets is newRxFx with the keyring the secrets store derives the
// journal's keys from (P4b's openSecrets over c/secrets/), instead of the
// fixture's.
func newRxFxSecrets(t *testing.T) *rxFx {
	t.Helper()
	mem := vfs.NewMem()
	fx := &rxFx{
		t: t, ctx: context.Background(), vfs: mem, fs: storage.FSOf(mem), dbPath: "/gotham/main.pebble", secret: true,
		state: t.TempDir(), backup: filepath.Join(t.TempDir(), "backups"),
		clock: &rxClock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)},
	}
	if err := os.MkdirAll(fx.backup, 0o700); err != nil {
		t.Fatal(err)
	}
	db := fx.openDB(fx.dbPath)
	fx.keys = openTestSecrets(t, db, fx.state, nil).store
	fx.attach(db, fx.dbPath)
	return fx
}

// editJournalLine rewrites line idx of the journal file (0 is the header),
// replacing from with to in its text: a hand edit the MAC must catch.
func editJournalLine(t *testing.T, path string, idx int, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if idx >= len(lines) || !strings.Contains(lines[idx], from) {
		t.Fatalf("line %d of the journal does not contain %q: %q", idx, from, lines)
	}
	lines[idx] = strings.Replace(lines[idx], from, to, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dropJournalLine deletes line idx of the journal file.
func dropJournalLine(t *testing.T, path string, idx int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	lines = slices.Delete(lines, idx, idx+1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}
