// file: internal/books/cutover/m4/rig_test.go
// version: 1.4.0
// guid: d82c4a15-9e63-4f07-b1a9-7035e8c6f241
// last-edited: 2026-11-18

package m4_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// The M4 test rig: books' store (an empty one with synthetic rows, written
// with the store's own row types so the frozen decoders are held to HEAD's JSON)
// in an in-memory filesystem, cut over by M4 alone through the kernel's
// data-migration runner and its P4a preflight, never through books.Module().
// The cutover build is books' real storage spec raised to its cutover level
// behind books' level step with the ops families' claims retired, so M4's Source
// is the one a registered step has. Core's task keyspace is the real
// tasks.NativeImporter's.

const (
	rigDir      = "/batcave/db"
	rigStore    = "/batcave/db/audiobooks.pebble"
	rigBuildOld = "batcomputer-1"
	rigBuildNew = "batcomputer-2"
	ownerTasks  = "core/tasks"
	corpusRel   = "internal/books/testdata/r2corpus"
)

// withCorpus seeds the rig from the R2 corpus's store ("stage1": what R0 wrote
// through its API; "overlay": stage 1 plus the hand-built rows of F2-13).
func withCorpus(name string) func(*rig) { return func(r *rig) { r.corpus = name } }

func rigDiskUsage() vfs.DiskUsage {
	return vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}
}

func crashClone(m *vfs.MemFS) *vfs.MemFS {
	c := m.CrashClone(vfs.CrashCloneCfg{})
	c.TestingSetDiskUsage(rigDiskUsage())
	return c
}

var errKilled = errors.New("the process was killed here")

func fixedOne() tasks.Parallelism {
	return tasks.Sequential("test: rows in key order so a kill lands on a known row")
}

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

// ---- the importer ----

// lazyImporter is the importer M4 is built with before the store is open: the
// real tasks.NativeImporter over core's task keyspace, bound between Prepare and
// Migrate. It counts the Run calls per run, so a test can tell "imported once"
// from "imported twice".
type lazyImporter struct {
	mu   sync.Mutex
	imp  tasks.Importer
	runs map[tasks.RunID]int
	// clocks counts the Clock calls per kind.
	clocks map[tasks.Kind]int
}

func (l *lazyImporter) bind(ks storage.Keyspace) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.imp = tasks.NativeImporter(ks)
}

func (l *lazyImporter) get() tasks.Importer {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.imp
}

func (l *lazyImporter) runCalls(id tasks.RunID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runs[id]
}

func (l *lazyImporter) Run(ctx context.Context, b *storage.Batch, r tasks.RunRecord) error {
	l.mu.Lock()
	if l.runs == nil {
		l.runs = map[tasks.RunID]int{}
	}
	l.runs[r.ID]++
	l.mu.Unlock()
	return l.get().Run(ctx, b, r)
}

func (l *lazyImporter) RunData(ctx context.Context, b *storage.Batch, id tasks.RunID, d tasks.RunData) error {
	return l.get().RunData(ctx, b, id, d)
}

func (l *lazyImporter) clockCalls(k tasks.Kind) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clocks[k]
}

func (l *lazyImporter) Clock(ctx context.Context, b *storage.Batch, k tasks.Kind, c tasks.Clock) error {
	l.mu.Lock()
	if l.clocks == nil {
		l.clocks = map[tasks.Kind]int{}
	}
	l.clocks[k]++
	l.mu.Unlock()
	return l.get().Clock(ctx, b, k, c)
}

func (l *lazyImporter) Pause(ctx context.Context, b *storage.Batch, p tasks.Pause) error {
	return l.get().Pause(ctx, b, p)
}

func (l *lazyImporter) StandDown(ctx context.Context, b *storage.Batch, s tasks.StandDown) error {
	return l.get().StandDown(ctx, b, s)
}

func (l *lazyImporter) Debounce(ctx context.Context, b *storage.Batch, d tasks.DebounceBucket) error {
	return l.get().Debounce(ctx, b, d)
}

func (l *lazyImporter) Stamp(ctx context.Context, b *storage.Batch, s entity.Ref, k tasks.Kind, at time.Time) error {
	return l.get().Stamp(ctx, b, s, k, at)
}

