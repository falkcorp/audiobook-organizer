// file: internal/books/cutover/identity_rig_test.go
// version: 1.0.0
// guid: 740040f4-4986-45bf-b6ac-2aa31a78a7b9
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
)

// The M3 test rig: books' R2 corpus (F2-12's stage 1, F2-13's overlay and
// blocker stores), copied into an in-memory filesystem, cut over by M3 alone
// through the kernel's data-migration runner and its P4a preflight, never
// through books.Module(). The cutover build is books' real storage spec
// raised to its cutover level behind books' level step with the identity
// families' claims retired, so M3's Source is the one a registered step has
// (§5S.3.13). Core's auth and rbac importers are the doubles of
// identity_doubles_test.go over three keyspaces.

const (
	idOwnerAuth  = "core/auth"
	idOwnerRBAC  = "core/rbac"
	idOwnerAudit = "core/audit"
)

var idNow = time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)

const idKeyTTL = 90 * 24 * time.Hour

// idAtoms is books' atom table (05-rbac §6.1) as a fixture. internal/books
// holds the real one, which this package cannot import (books will import
// this one); TestIdentityFixture_PinsBooksAtomTable pins the two together.
func idAtoms() []m3.AtomRow {
	row := func(n string, s ...rbac.Permission) m3.AtomRow { return m3.AtomRow{Name: n, Scopes: s} }
	return []m3.AtomRow{
		row("library.view", "books:view", "core:tasks.view"),
		row("library.edit_metadata", "books:edit"),
		row("library.delete", "books:delete"),
		row("library.organize", "books:organize"),
		row("scan.trigger", "books:scan"),
		row("integrations.manage", "core:integrations.manage", "books:integrations.manage"),
		row("users.manage", "core:users.manage", "core:roles.view"),
		row("settings.manage", "core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"),
		row("playlists.create", "books:playlists.create"),
		row("collections.manage", "books:collections.manage"),
		row("requests.create", "books:request"),
		row("requests.approve", "books:request.approve"),
		row("fingerprint.worker", "books:fingerprint.worker"),
	}
}

// idSeedRoles are HEAD's four seed roles with the native sets the cutover
// build's seed grants them (the shape F4-08 resolves from books' DefaultGrants
// and core's seed).
func idSeedRoles() []m3.SeedRole {
	member := []rbac.Permission{"core:self", "core:tasks.view", "core:system.status", "core:metrics.read", "books:reading", "books:view", "books:request"}
	return []m3.SeedRole{
		{ID: "admin", Permissions: []rbac.Permission{rbac.CoreAdmin}},
		{ID: "editor", Permissions: append(slices.Clone(member), "books:edit", "books:organize", "books:scan", "books:playlists.create", "books:collections.manage")},
		{ID: "viewer", Permissions: member},
		{ID: "fp-worker", Permissions: []rbac.Permission{"books:fingerprint.worker"}},
	}
}

func idAmbient() []rbac.Permission {
	return []rbac.Permission{"core:self", "core:metrics.read", "books:reading"}
}

// idCatalog is core's scopes and books' (05-rbac §6.1): books:fingerprint.worker
// is the one Confining scope.
func idCatalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	specs := rbac.CoreScopes()
	for _, n := range []string{"view", "edit", "delete", "organize", "scan", "integrations.manage", "settings.manage", "playlists.create",
		"collections.manage", "request", "request.approve", "reading", "admin.view", "admin.debug"} {
		specs = append(specs, rbac.PermissionSpec{Name: rbac.Permission("books:" + n), Title: n, Description: "books " + n})
	}
	specs = append(specs, rbac.PermissionSpec{Name: "books:fingerprint.worker", Title: "worker", Description: "fingerprint worker", Confining: true})
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// idHandles are what a boot's M3 reaches, set between Prepare and Migrate.
type idHandles struct {
	mu     sync.RWMutex
	auth   *idAuth
	roles  *idRoles
	audit  *idAudit
	legacy storage.Reader
}

func (h *idHandles) get() (*idAuth, *idRoles, *idAudit, storage.Reader) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.auth, h.roles, h.audit, h.legacy
}

type lazyAuth struct{ h *idHandles }

func (l lazyAuth) ImportUser(b *storage.Batch, u *auth.User, hash string) error {
	a, _, _, _ := l.h.get()
	return a.ImportUser(b, u, hash)
}

func (l lazyAuth) ImportAPIKey(b *storage.Batch, k *auth.APIKey, sum [32]byte) error {
	a, _, _, _ := l.h.get()
	return a.ImportAPIKey(b, k, sum)
}

func (l lazyAuth) ImportIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	a, _, _, _ := l.h.get()
	return a.ImportIdentity(b, id)
}

