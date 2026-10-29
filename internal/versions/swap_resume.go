// file: internal/versions/swap_resume.go
// version: 1.1.0
// guid: df1395df-3cdf-4ee8-ab8b-92528f30360c
// last-edited: 2026-10-29
//
// Finding and resuming a version swap that did not finish.
//
// RunVersionSwap leaves a book's versions in swapping_out (the one leaving) and
// swapping_in (the one entering) when it cannot finish and cannot put things
// back (ErrSwapIncomplete), and the process dying mid-swap does the same. The
// book then has no active version. ListSwapIncompleteBooks finds those books and
// ResumeVersionSwap repairs one: it reads the version rows, the file rows and
// what is on disk, and either finishes the swap or undoes it.
//
// What it decides, and why it is safe:
//
//   - A move is never inferred from a name. Every file row is looked up at the
//     two places it can be (the book's folder, and its own version's slot) with
//     Lstat. It is at exactly one of them, or the book is refused: at both, at
//     neither, a symbolic link, or not a regular file is "I cannot tell", not a
//     guess. Where the two versions' files share a name (two .m4b files), the
//     one file at the book's folder is told apart by elimination (which slot
//     already holds the other version's file) and by its size, and a file that
//     another book's row names is never the swap's.
//   - A file is only moved or repointed to when it is proven to be the
//     version's. One found at the place its row does not name (the swap moved it
//     and stopped before the row followed, or somebody put a file of that name
//     there) must match the row's recorded size or hash; a row that recorded
//     neither cannot vouch for it, and the book is refused. A size or hash that
//     disagrees refuses in every case.
//   - An entering version left in swapping_in beside an active version (the
//     leaving version's restore succeeded, the entering one's did not) may share
//     file names with it: the active version's rows are known occupants of the
//     book's folder, so a file there under such a name is the active version's
//     and the entering version's file is the one in its slot.
//   - FINISH is chosen only when it needs no file move at all: every entering
//     file is already at the book's folder and every leaving file is in its slot.
//     Then it only repoints the rows, sets the book's path and writes the
//     statuses. In every other state the files are moved back to where the swap
//     found them (UNDO), entering files first, which frees the names the leaving
//     files return to. Both are no-replace moves (movePreservingAttrs), so a
//     path someone has taken since is an error and not a clobber.
//   - It changes nothing when the state is not one it recognizes: another
//     version of the book is already active (an ingest made one while the book
//     had none), a file row belongs to no version, the versions are not exactly
//     one swapping_out and one swapping_in (or one of them alone, see below), or
//     the disk disagrees with the rows. The refusal says why.
//   - A failed resume leaves a state the next resume reads from the disk again,
//     so it is safe to call as often as it takes. Nothing is remembered between
//     calls.
//
// The caller holds the merge lock, as the caller of RunVersionSwap does.

package versions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// swapResumeMoveChangeType is the BookPathChange.ChangeType of each file an
// undo moves back (the book-level record of the path ResumeVersionSwap sets
// uses "version_swap_resume").
const swapResumeMoveChangeType = "version_swap_resume_file"

// Resume actions: what ResumeVersionSwap did, or would do.
const (
	// SwapResumeFinish completes the swap: the entering version becomes active
	// and the leaving one alt.
	SwapResumeFinish = "finish"
	// SwapResumeUndo puts the files back and makes the leaving version active
	// again; the entering one is alt again.
	SwapResumeUndo = "undo"
)

// ErrSwapResumeRefused is what ResumeVersionSwap returns (wrapped, errors.Is),
// having changed nothing, when the state of the book is not one it can repair
// without guessing. The message says what it found. A refusal is a conflict with
// the library's state, not a fault: the caller answers it with a 409.
var ErrSwapResumeRefused = errors.New("the unfinished swap cannot be resumed safely")

// ErrNoSwapToResume is what ResumeVersionSwap returns (wrapped, errors.Is),
// having changed nothing, for a book none of whose versions is in
// swapping_out/swapping_in.
var ErrNoSwapToResume = errors.New("the book has no unfinished swap")

