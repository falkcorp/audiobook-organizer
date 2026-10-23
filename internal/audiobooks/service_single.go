// file: internal/audiobooks/service_single.go
// version: 1.26.1
// guid: d6a0e5f4-a7b8-9c01-bd2e-3f4a5b6c7d8e
// last-edited: 2026-10-23

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// GetAudiobook retrieves a single audiobook by ID with full metadata provenance
func (svc *AudiobookService) GetAudiobook(ctx context.Context, id string) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Read the store every time; there is no per-book cache. Most book writes
	// (fixers, the scanner, metadata applies) never reach this service, so the
	// 24h cache removed on 2026-10-06 served stale rows. Measured on prod that
	// day, the whole uncached detail read is ~31 ms median / 48 ms p90 against
	// ~7 ms cached. The store read is a memdb lookup; most of that time is the
	// per-request work below (the file tag read, metadata state, author and
	// series names, provenance), so don't re-add a cache to speed the store.
	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// Load metadata state and extract file metadata
	state, err := svc.loadMetadataState(book.ID)
	if err != nil {
		slog.Info("[ERROR] GetAudiobook failed to load metadata state for", "book", book.ID, "err", err)
		// Don't fail the entire request, just use empty state
		state = map[string]metadataFieldState{}
	}

	authorName, seriesName := resolveAuthorAndSeriesNames(svc.store, book)

	meta := svc.extractBookFileMetadata(book, authorName)

	// Backfill duration from the file when the stored row has none. Only the
	// derived field is written, onto a row the store re-reads (fillMediaInfo),
	// and the response is built from that row -- never UpdateBook(book): book
	// was read before ffprobe and the tag read, and a whole-struct save of it
	// reverted any apply or edit that landed in between.
	if book.FilePath != "" && book.Duration == nil && svc.fileProbeIsWholeBook(book.ID) {
		if mi, miErr := extractMediaInfo(book.FilePath); miErr == nil && mi.Duration > 0 {
			book.Duration = &mi.Duration
			if fresh := svc.fillMediaInfo(book.ID, database.BookMediaInfoPatch{Duration: &mi.Duration}); fresh != nil {
				book = fresh
			}
		}
	}

	// Build metadata provenance
	book.MetadataProvenance = metafetch.BuildMetadataProvenance(book, state, meta, authorName, seriesName, nil)
	nowUTC := time.Now().UTC()
	book.MetadataProvenanceAt = &nowUTC

	return book, nil
}

