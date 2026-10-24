// file: internal/audiobooks/purge_files.go
// version: 1.0.0
// guid: e3a7c1d4-58b2-4f96-9d0e-6b4a2c8f1e57
// last-edited: 2026-10-24

package audiobooks

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

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
// window left out still owns its file.
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
	path    string
	isDir   bool
	bookIDs []string
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
		f = &pendingFile{path: v.remove, isDir: v.isDir}
		p.byPath[v.remove] = f
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
		errs = append(errs, fmt.Sprintf("%s: failed to delete file (tombstone preserved): %v", bookID, err))
		// DB record gone, file still exists, tombstone preserved for sweeper
	} else if err == nil {
		removed++
		removeEmptyParentsUpToRoot(v.remove, root)
	}
	return removed, errs
}

// errTrashedTwin names the trashed books that still have path as their file.
func errTrashedTwin(path string, ids []string) error {
	return fmt.Errorf("%s is still the path of trashed book(s) %v, which this pass did not purge", path, ids)
}

// otherTrashedRowAt returns a non-nil error when a book still in the trash
// names path. Fail closed: a trash that cannot be listed cannot vouch for a
// path. It is the single-book purge's check (DiscardProgressAndPurge), where no
// pass exists to wait for; the purged book's own row is already gone.
func (svc *AudiobookService) otherTrashedRowAt(path string) error {
	trash, err := svc.store.ListSoftDeletedBooks(purgeListLimit, 0, nil)
	if err != nil {
		return fmt.Errorf("cannot verify %s is not a trashed book's file: listing the trash: %w", path, err)
	}
	if ids := trashedIDsByPath(trash, map[string]struct{}{path: {}}, nil)[path]; len(ids) > 0 {
		return errTrashedTwin(path, ids)
	}
	return nil
}

// removeDeferredFiles is the second half of a purge pass's file removal, run
// once every book has been decided. For each path a purged book named it
// checks, as the pass left the store:
//
//   - planPurgeFile again (protected path, library root, a book_file row or a
//     live book naming it, still there);
//   - no book still in the trash names it: the pass purged some books and kept
//     others, and a kept one's file is its own;
//   - when the pass is bound to a preview, that the preview listed the path
//     (a file the preview said stays is never removed, whatever changed since).
//
// It reports how many things it removed and the error lines.
func (svc *AudiobookService) removeDeferredFiles(files *pendingFiles, preview *purgePreviewRecord) (removed int, errs []string) {
	pending := files.sorted()
	if len(pending) == 0 {
		return 0, nil
	}
	root := config.Snapshot().RootDir
	paths := make(map[string]struct{}, len(pending))
	for _, f := range pending {
		paths[f.path] = struct{}{}
	}
	trash, listErr := svc.store.ListSoftDeletedBooks(purgeListLimit, 0, nil)
	var twins map[string][]string
	if listErr == nil {
		twins = trashedIDsByPath(trash, paths, nil)
	}
	for _, f := range pending {
		bookID := f.bookIDs[0]
		v := svc.planPurgeFile(&database.Book{ID: bookID, FilePath: f.path})
		switch {
		case v.protected:
			singleLog.Debug("purge skipping file deletion: protected path %s (book %s)",
				logger.SanitizeLogValue(f.path), logger.SanitizeLogValue(bookID))
			continue
		case v.keptErr != nil:
			errs = append(errs, fmt.Sprintf("%s: book purged, file kept: %v", bookID, v.keptErr))
			continue
		case v.remove == "":
			// Already gone, or a directory that is not empty.
			continue
		}
		switch {
		case listErr != nil:
			errs = append(errs, fmt.Sprintf("%s: book purged, file kept: cannot verify %s is not a trashed book's file: listing the trash: %v", bookID, f.path, listErr))
			continue
		case len(twins[f.path]) > 0:
			errs = append(errs, fmt.Sprintf("%s: book purged, file kept: %v", bookID, errTrashedTwin(f.path, twins[f.path])))
			continue
		case !preview.listsFile(f.path):
			errs = append(errs, fmt.Sprintf("%s: book purged, file kept: the preview this pass is bound to did not list %s for removal", bookID, f.path))
			continue
		}
		n, e := removePurgedFile(bookID, v, root)
		removed += n
		errs = append(errs, e...)
	}
	return removed, errs
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
	bound     time.Time
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
