// file: internal/audiobooks/purge_file_plan.go
// version: 1.6.0
// guid: 6f2c9b14-7a35-4d80-91e6-3b8d0c5a7e42
// last-edited: 2026-10-26

package audiobooks

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// filePlan is what a purge with delete_files decided about one book's file
// BEFORE the book's row was deleted (decideFile), carried to the row delete
// (purgeDeleteRow journals it there) and to purgeFinish (which hands it to the
// pass's removal). A nil plan means "nothing was decided ahead": the caller
// plans when it needs to.
type filePlan struct {
	// path is the FilePath the decision was made for. A row whose path is
	// different by the time it is deleted was not decided (stillFor).
	path string
	v    purgeFileVerdict
	// journal: the file is to be removed, and the removal is journaled with the
	// row delete. For a pass that is when the bound preview listed it: a path the
	// preview said stays is never journaled, because a later replay has no
	// preview to consult and must not remove what the owner was told would stay.
	// A single-book purge (no pass, no preview: "Purge now", discard progress and
	// purge) is the owner's explicit act on that one book and is always journaled.
	journal bool
	// root is the pass's (or the single-book action's) library-root guard, so the
	// removal that follows the row delete reuses the check the decision made
	// instead of sampling the library again (see preLockRoot).
	root *rootGuard
}

// errPathUnresolvable marks a path lookup that failed because the path itself
// cannot be resolved (ELOOP: a symlink loop, ENAMETOOLONG), as opposed to the
// filesystem failing to answer. It always accompanies errPathLookupFailed. Such a
// path is not going to resolve by trying again, so the pass does not hold a book
// in the trash for it and a journal record gives up after a few passes
// (maxUnresolvableAttempts).
var errPathUnresolvable = errors.New("path cannot be resolved")

// errPathFolderMissing marks a file that is not there while the folder it would
// be in is not there either, or is there but empty (the mount point a dataset
// that is not mounted leaves behind; folderCheck says what exempts a folder).
// That is what a file that was removed together with its folder looks like, and
// also what a file on a library that is not mounted looks like (nested datasets
// mount apart from the root, so the root can be populated and a folder under it
// still be absent). It cannot be told apart per path, so it is "cannot tell": it
// always accompanies errPathLookupFailed, a book stays in the trash for it
// (fileUndecided), and a journal record is kept for as long as the folder is
// missing (no cap: the record is the only thing that still names the file) and
// reported as one aggregate line per pass.
var errPathFolderMissing = errors.New("folder of the path is missing")

// maxUnresolvableAttempts is how many passes a journal record whose path cannot
// be resolved is retried before it is cleared with an error line.
const maxUnresolvableAttempts = 3

// pathIsGone reports whether err from a stat or read means there is nothing at
// the path: ENOENT, or ENOTDIR (a parent is not a directory, so the path cannot
// exist, which is how the purge always read it).
func pathIsGone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// statLookupError is the keptErr of a stat or read that failed other than as
// "gone". It is a failed lookup (errPathLookupFailed), and also
// errPathUnresolvable when the path itself is the problem.
func statLookupError(path, op string, err error) error {
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENAMETOOLONG) {
		return fmt.Errorf("cannot verify %s is gone or removable: %s: %w: %w: %w", path, op, errPathLookupFailed, errPathUnresolvable, err)
	}
	return fmt.Errorf("cannot verify %s is gone or removable: %s: %w: %w", path, op, errPathLookupFailed, err)
}