func resumeRefusal(bookID, format string, args ...any) error {
	return fmt.Errorf("resume the swap of book %s: %w: %s", bookID, ErrSwapResumeRefused, fmt.Sprintf(format, args...))
}

// SwapResumeResult is what a resume did.
type SwapResumeResult struct {
	// Action is SwapResumeFinish or SwapResumeUndo.
	Action string
	BookID string
	// LeavingVersionID and EnteringVersionID are the two ends of the swap; one
	// is empty when only the other was left in swapping_* (the swap had got as
	// far as the status writes).
	LeavingVersionID  string
	EnteringVersionID string
	// ActiveVersionID is the book's active version now.
	ActiveVersionID string
	// BookPath is the book's file_path now.
	BookPath string
	// Moved is the number of files this call moved.
	Moved int
	// From and To are the leaving and entering versions as written, set only
	// for a finished swap, for the Deluge follow: To whenever the entering
	// version is known, From only when the leaving one was left in swapping_out
	// too (a swap that got as far as the status writes can have only To).
	From, To *database.BookVersion
}

// swapItem is one file row of the leaving or entering version and where its
// file is.
type swapItem struct {
	row      database.BookFile
	leaving  bool
	ownerID  string
	root     string // where it is in the book's folder
	slot     string // where it is in its own version's slot
	atRoot   bool   // false: it is at slot
	presence os.FileInfo
	// rootTaken: an active version's row names root, so whatever file is there
	// is that version's, not this (entering) item's. Set by locateSwapItems.
	rootTaken bool
}

func (it *swapItem) here() string {
	if it.atRoot {
		return it.root
	}
	return it.slot
}

type swapMove struct{ src, dst string }

type swapRowFix struct {
	item *swapItem
	want string
}

type swapStatusStep struct {
	ver *database.BookVersion
	to  string
}

// swapResumePlan is everything ResumeVersionSwap would do, decided from the
// state as it is, before anything is written.
type swapResumePlan struct {
	action   string
	bookID   string
	book     *database.Book
	bookDir  string
	leaving  *database.BookVersion
	entering *database.BookVersion
	moves    []swapMove
	rows     []swapRowFix
	// bookPath is the path the book's file_path is to be set to, or "" to leave it.
	bookPath string
	statuses []swapStatusStep
	// active is the version that is active when the plan is done.
	active string
}