func (l *lazyImporter) RenameKind(ctx context.Context, b *storage.Batch, from, to tasks.Kind, cursor string, limit int) (string, error) {
	return l.get().RenameKind(ctx, b, from, to, cursor, limit)
}

// ---- the rig ----

type rig struct {
	t          testing.TB
	chunkItems int
	// mutate edits books' legacy range after the pre-freeze boot.
	mutate      func(ctx context.Context, t testing.TB, lr storage.LegacyRange)
	parallelism func(*datamigrate.DataMigration)
	imp         *lazyImporter
	logs        *lockedBuffer
	// legacy is books' legacy range of the booted store, bound between Prepare
	// and Migrate (Config.Legacy).
	legacy   atomic.Pointer[storage.LegacyRange]
	reconcil atomic.Int64
	// corpus is "stage1" or "overlay" (copied from the R2 corpus), or "" for an
	// empty store with synthetic rows.
	corpus string
	// view, when set, is consulted by every point read the step makes through
	// Config.Legacy: handled says the value (or err, e.g. storage.ErrNotFound)
	// is the answer. It stands for what a worker sees while another worker's
	// chunk commits, and for what HEAD's reconcile leaves behind (the runner's
	// own iterator keeps the snapshot it started from).
	view func(key string) (val []byte, handled bool, err error)
	// zone is the window clock's zone; the default is gotham.
	zone *time.Location
	// reconcileFn, when set, is what HEAD's reconcile does in the rig.
	reconcileFn func(ctx context.Context) error
}

// viewReader is a storage.Reader whose point reads go through view first.
type viewReader struct {
	storage.Reader
	view func(key string) ([]byte, bool, error)
}

func (v viewReader) Get(ctx context.Context, key []byte) ([]byte, error) {
	if val, handled, err := v.view(string(key)); handled {
		return val, err
	}
	return v.Reader.Get(ctx, key)
}

func newRig(t testing.TB, opts ...func(*rig)) *rig {
	t.Helper()
	r := &rig{t: t, chunkItems: 1, imp: &lazyImporter{}, logs: &lockedBuffer{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *rig) levelVersion() int {
	last := 0
	for _, m := range store.BooksSpec(nil).Migrations {
		last = max(last, m.Version)
	}
	return last + 1
}

// spec is books' storage spec for the build: the pre-cutover one (every family
// read-write), or the cutover build's with books at its cutover level behind
// books' level step and M4's families retired.
func (r *rig) spec(cut bool) storage.Spec {
	spec := store.BooksSpec(nil)
	if !cut {
		return spec
	}
	spec.Level = cutover.BooksCutoverLevel
	spec.Migrations = append(spec.Migrations, cutover.BooksLevelStep(r.levelVersion(), cutover.BooksCutoverLevel))
	retired := 0
	for i := range spec.Claims {
		for _, p := range m4.SourcePrefixes() {
			if strings.HasPrefix(spec.Claims[i].Prefix, p) {
				spec.Claims[i].Mode = storage.ClaimRetired
				retired++
				break
			}
		}
	}
	if retired == 0 {
		r.t.Fatal("books' storage spec declares no claim under M4's source prefixes")
	}
	// The shared setting: family stays read-write; the rows M4 moves are retired
	// as sub-claims of it (the interval clocks by prefix, the rest by exact key).
	for _, p := range m4.SourcePrefixes() {
		if strings.HasPrefix(p, "setting:") {
			spec.Claims = append(spec.Claims, storage.Claim{Prefix: p, Mode: storage.ClaimRetired, Description: "M4 source: " + p})
		}
	}
	for _, k := range m4.SourceKeys() {
		spec.Claims = append(spec.Claims, storage.Claim{Key: k, Mode: storage.ClaimRetired, Description: "M4 source: " + k})
	}
	return spec
}

// tasksSpec mirrors core's task keyspace claim (core/internal/taskstore's
// StorageSpec: the run, debounce, sched and idem families Serialized), which
// this package cannot import.
func tasksSpec() storage.Spec {
	var ser []string
	for _, fam := range []string{"run", "debounce", "sched", "idem"} {
		ser = append(ser, string(keys.Prefix(keys.Str(fam))))
	}
	return storage.Spec{Keyspace: true, Serialized: ser}
}

func (r *rig) owners(cut bool) []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: cutover.BooksOwner, Spec: r.spec(cut), Enabled: true},
		{Owner: ownerTasks, Spec: tasksSpec(), Enabled: true},
	}
}