// GetAudiobookTags retrieves metadata tags and media info for an audiobook
func (svc *AudiobookService) GetAudiobookTags(ctx context.Context, id string, compareID string, snapshotTS string) (map[string]any, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	state, err := svc.loadMetadataState(book.ID)
	if err != nil {
		slog.Info("[ERROR] GetAudiobookTags failed to load metadata state for", "book", book.ID, "err", err)
		state = map[string]metadataFieldState{}
	}

	authorName, seriesName := resolveAuthorAndSeriesNames(svc.store, book)

	response := map[string]any{
		"media_info": map[string]any{
			"codec":       stringVal(book.Codec),
			"bitrate":     intVal(book.Bitrate),
			"sample_rate": intVal(book.SampleRate),
			"channels":    intVal(book.Channels),
			"bit_depth":   intVal(book.BitDepth),
			"quality":     stringVal(book.Quality),
			"duration":    intVal(book.Duration),
		},
		"tags": map[string]database.MetadataProvenanceEntry{},
	}

	meta := svc.extractBookFileMetadata(book, authorName)

	// Backfill empty media_info from the file when DB fields are missing. As
	// in GetAudiobook, only the derived still-empty fields are written
	// (fillMediaInfo), never the whole struct read before ffprobe.
	if book.FilePath != "" && (book.Codec == nil || book.Bitrate == nil || book.SampleRate == nil) {
		if mi, err := extractMediaInfo(book.FilePath); err == nil {
			var patch database.BookMediaInfoPatch
			if book.Codec == nil && mi.Codec != "" {
				book.Codec = &mi.Codec
				patch.Codec = &mi.Codec
			}
			if book.Bitrate == nil && mi.Bitrate > 0 {
				book.Bitrate = &mi.Bitrate
				patch.Bitrate = &mi.Bitrate
			}
			if book.SampleRate == nil && mi.SampleRate > 0 {
				book.SampleRate = &mi.SampleRate
				patch.SampleRate = &mi.SampleRate
			}
			if book.Channels == nil && mi.Channels > 0 {
				book.Channels = &mi.Channels
				patch.Channels = &mi.Channels
			}
			if book.Duration == nil && mi.Duration > 0 && svc.fileProbeIsWholeBook(book.ID) {
				book.Duration = &mi.Duration
				patch.Duration = &mi.Duration
			}
			if !patch.IsEmpty() {
				if fresh := svc.fillMediaInfo(book.ID, patch); fresh != nil {
					book = fresh
				}
				response["media_info"] = map[string]any{
					"codec":       stringVal(book.Codec),
					"bitrate":     intVal(book.Bitrate),
					"sample_rate": intVal(book.SampleRate),
					"channels":    intVal(book.Channels),
					"bit_depth":   intVal(book.BitDepth),
					"quality":     stringVal(book.Quality),
					"duration":    intVal(book.Duration),
				}
			}
		}
	}

	// Load comparison metadata if compare_id is provided
	var comparisonValues map[string]any
	if snapshotTS != "" {
		ts, err := time.Parse(time.RFC3339Nano, snapshotTS)
		if err != nil {
			return nil, fmt.Errorf("invalid snapshot timestamp: %w", err)
		}
		snapshotBook, verErr := svc.store.GetBookAtVersion(id, ts)
		if verErr == nil && snapshotBook != nil {
			snapshotAuthorName, snapshotSeriesName := resolveAuthorAndSeriesNames(svc.store, snapshotBook)
			comparisonValues = buildComparisonValuesFromBook(snapshotBook, snapshotAuthorName, snapshotSeriesName)
		} else {
			// Fallback: reconstruct "before" state from activity log old_values
			slog.Debug("GetAudiobookTags GetBookAtVersion failed (), falling back to activity log for snapshot at", "verErr", verErr, "snapshotTS", snapshotTS)
			if svc.activityService != nil {
				comparisonValues = buildComparisonValuesFromActivityLog(ctx, svc.activityService, id, ts)
			}
		}
	} else if compareID != "" {
		compBook, err := svc.store.GetBookByID(compareID)
		if err != nil {
			slog.Warn("GetAudiobookTags failed to load comparison book", "compareID", compareID, "err", err)
		} else if compBook != nil && compBook.FilePath != "" {
			if cm, err := metadata.ExtractMetadata(compBook.FilePath, nil); err == nil {
				comparisonValues = buildComparisonValuesFromMetadata(&cm)
			} else {
				slog.Warn("GetAudiobookTags failed to extract comparison metadata for", "compBook", compBook.FilePath, "err", err)
			}
		}
	}

	tags := metafetch.BuildMetadataProvenance(book, state, meta, authorName, seriesName, comparisonValues)
	response["tags"] = tags

	return response, nil
}

// singleLog is the logger for this file's read-path backfill writes.
var singleLog = logger.New("audiobooks")

// extractMediaInfo probes a book's audio file. A variable so tests can stand
// in for ffprobe and land a concurrent write while it "runs".
var extractMediaInfo = mediainfo.Extract

// fillMediaInfo writes a read path's derived media fields through
// FillBookMediaInfo, which re-reads the row inside the store and fills only
// fields that are still empty there, writing nothing if none is. It returns
// the stored row as it stands afterwards -- a concurrent apply's changes
// included -- or nil when the write failed or the book is gone, in which case
// the caller keeps the row it has.
func (svc *AudiobookService) fillMediaInfo(id string, patch database.BookMediaInfoPatch) *database.Book {
	if patch.IsEmpty() {
		return nil
	}
	fresh, err := svc.store.FillBookMediaInfo(id, patch)
	if err != nil {
		singleLog.Warn("failed to backfill media info for book %s: %v", id, err)
		return nil
	}
	return fresh
}

func (svc *AudiobookService) extractBookFileMetadata(book *database.Book, authorName string) metadata.Metadata {
	var meta metadata.Metadata
	if book == nil || book.FilePath == "" {
		return meta
	}

	m, err := metadata.ExtractMetadata(book.FilePath, nil)
	if err != nil {
		slog.Warn("audiobook_service failed to extract metadata for", "book", book.FilePath, "err", err)
		return meta
	}

	if m.OrganizerTagVersion == "" &&
		strings.TrimSpace(authorName) != "" &&
		strings.TrimSpace(m.Narrator) != "" &&
		strings.EqualFold(strings.TrimSpace(m.Artist), strings.TrimSpace(m.Narrator)) {
		m.Artist = authorName
		m.AuthorSource = "database author fallback"
	}

	return m
}

