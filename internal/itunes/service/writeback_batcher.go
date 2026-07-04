// file: internal/itunes/service/writeback_batcher.go
// version: 5.4.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e90
// last-edited: 2026-07-03
//
// Combined write-back batcher: handles location updates, track additions,
// and track removals in a single ITL read-modify-write cycle.
//
// SAFETY RAILS (v5.0.0):
//   - MaxRemovesPerFlush hard-caps the number of removes that can land
//     in a single flush. A flush exceeding the cap is REFUSED entirely
//     (no partial writes) and logged loudly. Prevents any single bug
//     or runaway loop from wiping the user's iTunes library.
//   - DryRun mode (env ITUNES_WRITEBACK_DRYRUN=true) logs every flush
//     in detail but performs NO write to disk. Use it to diagnose
//     a suspicious enqueue pattern without risk.

package itunesservice

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// MaxRemovesPerFlush is the hard cap on iTunes track removals applied
// in a single batcher flush. Any flush whose pendingRemoves set
// exceeds this count is REFUSED — pendingRemoves are dropped, the
// flush logs an error, and the operator is expected to investigate.
//
// Rationale: bulk-remove paths historically wiped legitimate primary
// tracks when the DB was inconsistent (see the ~90 K-track shrink
// incident, May 2026). With targeted-only removes (one PID per
// explicit user delete), no legitimate flush should ever hit this
// cap — it's purely a circuit breaker.
const MaxRemovesPerFlush = 50

// dryRunEnabled returns true when ITUNES_WRITEBACK_DRYRUN is set to
// a truthy value. Checked at flush time so it can be toggled without
// a process restart by editing the systemd unit / env file and
// kicking the service.
func dryRunEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ITUNES_WRITEBACK_DRYRUN"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// WriteBackBatcherConfig is the tiny config surface the batcher needs.
// Deliberately not using config.AppConfig directly — this makes the
// batcher movable to a package that doesn't import internal/config
// (see iTunes service extraction, spec 2026-04-18). Populated at
// construction and mutable via UpdateConfig for hot-reload support.
type WriteBackBatcherConfig struct {
	AutoWriteBack       bool
	ITLWriteBackEnabled bool
	LibraryWritePath    string
}

// WriteBackStore is the narrow slice of database.Store the batcher needs.
// Defined here (not in internal/database) so the batcher stays package-
// portable: when the concrete type moves to internal/itunes/service/ in
// Phase 2 M1 it drags its deps along without re-importing internal/database
// via the service-level Store composite.
type WriteBackStore interface {
	database.BookStore       // GetBookByID, MarkITunesSynced
	database.AuthorReader    // GetAuthorByID
	database.BookFileStore   // GetBookFiles
	database.ExternalIDStore // MarkExternalIDRemoved
}

// WriteBackBatcher collects ITL operations and flushes them in a single batch
// after a debounce delay. Supports location updates, track additions, and
// track removals — all applied in one read-modify-write cycle.
//
// Adaptive debounce: the initial delay is 5s, but if new enqueues arrive
// within the window, the timer extends up to maxDelay (30s). This batches
// rapid-fire applies into a single ITL write instead of multiple.
type WriteBackBatcher struct {
	mu             sync.Mutex
	pendingBooks   map[string]bool      // book IDs for location updates
	pendingAdds    []itunes.ITLNewTrack // tracks to add
	pendingRemoves map[string]bool      // PIDs to remove (lowercase hex)
	timer          *time.Timer
	delay          time.Duration
	maxDelay       time.Duration
	firstEnqueue   time.Time // when the first enqueue in this batch happened
	stopCh         chan struct{}
	stopped        bool

	// Config fields — populated at construction, mutable via UpdateConfig.
	// Reads use cfgMu (separate from mu so config-reload doesn't block the
	// main pending-ops critical section).
	cfgMu               sync.RWMutex
	autoWriteBack       bool
	itlWriteBackEnabled bool
	libraryWritePath    string

	// store is the narrow database surface used by the flush goroutine
	// and the EnqueueRemove best-effort tombstone marker. May be nil
	// (pre-wiring path in older test fixtures); runtime call sites
	// nil-guard.
	store WriteBackStore

	// libraryNotInUse is an optional pre-flight check wired to the
	// iTunes sync-service running-state signal. When non-nil, flush()
	// calls it before writing; a non-nil error aborts the write and
	// the batch is re-queued on the next timer tick rather than lost.
	// Set via SetLibraryNotInUse. Safe to call after construction.
	libraryNotInUse func() error

	// flushFailures counts CONSECUTIVE failed flush attempts (parse or
	// write errors). Failed batches are re-enqueued for retry up to
	// maxFlushFailures; after that the batch is dropped with a loud
	// error so a persistent fault (contract rejection, corrupt library)
	// cannot hot-loop a 30MB re-encode forever. Reset on any success.
	flushFailures int
}

