// file: internal/database/pebble_store_open.go
// version: 2.2.1
// guid: 2b0b5b99-cf7b-48ef-a8d6-5ff58c6d70db
// last-edited: 2026-11-09

package database

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/buildid"
	"github.com/falkcorp/mediacore/storage"
	ulid "github.com/oklog/ulid/v2"
)

// pebbleOpenLog logs through internal/logger, which sanitizes values, rather than
// calling log/slog directly (TestGuard_NoDirectSlogCalls).
var pebbleOpenLog = logger.New("database.pebble-open")

// THE OPENERS (release R1, 05-storage §5S.9.3). Core storage opens the one
// store; books reaches it only through its claims on a storage.LegacyRange.
// There are two ways in:
//
//   - The served app: the kernel opens, prepares and migrates the store
//     (06 P2-P4b) with books' Spec (BooksSpec) as one owner, and books'
//     Register builds the store over its Deps.Storage.Legacy with
//     NewBooksStore, which writes nothing (§5S.4 step 12). Books' start hook
//     then runs RunBootRepairs, before the server's hook.
//   - Every standalone caller (the CLI commands, the offline tools, tests):
//     NewPebbleStore and NewPebbleStoreInMemory open the same store the
//     same way through storage.Open, Prepare and Migrate with books as the
//     only owner, build the store over books' LegacyRange, and run HEAD's
//     open-time repairs (runOpenRepairs). The store owns that core DB and its
//     Close closes it.
//
// Either way the first boot of a store HEAD wrote takes the pre-migration
// checkpoint before it writes anything (core's Prepare), books' migrations
// run as core steps over HEAD's own version rows (booksBookkeeping), no
// counter is seeded (an absent counter hands out 1) and the storage-format
// stamp and sidecar are the guard's legacy projection (§5S.3.7), which stays
// at HEAD's format 1 at R1.

// NewPebbleStore opens the store at path for a standalone caller: through
// core storage, with books as the only owner (see THE OPENERS). A relative
// path is resolved against the working directory.
func NewPebbleStore(path string) (*PebbleStore, error) {
	return OpenStandalone(path, StandaloneOptions{})
}

// NewPebbleStoreInMemory opens a store backed by an in-memory filesystem.
// Nothing it writes ever reaches the disk, and the contents vanish when the
// process exits.
//
// This exists for tests. Every Pebble write in this package passes pebble.Sync
// explicitly, so on a real filesystem each one costs a genuine fsync. That is
// invisible in production — a handful of syncs against a long-lived database —
// but a test that builds a fresh store pays it for every migration and
// op-definition upsert, and internal/server did that 275 times.
//
// Measured on 2026-08-15: setupTestServer cost 1.61s, of which 0.935s was
// RunMigrations, 0.522s NewServer and 0.152s this constructor. A CPU profile
// showed 90ms of CPU for the 1.6s — 94% of it blocked in os.(*File).Sync, not
// computing. Backing the same code with an in-memory filesystem took the
// identical work to 0.01s and the internal/server package from 585.1s to
// 41.8s.
//
// Callers that need bytes on a real disk — anything that copies, backs up or
// re-opens the database by path — must use NewPebbleStore instead. Each call
// gets its own isolated filesystem, so two stores opened at the same path do
// not share state.
func NewPebbleStoreInMemory(path string) (*PebbleStore, error) {
	return OpenStandalone(path, StandaloneOptions{FS: storage.MemFS()})
}

// StandaloneOptions are a standalone open's inputs beyond the path.
type StandaloneOptions struct {
	// FS is the filesystem the store opens on; zero is the OS's.
	FS storage.FS
	// StateFiles are copied into the pre-migration checkpoint the first open
	// of a store HEAD wrote takes (the encryption key and the gap-fill
	// config, books.StateFiles).
	StateFiles []storage.StateFile
	// ExpectClaimViolations exempts this store from the test-binary claim
	// guard (claimGuardedClose): a test that makes an out-of-claim access on
	// purpose, to check strict claims refuse it, sets it. Outside a test
	// binary it changes nothing.
	ExpectClaimViolations bool
}

