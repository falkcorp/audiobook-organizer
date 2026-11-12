// file: internal/books/cutover/harness_test.go
// version: 1.4.0
// guid: 2f6d7b3a-71c4-4a7c-b6a3-1e4f2b9c0d05
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
)

// The M2 test rig: books' R2 corpus (F2-12 stage 1, F2-13's overlay), copied
// into an in-memory filesystem, cut over by M2 alone through the kernel's
// data-migration runner (F2-15's harness: kernel.NewDataMigrations, storage's
// Open, Prepare and Migrate), never through books.Module(). The pre-cutover
// build is books' real storage spec with its four encrypted rows read-write;
// the cutover build raises books to its cutover level behind books' level step
// and retires the four rows' claims, so M2's Source is the one a registered
// step has (§5S.3.13).
//
// Core's config store is the real one for the two FailClosed keys (their
// sections are declared here with core's own tags) and a table-backed double
// for every other key the rename table names: the double accepts exactly the
// table's key targets, skips a value equal to its default (C4) like
// Importer.Stage does, and writes the same row format. The schema-faithful run
// over core's real registry is F3-07's (TestRenameTable_EveryKeyRegistered).
// Core's secrets store is the real one, over a real state directory.

const (
	rigDir      = "/batcave/db"
	rigStore    = "/batcave/db/audiobooks.pebble"
	rigAppID    = "abo"
	rigBuildOld = "batcomputer-1"
	rigBuildNew = "batcomputer-2"
	corpusRel   = "internal/books/testdata/r2corpus"
)

// rigHostCPUs is the CPU count every test runs as: the host that recorded
// testdata/head_defaults.json (concurrent_scans 10 = max(NumCPU, 4)). HEAD
// computes that default from the machine it runs on and M2 compares against it
// at call time (headNumCPU), so without a pin a test's verdict would depend on
// the machine running it. A test that needs another host calls pinHostCPUs.
const rigHostCPUs = 10

func init() { headNumCPU = func() int { return rigHostCPUs } }

// pinHostCPUs makes the tests' host report n CPUs until the test ends. It
// writes a package variable, so it must not run in a parallel test: the t.Setenv
// below panics under t.Parallel (called before or after), which makes that a
// failure instead of a data race. The variable it sets is never read.
func pinHostCPUs(t testing.TB, n int) {
	t.Helper()
	t.Setenv("ABO_CUTOVER_TEST_HOST_CPUS", strconv.Itoa(n))
	prev := headNumCPU
	headNumCPU = func() int { return n }
	t.Cleanup(func() { headNumCPU = prev })
}

func rigDiskUsage() vfs.DiskUsage {
	return vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}
}

// ---- core's config store, as M2 sees it ----

// rowStore is core's override store over c/config/: one row per key,
// keys.Join("v", path), an envelope of value, generation, time and author
// (the layout of core/internal/configstore, which an app module cannot import).
type rowStore struct{ ks storage.Keyspace }

type rowEnvelope struct {
	Value json.RawMessage `json:"value"`
	Gen   uint64          `json:"gen"`
	By    string          `json:"by"`
}

func rowKey(p config.Path) []byte { return keys.Join(keys.Str("v"), keys.Str(string(p))) }

func (s rowStore) Load(ctx context.Context) iter.Seq2[config.Override, error] {
	return func(yield func(config.Override, error) bool) {
		for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str("v"))) {
			if err != nil {
				yield(config.Override{}, err)
				return
			}
			segs, err := keys.Split(kv.Key)
			if err != nil || len(segs) != 2 {
				yield(config.Override{}, fmt.Errorf("c/config/ key %q is not a row", kv.Key))
				return
			}
			var e rowEnvelope
			if err := json.Unmarshal(kv.Value, &e); err != nil {
				yield(config.Override{}, err)
				return
			}
			if !yield(config.Override{Path: config.Path(segs[1]), Value: e.Value, Gen: e.Gen, By: e.By}, nil) {
				return
			}
		}
	}
}

