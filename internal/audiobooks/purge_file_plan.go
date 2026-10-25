// file: internal/audiobooks/purge_file_plan.go
// version: 1.0.0
// guid: 6f2c9b14-7a35-4d80-91e6-3b8d0c5a7e42
// last-edited: 2026-10-25

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
	// journal: the file is to be removed by the pass, and the bound preview
	// listed it, so its removal is journaled with the row delete. A path the
	// preview said stays is never journaled: a later replay has no preview to
	// consult, and must not remove what the owner was told would stay.
	journal bool
}

// errPathUnresolvable marks a path lookup that failed because the path itself
// cannot be resolved (ELOOP: a symlink loop, ENAMETOOLONG), as opposed to the
// filesystem failing to answer. It always accompanies errPathLookupFailed. Such a
// path is not going to resolve by trying again, so the pass does not hold a book
// in the trash for it and a journal record gives up after a few passes
// (maxUnresolvableAttempts).
var errPathUnresolvable = errors.New("path cannot be resolved")

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

// decideFile plans the removal of book's file ahead of the row delete. The
// outcome is non-nil only when the file cannot be decided (a lookup or a stat
// failed for a reason that may pass): the book then stays in the trash, counted
// as failed, and the next pass decides it again. Every other verdict (remove it,
// keep it, protected, already gone, an unresolvable path) lets the purge go on;
// the ones that keep the file are reported by purgeFinish.
func (svc *AudiobookService) decideFile(book *database.Book, env purgeEnv) (*filePlan, *purgeOutcome) {
	v := svc.planPurgeFile(book)
	if v.keptErr != nil && errors.Is(v.keptErr, errPathLookupFailed) && !errors.Is(v.keptErr, errPathUnresolvable) {
		return nil, &purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf(
			"%s: not purged, kept in the trash: cannot decide what to do with its file, so the next pass decides it again: %v", book.ID, v.keptErr)}}
	}
	return &filePlan{
		path:    book.FilePath,
		v:       v,
		journal: v.remove != "" && env.files != nil && env.files.preview.listsFile(v.remove),
	}, nil
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