func (l lazyAuth) ImportInviteAllow(b *storage.Batch, email string) error {
	a, _, _, _ := l.h.get()
	return a.ImportInviteAllow(b, email)
}

type lazyRoles struct{ h *idHandles }

func (l lazyRoles) ImportRole(b *storage.Batch, r *rbac.Role) error {
	_, ro, _, _ := l.h.get()
	return ro.ImportRole(b, r)
}

type lazyNative struct{ h *idHandles }

func (l lazyNative) GetUser(ctx context.Context, id auth.UserID) (*auth.User, error) {
	a, _, _, _ := l.h.get()
	return a.GetUser(ctx, id)
}

func (l lazyNative) GetRole(ctx context.Context, n rbac.RoleName) (*rbac.Role, error) {
	_, ro, _, _ := l.h.get()
	return ro.GetRole(ctx, n)
}

type lazyAudit struct{ h *idHandles }

func (l lazyAudit) RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error {
	_, _, au, _ := l.h.get()
	return au.RecordIn(ctx, b, e)
}
func (lazyAudit) Record(context.Context, audit.Event) error { return errors.New("not used by M3") }
func (lazyAudit) Emit(context.Context, audit.Event)         {}

// idRig is one corpus store and its M3.
type idRig struct {
	t          testing.TB
	name       string // the corpus store: "stage1", "overlay", "blockers/<case>"
	srcDB      string
	h          *idHandles
	logs       *lockedBuffer
	chunkItems int
	mutate     func(t testing.TB, lr storage.LegacyRange)
	// config edits the m3.Config before the step is built.
	config func(*m3.Config)
	// preMigrate runs with the stores open, before the data steps.
	preMigrate func(ctx context.Context, e *idEnv) error
}

func newIDRig(t testing.TB, name string, opts ...func(*idRig)) *idRig {
	t.Helper()
	src := filepath.Join(repoRoot(t), filepath.FromSlash(corpusRel), filepath.FromSlash(name))
	r := &idRig{t: t, name: name, srcDB: filepath.Join(src, "db"), h: &idHandles{}, logs: &lockedBuffer{}, chunkItems: 1}
	for _, o := range opts {
		o(r)
	}
	return r
}

// spec is books' storage spec for the build: the pre-cutover one (every family
// read-write), or the cutover build's with books at its cutover level behind
// books' level step and the identity families' claims retired.
func (r *idRig) spec(cutover bool) storage.Spec {
	spec := store.BooksSpec(nil)
	if !cutover {
		return spec
	}
	spec.Level = BooksCutoverLevel
	spec.Migrations = append(spec.Migrations, BooksLevelStep(r.levelVersion(), BooksCutoverLevel))
	retired := m3.SourcePrefixes()
	for i := range spec.Claims {
		if slices.Contains(retired, spec.Claims[i].Prefix) {
			spec.Claims[i].Mode = storage.ClaimRetired
		}
	}
	// Ownership table row 8: the two bootstrap rows are exact-key claims inside
	// the read-write setting: family, retired in the cutover build. booksclaims.go
	// declares no claim for them at R1, so the flip (F4-08) adds these two.
	for _, k := range m3.SourceKeys() {
		spec.Claims = append(spec.Claims, storage.Claim{Key: k, Mode: storage.ClaimRetired, Serialized: true, Description: "HEAD's bootstrap row (ownership table row 8)"})
	}
	return spec
}

func (r *idRig) levelVersion() int {
	last := 0
	for _, m := range store.BooksSpec(nil).Migrations {
		last = max(last, m.Version)
	}
	return last + 1
}

func (r *idRig) owners(cutover bool) []storage.OwnerSpec {
	ks := storage.Spec{Keyspace: true, Serialized: []string{""}}
	return []storage.OwnerSpec{
		{Owner: BooksOwner, Spec: r.spec(cutover), Enabled: true},
		{Owner: idOwnerAuth, Spec: ks, Enabled: true},
		{Owner: idOwnerRBAC, Spec: ks, Enabled: true},
		// Audit's own keyspace is not Serialized: its rows are appended, never
		// updated, so a write carries no Expect (the real audit store's shape).
		{Owner: idOwnerAudit, Spec: storage.Spec{Keyspace: true}, Enabled: true},
	}
}