// maxFlushFailures caps consecutive failed-flush retries before the
// pending batch is dropped (see WriteBackBatcher.flushFailures).
const maxFlushFailures = 3

// NewWriteBackBatcher creates a batcher with the given debounce delay.
func NewWriteBackBatcher(delay time.Duration, cfg WriteBackBatcherConfig, store WriteBackStore) *WriteBackBatcher {
	return &WriteBackBatcher{
		pendingBooks:        make(map[string]bool),
		pendingRemoves:      make(map[string]bool),
		delay:               delay,
		maxDelay:            30 * time.Second,
		stopCh:              make(chan struct{}),
		autoWriteBack:       cfg.AutoWriteBack,
		itlWriteBackEnabled: cfg.ITLWriteBackEnabled,
		libraryWritePath:    cfg.LibraryWritePath,
		store:               store,
	}
}

// UpdateConfig is safe to call while the flush goroutine is running.
// Use from the server's config-reload path when/if one is wired up.
func (b *WriteBackBatcher) UpdateConfig(cfg WriteBackBatcherConfig) {
	b.cfgMu.Lock()
	b.autoWriteBack = cfg.AutoWriteBack
	b.itlWriteBackEnabled = cfg.ITLWriteBackEnabled
	b.libraryWritePath = cfg.LibraryWritePath
	b.cfgMu.Unlock()
}

// SetLibraryNotInUse wires the iTunes-running signal into the batcher.
// The check function is called immediately before each ITL write; if it
// returns a non-nil error the write is skipped this cycle and the pending
// state is preserved for the next timer tick. Safe to call after Start.
func (b *WriteBackBatcher) SetLibraryNotInUse(check func() error) {
	b.mu.Lock()
	b.libraryNotInUse = check
	b.mu.Unlock()
}

// autoWriteBackEnabled returns the current AutoWriteBack value under RLock.
func (b *WriteBackBatcher) autoWriteBackEnabled() bool {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.autoWriteBack
}

// flushEnabled returns the current ITLWriteBackEnabled + LibraryWritePath
// pair under RLock, for use at flush time.
func (b *WriteBackBatcher) flushEnabled() (bool, string) {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.itlWriteBackEnabled, b.libraryWritePath
}

// Enqueue adds a book ID to the pending location-update batch.
func (b *WriteBackBatcher) Enqueue(bookID string) {
	if !b.autoWriteBackEnabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.pendingBooks[bookID] = true
	b.resetTimer()
}

// EnqueueAdd queues a new track for insertion into the ITL.
func (b *WriteBackBatcher) EnqueueAdd(track itunes.ITLNewTrack) {
	if !b.autoWriteBackEnabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.pendingAdds = append(b.pendingAdds, track)
	b.resetTimer()
}

// EnqueueRemove queues a track PID for removal from the ITL.
// Also marks the PID as removed in the external_id_map.
func (b *WriteBackBatcher) EnqueueRemove(pid string) {
	if !b.autoWriteBackEnabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.pendingRemoves[strings.ToLower(pid)] = true
	b.resetTimer()

	// Mark as removed in DB (best-effort, outside lock). Failure leaves a
	// stale external-id mapping that later re-triggers dispatch for an
	// already-removed track — log it so the drift is visible instead of
	// discarding the error.
	go func() {
		if b.store == nil {
			return
		}
		if err := b.store.MarkExternalIDRemoved("itunes", pid); err != nil {
			slog.Warn("iTunes write-back MarkExternalIDRemoved failed; external-id map now stale", "pid", pid, "err", err)
		}
	}()
}