// GetSoftDeletedBooks retrieves soft-deleted audiobooks with optional age filter
func (svc *AudiobookService) GetSoftDeletedBooks(ctx context.Context, limit int, offset int, olderThanDays *int) ([]database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Normalize limit and offset
	if limit <= 0 || limit > 10000 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var cutoff *time.Time
	if olderThanDays != nil && *olderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*olderThanDays)
		cutoff = &ts
	}

	books, err := svc.store.ListSoftDeletedBooks(limit, offset, cutoff)
	if err != nil {
		return nil, err
	}

	if books == nil {
		books = []database.Book{}
	}

	return books, nil
}

// CountSoftDeletedBooks reports the EXACT size of the soft-deleted set. It
// exists because the only prior way to get a total was fetch-with-limit-10000
// and len(), which silently saturates above the guess and reads 10,000 rows to
// produce one integer — while a failed fetch reported total 0, which is the
// alarming direction to be wrong in (an empty trash and a broken count look
// identical). Callers must propagate the error, never default it to zero.
func (svc *AudiobookService) CountSoftDeletedBooks(ctx context.Context, olderThanDays *int) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	var cutoff *time.Time
	if olderThanDays != nil && *olderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*olderThanDays)
		cutoff = &ts
	}
	if cs := database.AsSoftDeletedCountStore(svc.store); cs != nil {
		return cs.CountSoftDeletedBooks(cutoff)
	}
	// Fallback for stores without the counting capability (mocks, mostly):
	// page the listing to exhaustion rather than guessing a single big limit —
	// a bounded guess is exactly the saturation bug this method replaces.
	total := 0
	const page = 1000
	for offset := 0; ; offset += page {
		books, err := svc.store.ListSoftDeletedBooks(page, offset, cutoff)
		if err != nil {
			return 0, err
		}
		total += len(books)
		if len(books) < page {
			return total, nil
		}
	}
}

// PurgeSoftDeletedBooks permanently deletes soft-deleted audiobooks.
//
// A book a user still has listening state on is purged only once that state
// is safe: when a member of its version group that Audiobookshelf lists
// exists, the state is carried there first, all or nothing
// (merge.CarryStateThenHardDelete with a precheck that re-checks the listing under
// the merge lock), and
// the book is purged in the same hold of the merge lock; a carry that does not
// complete keeps the book. With no listed sibling the book stays in the trash,
// flagged "has progress" in the trash listing, until the owner restores it or
// discards its progress (DiscardProgressAndPurge).
//
// The books are processed by a bounded worker pool, one version group per
// worker at a time (purgePartitions), so two workers never carry state into
// or delete from the same group concurrently. The carries, and each book's
// state check plus row delete, still run one at a time: each holds the
// global merge lock, which is what keeps a merge revert from putting state
// back on a book between its check and its delete. The file-row check and
// the on-disk file removal run outside it, in parallel.
func (svc *AudiobookService) PurgeSoftDeletedBooks(ctx context.Context, deleteFiles bool, olderThanDays *int) (*PurgeResult, error) {
	return svc.RunPurge(ctx, PurgeOptions{DeleteFiles: deleteFiles, OlderThanDays: olderThanDays})
}

