// file: internal/audiobooks/purge_files.go
// version: 1.2.0
// guid: e3a7c1d4-58b2-4f96-9d0e-6b4a2c8f1e57
// last-edited: 2026-10-25

package audiobooks

import (
	"errors"
	"fmt"
	"os"
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
// set) and the now-empty parents inside root. It reports how many things it
// removed and the error lines for bookID.
func removePurgedFile(bookID string, v purgeFileVerdict, root string) (removed int, errs []string) {
	if v.isDir {
		// Directory-based book. It owns no file rows, so there is nothing of
		// its own inside to remove: the directory goes only because it is
		// already empty (planPurgeFile checked).
		if rmErr := os.Remove(v.remove); rmErr != nil && !os.IsNotExist(rmErr) {
			errs = append(errs, fmt.Sprintf("%s: failed to remove empty dir %s: %v", bookID, v.remove, rmErr))
		} else if rmErr == nil {
			removed++
			removeEmptyParentsUpToRoot(v.remove, root)
		}
		return removed, errs
	}
	// Single-file book
	if err := os.Remove(v.remove); err != nil && !os.IsNotExist(err) {
		// The row is gone and so is its tombstone (purgeFinish): the file is
		// left on disk, and a purge pass remembers the removal and retries it
		// (purge_pending_journal.go).
		errs = append(errs, fmt.Sprintf("%s: book purged, but its file could not be removed: %v", bookID, err))
	} else if err == nil {
		removed++
		removeEmptyParentsUpToRoot(v.remove, root)
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
	ids, err := svc.store.BookIDsAtPath(path)
	if err != nil {
		return fmt.Errorf("cannot verify %s is not a book's file: books-at-path lookup: %w: %w", path, errPathLookupFailed, err)
	}
	if len(ids) > 0 {
		return errTrashedTwin(path, ids)
	}
	return nil
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
// so no two workers read, remove or journal-settle the same file, and every
// store read it makes is already used from RunPurge's concurrent workers. The
// one thing workers can share is a parent directory: removeEmptyParentsUpToRoot
// removes it only when it reads it empty, and of any set of workers removing
// the files of one directory the last to read it sees every removal, so the
// directory is never left empty. A worker that reads it before a sibling has
// removed its file stops, and that sibling's own walk removes it. Each worker
// writes only its own slot of out; the lines are merged in path order, so the
// result is the same whatever the scheduling.
func (svc *AudiobookService) removeDeferredFiles(files *pendingFiles, preview *purgePreviewRecord) (removed int, errs []string) {
	pending := files.sorted()
	if len(pending) == 0 {
		return 0, nil
	}
	root := config.Snapshot().RootDir
	type outcome struct {
		removed int
		errs    []string
		retry   bool
	}
	out := make([]outcome, len(pending))
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for i, f := range pending {
		g.Go(func() error {
			n, e, retry := svc.removeDeferredFile(f, root, preview)
			out[i] = outcome{removed: n, errs: e, retry: retry}
			return nil
		})
	}
	_ = g.Wait() // the workers return nil: their outcomes are in out
	var settled []string
	for i, f := range pending {
		removed += out[i].removed
		errs = append(errs, out[i].errs...)
		if !out[i].retry {
			settled = append(settled, f.bookIDs...)
		}
	}
	if err := svc.forgetPendingFiles(settled); err != nil {
		errs = append(errs, fmt.Sprintf("the record of %d decided file removal(s) could not be cleared (a later pass will re-decide them, which removes nothing twice): %v", len(settled), err))
	}
	return removed, errs
}

// removeDeferredFile decides and does one pending path. retry is true when the
// outcome may change on a later pass, so the journal keeps the record.
func (svc *AudiobookService) removeDeferredFile(f *pendingFile, root string, preview *purgePreviewRecord) (removed int, errs []string, retry bool) {
	bookID := f.bookIDs[0]
	v := svc.planPurgeFile(&database.Book{ID: bookID, FilePath: f.path})
	switch {
	case v.protected:
		singleLog.Debug("purge skipping file deletion: protected path %s (book %s)",
			logger.SanitizeLogValue(f.path), logger.SanitizeLogValue(bookID))
		return 0, nil, false
	case v.keptErr != nil:
		return 0, []string{fmt.Sprintf("%s: book purged, file kept: %v", bookID, v.keptErr)}, errors.Is(v.keptErr, errPathLookupFailed)
	case v.remove == "":
		// Already gone, or a directory that is not empty.
		return 0, nil, false
	}
	// The path names a file, but is it the file the purge decided to remove?
	// A different one (replaced, or changed, since: fileIdentity) is nobody's
	// decision, so it stays, and the decision is settled: the file it was about
	// is gone. A record with no identity cannot be vouched for either.
	if f.idConflict || f.isDir != v.isDir || !f.id.matches(v.id, v.isDir) {
		singleLog.Warn("purge keeping %s: it is not the file the purge decided to remove (replaced or changed since; book %s)",
			logger.SanitizeLogValue(f.path), logger.SanitizeLogValue(bookID))
		return 0, []string{fmt.Sprintf("%s: book purged, file kept: %s is not the file this purge decided to remove (it was replaced or changed since)", bookID, f.path)}, false
	}
	if err := svc.otherBookRowAt(f.path); err != nil {
		return 0, []string{fmt.Sprintf("%s: book purged, file kept: %v", bookID, err)}, errors.Is(err, errPathLookupFailed)
	}
	if !preview.listsFile(f.path) {
		return 0, []string{fmt.Sprintf("%s: book purged, file kept: the preview this pass is bound to did not list %s for removal", bookID, f.path)}, false
	}
	n, e := removePurgedFile(bookID, v, root)
	return n, e, len(e) > 0
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