func (b *WriteBackBatcher) resetTimer() {
	if b.timer != nil {
		b.timer.Stop()
	}
	// Track when the first enqueue in this batch happened
	if b.firstEnqueue.IsZero() {
		b.firstEnqueue = time.Now()
	}
	// If we've been accumulating for longer than maxDelay, flush now
	elapsed := time.Since(b.firstEnqueue)
	if elapsed >= b.maxDelay {
		go b.flush()
		return
	}
	// Otherwise, extend the timer (but don't exceed maxDelay from first enqueue)
	remaining := b.maxDelay - elapsed
	delay := b.delay
	if delay > remaining {
		delay = remaining
	}
	b.timer = time.AfterFunc(delay, b.flush)
}

// HasPendingBook reports whether bookID is currently queued for a
// location update. Intended for test assertions in other packages that
// can no longer reach b.pendingBooks directly after the M1 step 2 move.
// Safe to call concurrently with Enqueue.
func (b *WriteBackBatcher) HasPendingBook(bookID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingBooks[bookID]
}

func (b *WriteBackBatcher) hasPending() bool {
	return len(b.pendingBooks) > 0 || len(b.pendingAdds) > 0 || len(b.pendingRemoves) > 0
}

// reEnqueue restores a snapshotted-and-cleared batch into the pending state
// and re-arms the timer, so a deferred or failed flush is retried on the next
// tick instead of lost. Mirrors the Enqueue* locking discipline.
func (b *WriteBackBatcher) reEnqueue(bookIDs []string, adds []itunes.ITLNewTrack, removes map[string]bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range bookIDs {
		b.pendingBooks[id] = true
	}
	b.pendingAdds = append(b.pendingAdds, adds...)
	for pid := range removes {
		b.pendingRemoves[pid] = true
	}
	if b.firstEnqueue.IsZero() && len(bookIDs)+len(adds)+len(removes) > 0 {
		b.firstEnqueue = time.Now()
	}
	b.resetTimer()
}