// purgeOne runs the purge of one soft-deleted book and reports what
// happened. It is safe to run for books of different version groups at once.
func (svc *AudiobookService) purgeOne(book *database.Book, deleteFiles bool, env purgeEnv) purgeOutcome {
	// Step 0: refuse a book that still owns book_file rows — BEFORE any
	// side effect (external-ID tombstones, iTunes removes, the tombstone
	// snapshot), because a book we will not delete must keep all of those.
	//
	// store.DeleteBook never deletes book_file rows, so purging such a
	// book used to leave every one of them naming a book with no row. The
	// commonest case is a dedup-merge loser: merge.MergeBooks soft-deletes
	// it and deliberately leaves its files on it (they are its own version
	// of the audio, not the survivor's), so every purged loser orphaned
	// its rows. Repointing them onto the survivor was considered and
	// rejected: that would hand the survivor a second copy of the book as
	// extra tracks — the merge contract says the loser's files stay the
	// loser's. Deleting the rows is data loss. So the book stays
	// soft-deleted (hidden, restorable) and is reported.
	//
	// DeleteBook enforces the same rule (database.ErrBookOwnsFiles); this
	// pre-check exists so the refusal happens before the side effects
	// above, not after them. Fail closed: an unreadable file list is not
	// proof there are no rows.
	if o, blocked := svc.purgeGate(book, env); blocked {
		return o
	}
	// Step 0b: a book a user still has listening state on, checked at the
	// same point (before any side effect). A merge moves a loser's progress
	// to the book holding the group's flag; progress still here is progress
	// that never reached a live book (the move failed with no repair record,
	// a pending repair still holds it, or the user trashed a book they were
	// listening to), and a plain hard delete would drop it. Fail closed: an
	// unreadable answer is not proof there is none.
	//
	// The check and the delete run in ONE hold of the merge lock
	// (purgeIfStateless): a merge revert (UndoSiblingMove putting a loser's
	// state back on it, UndoCombine restoring progress) writes listening
	// state onto a trashed book under that lock, so a check made outside it
	// could read "none", lose the race to the revert, and delete the book the
	// revert had just put a user's progress on.
	r := svc.purgeIfStateless(book, env)
	switch {
	case r.readErr != nil:
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: not purged: cannot re-read it under the merge lock: %v", book.ID, r.readErr)}}
	case r.notInTrash:
		return purgeOutcome{kind: purgeNotInTrash}
	case r.keptUndo:
		return purgeOutcome{kind: purgeKeptUndo}
	case r.undoErr != nil:
		return purgeOutcome{kind: purgeFailed, abort: purgeAbortOf(r.undoErr), errs: []string{fmt.Sprintf("%s: not purged: cannot tell under the merge lock whether an undo still needs it: %v", book.ID, r.undoErr)}}
	case r.stateErr != nil:
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: not purged: %s: %v", book.ID, stateCheckFailureReason(r.stateErr), r.stateErr)}}
	case r.hasState:
		return svc.purgeCarryingState(book, deleteFiles, env)
	}
	book = r.fresh
	if err := r.deleteErr; err != nil {
		if errors.Is(err, database.ErrBookOwnsFiles) {
			// A file row landed between the pre-check and the delete.
			return purgeOutcome{kind: purgeOwnsFiles}
		}
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: %v", book.ID, err)}}
	}
	n, errs := svc.purgeFinish(book, deleteFiles)
	return purgeOutcome{kind: purgePurged, filesDeleted: n, errs: errs}
}

// statelessPurge is what purgeIfStateless found and did.
type statelessPurge struct {
	// fresh is the book as re-read under the merge lock (deleted when
	// hasState, notInTrash and every error are unset).
	fresh *database.Book
	// notInTrash: restored, or already gone, by the time the lock was held.
	notInTrash bool
	// keptUndo: the row, as re-read under the lock, is needed by an undo that
	// can still run (undoHold: a combine's shell, an operation's journaled
	// soft-delete, an auto-merge's loser).
	keptUndo bool
	// undoErr: the undo records could not be read under the lock, so whether
	// the book is held is unknown. The book is kept (fail closed).
	undoErr                      error
	hasState                     bool
	readErr, stateErr, deleteErr error
}

// purgeIfStateless re-reads book, checks it for listening state and, when it
// is still in the trash and has none, deletes its row, all under the merge
// lock (see purgeOne step 0b). The re-read is what makes the lock count: book
// came from the listing, read before the lock, and a restore or a merge
// revert (both take the lock) may have brought it back since. A book that is
// no longer in the trash is left alone, and the delete and its tombstone use
// the row as it is now. The lock is released before it returns, so
// purgeCarryingState, which takes it itself, can follow (unless env.lockHeld:
// the caller holds it across both, and neither takes it). Lock order: the
// merge lock is the outermost lock; nothing here takes it again, the store
// calls underneath take only per-book locks, and no caller of the purge
// (the purge handler, DELETE /audiobooks/:id's trash path, the auto-purge
// op) holds it.
//
// The hold is not counted (merge.LockMergeRMWBookRowsOnly): everything it
// writes is the book row's delete, which the book change log records, plus
// tombstones nothing snapshots, and it never touches a book_file row (a book
// that owns one is refused, here and by DeleteBook). A counted hold per
// trashed book made the chapter-fragment fixer list the whole library again
// before nearly every row of an apply running beside the purge (see
// merge.MergeLockAcquisitions). Anything added under this hold that writes a
// file row, or anything else such a snapshot reads unlogged, must switch it
// back to merge.LockMergeRMW.
func (svc *AudiobookService) purgeIfStateless(book *database.Book, env purgeEnv) (r statelessPurge) {
	if !env.lockHeld {
		merge.LockMergeRMWBookRowsOnly()
		defer merge.UnlockMergeRMW()
	}
	cur, err := svc.store.GetBookByID(book.ID)
	switch {
	case err != nil:
		r.readErr = err
		return r
	case cur == nil || !cur.IsSoftDeleted():
		r.notInTrash = true
		return r
	}
	r.fresh = cur
	// The undo hold is decided on the row as it is NOW, not on the listing's
	// copy: a shell restored or re-deleted since carries another stamp and is
	// not held (undoHold.Holds). env.undo also re-reads the combine and
	// auto-merge journals when one was written since the pass began, and
	// reads the operation ledger for this book: every writer of those holds
	// this lock across the write (or journals before it deletes), so the
	// answer here is exact. A failed read keeps the book.
	_, held, uerr := env.undo.Holds(cur)
	switch {
	case uerr != nil:
		r.undoErr = uerr
		return r
	case held:
		r.keptUndo = true
		return r
	}
	r.hasState, r.stateErr = env.probe.Has(cur.ID)
	if r.stateErr != nil || r.hasState {
		return r
	}
	r.deleteErr = svc.purgeDeleteRow(cur)
	return r
}