// planSwapResume reads the book's versions, file rows and disk and decides what
// to do. It writes nothing. The error is ErrNoSwapToResume, a refusal
// (ErrSwapResumeRefused), or a failed read.
func planSwapResume(store swapPlanStore, bookID string) (*swapResumePlan, error) {
	vers, err := store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return nil, fmt.Errorf("list versions of book %s: %w", bookID, err)
	}
	var leaving, entering *database.BookVersion
	nOut, nIn := 0, 0
	var actives []*database.BookVersion
	for i := range vers {
		v := &vers[i]
		switch v.Status {
		case database.BookVersionStatusSwappingOut:
			leaving = v
			nOut++
		case database.BookVersionStatusSwappingIn:
			entering = v
			nIn++
		case database.BookVersionStatusActive:
			actives = append(actives, v)
		}
	}
	if nOut+nIn == 0 {
		return nil, fmt.Errorf("book %s: %w", bookID, ErrNoSwapToResume)
	}
	if nOut > 1 || nIn > 1 {
		return nil, resumeRefusal(bookID, "%d versions are swapping_out and %d are swapping_in; one swap leaves one of each, so this is not a state to guess at", nOut, nIn)
	}

	book, err := store.GetBookByID(bookID)
	if err != nil {
		return nil, fmt.Errorf("load book %s: %w", bookID, err)
	}
	if book == nil {
		return nil, resumeRefusal(bookID, "the book does not exist")
	}
	bookDir, why := swapBookDir(book)
	if why != "" {
		return nil, resumeRefusal(bookID, "%s", why)
	}
	slotA, slotB := "", ""
	if leaving != nil {
		slotA = leaving.ID
	}
	if entering != nil {
		slotB = entering.ID
	}
	if slotA == "" {
		slotA = slotB
	}
	if slotB == "" {
		slotB = slotA
	}
	if why := slotBlocker(bookDir, slotA, slotB); why != "" {
		return nil, resumeRefusal(bookID, "%s", why)
	}

	rows, err := store.GetBookFiles(bookID)
	if err != nil {
		return nil, fmt.Errorf("list files of book %s: %w", bookID, err)
	}
	var leavingItems, enteringItems []*swapItem
	for _, r := range rows {
		if r.VersionID == "" {
			return nil, resumeRefusal(bookID, "file %s belongs to no version yet (the library has not been migrated to versions), so which version's file it is cannot be told", r.FilePath)
		}
		switch {
		case leaving != nil && r.VersionID == leaving.ID:
			leavingItems = append(leavingItems, &swapItem{row: r, leaving: true, ownerID: r.VersionID})
		case entering != nil && r.VersionID == entering.ID:
			enteringItems = append(enteringItems, &swapItem{row: r, ownerID: r.VersionID})
		}
	}
	if entering != nil && len(enteringItems) == 0 {
		return nil, resumeRefusal(bookID, "the entering version %s has no files", entering.ID)
	}

	// An entering version left beside one active version: the active version's
	// files own the book's folder, whatever names the entering files share
	// with them.
	var occupied map[string]struct{}
	if leaving == nil && len(actives) == 1 {
		occupied = make(map[string]struct{})
		for _, r := range rows {
			if r.VersionID == actives[0].ID {
				occupied[targetKey(r.FilePath)] = struct{}{}
			}
		}
	}

	items := append(append([]*swapItem{}, leavingItems...), enteringItems...)
	why, err = locateSwapItems(store, bookDir, items, occupied)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return nil, resumeRefusal(bookID, "%s", why)
	}

	p := &swapResumePlan{bookID: bookID, book: book, bookDir: bookDir, leaving: leaving, entering: entering}

	switch {
	case leaving != nil && entering != nil:
		if len(actives) > 0 {
			return nil, resumeRefusal(bookID, "version %s is active as well (a version made active while the swap was stuck); resuming would leave two active versions", actives[0].ID)
		}
		finishNeedsMoves := false
		for _, it := range leavingItems {
			if it.atRoot {
				finishNeedsMoves = true
			}
		}
		for _, it := range enteringItems {
			if !it.atRoot {
				finishNeedsMoves = true
			}
		}
		if !finishNeedsMoves {
			p.planFinish(enteringItems, leavingItems)
		} else {
			p.planUndo(enteringItems, leavingItems)
		}
	case entering != nil:
		// Only the entering version is left in swapping_in: the swap got as far
		// as the status writes. What the other versions are says which way.
		switch {
		case len(actives) == 0:
			for _, it := range enteringItems {
				if !it.atRoot {
					return nil, resumeRefusal(bookID, "version %s is swapping_in and the book has no active version, but its file %s is in its slot, not in the book's folder; whether the swap finished cannot be told", entering.ID, it.slot)
				}
			}
			p.planFinish(enteringItems, nil)
		case len(actives) == 1:
			for _, it := range enteringItems {
				if it.atRoot {
					return nil, resumeRefusal(bookID, "version %s is swapping_in and version %s is active, and its file %s is in the book's folder, where the active version's files belong; which of them the book plays cannot be told", entering.ID, actives[0].ID, it.root)
				}
			}
			p.planUndo(enteringItems, nil)
			p.active = actives[0].ID
		default:
			return nil, resumeRefusal(bookID, "%d versions are active beside the swapping_in version %s", len(actives), entering.ID)
		}
	default:
		// Only the leaving version is left in swapping_out.
		if len(actives) > 0 {
			return nil, resumeRefusal(bookID, "version %s is active beside the swapping_out version %s", actives[0].ID, leaving.ID)
		}
		for _, it := range leavingItems {
			if !it.atRoot {
				return nil, resumeRefusal(bookID, "version %s is swapping_out and the book has no active version, but its file %s is in its slot, not in the book's folder; whether the swap finished cannot be told", leaving.ID, it.slot)
			}
		}
		p.planUndo(nil, leavingItems)
	}
	return p, nil
}