// flush writes all pending operations to the iTunes ITL binary in one pass.
func (b *WriteBackBatcher) flush() {
	b.mu.Lock()
	if !b.hasPending() {
		b.mu.Unlock()
		return
	}
	// Snapshot and clear pending state
	bookIDs := make([]string, 0, len(b.pendingBooks))
	for id := range b.pendingBooks {
		bookIDs = append(bookIDs, id)
	}
	adds := b.pendingAdds
	removes := make(map[string]bool, len(b.pendingRemoves))
	for pid := range b.pendingRemoves {
		removes[pid] = true
	}
	b.pendingBooks = make(map[string]bool)
	b.pendingAdds = nil
	b.pendingRemoves = make(map[string]bool)
	b.firstEnqueue = time.Time{} // reset for next batch
	b.mu.Unlock()

	// SAFETY RAIL: cap removes per flush. If exceeded, REFUSE the
	// entire flush (don't apply a partial subset — that's how we got
	// in trouble before). Pending removes are dropped, the operator
	// is expected to investigate. Adds and metadata updates are also
	// skipped to avoid an inconsistent state mid-incident.
	if len(removes) > MaxRemovesPerFlush {
		slog.Error("iTunes write-back REFUSING flush. Dropped removes, adds, location-updates without writing. This is a safety circuit-breaker; investigate what enqueued so many removes before re-enabling writes.",
			"removes_count", len(removes),
			"maxRemovesPerFlush", MaxRemovesPerFlush,
			"dropped_removes", len(removes),
			"dropped_adds", len(adds),
			"dropped_updates", len(bookIDs))
		return
	}

	dryRun := dryRunEnabled()

	store := b.store
	if store == nil {
		slog.Warn("iTunes write-back no database store")
		return
	}

	itlEnabled, writePath := b.flushEnabled()
	if !itlEnabled || writePath == "" {
		slog.Warn("iTunes write-back ITL write-back not configured")
		return
	}

	// Check whether iTunes is currently using the library file. When
	// the check function is wired, a non-nil error means "in use":
	// skip this cycle so we don't race a concurrent iTunes write.
	// The pending state was already snapshotted and cleared above, so
	// we re-enqueue the book IDs to preserve them for the next tick.
	b.mu.Lock()
	notInUse := b.libraryNotInUse
	b.mu.Unlock()
	if notInUse != nil {
		if err := notInUse(); err != nil {
			slog.Warn("iTunes write-back deferred: library in use", "reason", err)
			b.reEnqueue(bookIDs, adds, removes)
			return
		}
	}

	// Parse the current ITL library to enable diff-before-write.
	// We build a PID → track index once per flush, then compare each
	// desired update against the current values. This prevents writing
	// ~90K mhoh blocks every sync when only a handful of tracks have
	// actually changed (HIGH-3 / T008 fix).
	//
	// Parse failure DEFERS the flush (SPEC 3 §4): an unreadable library
	// means we cannot know what we would be writing over — a transiently
	// locked file heals on the next tick, and a persistently unreadable
	// one hits the maxFlushFailures cap instead of being blind-written.
	// The old behavior (WARN + write ALL metadata updates without a diff)
	// was the HIGH-3 blast-radius amplifier running exactly when the
	// library was least trustworthy.
	var tracksByPID map[string]*itunes.ITLTrack
	if lib, err := parseITLFn(writePath); err != nil {
		b.mu.Lock()
		b.flushFailures++
		failures := b.flushFailures
		b.mu.Unlock()
		if failures >= maxFlushFailures {
			slog.Error("iTunes write-back DROPPING batch: library unparseable after retries",
				"err", err, "path", writePath, "attempts", failures,
				"dropped_updates", len(bookIDs), "dropped_adds", len(adds), "dropped_removes", len(removes))
			return
		}
		slog.Warn("iTunes write-back deferred: library unparseable, will retry",
			"err", err, "path", writePath, "attempt", failures)
		b.reEnqueue(bookIDs, adds, removes)
		return
	} else {
		tracksByPID = make(map[string]*itunes.ITLTrack, len(lib.Tracks))
		for i := range lib.Tracks {
			t := &lib.Tracks[i]
			pid := strings.ToLower(hex.EncodeToString(t.PersistentID[:]))
			tracksByPID[pid] = t
		}
	}

	// Build location and metadata updates from book IDs
	var locationUpdates []itunes.ITLLocationUpdate
	var metadataUpdates []itunes.ITLMetadataUpdate
	var skippedMetadata, changedMetadata int
	for _, id := range bookIDs {
		book, err := store.GetBookByID(id)
		if err != nil || book == nil {
			continue
		}
		// Only primary versions are written to iTunes. A non-primary
		// version of a book that was somehow enqueued (e.g. via a
		// metadata edit on the alternate format) must NOT push to
		// iTunes — its PID, if any, was created in error and the
		// orphan-cleanup pass will remove it.
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}

		// Get author name for metadata
		authorName := ""
		if book.AuthorID != nil {
			if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
				authorName = author.Name
			}
		}
		// Narrator ends up in the Composer field for audiobooks —
		// Apple Music shows it there and most audiobook workflows
		// (scanners, converters, players) key on that mapping.
		narrator := ""
		if book.Narrator != nil {
			narrator = *book.Narrator
		}
		// Genre: prefer the book's own genre when set, fall back to
		// "Audiobook" so iTunes classifies correctly. Previously
		// every write hardcoded "Audiobook" even when the user had
		// set a more specific value.
		genre := "Audiobook"
		if book.Genre != nil && *book.Genre != "" {
			genre = *book.Genre
		}

		files, _ := store.GetBookFiles(id)
		if len(files) > 0 {
			for _, f := range files {
				if f.ITunesPersistentID == "" {
					continue
				}
				// Diff-before-write (T008 / HIGH-3): only enqueue an
				// ITLMetadataUpdate (and/or location update) when at
				// least one field differs from the current library value.
				// ITLTrack.Composer is not stored in the parsed struct
				// (0x0C not read), so it is always included in the update
				// when other fields change.
				pidKey := strings.ToLower(f.ITunesPersistentID)
				desiredLoc := ""
				if f.ITunesPath != "" {
					// SPEC §1b / TASK-006: normalize f.ITunesPath (which has
					// historically held BOTH native paths and file:// URLs)
					// into the canonical WinPath. Unmappable values are NOT
					// written — per-item WARN + metric, never a raw value into
					// 0x0D (the CRIT-2 corruption).
					if winPath, ok := normalizeITunesLocation(f.ITunesPersistentID, f.ITunesPath); ok {
						desiredLoc = winPath
					}
				}

				if cur, ok := tracksByPID[pidKey]; ok {
					// We have current library state: compare field by field.
					// Location update is suppressed when the desired path
					// matches the current 0x0D value (or is unmappable/empty).
					locationChanged := desiredLoc != "" && cur.Location != desiredLoc
					metadataChanged := cur.Name != f.Title ||
						cur.Album != book.Title ||
						cur.Artist != authorName ||
						cur.Genre != genre

					if !locationChanged && !metadataChanged {
						skippedMetadata++
						continue
					}
					if locationChanged {
						locationUpdates = append(locationUpdates, itunes.ITLLocationUpdate{
							PersistentID: f.ITunesPersistentID,
							NewLocation:  desiredLoc,
						})
					}
					if metadataChanged {
						changedMetadata++
						metadataUpdates = append(metadataUpdates, itunes.ITLMetadataUpdate{
							PersistentID: f.ITunesPersistentID,
							Name:         f.Title,
							Album:        book.Title,
							Artist:       authorName,
							Composer:     narrator,
							Genre:        genre,
						})
					}
				} else {
					// No current library state for this PID (track is new or
					// library parse failed) — emit both unconditionally.
					if desiredLoc != "" {
						locationUpdates = append(locationUpdates, itunes.ITLLocationUpdate{
							PersistentID: f.ITunesPersistentID,
							NewLocation:  desiredLoc,
						})
					}
					changedMetadata++
					metadataUpdates = append(metadataUpdates, itunes.ITLMetadataUpdate{
						PersistentID: f.ITunesPersistentID,
						Name:         f.Title,
						Album:        book.Title,
						Artist:       authorName,
						Composer:     narrator,
						Genre:        genre,
					})
				}
			}
		} else if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
			pidKey := strings.ToLower(*book.ITunesPersistentID)
			if cur, ok := tracksByPID[pidKey]; ok {
				if cur.Name == book.Title &&
					cur.Album == book.Title &&
					cur.Artist == authorName &&
					cur.Genre == genre {
					skippedMetadata++
					continue
				}
			}
			changedMetadata++
			metadataUpdates = append(metadataUpdates, itunes.ITLMetadataUpdate{
				PersistentID: *book.ITunesPersistentID,
				Name:         book.Title,
				Album:        book.Title,
				Artist:       authorName,
				Composer:     narrator,
				Genre:        genre,
			})
		}
	}

	ops := itunes.ITLOperationSet{
		Removes:         removes,
		Adds:            adds,
		LocationUpdates: locationUpdates,
		MetadataUpdates: metadataUpdates,
	}

	if ops.IsEmpty() {
		return
	}

	slog.Info("iTunes write-back flushing",
		"locationUpdates", len(locationUpdates),
		"metadataUpdates_changed", changedMetadata,
		"metadataUpdates_skipped", skippedMetadata,
		"adds", len(adds),
		"removes", len(removes))

	if dryRun {
		slog.Info("iTunes write-back DRY-RUN active — no file written",
			"locationUpdates", len(locationUpdates), "metadataUpdates", len(metadataUpdates), "adds", len(adds), "removes", len(removes), "path", writePath)
		for pid := range removes {
			slog.Info("iTunes write-back DRY-RUN would remove PID", "pid", pid)
		}
		return
	}

	itlPath := writePath // from b.flushEnabled() above
	if err := SafeWriteITL(itlPath, ops); err != nil {
		b.mu.Lock()
		b.flushFailures++
		failures := b.flushFailures
		b.mu.Unlock()
		if failures >= maxFlushFailures {
			// A persistent failure (contract rejection, corrupt library) —
			// dropping after N attempts prevents a 30MB re-encode hot loop,
			// and ERROR (not the old silent WARN) makes the loss visible.
			slog.Error("iTunes write-back DROPPING batch after repeated failures",
				"err", err, "attempts", failures,
				"dropped_updates", len(bookIDs), "dropped_adds", len(adds), "dropped_removes", len(removes))
			return
		}
		slog.Error("iTunes write-back failed; batch re-enqueued for retry",
			"err", err, "attempt", failures)
		b.reEnqueue(bookIDs, adds, removes)
		return
	}

	// Success: reset the consecutive-failure counter and pin this state as
	// the last-known-good anchor. .bak-lkg is exempt from rotation, so even
	// after keep-10 churns through timestamped backups a validated copy of a
	// successfully-written library survives (SPEC 3 §4 — before this wiring,
	// PinLastKnownGood had no production caller and .bak-lkg never existed).
	b.mu.Lock()
	b.flushFailures = 0
	b.mu.Unlock()
	if err := itlPinLKGFn(itlPath); err != nil {
		slog.Warn("iTunes write-back could not pin last-known-good", "err", err, "path", itlPath)
	}

	// Mark the books we wrote as iTunes-synced so downstream UI /
	// filters reflect current state. Only books with at least one
	// update (location or metadata) count — pendingBooks may include
	// IDs whose PIDs weren't found in the DB, those stay unmarked.
	if len(bookIDs) > 0 {
		if n, markErr := store.MarkITunesSynced(bookIDs); markErr != nil {
			slog.Warn("iTunes write-back MarkITunesSynced failed", "markErr", markErr)
		} else if n > 0 {
			slog.Info("iTunes write-back marked books as iTunes-synced", "n", n)
		}
	}
}