// activity is the activity rewrite over this rig, built the way F5-18 builds
// it (after the flip's level step).
func (r *rig) activity() datamigrate.DataMigration {
	dm, err := m4.NewActivity(m4.ActivityConfig{Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))},
		cutover.TasksCutoverID+"-activity", cutover.BooksCutoverLevel, []storage.StepRef{cutover.BooksLevelStepRef(r.levelVersion())})
	if err != nil {
		r.t.Fatal(err)
	}
	return dm
}

// config is M4's Config over this rig.
func (r *rig) config() m4.Config {
	return m4.Config{
		Importer: r.imp,
		Legacy: func() storage.Reader {
			lr := r.legacy.Load()
			if lr == nil {
				r.t.Fatal("the legacy range is read before the store is open")
			}
			if r.view != nil {
				return viewReader{Reader: *lr, view: r.view}
			}
			return *lr
		},
		ScanStandDownKey: scanKey,
		StampAt:          stampAt,
		Location:         cmp.Or(r.zone, gotham),
		Reconcile: func(ctx context.Context) error {
			r.reconcil.Add(1)
			if r.reconcileFn == nil {
				return nil
			}
			return r.reconcileFn(ctx)
		},
		Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func m4Config(r *rig) m4.Config { return r.config() }

// migration is M4 over this rig, built the way F5-18 builds it.
func (r *rig) migration() datamigrate.DataMigration {
	dm, err := m4.New(r.config(), cutover.TasksCutoverID, cutover.BooksCutoverLevel, []storage.StepRef{cutover.BooksLevelStepRef(r.levelVersion())})
	if err != nil {
		r.t.Fatal(err)
	}
	if r.parallelism != nil {
		r.parallelism(&dm)
	}
	return dm
}

// seed writes the pre-cutover store on a fresh crashable filesystem: the
// corpus's Pebble directory and its sidecar (when r.corpus is set), the
// pre-freeze build's boot, then r.mutate over books' legacy range.
func (r *rig) seed(t testing.TB) *vfs.MemFS {
	t.Helper()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(rigDiskUsage())
	if err := mem.MkdirAll(rigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if r.corpus != "" {
		src := filepath.Join(repoRoot(t), filepath.FromSlash(corpusRel), r.corpus, "db")
		for _, n := range []string{"audiobooks.pebble", "audiobooks.pebble.storage-format"} {
			if _, err := vfs.Clone(vfs.Default, mem, filepath.Join(src, n), rigDir+"/"+n); err != nil {
				t.Fatalf("copy the corpus store %s: %v", n, err)
			}
		}
	}
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(mem), Owners: r.owners(false), Build: rigBuildOld, FreeSpaceMargin: 1, Clock: crashmatrix.Now})
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
	lr, _ := db.Legacy(cutover.BooksOwner)
	if r.mutate != nil {
		r.mutate(ctx, t, lr)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	syncTree(t, mem, rigDir)
	return mem
}

func (r *rig) open(ctx context.Context, fsys vfs.FS, migs []datamigrate.DataMigration) (*storage.Opened, error) {
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

// boot is one boot of the cutover build over fsys with dm (P2, P4a, P4b).
func (r *rig) boot(ctx context.Context, fsys vfs.FS, dm datamigrate.DataMigration, observe func(ctx context.Context, db *storage.DB) error) error {
	o, err := r.open(ctx, fsys, []datamigrate.DataMigration{dm})
	if err != nil {
		return err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	ks, _ := db.Keyspace(ownerTasks)
	r.imp.bind(ks)
	lr, _ := db.Legacy(cutover.BooksOwner)
	r.legacy.Store(&lr)
	err = db.Migrate(ctx)
	if err == nil && observe != nil {
		err = observe(ctx, db)
	}
	return errors.Join(err, db.Close())
}

// ---- reading a store back ----

type snap struct {
	legacy     storage.Reader
	Act        map[string]string // every row of the activity feed's act: families
	Left       map[string]string // rows still in books' source families
	Tasks      map[string]string // every row of core's task keyspace
	Quarantine []quarantined
	State      string
	Verify     datamigrate.VerifyReport
	Stores     tasks.Stores
	db         *storage.DB
}

type quarantined struct{ Key, Reason, Step string }

func read(ctx context.Context, t testing.TB, db *storage.DB, dm datamigrate.DataMigration) (*snap, error) {
	t.Helper()
	s := &snap{Left: map[string]string{}, Act: map[string]string{}, Tasks: map[string]string{}, db: db}
	lr, _ := db.Legacy(cutover.BooksOwner)
	s.legacy = lr
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: []byte("act:"), End: []byte("act;")}) {
		if err != nil {
			return nil, err
		}
		s.Act[string(kv.Key)] = string(kv.Value)
	}
	// Left is read over the families as the test names them, not over
	// m4.SourceRanges: a family the step forgot to claim must still show up.
	// opv2: and op: are whole families (5S.9.1 rows 11 and 13: the v2 store and
	// every op: child), so a child nobody listed is still seen.
	for _, rg := range []storage.Range{
		{Start: []byte("opv3:"), End: []byte("opv3;")},
		{Start: []byte("opv2:"), End: []byte("opv2;")},
		{Start: []byte("op:"), End: []byte("op;")},
		// v1 (5S.9.1 row 12): operation: and operationlog: are separate
		// families, so the ranges are too.
		{Start: []byte("operation:"), End: []byte("operation;")},
		{Start: []byte("operationlog:"), End: []byte("operationlog;")},
		{Start: []byte("op_result:"), End: []byte("op_result;")},
		{Start: []byte("opsummary:"), End: []byte("opsummary;")},
		{Start: []byte("opstate:"), End: []byte("opstate;")},
		{Start: []byte("setting:scheduled_task_last_tick."), End: []byte("setting:scheduled_task_last_tick/")},
		{Start: []byte("setting:library_scan_full_last_run"), End: []byte("setting:library_scan_full_last_run\x00")},
		{Start: []byte("setting:maintenance_window_last_run"), End: []byte("setting:maintenance_window_last_run\x00")},
		{Start: []byte("setting:registry.operations_paused"), End: []byte("setting:registry.operations_paused\x00")},
		{Start: []byte("setting:registry.scan_standdown"), End: []byte("setting:registry.scan_standdown\x00")},
	} {
		for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: rg.Start, End: rg.End}) {
			if err != nil {
				return nil, err
			}
			s.Left[string(kv.Key)] = string(kv.Value)
		}
	}
	ks, _ := db.Keyspace(ownerTasks)
	for kv, err := range ks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			return nil, err
		}
		s.Tasks[string(kv.Key)] = string(kv.Value)
	}
	s.Stores = tasks.NativeStores(ks)
	entries, err := db.Quarantine(ctx, cutover.TasksCutoverID)
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		s.Quarantine = append(s.Quarantine, quarantined{Key: string(en.Record.Key), Reason: en.Record.Reason, Step: en.Record.Step})
	}
	slices.SortFunc(s.Quarantine, func(a, b quarantined) int { return strings.Compare(a.Key, b.Key) })
	sks, _ := db.Keyspace("core/storage")
	switch raw, err := sks.Get(ctx, storage.DataMigrationKey(dm.ID)); {
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

func (r *rig) run(t testing.TB) (*snap, error) {
	t.Helper()
	return r.runOn(t, r.seed(t))
}

func (r *rig) runOn(t testing.TB, fsys vfs.FS) (*snap, error) {
	t.Helper()
	return r.runWith(t, fsys, r.migration())
}

// runWith boots fsys with dm and returns the snapshot taken inside the boot.
// The snapshot's Stores read core's keyspace and stay valid only inside the
// boot, so tests that need them use boot's observe callback directly (see
// runObserve).
func (r *rig) runWith(t testing.TB, fsys vfs.FS, dm datamigrate.DataMigration) (*snap, error) {
	t.Helper()
	var s *snap
	err := r.boot(context.Background(), fsys, dm, func(ctx context.Context, db *storage.DB) error {
		var err error
		s, err = read(ctx, t, db, dm)
		return err
	})
	if s != nil {
		s.Stores, s.db = tasks.Stores{}, nil
	}
	return s, err
}

// runObserve boots a fresh seeded store and hands observe the booted store's
// snapshot, with Stores readable.
func (r *rig) runObserve(t testing.TB, observe func(ctx context.Context, s *snap)) {
	t.Helper()
	dm := r.migration()
	err := r.boot(context.Background(), r.seed(t), dm, func(ctx context.Context, db *storage.DB) error {
		s, err := read(ctx, t, db, dm)
		if err != nil {
			return err
		}
		observe(ctx, s)
		return nil
	})
	if err != nil {
		t.Fatalf("boot: %v\n%s", err, r.logs)
	}
}

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

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd() // not runtime.Caller: CI builds with -trimpath
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "books")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no module root above the test's directory")
		}
		dir = parent
	}
}