func (s rowStore) Stage(ctx context.Context, b *storage.Batch, set []config.Override, unset []config.Path) error {
	b.Bind(s.ks)
	w := b.In(s.ks)
	for _, ov := range set {
		if _, err := b.GetForUpdate(ctx, s.ks, rowKey(ov.Path)); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		v, err := json.Marshal(rowEnvelope{Value: ov.Value, Gen: ov.Gen, By: ov.By})
		if err != nil {
			return err
		}
		if err := w.Set(rowKey(ov.Path), v); err != nil {
			return err
		}
	}
	for _, p := range unset {
		if err := w.Delete(rowKey(p)); err != nil {
			return err
		}
	}
	return nil
}

// authSection and gateSection are core's two FailClosed keys, with core's tags
// (core/kernel/core_sections.go).
type authSection struct {
	Mode string `json:"mode" reload:"restart" protect:"owner" schema:"enum=required|disabled"`
}

type gateSection struct {
	Enabled bool `json:"enabled" reload:"restart" protect:"admin"`
}

func realConfigStore(ctx context.Context, target config.OverrideStore) (*config.Store, error) {
	st, err := newFailClosedStore(config.Options{AppID: rigAppID})
	if err != nil {
		return nil, err
	}
	if err := st.Load(ctx); err != nil {
		return nil, err
	}
	st.SetImportTarget(target)
	return st, nil
}

// newFailClosedStore is a real config store with the two FailClosed keys M2
// must stage registered, as core registers them (core.auth.mode,
// core.http.basic_gate.enabled): the schema-faithful half of the rig.
func newFailClosedStore(o config.Options) (*config.Store, error) {
	st, err := config.NewStore(o)
	if err != nil {
		return nil, err
	}
	if _, err := config.RegisterSections(st.Registry(),
		config.Define("core.auth", authSection{Mode: "required"},
			config.FailClosed("core.auth.mode", json.RawMessage(`"required"`), json.RawMessage(`"disabled"`))),
		config.Define("core.http.basic_gate", gateSection{Enabled: false},
			config.FailClosed("core.http.basic_gate.enabled", json.RawMessage(`true`), json.RawMessage(`false`))),
	); err != nil {
		return nil, err
	}
	return st, nil
}

// stageCall is one Importer.Stage call M2 made.
type stageCall struct {
	Path  string
	Value string
	By    string
	Real  bool // the real store took it
}

// tableImporter is core's Importer as the rig has it: the real store for the
// registered keys, else the table-backed double.
type tableImporter struct {
	real     config.Importer
	registry map[string]bool
	target   rowStore
	allowed  map[string]bool
	defaults map[string]json.RawMessage
	refuse   map[string]error // paths whose Stage fails with this error (a registry refusing a value)

	mu    sync.Mutex
	calls []stageCall
}

func newTableImporter(real config.Importer, target rowStore) *tableImporter {
	ti := &tableImporter{real: real, target: target, registry: map[string]bool{"core.auth.mode": true, "core.http.basic_gate.enabled": true},
		allowed: map[string]bool{}, defaults: map[string]json.RawMessage{}}
	for _, r := range Table().Rows {
		for _, t := range r.Targets {
			switch t.Kind {
			case TargetKey:
				if !isConcretePath(t.Path) || strings.HasSuffix(t.Path, ".*") {
					continue
				}
				ti.allowed[t.Path] = true
				if def, ok := headDefault(r.Head); ok && !r.Container {
					if raw, err := json.Marshal(def); err == nil {
						if out, _, err := Convert(t.Convert, raw); err == nil {
							ti.defaults[t.Path] = out
						}
					}
				}
			case TargetEntry, TargetFold:
				ti.allowed[t.List] = true
				ti.defaults[t.List] = json.RawMessage(`[]`)
			}
		}
	}
	return ti
}

func (ti *tableImporter) record(c stageCall) {
	ti.mu.Lock()
	ti.calls = append(ti.calls, c)
	ti.mu.Unlock()
}