// Test hooks. Production code wires these to the real itunes
// package functions at package init. Tests override them so the
// safe-write cycle and diff-before-write logic can be unit-tested
// without needing a valid ITL fixture on disk — the fixture itself
// is fragile, format changes have broken it before, and mocking the
// external calls lets us test the logic in isolation.
var (
	itlValidateFn        = itunes.ValidateITL
	itlApplyOperationsFn = itunes.ApplyITLOperations
	parseITLFn           = itunes.ParseITL
	itlPinLKGFn          = itunes.PinLastKnownGood
	itlAuditFileFn       = auditITLFile
)

// auditITLFile re-reads a written ITL and runs the full ITLSafetyContract
// (AuditITL) on it — the service wrapper's equivalent of the hardened
// path's step-5 re-read validation.
func auditITLFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-reading %s: %w", path, err)
	}
	if v := itunes.AuditITL(data); !v.Pass {
		return fmt.Errorf("%s", v.Error())
	}
	return nil
}

// SafeWriteITL performs a backup → write-temp → validate-temp →
// rename → validate-final → cleanup cycle for ITL write-back. At
// every failure point the original ITL is either untouched or
// restored from the backup, so a corrupted write can never leave
// the user with an unreadable library file.
//
// Sequence:
//  1. Ensure the source ITL currently parses (pre-condition check).
//     If it doesn't, abort — we won't compound an existing problem.
//  2. Copy itlPath → itlPath+".bak-YYYYMMDD-HHMMSS" as the rollback
//     anchor. Prune older backups to keep the last `itlBackupRetention`.
//  3. Run ApplyITLOperations to produce itlPath+".tmp".
//  4. Validate the .tmp file. If invalid, remove .tmp and abort — the
//     original itlPath is still intact.
//  5. Rename .tmp over itlPath.
//  6. Validate the renamed itlPath. If invalid (the rename itself
//     produced corruption, or the write was partial), copy the
//     backup back over itlPath.
//  7. Log the result.
func SafeWriteITL(itlPath string, ops itunes.ITLOperationSet) error {
	// Step 1: sanity-check the source. If the ITL we're about to
	// write over is ALREADY corrupted, the write has nothing to
	// validate against and a rollback wouldn't help anyway.
	if err := itlValidateFn(itlPath); err != nil {
		return fmt.Errorf("source ITL validation failed (refusing to write to a broken file): %w", err)
	}

	// Step 2: backup-before-write.
	backupPath, backupErr := writeITLBackup(itlPath)
	if backupErr != nil {
		// A failing backup isn't fatal for the write — the user can
		// still recover from the next successful run — but we log
		// prominently so they know the safety net was missing.
		slog.Warn("iTunes write-back backup failed (); proceeding without a rollback anchor", "backupErr", backupErr)
		backupPath = ""
	} else {
		if pruneErr := pruneITLBackups(itlPath, itlBackupRetention); pruneErr != nil {
			slog.Warn("iTunes write-back backup prune failed", "pruneErr", pruneErr)
		}
	}

	// Step 3: write the updated ITL to a temp file.
	tmpPath := itlPath + ".tmp"
	result, err := itlApplyOperationsFn(itlPath, tmpPath, ops)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ApplyITLOperations: %w", err)
	}

	// Step 4: validate the temp BEFORE renaming. If the write
	// produced a file iTunes can't read, the original is still
	// intact at itlPath and we abort cleanly.
	if err := itlValidateFn(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("validation of temp ITL failed (original preserved): %w", err)
	}

	// Step 4b: run the FULL safety contract on the re-read temp bytes —
	// the same step-5 re-read audit the hardened itunes.SafeWriteITL
	// performs. ValidateITL only proves the header decodes and a track
	// exists; AuditITL catches encode/encrypt/deflate-path corruption
	// (the historic risk area) before the rename lands it.
	if err := itlAuditFileFn(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("contract audit of temp ITL failed (original preserved): %w", err)
	}

	// Step 5: rename .tmp over the original.
	if err := renameFile(tmpPath, itlPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename .tmp → itl: %w", err)
	}

	// Step 6: validate the final file. On paranoid-filesystem
	// failures or weird permission issues the rename can land but
	// the result is still corrupt. Catch that and roll back.
	if err := itlValidateFn(itlPath); err != nil {
		slog.Error("iTunes write-back post-rename validation failed ()", "err", err)
		if backupPath != "" {
			if rbErr := copyFileContents(backupPath, itlPath); rbErr != nil {
				return fmt.Errorf("post-rename validation failed AND backup restore failed: validation=%v restore=%v", err, rbErr)
			}
			slog.Info("iTunes write-back restored from backup after corrupted write", "backupPath", backupPath)
			return fmt.Errorf("post-rename validation failed (restored from backup): %w", err)
		}
		return fmt.Errorf("post-rename validation failed (no backup available): %w", err)
	}

	slog.Info("iTunes write-back operations applied and validated", "result", result.UpdatedCount)
	return nil
}

