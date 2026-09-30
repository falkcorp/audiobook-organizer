// file: internal/scanner/scan_book_lock.go
// version: 1.0.0
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
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
// is writing goes to the back of the chunk and the worker moves on. Later
// rounds wait, bounded by scanLockWait, and a book still busy after the last
// round is recorded as a "busy" FileFailure and picked up by the next scan.
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

// scanLockWait bounds one blocking acquire in rounds >= 1. An apply holds its
// book across its whole file job (tag write + rename): 67s for a large
// multi-file book on 2026-09-30. A var so tests can shorten it.
var scanLockWait = 90 * time.Second

// Process-wide counters; ProcessBooksParallel reports each run's delta.
var (
	scanLockGroupCapped   atomic.Int64 // version groups too large to lock whole
	scanLockVanishedCount atomic.Int64 // books skipped: files gone by the time the lock was held
	scanLockRequeuedCount atomic.Int64 // books sent to the back of the chunk (busy, or widened)
	scanLockGaveUpCount   atomic.Int64 // books left for the next scan after the bounded waits
	scanLockKeptFields    atomic.Int64 // columns kept because another writer changed them mid-scan
)

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

func asWiden(err error) (*errScanLockWiden, bool) {
	var w *errScanLockWiden
	if errors.As(err, &w) {
		return w, true
	}
	return nil, false
}

// rowSnap is the value of every tag-derived column applyScannerFields may
// overlay, as the row stood when the scanner took its lock (before the tag
// read), or as the scanner itself last wrote it.
type rowSnap struct {
	Title                                             string
	AuthorID, SeriesID, SeriesSequence                *int
	Narrator, Language, Publisher, ASIN               *string
	WorkID, OpenLibraryID, HardcoverID, GoogleBooksID *string
}

func snapOf(b *database.Book) rowSnap {
	return rowSnap{
		Title: b.Title, AuthorID: b.AuthorID, SeriesID: b.SeriesID, SeriesSequence: b.SeriesSequence,
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
	workChanged := !eqStr(work, snap.WorkID)
	olChanged := !eqStr(ol, snap.OpenLibraryID)
	hcChanged := !eqStr(hc, snap.HardcoverID)
	gbChanged := !eqStr(gb, snap.GoogleBooksID)

	applyScannerFields(cur, scanned, eff)

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
func resolveScanLockSet(b *Book) ([]string, map[string]*database.Book) {
	st := getStore()
	rows := map[string]*database.Book{}
	if st == nil {
		return nil, rows
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
			continue
		}
		if len(members) > scanLockMaxGroup {
			scanLockGroupCapped.Add(1)
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
	return ids, rows
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
)

// acquireScanBookLock takes the lock set for b. Round 0 never waits. The
// returned hold is nil unless the outcome is scanLockHeld.
//
// freshSnaps: the main pass is about to read the tags, so every held row's
// snapshot is retaken now. The AI-phase re-save passes false: its baseline is
// what the main pass last wrote (rememberRow), and a row another writer
// changed since then must keep that change. Rows with no snapshot yet get one.
func acquireScanBookLock(ctx context.Context, b *Book, round int, freshSnaps bool) (*scanlock.Hold, scanLockOutcome) {
	for try := 0; try < scanLockResolveTries; try++ {
		ids, _ := resolveScanLockSet(b)
		var h *scanlock.Hold
		if round == 0 {
			var ok bool
			if h, ok = scanlock.Books.TryLockSet(ids); !ok {
				return nil, scanLockBusy
			}
		} else {
			wctx, cancel := context.WithTimeout(ctx, scanLockWait)
			var err error
			h, err = scanlock.Books.LockSet(wctx, ids)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil, scanLockCanceled
				}
				return nil, scanLockGaveUp
			}
		}
		// Re-resolve under the lock: an apply that held a row may have renamed
		// or regrouped it while we waited.
		again, rows := resolveScanLockSet(b)
		if !subset(again, h) {
			h.Release()
			if round == 0 {
				return nil, scanLockBusy
			}
			continue
		}
		if filesVanished(b) {
			h.Release()
			return nil, scanLockVanished
		}
		if freshSnaps {
			b.rowSnaps = nil
		}
		for _, id := range again {
			if _, have := b.rowSnaps[id]; !have {
				b.rememberRow(rows[id])
			}
		}
		return h, scanLockHeld
	}
	return nil, scanLockGaveUp
}

// saveBookUnderScanLock is the AI phase's save: it runs after the worker has
// released the book, so it takes the lock again (bounded wait, holding
// nothing) and saves under it, restarting once if the save discovers a row it
// does not hold. saved is false when the book's files vanished; err is
// non-nil for a real save failure or the bounded wait expiring.
func saveBookUnderScanLock(ctx context.Context, book *Book, save func(context.Context, *Book) error) (saved bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		hold, outcome := acquireScanBookLock(ctx, book, 1, false)
		switch outcome {
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
