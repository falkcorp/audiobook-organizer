// file: internal/database/pebble_store_open.go
// version: 1.0.1
// guid: 2b0b5b99-cf7b-48ef-a8d6-5ff58c6d70db
// last-edited: 2026-11-06

package database

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	ulid "github.com/oklog/ulid/v2"
)

// pebbleOpenLog logs through internal/logger, which sanitizes values, rather than
// calling log/slog directly (TestGuard_NoDirectSlogCalls).
var pebbleOpenLog = logger.New("database.pebble-open")

// NewPebbleStore creates a new PebbleDB store
func NewPebbleStore(path string) (*PebbleStore, error) {
	return newPebbleStore(path, nil)
}

// NewPebbleStoreInMemory opens a PebbleDB backed by an in-memory filesystem.
// Nothing it writes ever reaches the disk, and the contents vanish when the
// process exits.
//
// This exists for tests. Every Pebble write in this package passes pebble.Sync
// explicitly, so on a real filesystem each one costs a genuine fsync. That is
// invisible in production — a handful of syncs against a long-lived database —
// but a test that builds a fresh store pays it for all 60 migrations plus every
// op-definition upsert, and internal/server did that 275 times.
//
// Measured on 2026-08-15: setupTestServer cost 1.61s, of which 0.935s was
// RunMigrations, 0.522s NewServer and 0.152s this constructor. A CPU profile
// showed 90ms of CPU for the 1.6s — 94% of it blocked in os.(*File).Sync, not
// computing. Backing the same code with vfs.NewMem took the identical work to
// 0.01s and the internal/server package from 585.1s to 41.8s. The migrations
// still run; only the flush to disk goes away.
//
// Two alternatives were measured and rejected: Options.DisableWAL fails
// outright ("pebble: WAL disabled") because the writes pass pebble.Sync
// explicitly, and Options.WALMinSyncInterval made it *worse* (4.95s) by adding
// wait rather than removing syncs.
//
// Callers that need bytes on a real disk — anything that copies, backs up or
// re-opens the database by path — must use NewPebbleStore instead. Each call
// gets its own isolated filesystem, so two stores opened at the same path do
// not share state.
func NewPebbleStoreInMemory(path string) (*PebbleStore, error) {
	return newPebbleStore(path, vfs.NewMem())
}

// newPebbleStore is the shared constructor. A nil fs means the real filesystem,
// which is what production uses. It is the open phase (which writes nothing
// and refuses a store whose storage format this build cannot serve) followed
// by the init phase.
func newPebbleStore(path string, fs vfs.FS) (*PebbleStore, error) {
	db, st, err := openPebbleChecked(path, fs, openForServe)
	if err != nil {
		return nil, err
	}
	return initPebbleStore(db, path, fs, st)
}

// pebbleOpenMode selects which storage-format refusals openPebbleChecked
// applies.
type pebbleOpenMode int

const (
	// openForServe is every normal open: it refuses a store that is newer
	// than this build, older than this build, or carries the storage
	// migration marker.
	openForServe pebbleOpenMode = iota
	// openForCutover refuses only a store that is newer than this build. The
	// release B cut-over (RunCutover) is its only intended production caller;
	// it is the one open allowed to see an older or marked store.
	openForCutover
)