// planFinish sets the plan to complete the swap. No file moves: the entering
// files are at the book's folder and the leaving ones in their slot.
func (p *swapResumePlan) planFinish(entering, leaving []*swapItem) {
	p.action = SwapResumeFinish
	for _, it := range leaving {
		p.rows = append(p.rows, swapRowFix{item: it, want: it.slot})
	}
	for _, it := range entering {
		p.rows = append(p.rows, swapRowFix{item: it, want: it.root})
	}
	p.bookPath = bookPathFor(p.book, entering)
	if p.leaving != nil {
		p.statuses = append(p.statuses, swapStatusStep{ver: p.leaving, to: database.BookVersionStatusAlt})
	}
	p.statuses = append(p.statuses, swapStatusStep{ver: p.entering, to: database.BookVersionStatusActive})
	p.active = p.entering.ID
}

// planUndo sets the plan to put the swap back: the entering files go back to
// their slot (first, which frees the names), then the leaving files to the
// book's folder.
func (p *swapResumePlan) planUndo(entering, leaving []*swapItem) {
	p.action = SwapResumeUndo
	for _, it := range entering {
		if it.atRoot {
			p.moves = append(p.moves, swapMove{src: it.root, dst: it.slot})
		}
		p.rows = append(p.rows, swapRowFix{item: it, want: it.slot})
	}
	for _, it := range leaving {
		if !it.atRoot {
			p.moves = append(p.moves, swapMove{src: it.slot, dst: it.root})
		}
		p.rows = append(p.rows, swapRowFix{item: it, want: it.root})
	}
	p.bookPath = bookPathFor(p.book, leaving)
	if p.entering != nil {
		p.statuses = append(p.statuses, swapStatusStep{ver: p.entering, to: database.BookVersionStatusAlt})
	}
	if p.leaving != nil {
		p.statuses = append(p.statuses, swapStatusStep{ver: p.leaving, to: database.BookVersionStatusActive})
		p.active = p.leaving.ID
	}
}

// bookPathFor is the path the book's file_path must name when the given
// version's files are at the book's folder: unchanged if it already names one of
// them, else the first. "" when the version has no files or nothing changes.
func bookPathFor(book *database.Book, items []*swapItem) string {
	if len(items) == 0 {
		return ""
	}
	cur := canonPath(book.FilePath)
	for _, it := range items {
		if canonPath(it.root) == cur {
			return ""
		}
	}
	return items[0].root
}

