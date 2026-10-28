// file: internal/versions/swap_target.go
// version: 1.2.0
// guid: 0e5b7a4c-83d1-4f6a-9c2e-7d1a5b3f8e64
// last-edited: 2026-10-28

package versions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrNoSwapTarget is what PickSwapTarget returns, having changed nothing, when
// no other version of the book can take over as its active version. Callers
// branch on it with errors.Is; the concrete *NoSwapTargetError says why.
var ErrNoSwapTarget = errors.New("no other version can take over as the active version")

// NoSwapTargetError is the reason no swap target exists: one line per version
// (or per book-wide obstacle) that was passed over.
type NoSwapTargetError struct {
	BookID        string
	FromVersionID string
	// Reasons names each version that was passed over and why, or the
	// book-wide obstacle (an unfinished swap, files that no version owns).
	Reasons []string
}

// Why is the reasons as one sentence fragment a person can read.
func (e *NoSwapTargetError) Why() string {
	if len(e.Reasons) == 0 {
		return "the book has no other version"
	}
	return strings.Join(e.Reasons, "; ")
}

func (e *NoSwapTargetError) Error() string {
	return fmt.Sprintf("%v for book %s (version %s is its active version): %s",
		ErrNoSwapTarget, e.BookID, e.FromVersionID, e.Why())
}

func (e *NoSwapTargetError) Is(target error) bool { return target == ErrNoSwapTarget }

// swapTargetStore is what PickSwapTarget reads: the book's versions, its file
// rows and the book itself (its folder is where a swapped-in version's files
// land).
type swapTargetStore interface {
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookByID(id string) (*database.Book, error)
}

// PickSwapTarget chooses the version that takes over as the active version of
// bookID when its active version fromID is about to leave (be trashed). It
// changes nothing. The rule, which is what makes the choice deterministic:
//
//   - only a version of status alt is a candidate. That excludes every other
//     status by itself: trash, purging, purge_failed, inactive_purged, retired
//     (its files belong to another book), pending, blocked_for_redownload and
//     swapping_*;
//   - it must own files: at least one book_file row whose VersionID is the
//     version's own, because a swap that moves no file leaves the book serving
//     the files of the version that is leaving;
//   - those files, and the leaving version's, must be where RunVersionSwap
//     assumes them and movable as it will move them (see swapPreflight): the
//     leaving version's in the book's own folder, the entering version's in its
//     own .versions/{id}/ slot and no file of either the same file as one of the
//     other, so a failure ends here, as a refusal with nothing written, rather
//     than half way through the moves;
//   - of the candidates left, the one most recently ingested wins; equal
//     IngestDates go to the smaller version ID.
//
// It refuses (*NoSwapTargetError, errors.Is ErrNoSwapTarget) outright, whatever
// the candidates, while a swap of the book is unfinished (a version in
// swapping_in/swapping_out) or a file row of the book names no version
// (VersionID empty, a library not yet migrated): RunVersionSwap folds such rows
// into BOTH versions' file sets and would move the same file twice.
func PickSwapTarget(store swapTargetStore, bookID, fromID string) (*database.BookVersion, error) {
	all, err := store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return nil, fmt.Errorf("list versions of book %s: %w", bookID, err)
	}
	refused := func(reasons ...string) error {
		return &NoSwapTargetError{BookID: bookID, FromVersionID: fromID, Reasons: reasons}
	}

	var cands []database.BookVersion
	var passedOver []string
	for _, v := range all {
		if v.ID == fromID {
			continue
		}
		switch v.Status {
		case database.BookVersionStatusSwappingIn, database.BookVersionStatusSwappingOut:
			return nil, refused(fmt.Sprintf("an earlier swap of this book did not finish (version %s is %s)", v.ID, v.Status))
		case database.BookVersionStatusAlt:
			cands = append(cands, v)
		default:
			passedOver = append(passedOver, fmt.Sprintf("version %s is %s", v.ID, v.Status))
		}
	}
	sort.Slice(passedOver, func(i, j int) bool { return passedOver[i] < passedOver[j] })

	if len(cands) == 0 {
		return nil, refused(passedOver...)
	}

	rows, err := store.GetBookFiles(bookID)
	if err != nil {
		return nil, fmt.Errorf("list files of book %s: %w", bookID, err)
	}
	byVersion := make(map[string][]database.BookFile)
	for _, f := range rows {
		if f.VersionID == "" {
			return nil, refused(fmt.Sprintf("file %s belongs to no version yet (the library has not been migrated to versions)", f.FilePath))
		}
		byVersion[f.VersionID] = append(byVersion[f.VersionID], f)
	}

	book, err := store.GetBookByID(bookID)
	if err != nil {
		return nil, fmt.Errorf("load book %s: %w", bookID, err)
	}
	if book == nil {
		return nil, fmt.Errorf("book %s not found", bookID)
	}
	bookDir, why := swapBookDir(book)
	if why != "" {
		return nil, refused(why)
	}

	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].IngestDate.Equal(cands[j].IngestDate) {
			return cands[i].IngestDate.After(cands[j].IngestDate)
		}
		return cands[i].ID < cands[j].ID
	})
	reasons := passedOver
	for i := range cands {
		v := cands[i]
		if why := swapPreflight(bookDir, fromID, v.ID, byVersion[fromID], byVersion[v.ID]); why != "" {
			reasons = append(reasons, fmt.Sprintf("version %s: %s", v.ID, why))
			continue
		}
		return &v, nil
	}
	return nil, refused(reasons...)
}