// openPebbleChecked is the open phase: pebble.Open, then the storage-format
// checks. It writes nothing to the store, so a refused open leaves the store
// exactly as it found it, and "empty at open" is decided here, before the init
// phase writes its first counter.
//
// It passes no FormatMajorVersion. Pebble resolves that to
// FormatMinSupported (options.go: FormatDefault), and Open only ratchets the
// on-disk format UP to the requested version, so an existing store keeps its
// format and a refused open cannot ratchet it. initPebbleStore ratchets to
// PebbleFormatMajorVersion once the checks have passed; a cut-over caller of
// openForCutover must do the same before it writes.
func openPebbleChecked(path string, fs vfs.FS, mode pebbleOpenMode) (*pebble.DB, storageFormatState, error) {
	var st storageFormatState
	opts := &pebble.Options{}
	if fs != nil {
		opts.FS = fs
	}
	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, st, fmt.Errorf("failed to open PebbleDB: %w", err)
	}
	fail := func(err error) (*pebble.DB, storageFormatState, error) {
		if closeErr := db.Close(); closeErr != nil {
			storageFormatLog.Error("close PebbleDB after refused open: path=%s error=%s",
				logger.SanitizeLogValue(path), logger.SanitizeLogValue(closeErr.Error()))
		}
		return nil, st, err
	}

	if st.stamp, st.stampPresent, err = readStorageFormatStamp(db); err != nil {
		return fail(fmt.Errorf("%s: %w", path, err))
	}
	if st.markerPresent, err = storageMigrationMarkerPresent(db); err != nil {
		return fail(fmt.Errorf("%s: %w", path, err))
	}
	if st.emptyAtOpen, err = storeIsEmpty(db); err != nil {
		return fail(fmt.Errorf("%s: %w", path, err))
	}
	sidecarFS := fs
	if sidecarFS == nil {
		sidecarFS = vfs.Default
	}
	if sc, ok, scErr := readStorageFormatSidecar(sidecarFS, path); scErr != nil {
		// Unreadable content: the init phase rewrites the sidecar from the
		// stamp. The stamp itself is still checked below.
		storageFormatLog.Error("storage format sidecar unreadable; it will be rewritten from the stamp: path=%s error=%s",
			logger.SanitizeLogValue(StorageFormatSidecarPath(path)), logger.SanitizeLogValue(scErr.Error()))
	} else {
		st.sidecar, st.sidecarPresent = sc, ok
	}

	switch {
	case st.stampPresent:
		st.effective = st.stamp
	case !st.emptyAtOpen:
		st.effective = legacyStorageFormat
	default:
		st.effective = buildStorageFormat
	}

	if st.stampPresent && st.stamp > buildStorageFormat {
		return fail(&StorageFormatTooNewError{Path: path, Source: "stamp", Stamp: st.stamp, Supported: buildStorageFormat})
	}
	if st.sidecarPresent && st.sidecar > buildStorageFormat {
		return fail(&StorageFormatTooNewError{Path: path, Source: "sidecar", Stamp: st.sidecar, Supported: buildStorageFormat})
	}
	if mode == openForServe && (st.effective < buildStorageFormat || st.markerPresent) {
		return fail(&StorageMigrationRequiredError{
			Path:          path,
			Stamp:         st.effective,
			Supported:     buildStorageFormat,
			MarkerPresent: st.markerPresent,
		})
	}
	return db, st, nil
}