// locateSwapItems finds each item's file on disk, at its root path or its slot
// path, and sets item.atRoot. It returns why, or "" when every file is
// accounted for (see the file comment for the rules); the error is a lookup that
// could not be completed, which is a fault and not a refusal.
//
// occupied holds the targetKey of every path an active version's row names. An
// entering item whose root path is one of them is not at its root: that file is
// the active version's (it only matters when the entering and active versions
// share a file name), and the item must be in its slot.
func locateSwapItems(store swapPlanStore, bookDir string, items []*swapItem, occupied map[string]struct{}) (string, error) {
	canonDir := canonPath(bookDir)
	byBase := make(map[string][]*swapItem)
	var bases []string
	for _, it := range items {
		if !filepath.IsAbs(it.row.FilePath) {
			return fmt.Sprintf("file %q is not an absolute path", it.row.FilePath), nil
		}
		base := filepath.Base(it.row.FilePath)
		slotDir := VersionSlotDir(bookDir, it.ownerID)
		switch got := canonPath(filepath.Dir(it.row.FilePath)); got {
		case canonDir, canonPath(slotDir):
		default:
			return fmt.Sprintf("file %s is in %s, neither the book's folder %s nor the version's slot %s", it.row.FilePath, got, canonDir, slotDir), nil
		}
		it.root = filepath.Join(bookDir, base)
		it.slot = filepath.Join(slotDir, base)
		if _, taken := occupied[targetKey(it.root)]; taken && !it.leaving {
			it.rootTaken = true
		}
		if _, seen := byBase[base]; !seen {
			bases = append(bases, base)
		}
		byBase[base] = append(byBase[base], it)
	}
	sort.Strings(bases)

	type seen struct {
		exists bool
		info   os.FileInfo
	}
	look := func(path string) (seen, string) {
		fi, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return seen{}, ""
		case err != nil:
			return seen{}, fmt.Sprintf("%s cannot be checked (%v)", path, err)
		case fi.Mode()&os.ModeSymlink != 0:
			return seen{}, fmt.Sprintf("%s is a symbolic link, and moving a link leaves what it points at behind", path)
		case !fi.Mode().IsRegular():
			return seen{}, fmt.Sprintf("%s is not a regular file", path)
		}
		return seen{exists: true, info: fi}, ""
	}

	for _, base := range bases {
		group := byBase[base]
		switch len(group) {
		case 1:
			it := group[0]
			var r seen
			if !it.rootTaken {
				var why string
				if r, why = look(it.root); why != "" {
					return why, nil
				}
			}
			s, why := look(it.slot)
			if why != "" {
				return why, nil
			}
			switch {
			case r.exists && s.exists:
				return fmt.Sprintf("file %s is at both %s and %s; which is the version's cannot be told", base, it.root, it.slot), nil
			case !r.exists && !s.exists:
				return fmt.Sprintf("file %s is at neither %s nor %s: it is missing", base, it.root, it.slot), nil
			}
			it.atRoot = r.exists
			it.presence = r.info
			if s.exists {
				it.presence = s.info
			}
		case 2:
			var l, e *swapItem
			for _, it := range group {
				if it.leaving {
					l = it
				} else {
					e = it
				}
			}
			if l == nil || e == nil {
				return fmt.Sprintf("two files of one version are both named %s, so they would land on one path", base), nil
			}
			r, why := look(l.root)
			if why != "" {
				return why, nil
			}
			ls, why := look(l.slot)
			if why != "" {
				return why, nil
			}
			es, why := look(e.slot)
			if why != "" {
				return why, nil
			}
			// One name, one place in the book's folder: whichever of the two
			// versions' slots already holds its file says the file at the folder
			// is the other's.
			switch {
			case ls.exists && es.exists && !r.exists:
				l.atRoot, e.atRoot = false, false
				l.presence, e.presence = ls.info, es.info
			case ls.exists && !es.exists && r.exists:
				l.atRoot, e.atRoot = false, true
				l.presence, e.presence = ls.info, r.info
			case !ls.exists && es.exists && r.exists:
				l.atRoot, e.atRoot = true, false
				l.presence, e.presence = r.info, es.info
			default:
				return fmt.Sprintf("the leaving and entering versions both have a file named %s and the disk does not show where each is (in the book's folder: %t, leaving slot: %t, entering slot: %t)", base, r.exists, ls.exists, es.exists), nil
			}
		default:
			return fmt.Sprintf("%d files are named %s", len(group), base), nil
		}
	}

	own := make(map[string]struct{}, len(items))
	for _, o := range items {
		own[o.row.ID] = struct{}{}
	}
	asked := make(map[string]struct{})
	for _, it := range items {
		if why := it.vouchedFor(); why != "" {
			return why, nil
		}
		// Both places the file can be: where it is, and where an undo or a
		// finish puts it. A row of another file at either would end up sharing a
		// path with it. The active version's own file at a taken root is the one
		// row that is expected there.
		for _, path := range []string{it.root, it.slot} {
			if path == it.root && it.rootTaken {
				continue
			}
			for _, spelling := range pathSpellings(path) {
				if _, done := asked[spelling]; done {
					continue
				}
				asked[spelling] = struct{}{}
				named, err := store.BookFilesAtPath(spelling)
				if err != nil {
					return "", fmt.Errorf("check whether another row names %s: %w", spelling, err)
				}
				for _, n := range named {
					if _, ok := own[n.ID]; !ok {
						return fmt.Sprintf("%s is also named by another file row (%s, book %s), so the file at %s cannot be moved to or from it", spelling, n.ID, n.BookID, it.here()), nil
					}
				}
			}
		}
	}
	return "", nil
}

