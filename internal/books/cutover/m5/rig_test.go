// file: internal/books/cutover/m5/rig_test.go
// version: 1.0.0
// guid: b17d6f02-35a9-4c8e-a4f1-0e92c7d3b865
// last-edited: 2026-11-13

package m5_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m5"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// The M5 test rig: books' store (the R2 corpus's stage 1 or overlay copy, or an
// empty store with synthetic rows) in an in-memory filesystem, cut over by M5
// alone through the kernel's data-migration runner and its P4a preflight, never
// through books.Module(). The cutover build is books' real storage spec raised
// to its cutover level behind books' level step with the syslog: claim retired,
// so M5's Source is the one a registered step has.
//
// The journal is a double over one keyspace of that same store: m5 cannot import
// core/internal/logjournal (Go's internal rule), and the real importer's own
// idempotence is proven where it lives (core/internal/logjournal's tests). What
// this package proves is M5's side of the contract: one Import per row before
// that row's delete, and a resume that imports onto the same key. The double
// derives its key the way the contract says (time and SourceID) and counts the
// Import calls it gets per SourceID, so a test can tell "journaled once" from
// "imported once".

const (
	rigDir      = "/batcave/db"
	rigStore    = "/batcave/db/audiobooks.pebble"
	rigBuildOld = "batcomputer-1"
	rigBuildNew = "batcomputer-2"
	corpusRel   = "internal/books/testdata/r2corpus"
	// ownerLog is the keyspace the journal double writes to.
	ownerLog = "core/log"
)

// fixedOne is a one-shard declaration: the rows are stepped in key order, which
// a test that kills the step at its nth row needs.
func fixedOne() tasks.Parallelism {
	return tasks.Sequential("test: rows in key order so a kill lands on a known row")
}

// crashClone is the filesystem as a power cut would leave it (only what was
// synced), with the disk-usage report a CrashClone does not carry over.
func crashClone(m *vfs.MemFS) *vfs.MemFS {
	c := m.CrashClone(vfs.CrashCloneCfg{})
	c.TestingSetDiskUsage(rigDiskUsage())
	return c
}

// errKilled is the injected crash: a Step wrapper returns it where a process
// would die.
var errKilled = errors.New("the process was killed here")

func rigDiskUsage() vfs.DiskUsage {
	return vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}
}

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
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

// ---- the journal double ----

// journaled is a journal record as the double stores it.
type journaled struct {
	Time      time.Time `json:"time"`
	Level     int       `json:"level"`
	Module    string    `json:"module"`
	Subsystem string    `json:"subsystem"`
	Message   string    `json:"message"`
	UserID    string    `json:"user_id,omitempty"`
	SourceID  string    `json:"source_id"`
}

// journalDouble is log.Importer over a keyspace bound after Prepare, with the
// key the contract names: a function of the record's time (millisecond) and its
// SourceID, so importing the same row again overwrites the same row.
type journalDouble struct {
	mu    sync.Mutex
	ks    storage.Keyspace
	calls map[string]int // SourceID -> Import calls that carried it
	// failFor, when set, makes Import return its error for that SourceID without
	// writing.
	failFor map[string]error
}

func (j *journalDouble) bind(ks storage.Keyspace) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ks = ks
}

func (j *journalDouble) callsFor(id string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls[id]
}

func (j *journalDouble) totalCalls() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, c := range j.calls {
		n += c
	}
	return n
}

func journalKey(t time.Time, sourceID string) []byte {
	sum := sha256.Sum256([]byte(sourceID))
	return keys.Join(keys.Str("j"), keys.Str(fmt.Sprintf("%016d", t.UnixMilli())), keys.Str(hex.EncodeToString(sum[:10])))
}

func (j *journalDouble) Import(ctx context.Context, recs []log.ImportedRecord) error {
	j.mu.Lock()
	ks := j.ks
	if j.calls == nil {
		j.calls = map[string]int{}
	}
	for _, r := range recs {
		j.calls[r.SourceID]++
	}
	fail := j.failFor
	j.mu.Unlock()
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	for _, r := range recs {
		if err := fail[r.SourceID]; err != nil {
			return err
		}
		jr := journaled{Time: r.Time.UTC(), Level: int(r.Level), Module: r.Module, Subsystem: r.Subsystem, Message: r.Message, SourceID: r.SourceID}
		for _, a := range r.Attrs {
			if a.Key == m5.UserIDAttr {
				jr.UserID = a.Value.String()
			}
		}
		raw, err := json.Marshal(jr)
		if err != nil {
			return err
		}
		if err := b.Set(journalKey(r.Time, r.SourceID), raw); err != nil {
			return err
		}
	}
	return b.Commit(ctx, storage.Sync)
}

