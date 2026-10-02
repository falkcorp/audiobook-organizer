// file: internal/database/book_trash.go
// version: 1.1.0
// guid: 9b303f90-f72d-46b2-8cb2-be9a988a748c
// last-edited: 2026-10-01

package database

import (
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Library states the trash/restore rule reads and writes.
const (
	trashLibraryState     = "deleted"
	organizedLibraryState = "organized"
	importedLibraryState  = "imported"
)

// RememberLibraryStateBeforeTrash records b's library state in
// PreTrashLibraryState so a later restore can put it back. Call it on the row
// inside the trash write, BEFORE overwriting LibraryState with "deleted".
//
// Only the two trash paths that relabel the state need it (DeleteAudiobook's
// soft delete and reconcile's soft delete). Every other trash path leaves
// LibraryState alone, so for those rows the current state already is the
// state before the trash.
//
// A row whose state is empty or already "deleted" records nothing and keeps
// any value already recorded: a row another trash path marked while it was
// still "organized", then relabelled here, records "organized"; a second
// relabel of an already-"deleted" row never records "deleted" over it.
func RememberLibraryStateBeforeTrash(b *Book) {
	if b == nil || b.LibraryState == nil {
		return
	}
	s := strings.TrimSpace(*b.LibraryState)
	if s == "" || strings.EqualFold(s, trashLibraryState) {
		return
	}
	b.PreTrashLibraryState = &s
}

// TrashRestoreEnv is what the restore rule needs to decide a library state
// for a row trashed before PreTrashLibraryState existed.
type TrashRestoreEnv struct {
	// RootDir is the library root. Empty means no row is decided organized.
	RootDir string
	// ITunesRoots are the protected iTunes roots (merge.ITunesProtectedRoots).
	// A file under one, or under the frozen books/itunes/ tree, is never
	// evidence of an organized book.
	ITunesRoots []string
	// ITunesRootsUnknown is set when the roots could not be resolved. The
	// fallback then fails closed to "imported": a book is only made visible
	// to ABS when it is provably outside the iTunes library.
	ITunesRootsUnknown bool
}

// RestoreLibraryStateFromTrash sets the library state a book returns to when
// it leaves the trash, and clears the recorded pre-trash state. It does not
// touch the trash bits; RestoreBookFromTrash does that for a user restore.
//
// The rule picks a candidate state:
//
//   - A state other than "deleted" or empty is kept: the trash path that put
//     the row there did not relabel it, so it is the state before the trash.
//   - Otherwise the recorded PreTrashLibraryState, when there is one.
//   - Otherwise (a row trashed before the state was recorded) "organized".
//
// and then never answers "organized" (which is what ABS lists) unless the
// book's present files prove it: at least one present file, every present
// file inside env.RootDir and outside the iTunes library. A candidate
// "organized" that fails that check, kept, recorded or legacy alike, becomes
// "imported". A combine's absorbed shell is the case that forced this
// (adversarial review of #3649, finding 2): the combine moves its files to the
// survivor and keeps its "organized" label, so restoring it from the trash
// brought back an empty "organized" book.
//
// files are the book's file rows; a row with Missing set is not present.
//
// Until 2026-10-01 RestoreAudiobook always wrote "imported", so restoring a
// book from the library folder dropped it out of ABS, which lists only
// "organized" rows; and the bulk restore and operation revert left the
// "deleted" label in place.
func RestoreLibraryStateFromTrash(b *Book, files []BookFile, env TrashRestoreEnv) {
	if b == nil {
		return
	}
	recorded := b.PreTrashLibraryState
	b.PreTrashLibraryState = nil
	state := organizedLibraryState // a legacy row with no record
	switch {
	case b.LibraryState != nil && isLiveLibraryState(*b.LibraryState):
		state = strings.TrimSpace(*b.LibraryState)
	case recorded != nil && isLiveLibraryState(*recorded):
		state = strings.TrimSpace(*recorded)
	}
	if strings.EqualFold(state, organizedLibraryState) && !presentFilesAreOrganized(files, env) {
		state = importedLibraryState
	}
	b.LibraryState = &state
}

// isLiveLibraryState reports whether s names a state other than the trash
// label (and is not empty).
func isLiveLibraryState(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.EqualFold(s, trashLibraryState)
}

// IsInTrash reports whether b is in the trash: its deletion flag is set, or a
// trash path labelled it "deleted". A nil or false flag with any other label
// is a live row.
func IsInTrash(b *Book) bool {
	return b != nil && (bookIsSoftDeleted(b) ||
		(b.LibraryState != nil && strings.EqualFold(strings.TrimSpace(*b.LibraryState), trashLibraryState)))
}

// RestoreBookFromTrash is the user-facing restore of one row: it clears the
// trash bits, restores the library state (RestoreLibraryStateFromTrash) and
// clears MergedIntoBookID, so a restored merge loser is a book of its own
// again instead of a row every merge-aware path keeps hiding. It reports
// whether the row was in the trash (IsInTrash).
//
// The bit is written as an explicit false, as every restore path always did.
// A row that is not in the trash is left exactly as it is and reported false:
// "restoring" a live row must not unlink a live merge loser, relabel it, or
// (in the callers) make it yield its primary flag. Until the review of #3649
// a live row still had its flag rewritten to false, and the callers yielded
// regardless, so a "restore" of a never-trashed primary demoted it.
//
// The sync-identity redirect a merge left on the row is not a book column;
// merge.RestoreFromTrash removes it alongside this.
func RestoreBookFromTrash(b *Book, files []BookFile, env TrashRestoreEnv) bool {
	if !IsInTrash(b) {
		return false
	}
	notMarked := false
	b.MarkedForDeletion = &notMarked
	b.MarkedForDeletionAt = nil
	b.MergedIntoBookID = nil
	RestoreLibraryStateFromTrash(b, files, env)
	return true
}

// presentFilesAreOrganized reports whether files has at least one present row
// and every present row is under env.RootDir and outside the iTunes library.
func presentFilesAreOrganized(files []BookFile, env TrashRestoreEnv) bool {
	if strings.TrimSpace(env.RootDir) == "" || env.ITunesRootsUnknown {
		return false
	}
	present := 0
	for i := range files {
		f := &files[i]
		if f.Missing {
			continue
		}
		present++
		p := strings.TrimSpace(f.FilePath)
		if p == "" || !pathutil.IsWithin(p, env.RootDir) || underITunesLibrary(p, env.ITunesRoots) {
			return false
		}
	}
	return present > 0
}

// underITunesLibrary reports whether p is in the frozen books/itunes/ tree or
// under one of the configured iTunes roots.
func underITunesLibrary(p string, roots []string) bool {
	if pathutil.UnderFrozenITunesTree(p) {
		return true
	}
	clean := filepath.Clean(p)
	for _, root := range roots {
		if pathutil.IsWithin(clean, root) {
			return true
		}
	}
	return false
}
