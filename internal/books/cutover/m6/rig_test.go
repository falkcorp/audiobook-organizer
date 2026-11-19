// file: internal/books/cutover/m6/rig_test.go
// version: 1.0.4
// guid: 7be2c0d4-8a51-4c3e-b0a6-2f1d94c7e6a3
// last-edited: 2026-11-19

package m6_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// The M6 test rig: books' store (an empty one with synthetic Batman-shaped HEAD
// rows) in an in-memory filesystem, cut over by M6 alone through the kernel's
// data-migration runner and its P4a preflight, never through books.Module().
// M6's importers are core's real ones (integrations, ai and notify over their
// own keyspaces, with the audit store the integrations importer needs doubled to
// a keyspace write). The two importers whose real implementations are the whole
// config and secrets machinery (config.Store, secrets.Store with its keyring)
// are doubles over a keyspace each, in keeping with m5's journal double; what
// they prove is M6's side of the contract: what is staged, under which name,
// into the chunk's batch.

const (
	rigDir      = "/batcave/db"
	rigStore    = "/batcave/db/audiobooks.pebble"
	rigBuildOld = "batcomputer-1"
	rigBuildNew = "batcomputer-2"

	ownerInteg   = "core/integrations"
	ownerAI      = "core/ai"
	ownerNotify  = "core/notify"
	ownerConfig  = "core/config"
	ownerSecrets = "core/secrets"
	ownerAudit   = "core/audit"
)

const corpusRel = "internal/books/testdata/r2corpus"

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

// fromCorpus seeds the store from the R2 corpus's stage1 or overlay copy.
func fromCorpus(name string) func(*rig) { return func(r *rig) { r.corpus = name } }

var errKilled = errors.New("the process was killed here")

func rigDiskUsage() vfs.DiskUsage {
	return vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}
}

// crashClone is the filesystem as a power cut would leave it (only what was
// synced), with the disk-usage report a CrashClone does not carry over.
func crashClone(m *vfs.MemFS) *vfs.MemFS {
	c := m.CrashClone(vfs.CrashCloneCfg{})
	c.TestingSetDiskUsage(rigDiskUsage())
	return c
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

// ---- the doubles ----

// bound is a keyspace bound after Prepare.
type bound struct {
	mu sync.Mutex
	ks storage.Keyspace
}

func (b *bound) bind(ks storage.Keyspace) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ks = ks
}

func (b *bound) get() storage.Keyspace {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ks
}

// secretsDouble is secrets.Importer over the core/secrets keyspace, plus the
// secrets M2 left in the store before M6 ran (seed) and the ones it marked
// unreadable. A secret staged into the store is a row whose value is the
// plaintext, which is exactly what the "no secret in any other row" tests look
// for.
type secretsDouble struct {
	bound
	mu         sync.Mutex
	seed       map[secrets.Name]string
	unreadable map[secrets.Name]bool
	imports    []secrets.Name
}

func newSecrets() *secretsDouble {
	return &secretsDouble{seed: map[secrets.Name]string{}, unreadable: map[secrets.Name]bool{}}
}

func (s *secretsDouble) ImportSecret(ctx context.Context, b *storage.Batch, w secrets.Write) error {
	// The real store refuses a name no secret can have; the double must too, or a
	// stand-in that spells one wrong is never caught.
	if err := w.Name.Validate(); err != nil {
		return fmt.Errorf("the secrets store refuses the name %q: %w", w.Name, err)
	}
	s.mu.Lock()
	s.imports = append(s.imports, w.Name)
	s.mu.Unlock()
	ks := s.get()
	b.Bind(ks)
	raw, err := b.GetForUpdate(ctx, ks, []byte(w.Name))
	switch {
	case err == nil:
		if string(raw) == w.Value.Reveal() {
			return nil
		}
		return fmt.Errorf("%w: %s", storage.ErrImportConflict, w.Name)
	case !errors.Is(err, storage.ErrNotFound):
		return err
	}
	return b.In(ks).Set([]byte(w.Name), []byte(w.Value.Reveal()))
}