// swapBookDir is the folder RunVersionSwap works in, the parent of the book's
// FilePath, or why it cannot be trusted: a FilePath that is empty (its parent
// would be "." and the swap would move files into the process's working
// directory), not absolute, one that ends in a separator (it names a folder, not
// a file, and filepath.Dir of it is the path itself, which would make the
// file's own name the "book folder"), or a folder (see bookPathIsFolder).
func swapBookDir(book *database.Book) (dir, why string) {
	switch {
	case book.FilePath == "":
		return "", "the book has no file path, so its folder is unknown"
	case strings.HasSuffix(book.FilePath, "/") || strings.HasSuffix(book.FilePath, string(filepath.Separator)):
		return "", fmt.Sprintf("the book's path %q ends in a path separator, so it is not a file", book.FilePath)
	case !filepath.IsAbs(book.FilePath):
		return "", fmt.Sprintf("the book's path %q is not absolute, so its folder is unknown", book.FilePath)
	case bookPathIsFolder(book.FilePath):
		return "", folderBookReason(book.FilePath)
	}
	return filepath.Dir(book.FilePath), ""
}

// bookPathIsFolder reports whether the book's own path is a folder. A multi-file
// book organized in place keeps its folder in Book.FilePath (organizer
// inplace_multifile), while the version machinery (RunVersionSwap, the purge, the
// Deluge follow) takes the folder of the book's FilePath to be its parent: for a
// folder that is the author's folder, where a swap would move the entering
// version's files in among other books' and rewrite FilePath to a single file.
// Such a book is refused until the swap knows the layout.
func bookPathIsFolder(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func folderBookReason(path string) string {
	return fmt.Sprintf("the book's path %s is a folder (a multi-file book); swapping versions of such a book is not supported", path)
}

// swapPreflight reports why RunVersionSwap(from -> to) would fail or not do its
// job, from what is on disk now, or "" when it should go through. PickSwapTarget
// asks it of every candidate and RunVersionSwap asks it again before its first
// write, so what the picker approved is what the swap will see.
//
// It checks the layout the swap assumes (swapLayoutBlocker), the folders the
// files move through (slotBlocker) and then the moves' own conditions
// (movePreservingAttrs) without making them: a file is movable when it EXISTS
// and its destination is FREE. There is no third case. A source that is missing
// while a file of the same name sits at the destination is not "already moved":
// it is another book's file (or a leftover) that the swap would adopt, switch
// the book to, and the purge would then delete the real audio behind it.
//
// RunVersionSwap moves the leaving version's files into its slot first, so a
// root path they occupy is free for the entering version's files; that is
// allowed for here.
func swapPreflight(bookDir, fromID, toID string, fromFiles, toFiles []database.BookFile) string {
	if len(toFiles) == 0 {
		return "it has no files"
	}
	if why := swapLayoutBlocker(bookDir, fromID, toID, fromFiles, toFiles); why != "" {
		return why
	}
	if why := slotBlocker(bookDir, fromID, toID); why != "" {
		return why
	}
	vacated := make(map[string]struct{})
	seen := make(map[string]string)
	for _, f := range fromFiles {
		dst := filepath.Join(VersionSlotDir(bookDir, fromID), filepath.Base(f.FilePath))
		if prev, dup := seen[dst]; dup {
			return fmt.Sprintf("the active version's files %s and %s would both land at %s", prev, f.FilePath, dst)
		}
		seen[dst] = f.FilePath
		if why := moveBlocker(f.FilePath, dst, nil); why != "" {
			return "the active version's " + why
		}
		vacated[f.FilePath] = struct{}{}
	}
	seen = make(map[string]string)
	for _, f := range toFiles {
		dst := filepath.Join(bookDir, filepath.Base(f.FilePath))
		if prev, dup := seen[dst]; dup {
			return fmt.Sprintf("its files %s and %s would both land at %s", prev, f.FilePath, dst)
		}
		seen[dst] = f.FilePath
		if why := moveBlocker(f.FilePath, dst, vacated); why != "" {
			return "its " + why
		}
	}
	return ""
}

// slotBlocker asks of the two folders the swap moves through whether they are
// what the swap takes them to be. `.versions` and both slots (.versions/{id})
// must each be absent or a real directory, not a link and not a file, whose
// canonical path is the canonical book folder joined with .versions/{id}. The
// swap creates the leaving version's slot with MkdirAll, which follows links,
// and moves files through both: a slot that is a symlink out, or a .versions
// that is a symlink to another book's, would put the leaving version's files in
// that other folder, where the purge's slot checks do not look.
func slotBlocker(bookDir, fromID, toID string) string {
	canonDir := canonPath(bookDir)
	versionsDir := VersionsDir(bookDir)
	if why := plainDirOrAbsent(versionsDir, filepath.Join(canonDir, VersionsDirName)); why != "" {
		return why
	}
	for _, id := range []string{fromID, toID} {
		if why := plainDirOrAbsent(VersionSlotDir(bookDir, id), filepath.Join(canonDir, VersionsDirName, id)); why != "" {
			return why
		}
	}
	return ""
}

// plainDirOrAbsent is "" when path does not exist or is a real directory whose
// canonical path is wantCanon, else why not.
func plainDirOrAbsent(path, wantCanon string) string {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ""
	case err != nil:
		return fmt.Sprintf("the folder %s cannot be checked (%v)", path, err)
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Sprintf("the folder %s is a symbolic link, not a real folder of this book", path)
	case !fi.IsDir():
		return fmt.Sprintf("%s is a file, not a folder", path)
	}
	if got := canonPath(path); got != wantCanon {
		return fmt.Sprintf("the folder %s resolves to %s, not %s", path, got, wantCanon)
	}
	return ""
}

