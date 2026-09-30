// file: internal/scanner/scan_book_lock.go
// version: 1.3.0
// guid: fe71f301-a85e-4dad-98df-b135676ab1a7
// last-edited: 2026-09-30

package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
)

// The scanner's half of the per-book lock (internal/scanlock). The worker in
// ProcessBooksParallel locks the book rows it is about to read and merge
// BEFORE the tag read, and holds them through the merge, the book_file rows,
// the chapters and the scan-cache stamp. An apply or write-back of the same
// book waits for it; an apply of any other book never does.
//
// Why the span starts before the tag read and not at the write: the merge
// overlays the tags the worker read onto the row as it stands at WRITE time.
// Locking only the write would still overlay pre-apply tag values onto a
// post-apply row -- the clobber this exists to stop.
//
// THE SCANNER NEVER WAITS FIRST. Round 0 only TRIES the lock; a book an apply
// is writing goes to the back of the chunk and the worker moves on. Each later
// round waits under ONE deadline shared by every book in it (scanLockWait from
// the round's start), not a fresh clock per book: the worker pool runs the
// round's books a few at a time, and per-book clocks started only when a book
// reached a worker would let a large deferred set wait scanLockWait times
// (books / workers) -- hours. A book still busy when its round's deadline has
// passed is recorded at once as a "busy" FileFailure (no further round), and
// the next scan picks it up.
// library.scan runs under the registry's progress watchdog, so an unbounded
// wait here would be a scan killed by an apply.

const (
	// scanLockMaxGroup caps how many version-group members the scanner locks
	// along with a row. Version groups are one book's versions (original,
	// library copy, a re-rip); a group past this size is bad data, and locking
	// all of it would make one scanned book contend with a whole cluster.
	scanLockMaxGroup = 32
	// scanLockRounds is the number of blocking rounds after round 0.
	scanLockRounds = 3
	// scanLockResolveTries bounds re-resolution when the lock set keeps
	// changing under the scanner (a row moved or was regrouped mid-acquire).
	scanLockResolveTries = 3
)

// scanLockWait bounds one deferred round (every book in it shares the
// deadline) and one AI-phase save. An apply holds its book across its whole
// file job (tag write + rename): 67s for a large multi-file book on
// 2026-09-30. A var so tests can shorten it.
var scanLockWait = 90 * time.Second

// scanLockBeatEvery is how often a scanner goroutine blocked on a book's lock
// reports progress while it waits. library.scan's only liveness signal to the
// registry watchdog is UpdateProgress, and a deferred round whose every worker
// is waiting on an apply reports nothing else for up to scanLockWait. A var so
// tests can shorten it.
var scanLockBeatEvery = 25 * time.Second

type scanBeatKey struct{}

// withScanBeat carries the run's progress beat to the lock waits:
// acquireScanBookLock runs in the worker and in the AI phase's save, neither
// of which has the run's logger in reach.
func withScanBeat(ctx context.Context, beat func(message string)) context.Context {
	return context.WithValue(ctx, scanBeatKey{}, beat)
}

func scanBeatFrom(ctx context.Context) func(string) {
	beat, _ := ctx.Value(scanBeatKey{}).(func(string))
	return beat
}

// lockSetIdleBeating is scanlock.Books.LockSetIdle with a progress beat every
// scanLockBeatEvery while it blocks. ctx supplies the beat (see
// withScanBeat); waitCtx bounds the wait.
func lockSetIdleBeating(ctx, waitCtx context.Context, ids []string, path string) (*scanlock.Hold, error) {
	beat := scanBeatFrom(ctx)
	if beat == nil {
		return scanlock.Books.LockSetIdle(waitCtx, ids)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(scanLockBeatEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				beat("waiting for " + filepath.Base(path) + ": an apply or write-back is writing it")
			}
		}
	})
	h, err := scanlock.Books.LockSetIdle(waitCtx, ids)
	close(stop)
	wg.Wait()
	return h, err
}

// Process-wide counters; ProcessBooksParallel reports each run's delta.
var (
	scanLockGroupCapped   atomic.Int64 // version groups too large to lock whole
	scanLockVanishedCount atomic.Int64 // books skipped: files gone by the time the lock was held
	scanLockRequeuedCount atomic.Int64 // books sent to the back of the chunk (busy, or widened)
	scanLockGaveUpCount   atomic.Int64 // books left for the next scan after the bounded waits
	scanLockKeptFields    atomic.Int64 // columns kept because another writer changed them mid-scan
	scanLockGroupErrs     atomic.Int64 // version-group lookups that failed; the book was treated as busy
	// books left for the next scan because their version group could not be
	// read (a store error, not an apply holding them)
	scanLockUnreadableCount atomic.Int64
)