// migration is M3 over this rig, built the way F4-08 builds it.
func (r *idRig) migration() datamigrate.DataMigration {
	cfg := m3.Config{
		Atoms: idAtoms(), SeedRoles: idSeedRoles(), Catalog: idCatalog(r.t), Ambient: idAmbient(),
		Auth: lazyAuth{r.h}, Roles: lazyRoles{r.h}, Native: lazyNative{r.h}, Audit: lazyAudit{r.h},
		Legacy: func() storage.Reader { _, _, _, l := r.h.get(); return l },
		Now:    func() time.Time { return idNow }, KeyTTL: idKeyTTL,
		Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if r.config != nil {
		r.config(&cfg)
	}
	dm, err := IdentityStep(cfg, BooksCutoverLevel, BooksLevelStepRef(r.levelVersion()))
	if err != nil {
		r.t.Fatal(err)
	}
	return dm
}

// seed writes the pre-cutover store on fsys: the corpus's Pebble directory and
// its sidecar, then r.mutate over books' legacy range.
func (r *idRig) seed(t testing.TB, fsys vfs.FS) {
	t.Helper()
	if err := fsys.MkdirAll(rigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"audiobooks.pebble", "audiobooks.pebble.storage-format"} {
		if _, err := vfs.Clone(vfs.Default, fsys, filepath.Join(r.srcDB, n), rigDir+"/"+n); err != nil {
			t.Fatalf("copy the corpus store %s: %v", n, err)
		}
	}
	// Always boot once as the pre-freeze build: the store a release served has
	// its install marker (storage cutover-blockers refuses one without), and
	// r.mutate then edits books' legacy range.
	{
		ctx := context.Background()
		o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(fsys), Owners: r.owners(false), Build: rigBuildOld, FreeSpaceMargin: 1, Clock: crashmatrix.Now})
		if err != nil {
			t.Fatalf("seed open: %v", err)
		}
		db, err := o.Prepare(ctx)
		if err == nil {
			err = db.Migrate(ctx)
		}
		if err != nil {
			t.Fatalf("seed boot: %v", errors.Join(err, o.Close()))
		}
		lr, _ := db.Legacy(BooksOwner)
		if r.mutate != nil {
			r.mutate(t, lr)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("seed close: %v", err)
		}
	}
	syncTree(t, fsys, rigDir)
}

func (r *idRig) newMem(t testing.TB) *vfs.MemFS {
	t.Helper()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(rigDiskUsage())
	r.seed(t, mem)
	return mem
}

// idEnv is what an observer can read: the store and the three doubles'
// keyspaces.
type idEnv struct {
	db                 *storage.DB
	authKS, rbacKS, au storage.Keyspace
	h                  *idHandles
}

// open is one boot's P2 and P4a over fsys: the store opened with the data
// steps and the cutover preflight run. A blocker is returned with the store
// closed and nothing written.
func (r *idRig) open(ctx context.Context, fsys vfs.FS, migs []datamigrate.DataMigration) (*storage.Opened, error) {
	owners := r.owners(true)
	cfg := kernel.DataMigrationsConfig{ChunkItems: r.chunkItems, Build: rigBuildNew, Logger: slog.New(slog.DiscardHandler),
		Quarantine: storage.QuarantineWriter(nil), Preflight: kernel.CutoverPreflight(slog.New(slog.NewTextHandler(r.logs, nil)))}
	dms, err := kernel.NewDataMigrations(cfg, owners, migs...)
	if err != nil {
		return nil, err
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(fsys), Owners: owners, Build: rigBuildNew,
		FreeSpaceMargin: 1, DataSteps: dms.Steps(), Clock: crashmatrix.Now})
	if err != nil {
		return nil, err
	}
	if err := dms.Preflight(ctx, o); err != nil {
		return nil, errors.Join(err, o.Close())
	}
	return o, nil
}