// claimGuardedClose is the close a standalone store hands newCoreStore. In a
// test binary (testing.Testing) it reads core's out-of-claim counts before it
// closes the DB and panics when any series is non-zero, naming each series
// and its first keys: a family books writes but keyfamilies.go misses then
// fails the test that wrote it even when the caller swallowed
// storage.ErrOutsideKeyspace (MarkIdentDirtyBestEffort-style best-effort
// writers do), and a scan clipped short leaves no count at all, so the
// whole-suite run is the backstop for reads (R1-23 review B1). The guard
// reads what core refused, not books' claims, so it cannot share a mistake
// with the registry. A store a test never closes is not checked. Production,
// and a store opened with ExpectClaimViolations, close plainly.
func claimGuardedClose(db *storage.DB, expect bool) func() error {
	if expect || !testing.Testing() {
		return db.Close
	}
	return func() error {
		counts, keys := db.ClaimViolations(), db.ClaimViolationKeys()
		err := db.Close()
		if len(counts) > 0 {
			panic(claimViolationReport(counts, keys))
		}
		return err
	}
}

// claimViolationReport is the claim guard's panic message: every series with
// its count and first keys, sorted.
func claimViolationReport(counts map[storage.ClaimViolation]uint64, keys map[storage.ClaimViolation][]string) string {
	lines := make([]string, 0, len(counts))
	for v, n := range counts {
		lines = append(lines, fmt.Sprintf("  %s %s %s x%d: %s", v.Owner, v.Op, v.Mode, n, strings.Join(keys[v], "; ")))
	}
	slices.Sort(lines)
	return "storage claim guard: out-of-claim accesses through a standalone store this test did not expect " +
		"(register the family in internal/database/keyfamilies.go, or open a store that refuses on purpose " +
		"with StandaloneOptions.ExpectClaimViolations):\n" + strings.Join(lines, "\n")
}

// standaloneBuildID is a standalone open's binary ID (05-storage §5S.3.7
// "Binary ID"): the store's build fields name the executable, as the
// kernel's do. Hashed once per process.
var standaloneBuildID = sync.OnceValues(func() (string, error) { return buildid.Binary("standalone") })

