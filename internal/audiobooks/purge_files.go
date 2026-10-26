// file: internal/audiobooks/purge_files.go
// version: 1.0.0
// guid: e3a7c1d4-58b2-4f96-9d0e-6b4a2c8f1e57
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// purgeListLimit is the page size a purge pass lists the whole trash with (48k
// rows on production).
const purgeListLimit = 1_000_000

// trashedAtOrBefore reports whether b was trashed at or before t
// (database.Book.TrashedAt: the stamp, else the row's last write). A book whose
// trash time is unknown is not provably older, so it is not "at or before": a
// bounded pass leaves it alone, and a retention window does not take it.
func trashedAtOrBefore(b *database.Book, t time.Time) bool {
	at := b.TrashedAt()
	return at != nil && !at.After(t)
}

// trashedIDsByPath returns, for each of paths, the ids of the books in trash
// that name it as their FilePath, leaving out the ids except accepts. trash is
// the whole trash listing, not one a retention window cut: a trashed book the
// window left out still owns its file. The PREVIEW's settlement uses it
// (settlePreviewFiles), where one listing the pass already took is the right
// tool and the answer is a forecast; the real removal asks the store about each
// path when it is about to remove it (otherBookRowAt).
func trashedIDsByPath(trash []database.Book, paths map[string]struct{}, except func(id string) bool) map[string][]string {
	out := map[string][]string{}
	for i := range trash {
		b := &trash[i]
		if b.FilePath == "" {
			continue
		}
		if _, ok := paths[b.FilePath]; !ok {
			continue
		}
		if except != nil && except(b.ID) {
			continue
		}
		out[b.FilePath] = append(out[b.FilePath], b.ID)
	}
	for p := range out {
		slices.Sort(out[p])
	}
	return out
}

// pendingFile is a file, or empty directory, a book purge decided to remove with
// delete_files. It is held until every book of the pass has been decided, then
// re-checked (removeDeferredFiles).
type pendingFile struct {
	path  string
	isDir bool
	// id is the file's identity when the purge decided to remove it. Books that
	// name one path were decided at different moments; if what they saw
	// differs, the file changed in between and idConflict is set: it is not
	// the file any of them decided on, so it stays.
	id         fileIdentity
	idConflict bool
	bookIDs    []string
	// attempts is how many passes a path that cannot be resolved has used so far
	// (the largest count among its journal records), and recordedAt the time the
	// first record was written. Both are read back from the journal
	// (readPendingFiles); an entry a pass made itself starts at zero.
	attempts   int
	recordedAt time.Time
	// waitingIDs are the books whose journal records carry WaitingForFolder
	// (read back from the journal; empty for an entry a pass made itself).
	waitingIDs []string
}

// allWaiting reports whether every journal record of the path is waiting for a
// folder: only then is the path one no purge can settle. A path with a record
// that is not (a later pass journaled the same path again) is still a removal a
// purge will try.
func (f *pendingFile) allWaiting() bool {
	return len(f.bookIDs) > 0 && len(f.waitingIDs) == len(f.bookIDs)
}

// pendingFiles collects the removals a purge pass decided on. Workers add to it
// concurrently.
//
// Why a pass does not remove a file the moment its book is purged: another
// trashed book can name the same path, and whether that file may go depends on
// what that book's own purge decides. If it is kept (an undo can still run, it
// holds listening state, or the bound left it out) the file is its file, and
// removing it leaves a restorable book with no audio. If it is purged too, the
// file is nobody's and must go, or a delete_files purge of two trashed twins
// leaves their file on disk for good. Only after the last book is decided is
// the answer the same one the preview gave (settlePreviewFiles).
type pendingFiles struct {
	// preview is the preview the pass is bound to, or nil. Only a path it
	// listed is journaled (purge_pending_journal.go): a later pass replays the
	// journal with no preview in memory, and must not remove a file the
	// screen the owner approved said would stay.
	preview *purgePreviewRecord

	mu     sync.Mutex
	byPath map[string]*pendingFile
}