// stateCheckFailureReason names why the listening-state check of a book
// failed, for the purge's report. The two permanent sync-graph failures get
// their own reasons: they refuse the book on every run until repaired, and
// the generic "cannot read" reads like a transient error that a retry clears.
func stateCheckFailureReason(err error) string {
	switch {
	case errors.Is(err, merge.ErrBookmarkCheckRedirectBroken):
		return "its sync id's redirect chain is broken, so its bookmarks cannot be checked (refused on every run until the redirect is repaired)"
	case errors.Is(err, merge.ErrBookmarkCheckAliasLimit):
		return "its sync id has more merged aliases than the alias cap, so its bookmarks cannot be checked (refused on every run until the alias graph is repaired)"
	default:
		return "cannot read users' listening state on it"
	}
}

// purgeCarryingState purges a book that holds users' listening state: the
// state goes to a version-group sibling Audiobookshelf lists first, then the row is deleted
// in the same hold of the merge lock. No live sibling keeps the book
// (purgeKeptHasProgress); a carry that does not complete keeps it too
// (purgeCarryFailed) with every user's state put back on it.
func (svc *AudiobookService) purgeCarryingState(book *database.Book, deleteFiles bool, env purgeEnv) purgeOutcome {
	sibling, err := svc.liveSiblingFor(book)
	if err != nil {
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: not purged, listening state kept on it: cannot look for a live version to carry it to: %v", book.ID, err)}}
	}
	if sibling == "" {
		return purgeOutcome{kind: purgeKeptHasProgress}
	}
	merger, ok := database.AsCapability[merge.UserProgressMerger](svc.store)
	if !ok {
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: not purged, listening state kept on it: the store cannot carry listening state", book.ID)}}
	}
	// Re-check under the merge lock, before anything moves, that ABS still
	// lists the sibling: its primary flag or state can change between the
	// choice above and the carry. A failed re-read is a failed carry
	// (merge.ErrCarryTargetUnreadable), reported with its error -- never a
	// quiet kept-has-progress.
	//
	// The doomed book is re-read there too: book came from the listing, and a
	// restore or a merge revert (which take the same lock) may have brought it
	// back since. One no longer in the trash is left alone, and the delete and
	// its tombstone use the row as it is now.
	targetCheck := merge.CarryTargetPrecheck(svc.store, sibling)
	doomed := book
	precheck := func() error {
		cur, gerr := svc.store.GetBookByID(book.ID)
		if gerr != nil {
			return fmt.Errorf("re-read %s: %w", book.ID, gerr)
		}
		if cur == nil || !cur.IsSoftDeleted() {
			return fmt.Errorf("%w: %s was restored meanwhile", ErrNotInTrash, book.ID)
		}
		doomed = cur
		// The merge lock is held here, so this is the same exact undo-hold
		// decision purgeIfStateless made: a combine, an operation or an
		// auto-merge can have journaled this book in the gap since. Refused
		// before anything moves.
		_, held, uerr := env.undo.Holds(cur)
		switch {
		case uerr != nil:
			return fmt.Errorf("cannot tell whether an undo still needs %s: %w", book.ID, uerr)
		case held:
			return fmt.Errorf("%w: %s", errUndoHeldUnderLock, book.ID)
		}
		return targetCheck()
	}
	carryThenDelete := merge.CarryStateThenHardDelete
	if env.lockHeld {
		carryThenDelete = merge.CarryStateThenHardDeleteLocked
	}
	err = carryThenDelete(merger, sibling, book.ID, precheck, func() error {
		return svc.purgeDeleteRow(doomed)
	})
	switch {
	case errors.Is(err, ErrNotInTrash):
		return purgeOutcome{kind: purgeNotInTrash}
	case errors.Is(err, errUndoHeldUnderLock):
		return purgeOutcome{kind: purgeKeptUndo}
	case errors.Is(err, merge.ErrCarryTargetUnreadable):
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: not purged, listening state kept on it: %v", book.ID, err)}}
	case errors.Is(err, merge.ErrCarryPrecheckRefused) && errors.Is(err, merge.ErrBookHasListeningState):
		// Nothing moved; with no listed version to carry to, the book stays
		// in the trash flagged "has progress", as when there was none.
		return purgeOutcome{kind: purgeKeptHasProgress}
	case errors.Is(err, merge.ErrCarryPrecheckRefused):
		// Refused before anything moved (the doomed book could not be
		// re-read): the state is still on it.
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: not purged, listening state kept on it: %v", book.ID, err)}}
	case errors.Is(err, merge.ErrStateCarryIncomplete):
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: not purged, listening state kept on it: %v", book.ID, err)}}
	case errors.Is(err, database.ErrBookOwnsFiles):
		// The state is on the sibling now; the row stays because a file row
		// landed between the pre-check and the delete.
		return purgeOutcome{kind: purgeOwnsFiles, errs: []string{fmt.Sprintf("%s: listening state moved to %s, but the book was not purged: %v", book.ID, sibling, err)}}
	case err != nil:
		return purgeOutcome{kind: purgeFailed, abort: purgeAbortOf(err), errs: []string{fmt.Sprintf("%s: listening state moved to %s, but the book was not purged: %v", book.ID, sibling, err)}}
	}
	n, errs := svc.purgeFinish(doomed, deleteFiles)
	return purgeOutcome{kind: purgeCarried, filesDeleted: n, errs: errs}
}