// scanLockLog reports lock-set resolution failures, which have no run logger
// in reach (resolveScanLockSet runs inside the worker and the AI phase).
var scanLockLog = logger.New("scanner.scanlock")

// FileFailureStageBusy: the book was being written by an apply for longer than
// the scanner will wait. Nothing was read or written; the next scan picks it up.
const FileFailureStageBusy = "busy"

// errScanLockWiden is returned by saveBookToDatabase BEFORE any write when it
// discovers a row the worker does not hold (a content-hash duplicate, an
// organizer-ID relink target, a segment-vote owner, a row created
// concurrently). The worker releases everything and restarts the book with
// those rows pre-locked: adding a key while holding others could deadlock
// against an apply holding the new key (scanlock R1).
type errScanLockWiden struct{ ids []string }

func (e *errScanLockWiden) Error() string {
	return fmt.Sprintf("scan lock: book rows %v discovered mid-save; restarting the book with them locked", e.ids)
}

// requireHeld returns errScanLockWiden when ctx carries a scan hold that does
// not cover every id. With no hold on ctx (callers outside the scan worker:
// tests, the directory importer) it is a no-op.
func requireHeld(ctx context.Context, ids ...string) error {
	h := scanlock.HoldFrom(ctx)
	if h == nil {
		return nil
	}
	var missing []string
	for _, id := range ids {
		if id != "" && !h.Has(id) {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &errScanLockWiden{ids: missing}
}

// requirePathRowHeld runs before a content-hash branch writes a version link
// onto the partner row. The create-if-absent step after it re-reads the path
// and restarts the book (errScanLockWiden) when a row appeared there that the
// worker does not hold -- but by then the branch has already linked the
// partner, and the restart leaves the partner in a group of one. Checking the
// path row here, before the write, restarts first. A lookup error aborts the
// save before any write for the same reason. No-op outside the scan worker.
func requirePathRowHeld(ctx context.Context, path string) error {
	if scanlock.HoldFrom(ctx) == nil {
		return nil
	}
	st := getStore()
	if st == nil {
		return nil
	}
	row, err := st.GetBookByFilePath(path)
	if err != nil {
		return fmt.Errorf("book lookup failed: %w", err)
	}
	if row == nil {
		return nil
	}
	return requireHeld(ctx, row.ID)
}

func asWiden(err error) (*errScanLockWiden, bool) {
	var w *errScanLockWiden
	if errors.As(err, &w) {
		return w, true
	}
	return nil, false
}

// rowSnap is the value of every tag-derived column applyScannerFields may
// overlay, plus the row's FilePath, as the row stood when the scanner took its
// lock (before the tag read), or as the scanner itself last wrote it.
type rowSnap struct {
	Title, FilePath                                   string
	AuthorID, SeriesID, SeriesSequence                *int
	Narrator, Language, Publisher, ASIN               *string
	WorkID, OpenLibraryID, HardcoverID, GoogleBooksID *string
}

func snapOf(b *database.Book) rowSnap {
	return rowSnap{
		Title: b.Title, FilePath: b.FilePath, AuthorID: b.AuthorID, SeriesID: b.SeriesID, SeriesSequence: b.SeriesSequence,
		Narrator: b.Narrator, Language: b.Language, Publisher: b.Publisher, ASIN: b.ASIN,
		WorkID: b.WorkID, OpenLibraryID: b.OpenLibraryID, HardcoverID: b.HardcoverID, GoogleBooksID: b.GoogleBooksID,
	}
}

func eqInt(a, b *int) bool    { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func eqStr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// rememberRow records (or refreshes) the snapshot for a row. The scanner calls
// it when it locks the row and again after every write it makes to the row,
// so "changed since the scanner last saw it" never means "the scanner changed
// it" -- the AI-phase re-save must be able to overlay the main pass's values.
func (b *Book) rememberRow(row *database.Book) {
	if row == nil || row.ID == "" {
		return
	}
	if b.rowSnaps == nil {
		b.rowSnaps = map[string]rowSnap{}
	}
	b.rowSnaps[row.ID] = snapOf(row)
}

// mergeScannedKeepingForeignEdits is applyScannerFields plus the safety net:
// every tag-derived column whose current value differs from the scanner's
// snapshot was changed by ANOTHER writer after the scanner read the book, and
// keeps that writer's value. The lock stops applies from landing in that
// window; this covers writers that do not take it (maintenance ops, AI parse,
// the write-back batcher's tag write racing a non-scan path). It returns how
// many columns it kept. With no snapshot for the row it is applyScannerFields.
func mergeScannedKeepingForeignEdits(cur, scanned *database.Book, locked map[string]bool, snap *rowSnap) int {
	if snap == nil {
		applyScannerFields(cur, scanned, locked)
		return 0
	}
	eff := make(map[string]bool, len(locked)+8)
	for k, v := range locked {
		eff[k] = v
	}
	kept := 0
	mark := func(changed bool, key string) {
		if changed && !eff[key] {
			eff[key] = true
			kept++
		}
	}
	mark(cur.Title != snap.Title, database.FieldKeyTitle)
	mark(!eqInt(cur.AuthorID, snap.AuthorID), database.FieldKeyAuthorName)
	mark(!eqInt(cur.SeriesID, snap.SeriesID), database.FieldKeySeriesName)
	mark(!eqInt(cur.SeriesSequence, snap.SeriesSequence), database.FieldKeySeriesPosition)
	mark(!eqStr(cur.Narrator, snap.Narrator), database.FieldKeyNarrator)
	mark(!eqStr(cur.Language, snap.Language), database.FieldKeyLanguage)
	mark(!eqStr(cur.Publisher, snap.Publisher), database.FieldKeyPublisher)
	mark(!eqStr(cur.ASIN, snap.ASIN), database.FieldKeyASIN)

	// The four columns with no lock key: remember and restore.
	work, ol, hc, gb := cur.WorkID, cur.OpenLibraryID, cur.HardcoverID, cur.GoogleBooksID
	// The row moved under the scan (an apply's rename, an organize repoint):
	// keep where it points now. The scanned path is where the file WAS when
	// the walk listed it, and the file-derived columns describe that file, so
	// the whole file identity (path, format, hash, size) stays as the mover
	// left it; the tag-derived columns still merge as above.
	pathMoved := cur.FilePath != snap.FilePath
	path, format, hash, size := cur.FilePath, cur.Format, cur.FileHash, cur.FileSize
	workChanged := !eqStr(work, snap.WorkID)
	olChanged := !eqStr(ol, snap.OpenLibraryID)
	hcChanged := !eqStr(hc, snap.HardcoverID)
	gbChanged := !eqStr(gb, snap.GoogleBooksID)

	applyScannerFields(cur, scanned, eff)
	if pathMoved {
		cur.FilePath, cur.Format, cur.FileHash, cur.FileSize = path, format, hash, size
		kept++
	}

	for _, c := range []struct {
		changed bool
		dst     **string
		v       *string
	}{{workChanged, &cur.WorkID, work}, {olChanged, &cur.OpenLibraryID, ol}, {hcChanged, &cur.HardcoverID, hc}, {gbChanged, &cur.GoogleBooksID, gb}} {
		if c.changed {
			*c.dst = c.v
			kept++
		}
	}
	return kept
}

// resolveScanLockSet returns the book rows the worker must hold for b: the row
// at its path, for a multi-file book also the row at its directory and the
// owner of its first segment, plus every member of each such row's version
// group (capped), plus any rows a previous attempt discovered mid-save.
//
// The version group is what lets an apply lock only its own book: the scanner
// reaching a protected book's library copy S locks {S, X} and so waits for an
// apply of the original X, and the scanner reaching X locks {X, S}.
//
// A failed version-group lookup is an error, and the caller treats the book as
// busy (FAIL CLOSED). Skipping the group would lock {S} without {X}, and the
// scanner could then merge S while an apply of X -- which locks only X -- is
// writing it: exactly the race the version-group widening closes. Busy costs
// one requeue, or a busy FileFailure and the next scan.
func resolveScanLockSet(b *Book) ([]string, map[string]*database.Book, error) {
	st := getStore()
	rows := map[string]*database.Book{}
	if st == nil {
		return nil, rows, nil
	}
	add := func(row *database.Book) {
		if row != nil && row.ID != "" {
			rows[row.ID] = row
		}
	}
	if r, err := st.GetBookByFilePath(b.FilePath); err == nil {
		add(r)
	}
	if len(b.SegmentFiles) > 1 {
		if r, err := st.GetBookByFilePath(filepath.Dir(b.FilePath)); err == nil {
			add(r)
		}
		if bf, err := st.GetBookFileByPath(b.SegmentFiles[0]); err == nil && bf != nil && bf.BookID != "" {
			if r, err := st.GetBookByID(bf.BookID); err == nil {
				add(r)
			}
		}
	}
	for _, id := range b.scanLockExtra {
		if _, ok := rows[id]; ok {
			continue
		}
		if r, err := st.GetBookByID(id); err == nil && r != nil {
			add(r)
		} else {
			// A row that vanished still gets its key: an apply may be
			// mid-rename on it.
			rows[id] = nil
		}
	}
	seen := map[string]bool{}
	for _, r := range rowValues(rows) {
		if r == nil || r.VersionGroupID == nil || *r.VersionGroupID == "" || seen[*r.VersionGroupID] {
			continue
		}
		seen[*r.VersionGroupID] = true
		members, err := st.GetBooksByVersionGroup(*r.VersionGroupID)
		if err != nil {
			scanLockGroupErrs.Add(1)
			scanLockLog.Warn("scan lock: version group %s of book %s could not be read (%v); treating %s as busy rather than locking it without its versions",
				*r.VersionGroupID, r.ID, err, b.FilePath)
			return nil, nil, fmt.Errorf("scan lock: version group %s: %w", *r.VersionGroupID, err)
		}
		if len(members) > scanLockMaxGroup {
			// Too many to lock whole. Still lock the members holding the SAME
			// content as this row -- a protected original and its library
			// copy -- because an apply or organize of the original acts on
			// the copy's files while holding only the original.
			scanLockGroupCapped.Add(1)
			ids := contentIDs(r)
			for i := range members {
				m := members[i]
				if _, ok := rows[m.ID]; ok || !sharesContent(ids, &m) {
					continue
				}
				rows[m.ID] = &m
			}
			continue
		}
		for i := range members {
			if _, ok := rows[members[i].ID]; !ok {
				m := members[i]
				rows[m.ID] = &m
			}
		}
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, rows, nil
}

// contentIDs is every content hash a row carries (FileHash,
// OriginalFileHash, OrganizedFileHash): the identities organizer.IsLibraryCopyOf
// matches an original to its library copy by.
func contentIDs(b *database.Book) map[string]bool {
	out := map[string]bool{}
	for _, h := range []*string{b.FileHash, b.OriginalFileHash, b.OrganizedFileHash} {
		if h != nil && *h != "" {
			out[*h] = true
		}
	}
	return out
}

func sharesContent(ids map[string]bool, b *database.Book) bool {
	for h := range contentIDs(b) {
		if ids[h] {
			return true
		}
	}
	return false
}

func rowValues(m map[string]*database.Book) []*database.Book {
	out := make([]*database.Book, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

type scanLockOutcome int

const (
	scanLockHeld     scanLockOutcome = iota
	scanLockBusy                     // round 0: someone holds a row; requeue
	scanLockGaveUp                   // a bounded wait expired; record busy
	scanLockVanished                 // the book's files are gone; skip it
	scanLockCanceled                 // ctx canceled
	// the book's rows or version group could not be read, so its lock set is
	// unknown; it is NOT locked (fail closed) and nothing is held. The
	// returned error says why: a store failure, not an apply.
	scanLockUnreadable
)

// acquireScanBookLock takes the lock set for b. Round 0 never waits. Rounds
// >= 1 first try, then wait until waitCtx's deadline -- the round's shared
// deadline, which may already have passed when the book reaches a worker; the
// book is then taken only if it is free right now. ctx is the scan's own
// context: its cancellation is scanLockCanceled, waitCtx expiring is
// scanLockGaveUp. The returned hold is nil unless the outcome is scanLockHeld;
// readErr is non-nil only with scanLockUnreadable.
//
// freshSnaps: the main pass is about to read the tags, so every held row's
// snapshot is retaken now. The AI-phase re-save passes false: its baseline is
// what the main pass last wrote (rememberRow), and a row another writer
// changed since then must keep that change. Rows with no snapshot yet get one.
func acquireScanBookLock(ctx, waitCtx context.Context, b *Book, round int, freshSnaps bool) (_ *scanlock.Hold, _ scanLockOutcome, readErr error) {
	for try := 0; try < scanLockResolveTries; try++ {
		ids, _, rerr := resolveScanLockSet(b)
		if rerr != nil {
			return nil, scanLockUnreadable, rerr
		}
		h, ok := scanlock.Books.TryLockSetIdle(ids)
		if !ok {
			if round == 0 {
				return nil, scanLockBusy, nil
			}
			if ctx.Err() != nil {
				return nil, scanLockCanceled, nil
			}
			// Try first, THEN wait: with the shared deadline already past,
			// LockSetIdle's select could pick ctx.Done over a free key.
			if waitCtx.Err() != nil {
				return nil, scanLockGaveUp, nil
			}
			var err error
			h, err = lockSetIdleBeating(ctx, waitCtx, ids, b.FilePath)
			if err != nil {
				if ctx.Err() != nil {
					return nil, scanLockCanceled, nil
				}
				return nil, scanLockGaveUp, nil
			}
		}
		// Re-resolve under the lock: an apply that held a row may have renamed
		// or regrouped it while we waited.
		again, rows, rerr := resolveScanLockSet(b)
		if rerr != nil {
			h.Release()
			return nil, scanLockUnreadable, rerr
		}
		if !subset(again, h) {
			h.Release()
			if round == 0 {
				return nil, scanLockBusy, nil
			}
			continue
		}
		if filesVanished(b) {
			h.Release()
			return nil, scanLockVanished, nil
		}
		if freshSnaps {
			b.rowSnaps = nil
		}
		for _, id := range again {
			if _, have := b.rowSnaps[id]; !have {
				b.rememberRow(rows[id])
			}
		}
		return h, scanLockHeld, nil
	}
	return nil, scanLockGaveUp, nil
}

// saveBookUnderScanLock is the AI phase's save: it runs after the worker has
// released the book, so it takes the lock again (bounded wait, holding
// nothing) and saves under it, restarting once if the save discovers a row it
// does not hold. saved is false when the book's files vanished; err is
// non-nil for a real save failure or the bounded wait expiring.
func saveBookUnderScanLock(ctx context.Context, book *Book, save func(context.Context, *Book) error) (saved bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		wctx, cancel := context.WithTimeout(ctx, scanLockWait)
		hold, outcome, readErr := acquireScanBookLock(ctx, wctx, book, 1, false)
		cancel()
		switch outcome {
		case scanLockUnreadable:
			scanLockUnreadableCount.Add(1)
			return false, fmt.Errorf("scan lock: the version group of %s could not be read, so it was not saved; the next scan picks it up: %w", book.FilePath, readErr)
		case scanLockVanished:
			scanLockVanishedCount.Add(1)
			return false, nil
		case scanLockCanceled:
			return false, ctx.Err()
		case scanLockBusy, scanLockGaveUp:
			scanLockGaveUpCount.Add(1)
			return false, fmt.Errorf("scan lock: %s was held by an apply longer than %s; the next scan picks it up", book.FilePath, scanLockWait)
		}
		serr := save(scanlock.WithHold(ctx, hold), book)
		hold.Release()
		if w, ok := asWiden(serr); ok {
			book.scanLockExtra = append(book.scanLockExtra, w.ids...)
			continue
		}
		return serr == nil, serr
	}
	return false, fmt.Errorf("scan lock: %s: its rows kept changing while the AI-phase save tried to lock them", book.FilePath)
}

func subset(ids []string, h *scanlock.Hold) bool {
	for _, id := range ids {
		if !h.Has(id) {
			return false
		}
	}
	return true
}

// filesVanished reports whether the book's path, or any of its segment files,
// no longer exists. The walk listed them; an apply that renamed the book in
// the meantime moved them. Only a definite not-exist counts: any other stat
// error is left to the read path, which already records it.
func filesVanished(b *Book) bool {
	gone := func(p string) bool {
		_, err := os.Stat(p)
		return err != nil && errors.Is(err, os.ErrNotExist)
	}
	if gone(b.FilePath) {
		return true
	}
	for _, s := range b.SegmentFiles {
		if gone(s) {
			return true
		}
	}
	return false
}