// markJournaled notes what the journal record of bookID's removal of path says,
// keeping the largest attempt count and the earliest write time of its records,
// and which of them wait for a folder.
func (p *pendingFiles) markJournaled(path, bookID string, attempts int, recordedAt time.Time, waiting bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.byPath[path]
	if f == nil {
		return
	}
	if waiting {
		f.waitingIDs = append(f.waitingIDs, bookID)
	}
	f.attempts = max(f.attempts, attempts)
	if f.recordedAt.IsZero() || (!recordedAt.IsZero() && recordedAt.Before(f.recordedAt)) {
		f.recordedAt = recordedAt
	}
}

func (p *pendingFiles) add(bookID string, v purgeFileVerdict) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byPath == nil {
		p.byPath = map[string]*pendingFile{}
	}
	f, ok := p.byPath[v.remove]
	if !ok {
		f = &pendingFile{path: v.remove, isDir: v.isDir, id: v.id}
		p.byPath[v.remove] = f
	} else if f.id != v.id || f.isDir != v.isDir {
		f.idConflict = true
	}
	f.bookIDs = append(f.bookIDs, bookID)
}

// sorted returns the pending removals in path order.
func (p *pendingFiles) sorted() []*pendingFile {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*pendingFile, 0, len(p.byPath))
	for _, f := range p.byPath {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// removePurgedFile removes the file or empty directory v names (v.remove is
// set). It does NOT remove the folders that empties: that is the caller's, once
// it has finished deciding every path it is going to decide, because a folder
// removed in the middle of a run makes a sibling path's "not there" read as "its
// folder is missing too" (errPathFolderMissing) depending on which worker got
// there first. It reports how many things it removed and the error lines for
// bookID.
func removePurgedFile(bookID string, v purgeFileVerdict) (removed int, errs []string) {
	if v.isDir {
		// Directory-based book. It owns no file rows, so there is nothing of
		// its own inside to remove: the directory goes only because it is
		// already empty (planPurgeFile checked).
		if rmErr := os.Remove(v.remove); rmErr != nil && !os.IsNotExist(rmErr) {
			errs = append(errs, fmt.Sprintf("%s: failed to remove empty dir %s: %v", bookID, v.remove, rmErr))
		} else if rmErr == nil {
			removed++
		}
		return removed, errs
	}
	// Single-file book
	if err := os.Remove(v.remove); err != nil && !os.IsNotExist(err) {
		// The row is gone and so is its tombstone (purgeFinish): the file is
		// left on disk. Its removal was journaled before the row went
		// (purgeDeleteRow), whether the purge was a pass or a single book, and the
		// caller keeps that record when this fails, so a later pass retries it
		// (purge_pending_journal.go).
		errs = append(errs, fmt.Sprintf("%s: book purged, but its file could not be removed: %v", bookID, err))
	} else if err == nil {
		removed++
	}
	return removed, errs
}

// errPathLookupFailed marks a "does anything still name this path?" question
// the store could not answer. It is transient: the answer is "ask again", not
// "no". A removal that failed this way is retried by the next pass
// (purge_pending_journal.go), while a definite "something names it" is final.
var errPathLookupFailed = errors.New("path lookup failed")

// errTrashedTwin names the books that still have path as their file.
func errTrashedTwin(path string, ids []string) error {
	return fmt.Errorf("%s is still the path of book(s) %v, which this pass did not purge", path, ids)
}

// otherBookRowAt returns a non-nil error when any book row, live or trashed,
// still names path. It asks the store about THIS path, at the moment the caller
// is about to remove the file, and never works from a listing taken earlier:
// the answer a trash listing gave a few seconds ago does not cover a live book
// trashed since, which is restorable and wants its audio. The book_atpath
// index holds every row, so the read is one index seek (and it replaces a scan
// of the whole trash, 48k rows on production).
//
// The purged books' own rows are already deleted when this runs, so there is
// no exclusion list: whatever it finds is a book this pass did not purge. A
// failed lookup is an error too (errPathLookupFailed): fail closed, a path
// that cannot be vouched for is not a free path.
func (svc *AudiobookService) otherBookRowAt(path string) error {
	rows, err := svc.bookRowsAtPath()
	if err != nil {
		return fmt.Errorf("cannot verify %s is not a book's file: %w: %w", path, errPathLookupFailed, err)
	}
	ids, err := rows.BookIDsAtPath(path)
	if err != nil {
		return fmt.Errorf("cannot verify %s is not a book's file: books-at-path lookup: %w: %w", path, errPathLookupFailed, err)
	}
	if len(ids) > 0 {
		return errTrashedTwin(path, ids)
	}
	return nil
}

// bookRowsAtPath resolves the all-rows path lookup from the service's store.
// It is a capability, not a Store method (database.BookRowPathReader), so a
// store that cannot answer it makes the removal fail closed: the file stays on
// disk and the journal retries it, rather than the purge deleting a file a
// trashed book may still want. Never a silent "no rows".
func (svc *AudiobookService) bookRowsAtPath() (database.BookRowPathReader, error) {
	rows, ok := database.AsCapability[database.BookRowPathReader](svc.store)
	if !ok {
		return nil, fmt.Errorf("store %T does not provide the all-rows book path lookup (database.BookRowPathReader)", svc.store)
	}
	return rows, nil
}

// removeDeferredFiles is the second half of a purge pass's file removal, run
// once every book has been decided (and again, from the journal, by a later
// pass for a pass that never got here: replayPendingFileRemovals). For each path
// a purged book named it checks, as the store is NOW:
//
//   - planPurgeFile again (protected path, library root, a book_file row or a
//     live book naming it, still there);
//   - no book row at all, live or trashed, names it (otherBookRowAt, asked per
//     path just before the removal): the pass purged some books and kept
//     others, and a kept one's file is its own, and a book trashed since the
//     pass began is restorable;
//   - when the pass is bound to a preview, that the preview listed the path
//     (a file the preview said stays is never removed, whatever changed since).
//
// It reports how many things it removed and the error lines, in path order. A
// path it decided (removed, already gone, or kept for a reason that will not
// change) is forgotten from the journal; one that failed for a reason that may
// pass (a lookup error, a failed stat or os.Remove) stays recorded for the next
// pass.
//
// The paths are decided by a bounded worker pool, as the pass's own books are
// (RunPurge): each path costs several store reads, a stat and a remove, and
// there is one per purged book that owned a file, up to the size of the trash.
// It is safe because the paths are distinct (pendingFiles keys them by path),
// so no two workers read, remove or journal-settle the same file, and every store
// read it makes is already used from RunPurge's concurrent workers. Workers only
// unlink: the folders a removal empties are removed after every path is decided,
// in path order, so the outcome does not depend on which worker ran first. Each
// worker forgets its path's record as soon as it has decided it.
func (svc *AudiobookService) removeDeferredFiles(ctx context.Context, files *pendingFiles, preview *purgePreviewRecord) (removed int, errs []string) {
	pending := files.sorted()
	if len(pending) == 0 {
		return 0, nil
	}
	root := config.Snapshot().RootDir
	// Every "not there" below reads as "already gone" and settles its record, so
	// with the library root not up (an unmounted library is an empty directory)
	// nothing is decided and every record is kept for a later pass
	// (libraryRootReady). The pass's own entries are among them: it checked at
	// its start, and a mount lost since is caught here.
	if rootErr := svc.libraryRootReady(root); rootErr != nil {
		return 0, []string{fmt.Sprintf("%d file removal(s) not carried out, their records kept for a later pass: %v", len(pending), rootErr)}
	}
	out := make([]deferredOutcome, len(pending))
	// The folders this run is unlinking in (see folderCheck).
	emptied := &dirSet{}
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for i, f := range pending {
		g.Go(func() error {
			if ctx.Err() != nil {
				out[i] = deferredOutcome{retry: true, notReached: true}
				return nil
			}
			o := svc.removeDeferredFile(f, root, preview, emptied)
			// A decided path forgets its record at once, right after its own
			// unlink: a crash then leaves a record only for the paths not yet
			// reached, and not for every file the run had already removed (whose
			// record a replay could only read as "gone, and its folder too").
			if !o.retry {
				if err := svc.forgetPendingFiles(f.bookIDs); err != nil {
					o.errs = append(o.errs, fmt.Sprintf("the record of the removal of %s could not be cleared (a later pass will decide it again, which removes nothing twice): %v", f.path, err))
				}
			}
			out[i] = o
			return nil
		})
	}
	_ = g.Wait() // the workers return nil: their outcomes are in out
	// Every path is decided; only now are the folders the removals emptied
	// removed, in path order, so no decision above could see a folder this run
	// took away.
	notReached, folderMissing := 0, 0
	exampleMissing := -1
	for i, f := range pending {
		o := out[i]
		if o.removedPath != "" {
			removeEmptyParentsUpToRoot(o.removedPath, root)
		}
		if o.notReached {
			notReached++
		}
		if o.folderMissing {
			if folderMissing == 0 {
				exampleMissing = i
			}
			folderMissing++
		}
		removed += o.removed
		errs = append(errs, o.errs...)
		if o.retry && o.bump {
			// Counted for the pass's own entry too: its record was journaled with
			// no attempts used, and this pass is the first to try it.
			if err := svc.recordUnresolvableAttempt(f); err != nil {
				errs = append(errs, fmt.Sprintf("could not count this pass against the pending removal of %s: %v", f.path, err))
			}
		}
		// What the owner sees: a record this pass could not decide for want of its
		// folder is marked, so the trash listing and a preview count it apart from
		// the removals a purge will carry out; one that is retried for any other
		// reason (the folder is back) is unmarked. A path not reached (the run was
		// stopped) says nothing about its folder.
		switch {
		case o.folderMissing && !f.allWaiting():
			if err := svc.setWaitingForFolder(f, true); err != nil {
				errs = append(errs, fmt.Sprintf("could not mark the pending removal of %s as waiting for its folder: %v", f.path, err))
			}
		case o.retry && !o.folderMissing && !o.notReached && len(f.waitingIDs) > 0:
			if err := svc.setWaitingForFolder(f, false); err != nil {
				errs = append(errs, fmt.Sprintf("could not clear the waiting-for-folder mark of the pending removal of %s: %v", f.path, err))
			}
		}
	}
	if folderMissing > 0 {
		ex := pending[exampleMissing]
		errs = append(errs, fmt.Sprintf("%d pending file removal(s) are waiting for a folder that is missing or empty and are kept, to be looked at again on every pass; for example %s (book %s), whose folder %s is not there or holds nothing: the file may have been removed together with its folder, or that part of the library is not mounted. Nothing removes these records while the folder stays like that; if it is gone for good, the owner can forget them in the purge dialog (that deletes only the records, never a file or a book)", folderMissing, ex.path, ex.bookIDs[0], filepath.Dir(ex.path)))
	}
	if notReached > 0 {
		errs = append(errs, fmt.Sprintf("stopped (%v): %d file removal(s) not looked at, their records kept for a later run", ctx.Err(), notReached))
	}
	return removed, errs
}

// deferredOutcome is what removeDeferredFile decided about one pending path.
type deferredOutcome struct {
	removed int
	errs    []string
	// retry: the outcome may change on a later pass, so the journal keeps the
	// record.
	retry bool
	// bump: the retry is for a path that cannot be resolved, which counts
	// against its attempts (maxUnresolvableAttempts).
	bump bool
	// folderMissing: the file is not there and neither is its folder, which a
	// removed folder and a library that is not mounted both look like. The record
	// is kept for every later pass, with no line of its own: the pass reports all
	// such records as one line (removeDeferredFiles).
	folderMissing bool
	// removedPath is the path this run unlinked, whose emptied parents are removed
	// once every path is decided.
	removedPath string
	// notReached: ctx was done before this path was looked at; its record is
	// kept for the next run.
	notReached bool
}

// removeDeferredFile decides and does one pending path.
//
// A path that cannot be resolved at all (ELOOP, ENAMETOOLONG: errPathUnresolvable)
// is retried only maxUnresolvableAttempts passes, and then cleared with a line
// saying so: it will not resolve by trying again, and a record that is never
// cleared is a line in every purge report for ever. bump says the retry counts
// against that limit. A path whose folder is missing along with it
// (errPathFolderMissing) is different: it does resolve, by the library being
// mounted again, and the record is the only thing left that names the file, so it
// is kept however many passes go by.
func (svc *AudiobookService) removeDeferredFile(f *pendingFile, root string, preview *purgePreviewRecord, emptied *dirSet) deferredOutcome {
	bookID := f.bookIDs[0]
	v := svc.planPurgeFileIn(&database.Book{ID: bookID, FilePath: f.path}, emptied)
	switch {
	case v.protected:
		singleLog.Debug("purge skipping file deletion: protected path %s (book %s)",
			logger.SanitizeLogValue(f.path), logger.SanitizeLogValue(bookID))
		return deferredOutcome{}
	case v.keptErr != nil:
		line := fmt.Sprintf("%s: book purged, file kept: %v", bookID, v.keptErr)
		if !errors.Is(v.keptErr, errPathLookupFailed) {
			return deferredOutcome{errs: []string{line}}
		}
		if errors.Is(v.keptErr, errPathFolderMissing) {
			return deferredOutcome{retry: true, folderMissing: true}
		}
		if errors.Is(v.keptErr, errPathUnresolvable) {
			if f.attempts+1 >= maxUnresolvableAttempts {
				return deferredOutcome{errs: []string{fmt.Sprintf("%s (gave up after %d passes: the path cannot be resolved, so the file is left where it is and its record is cleared)", line, maxUnresolvableAttempts)}}
			}
			return deferredOutcome{errs: []string{line}, retry: true, bump: true}
		}
		return deferredOutcome{errs: []string{line}, retry: true}
	case v.remove == "":
		// Already gone, or a directory that is not empty.
		return deferredOutcome{}
	}
	// The path names a file, but is it the file the purge decided to remove?
	// A different one (replaced, or changed, since: fileIdentity) is nobody's
	// decision, so it stays, and the decision is settled: the file it was about
	// is gone. A record with no identity cannot be vouched for either.
	if f.idConflict || f.isDir != v.isDir || !f.id.matches(v.id, v.isDir) {
		singleLog.Warn("purge keeping %s: it is not the file the purge decided to remove (replaced or changed since; book %s)",
			logger.SanitizeLogValue(f.path), logger.SanitizeLogValue(bookID))
		return deferredOutcome{errs: []string{fmt.Sprintf("%s: book purged, file kept: %s is not the file this purge decided to remove (it was replaced or changed since)", bookID, f.path)}}
	}
	if err := svc.otherBookRowAt(f.path); err != nil {
		return deferredOutcome{errs: []string{fmt.Sprintf("%s: book purged, file kept: %v", bookID, err)}, retry: errors.Is(err, errPathLookupFailed)}
	}
	if !preview.listsFile(f.path) {
		return deferredOutcome{errs: []string{fmt.Sprintf("%s: book purged, file kept: the preview this pass is bound to did not list %s for removal", bookID, f.path)}}
	}
	// Registered BEFORE the unlink: a neighbour's already-gone file that finds this
	// folder empty must find it in the set (folderCheck).
	emptied.add(filepath.Dir(f.path))
	n, e := removePurgedFile(bookID, v)
	o := deferredOutcome{removed: n, errs: e, retry: len(e) > 0}
	if n > 0 {
		o.removedPath = f.path
	}
	return o
}

// settlePreviewFiles finishes a preview's file account once every book is
// decided: a path some purgeable book would remove is listed only when no
// trashed book that is NOT purgeable names it too, exactly the rule the real
// pass applies at the end (removeDeferredFiles), so the list and the removal
// cannot disagree. A path a kept book names is counted in FilesKept. trash is
// the whole trash listing.
func (r *PurgeResult) settlePreviewFiles(trash []database.Book) {
	if len(r.fileCands) == 0 {
		return
	}
	twins := trashedIDsByPath(trash, r.fileCands, func(id string) bool {
		_, purged := r.purgeable[id]
		return purged
	})
	paths := make([]string, 0, len(r.fileCands))
	for p := range r.fileCands {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if len(twins[p]) > 0 {
			r.FilesKept++
			continue
		}
		r.FilesToDeleteCount++
		r.FilesToDelete = append(r.FilesToDelete, p)
	}
}

// purgePreviewRecord is what a preview decided, kept in memory so the purge
// that follows it can be held to it: the books it said would be purged and the
// files it said would be removed. A book it showed as kept (it owns files, an
// undo can still run, it holds listening state) can become purgeable between
// the preview and the click; the purge leaves it alone, because the screen the
// user approved did not say it would go.
type purgePreviewRecord struct {
	bound time.Time
	// started is when the preview began: a book trashed at or before it was
	// in the trash when the owner looked.
	started   time.Time
	purgeable map[string]struct{}
	files     map[string]struct{}
}

// includes reports whether the preview listed the book as one the pass would
// purge. A nil record (no preview binds the pass: the nightly one) includes
// every book.
func (p *purgePreviewRecord) includes(bookID string) bool {
	if p == nil {
		return true
	}
	_, ok := p.purgeable[bookID]
	return ok
}

// listsFile reports whether the preview listed path for removal. A nil record
// lists every path.
func (p *purgePreviewRecord) listsFile(path string) bool {
	if p == nil {
		return true
	}
	_, ok := p.files[path]
	return ok
}

// purgePreviewsKept is how many previews the service remembers. A dialog holds
// one; a second admin's open dialog or a re-run preview needs its own.
const purgePreviewsKept = 4

// rememberPreview keeps a finished preview for the purge that carries its
// trashed_before. In memory only, like the breakdown: a restart forgets it and
// the purge then asks for a new preview (ErrPurgePreviewUnknown).
func (svc *AudiobookService) rememberPreview(r *PurgeResult) {
	if r.TrashedBefore == nil || r.StoppedEarly {
		return
	}
	rec := &purgePreviewRecord{
		bound:     *r.TrashedBefore,
		started:   r.startedAt,
		purgeable: r.purgeable,
		files:     make(map[string]struct{}, len(r.FilesToDelete)),
	}
	for _, f := range r.FilesToDelete {
		rec.files[f] = struct{}{}
	}
	svc.purgePreviewMu.Lock()
	defer svc.purgePreviewMu.Unlock()
	kept := svc.purgePreviews[:0:0]
	for _, p := range svc.purgePreviews {
		if !p.bound.Equal(rec.bound) {
			kept = append(kept, p)
		}
	}
	kept = append(kept, rec)
	if len(kept) > purgePreviewsKept {
		kept = kept[len(kept)-purgePreviewsKept:]
	}
	svc.purgePreviews = kept
}

// previewFor returns the preview that returned bound, or nil.
func (svc *AudiobookService) previewFor(bound time.Time) *purgePreviewRecord {
	svc.purgePreviewMu.Lock()
	defer svc.purgePreviewMu.Unlock()
	for _, p := range svc.purgePreviews {
		if p.bound.Equal(bound) {
			return p
		}
	}
	return nil
}