// ---- synthetic runs, written with the store's own row types ----

var epoch = time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)

// scanKey is the scan kinds' stand-down key, as the rig's books registration
// would name it; gotham is the zone the window's date was written in (west of
// UTC, so reading the date as UTC puts it hours ahead of the window that ran).
const scanKey tasks.Key = "books.library.scan"

// stampAt is the instant the rig imports every stamp at.
var stampAt = time.Date(2026, 11, 6, 4, 0, 0, 0, time.UTC)

var gotham = time.FixedZone("Gotham", -5*3600)

// runSpec is one synthetic run and what it owns.
type runSpec struct {
	id, def                string
	st                     state.V3
	wait, stop, why        string
	mode, trigger          string
	params                 any
	priority               int
	queued                 time.Time
	completed, started     *time.Time
	parent                 string
	evts, logs, items      int
	intents                int
	ckpt, ledger           bool
	snaps                  int
	rows, rowmarks, marks  int
	derived                bool
	noQueued               bool
	corruptRun, corruptLog bool
	// v2 mirror rows (opv2:): the operation row, the resume state, log and
	// error lines, and the indexes HEAD rebuilds from the operation row.
	v2op, v2state, v2derived bool
	v2logs, v2errs           int
	// result and errText fill the run's Result and Error; system makes the
	// actor HEAD's system actor (no user, auth method "system").
	result, errText string
	system          bool
}