// fileUndecided is the outcome of a book whose file cannot be decided: a lookup
// or a stat failed for a reason that may pass (v.keptErr is errPathLookupFailed
// and not errPathUnresolvable), so the book stays in the trash, counted as
// failed. nil when the file is decided (remove it, keep it, protected, already
// gone, an unresolvable path). The preview asks it too (previewPurged), so a
// preview and the run that follows it count the book the same way.
//
// There is no attempt cap, on purpose. A cap would have to purge the book
// without a decision about its file, which is exactly the file left with no
// row and no record that deciding first exists to prevent; and the book is
// restorable, hidden and harmless in the trash. What a persistent failure (a
// directory the process cannot read: EACCES, a dying disk: EIO, a stale
// handle: ESTALE) does need is to be reported as persistent, so the line says
// it repeats on every delete_files pass and names the way out.
func fileUndecided(book *database.Book, v purgeFileVerdict) *purgeOutcome {
	if v.keptErr == nil || !errors.Is(v.keptErr, errPathLookupFailed) || errors.Is(v.keptErr, errPathUnresolvable) {
		return nil
	}
	return &purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf(
		"%s: not purged, kept in the trash: cannot decide what to do with its file (%v); every purge with \"also delete files\" asks again and keeps it until the path can be read, and a purge without that option is not held up by it", book.ID, v.keptErr)}}
}

// decideFile plans the removal of book's file ahead of the row delete. The
// outcome is non-nil only when the file cannot be decided (fileUndecided), or
// the library root is not ready (libraryRootReady): the book then stays in the
// trash, counted as failed, and the next pass decides it again. Every other
// verdict (remove it, keep it, protected, already gone, an unresolvable path)
// lets the purge go on; the ones that keep the file are reported by purgeFinish.
//
// The root check is the pass's guard (env.root: checked at the pass's start and
// again at most every rootGuardTTL), and a fresh one per book for the
// single-book paths that run no pass.
func (svc *AudiobookService) decideFile(book *database.Book, env purgeEnv) (*filePlan, *purgeOutcome) {
	if err := svc.rootCheck(env); err != nil {
		// A root that stopped being ready is the same for every remaining book:
		// the pass stops with it as the reason (purgeOutcome.abort) instead of
		// asking the library again for each one.
		return nil, &purgeOutcome{kind: purgeFailed, abort: purgeAbortOf(err), errs: []string{fmt.Sprintf("%s: not purged, kept in the trash: %v", book.ID, err)}}
	}
	v := svc.planPurgeFile(book)
	if o := fileUndecided(book, v); o != nil {
		return nil, o
	}
	return &filePlan{
		path:    book.FilePath,
		v:       v,
		journal: v.remove != "" && (env.files == nil || env.files.preview.listsFile(v.remove)),
		root:    env.root,
	}, nil
}

// undecidedOutcome is what a book whose file could not be decided (decideFile)
// is counted as. Usually that outcome itself (failed, kept in the trash). A book
// that holds listening state no live version can take is kept for THAT reason
// whatever its file is doing, and the preview says so (previewOne checks the
// state before it plans the file): the file check runs first only so that it
// precedes the delete, so when it fails the reason the book stays is still the
// state's. The state check here is lock-free and only reclassifies; the
// authoritative one still runs under the merge lock (purgeIfStateless) for every
// book that gets that far, and a failed read here changes nothing.
func (svc *AudiobookService) undecidedOutcome(book *database.Book, env purgeEnv, undecided purgeOutcome) purgeOutcome {
	if undecided.abort != nil || env.probe == nil {
		return undecided
	}
	has, err := env.probe.Has(book.ID)
	if err != nil || !has {
		return undecided
	}
	if sibling, serr := svc.liveSiblingFor(book); serr == nil && sibling == "" {
		return purgeOutcome{kind: purgeKeptHasProgress}
	}
	return undecided
}

// isFor reports whether the plan was made for book's path.
func (p *filePlan) isFor(book *database.Book) bool {
	return p != nil && p.path == book.FilePath
}

// stillFor refuses a row whose path is not the one the file was decided for (it
// was edited since the pass listed it): nothing was decided about that file, so
// the book stays in the trash for the next pass. nil plan: nothing to check.
func (p *filePlan) stillFor(cur *database.Book) error {
	if p == nil || p.path == cur.FilePath {
		return nil
	}
	return fmt.Errorf("not purged: its file path changed while the pass ran (%q, was %q), so the file was not decided; the next pass decides it", cur.FilePath, p.path)
}