// vouchedFor is "" when the file found at it.here() may be taken for the row's,
// else why not. A size the row recorded must be the file's, and so must a hash
// it recorded; those are the checks that tell the file apart from another with
// the same name. A file at the path its row names is the row's by the row's
// word. One at the other place (the swap moved it and stopped before the row
// followed) is only the row's when the row recorded a size or a hash it matches:
// a row that recorded neither cannot vouch for a file that may be a stranger's,
// and moving or repointing it would hand that file to the version.
func (it *swapItem) vouchedFor() string {
	if it.row.FileSize > 0 && it.presence != nil && it.presence.Size() != it.row.FileSize {
		return fmt.Sprintf("the file at %s is %d bytes and its row says %d", it.here(), it.presence.Size(), it.row.FileSize)
	}
	if canonPath(filepath.Dir(it.row.FilePath)) == canonPath(filepath.Dir(it.here())) {
		return ""
	}
	if it.row.FileHash != "" {
		got, err := filehash.BookFileHash(it.here())
		if err != nil {
			return fmt.Sprintf("the file at %s cannot be hashed to check it is the version's (%v)", it.here(), err)
		}
		if got != it.row.FileHash {
			return fmt.Sprintf("the file at %s is not the one its row recorded (its hash is %s, the row's is %s)", it.here(), got, it.row.FileHash)
		}
		return ""
	}
	if it.row.FileSize > 0 {
		return ""
	}
	return fmt.Sprintf("the file at %s is not where its row (%s) names it and the row recorded neither a size nor a hash, so it cannot be told from another file of that name", it.here(), it.row.FilePath)
}