func (ti *tableImporter) Stage(b *storage.Batch, p config.Path, v json.RawMessage, by string) error {
	ti.record(stageCall{Path: string(p), Value: string(v), By: by, Real: ti.registry[string(p)]})
	if ti.registry[string(p)] {
		return ti.real.Stage(b, p, v, by)
	}
	if !ti.allowed[string(p)] {
		return fmt.Errorf("%w: %s", config.ErrUnknownKey, p)
	}
	if err := ti.refuse[string(p)]; err != nil {
		return err
	}
	if def, ok := ti.defaults[string(p)]; ok && jsonSame(def, v) || !ok && isZeroJSON(v) {
		return nil
	}
	return ti.target.Stage(context.Background(), b, []config.Override{{Path: p, Value: v, By: by}}, nil)
}

func (ti *tableImporter) Default(p config.Path) (json.RawMessage, error) {
	if ti.registry[string(p)] {
		return ti.real.Default(p)
	}
	if d, ok := ti.defaults[string(p)]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("%w: %s has no default in the rig", config.ErrUnknownKey, p)
}

func (ti *tableImporter) staged() []stageCall {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	return slices.Clone(ti.calls)
}

// isZeroJSON reports whether v is the zero value of its JSON type: a key with
// no recorded default defaults to it.
func isZeroJSON(v json.RawMessage) bool {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return false
	}
	switch t := x.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case float64:
		return t == 0
	case bool:
		return !t
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// effective is the value a key has when only the staged rows and the
// defaults apply (no operator layer): the staged row, else the key's default,
// else the zero value of its type (ok false for an unrecorded default).
func (ti *tableImporter) effective(staged map[string]json.RawMessage, path string) (v json.RawMessage, zero bool) {
	if v, ok := staged[path]; ok {
		return v, false
	}
	if ti.registry[path] {
		d, _ := ti.real.Default(config.Path(path))
		return d, false
	}
	if d, ok := ti.defaults[path]; ok {
		return d, false
	}
	return nil, true
}

func jsonSame(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

// ---- the rig ----

// handles is what one boot's migration reaches, set between Prepare and
// Migrate (the importers are valid only then).
type handles struct {
	mu     sync.RWMutex
	sec    *secrets.Store
	cfg    *tableImporter
	legacy storage.Reader
}

func (h *handles) get() (*secrets.Store, *tableImporter, storage.Reader) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sec, h.cfg, h.legacy
}

type lazyConfig struct{ h *handles }

func (l lazyConfig) Stage(b *storage.Batch, p config.Path, v json.RawMessage, by string) error {
	_, c, _ := l.h.get()
	return c.Stage(b, p, v, by)
}

func (l lazyConfig) Default(p config.Path) (json.RawMessage, error) {
	_, c, _ := l.h.get()
	return c.Default(p)
}

type lazySecrets struct{ h *handles }

func (l lazySecrets) ImportSecret(ctx context.Context, b *storage.Batch, w secrets.Write) error {
	s, _, _ := l.h.get()
	return s.Importer().ImportSecret(ctx, b, w)
}

func (l lazySecrets) ImportUnreadable(ctx context.Context, b *storage.Batch, n secrets.Name, reason, by string) error {
	s, _, _ := l.h.get()
	return s.Importer().ImportUnreadable(ctx, b, n, reason, by)
}

// sealer is storage's quarantine sealer over the secrets importer, the
// adapter F3-07 binds at boot.
type sealer struct{ h *handles }

func (s sealer) Seal(ctx context.Context, b *storage.Batch, name string, raw []byte) error {
	sec, _, _ := s.h.get()
	return sec.Importer().ImportSecret(ctx, b, secrets.Write{Name: secrets.Name(name), Value: secrets.NewValue(string(raw)), By: "quarantine"})
}