// lazyImporter is the importer M5 is built with before the store is open: the
// double it reaches is bound between Prepare and Migrate.
type lazyImporter struct{ j *journalDouble }

func (l lazyImporter) Import(ctx context.Context, recs []log.ImportedRecord) error {
	return l.j.Import(ctx, recs)
}

// ---- the rig ----

// rig is one store and its M5.
type rig struct {
	t testing.TB
	// corpus is "stage1" or "overlay" (copied from the R2 corpus), or "" for an
	// empty store.
	corpus     string
	chunkItems int
	// mutate edits books' legacy range after the pre-freeze boot.
	mutate func(ctx context.Context, t testing.TB, lr storage.LegacyRange)
	// parallelism overrides the step's declaration (a test that needs a
	// deterministic order sets Fixed(1)).
	parallelism func(*datamigrate.DataMigration)
	j           *journalDouble
	logs        *lockedBuffer
}

func newRig(t testing.TB, corpus string, opts ...func(*rig)) *rig {
	t.Helper()
	r := &rig{t: t, corpus: corpus, chunkItems: 1, j: &journalDouble{}, logs: &lockedBuffer{}}
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
// books' level step and the syslog: claim retired.
func (r *rig) spec(cut bool) storage.Spec {
	spec := store.BooksSpec(nil)
	if !cut {
		return spec
	}
	spec.Level = cutover.BooksCutoverLevel
	spec.Migrations = append(spec.Migrations, cutover.BooksLevelStep(r.levelVersion(), cutover.BooksCutoverLevel))
	retired := false
	for i := range spec.Claims {
		if spec.Claims[i].Prefix == m5.Prefix {
			spec.Claims[i].Mode = storage.ClaimRetired
			retired = true
		}
	}
	if !retired {
		r.t.Fatalf("books' storage spec declares no %q claim", m5.Prefix)
	}
	return spec
}

func (r *rig) owners(cut bool) []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: cutover.BooksOwner, Spec: r.spec(cut), Enabled: true},
		// The journal is append-only: its rows are written, never updated, so a
		// write carries no Expect (the real journal's shape).
		{Owner: ownerLog, Spec: storage.Spec{Keyspace: true}, Enabled: true},
	}
}