// ResumeVersionSwap finishes or undoes the swap of book bookID that did not
// finish, as planSwapResume decides, and returns what it did. It changes nothing
// and returns an error wrapping ErrNoSwapToResume or ErrSwapResumeRefused when
// there is nothing to resume or the state is not one it can repair without
// guessing. An error wrapping ErrSwapIncomplete means it started and could not
// finish; the book is still resumable and calling it again reads the disk anew.
//
// The caller holds the merge lock, as the caller of RunVersionSwap does.
func ResumeVersionSwap(ctx context.Context, store SwapResumeStore, bookID string) (*SwapResumeResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := planSwapResume(store, bookID)
	if err != nil {
		return nil, err
	}

	res := &SwapResumeResult{Action: p.action, BookID: bookID, ActiveVersionID: p.active, BookPath: p.book.FilePath}
	if p.leaving != nil {
		res.LeavingVersionID = p.leaving.ID
	}
	if p.entering != nil {
		res.EnteringVersionID = p.entering.ID
	}

	// Files first, as the swap does: the rows and statuses follow the disk.
	for _, m := range p.moves {
		// Journalled before the move, as the quarantine moves are: a move whose
		// record cannot be written is not made, and the book stays resumable.
		if err := store.RecordPathChange(&database.BookPathChange{
			BookID:     bookID,
			OldPath:    m.src,
			NewPath:    m.dst,
			ChangeType: swapResumeMoveChangeType,
		}); err != nil {
			return res, fmt.Errorf("%w: resume (%s): record the move of %s to %s (not moved): %w", ErrSwapIncomplete, p.action, m.src, m.dst, err)
		}
		if _, err := movePreservingAttrs(m.src, m.dst); err != nil {
			return res, fmt.Errorf("%w: resume (%s): move %s to %s: %w", ErrSwapIncomplete, p.action, m.src, m.dst, err)
		}
		res.Moved++
	}
	if p.action == SwapResumeUndo {
		if p.entering != nil {
			pruneEmptySlot(p.bookDir, p.entering.ID)
		}
		if p.leaving != nil {
			pruneEmptySlot(p.bookDir, p.leaving.ID)
		}
	}

	var rowErrs []error
	for _, f := range p.rows {
		if err := setRowPath(store, bookID, f.item.row.ID, f.item.row.FilePath, f.want); err != nil {
			rowErrs = append(rowErrs, fmt.Errorf("file %s: %w", f.item.row.ID, err))
		}
	}
	if len(rowErrs) > 0 {
		return res, fmt.Errorf("%w: resume (%s): %w", ErrSwapIncomplete, p.action, errors.Join(rowErrs...))
	}

	if p.bookPath != "" {
		oldPath := p.book.FilePath
		updated, err := store.ModifyBook(bookID, func(b *database.Book) error {
			if b.FilePath == p.bookPath {
				return database.ErrSkipBookWrite
			}
			b.FilePath = p.bookPath
			return nil
		})
		if err == nil && updated == nil {
			err = fmt.Errorf("book %s no longer exists", bookID)
		}
		if err != nil {
			return res, fmt.Errorf("%w: resume (%s): update book file_path: %w", ErrSwapIncomplete, p.action, err)
		}
		res.BookPath = p.bookPath
		// The audit record is not part of the repair: it is done, and failing it
		// now would leave a repaired book looking unrepaired.
		if err := store.RecordPathChange(&database.BookPathChange{
			BookID:     bookID,
			OldPath:    oldPath,
			NewPath:    p.bookPath,
			ChangeType: "version_swap_resume",
		}); err != nil {
			lifecycleLog.Warn("record version swap resume path change for book %s (%s -> %s): %v", bookID, oldPath, p.bookPath, err)
		}
	}

	for _, st := range p.statuses {
		st.ver.Status = st.to
		if err := store.UpdateBookVersion(st.ver); err != nil {
			return res, fmt.Errorf("%w: resume (%s): set version %s to %s: %w", ErrSwapIncomplete, p.action, st.ver.ID, st.to, err)
		}
	}
	if p.action == SwapResumeFinish {
		res.From, res.To = p.leaving, p.entering
	}
	lifecycleLog.Info("resumed the swap of book %s: %s (leaving %q, entering %q, %d file(s) moved, active version %s)",
		bookID, p.action, res.LeavingVersionID, res.EnteringVersionID, res.Moved, res.ActiveVersionID)
	return res, nil
}

// setRowPath sets a book_file row's path to want, under the row's write stripe
// and only if the row still names the path it was read with (or already names
// want): a row another writer repointed meanwhile is an error, not overwritten.
func setRowPath(store interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}, bookID, fileID, read, want string) error {
	row, err := store.ModifyBookFile(bookID, fileID, func(r *database.BookFile) error {
		switch r.FilePath {
		case want:
			return database.ErrSkipBookFileWrite
		case read:
			r.FilePath = want
			return nil
		}
		return fmt.Errorf("the row names %s, not the %s it was read with", r.FilePath, read)
	})
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("the row no longer exists")
	}
	return nil
}

// SwapIncompleteFile is one file row of a version in a swap that did not finish.
type SwapIncompleteFile struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// SwapIncompleteVersion is one version of a book whose swap did not finish, with
// the file rows it owns.
type SwapIncompleteVersion struct {
	ID     string               `json:"id"`
	Status string               `json:"status"`
	Files  []SwapIncompleteFile `json:"files"`
}