// lockedBuffer is a log sink the tests read back.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// rig is one corpus store and its M2.
type rig struct {
	t        testing.TB
	name     string
	srcDB    string // the corpus's db/ directory
	stateDir string // a copy of the corpus's state directory: HEAD's key, then core's keyring
	confFile string // a copy of the corpus's config.yaml ("" when it has none)
	// databasePath is ConfigStepOptions.DatabasePath: HEAD's database path,
	// whose directory is the second place its key is searched. It is required;
	// newRig gives each rig an empty directory of its own.
	databasePath string
	h            *handles
	logs         *lockedBuffer

	chunkItems int
	refuse     map[string]error
	preMigrate observer // runs with the stores open, before the data steps
	mutate     func(t testing.TB, lr storage.LegacyRange)
	files      FileOps
	allowRegen bool
	noKeyDir   bool // the state directory has no key file once the store is seeded
	// afterSeed edits the state directory once the store is seeded (seeding
	// itself seals rows with HEAD's key, so the key is only broken after it).
	afterSeed func(t testing.TB, stateDir string)
	headKey   []byte // the corpus's key file, restored before each seeding
}

// newRig copies the corpus store name ("stage1" or "overlay") into a rig.
func newRig(t testing.TB, name string, opts ...func(*rig)) *rig {
	t.Helper()
	root := repoRoot(t)
	src := filepath.Join(root, filepath.FromSlash(corpusRel), name)
	r := &rig{t: t, name: name, srcDB: filepath.Join(src, "db"), h: &handles{}, logs: &lockedBuffer{}, chunkItems: 1}
	r.stateDir = t.TempDir()
	if err := copyTree(filepath.Join(src, "state"), r.stateDir); err != nil {
		t.Fatalf("copy the corpus state directory: %v", err)
	}
	r.databasePath = filepath.Join(t.TempDir(), "audiobooks.pebble")
	cfgDir := t.TempDir()
	if raw, err := os.ReadFile(filepath.Join(r.srcDB, "config.yaml")); err == nil {
		r.confFile = filepath.Join(cfgDir, "config.yaml")
		if err := os.WriteFile(r.confFile, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.headKey, _ = os.ReadFile(filepath.Join(r.stateDir, ".encryption_key"))
	for _, o := range opts {
		o(r)
	}
	return r
}

// breakKey applies the rig's state-directory edits after a seeding.
func (r *rig) breakKey(t testing.TB) {
	t.Helper()
	if r.noKeyDir {
		if err := os.Remove(filepath.Join(r.stateDir, ".encryption_key")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if r.afterSeed != nil {
		r.afterSeed(t, r.stateDir)
	}
}

// copyTree copies every regular file and directory of src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, raw, 0o600)
		}
		return nil
	})
}

// settingRows are the four encrypted rows and the blob, the keys M2 reads.
func settingRows() []string {
	return []string{"setting:" + blobRowName, "setting:basic_auth_password", "setting:google_books_api_key", "setting:hardcover_api_token", "setting:openai_api_key"}
}

// spec is books' storage spec for the build: the pre-cutover one (the four rows
// read-write, no level step), or the cutover build's (books at its cutover
// level behind books' level step, the four rows' claims retired).
func (r *rig) spec(cutover bool) storage.Spec {
	spec := store.BooksSpec(nil)
	if !cutover {
		return spec
	}
	spec.Level = BooksCutoverLevel
	spec.Migrations = append(spec.Migrations, BooksLevelStep(r.levelVersion(), BooksCutoverLevel))
	for i := range spec.Claims {
		k := spec.Claims[i].Key
		if k != "" && k != "setting:"+blobRowName && slices.Contains(settingRows(), k) {
			spec.Claims[i].Mode = storage.ClaimRetired
		}
	}
	return spec
}

// levelVersion is books' next contiguous version after its versioned steps.
func (r *rig) levelVersion() int {
	last := 0
	for _, m := range store.BooksSpec(nil).Migrations {
		last = max(last, m.Version)
	}
	return last + 1
}

func (r *rig) owners(cutover bool) []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: BooksOwner, Spec: r.spec(cutover), Enabled: true},
		{Owner: "core/config", Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
		{Owner: "core/secrets", Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
	}
}