func (s *secretsDouble) ImportUnreadable(context.Context, *storage.Batch, secrets.Name, string, string) error {
	return errors.New("the rig's secrets double does not import unreadable markers: M6 never does")
}

func (s *secretsDouble) names(ctx context.Context) ([]secrets.Name, error) {
	s.mu.Lock()
	set := map[secrets.Name]bool{}
	for n := range s.seed {
		set[n] = true
	}
	for n := range s.unreadable {
		set[n] = true
	}
	s.mu.Unlock()
	// A name in the store was written through ImportSecret, which validates; one
	// placed by a test's seed is held to the same rule.
	for n := range set {
		if err := n.Validate(); err != nil {
			return nil, fmt.Errorf("the rig's seed holds the name %q, which the secrets store would refuse: %w", n, err)
		}
	}
	if ks := s.get(); ks.Prefix() != "" {
		for kv, err := range ks.Scan(ctx, storage.ScanOptions{}) {
			if err != nil {
				return nil, err
			}
			set[secrets.Name(kv.Key)] = true
		}
	}
	return slices.Sorted(maps.Keys(set)), nil
}

func (s *secretsDouble) value(ctx context.Context, n secrets.Name) (string, bool, error) {
	s.mu.Lock()
	if s.unreadable[n] {
		s.mu.Unlock()
		return "", false, nil
	}
	v, ok := s.seed[n]
	s.mu.Unlock()
	if ok {
		return v, true, nil
	}
	if ks := s.get(); ks.Prefix() != "" {
		raw, err := ks.Get(ctx, []byte(n))
		switch {
		case err == nil:
			return string(raw), true, nil
		case !errors.Is(err, storage.ErrNotFound):
			return "", false, err
		}
	}
	return "", false, nil
}

// sealer is storage's quarantine sealer over the secrets double, the adapter
// the kernel binds at boot: a sensitive row's value is stored as a secret named
// by the record, never in the record.
type sealer struct{ s *secretsDouble }

func (z sealer) Seal(ctx context.Context, b *storage.Batch, name string, raw []byte) error {
	return z.s.ImportSecret(ctx, b, secrets.Write{Name: secrets.Name(name), Value: secrets.NewValue(string(raw)), By: "quarantine"})
}

// configDouble is config.Importer over the core/config keyspace: a staged list
// is one row named by its path, holding the JSON M6 gave. failFor refuses a path
// as config's schema validation would a bad value.
type configDouble struct {
	bound
	mu      sync.Mutex
	failFor map[config.Path]error
	staged  map[config.Path]int
}

func (c *configDouble) Stage(b *storage.Batch, p config.Path, v json.RawMessage, by string) error {
	c.mu.Lock()
	if c.staged == nil {
		c.staged = map[config.Path]int{}
	}
	c.staged[p]++
	err := c.failFor[p]
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return setSerialized(context.Background(), b, c.get(), []byte(p), v)
}

func (c *configDouble) Default(config.Path) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}

// auditDouble is integrations.AuditRecorder over the core/audit keyspace. A
// record's key is its action and instance, so staging it again after a resume
// writes the same row.
type auditDouble struct{ bound }

func (a *auditDouble) RecordIn(_ context.Context, b *storage.Batch, e audit.Event) error {
	raw, err := json.Marshal(map[string]any{"action": e.Action, "details": e.Details})
	if err != nil {
		return err
	}
	// core/audit's keyspace is not Serialized (append-only records), so the write
	// carries no Expect: storage refuses a precondition outside a Serialized range.
	b.Bind(a.get())
	return b.In(a.get()).Set([]byte(fmt.Sprintf("%s|%v", e.Action, e.Details["instance"])), raw)
}