// SwapIncompleteBook is a book with a version left in swapping_out/swapping_in:
// it has no active version until the swap is resumed.
type SwapIncompleteBook struct {
	BookID string `json:"book_id"`
	Title  string `json:"title,omitempty"`
	// BookPath is the book's own file_path ("" for a book that cannot be read).
	BookPath string `json:"book_path"`
	// Versions are the book's versions that take part in the swap or are the
	// book's active or alt versions, the swapping ones first.
	Versions []SwapIncompleteVersion `json:"versions"`
	// Resume is what ResumeVersionSwap would do now (SwapResumeFinish or
	// SwapResumeUndo), or "" when it would refuse; ResumeRefusal then says why.
	Resume        string `json:"resume,omitempty"`
	ResumeRefusal string `json:"resume_refusal,omitempty"`
}

// ListSwapIncompleteBooks lists the books that have a version in
// swapping_out/swapping_in, ordered by book id, each with its path, its
// versions' file rows and what a resume would do (read-only: the plan is made
// and thrown away). A book that cannot be read is listed with the reason in
// ResumeRefusal; only a failure to list the versions at all is an error.
func ListSwapIncompleteBooks(store SwapIncompleteLister) ([]SwapIncompleteBook, error) {
	stuck, err := store.ListSwappingBookVersions()
	if err != nil {
		return nil, fmt.Errorf("list versions in a swap that did not finish: %w", err)
	}
	ids := make(map[string]struct{})
	for _, v := range stuck {
		ids[v.BookID] = struct{}{}
	}
	bookIDs := make([]string, 0, len(ids))
	for id := range ids {
		bookIDs = append(bookIDs, id)
	}
	sort.Strings(bookIDs)

	out := make([]SwapIncompleteBook, 0, len(bookIDs))
	for _, id := range bookIDs {
		entry := SwapIncompleteBook{BookID: id, Versions: []SwapIncompleteVersion{}}
		var problems []string
		if book, err := store.GetBookByID(id); err != nil {
			problems = append(problems, fmt.Sprintf("the book cannot be read: %v", err))
		} else if book != nil {
			entry.Title, entry.BookPath = book.Title, book.FilePath
		} else {
			problems = append(problems, "the book does not exist")
		}
		vers, verr := store.GetBookVersionsByBookID(id)
		files, ferr := store.GetBookFiles(id)
		if verr != nil {
			problems = append(problems, fmt.Sprintf("its versions cannot be read: %v", verr))
		}
		if ferr != nil {
			problems = append(problems, fmt.Sprintf("its files cannot be read: %v", ferr))
		}
		sort.SliceStable(vers, func(i, j int) bool {
			si, sj := isSwappingStatus(vers[i].Status), isSwappingStatus(vers[j].Status)
			if si != sj {
				return si
			}
			return vers[i].ID < vers[j].ID
		})
		for _, v := range vers {
			if !isSwappingStatus(v.Status) && v.Status != database.BookVersionStatusActive && v.Status != database.BookVersionStatusAlt {
				continue
			}
			ve := SwapIncompleteVersion{ID: v.ID, Status: v.Status, Files: []SwapIncompleteFile{}}
			for _, f := range files {
				if f.VersionID == v.ID {
					ve.Files = append(ve.Files, SwapIncompleteFile{ID: f.ID, Path: f.FilePath})
				}
			}
			entry.Versions = append(entry.Versions, ve)
		}
		switch {
		case len(problems) > 0:
			entry.ResumeRefusal = strings.Join(problems, "; ")
		default:
			plan, perr := planSwapResume(store, id)
			switch {
			case perr == nil:
				entry.Resume = plan.action
			case errors.Is(perr, ErrNoSwapToResume):
				// Resumed between the listing and the plan: not stuck any more.
				continue
			default:
				entry.ResumeRefusal = perr.Error()
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func isSwappingStatus(s string) bool {
	return s == database.BookVersionStatusSwappingIn || s == database.BookVersionStatusSwappingOut
}