// initPebbleStore is the init phase: undecodable markers, the import-path key
// migration, counters, the storage format stamp and sidecar, then the async
// memdb warmup. db must come from openPebbleChecked, and st is what it found.
func initPebbleStore(db *pebble.DB, path string, fs vfs.FS, st storageFormatState) (*PebbleStore, error) {
	// The open phase opened at whatever format the store was already at; now
	// that the store passed the checks, bring it to the pinned format.
	if err := db.RatchetFormatMajorVersion(PebbleFormatMajorVersion); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			storageFormatLog.Error("close PebbleDB after format ratchet failure: path=%s error=%s",
				logger.SanitizeLogValue(path), logger.SanitizeLogValue(closeErr.Error()))
		}
		return nil, fmt.Errorf("ratchet %s to Pebble format %d: %w", path, PebbleFormatMajorVersion, err)
	}

	closingCtx, closingCancel := context.WithCancel(context.Background())
	store := &PebbleStore{
		db:            NewGatedDB(db),
		UseMemDB:      true, // in-memory query layer is the default after Phase 3
		closingCtx:    closingCtx,
		closingCancel: closingCancel,
	}
	store.seedWorksGeneration()
	if err := store.seedJournalClock(); err != nil {
		db.Close()
		return nil, err
	}

	// Load the undecodable-marker set before any write can stage (or skip) a
	// marker delete (pebble_store_atpath_markers.go).
	if err := store.ensureUndecodableMarkersLoaded(); err != nil {
		db.Close()
		return nil, err
	}

	pebbleOpenLog.Info("PebbleDB opened at %s (format version %d)", logger.SanitizeLogValue(path), db.FormatMajorVersion())

	if err := store.migrateImportPathKeys(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate import path keys: %w", err)
	}

	// Initialize counters if they don't exist
	counters := []string{"author", "author_alias", "series", "book", "import_path", "operationlog", "playlist", "playlistitem", "preference"}
	for _, counter := range counters {
		key := fmt.Sprintf("counter:%s", counter)
		if _, closer, err := db.Get([]byte(key)); err == pebble.ErrNotFound {
			if err := db.Set([]byte(key), []byte("1"), pebble.Sync); err != nil {
				db.Close()
				return nil, fmt.Errorf("failed to initialize counter %s: %w", counter, err)
			}
		} else if err == nil {
			closer.Close()
		} else {
			db.Close()
			return nil, fmt.Errorf("failed to check counter %s: %w", counter, err)
		}
	}

	// The identification index: recount its counters when it is built, stamp
	// it when the store is empty, or leave it not ready for
	// SuperviseIdentIndex's build.
	if err := store.loadIdentIndexState(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			identLog.Error("close PebbleDB after identification index load failure: path=%s error=%s",
				logger.SanitizeLogValue(path), logger.SanitizeLogValue(closeErr.Error()))
		}
		return nil, fmt.Errorf("load identification index state: %w", err)
	}

	// Verified emails: stamp rows an older build wrote without an email
	// source and rewrite the idx:user:email cache. A failure is logged and
	// the store serves anyway: verification is computed live, an unstamped
	// row counts as unverified, and the next open retries.
	if _, err := store.ReconcileVerifiedEmails(); err != nil {
		authLog.Error("verified email: reconcile at open failed; rows from an older build count as unverified until it succeeds: %v", err)
	}

	// Stamp the storage format (after the counters: the stamp is a preference
	// row and takes a preference ID) and mirror it in the sidecar that
	// `make rollback` reads.
	stamp, err := store.ensureStorageFormatStamp(st.emptyAtOpen)
	if err != nil {
		if closeErr := db.Close(); closeErr != nil {
			storageFormatLog.Error("close PebbleDB after storage format stamp failure: path=%s error=%s",
				logger.SanitizeLogValue(path), logger.SanitizeLogValue(closeErr.Error()))
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	sidecarFS := fs
	if sidecarFS == nil {
		sidecarFS = vfs.Default
	}
	sidecarPath := StorageFormatSidecarPath(path)
	if err := writeStorageFormatSidecar(sidecarFS, path, stamp); err != nil {
		// Serve anyway: the app must not refuse to start over a sidecar. The
		// rollback guard refuses when the sidecar is missing or unreadable, so
		// this failure stays visible at the one place it matters.
		storageFormatLog.Error("storage format sidecar write failed: path=%s error=%s",
			logger.SanitizeLogValue(sidecarPath), logger.SanitizeLogValue(err.Error()))
	}
	storageFormatLog.Info("storage format: stamp=%d supported=%d sidecar=%s",
		stamp, SupportedStorageFormat, logger.SanitizeLogValue(sidecarPath))

	// Initialize in-memory query layer. Warmup runs in a goroutine so the
	// server is available immediately — reads transparently fall back to
	// Pebble until memdb is ready (couple of minutes for ~50K books).
	// store.memPtr is only published once warmup completes successfully.
	//
	// Started LAST — after every early-return error path that calls db.Close()
	// directly — so a construction failure can never close the DB while the
	// warmup goroutine is iterating it. The goroutine iterates store.db, so
	// Close() must cancel it and wait for it to exit before closing the DB (else
	// NewIter races the close and panics). warmupDone is always non-nil so Close
	// can wait unconditionally; it is closed immediately when there is no warmup
	// goroutine to wait for.
	store.initMetrics()
	store.warmupDone = make(chan struct{})
	if memStore, memErr := NewMemStore(); memErr != nil {
		pebbleOpenLog.Warn("memdb init failed, in-memory queries disabled: %s", logger.SanitizeLogValue(memErr.Error()))
		close(store.warmupDone)
	} else {
		warmupCtx, warmupCancel := context.WithCancel(context.Background())
		store.warmupCancel = warmupCancel

		// Arm the pending-write buffer SYNCHRONOUSLY, before the goroutine is
		// launched. NewPebbleStore returns to its caller as soon as `go` runs, and
		// the server starts writing immediately; if arming happened inside the
		// goroutine, a write that beat it there would be dropped exactly as it was
		// before this fix. From here until publish, memSync records instead of
		// dropping. See memdb_pending.go.
		store.beginMemWarmupBuffering()

		go func() {
			started := time.Now()
			defer close(store.warmupDone)
			// Registered after the close above so it runs first: the duration
			// is stored before warmupDone closes, on every exit path.
			defer func() { store.warmupDuration.Store(time.Since(started).Milliseconds()) }()
			// Disarm on EVERY exit path — warmup error, Close() cancelling the
			// context mid-scan, or success (where publishWarmMemStore has already
			// disarmed and this is a no-op). Otherwise a cancelled warmup leaves
			// the store buffering forever into a slice nobody drains.
			defer store.endMemWarmupBuffering()

			runWarmupStartHook()
			pebbleOpenLog.Info("memdb warmup starting (async)")
			if warmErr := memStore.WarmFromPebble(warmupCtx, store); warmErr != nil {
				pebbleOpenLog.Warn("memdb warmup failed after %d ms, will stay on Pebble for reads: %s",
					time.Since(started).Milliseconds(), logger.SanitizeLogValue(warmErr.Error()))
				return
			}
			// Replays the buffered write-throughs into memStore and publishes it in
			// one critical section, so no write can land between the two.
			if !store.publishWarmMemStore(memStore) {
				return
			}
			pebbleOpenLog.Info("memdb warmup published after %d ms", time.Since(started).Milliseconds())
		}()
	}

	return store, nil
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
	// it to fully exit first.
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

func (p *PebbleStore) nextID(counter string) (int, error) {
	p.counterMu.Lock()
	defer p.counterMu.Unlock()

	key := []byte(fmt.Sprintf("counter:%s", counter))

	value, closer, err := p.db.Get(key)
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, err
	}

	nextID := id + 1
	if err := p.db.Set(key, []byte(strconv.Itoa(nextID)), pebble.Sync); err != nil {
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