// errUndoHeldUnderLock is what the carry's precheck returns when the book turns
// out, under the merge lock, to be needed by an undo that can still run (see
// undoHold). purgeCarryingState reports it as kept-undo.
var errUndoHeldUnderLock = errors.New("is needed by an undo that can still run")

// purgeDeleteRow is the purge's row delete: the tombstone snapshot, the row,
// and then the side effects that must happen only to a book that is really
// gone -- external-ID tombstones so a reimport is blocked. On a carry it runs
// under the merge lock as the carry's delete, so a carry that fails leaves
// none of these behind.
//
// Ordering: everything that can refuse (reading the external IDs) runs
// first, then the snapshot and DeleteBook; the side effects run only after
// DeleteBook succeeded. Until 2026-10-05 the tombstones ran BEFORE
// DeleteBook, so a DeleteBook that failed (for example a file row landing
// meanwhile: ErrBookOwnsFiles) left a live book whose external IDs were
// blocked. A side effect that fails after the delete is logged: the row is
// gone either way, and nothing here can put it back.
func (svc *AudiobookService) purgeDeleteRow(book *database.Book) error {
	eidStore := asExternalIDStore(svc.store)
	var extIDs []database.ExternalIDMapping
	if eidStore != nil {
		var err error
		if extIDs, err = eidStore.GetExternalIDsForBook(book.ID); err != nil {
			return fmt.Errorf("cannot read the external IDs to tombstone: %w", err)
		}
	}
	// Step 1: Create tombstone (snapshot of book for rollback)
	if err := svc.store.CreateBookTombstone(book); err != nil {
		return fmt.Errorf("failed to create tombstone: %w", err)
	}

	// Step 2: Delete from database (book record gone, tombstone preserved).
	// On failure the tombstone exists but the book still exists — the
	// sweeper will clean up the tombstone — and no side effect has run.
	if err := svc.store.DeleteBook(book.ID); err != nil {
		return fmt.Errorf("failed to delete DB record: %w", err)
	}

	// Step 3: the side effects, now that the row is gone.
	if eidStore != nil {
		for _, ext := range extIDs {
			if err := eidStore.TombstoneExternalID(ext.Source, ext.ExternalID); err != nil {
				singleLog.Warn("purge %s: external ID %s:%s not tombstoned, a reimport is not blocked: %v",
					logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(ext.Source), logger.SanitizeLogValue(ext.ExternalID), err)
			}
		}
	}
	return nil
}