// setSerialized writes key the way a Serialized owner's own store does: the
// key is read for update first, so the commit carries an Expect on it. A blind
// write to a Serialized range is refused by the storage layer.
func setSerialized(ctx context.Context, b *storage.Batch, ks storage.Keyspace, key, value []byte) error {
	b.Bind(ks)
	if _, err := b.GetForUpdate(ctx, ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return b.In(ks).Set(key, value)
}

// catalogView is the AI router's catalog as the importer asks of it.
type catalogView struct{ caps, kinds map[string]bool }

func (c catalogView) HasCapability(id string) bool { return c.caps[id] }
func (c catalogView) HasApplyKind(k string) bool   { return c.kinds[k] }

// topicCatalog is the events catalog the notify importer validates a rule's
// topics against: the twelve books topics, each notifiable.
type topicCatalog map[string]events.TopicSpec

func (c topicCatalog) Lookup(n string) (events.TopicSpec, bool) { s, ok := c[n]; return s, ok }
func (c topicCatalog) All() iter.Seq[events.TopicSpec] {
	return func(yield func(events.TopicSpec) bool) {
		for _, n := range slices.Sorted(maps.Keys(c)) {
			if !yield(c[n]) {
				return
			}
		}
	}
}

func booksTopics() topicCatalog {
	c := topicCatalog{}
	for _, n := range []string{
		"books.book.imported.v1", "books.book.deleted.v1", "books.metadata.applied.v1", "books.tags.written.v1",
		"books.file.organized.v1", "books.dedup.detected.v1", "books.dedup.merged.v1", "books.cover.changed.v1",
		"books.read-status.changed.v1", "books.scan.completed.v1", "books.book.quarantined.v1", "books.book.unquarantined.v1",
	} {
		c[n] = events.TopicSpec{Name: n, Notify: &events.NotifyMeta{Title: n, DefaultSeverity: "info"}}
	}
	return c
}

// The importers M6 is built with before the store is open, delegating to the
// real ones built between Prepare and Migrate.
type lazyInteg struct{ r *rig }
type lazyAI struct{ r *rig }
type lazyNotify struct{ r *rig }

func (l lazyInteg) ImportInstance(ctx context.Context, b *storage.Batch, i integrations.StoredInstance) error {
	if err := l.r.refused("instance", i.Config.Ref.String()); err != nil {
		return err
	}
	l.r.mu.Lock()
	imp := l.r.integ
	l.r.mu.Unlock()
	return imp.ImportInstance(ctx, b, i)
}

func (l lazyAI) ImportJob(ctx context.Context, b *storage.Batch, j ai.StoredJob) error {
	if err := l.r.refused("job", string(j.ID)); err != nil {
		return err
	}
	l.r.mu.Lock()
	imp := l.r.ai
	l.r.mu.Unlock()
	return imp.ImportJob(ctx, b, j)
}

func (l lazyNotify) ImportRule(ctx context.Context, b *storage.Batch, ru notify.Rule) error {
	l.r.mu.Lock()
	imp := l.r.notify
	l.r.mu.Unlock()
	return imp.ImportRule(ctx, b, ru)
}

// refused is the error the test makes an importer return for (kind, id): what a
// store that already holds a different row under the ID answers. nil when no
// test asked for one.
func (r *rig) refused(kind, id string) error {
	r.mu.Lock()
	h := r.refuse
	r.mu.Unlock()
	if h == nil {
		return nil
	}
	return h(kind, id)
}

// ---- the rig ----

type rig struct {
	t          testing.TB
	chunkItems int
	// mutate edits books' legacy range after the pre-freeze boot.
	mutate func(ctx context.Context, t testing.TB, lr storage.LegacyRange)
	// parallelism overrides the step's declaration.
	parallelism func(*datamigrate.DataMigration)
	// effective is the operator overlay the step's Effective answers, by
	// instance ref ("download/deluge"): URL field to the URL as supplied.
	effective map[string]map[string]string
	// effectiveErr makes Effective fail.
	effectiveErr error
	// corpus is "stage1" or "overlay" (copied from the R2 corpus), or "" for an
	// empty store the test fills.
	corpus string
	report *m6.Report
	logs   *lockedBuffer

	secrets *secretsDouble
	cfg     *configDouble
	audit   *auditDouble

	mu     sync.Mutex
	lr     storage.LegacyRange
	integ  integrations.Importer
	ai     ai.Importer
	notify notify.Importer

	// refuse makes an importer refuse (kind "instance" or "job", id as the
	// importer names it) with the error it returns.
	refuse func(kind, id string) error

	// noLogger builds the step without a Logger, as a caller that has none does.
	noLogger bool

	// aiCatalog and noCatalog shape the AI importer's catalog.
	noCatalog bool
}

func newRig(t testing.TB, opts ...func(*rig)) *rig {
	t.Helper()
	r := &rig{t: t, chunkItems: 1, logs: &lockedBuffer{}, secrets: newSecrets(), cfg: &configDouble{}, audit: &auditDouble{}, report: &m6.Report{}}
	// The secrets M2 left in the store: every test starts from the full set.
	maps.Copy(r.secrets.seed, secretValues)
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

// spec is books' storage spec for the build: the pre-cutover one, or the cutover
// build's with books at its cutover level behind books' level step, the claims
// M6 drains retired and the claim M6 writes added. counter:import_path and
// idx:sidx:dirty: are nested inside books' whole-family counter: and idx:
// claims and the real spec does not split them yet, so the rig splits them; the
// rootscan: family is a new books family with no claim yet, so the rig adds one.
func (r *rig) spec(cut bool) storage.Spec {
	spec := store.BooksSpec(nil)
	if !cut {
		return spec
	}
	spec.Level = cutover.BooksCutoverLevel
	spec.Migrations = append(spec.Migrations, cutover.BooksLevelStep(r.levelVersion(), cutover.BooksCutoverLevel))
	retire := map[string]bool{
		m6.PrefixAIJob: true, m6.PrefixAIJobBatch: true, m6.PrefixAIJobPayload: true, m6.PrefixImportPath: true,
		m6.PrefixDailyBudget: true, m6.PrefixThrottle: true,
	}
	hit := 0
	for i := range spec.Claims {
		c := &spec.Claims[i]
		if (c.Prefix != "" && retire[c.Prefix]) || (c.Key == m6.KeyBlob) {
			c.Mode = storage.ClaimRetired
			hit++
		}
	}
	if hit != len(retire)+1 {
		r.t.Fatalf("books' storage spec has %d of the %d claims M6 retires", hit, len(retire)+1)
	}
	spec.Claims = append(spec.Claims,
		storage.Claim{Key: m6.KeyImportCounter, Mode: storage.ClaimRetired, Description: "test: the import-path counter, split from counter:"},
		storage.Claim{Prefix: m6.PrefixDirty, Mode: storage.ClaimRetired, Description: "test: the search dirty set, split from idx:"},
		storage.Claim{Prefix: m6.PrefixRootScan, Mode: storage.ClaimReadWrite, Description: "test: books' per-root scan state"},
	)
	return spec
}

func (r *rig) owners(cut bool) []storage.OwnerSpec {
	ser := storage.Spec{Keyspace: true, Serialized: []string{""}}
	return []storage.OwnerSpec{
		{Owner: cutover.BooksOwner, Spec: r.spec(cut), Enabled: true},
		// core/integrations' own spec is not importable (core/internal/instancestore):
		// its Serialized range is the instance rows, c/integrations/instances/.
		{Owner: ownerInteg, Spec: storage.Spec{Keyspace: true, Serialized: []string{string(keys.Prefix(keys.Str("instances")))}}, Enabled: true},
		{Owner: ownerAI, Spec: ai.StorageSpec(), Enabled: true},
		{Owner: ownerNotify, Spec: notify.StorageSpec(), Enabled: true},
		{Owner: ownerConfig, Spec: ser, Enabled: true},
		{Owner: ownerSecrets, Spec: ser, Enabled: true},
		{Owner: ownerAudit, Spec: storage.Spec{Keyspace: true}, Enabled: true},
	}
}

// fixedOne is the step's own declaration: one shard, key order.
func fixedOne() tasks.Parallelism { return tasks.Sequential("test: one shard in key order") }

// logger is the step's logger: the rig's buffer, or none.
func (r *rig) logger() *slog.Logger {
	if r.noLogger {
		return nil
	}
	return slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// config is the step's Config over this rig, built the way F7-09 builds it.
func (r *rig) config() m6.Config {
	return m6.Config{
		Integrations: lazyInteg{r}, AI: lazyAI{r}, Notify: lazyNotify{r}, Config: r.cfg, Secrets: r.secrets,
		SecretNames: r.secrets.names, SecretValue: r.secrets.value,
		Legacy: r.legacy, Effective: r.overlay,
		Report: r.report,
		Logger: r.logger(),
	}
}

// overlay is Config.Effective: the operator's URLs the test set for ref.
func (r *rig) overlay(_ context.Context, ref integrations.Ref) (map[string]string, error) {
	if r.effectiveErr != nil {
		return nil, r.effectiveErr
	}
	return maps.Clone(r.effective[ref.String()]), nil
}

// migration is M6 over this rig.
func (r *rig) migration() datamigrate.DataMigration {
	dm, err := m6.New(r.config(), cutover.IntegrationsCutoverID, cutover.BooksCutoverLevel, []storage.StepRef{cutover.BooksLevelStepRef(r.levelVersion())})
	if err != nil {
		r.t.Fatal(err)
	}
	if r.parallelism != nil {
		r.parallelism(&dm)
	}
	return dm
}

// withStore opens fsys as the cutover build, prepared and with the doubles bound
// but not migrated, runs fn, and closes it: the state of a boot between Prepare
// and Migrate, which is where F7-09's dry run runs Plan.
func (r *rig) withStore(t testing.TB, fsys vfs.FS, fn func(ctx context.Context, db *storage.DB) error) error {
	t.Helper()
	ctx := context.Background()
	o, err := r.open(ctx, fsys, []datamigrate.DataMigration{r.migration()})
	if err != nil {
		return err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	r.bind(db)
	return errors.Join(fn(ctx, db), db.Close())
}

// dump is every row of every keyspace of the open store, including books' legacy
// range and core's own bookkeeping, keyed by keyspace.
func (r *rig) dump(ctx context.Context, t testing.TB, db *storage.DB) map[string]map[string][]byte {
	t.Helper()
	out := map[string]map[string][]byte{}
	lr, _ := db.Legacy(cutover.BooksOwner)
	out["legacy"] = scanAll(ctx, t, lr)
	for _, o := range []storage.Owner{ownerInteg, ownerAI, ownerNotify, ownerConfig, ownerSecrets, ownerAudit, "core/storage"} {
		ks, ok := db.Keyspace(o)
		if !ok {
			t.Fatalf("no keyspace for %s", o)
		}
		out[string(o)] = scanAll(ctx, t, ks)
	}
	return out
}

// legacy is books' legacy range of the open store; valid between Prepare and
// Close.
func (r *rig) legacy() storage.Reader {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lr
}

func (r *rig) seed(t testing.TB) *vfs.MemFS {
	t.Helper()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(rigDiskUsage())
	if err := mem.MkdirAll(rigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if r.corpus != "" {
		src := filepath.Join(repoRoot(t), filepath.FromSlash(corpusRel), r.corpus, "db")
		// The search index sits beside the store; M6 must leave it alone.
		for _, n := range []string{"audiobooks.pebble", "audiobooks.pebble.storage-format", "library.bleve"} {
			if _, err := vfs.Clone(vfs.Default, mem, filepath.Join(src, n), rigDir+"/"+n); err != nil {
				t.Fatalf("copy the corpus %s: %v", n, err)
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
		Quarantine: storage.QuarantineWriter(sealer{r.secrets}), Preflight: kernel.CutoverPreflight(slog.New(slog.NewTextHandler(r.logs, nil)))}
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

// bind builds the real importers over the open store's keyspaces and binds the
// doubles, between Prepare and Migrate.
func (r *rig) bind(db *storage.DB) {
	ks := func(o storage.Owner) storage.Keyspace {
		k, ok := db.Keyspace(o)
		if !ok {
			r.t.Fatalf("no keyspace for %s", o)
		}
		return k
	}
	r.secrets.bind(ks(ownerSecrets))
	r.cfg.bind(ks(ownerConfig))
	r.audit.bind(ks(ownerAudit))
	lr, _ := db.Legacy(cutover.BooksOwner)
	var aiOpts []ai.ImporterOption
	if !r.noCatalog {
		aiOpts = append(aiOpts, ai.ImportWithCatalog(catalogView{
			caps:  map[string]bool{"books.dedup_review": true, "books.author_dedup_batch": true},
			kinds: map[string]bool{"books.ai.batch-apply.dedup-review": true, "books.ai.batch-apply.author-dedup-batch": true},
		}))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lr = lr
	r.integ = integrations.NativeImporter(ks(ownerInteg), integrations.ImportAudit(r.audit))
	r.ai = ai.NativeImporter(ks(ownerAI), aiOpts...)
	r.notify = notify.NativeImporter(ks(ownerNotify), notify.WithCatalog(booksTopics()))
}

func (r *rig) boot(ctx context.Context, fsys vfs.FS, dm datamigrate.DataMigration, observe func(ctx context.Context, db *storage.DB) error) error {
	o, err := r.open(ctx, fsys, []datamigrate.DataMigration{dm})
	if err != nil {
		return err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	r.bind(db)
	err = db.Migrate(ctx)
	if observe != nil {
		err = errors.Join(err, observe(ctx, db))
	}
	return errors.Join(err, db.Close())
}

// ---- reading a store back ----

type quarantined struct {
	Key, Reason, Step, Value string
	Sealed                   bool
}

// snap is a booted store as the tests read it.
type snap struct {
	Legacy     map[string][]byte // every row of books' legacy range
	Rows       map[string]map[string][]byte
	Quarantine []quarantined
	State      string
	Verify     datamigrate.VerifyReport
}

func scanAll(ctx context.Context, t testing.TB, r storage.Reader) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for kv, err := range r.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	return out
}

func (r *rig) read(ctx context.Context, t testing.TB, db *storage.DB, dm datamigrate.DataMigration) (*snap, error) {
	t.Helper()
	s := &snap{Rows: map[string]map[string][]byte{}}
	lr, _ := db.Legacy(cutover.BooksOwner)
	s.Legacy = scanAll(ctx, t, lr)
	for _, o := range []storage.Owner{ownerInteg, ownerAI, ownerNotify, ownerConfig, ownerSecrets, ownerAudit} {
		ks, _ := db.Keyspace(o)
		s.Rows[string(o)] = scanAll(ctx, t, ks)
	}
	entries, err := db.Quarantine(ctx, cutover.IntegrationsCutoverID)
	if err != nil {
		return nil, err
	}
	for _, en := range entries {
		q := quarantined{Key: string(en.Record.Key), Reason: en.Record.Reason, Step: en.Record.Step, Value: string(en.Record.Value), Sealed: en.Sealed()}
		if en.Sealed() {
			v, ok, err := r.secrets.value(ctx, secrets.Name(en.Record.Secret))
			if err != nil || !ok {
				return nil, fmt.Errorf("quarantine record %d names the sealed secret %s, which is not readable (%v)", en.N, en.Record.Secret, err)
			}
			q.Value = v
		}
		s.Quarantine = append(s.Quarantine, q)
	}
	slices.SortFunc(s.Quarantine, func(a, b quarantined) int { return compare(a.Key, b.Key) })
	sks, _ := db.Keyspace("core/storage")
	switch raw, err := sks.Get(ctx, storage.DataMigrationKey(cutover.IntegrationsCutoverID)); {
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

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (r *rig) run(t testing.TB) (*snap, error) {
	t.Helper()
	return r.runOn(t, r.seed(t))
}

func (r *rig) runOn(t testing.TB, fsys vfs.FS) (*snap, error) {
	t.Helper()
	return r.runWith(t, fsys, r.migration())
}

func (r *rig) runWith(t testing.TB, fsys vfs.FS, dm datamigrate.DataMigration) (*snap, error) {
	t.Helper()
	var s *snap
	err := r.boot(context.Background(), fsys, dm, func(ctx context.Context, db *storage.DB) error {
		var err error
		s, err = r.read(ctx, t, db, dm)
		return err
	})
	return s, err
}

// before reads books' legacy range as the pre-cutover build left it, from a copy
// of fsys so the store itself is untouched.
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
	out := scanAll(ctx, t, lr)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return out
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

// withRows writes rows into books' legacy range after the pre-freeze boot.
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