// itlBackupRetention is how many rotating .bak-YYYYMMDD-HHMMSS
// files to keep per ITL file. Balances "enough history to
// investigate a regression" vs "don't fill the disk". Five is
// typical for config-file backup schemes.
const itlBackupRetention = 5

// writeITLBackup copies itlPath to a timestamped sibling and
// returns the new path. Uses time.Now with seconds precision so
// rapid-fire backups in the same millisecond collide by design —
// the per-run batcher's debounce makes that effectively
// impossible, but documenting the assumption.
func writeITLBackup(itlPath string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.bak-%s", itlPath, stamp)
	if err := copyFileContents(itlPath, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

// copyFileContents duplicates src to dst by reading the whole file
// into memory and writing it out. Small enough for ITL files
// (typically < 100 MB) and avoids needing io.Copy's dance.
func copyFileContents(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// pruneITLBackups deletes rotating backups beyond the keep limit.
// Sorts siblings by name (lexicographic on the timestamp suffix,
// which is monotonic) and removes the oldest excess.
func pruneITLBackups(itlPath string, keep int) error {
	if keep <= 0 {
		return nil
	}
	dir := filepath.Dir(itlPath)
	base := filepath.Base(itlPath) + ".bak-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var backups []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		backups = append(backups, filepath.Join(dir, name))
	}
	if len(backups) <= keep {
		return nil
	}
	sort.Strings(backups) // oldest first (lex sort on timestamp)
	toRemove := backups[:len(backups)-keep]
	for _, p := range toRemove {
		if err := os.Remove(p); err != nil {
			slog.Warn("iTunes write-back prune", "p", p, "err", err)
		}
	}
	return nil
}

// renameFile is a helper for os.Rename.
var renameFile = os.Rename

// Start is a no-op — the batcher begins processing on the first
// Enqueue, not at construction. Matches the serviceregistry.Starter
// signature so the container can drive lifecycle directly without an
// adapter wrap.
func (b *WriteBackBatcher) Start(_ context.Context) error {
	return nil
}

// Stop flushes any pending writes and stops the batcher. Signature
// matches serviceregistry.Stopper so Container.Stop can drive shutdown
// directly. The context is unused — flushing has its own timeout
// behavior inside the worker.
func (b *WriteBackBatcher) Stop(_ context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.stopped = true
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	b.flush()
	return nil
}