// boot is one boot of the cutover build over fsys with migs (P2, P4a, P4b).
func (r *idRig) boot(ctx context.Context, fsys vfs.FS, migs []datamigrate.DataMigration, observe func(ctx context.Context, e *idEnv) error) error {
	o, err := r.open(ctx, fsys, migs)
	if err != nil {
		return err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	env := r.bind(db)
	if r.preMigrate != nil {
		err = r.preMigrate(ctx, env)
	}
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err == nil && observe != nil {
		err = observe(ctx, env)
	}
	return errors.Join(err, db.Close())
}

// bind hands the doubles their keyspaces and the legacy range.
func (r *idRig) bind(db *storage.DB) *idEnv {
	aks, _ := db.Keyspace(idOwnerAuth)
	rks, _ := db.Keyspace(idOwnerRBAC)
	uks, _ := db.Keyspace(idOwnerAudit)
	lr, _ := db.Legacy(BooksOwner)
	r.h.mu.Lock()
	r.h.auth = &idAuth{ks: aks}
	r.h.roles = &idRoles{ks: rks, cat: idCatalog(r.t)}
	r.h.audit = &idAudit{ks: uks}
	r.h.legacy = lr
	r.h.mu.Unlock()
	return &idEnv{db: db, authKS: aks, rbacKS: rks, au: uks, h: r.h}
}

// ---- reading a store back ----

// idSnap is a booted store as the tests read it.
type idSnap struct {
	Legacy     map[string][]byte
	Users      map[string]storedUser
	Keys       map[string]storedKey
	Roles      map[string]rbac.Role
	Idents     map[string]auth.ExternalIdentity
	Allow      []string
	Events     []string
	Quarantine []quarantinedRow
	State      string
	Verify     datamigrate.VerifyReport
	raw        map[string]map[string][]byte // owner -> rows, for byte comparisons
}

// idRead collects a snapshot of e, running M3's own Verify over the legacy range.
func idRead(ctx context.Context, t testing.TB, e *idEnv, dm datamigrate.DataMigration) (*idSnap, error) {
	s := &idSnap{Legacy: map[string][]byte{}, Users: map[string]storedUser{}, Keys: map[string]storedKey{}, Roles: map[string]rbac.Role{},
		Idents: map[string]auth.ExternalIdentity{}, raw: map[string]map[string][]byte{}}
	lr, _ := e.db.Legacy(BooksOwner)
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			return nil, err
		}
		s.Legacy[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	for name, ks := range map[string]storage.Keyspace{"auth": e.authKS, "rbac": e.rbacKS, "audit": e.au} {
		s.raw[name] = dumpDouble(t, ctx, ks)
	}
	for k, v := range s.raw["auth"] {
		segs := splitKey(t, []byte(k))
		switch segs[0] {
		case kindUser:
			var u storedUser
			decodeInto(t, v, &u)
			s.Users[segs[1]] = u
		case kindKey:
			var sk storedKey
			decodeInto(t, v, &sk)
			s.Keys[segs[1]] = sk
		case kindIdent:
			var id auth.ExternalIdentity
			decodeInto(t, v, &id)
			s.Idents[segs[1]+"/"+segs[2]] = id
		case kindAllow:
			s.Allow = append(s.Allow, segs[1])
		}
	}
	for k, v := range s.raw["rbac"] {
		var role rbac.Role
		decodeInto(t, v, &role)
		s.Roles[splitKey(t, []byte(k))[1]] = role
	}
	for k := range s.raw["audit"] {
		segs := splitKey(t, []byte(k))
		s.Events = append(s.Events, segs[1]+" "+segs[2])
	}
	slices.Sort(s.Allow)
	slices.Sort(s.Events)
	entries, err := e.db.Quarantine(ctx, IdentityCutoverID)
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		s.Quarantine = append(s.Quarantine, quarantinedRow{N: en.N, Key: string(en.Record.Key), Reason: en.Record.Reason, Step: en.Record.Step, Sealed: en.Sealed(), Value: string(en.Record.Value)})
	}
	sks, _ := e.db.Keyspace("core/storage")
	switch raw, err := sks.Get(ctx, storage.DataMigrationKey(IdentityCutoverID)); {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return nil, err
	default:
		rec, err := storage.DataMigrationCodec().Decode(raw)
		if err != nil {
			return nil, err
		}
		s.State = rec.State
	}
	if s.State == "done" {
		if s.Verify, err = dm.Verify(ctx, lr); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func splitKey(t testing.TB, k []byte) []string {
	t.Helper()
	segs, err := keys.Split(k)
	if err != nil {
		t.Fatalf("a double's key %q: %v", k, err)
	}
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = string(s)
	}
	return out
}

func decodeInto(t testing.TB, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// run boots a fresh copy of the rig's store with M3 and returns what it left.
func (r *idRig) run(t testing.TB) (*idSnap, error) {
	t.Helper()
	return r.runOn(t, r.newMem(t))
}

func (r *idRig) runOn(t testing.TB, fsys vfs.FS) (*idSnap, error) {
	t.Helper()
	dm := r.migration()
	var snap *idSnap
	err := r.boot(context.Background(), fsys, []datamigrate.DataMigration{dm}, func(ctx context.Context, e *idEnv) error {
		var err error
		snap, err = idRead(ctx, t, e, dm)
		return err
	})
	return snap, err
}

// before reads the seeded store's legacy range as the pre-cutover build left it.
func (r *idRig) before(t testing.TB) map[string][]byte {
	t.Helper()
	mem := r.newMem(t)
	return r.dump(t, mem)
}

func (r *idRig) dump(t testing.TB, fsys vfs.FS) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(fsys), Owners: r.owners(false), Build: rigBuildOld, FreeSpaceMargin: 1, Clock: crashmatrix.Now})
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
	lr, _ := db.Legacy(BooksOwner)
	out := map[string][]byte{}
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		out[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// has reports whether any legacy key starts with one of prefixes.
func hasPrefix(legacy map[string][]byte, prefixes ...string) bool {
	for k := range legacy {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
	}
	return false
}

var _ = os.Getenv