// purgeFinish runs after the row is gone: the on-disk removal when
// requested, then the tombstone cleanup. It reports how many files or
// directories it removed and the errors to report.
func (svc *AudiobookService) purgeFinish(book *database.Book, deleteFiles bool) (filesDeleted int, errs []string) {
	// Step 3: Delete file if requested -- only from inside the library root
	// (RootDir), never from protected/import paths, and never a path
	// anything live still references.
	//
	// The book owned no book_file rows (step 0, enforced again by
	// DeleteBook), so the only path it names is its own FilePath. That
	// path is NOT necessarily its own: duplicate book rows over one file
	// are the commonest dedup merge, and the survivor keeps the very file
	// the loser's FilePath names. Removing it deleted the survivor's
	// audio. So a path any book_file row or any live book still names is
	// left alone. (A combine's absorbed shell has FilePath cleared for the
	// same reason — merge/service.go softDeleteAbsorbed.)
	//
	// The library-root check (purgePathInsideRoot) comes first. Until
	// 2026-10-06 the comment above promised "only from organizer root" but
	// nothing checked it: a soft-deleted book whose FilePath named a file
	// anywhere on disk the process could write (a sibling directory of the
	// root, a download folder no import path covers) had that file removed.
	if deleteFiles && book.FilePath != "" {
		root := config.Snapshot().RootDir
		if isProtectedPath(svc.store, book.FilePath) {
			slog.Debug("purge skipping file deletion for — protected path", "bookID", book.ID, "filePath", book.FilePath)
		} else if rootErr := purgePathInsideRoot(book.FilePath, root); rootErr != nil {
			errs = append(errs, fmt.Sprintf("%s: book purged, file kept: %v", book.ID, rootErr))
		} else if refErr := svc.purgePathStillReferenced(book.FilePath); refErr != nil {
			errs = append(errs, fmt.Sprintf("%s: book purged, file kept: %v", book.ID, refErr))
		} else {
			info, statErr := os.Stat(book.FilePath)
			if statErr == nil && info.IsDir() {
				// Directory-based book. It owns no file rows, so there is
				// nothing of its own inside to remove: remove the directory
				// only if it is already empty.
				if entries, rdErr := os.ReadDir(book.FilePath); rdErr == nil && len(entries) == 0 {
					if rmErr := os.Remove(book.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
						errs = append(errs, fmt.Sprintf("%s: failed to remove empty dir %s: %v", book.ID, book.FilePath, rmErr))
					} else if rmErr == nil {
						filesDeleted++
						removeEmptyParentsUpToRoot(book.FilePath, root)
					}
				}
			} else if statErr == nil {
				// Single-file book
				if err := os.Remove(book.FilePath); err != nil && !os.IsNotExist(err) {
					errs = append(errs, fmt.Sprintf("%s: failed to delete file (tombstone preserved): %v", book.ID, err))
					// DB record gone, file still exists, tombstone preserved for sweeper
				} else if err == nil {
					filesDeleted++
					removeEmptyParentsUpToRoot(book.FilePath, root)
				}
			}
			// If statErr is os.IsNotExist, file is already gone — that's fine
		}
	}

	// Step 4: Clean up tombstone (best-effort — sweeper handles failures)
	_ = svc.store.DeleteBookTombstone(book.ID)
	return filesDeleted, errs
}