// migration is M5 over this rig, built the way F5-18 builds it.
func (r *rig) migration() datamigrate.DataMigration {
	dm, err := m5.New(m5.Config{
		Importer: lazyImporter{r.j},
		Logger:   slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, cutover.LogCutoverID, cutover.BooksCutoverLevel, []storage.StepRef{cutover.BooksLevelStepRef(r.levelVersion())})
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
// pre-freeze build's boot over it, then r.mutate over books' legacy range.
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
	// Always boot once as the pre-freeze build: the store a release served has
	// its install marker (storage cutover-blockers refuses one without), and
	// r.mutate then edits books' legacy range.
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

// open is one boot's P2 and P4a over fsys with the data steps and the cutover
// preflight run. A blocker is returned with the store closed and nothing written.
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
	ks, _ := db.Keyspace(ownerLog)
	r.j.bind(ks)
	err = db.Migrate(ctx)
	if err == nil && observe != nil {
		err = observe(ctx, db)
	}
	return errors.Join(err, db.Close())
}

// ---- reading a store back ----

// snap is a booted store as the tests read it.
type snap struct {
	Left       map[string][]byte    // syslog: rows still in books' legacy range
	Journal    map[string]journaled // by journal key
	Quarantine []quarantined
	State      string
	Verify     datamigrate.VerifyReport
}

type quarantined struct {
	Key, Reason, Step, Value string
}

// read collects a snapshot of db after a boot. Verify is dm's own, run over the
// legacy range once the step is done.
func read(ctx context.Context, t testing.TB, db *storage.DB, dm datamigrate.DataMigration) (*snap, error) {
	t.Helper()
	s := &snap{Left: map[string][]byte{}, Journal: map[string]journaled{}}
	lr, _ := db.Legacy(cutover.BooksOwner)
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: m5.SourceRanges()[0].Start, End: m5.SourceRanges()[0].End}) {
		if err != nil {
			return nil, err
		}
		s.Left[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	jks, _ := db.Keyspace(ownerLog)
	for kv, err := range jks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			return nil, err
		}
		var jr journaled
		if err := json.Unmarshal(kv.Value, &jr); err != nil {
			t.Fatalf("a journal row does not decode: %v", err)
		}
		s.Journal[string(kv.Key)] = jr
	}
	entries, err := db.Quarantine(ctx, cutover.LogCutoverID)
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		s.Quarantine = append(s.Quarantine, quarantined{Key: string(en.Record.Key), Reason: en.Record.Reason, Step: en.Record.Step, Value: string(en.Record.Value)})
	}
	slices.SortFunc(s.Quarantine, func(a, b quarantined) int { return compareStrings(a.Key, b.Key) })
	sks, _ := db.Keyspace("core/storage")
	switch raw, err := sks.Get(ctx, storage.DataMigrationKey(cutover.LogCutoverID)); {
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

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// run boots a fresh copy of the rig's store with M5 and returns what it left.
func (r *rig) run(t testing.TB) (*snap, error) {
	t.Helper()
	return r.runOn(t, r.seed(t))
}

func (r *rig) runOn(t testing.TB, fsys vfs.FS) (*snap, error) {
	t.Helper()
	dm := r.migration()
	return r.runWith(t, fsys, dm)
}

func (r *rig) runWith(t testing.TB, fsys vfs.FS, dm datamigrate.DataMigration) (*snap, error) {
	t.Helper()
	var s *snap
	err := r.boot(context.Background(), fsys, dm, func(ctx context.Context, db *storage.DB) error {
		var err error
		s, err = read(ctx, t, db, dm)
		return err
	})
	return s, err
}

// syncTree makes every file under dir, every directory under it and every
// directory above it durable on fsys. A tree copied in with vfs.Clone is not:
// a crash clone of the unsynced copy opens as an empty store, which would make
// a crash-matrix cell compare nothing with nothing.
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

// before reads the syslog: rows of a seeded store as the pre-cutover build left
// them, from a copy of fsys so the store itself is untouched.
func (r *rig) before(t testing.TB, fsys *vfs.MemFS) map[string][]byte {
	t.Helper()
	clone := crashClone(fsys)
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(clone), Owners: r.owners(false), Build: rigBuildOld, FreeSpaceMargin: 1, Clock: crashmatrix.Now})
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
	lr, _ := db.Legacy(cutover.BooksOwner)
	out := map[string][]byte{}
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: m5.SourceRanges()[0].Start, End: m5.SourceRanges()[0].End}) {
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

// ---- synthetic rows ----

// batmanSources are the books subsystems a synthetic row names (HEAD's
// `source` is the writer's name).
var batmanSources = []string{"scanner", "organizer", "itunes-sync", "metadata-fetch", "dedup"}

// batmanMessages are synthetic messages: Batman titles, never a real library row.
var batmanMessages = []string{
	"Year One indexed", "The Long Halloween rescanned", "Hush matched to its series",
	"The Killing Joke moved into place", "A Death in the Family flagged for review",
	"Knightfall fingerprinted", "No Man's Land organized",
}

// syslogKey is HEAD's key for a row: syslog:<RFC3339Nano>:<source>.
func syslogKey(t time.Time, source string) string {
	return m5.Prefix + t.Format(time.RFC3339Nano) + ":" + source
}

// headRow is HEAD's stored row.
type headRow struct {
	ID        int       `json:"id"`
	UserID    string    `json:"user_id,omitempty"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// synth returns n synthetic rows, one per second from base, as key -> value.
func synth(t testing.TB, n int, base time.Time) map[string][]byte {
	t.Helper()
	levels := []string{"info", "warn", "error"}
	out := make(map[string][]byte, n)
	for i := range n {
		at := base.Add(time.Duration(i) * time.Second)
		src := batmanSources[i%len(batmanSources)] + "-" + strconv.Itoa(i)
		row := headRow{ID: i + 1, Source: src, Level: levels[i%len(levels)], Message: batmanMessages[i%len(batmanMessages)], CreatedAt: at}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out[syslogKey(at, src)] = raw
	}
	return out
}

// withRows is a rig option that writes rows into books' legacy range after the
// pre-freeze boot.
func withRows(rows map[string][]byte) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(ctx, t, lr)
			}
			for k, v := range rows {
				if err := lr.Set(ctx, []byte(k), v); err != nil {
					t.Fatalf("seed %s: %v", k, err)
				}
			}
		}
	}
}
