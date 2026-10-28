// file: internal/versions/swap_target.go
// version: 1.0.0
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
//   - those files, and the leaving version's, must be movable as RunVersionSwap
//     will move them (see swapPreflight), so the common failure ends here, as a
//     refusal with nothing written, rather than half way through the moves;
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
	if bookPathIsFolder(book.FilePath) {
		return nil, refused(folderBookReason(book.FilePath))
	}
	bookDir := filepath.Dir(book.FilePath)

	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].IngestDate.Equal(cands[j].IngestDate) {
			return cands[i].IngestDate.After(cands[j].IngestDate)
		}
		return cands[i].ID < cands[j].ID
	})
	reasons := passedOver
	for i := range cands {
		v := cands[i]
		if why := swapPreflight(bookDir, fromID, byVersion[fromID], byVersion[v.ID]); why != "" {
			reasons = append(reasons, fmt.Sprintf("version %s: %s", v.ID, why))
			continue
		}
		return &v, nil
	}
	return nil, refused(reasons...)
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
// job, from what is on disk now, or "" when it should go through. It repeats the
// moves' own checks (movePreservingAttrs) without making them: a file is
// movable when it exists (or is already at its destination) and its
// destination is free.
//
// RunVersionSwap moves the leaving version's files into its slot first, so a
// root path they occupy is free for the entering version's files; that is
// allowed for here.
func swapPreflight(bookDir, fromID string, fromFiles, toFiles []database.BookFile) string {
	if len(toFiles) == 0 {
		return "it has no files"
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
		if dst != f.FilePath {
			vacated[f.FilePath] = struct{}{}
		}
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

// moveBlocker is movePreservingAttrs's decision without the move: "" when src
// can be renamed to dst (or is already there), else why not. A dst listed in
// vacated is about to be emptied by an earlier move of the same swap.
func moveBlocker(src, dst string, vacated map[string]struct{}) string {
	if src == dst {
		return ""
	}
	_, srcErr := os.Stat(src)
	_, dstErr := os.Stat(dst)
	if os.IsNotExist(srcErr) && dstErr == nil {
		return "" // already moved by an earlier run
	}
	if srcErr != nil {
		return fmt.Sprintf("file %s cannot be read (%v)", src, srcErr)
	}
	if dstErr == nil {
		if _, freed := vacated[dst]; !freed {
			return fmt.Sprintf("file %s cannot move to %s: something is already there", src, dst)
		}
	}
	return ""
}