// migration is M2 over this rig.
func (r *rig) migration() datamigrate.DataMigration {
	opts := ConfigStepOptions{
		LevelStepVersion: r.levelVersion(),
		StateDir:         r.stateDir,
		DatabasePath:     r.databasePath,
		ConfigFile:       r.confFile,
		Legacy:           func() storage.Reader { _, _, l := r.h.get(); return l },
		AllowKeyRegen:    func() bool { return r.allowRegen },
		Files:            r.files,
		Logger:           slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return ConfigStep(lazyConfig{r.h}, lazySecrets{r.h}, opts)
}

// seed writes the pre-cutover store on fsys: the corpus's Pebble directory and
// its sidecar, then r.mutate over books' legacy range.
func (r *rig) seed(t testing.TB, fsys vfs.FS) {
	t.Helper()
	if err := fsys.MkdirAll(rigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"audiobooks.pebble", "audiobooks.pebble.storage-format"} {
		if _, err := vfs.Clone(vfs.Default, fsys, filepath.Join(r.srcDB, n), rigDir+"/"+n); err != nil {
			t.Fatalf("copy the corpus store %s: %v", n, err)
		}
	}
	if r.mutate == nil {
		syncTree(t, fsys, rigDir)
		r.breakKey(t)
		return
	}
	if r.headKey != nil {
		if err := os.WriteFile(filepath.Join(r.stateDir, ".encryption_key"), r.headKey, 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	r.mutate(t, lr)
	if err := db.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	syncTree(t, fsys, rigDir)
	r.breakKey(t)
}

// syncTree makes every file under dir, every directory under it and every
// directory above it durable on fsys. A tree copied in with vfs.Clone is not:
// a crash clone of the unsynced copy opens as an empty store, which would
// make a crash-matrix cell compare nothing with nothing.
func syncTree(t testing.TB, fsys vfs.FS, dir string) {
	t.Helper()
	syncDir := func(p string) {
		d, err := fsys.OpenDir(p)
		if err != nil {
			t.Fatalf("open directory %s: %v", p, err)
		}
		if err := errors.Join(d.Sync(), d.Close()); err != nil {
			t.Fatalf("sync directory %s: %v", p, err)
		}
	}
	var walk func(p string)
	walk = func(p string) {
		names, err := fsys.List(p)
		if err != nil {
			t.Fatalf("list %s: %v", p, err)
		}
		for _, n := range names {
			full := fsys.PathJoin(p, n)
			fi, err := fsys.Stat(full)
			if err != nil {
				t.Fatalf("stat %s: %v", full, err)
			}
			if fi.IsDir() {
				walk(full)
				continue
			}
			f, err := fsys.Open(full)
			if err != nil {
				t.Fatalf("open %s: %v", full, err)
			}
			if err := errors.Join(f.Sync(), f.Close()); err != nil {
				t.Fatalf("sync %s: %v", full, err)
			}
		}
		syncDir(p)
	}
	walk(dir)
	for p := fsys.PathDir(dir); p != "" && p != "." && p != fsys.PathDir(p); p = fsys.PathDir(p) {
		syncDir(p)
	}
	syncDir(fsys.PathDir(fsys.PathDir(dir)))
}

// observer sees one boot after Migrate, with the stores still open.
type observer func(ctx context.Context, e *bootEnv) error

// bootEnv is what an observer can read.
type bootEnv struct {
	db  *storage.DB
	sec *secrets.Store
	cfg *tableImporter
	ks  storage.Keyspace // c/config/
}

// boot is one boot of the cutover build over fsys with migs (P2, P4a, P4b):
// it opens the store, opens core's secrets and config stores, runs the data
// steps, calls observe and closes.
func (r *rig) boot(ctx context.Context, fsys vfs.FS, migs []datamigrate.DataMigration, observe observer) error {
	owners := r.owners(true)
	cfg := kernel.DataMigrationsConfig{ChunkItems: r.chunkItems, Build: rigBuildNew, Logger: slog.New(slog.DiscardHandler),
		Quarantine: storage.QuarantineWriter(sealer{r.h}), Preflight: kernel.CutoverPreflight(slog.New(slog.NewTextHandler(r.logs, nil)))}
	dms, err := kernel.NewDataMigrations(cfg, owners, migs...)
	if err != nil {
		return err
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(fsys), Owners: owners, Build: rigBuildNew,
		FreeSpaceMargin: 1, DataSteps: dms.Steps(), Clock: crashmatrix.Now})
	if err != nil {
		return err
	}
	if err := dms.Preflight(ctx, o); err != nil { // P4a: a blocker refuses the boot with nothing written
		return errors.Join(err, o.Close())
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	root, err := os.OpenRoot(r.stateDir)
	if err != nil {
		return errors.Join(err, db.Close())
	}
	secKS, _ := db.Keyspace("core/secrets")
	cfgKS, _ := db.Keyspace("core/config")
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: secKS, AppID: rigAppID, Now: crashmatrix.Now, AllowKeyRegen: r.allowRegen})
	if err != nil {
		return errors.Join(err, root.Close(), db.Close())
	}
	target := rowStore{ks: cfgKS}
	real, err := realConfigStore(ctx, target)
	if err != nil {
		return errors.Join(err, sec.Close(), root.Close(), db.Close())
	}
	ti := newTableImporter(real.Importer(), target)
	ti.refuse = r.refuse
	lr, _ := db.Legacy(BooksOwner)
	r.h.mu.Lock()
	r.h.sec, r.h.cfg, r.h.legacy = sec, ti, lr
	r.h.mu.Unlock()
	if r.preMigrate != nil {
		err = r.preMigrate(ctx, &bootEnv{db: db, sec: sec, cfg: ti, ks: cfgKS})
	}
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err == nil && observe != nil {
		err = observe(ctx, &bootEnv{db: db, sec: sec, cfg: ti, ks: cfgKS})
	}
	return errors.Join(err, real.Close(ctx), sec.Close(), root.Close(), db.Close())
}

// ---- reading a store back ----

// quarantined is one quarantine record with its value opened.
type quarantinedRow struct {
	N      uint64
	Key    string
	Reason string
	Step   string
	Sealed bool
	Value  string // the raw value: the record's, or the sealed secret's plaintext
}

// snapshot is a booted store as the oracle reads it.
type snapshot struct {
	Config     map[string]json.RawMessage
	Secrets    map[string]string // every readable secret
	Unreadable map[string]error  // secret name -> why it cannot be read
	Quarantine []quarantinedRow
	Legacy     map[string][]byte
	RawCore    map[string][]byte // every row of core's config, secrets and storage keyspaces, "<owner>/<key>"
	State      string
	Calls      []stageCall
	cfg        *tableImporter
}

// Effective is the value of key path with no operator layer: staged, else its
// default; zero reports a key with neither (its type's zero value applies).
func (s *snapshot) Effective(path string) (v json.RawMessage, zero bool) {
	return s.cfg.effective(s.Config, path)
}

// read collects a snapshot of e.
func read(ctx context.Context, e *bootEnv) (*snapshot, error) {
	s := &snapshot{Config: map[string]json.RawMessage{}, Secrets: map[string]string{}, Unreadable: map[string]error{}, Legacy: map[string][]byte{}}
	for ov, err := range (rowStore{ks: e.ks}).Load(ctx) {
		if err != nil {
			return nil, err
		}
		s.Config[string(ov.Path)] = ov.Value
	}
	for m, err := range e.sec.All(ctx) {
		if err != nil {
			return nil, err
		}
		if m.Err != nil {
			s.Unreadable[string(m.Name)] = m.Err
			continue
		}
		v, _, err := e.sec.Get(ctx, m.Name)
		if err != nil {
			s.Unreadable[string(m.Name)] = err
			continue
		}
		s.Secrets[string(m.Name)] = v.Reveal()
	}
	entries, err := e.db.Quarantine(ctx, ConfigCutoverID)
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		q := quarantinedRow{N: en.N, Key: string(en.Record.Key), Reason: en.Record.Reason, Step: en.Record.Step, Sealed: en.Sealed(), Value: string(en.Record.Value)}
		if en.Sealed() {
			v, ok := s.Secrets[en.Record.Secret]
			if !ok {
				return nil, fmt.Errorf("quarantine record %d names the sealed secret %s, which is not readable", en.N, en.Record.Secret)
			}
			q.Value = v
		}
		s.Quarantine = append(s.Quarantine, q)
	}
	lr, _ := e.db.Legacy(BooksOwner)
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			return nil, err
		}
		s.Legacy[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	s.RawCore = map[string][]byte{}
	for _, owner := range []storage.Owner{"core/config", "core/secrets", "core/storage"} {
		ks, _ := e.db.Keyspace(owner)
		for kv, err := range ks.Scan(ctx, storage.ScanOptions{}) {
			if err != nil {
				return nil, err
			}
			s.RawCore[string(owner)+"/"+string(kv.Key)] = bytes.Clone(kv.Value)
		}
	}
	sks, _ := e.db.Keyspace("core/storage")
	raw, err := sks.Get(ctx, storage.DataMigrationKey(ConfigCutoverID))
	switch {
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
	s.Calls = e.cfg.staged()
	s.cfg = e.cfg
	return s, nil
}

// newMem is a seeded in-memory filesystem: the pre-cutover store.
func (r *rig) newMem(t testing.TB) *vfs.MemFS {
	t.Helper()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(rigDiskUsage())
	r.seed(t, mem)
	return mem
}

// run boots a fresh copy of the rig's store with M2 and returns what it left.
func (r *rig) run(t testing.TB) (*snapshot, error) {
	t.Helper()
	return r.runOn(t, r.newMem(t))
}

// runOn is run over an existing seeded filesystem.
func (r *rig) runOn(t testing.TB, mem vfs.FS) (*snapshot, error) {
	t.Helper()
	var snap *snapshot
	err := r.boot(context.Background(), mem, []datamigrate.DataMigration{r.migration()}, func(ctx context.Context, e *bootEnv) error {
		var err error
		snap, err = read(ctx, e)
		return err
	})
	return snap, err
}

// before reads the seeded store as the pre-cutover build left it: books' legacy
// range, every key.
func (r *rig) before(t testing.TB) map[string][]byte {
	t.Helper()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(rigDiskUsage())
	r.seed(t, mem)
	return r.dump(t, mem)
}

// dump opens the store on fsys as the pre-cutover build and reads books'
// legacy range, every key.
func (r *rig) dump(t testing.TB, fsys vfs.FS) map[string][]byte {
	t.Helper()
	legacy, _ := r.dumpAt(t, fsys, rigStore)
	return legacy
}

// dumpAt is dump for the store at path, and also returns the rows of core's
// config and secrets keyspaces ("<owner>/<key>"): what a restored
// pre-cutover store must not hold.
func (r *rig) dumpAt(t testing.TB, fsys vfs.FS, path string) (legacy, core map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: path, FS: storage.FSOf(fsys), Owners: r.owners(false), Build: rigBuildOld, FreeSpaceMargin: 1, Clock: crashmatrix.Now})
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
	legacy = map[string][]byte{}
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		legacy[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	core = map[string][]byte{}
	for _, owner := range []storage.Owner{"core/config", "core/secrets"} {
		ks, ok := db.Keyspace(owner)
		if !ok {
			continue
		}
		for kv, err := range ks.Scan(ctx, storage.ScanOptions{}) {
			if err != nil {
				t.Fatal(err)
			}
			core[string(owner)+"/"+string(kv.Key)] = bytes.Clone(kv.Value)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return legacy, core
}

// ---- fixtures: the blob and the rows, written the way HEAD writes them ----

// settingRow is HEAD's settings row (key, value, type, is_secret).
func settingRow(key, value, typ string, secret bool) []byte {
	raw, err := json.Marshal(headSetting{Key: key, Value: value, Type: typ, IsSecret: secret})
	if err != nil {
		panic(err)
	}
	return raw
}

// encryptedRow is a secret row HEAD wrote: the value sealed with the key in
// stateDir (HEAD's own cipher, frozen in head_cipher.go).
func encryptedRow(t testing.TB, stateDir, key, plaintext string) []byte {
	t.Helper()
	k, err := os.ReadFile(filepath.Join(stateDir, ".encryption_key"))
	if err != nil {
		t.Fatal(err)
	}
	return settingRow(key, encryptForTest(k, plaintext), "string", true)
}

// sortedKeys lists m's keys.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