func tptr(t time.Time) *time.Time { return &t }

func runID(i int) string { return fmt.Sprintf("01HBAT%020d", i) }

// seedRun writes one run's rows into lr and returns the keys it wrote.
func seedRun(ctx context.Context, t testing.TB, lr storage.LegacyRange, s runSpec) []string {
	t.Helper()
	var wrote []string
	put := func(key string, v any) {
		t.Helper()
		var raw []byte
		switch x := v.(type) {
		case []byte:
			raw = x
		default:
			var err error
			if raw, err = json.Marshal(v); err != nil {
				t.Fatal(err)
			}
		}
		if err := lr.Set(ctx, []byte(key), raw); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
		wrote = append(wrote, key)
	}
	if s.mode == "" {
		s.mode = "live"
	}
	if s.queued.IsZero() {
		s.queued = epoch
	}
	params, err := json.Marshal(s.params)
	if err != nil {
		t.Fatal(err)
	}
	if s.params == nil {
		params = []byte("{}")
	}
	rec := store.RunRecord{
		SchemaVersion: 3, ID: s.id, DefID: s.def, State: s.st, WaitReason: s.wait, StopReason: s.stop, InterruptedWhy: s.why,
		Mode: s.mode, Trigger: s.trigger, Params: params, ParentID: s.parent, Attempt: 1, FenceEpoch: 2,
		Actor: store.Actor{UserID: "bruce.wayne", AuthMethod: "session"}, Priority: s.priority,
		StartedAt: s.started, CompletedAt: s.completed, QueuedAt: tptr(s.queued),
		Progress: store.ProgressSnapshot{Done: 7, Total: ptr(int64(9)), Current: "The Long Halloween", UpdatedAt: s.queued},
	}
	if s.noQueued {
		rec.QueuedAt = nil
	}
	if s.result != "" {
		rec.Result = json.RawMessage(s.result)
	}
	rec.Error = s.errText
	if s.system {
		rec.Actor = store.Actor{AuthMethod: "system"}
	}
	if s.corruptRun {
		put("opv3:run:"+s.id, []byte(`{"v":3,"id":`))
	} else {
		put("opv3:run:"+s.id, rec)
	}
	put("opv3:prog:"+s.id, struct {
		Progress       store.ProgressSnapshot `json:"progress"`
		LastProgressAt *time.Time             `json:"last_progress_at,omitempty"`
	}{rec.Progress, tptr(s.queued.Add(time.Minute))})
	for i := 1; i <= s.evts; i++ {
		at := s.queued.Add(time.Duration(i) * time.Second)
		put(fmt.Sprintf("opv3:evt:%s:%020d:%06d", s.id, at.UnixNano(), i), store.Transition{Seq: uint64(i), From: state.V3Queued, To: state.V3Running, Event: "claim", Attempt: 1, FenceEpoch: 2, At: at})
	}
	for i := 1; i <= s.intents; i++ {
		put(fmt.Sprintf("opv3:intent:%s:%010d", s.id, i), store.Intent{Seq: uint64(i), Subject: "book:hush-" + fmt.Sprint(i), Kind: "modify", Planned: json.RawMessage(`{"title":"Hush"}`), WrittenAt: s.queued})
	}
	for i := 1; i <= s.items; i++ {
		put(fmt.Sprintf("opv3:item:%s:failed:no-man-s-land-%d", s.id, i), store.ItemOutcome{Key: fmt.Sprintf("no-man-s-land-%d", i), Outcome: "failed", Error: "mislabeled", Chunk: 1})
	}
	for i := 1; i <= s.logs; i++ {
		at := s.queued.Add(time.Duration(i) * time.Millisecond)
		key := fmt.Sprintf("opv3:log:%s:%020d:%010d", s.id, at.UnixNano(), i)
		if s.corruptLog && i == 1 {
			put(key, []byte(`not json`))
			continue
		}
		put(key, store.LogLine{At: at, Level: "info", Message: "Knightfall fingerprinted"})
	}
	if s.ckpt {
		put("opv3:ckpt:"+s.id, store.Checkpoint{SchemaVersion: 1, Phase: "scan", State: json.RawMessage(`{"cursor":"Year One"}`), LegacyState: []byte(`{"v1":true}`), SavedAt: s.queued})
	}
	if s.ledger {
		put("opv3:ledger:"+s.id, store.Ledger{SchemaVersion: 1, ChunkSize: 100, ChunkCount: 3, Done: []byte{1}, Items: store.LedgerItems{Done: 100}, Counters: map[string]int64{"ok": 100}, UpdatedAt: s.queued})
	}
	for i := 0; i < s.snaps; i++ {
		put(fmt.Sprintf("opv3:snap:%s:%06d", s.id, i), []string{"a-death-in-the-family", "year-one"})
	}
	if s.snaps > 0 {
		put("opv3:snap:"+s.id+":chunks", []int32{0, 1})
	}
	for i := 1; i <= s.rows; i++ {
		rid := fmt.Sprintf("row-%03d", i)
		put("opv2:row:"+s.id+":"+rid, []byte(fmt.Sprintf(`{"id":%q,"title":"The Killing Joke"}`, rid)))
		put("opv2:rowix:"+s.id+":"+rid, []byte(`{"class":"safe"}`))
	}
	for i := 1; i <= s.rowmarks; i++ {
		put(fmt.Sprintf("opv3:rowmark:%s:row-%03d", s.id, i), store.RowMarkV3{RowID: fmt.Sprintf("row-%03d", i), MarkedAt: s.queued})
	}
	for i := 1; i <= s.marks; i++ {
		put(fmt.Sprintf("opv3:mark:%s:applied\x00item-%03d", s.id, i), struct{}{})
	}
	if s.derived {
		put("opv3:ix:state:"+string(s.st)+":"+s.id, []byte(nil))
		put(fmt.Sprintf("opv3:ix:def:%s:%020d:%s", s.def, s.queued.UnixNano(), s.id), []byte(nil))
		put("opv3:q:"+s.id, []byte(nil))
		put("opv3:def:"+s.def, []byte(`{"id":"`+s.def+`"}`))
		put("opv3:meta:counts", []byte(`{"queued":1}`))
	}
	if s.v2op {
		put("opv2:op:"+s.id, map[string]any{"ID": s.id, "DefID": s.def, "Status": "completed", "Priority": s.priority})
	}
	if s.v2state {
		put("opv2:state:"+s.id, store.OpStateV2Row{OperationID: s.id, StateBlob: []byte(`{"resume":{"phase":"xml"}}`), SchemaVersion: 2, WrittenAt: s.queued})
	}
	for i := 1; i <= s.v2logs; i++ {
		at := s.queued.Add(time.Duration(i) * time.Millisecond)
		put(fmt.Sprintf("opv2:log:%s:%020d:%010d", s.id, at.UnixNano(), 10+i), store.OpLogV2Row{OperationID: s.id, Level: "warn", Message: fmt.Sprintf("Arkham intake %d", i), Attrs: `{"phase":"xml","inmates":12}`, CreatedAt: at})
	}
	for i := 1; i <= s.v2errs; i++ {
		at := s.queued.Add(time.Duration(i) * time.Second)
		put(fmt.Sprintf("opv2:err:%s:%020d", s.id, at.UnixNano()), store.OpErrorV2Row{OperationID: s.id, Plugin: "maintenance", DefID: s.def, Message: fmt.Sprintf("Scarecrow escaped %d", i), Attrs: `{"error":"fear gas"}`, OccurredAt: at})
	}
	if s.v2derived {
		put("opv2:act:"+s.id, []byte(nil))
		put("opv2:def:"+s.def, []byte(`{"id":"`+s.def+`"}`))
		put(fmt.Sprintf("opv2:done:%020d:%s", s.queued.UnixNano(), s.id), []byte(nil))
		put(fmt.Sprintf("opv2:open:%020d:%s", s.queued.UnixNano(), s.id), []byte(nil))
		put(fmt.Sprintf("opv2:q:%03d:%020d:%s", 999-s.priority, s.queued.UnixNano(), s.id), []byte(nil))
		put("opv2:wait:"+s.id, []byte(nil))
	}
	return wrote
}