// swapLayoutBlocker is the part of the preflight that asks whether the files
// are where RunVersionSwap assumes they are, which is what its moves rest on.
// Without it the swap trusts rows it never looked at:
//
//   - the leaving version's files must be in the book's own folder, the parent
//     of Book.FilePath. A FilePath gone stale (the book moved, the row did not)
//     sends the entering files to the wrong folder and leaves FilePath pointing
//     at nothing;
//   - the entering version's files must be in ITS slot, .versions/{toID}/, in the
//     form stored and with symlinks resolved. A file anywhere else (a download
//     folder, another book's folder) is not the swap's to take, and the purge's
//     served-file guard keys on the slot as well;
//   - no file of the entering version may be the same file as one of the leaving
//     version's: the same path, the same path once symlinks are resolved, or the
//     same inode (a hard link, a symlink). The swap would move that file into
//     the leaving version's slot, treat the move to the root as already done,
//     and leave FilePath at a path that no longer exists;
//   - neither side may be a symbolic link itself: moving a link leaves its
//     target behind, and a relative link breaks when it moves.
//
// The checks are on paths, parents and Lstat, never on whether a file exists.
func swapLayoutBlocker(bookDir, fromID, toID string, fromFiles, toFiles []database.BookFile) string {
	canonDir := canonPath(bookDir)
	for _, f := range fromFiles {
		if !filepath.IsAbs(f.FilePath) {
			return fmt.Sprintf("book path is stale: the active version's file %q is not an absolute path", f.FilePath)
		}
		if got := canonPath(filepath.Dir(f.FilePath)); got != canonDir {
			return fmt.Sprintf("book path is stale: the active version's file %s is in %s, not in the book's folder %s", f.FilePath, got, canonDir)
		}
		if isSymlink(f.FilePath) {
			return fmt.Sprintf("the active version's file %s is a symbolic link", f.FilePath)
		}
	}
	toSlot := VersionSlotDir(bookDir, toID)
	canonSlot := canonPath(toSlot)
	for _, f := range toFiles {
		if !filepath.IsAbs(f.FilePath) {
			return fmt.Sprintf("its file %q is not an absolute path", f.FilePath)
		}
		for _, from := range fromFiles {
			if sameFile(f.FilePath, from.FilePath) {
				return fmt.Sprintf("its file %s is the same file as the active version's file %s", f.FilePath, from.FilePath)
			}
		}
		if isSymlink(f.FilePath) {
			return fmt.Sprintf("its file %s is a symbolic link", f.FilePath)
		}
		if filepath.Dir(f.FilePath) != filepath.Clean(toSlot) || canonPath(filepath.Dir(f.FilePath)) != canonSlot || !inVersionSlot(f.FilePath, toID) {
			return fmt.Sprintf("its file %s is not inside its own slot %s (a swap only takes the files of a version that are in its slot)", f.FilePath, toSlot)
		}
	}
	return ""
}