// purgePathInsideRoot returns nil only when path is strictly inside the
// library root, so the purge may remove it from disk; otherwise an error
// naming why it may not. Fail closed:
//   - no root configured: nothing is removed (there is no "inside");
//   - a relative path: nothing to anchor it to, refused;
//   - the root itself: refused (the empty-directory branch would remove the
//     library root);
//   - lexically outside the root (a sibling such as "<root>2", or a ".."
//     escape): refused;
//   - inside lexically but resolving outside through a symlink
//     (pathvalidation.SecureJoinResolved, which also resolves the root, so a
//     root reached through a symlink such as macOS /var -> /private/var still
//     matches): refused, as is a root or path that cannot be resolved.
func purgePathInsideRoot(path, root string) error {
	if root == "" {
		return fmt.Errorf("no library root (root_dir) is configured, so nothing is deleted from disk; %s left in place", path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s is not an absolute path, so it cannot be checked against the library root %s", path, root)
	}
	cleanRoot := filepath.Clean(root)
	rel, err := filepath.Rel(cleanRoot, filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("%s cannot be placed under the library root %s: %w", path, root, err)
	}
	if rel == "." {
		return fmt.Errorf("%s is the library root itself", path)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside the library root %s", path, root)
	}
	if _, err := pathvalidation.SecureJoinResolved(cleanRoot, rel); err != nil {
		return fmt.Errorf("%s does not resolve inside the library root %s: %w", path, root, err)
	}
	return nil
}

// purgePathStillReferenced returns a non-nil error naming the reason when path
// must not be removed from disk by a purge: a book_file row still names it, or
// a live book's FilePath is it. Any lookup error is returned too — fail closed,
// an unanswerable question is not a free path.
func (svc *AudiobookService) purgePathStillReferenced(path string) error {
	rows, err := svc.store.BookFilesAtPath(path)
	if err != nil {
		return fmt.Errorf("cannot verify %s is unreferenced: book_file lookup: %w", path, err)
	}
	if len(rows) > 0 {
		return fmt.Errorf("%s is still book_file %s of book %s (%d row(s) at this path)", path, rows[0].ID, rows[0].BookID, len(rows))
	}
	ids, err := svc.store.LiveBookIDsAtPath(path)
	if err != nil {
		return fmt.Errorf("cannot verify %s is unreferenced: live-books-at-path lookup: %w", path, err)
	}
	if len(ids) > 0 {
		return fmt.Errorf("%s is still the path of live book(s) %v", path, ids)
	}
	return nil
}

// removeEmptyParentsUpToRoot removes the now-empty parent directories of path,
// walking up while they stay strictly inside root (the RootDir purgeFinish
// checked path against, read once so the check and the walk agree).
func removeEmptyParentsUpToRoot(path, root string) {
	if root == "" {
		return
	}
	parentDir := filepath.Dir(path)
	for parentDir != root &&
		pathutil.IsWithin(parentDir, root) &&
		parentDir != "/" {
		pe, peErr := os.ReadDir(parentDir)
		if peErr != nil || len(pe) > 0 {
			break
		}
		if os.Remove(parentDir) != nil {
			break
		}
		parentDir = filepath.Dir(parentDir)
	}
}

// RestoreAudiobook restores a soft-deleted audiobook
func (svc *AudiobookService) RestoreAudiobook(ctx context.Context, id string) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	// Restore runs under the merge lock, which a running combine holds for its
	// whole run: take it with a bound and answer ErrMergeBusy promptly instead
	// of hanging the request. The lock is KEPT and the restore runs under that
	// same hold (merge.RestoreFromTrashLocked), so a combine that starts after
	// the wait cannot make the request hang either.
	unlock, err := merge.TryLockMergeRMWWithin(merge.InteractiveMergeLockWait)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// The shared user restore (merge.RestoreFromTrashLocked): back to the state
	// the book had before the trash, not "imported" (until 2026-10-01 this
	// always wrote "imported", so restoring a book from the library folder
	// dropped it out of ABS); unlinked from its merge survivor, sync redirect
	// included; and a former primary yields to the member that has held the
	// flag since. A row that is not in the trash is returned unchanged.
	// It also hands off the group's primary (EnsureSinglePrimary) before it
	// decides the redirect, so no hand-off runs here.
	res, err := merge.RestoreFromTrashLocked(svc.store, id, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.Book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}
	if !res.Restored {
		return res.Book, nil
	}
	svc.InvalidateListCache()
	return res.Book, nil
}

// handOffPrimary runs versionprimary.EnsureSinglePrimary on b's version
// group after a delete or restore changed whether b is live, so the group
// ends with one live eligible primary, or is held (nothing written) for
// version-group-primary-repair. Best-effort: the delete or restore has
// committed; a failed hand-off is logged. A nil or ungrouped b is a no-op.
func (svc *AudiobookService) handOffPrimary(b *database.Book) {
	if b == nil || b.VersionGroupID == nil || *b.VersionGroupID == "" {
		return
	}
	gid := *b.VersionGroupID
	if _, err := versionprimary.EnsureSinglePrimary(context.Background(), svc.store, gid,
		versionprimary.Env{RootDir: config.AppConfig.RootDir}); err != nil {
		singleLog.Warn("version group %s: primary hand-off after deleting or restoring %s failed: %v",
			logger.SanitizeLogValue(gid), logger.SanitizeLogValue(b.ID), err)
	}
}

// CountAudiobooks returns the total count of audiobooks
func (svc *AudiobookService) CountAudiobooks(ctx context.Context) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	count, err := svc.store.CountPrimaryBooks()
	if err != nil {
		return 0, err
	}
	return count, nil
}

// fileProbeIsWholeBook reports whether probing Book.FilePath measures the
// whole book, i.e. the book has at most one file row. For a multi-file book
// FilePath is one chapter (or the folder), and backfilling its probed duration
// into Book.Duration stored ONE chapter's length as the book's runtime —
// which RecomputeBookAggregates then kept whenever no file row had a duration.
// Book.Duration for a multi-file book comes from its file rows only
// (RecomputeBookAggregates; the canonical runtime is database.ComputeBookRuntime).
// A read error answers false: no backfill beats a wrong one.
func (svc *AudiobookService) fileProbeIsWholeBook(bookID string) bool {
	files, err := svc.store.GetBookFiles(bookID)
	return err == nil && len(files) <= 1
}