func ptr[T any](v T) *T { return &v }

// withRuns is a rig option that writes runs into books' legacy range after the
// pre-freeze boot and records every key it wrote, by run.
func withRuns(wrote map[string][]string, specs ...runSpec) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(ctx, t, lr)
			}
			for _, s := range specs {
				keys := seedRun(ctx, t, lr, s)
				if wrote != nil {
					wrote[s.id] = keys
				}
			}
		}
	}
}

// withRaw writes raw rows into books' legacy range.
func withRaw(rows map[string]string) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(ctx, t, lr)
			}
			for k, v := range rows {
				if err := lr.Set(ctx, []byte(k), []byte(v)); err != nil {
					t.Fatalf("seed %s: %v", k, err)
				}
			}
		}
	}
}

// settingRowJSON is a setting: row as HEAD's SetSetting wrote it.
func settingRowJSON(key, value string) string {
	raw, err := json.Marshal(store.Setting{Key: key, Value: value, Type: "string"})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// setting is the raw row for withRaw: the key's setting: row holding value.
func setting(rows map[string]string, key, value string) {
	rows["setting:"+key] = settingRowJSON(key, value)
}

// schedRowJSON is an opv3:sched: row as PutSchedV3 wrote it.
func schedRowJSON(def string, fire, gate *time.Time, missed int64) string {
	raw, err := json.Marshal(store.SchedState{DefID: def, LastFire: fire, Missed: missed, GateClosedAt: gate})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// batchRowJSON is an op:batch: row as AddToBatchBucket wrote it.
func batchRowJSON(subType, subID string, at time.Time) string {
	raw, err := json.Marshal(store.BatchBucketEntry{Sub: store.OpSubject{Type: subType, ID: subID}, AddedAt: at.UnixNano()})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// legacyGet reads one key of books' legacy range after the boot.
func (s *snap) legacyGet(ctx context.Context, key string) (string, error) {
	raw, err := s.legacy.Get(ctx, []byte(key))
	return string(raw), err
}

// revJSON is an op:completion: or op:deprev: row.
func revJSON(rev uint64) string { return fmt.Sprintf(`{"rev":%d}`, rev) }