// OpenStandalone opens the store at path through core storage with books as
// the only owner: Open (the guard, the claim and foreign-key checks, which
// write nothing), Prepare (a fresh install's one batch, or the pre-migration
// checkpoint and the install marker), Migrate (books' pending steps), then
// the store over books' LegacyRange and HEAD's open-time repairs. A failure
// closes what it opened.
func OpenStandalone(path string, o StandaloneOptions) (*PebbleStore, error) {
	ctx := context.Background()
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("pebble store: resolve %s: %w", path, err)
	}
	build, err := standaloneBuildID()
	if err != nil {
		return nil, fmt.Errorf("pebble store: %w", err)
	}
	opened, err := storage.Open(ctx, storage.OpenOptions{
		Path:       abs,
		FS:         o.FS,
		Build:      build,
		StateFiles: o.StateFiles,
		Owners:     []storage.OwnerSpec{{Owner: BooksOwner, Enabled: true, Spec: BooksSpec()}},
		Logger:     logger.FromContext(ctx).With("subsystem", "storage"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open PebbleDB: %w", err)
	}
	db, err := opened.Prepare(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to prepare PebbleDB: %w", err), opened.Close())
	}
	if err := db.Migrate(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to run migrations: %w", err), db.Close())
	}
	if !db.Serves(BooksOwner) {
		return nil, errors.Join(fmt.Errorf("pebble store: books' migrations did not all run (applied %d)", db.AppliedVersion(BooksOwner)), db.Close())
	}
	lr, ok := db.Legacy(BooksOwner)
	if !ok {
		return nil, errors.Join(errors.New("pebble store: core storage gave books no legacy range"), db.Close())
	}
	closeDB := claimGuardedClose(db, o.ExpectClaimViolations)
	store, err := newCoreStore(lr, closeDB, warmNow)
	if err != nil {
		return nil, errors.Join(err, closeDB())
	}
	store.db.setStorageAdmin(db.Admin())
	if err := store.runOpenRepairs(); err != nil {
		return nil, errors.Join(err, store.Close())
	}
	pebbleOpenLog.Info("PebbleDB opened at %s through core storage (books at version %d)", logger.SanitizeLogValue(abs), db.AppliedVersion(BooksOwner))
	return store, nil
}

// NewBooksStore is books' constructor in the served app (05-storage §5S.9.3:
// HEAD's NewPebbleStore(path) becomes a constructor over books' storage
// handles): the store over r, books' LegacyRange from Deps.Storage, after
// the kernel has opened, prepared and migrated the store. It writes nothing
// (§5S.4 step 12, TestBooksConstructorWritesNothing): HEAD's open-time
// writers run in RunBootRepairs, books' start hook, which also starts the
// memdb warmup, so a boot that fails between Register and the start hooks
// leaves no goroutine reading the store the kernel is closing. The kernel owns the core
// DB, so the store's Close drains books' own readers and leaves the DB open;
// the kernel closes it after every module stopped. Attach core's admin
// surface with AttachStorageAdmin before anything administers the store.
//
// It takes the LegacyRange rather than module.StorageHandles because
// internal/database may not import mediacore/module (internal/arch:
// moduleAppImporters); books passes the handles' Legacy member.
func NewBooksStore(r storage.LegacyRange) (*PebbleStore, error) {
	return newCoreStore(r, nil, warmDeferred)
}

// AttachStorageAdmin hands the store core's admin surface (checkpoint,
// compaction, flush, size estimates, the collector's sample). The served app
// calls it with app.App.StorageAdmin once New has returned.
func (p *PebbleStore) AttachStorageAdmin(a *storage.Admin) {
	if a == nil {
		return
	}
	p.db.setStorageAdmin(a)
}

// newStepStore is the store a legacy migration step runs HEAD's
// MigrationFunc over: books' LegacyRange for the step, constructed like
// books' own store but without the memdb warmup (reads fall back to Pebble),
// and never closed (it owns nothing).
func newStepStore(r storage.LegacyRange) (*PebbleStore, error) {
	return newCoreStore(r, nil, warmNever)
}

// warmMode is when a core store starts its memdb warmup.
type warmMode int

const (
	// warmNever: no warmup; reads go to Pebble (a migration step's store).
	warmNever warmMode = iota
	// warmNow: the constructor starts it (the standalone opener, as HEAD's
	// open did).
	warmNow
	// warmDeferred: RunBootRepairs starts it (books' served store); a Close
	// before then means it never starts.
	warmDeferred
)

// newCoreStore builds a store over books' LegacyRange. It reads and writes
// nothing beyond the reads of construction: the journal clock seeded from
// its key (or from a read-only journal scan), the undecodable-marker set and
// the identification index state loaded. warm says when the async memdb
// warmup, which only reads, starts. closeStore closes the core DB the caller
// opened (nil: the kernel owns it).
func newCoreStore(r storage.LegacyRange, closeStore func() error, warm warmMode) (*PebbleStore, error) {
	closingCtx, closingCancel := context.WithCancel(context.Background())
	store := &PebbleStore{
		db:            newLegacyGatedDB(r, closeStore),
		UseMemDB:      true, // in-memory query layer is the default after Phase 3
		closingCtx:    closingCtx,
		closingCancel: closingCancel,
	}
	fail := func(err error) (*PebbleStore, error) {
		closingCancel()
		return nil, err
	}
	store.seedWorksGeneration()
	if err := store.seedJournalClock(); err != nil {
		return fail(err)
	}
	// Load the undecodable-marker set before any write can stage (or skip) a
	// marker delete (pebble_store_atpath_markers.go).
	if err := store.ensureUndecodableMarkersLoaded(); err != nil {
		return fail(err)
	}
	if err := store.loadIdentIndexState(); err != nil {
		return fail(fmt.Errorf("load identification index state: %w", err))
	}
	store.initMetrics()
	store.warmupDone = make(chan struct{})
	switch warm {
	case warmNow:
		store.warmOnce.Do(store.startMemWarmup)
	case warmNever:
		store.warmOnce.Do(store.skipMemWarmup)
	case warmDeferred:
	}
	return store, nil
}

// skipMemWarmup marks the warmup finished with none run: warmupDone closes
// and reads stay on Pebble. Called only inside warmOnce.
func (p *PebbleStore) skipMemWarmup() { close(p.warmupDone) }

// startMemWarmup initializes the in-memory query layer. Warmup runs in a
// goroutine so the server is available immediately — reads transparently
// fall back to Pebble until memdb is ready (couple of minutes for ~50K
// books). store.memPtr is only published once warmup completes successfully.
//
// The goroutine iterates p.db, so Close() must cancel it and wait for it to
// exit before closing the DB (else NewIter races the close and panics).
// warmupDone is always non-nil so Close can wait unconditionally; it is
// closed immediately when there is no warmup goroutine to wait for. Called
// only inside warmOnce.
func (p *PebbleStore) startMemWarmup() {
	memStore, memErr := NewMemStore()
	if memErr != nil {
		pebbleOpenLog.Warn("memdb init failed, in-memory queries disabled: %s", logger.SanitizeLogValue(memErr.Error()))
		close(p.warmupDone)
		return
	}
	warmupCtx, warmupCancel := context.WithCancel(context.Background())
	p.warmupCancel = warmupCancel

	// Arm the pending-write buffer SYNCHRONOUSLY, before the goroutine is
	// launched. The constructor returns to its caller as soon as `go` runs,
	// and the server starts writing immediately; if arming happened inside
	// the goroutine, a write that beat it there would be dropped. From here
	// until publish, memSync records instead of dropping. See
	// memdb_pending.go.
	p.beginMemWarmupBuffering()

	go func() {
		started := time.Now()
		defer close(p.warmupDone)
		// Registered after the close above so it runs first: the duration
		// is stored before warmupDone closes, on every exit path.
		defer func() { p.warmupDuration.Store(time.Since(started).Milliseconds()) }()
		// Disarm on EVERY exit path — warmup error, Close() cancelling the
		// context mid-scan, or success (where publishWarmMemStore has already
		// disarmed and this is a no-op). Otherwise a cancelled warmup leaves
		// the store buffering forever into a slice nobody drains.
		defer p.endMemWarmupBuffering()

		runWarmupStartHook()
		pebbleOpenLog.Info("memdb warmup starting (async)")
		if warmErr := memStore.WarmFromPebble(warmupCtx, p); warmErr != nil {
			pebbleOpenLog.Warn("memdb warmup failed after %d ms, will stay on Pebble for reads: %s",
				time.Since(started).Milliseconds(), logger.SanitizeLogValue(warmErr.Error()))
			return
		}
		// Replays the buffered write-throughs into memStore and publishes it in
		// one critical section, so no write can land between the two.
		if !p.publishWarmMemStore(memStore) {
			return
		}
		pebbleOpenLog.Info("memdb warmup published after %d ms", time.Since(started).Milliseconds())
	}()
}

// runOpenRepairs is HEAD's open-time writers that §5S.9.3 moves to books'
// start hook, minus the ops reconcile (HEAD ran that in InitializeStore, not
// at open): the absent storage_format stamp (stampStorageFormat), the
// journal clock key, the empty identification index's stamp and the
// verified-email reconcile. The standalone opener runs them at open, where
// HEAD did; both callers run them after the boot's pre-migration checkpoint.
func (p *PebbleStore) runOpenRepairs() error {
	if err := p.stampStorageFormat(); err != nil {
		return err
	}
	if err := p.repairJournalClock(); err != nil {
		return err
	}
	if err := p.stampEmptyIdentIndex(); err != nil {
		return fmt.Errorf("load identification index state: %w", err)
	}
	// Verified emails: stamp rows an older build wrote without an email
	// source and rewrite the idx:user:email cache. A failure is logged and
	// the store serves anyway: verification is computed live, an unstamped
	// row counts as unverified, and the next open retries.
	if _, err := p.ReconcileVerifiedEmails(); err != nil {
		authLog.Error("verified email: reconcile at open failed; rows from an older build count as unverified until it succeeds: %v", err)
	}
	return nil
}

// RunBootRepairs is books' boot repairs (05-storage §5S.9.3; 06 §6.5.2 step
// 2): HEAD's open-time writers (runOpenRepairs) and the opv3 reconcile HEAD
// ran in InitializeStore, before books serves and before the operations
// registry, whose boot resume acts on opv3 records. A reconcile failure is
// logged, not returned: opv2: is still the source of truth the registry runs
// from, and the next boot retries. It then starts the memdb warmup of a
// store NewBooksStore built (a no-op on any other store, or after Close).
func (p *PebbleStore) RunBootRepairs(ctx context.Context) error {
	if err := p.runOpenRepairs(); err != nil {
		return err
	}
	if _, err := p.ReconcileOpsV3WithV2(ctx); err != nil {
		opsV3Log.Error("opv3 reconcile failed; opv3 records may be stale until each run's next write, and the next boot retries: %v", err)
	}
	p.warmOnce.Do(p.startMemWarmup)
	return nil
}

// DefaultCloseDrainTimeout bounds Close's wait for in-flight engine accesses.
// The unit's TimeoutStopSec is 30s and the HTTP drain and ops registry spend
// part of it before the store closes, so this leaves room for both.
const DefaultCloseDrainTimeout = 10 * time.Second

var pebbleCloseLog = logger.New("database.pebble-close")

// closingContext returns a context cancelled when Close begins. A store built
// without initPebbleStore (test fixtures) gets one that is never cancelled.
func (p *PebbleStore) closingContext() context.Context {
	if p.closingCtx == nil {
		return context.Background()
	}
	return p.closingCtx
}

// Close closes the database.
//
// The engine is closed only after every in-flight access has released its
// lease (pebble_gate.go): an iterator, snapshot or Get closer released after
// pebble's Close corrupts its memtable refcounts and panics. Order matters:
//
//  1. Stop the goroutines Close owns (memdb warmup, stats recompute) and
//     cancel closingCtx so cooperative long scans unwind.
//  2. Mark the timeline index closed BEFORE the drain, so a reconcile that
//     finishes while Close waits is refused trust.
//  3. Refuse new leases, sync the WAL, wait for held ones, sync again
//     (GatedDB.drainAndSeal), holding NO store mutex: an in-flight op may
//     need opsMu, counterMu, a metadata stripe or sampleMu to finish and
//     release its lease.
//  4. Only then take sampleMu and close the engine.
//
// If the drain times out, the engine is left open and Close returns an
// error. Every row committed before the timeout has been synced to the WAL
// by then, so it survives the process exiting; closing under a held lease
// would crash the process instead. The WAL syncs before the engine closes are
// bounded too (a stalled disk can hold an fsync arbitrarily long): Close stops
// waiting for one at its deadline, at most DefaultCloseDrainTimeout plus a
// second in total, and the sync keeps a lease, so the engine stays open for as
// long as it runs. The bound ends where pebble's own Close begins: that call
// waits for running flushes and compactions and syncs the WAL itself, and is
// not bounded here.
//
// Close is idempotent: a second call, concurrent or later, returns the first
// call's result.
func (p *PebbleStore) Close() error {
	p.closeOnce.Do(func() { p.closeErr = p.closeStore() })
	return p.closeErr
}

func (p *PebbleStore) closeStore() error {
	if p.closingCancel != nil {
		p.closingCancel()
	}

	// Stop the async memdb warmup before closing the DB. The warmup goroutine
	// iterates p.db; closing the DB out from under it races warmIter's NewIter
	// and panics ("pebble: closed" / nil-pointer deref). Cancel it and wait for
	// it to fully exit first. A deferred warmup that never started never
	// will: claiming warmOnce here closes warmupDone with no goroutine.
	if p.warmupDone != nil {
		p.warmOnce.Do(p.skipMemWarmup)
	}
	if p.warmupCancel != nil {
		p.warmupCancel()
	}
	if p.warmupDone != nil {
		<-p.warmupDone
	}

	// Same hazard, second goroutine: GetDashboardStats can have a background
	// stats recompute in flight, and computeLibraryStats iterates p.db. Taking
	// libraryCountsRecomputeMu waits for it — the recompute holds that mutex from
	// the TryLock that started it until it returns — and setting the flag under
	// the same mutex stops a new one from starting after we let go.
	p.libraryCountsRecomputeMu.Lock()
	p.libraryStatsClosed = true
	p.libraryCountsRecomputeMu.Unlock()

	// A closed store reports the timeline index untrusted, and refuses any
	// later flip to trusted (markOpsV2TimelineClosed).
	p.markOpsV2TimelineClosed()

	// Lower the journal clock's persisted ceiling to its actual high-water mark,
	// so the next open does not start a window ahead of it. A failure only means
	// the next open starts at the (higher) ceiling, which is still correct.
	if err := p.journalClock.settle(p.persistJournalCeiling); err != nil {
		journalClockLog.Warn("close: could not settle the journal clock ceiling: %s", logger.SanitizeLogValue(err.Error()))
	}

	sealed, syncErr := p.db.drainAndSeal(p.closeDrainTimeout)
	if !sealed {
		// The drain timed out: the WAL is synced, the engine stays open.
		p.dbClosed.Store(true)
		return syncErr
	}

	p.sampleMu.Lock()
	defer p.sampleMu.Unlock()
	p.dbClosed.Store(true)
	return errors.Join(syncErr, p.db.closeEngine())
}

// Checkpoint writes a consistent point-in-time snapshot of the PebbleDB to
// destDir using Pebble's built-in checkpoint mechanism. The snapshot is safe
// to archive without the risk of torn writes that a live filepath.Walk incurs.
// destDir must not exist; Pebble creates it.
func (p *PebbleStore) Checkpoint(destDir string) error {
	return p.db.Checkpoint(destDir)
}

// DB returns the store's gated engine handle, for the stores that borrow it
// (embedding, metrics, activity, AI scan, catalog). Borrowers' accesses hold
// leases on the same gate, so Close waits for them too.
func (p *PebbleStore) DB() *GatedDB {
	return p.db
}

// Helper functions

// nextID hands out counter's next ID and advances it with its own Sync
// write. An absent counter hands out 1 (05-storage §5S.9.3: HEAD seeded every
// counter at open; R1 seeds none, so a construction writes nothing).
func (p *PebbleStore) nextID(counter string) (int, error) {
	p.counterMu.Lock()
	defer p.counterMu.Unlock()

	key := []byte(fmt.Sprintf("counter:%s", counter))

	id := 1
	value, closer, err := p.db.Get(key)
	switch {
	case err == nil:
		id, err = strconv.Atoi(string(value))
		_ = closer.Close()
		if err != nil {
			return 0, fmt.Errorf("counter %s: %w", counter, err)
		}
	case !errors.Is(err, pebble.ErrNotFound):
		return 0, err
	}

	if err := p.db.Set(key, []byte(strconv.Itoa(id+1)), pebble.Sync); err != nil {
		return 0, err
	}

	return id, nil
}

// orderedULIDSource hands out ULIDs that sort in the order they were handed
// out, process-wide, including within one millisecond.
//
// newULID used to build a fresh ulid.Monotonic source on every call. A
// monotonic source only orders the ids IT produced, so one per call ordered
// nothing: two ids stamped with the same millisecond carried independent
// random entropy and sorted either way round with equal odds (measured: 49,989
// of 100,000 consecutive ids out of order). Everything that breaks a tie on the
// id (search rank, the paged book listings) then put two rows created in the
// same millisecond in an arbitrary order, and the same pair of books could
// come out differently on the next run.
//
// The millisecond is read under the same lock as the entropy and never moves
// backwards, so neither two goroutines racing on the clock nor a wall-clock
// step can hand a later caller a smaller id.
type orderedULIDSource struct {
	mu      sync.Mutex
	now     func() time.Time
	entropy *ulid.MonotonicEntropy
	lastMS  uint64
}

func newOrderedULIDSource(now func() time.Time, random io.Reader) *orderedULIDSource {
	return &orderedULIDSource{now: now, entropy: ulid.Monotonic(random, 0)}
}

func (s *orderedULIDSource) next() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := max(ulid.Timestamp(s.now()), s.lastMS)
	id, err := ulid.New(ms, s.entropy)
	if errors.Is(err, ulid.ErrMonotonicOverflow) {
		// The 80-bit entropy ran out of room inside this millisecond. Borrow
		// the next one: it draws fresh entropy and still sorts after every id
		// already handed out, and lastMS keeps later callers from going back.
		ms++
		id, err = ulid.New(ms, s.entropy)
	}
	if err != nil {
		return "", err
	}
	s.lastMS = ms
	return id.String(), nil
}

var orderedULIDs = newOrderedULIDSource(time.Now, rand.Reader)

// newULID returns a record id that sorts after every id this process handed
// out before it. The low bits are still crypto/rand, but an id is NOT a
// secret: within one millisecond it is the previous id plus a bounded random
// step. Anything used as a credential must use newUnlinkedULID.
func newULID() (string, error) {
	return orderedULIDs.next()
}