// isSymlink reports whether p itself, not what it points at, is a symbolic link.
func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// sameFile reports whether a and b are one file: the same path, the same path
// with symlinks resolved, or (both readable) the same inode, which is what a
// hard link or a symlink to the other is.
func sameFile(a, b string) bool {
	if a == b || canonPath(a) == canonPath(b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// moveBlocker is movePreservingAttrs's decision without the move: "" when src
// is there and dst is free, else why not. Both are Lstat, so a dangling link is a
// file that is there. A dst listed in vacated is about to be emptied by an
// earlier move of the same swap.
func moveBlocker(src, dst string, vacated map[string]struct{}) string {
	if src == dst {
		return fmt.Sprintf("file %s would be moved onto itself", src)
	}
	if _, err := os.Lstat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Sprintf("file %s is missing, so there is nothing to move (cannot be read: %v)", src, err)
		}
		return fmt.Sprintf("file %s cannot be read (%v)", src, err)
	}
	_, dstErr := os.Lstat(dst)
	switch {
	case dstErr == nil:
		if _, freed := vacated[dst]; !freed {
			return fmt.Sprintf("file %s cannot move to %s: something is already there", src, dst)
		}
	case !errors.Is(dstErr, os.ErrNotExist):
		return fmt.Sprintf("file %s cannot move to %s: the destination cannot be checked (%v)", src, dst, dstErr)
	}
	return ""
}
