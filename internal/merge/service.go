// file: internal/merge/service.go
// version: 1.30.0
// guid: 7d736d2d-e0df-40bd-9f4b-0a07bc2eb6ae
// last-edited: 2026-09-19

package merge

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	ulid "github.com/oklog/ulid/v2"
)

// WriteBackEnqueuer is satisfied by anything that can enqueue an iTunes
// track removal (e.g. *server.WriteBackBatcher).
type WriteBackEnqueuer interface {
	EnqueueRemove(pid string)
}

// ExternalIDReassigner is the subset of external-ID operations that
// Service needs. Satisfied by the concrete store when it implements
// ReassignExternalIDs.
type ExternalIDReassigner interface {
	ReassignExternalIDs(oldBookID, newBookID string) error
}

// AsExternalIDReassigner returns the ExternalIDReassigner if the given
// store, or any store it wraps, implements it, or nil otherwise.
//
// It resolves through database.AsCapability rather than a bare type
// assertion so a decorator that embeds a NARROW interface and exposes the
// full store only through Unwrap (registry.prodSchedulerStore is one) does
// not hide the capability. Both MergeBooks call sites treat nil as "backend
// has no external IDs", so a bare assertion failing through such a wrapper
// would let a merge succeed while leaving the loser's iTunes PID/ASIN
// mappings behind. An opaque decorator (no Unwrap) still resolves to nil.
func AsExternalIDReassigner(s any) ExternalIDReassigner {
	if s == nil {
		return nil
	}
	if eid, ok := database.AsCapability[ExternalIDReassigner](s); ok {
		return eid
	}
	return nil
}

// Service handles merging duplicate books into version groups.
//
// MergeBooks and CombineBooks serialize their read-modify-writes on the
// package-level mergeSerializeMu (see serialize.go), shared with the sibling
// dedup.MergeBooks path so any two merges are mutually exclusive on a shared
// book row.
type Service struct {
	db               Store
	writeBackBatcher WriteBackEnqueuer
	syncFollower     SyncFollower
}

// SetWriteBackBatcher sets the iTunes write-back batcher.
func (ms *Service) SetWriteBackBatcher(b WriteBackEnqueuer) {
	ms.writeBackBatcher = b
}

// SetSyncFollower overrides the sync-identity follower wired by NewService.
//
// Why a settable field instead of a type assertion at the call site: tests (and
// some production wiring) hand this Service a wrapper that EMBEDS
// database.Store — e.g. serializeProbe in service_concurrent_test.go — and a
// database.AsSyncIdentityStore assertion through such a wrapper returns nil,
// which would silently no-op the whole hook without failing anything. Injecting
// once, mirroring SetWriteBackBatcher, makes that failure mode explicit.
func (ms *Service) SetSyncFollower(f SyncFollower) {
	ms.syncFollower = f
}

// Result contains the outcome of a merge operation.
type Result struct {
	PrimaryID      string `json:"primary_id"`
	VersionGroupID string `json:"version_group_id"`
	// MergedCount is the number of participant books in the merge, primary
	// INCLUDED. It is not the number of records destroyed — a caller reporting
	// "how many records were soft-deleted" must use SoftDeleted, not this.
	MergedCount int `json:"merged_count"`
	// SoftDeleted is the number of loser records this call actually soft-deleted
	// — the true blast radius. It excludes the primary and any loser that was
	// already soft-deleted (whose cleanup is skipped) or left live by a
	// per-loser error. On a fully successful clean-group merge it is
	// len(participants)-1.
	SoftDeleted int `json:"soft_deleted"`
}

// NewService creates a new Service. The sync-identity follower is wired
// automatically when db supports it (the production *PebbleStore does); stores
// that do not — mocks, and decorators that embed the Store interface rather
// than forwarding capability methods — yield nil and the merge paths simply do
// not touch sync identity. Override with SetSyncFollower.
func NewService(db Store) *Service {
	follower := database.AsSyncIdentityStore(db)
	if follower == nil {
		// Say so out loud. A nil follower silently disables the whole
		// merge-follow hook, and the only way it can happen in production is
		// someone wrapping the concrete store in a decorator that embeds the
		// Store interface — exactly the kind of change whose blast radius is
		// invisible otherwise.
		slog.Warn("merge: store does not implement SyncIdentityStore; merges will NOT carry ABS sync identity or listening progress")
	}
	return &Service{db: db, syncFollower: follower}
}

// SoftDeletedInputError is returned when a merge is asked to include a book
// that is already soft-deleted, either as a loser or as the forced primary.
//
// Why a typed error: the FullScan exact-hash pass and the review lanes call
// MergeBooks in loops and need to tell "this pair is stale, skip it" from "the
// store is broken, stop". Before this guard the soft-delete pre-check lived in
// two of the ~ten callers; a merge that reached here with a soft-deleted
// primary produced a version group whose only primary was a deleted row, and
// the purge job then hard-deleted both sides (dedup bug hunt F2/F4).
type SoftDeletedInputError struct {
	BookID    string
	AsPrimary bool
}

func (e *SoftDeletedInputError) Error() string {
	role := "loser"
	if e.AsPrimary {
		role = "primary"
	}
	return fmt.Sprintf("book %s is soft-deleted and cannot be merged as %s", e.BookID, role)
}

// FilelessPrimaryError is returned when the caller forces a primary that has
// no audio route (HasAudioRoute: no book_file rows AND an empty FilePath)
// while another book in the merge has one. Keeping the file-less book would
// leave the version group's only live member with no route to any audio and
// put the only book that HAS audio on the purge clock (dedup bug hunt F1).
// The caller's choice is refused rather than silently overridden: a user who
// picked the file-less entry should see why it lost.
type FilelessPrimaryError struct {
	PrimaryID   string
	FileBearing []string
}

func (e *FilelessPrimaryError) Error() string {
	return fmt.Sprintf("primary %s has no files on record but %s do; refusing to keep the file-less book",
		e.PrimaryID, strings.Join(e.FileBearing, ", "))
}

// BookNotFoundError is returned when a merge names a book that the store
// cannot load. Callers used to detect this by substring-matching "not found"
// on the error text (three HTTP handlers did); a typed error lets them use
// errors.As and stops a rename of the message from silently turning a 409/404
// into a 500.
type BookNotFoundError struct {
	BookID string
}

func (e *BookNotFoundError) Error() string {
	return fmt.Sprintf("book %s not found", e.BookID)
}

// ProvisionalScanError is returned when a merge participant still has a
// provisional (hash-less) book_file row: a merge decided without a file hash
// rests on title similarity alone, so the service refuses until the book's
// deep scan has run. It is a refusal of the request's timing, not a server
// fault, and is mapped to 409 by the handlers.
type ProvisionalScanError struct {
	BookID string
}

func (e *ProvisionalScanError) Error() string {
	return fmt.Sprintf("book %s is awaiting a full scan and cannot be merged yet; "+
		"run its deep scan first (a merge decided without a file hash rests on "+
		"title similarity alone)", e.BookID)
}

// IsRefusal reports whether err is one of the service's typed refusals of the
// caller's request — a file-less forced primary, a soft-deleted participant,
// or a participant awaiting its deep scan. These are 409-class: the request
// was understood and declined for a reason the caller can act on. A missing
// book (BookNotFoundError) is deliberately NOT a refusal — it is 404 and, for
// the dedup handler, the signal that a candidate is stale.
func IsRefusal(err error) bool {
	var fileless *FilelessPrimaryError
	var softDeleted *SoftDeletedInputError
	var provisional *ProvisionalScanError
	return errors.As(err, &fileless) || errors.As(err, &softDeleted) || errors.As(err, &provisional) ||
		errors.Is(err, ErrITunesProtected)
}

// HasAudioRoute reports whether a book row can reach audio at all: it has at
// least one book_file row, or its own FilePath names a file. Both are routes
// the rest of the codebase actually plays through — audio_sample, the M3U
// export, organize and the purge all read book.FilePath for single-file
// books — so a row with a FilePath and no book_file rows is NOT file-less.
// 12,525 prod books (20.4%, census 2026-08-25) have exactly that shape
// because chapter consolidation was disabled when they were scanned; an
// earlier version of this tier called them file-less and demoted every one
// of them below any duplicate that happened to have rows, including iTunes
// ghosts.
//
// On-disk existence is deliberately NOT consulted: 41.8% of prod book_file
// rows had no bytes behind them when this was written, largely from moved
// and unmounted volumes, and a survivor election must not flip on a mount
// that is down for the afternoon.
func HasAudioRoute(b *database.Book, files []database.BookFile) bool {
	return len(files) > 0 || b.FilePath != ""
}

// ElectPrimary picks the index of the book to keep, or -1 if no book is
// eligible. Soft-deleted rows are never eligible. A book with an audio route
// (HasAudioRoute) always beats one with none; inside that tier BookIsBetter
// decides, and an exact tie is broken deterministically (see preferOnTie).
// filesByID must hold an entry for every book (nil is "no files").
//
// The tier is binary on purpose. Counting files would let a twelve-track mp3
// rip outrank a single-file m4b, which is the opposite of BookIsBetter's
// format rule; the only fact the tier encodes is "this row can reach audio at
// all".
func ElectPrimary(books []*database.Book, filesByID map[string][]database.BookFile) int {
	bestIdx := -1
	for i, b := range books {
		if b.IsSoftDeleted() {
			// A deleted row is never the survivor. Returns -1 if every book
			// is soft-deleted.
			continue
		}
		if bestIdx < 0 {
			bestIdx = i
			continue
		}
		iHas := HasAudioRoute(b, filesByID[b.ID])
		bestHas := HasAudioRoute(books[bestIdx], filesByID[books[bestIdx].ID])
		switch {
		case iHas && !bestHas:
			bestIdx = i
		case iHas == bestHas && preferOnTie(b, books[bestIdx]):
			bestIdx = i
		}
	}
	return bestIdx
}

// preferOnTie reports whether a should be elected over b. BookIsBetter
// decides when it has an opinion; when it has none in either direction (exact
// duplicates tie on every rule — same bytes means same size, bitrate and
// format) the election must not fall back to argument order, because the
// engine passes [scanned, owner] in scan order and a merge that failed after
// its version-group writes is retried with the pair reversed. Argument-order
// ties flipped the primary on that rerun (measured: c primary, rerun → d
// primary, c soft-deleted). So a tie prefers the book that is already the
// group's primary, then the older ULID — the earlier import is the one more
// likely to have been curated.
func preferOnTie(a, b *database.Book) bool {
	if BookIsBetter(a, b) {
		return true
	}
	if BookIsBetter(b, a) {
		return false
	}
	aPrim := a.IsPrimaryVersion != nil && *a.IsPrimaryVersion
	bPrim := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
	if aPrim != bPrim {
		return aPrim
	}
	return a.ID < b.ID
}

// MergeBooks merges a set of books into a single version group.
//
// Semantics (confirmed 2026-04-11 after an investigation into
// orphaned ITL entries):
//
//  1. Winner is chosen (user-supplied primaryID or auto-picked
//     via BookIsBetter) and given IsPrimaryVersion=true. Losers
//     get IsPrimaryVersion=false and are soft-deleted.
//  2. External IDs (iTunes PIDs, Audible ASINs, etc.) are
//     reassigned from losers to the winner so lookups still
//     resolve to the surviving entity.
//  3. **iTunes ITL cleanup**: before reassignment, we collect
//     each loser's iTunes PIDs and enqueue them for removal via
//     writeBackBatcher.EnqueueRemove. This matches the
//     behavior of maintenance_fixups.mergeDuplicateBook — the
//     UI merge path used to skip this step, which left the
//     losers' tracks alive in the iTunes library forever.
//  4. Loser DB rows are soft-deleted (MarkedForDeletion=true).
//     They stay recoverable via the existing soft-delete
//     restore flow for at least the retention window.
//  5. Loser files on disk are NOT touched, and neither are
//     the loser's book_file rows: they stay the loser's (its
//     own version). Nothing later removes them — the purge
//     refuses a book that still owns file rows
//     (database.ErrBookOwnsFiles), and the archive sweep that
//     once deleted them was retired on 2026-09-19.
//
// If primaryID is empty, the best book is auto-selected by ElectPrimary
// (a book with an audio route beats one without; then BookIsBetter:
// organized path, curation, M4B, bitrate, size; then a deterministic
// tie-break). If primaryID is provided, that book is set as the primary
// unless it has no audio route while another does (FilelessPrimaryError). A
// soft-deleted participant is refused (SoftDeletedInputError) unless it is a
// loser already in the group this merge resolves to — that is a completed
// merge being replayed, and the replayed loser's row is left exactly as it
// is: no external-ID reroute, no ITL removals, no second soft-delete (only
// the idempotent sync-identity follow re-runs). A book the store has no row
// for is BookNotFoundError; a store failure is returned as itself.
func (ms *Service) MergeBooks(bookIDs []string, primaryID string) (*Result, error) {
	return ms.MergeBooksWithOptions(bookIDs, primaryID, MergeOptions{})
}

// MergeOptions adjusts what MergeBooksWithOptions carries onto the survivor.
type MergeOptions struct {
	// CarryITunesFields copies each loser's iTunes-provenance Book fields onto
	// the survivor first-win (TransferITunesMetadataFirstWin), inside the
	// merge lock and in the same write that marks the survivor primary. An
	// error writing it fails the merge before any loser is soft-deleted.
	CarryITunesFields bool
}

// MergeBooksWithOptions is MergeBooks with MergeOptions; MergeBooks is this
// with the zero value.
func (ms *Service) MergeBooksWithOptions(bookIDs []string, primaryID string, opts MergeOptions) (*Result, error) {
	// De-duplicate the incoming ID list before anything else. Every current
	// caller either de-dupes itself or trusts a request body (e.g. the
	// /audiobooks/merge handler passes req.BookIDs straight through) — if
	// duplicate IDs reach here, the version-group loop below writes that
	// book's row twice, and the LAST write wins. A duplicated primary would
	// get its own IsPrimaryVersion=true immediately overwritten back to
	// false by its second occurrence, while the loser-cleanup loop (which
	// skips only book.ID == resolvedPrimaryID) still treats both occurrences
	// as "the primary" and never soft-deletes it — leaving the book neither
	// primary nor soft-deleted, and the version group with NO live primary.
	// This is the exact corruption class applyBookMergeReroute (#2007 /
	// F6-T10) already guards against by de-duping before calling in, but that
	// guard lived only at that one caller; enforcing it here protects every
	// caller, present and future, regardless of whether it remembers to
	// de-dupe first.
	seen := make(map[string]bool, len(bookIDs))
	deduped := make([]string, 0, len(bookIDs))
	for _, id := range bookIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		deduped = append(deduped, id)
	}
	bookIDs = deduped

	if len(bookIDs) < 2 {
		return nil, fmt.Errorf("need at least 2 book IDs to merge")
	}

	// Serialize the entire read-modify-write against every other merge-family
	// path (MergeBooks / CombineBooks / dedup.MergeBooks) — see the
	// mergeSerializeMu doc comment. Scoped to the merge itself; nothing
	// slow/blocking runs while it is held.
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	// Fetch all books
	var books []*database.Book
	for _, id := range bookIDs {
		book, err := ms.db.GetBookByID(id)
		if err != nil {
			// A store failure is NOT "not found": the store returns (nil, nil)
			// for a missing key and a non-nil error only for I/O or a corrupt
			// row. Typing it as not-found would let the dedup handler mark
			// the candidate "merged" for a pair that was never merged.
			return nil, fmt.Errorf("load book %s: %w", id, err)
		}
		if book == nil {
			return nil, &BookNotFoundError{BookID: id}
		}
		books = append(books, book)
	}

	// Validate an explicit primary before anything costs a file read.
	bestIdx := -1
	if primaryID != "" {
		for i, b := range books {
			if b.ID == primaryID {
				bestIdx = i
				break
			}
		}
		if bestIdx < 0 {
			return nil, fmt.Errorf("primary_id %s not in book_ids", primaryID)
		}
	}

	// Determine the version group ID from the LIVE participants only (reuse if
	// any live book already has one). A soft-deleted book's group is not
	// consulted: it is the group of whatever merge already consumed that book,
	// and letting it pick the group here is exactly how a stale pair pulled a
	// live book into some unrelated book's version group.
	versionGroupID := ""
	reusedGroup := false
	for _, b := range books {
		if b.IsSoftDeleted() {
			continue
		}
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			versionGroupID = *b.VersionGroupID
			reusedGroup = true
			break
		}
	}
	if versionGroupID == "" {
		versionGroupID = ulid.Make().String()
	}

	// Refuse soft-deleted participants. GetBookByID returns soft-deleted rows,
	// and every stale index in the system (book:hash:, a review candidate
	// written before a manual merge, a queued op) can hand one in. Merging INTO
	// one produces a group whose primary is a deleted row; merging one AS a
	// loser into a DIFFERENT group re-routes its external IDs a second time
	// and drags the live book into that other group. Neither is ever wanted,
	// so this is the chokepoint's job, not each caller's.
	//
	// The one soft-deleted shape that IS allowed: a loser that already belongs
	// to the very group this merge resolves to. That is a completed merge being
	// replayed (a review verdict re-applied, a retried op) — or a version the
	// USER deleted from a group whose live members are now being merged. Either
	// way the per-loser cleanup below is skipped for it (see the loop): the
	// first case already ran it, and the second must not have its external IDs
	// stripped and its 30-day purge clock restarted by a merge it is not part
	// of. It can never be elected or forced primary (ElectPrimary skips
	// soft-deleted rows; the explicit case is refused here).
	for _, b := range books {
		if !b.IsSoftDeleted() {
			continue
		}
		if b.ID == primaryID {
			return nil, &SoftDeletedInputError{BookID: b.ID, AsPrimary: true}
		}
		if reusedGroup && b.VersionGroupID != nil && *b.VersionGroupID == versionGroupID {
			continue
		}
		return nil, &SoftDeletedInputError{BookID: b.ID}
	}

	// Refuse to merge a book whose files have not been content-scanned.
	//
	// A merge picks a winner and soft-deletes the losers. Deciding that without
	// a trustworthy file hash means deciding on title/author similarity alone,
	// and this repo has already MEASURED that dedup collisions in the repoint
	// bucket are frequently genuine duplicate BOOKS rather than false pairs. A
	// bulk operation that silently joins two different books is the same class
	// of data loss the missing-file lane exists to prevent.
	//
	// Enforced here rather than at the callers for the reason the de-dupe guard
	// above gives: a guard that lives at one caller protects one caller. This is
	// the chokepoint every merge path funnels through.
	//
	// Deliberately NOT applied to browsing, playing, or a single manual edit --
	// a user acting on one book can see what they are doing. See
	// docs/design/2026-08-23-staged-library-scan-design.md.
	//
	// Fail CLOSED on a read error: if we cannot tell whether a book is
	// provisional, we do not merge it. The alternative silently reintroduces the
	// hazard on exactly the paths where the store is unhealthy.
	filesByID := make(map[string][]database.BookFile, len(books))
	for _, b := range books {
		files, err := ms.db.GetBookFiles(b.ID)
		if err != nil {
			return nil, fmt.Errorf("cannot verify scan state for book %s, refusing to merge: %w", b.ID, err)
		}
		if database.AnyProvisional(files) {
			return nil, &ProvisionalScanError{BookID: b.ID}
		}
		filesByID[b.ID] = files
	}
	if err := GuardITunesProtectedLoaded(books, filesByID); err != nil {
		return nil, err
	}

	// Survivor election is file-aware (see ElectPrimary). An explicit primary
	// is honored unless it would strand the group's audio: a forced primary
	// with no book_file rows loses to nothing, so refuse rather than keep a
	// row that cannot play while the one that can is soft-deleted onto the
	// purge clock. Merging books that ALL lack file rows is still allowed —
	// there is no audio to lose, and refusing would make the file-less ghost
	// class impossible to tidy until it is repaired.
	if bestIdx < 0 {
		bestIdx = ElectPrimary(books, filesByID)
		if bestIdx < 0 {
			// Unreachable after the guard above (a live participant always
			// exists once any soft-deleted one was admitted), kept so a future
			// reordering cannot elect a deleted row. Deliberately NOT a
			// SoftDeletedInputError: books[0] may be live, so that message
			// would be false about it, and the engine treats that type as a
			// benign "stale pair, skip" — an invariant break must not be
			// downgraded to a skipped scan item.
			return nil, fmt.Errorf("no live participant eligible as primary (ids=%v)", bookIDs)
		}
	} else if !HasAudioRoute(books[bestIdx], filesByID[books[bestIdx].ID]) {
		var fileBearing []string
		for _, b := range books {
			// An admitted soft-deleted loser can never be kept, so its route
			// is not a reason to refuse the requested primary.
			if !b.IsSoftDeleted() && HasAudioRoute(b, filesByID[b.ID]) {
				fileBearing = append(fileBearing, b.ID)
			}
		}
		if len(fileBearing) > 0 {
			return nil, &FilelessPrimaryError{PrimaryID: books[bestIdx].ID, FileBearing: fileBearing}
		}
	}

	// Ordering: this runs AFTER the cheap argument checks above (a bad
	// primary_id should not cost a file read per book) and BEFORE the version
	// group work below, which is where writes begin. Everything between is pure
	// computation.
	// INVARIANT (VG-DOUBLE-PRIMARY): a version group must never have more than
	// one is_primary_version=true member. When we REUSE an existing group ID,
	// ALL current members must be re-evaluated, not just the ones in this
	// call. The primary-flag loop below writes only the books in `books`, so a
	// member that joined the group in a PRIOR merge and is absent from this
	// call would keep its is_primary_version=true and the group would end up
	// with two live primaries — measured on prod 2026-08-11 in 10 of 15
	// sampled groups.
	//
	// Load the pre-existing membership BEFORE any write so a read failure
	// aborts the merge with nothing half-applied. GetBooksByVersionGroup
	// already filters soft-deleted rows, so this is the live membership only;
	// a soft-deleted row still flagged primary is a separate invariant
	// (store_invariants (a)) that this accessor cannot see and this fix does
	// not claim to repair.
	//
	// RACE, deliberately unfixed here: this read-then-write is protected only
	// by mergeSerializeMu, which covers the merge family. A non-merge writer
	// (regroup apply, reconcile.ElectMissingPrimaries) that adds a primary to
	// this group between the read and the writes below can still leave two.
	// Out of scope per the item; noted so the next reader does not assume it
	// is handled.
	var preExistingMembers []database.Book
	if reusedGroup {
		members, err := ms.db.GetBooksByVersionGroup(versionGroupID)
		if err != nil {
			return nil, fmt.Errorf("failed to load version group %s membership: %w", versionGroupID, err)
		}
		preExistingMembers = members
	}

	// Update all books to share the version group. Winner is
	// marked primary; losers are marked non-primary. We still
	// persist the flag on losers here so the version group is
	// queryable and the relationship survives through the
	// soft-delete call below.
	resolvedPrimaryID := books[bestIdx].ID
	for i, book := range books {
		isPrimary := i == bestIdx
		if !isPrimary && book.IsSoftDeleted() &&
			book.VersionGroupID != nil && *book.VersionGroupID == versionGroupID &&
			book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			// Replayed loser already carries exactly these values. Under the
			// copy-on-write book_ver: history every UpdateBook is a full
			// snapshot, so an unchanged rewrite is not free.
			continue
		}
		book.VersionGroupID = &versionGroupID
		book.IsPrimaryVersion = &isPrimary
		// The iTunes carry (opts.CarryITunesFields) is applied to the survivor
		// HERE, inside mergeSerializeMu and before any loser is soft-deleted,
		// so a merge whose carry cannot be written fails with every loser still
		// live and still holding its stats. It used to run in the dedup op's
		// caller before this lock was taken, and its write failure was only
		// logged -- the merge went on and the stats stayed behind on a row
		// headed for the purge clock (A1#10). The loser values it copies from
		// were read above, under this same lock.
		//
		// Written through ModifyBook, not UpdateBook(book): `book` was read at
		// the top of this call, and a full-row write of it reverts any
		// non-merge writer (metadata apply, a user edit) that committed to this
		// row since. Only the fields this merge owns are set on the fresh row,
		// and books[i] is replaced with what was stored: the loser-cleanup
		// loop below reads IsSoftDeleted() (MarkedForDeletion) off it, and
		// that must be the row as written, not as read at the top.
		stored, err := ms.db.ModifyBook(book.ID, func(fresh *database.Book) error {
			fresh.VersionGroupID = &versionGroupID
			fresh.IsPrimaryVersion = &isPrimary
			if isPrimary && opts.CarryITunesFields {
				for j, from := range books {
					if j != bestIdx {
						TransferITunesMetadataFirstWin(fresh, from)
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update book %s: %w", book.ID, err)
		}
		if stored == nil {
			return nil, &BookNotFoundError{BookID: book.ID}
		}
		books[i] = stored
	}

	// Demote every pre-existing member of a REUSED group that the loop above
	// did not just write. Two independent guards, because the books in this
	// call frequently already carry versionGroupID and are therefore in
	// preExistingMembers too: `seen` is exactly the set of book IDs passed to
	// this call, and resolvedPrimaryID is checked separately so we can never
	// demote the winner and produce a group with ZERO primaries.
	//
	// The election itself is deliberately NOT widened to these books: an
	// explicit primaryID must win (documented MergeBooks semantics), and
	// merge's BookIsBetter rule is not reconcile.electPrimaryFor's
	// earliest-created rule — running a second, disagreeing election here is
	// the bug class this fix exists to remove. The winner is whoever this call
	// elected; everyone else in the group is demoted.
	//
	// nil is NOT false, so a nil member must be rewritten to an explicit false
	// rather than skipped. Only members already storing an explicit false are
	// left alone.
	//
	// Do NOT read that as "nil means primary everywhere" -- an earlier version
	// of this comment said so and it is wrong. The readers disagree, which is
	// the TASK-002/003/004 split:
	//
	//	nil == PRIMARY:      pebble_store.go  `eff := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion`
	//	                     memdb_reads.go   likewise
	//	                     memdb_schema.go  effectiveBoolFieldIndex{Default: true}
	//	nil == NOT primary:  reconcile/elect_primaries.go:150 and :207
	//	                     pebble_store.go  sortVersions
	//	                     dbtest/invariants.go
	//
	// This write is correct under BOTH readings -- it is the repair if nil
	// counts as primary, and a harmless no-op if it does not -- which is
	// exactly why it is safe to make while that disagreement is unresolved.
	// It is spelled out because a future reader who trusts one reading will
	// mis-predict what this loop does at the other call sites.
	for i := range preExistingMembers {
		member := &preExistingMembers[i]
		if seen[member.ID] || member.ID == resolvedPrimaryID {
			continue
		}
		if member.IsPrimaryVersion != nil && !*member.IsPrimaryVersion {
			continue
		}
		// Written through ModifyBook, never UpdateBook(member): the store
		// re-reads the row under the book's write lock and only
		// IsPrimaryVersion is set on it. That covers both hazards at once --
		// GetBooksByVersionGroup is documented as possibly serving a slim
		// projection (elect_primaries.go:232 says so about this exact
		// accessor; regroup_apply.go:290 and reconcile.go:810 state the same
		// rule for their own writes), and a column another writer commits
		// between a read and a whole-row write would be reverted by it.
		alreadyDemoted := false
		demoted, err := ms.db.ModifyBook(member.ID, func(b *database.Book) error {
			alreadyDemoted = b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion
			if alreadyDemoted {
				return database.ErrSkipBookWrite
			}
			notPrimary := false
			b.IsPrimaryVersion = &notPrimary
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to demote pre-existing version-group member %s: %w", member.ID, err)
		}
		if demoted == nil {
			return nil, fmt.Errorf("failed to load pre-existing version-group member %s for demotion: book not found", member.ID)
		}
		if alreadyDemoted {
			continue
		}
		slog.Info("merge demoted pre-existing version-group member",
			"id", member.ID, "group", versionGroupID, "primary", resolvedPrimaryID)
	}

	// --- Per-loser cleanup ---
	//
	// For each non-primary book we:
	//  (a) collect its iTunes PIDs BEFORE reassignment so we
	//      know which tracks to remove from the ITL,
	//  (b) reassign all external IDs to the winner so future
	//      lookups resolve,
	//  (c) enqueue ITL removals for the collected PIDs so
	//      iTunes no longer shows duplicate tracks for this
	//      version group,
	//  (d) soft-delete the loser so it drops off the default
	//      library view. Its files on disk and its book_file
	//      rows are left alone (see item 5 on MergeBooks).
	//
	// Ordering is the repair path: a loser is soft-deleted (d) ONLY after
	// (a)-(c) succeeded for it. If (a) or (b) fails the loser is left LIVE
	// (still a non-primary member of the group, visible as an extra version)
	// and reported, so a retried merge re-enters this loop for it. Soft-
	// deleting first and warning about a failed reassign would return
	// success with ext_id:* rows still pointing at a deleted book, and the
	// soft-deleted-loser skip below would then make that permanent.
	eidStore := AsExternalIDReassigner(ms.db)
	var loserErrs []error
	softDeleted := 0
	for _, book := range books {
		if book.ID == resolvedPrimaryID {
			continue
		}
		// Admitted soft-deleted loser (see the guard above): already merged
		// away, or deleted by the user. SoftDeleteBook below is NOT
		// idempotent — it rewrites MarkedForDeletionAt, restarting the
		// 30-day retention — and for a user-deleted version
		// ReassignExternalIDs would strip the IDs a RestoreAudiobook brings
		// back. Leave the row exactly as it is. (The sync-identity follow
		// further down is idempotent and still runs for it: a first merge
		// that crashed between SoftDeleteBook and FollowMerge has no other
		// repair path.)
		if book.IsSoftDeleted() {
			slog.Debug("merge: loser already soft-deleted; cleanup skipped", "id", book.ID, "primary", resolvedPrimaryID)
			continue
		}

		// (a) Collect PIDs before reassignment. A read failure here is not
		// skippable: once (b) moves the mappings to the winner the loser's
		// PIDs are unrecoverable, so the ITL removals would be lost for good.
		var dupPIDs []string
		mappings, err := ms.db.GetExternalIDsForBook(book.ID)
		if err != nil {
			slog.Error("merge: cannot read loser's external IDs; loser left live for retry",
				"id", book.ID, "primary", resolvedPrimaryID, "err", err)
			loserErrs = append(loserErrs, fmt.Errorf("read external IDs of loser %s: %w", book.ID, err))
			continue
		}
		for _, m := range mappings {
			if m.Source == "itunes" && m.ExternalID != "" && !m.Tombstoned {
				dupPIDs = append(dupPIDs, m.ExternalID)
			}
		}

		// (b) Reassign external IDs to the winner. On failure the loser stays
		// live so the retry re-runs this step; see the ordering note above.
		if eidStore != nil {
			if err := eidStore.ReassignExternalIDs(book.ID, resolvedPrimaryID); err != nil {
				slog.Error("merge: ReassignExternalIDs failed; loser left live for retry",
					"from", book.ID, "to", resolvedPrimaryID, "err", err)
				loserErrs = append(loserErrs, fmt.Errorf("reassign external IDs of loser %s: %w", book.ID, err))
				continue
			}
		}

		// (c) Queue iTunes removals for the loser's tracks so
		// the ITL stops showing them. Best-effort — a nil
		// batcher (e.g. tests, or iTunes write-back disabled)
		// means we just skip.
		if ms.writeBackBatcher != nil && len(dupPIDs) > 0 {
			for _, pid := range dupPIDs {
				ms.writeBackBatcher.EnqueueRemove(pid)
			}
			slog.Info("merge queued ITL removals for loser", "count", len(dupPIDs), "id", book.ID)
		}

		// (d) Soft-delete the loser. A failure here is a real failure of
		// the merge: the loser is already a non-primary member of the
		// group, so leaving it live is visible (it shows as an extra
		// version) but it is NOT silently destroyed — SoftDeleteBook no
		// longer falls back to a hard delete. Keep going so the other
		// losers are handled, then report every one that stayed live.
		if err := SoftDeleteBook(ms.db, book.ID); err != nil {
			slog.Error("merge soft-delete failed; loser left live as a non-primary version",
				"id", book.ID, "primary", resolvedPrimaryID, "err", err)
			loserErrs = append(loserErrs, fmt.Errorf("soft-delete loser %s: %w", book.ID, err))
			continue
		}
		softDeleted++
	}
	if len(loserErrs) > 0 {
		return nil, fmt.Errorf("merge into %s applied but %d loser(s) could not be cleaned up and remain live: %w",
			resolvedPrimaryID, len(loserErrs), errors.Join(loserErrs...))
	}

	// --- Sync-identity + progress follow ---
	//
	// Still inside mergeSerializeMu (see the Lock above): that single
	// process-wide lock already makes every merge-family read-modify-write
	// mutually exclusive with every other one, so anything added here is
	// exactly-once with respect to concurrent merges by construction. Do NOT
	// "fix" this by adding per-book-pair partitioning on top — CLAUDE.md's
	// partition-by-book-ID guidance targets whole-library batch loops, not a
	// single already-fully-serialized RMW, and a second scheme could subtly
	// fight this lock.
	//
	// The winner's ULID never changes here (resolvedPrimaryID is an existing
	// book's ID) and losers are only soft-deleted, so the winner's reverse
	// index needs no repoint: this is a loser-only redirect. Kept as its own
	// separately-testable step rather than folded into the loop above.
	// Already-soft-deleted losers are deliberately INCLUDED, unlike in the
	// cleanup loop: FollowMerge is idempotent (RecordSyncMerge returns early
	// on an existing redirect; progress merge keeps the furthest position),
	// and a replay is the only repair for a first merge that crashed after
	// SoftDeleteBook but before this best-effort follow.
	losers := make([]string, 0, len(books)-1)
	for _, book := range books {
		if book.ID != resolvedPrimaryID {
			losers = append(losers, book.ID)
		}
	}
	FollowMerge(ms.db, ms.syncFollower, resolvedPrimaryID, losers)

	return &Result{
		PrimaryID:      resolvedPrimaryID,
		VersionGroupID: versionGroupID,
		MergedCount:    len(books),
		SoftDeleted:    softDeleted,
	}, nil
}

// CombineResult is the outcome of a CombineBooks call.
type CombineResult struct {
	PrimaryID  string `json:"primary_id"`
	FilesMoved int    `json:"files_moved"`
	// BooksDeleted counts absorbed books SOFT-deleted by the combine. The wire
	// key keeps its old name for API compatibility; the books are recoverable
	// via UndoCombine(JournalID) until the purge job removes them.
	BooksDeleted int `json:"books_deleted"`
	// JournalID is the undo key (POST /api/v1/merge/undo/:journal_id).
	JournalID string `json:"journal_id,omitempty"`
	// UndoUnavailable is set, and JournalID empty, when the combine applied
	// but its journal could not be finalized, so it cannot be undone.
	UndoUnavailable string `json:"undo_unavailable,omitempty"`
}

// CombineOverride holds optional metadata fields to apply to the survivor book
// after all files have been reassigned. Only non-empty strings take effect;
// omitting a field leaves the survivor's existing value untouched.
type CombineOverride struct {
	Title    string `json:"title,omitempty"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
}

// CombineBooks combines several books into ONE multi-file book — distinct from
// MergeBooks, which links them as alternate VERSIONS in a version group. Every
// selected book's audio files become real BookFiles on the survivor, and the
// absorbed shells are SOFT-deleted and journaled so the combine can be undone
// (UndoCombine, combine_journal.go). This is the manual analogue of the
// shattered-book heal (applyFSRegroup): use it to reassemble tracks that were
// imported as one book per file (e.g. an untagged folder of chapters).
//
// Why soft-delete: an approved review-queue "combine" or "duplicate-of" runs
// this with no human at the keyboard for the specific rows, and a hard delete
// made every such approval permanent. The journal is written BEFORE the first
// mutation; if it cannot be written the combine is refused, because an
// irreversible combine with no undo key is worse than no combine at all (the
// same rule dedup.MergeBooksJournaled follows).
//
// DB-only: files stay where they are on disk (most shattered sets are already in
// one folder). Run organize afterward to physically co-locate if desired.
//
// override is optional: non-empty fields overwrite the survivor's metadata after
// the combine. Leave it nil or empty to keep the survivor's existing metadata.
func (ms *Service) CombineBooks(bookIDs []string, primaryID string, override *CombineOverride) (*CombineResult, error) {
	if len(bookIDs) < 2 {
		return nil, fmt.Errorf("need at least 2 books to combine")
	}
	if primaryID == "" {
		return nil, fmt.Errorf("primary_id is required for combine")
	}

	// Serialize the entire read-modify-write against every other merge-family
	// path (MergeBooks / CombineBooks / UndoCombine / dedup.MergeBooks).
	// CombineBooks is a synchronous HTTP handler with no concurrency key, so two
	// combines — or a combine racing a MergeBooks on a shared book — can
	// otherwise interleave GetBookByID -> MoveBookFilesToBook ->
	// ReassignExternalIDs -> soft-delete -> UpdateBook and corrupt the same way
	// #1930 fixed for MergeBooks. Scoped to the combine itself; only local DB
	// work runs while it is held. (CombineBooks never calls MergeBooks or
	// dedup.MergeBooks, so the non-reentrant mutex is taken exactly once.)
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	survivor, err := ms.db.GetBookByID(primaryID)
	if err != nil {
		return nil, fmt.Errorf("load book %s: %w", primaryID, err)
	}
	if survivor == nil {
		return nil, &BookNotFoundError{BookID: primaryID}
	}
	// Validate all IDs up front so a bad ID aborts before any mutation.
	seen := map[string]bool{}
	books := make(map[string]*database.Book, len(bookIDs))
	for _, id := range bookIDs {
		if seen[id] {
			return nil, fmt.Errorf("duplicate book id %s", id)
		}
		seen[id] = true
		b, err := ms.db.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("load book %s: %w", id, err)
		}
		if b == nil {
			return nil, &BookNotFoundError{BookID: id}
		}
		// A soft-deleted participant is refused, as MergeBooks refuses it. It
		// is already merged away or trashed; combining it would re-stamp its
		// MarkedForDeletionAt (restarting retention) and journal an undo that
		// "restores" a book the user had deleted.
		if b.IsSoftDeleted() {
			return nil, &SoftDeletedInputError{BookID: id, AsPrimary: id == primaryID}
		}
		books[id] = b
	}
	if !seen[primaryID] {
		return nil, fmt.Errorf("primary_id %s not in book_ids", primaryID)
	}
	if err := GuardITunesProtected(ms.db, bookIDs); err != nil {
		return nil, err
	}

	res := &CombineResult{PrimaryID: primaryID}
	eidStore := AsExternalIDReassigner(ms.db)

	// Users are listed once so each FollowMerge's progress drain can be
	// snapshotted for undo. Household-scale, not library-scale.
	var users []database.User
	if ms.syncFollower != nil {
		if users, err = ms.db.ListUsers(); err != nil {
			return nil, fmt.Errorf("list users for combine journal: %w", err)
		}
	}

	// --- Journal, before the first mutation ---
	journal := &CombineJournal{
		ID:             ulid.Make().String(),
		Status:         CombineJournalPending,
		CreatedAt:      time.Now().UTC(),
		SurvivorID:     primaryID,
		ITunesRemovals: []string{},
	}
	ownFiles, err := ms.db.GetBookFiles(primaryID)
	if err != nil {
		return nil, fmt.Errorf("load files of %s: %w", primaryID, err)
	}
	for _, f := range ownFiles {
		journal.SurvivorOwnFiles = append(journal.SurvivorOwnFiles, CombineFileMove{
			FileID: f.ID, FromBookID: primaryID, FilePath: f.FilePath,
			DiscBefore: f.DiscNumber, TrackBefore: f.TrackNumber,
		})
	}
	movedFiles := make(map[string][]string, len(bookIDs))
	bulk := make([]database.BookFileMove, 0, len(bookIDs))
	for _, id := range bookIDs {
		if id == primaryID {
			continue
		}
		b := books[id]
		files, err := ms.db.GetBookFiles(id)
		if err != nil {
			return nil, fmt.Errorf("load files of %s: %w", id, err)
		}
		a := CombineAbsorbed{
			BookID:           id,
			FilePath:         b.FilePath,
			IsPrimaryVersion: b.IsPrimaryVersion,
			VersionGroupID:   b.VersionGroupID,
			SyncRedirected:   ms.syncFollower != nil,
		}
		if len(files) > 0 {
			ids := make([]string, len(files))
			for i := range files {
				ids[i] = files[i].ID
				a.Files = append(a.Files, CombineFileMove{
					FileID: files[i].ID, FromBookID: id, FilePath: files[i].FilePath,
					DiscBefore: files[i].DiscNumber, TrackBefore: files[i].TrackNumber,
				})
			}
			movedFiles[id] = ids
			bulk = append(bulk, database.BookFileMove{FileIDs: ids, SourceBookID: id})
		}
		if eidStore != nil {
			mappings, err := ms.db.GetExternalIDsForBook(id)
			if err != nil {
				return nil, fmt.Errorf("load external ids of %s: %w", id, err)
			}
			for _, m := range mappings {
				// Tombstoned mappings are dead reimport blockers; they ride along
				// with ReassignExternalIDs but resolve to no book, so they are
				// neither checked nor moved back by undo.
				if !m.Tombstoned {
					a.ExternalIDs = append(a.ExternalIDs, CombineExternalID{Source: m.Source, ExternalID: m.ExternalID})
				}
			}
		}
		journal.Absorbed = append(journal.Absorbed, a)
	}
	if err := ms.putCombineJournal(journal); err != nil {
		return nil, fmt.Errorf("combine refused: undo journal could not be written: %w", err)
	}

	// Materialize the survivor's own single-file (virtual-segment) audio as a
	// BookFile so the combined book owns ALL its files explicitly.
	if fm, n := ms.ensureOwnFile(survivor); n > 0 {
		res.FilesMoved += n
		if fm.FileID != "" {
			journal.SurvivorFiles = append(journal.SurvivorFiles, fm)
		}
	}

	// Move every absorbed book's files onto the survivor in ONE batch, before the
	// per-book loop below.
	//
	// MoveBookFilesToBook recomputes both of its books, so moving inside the loop
	// recomputed the survivor once per absorbed book — each one re-reading the
	// survivor's entire, steadily growing file set. Batching pays a single
	// recompute per distinct book for the whole combine.
	//
	// The batch is atomic, so a failure here writes nothing and returns. No
	// per-book fallback, deliberately — a partially-moved combine is exactly what
	// the early return exists to prevent.
	if len(bulk) > 0 {
		if err := ms.db.MoveBookFilesToBookBulk(bulk, survivor.ID); err != nil {
			return nil, fmt.Errorf("move files -> %s: %w", survivor.ID, err)
		}
	}

	kept := make([]CombineAbsorbed, 0, len(journal.Absorbed))
	for _, entry := range journal.Absorbed {
		a := &entry
		id := a.BookID

		// A book deleted by something else between validation and here is
		// skipped, not fatal. If the bulk move already took its files, its
		// entry stays so the journal still names those moves; undo then
		// refuses, because a vanished book cannot be restored.
		if cur, _ := ms.db.GetBookByID(id); cur == nil {
			mlog.Warn("combine: absorbed book vanished mid-combine; skipped id=%s journal=%s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(journal.ID))
			if len(movedFiles[id]) > 0 {
				journal.Warnings = append(journal.Warnings, fmt.Sprintf("absorbed book %s vanished after its files moved", id))
				kept = append(kept, entry)
			}
			continue
		}

		// Attach this book's files to the survivor. The move itself happened in
		// the batched pre-pass above; movedFiles carries the ids it took.
		if ids := movedFiles[id]; len(ids) > 0 {
			// Carry each moved file's sync_file identity (ino) onto the
			// survivor, still inside mergeSerializeMu.
			FollowFileMove(ms.db, id, survivor.ID, ids)
			res.FilesMoved += len(ids)
		} else if a.FilePath != "" {
			fm, n := ms.attachVirtualFile(books[id], survivor.ID)
			res.FilesMoved += n
			if fm.FileID != "" {
				a.Files = append(a.Files, fm)
			}
		}

		// Reassign external IDs to the survivor.
		if eidStore != nil {
			if err := eidStore.ReassignExternalIDs(id, survivor.ID); err != nil {
				slog.Warn("combine ReassignExternalIDs", "from", id, "to", survivor.ID, "err", err)
				// Nothing moved, so undo has nothing to move back.
				a.ExternalIDs = nil
				journal.Warnings = append(journal.Warnings, fmt.Sprintf("external ids of %s were not reassigned: %v", id, err))
			}
		}

		// Carry the absorbed shell's sync identity and listening position onto
		// the survivor, snapshotting both sides first so undo can put them
		// back (mergeUserProgress DRAINS the loser; nothing else keeps it).
		var before, after progressSnap
		if ms.syncFollower != nil {
			before = ms.snapProgressPair(users, id, survivor.ID, journal)
		}
		FollowMerge(ms.db, ms.syncFollower, survivor.ID, []string{id})
		if ms.syncFollower != nil {
			after = ms.snapProgressPair(users, id, survivor.ID, journal)
			a.Progress = buildProgressJournal(before, after)
		}

		// Guard (mirrors applyFSRegroup): never retire a book that still owns
		// files — that would orphan audio. After the move it must be empty.
		if remaining, _ := ms.db.GetBookFiles(id); len(remaining) != 0 {
			return nil, fmt.Errorf("book %s still owns %d files after move; aborting soft-delete (journal %s left %s)",
				id, len(remaining), journal.ID, CombineJournalPending)
		}
		stamp, err := ms.softDeleteAbsorbed(id)
		if err != nil {
			return nil, fmt.Errorf("soft-delete absorbed book %s (journal %s left %s): %w", id, journal.ID, CombineJournalPending, err)
		}
		a.MarkedForDeletionAt = stamp
		res.BooksDeleted++
		kept = append(kept, *a)
	}
	journal.Absorbed = kept

	if err := ms.db.RecomputeBookAggregates(survivor.ID); err != nil {
		slog.Warn("combine RecomputeBookAggregates", "id", survivor.ID, "err", err)
	}

	if override != nil && (override.Title != "" || override.Author != "" || override.Narrator != "") {
		journal.Override = ms.applyCombineOverride(primaryID, override)
	}

	journal.Status = CombineJournalApplied
	if err := ms.putCombineJournal(journal); err != nil {
		// The combine is done and cannot be failed now. Say plainly that this
		// one has no undo, both in the log and to the caller.
		mlog.Error("combine applied but its undo journal could not be finalized; this combine is NOT undoable journal=%s survivor=%s err=%s",
			journal.ID, logger.SanitizeLogValue(survivor.ID), logger.SanitizeLogValue(fmt.Sprint(err)))
		res.UndoUnavailable = err.Error()
	} else {
		res.JournalID = journal.ID
	}

	mlog.Info("combined books into one survivor=%s journal=%s files_moved=%d books_soft_deleted=%d",
		logger.SanitizeLogValue(survivor.ID), journal.ID, res.FilesMoved, res.BooksDeleted)
	return res, nil
}

// softDeleteAbsorbed soft-deletes a combine's absorbed shell and CLEARS its
// FilePath, returning the MarkedForDeletionAt stamp as the store kept it.
//
// Clearing FilePath is what makes the soft delete safe: the path now belongs to
// a file row the survivor owns, and PurgeSoftDeletedBooks (with delete-files
// on) os.Remove()s a purged single-file book's FilePath. The journal keeps the
// original for undo. A separate function from SoftDeleteBook because
// MergeBooks' losers keep their own files and must keep their FilePath.
func (ms *Service) softDeleteAbsorbed(bookID string) (*time.Time, error) {
	// ModifyBook: the three columns are set on the row as it is under the
	// write lock, so a column another writer committed in the meantime is
	// not reverted by a whole-row write of a stale read.
	written, err := ms.db.ModifyBook(bookID, func(b *database.Book) error {
		t := true
		now := time.Now()
		b.MarkedForDeletion = &t
		b.MarkedForDeletionAt = &now
		b.FilePath = ""
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("soft-delete absorbed %s: %w", bookID, err)
	}
	if written == nil {
		return nil, &BookNotFoundError{BookID: bookID}
	}
	stored, err := ms.db.GetBookByID(bookID)
	if err != nil || stored == nil || stored.MarkedForDeletionAt == nil {
		return nil, fmt.Errorf("read back soft-delete of %s: %v", bookID, err)
	}
	return stored.MarkedForDeletionAt, nil
}

// progressSnap is one moment's progress on both sides of a FollowMerge.
type progressSnap struct {
	absState, survState map[string]*database.UserBookState
	absPos, survPos     map[string][]database.UserPosition
}

func (ms *Service) snapProgressPair(users []database.User, absorbedID, survivorID string, j *CombineJournal) progressSnap {
	var s progressSnap
	var err error
	if s.absState, s.absPos, err = snapshotProgress(ms.db, users, absorbedID); err == nil {
		s.survState, s.survPos, err = snapshotProgress(ms.db, users, survivorID)
	}
	if err != nil {
		mlog.Warn("combine: progress snapshot failed; undo will not restore listening progress for this book absorbed=%s err=%s",
			logger.SanitizeLogValue(absorbedID), logger.SanitizeLogValue(fmt.Sprint(err)))
		j.Warnings = append(j.Warnings, fmt.Sprintf("progress of %s not journaled: %v", absorbedID, err))
		return progressSnap{}
	}
	return s
}

// buildProgressJournal keeps only users who had progress on the absorbed book
// before the follow — nobody else's progress was touched by FollowMerge.
func buildProgressJournal(before, after progressSnap) []CombineUserProgress {
	var out []CombineUserProgress
	userIDs := map[string]bool{}
	for u := range before.absState {
		userIDs[u] = true
	}
	for u := range before.absPos {
		userIDs[u] = true
	}
	ids := make([]string, 0, len(userIDs))
	for u := range userIDs {
		ids = append(ids, u)
	}
	slices.Sort(ids)
	for _, u := range ids {
		out = append(out, CombineUserProgress{
			UserID:              u,
			AbsorbedState:       before.absState[u],
			AbsorbedPositions:   before.absPos[u],
			SurvivorStateBefore: before.survState[u],
			SurvivorPosBefore:   before.survPos[u],
			SurvivorStateAfter:  after.survState[u],
			SurvivorPosAfter:    after.survPos[u],
		})
	}
	return out
}

// applyCombineOverride writes the caller's title/narrator/author onto the
// survivor and returns what the survivor held before, for undo. UpdateBook
// does a full column replacement, so it re-fetches after the aggregate
// recompute and patches only the non-empty override fields.
func (ms *Service) applyCombineOverride(primaryID string, override *CombineOverride) *CombineOverrideUndo {
	undo := &CombineOverrideUndo{Applied: *override, FieldStates: map[string]*database.MetadataFieldState{}}

	// Lock rows the override is about to write, as they were before.
	lockKeys := []string{}
	if override.Title != "" {
		lockKeys = append(lockKeys, database.FieldKeyTitle)
	}
	if override.Narrator != "" {
		lockKeys = append(lockKeys, database.FieldKeyNarrator)
	}
	if override.Author != "" {
		lockKeys = append(lockKeys, database.FieldKeyAuthorName)
	}
	if states, err := ms.db.GetMetadataFieldStates(primaryID); err == nil {
		byField := map[string]database.MetadataFieldState{}
		for _, st := range states {
			byField[st.Field] = st
		}
		for _, k := range lockKeys {
			if st, ok := byField[k]; ok {
				st := st
				undo.FieldStates[k] = &st
			} else {
				undo.FieldStates[k] = nil
			}
		}
	} else {
		mlog.Warn("combine override: could not read lock rows for undo journal id=%s err=%s", logger.SanitizeLogValue(primaryID), logger.SanitizeLogValue(fmt.Sprint(err)))
	}

	if override.Title != "" || override.Narrator != "" {
		// ModifyBook: only Title and Narrator are written, on the row as it
		// is under the write lock, so a column another writer committed in
		// the meantime survives. The undo before-values come from that same
		// row, the one the write actually replaces.
		fresh, err := ms.db.ModifyBook(primaryID, func(b *database.Book) error {
			undo.TitleBefore = b.Title
			undo.NarratorBefore = b.Narrator
			if override.Title != "" {
				b.Title = override.Title
			}
			if override.Narrator != "" {
				b.Narrator = &override.Narrator
			}
			return nil
		})
		if err != nil || fresh == nil {
			slog.Warn("combine override ModifyBook", "id", primaryID, "err", err)
			// Not applied, so nothing for undo to roll back or check.
			undo.Applied.Title, undo.Applied.Narrator = "", ""
		} else {
			slog.Info("combine applied metadata override", "id", fresh.ID,
				"title", override.Title, "narrator", override.Narrator)
			// The override is the user's explicit choice for the
			// survivor. Record it as a lock, or the next metadata fetch
			// is free to replace the value they just picked.
			userSet := map[string]any{}
			if override.Title != "" {
				userSet[database.FieldKeyTitle] = override.Title
			}
			if override.Narrator != "" {
				userSet[database.FieldKeyNarrator] = override.Narrator
			}
			if err := database.RecordUserOverrides(ms.db, fresh.ID, userSet); err != nil {
				slog.Warn("combine override lock rows", "id", fresh.ID, "err", err)
			}
		}
	}
	// Author resolution: find or create by name, then link to the survivor.
	//
	// Creation gate. The override is often PREFILLED from one of the books
	// being combined rather than typed, so a book already carrying an
	// author row like "Track 01" would otherwise propagate that name onto
	// the survivor. Logged at Warn rather than dropped quietly: every
	// sibling override on this path surfaces its failures, and a rejected
	// author is a change the caller asked for that did not happen.
	//
	// The predicate lives in internal/personname, not internal/dedup: six
	// dedup files import this package, so anything reachable from here has
	// to sit in a leaf.
	cleanedAuthor := ""
	if override.Author != "" {
		if c, ok := personname.CleanAuthorNameForCreation(override.Author); ok {
			cleanedAuthor = c
		} else {
			slog.Warn("combine override author rejected as unusable; survivor left unchanged",
				"id", logger.SanitizeLogValue(primaryID), "author", logger.SanitizeLogValue(override.Author))
		}
	}
	if cleanedAuthor == "" {
		undo.Applied.Author = ""
		delete(undo.FieldStates, database.FieldKeyAuthorName)
		return undo
	}
	author, err := ms.db.GetAuthorByName(cleanedAuthor)
	if err == nil && author == nil {
		author, err = ms.db.CreateAuthor(cleanedAuthor)
	}
	if err != nil || author == nil {
		if err != nil {
			slog.Warn("combine override author", "name", logger.SanitizeLogValue(override.Author), "err", logger.SanitizeLogValue(fmt.Sprint(err)))
		}
		undo.Applied.Author = ""
		delete(undo.FieldStates, database.FieldKeyAuthorName)
		return undo
	}
	if prior, aerr := ms.db.GetBookAuthors(primaryID); aerr == nil {
		undo.AuthorsBefore = prior
	} else {
		mlog.Warn("combine override: could not read prior authors for undo journal id=%s err=%s", logger.SanitizeLogValue(primaryID), logger.SanitizeLogValue(fmt.Sprint(aerr)))
	}
	// Surface failures instead of swallowing them: a dropped write here
	// silently discards the user's explicit author choice while Combine
	// still reports success (matches the sibling title/narrator path above).
	if saErr := ms.db.SetBookAuthors(primaryID, []database.BookAuthor{
		{BookID: primaryID, AuthorID: author.ID, Role: "author", Position: 0},
	}); saErr != nil {
		slog.Warn("combine override SetBookAuthors", "id", logger.SanitizeLogValue(primaryID), "author", logger.SanitizeLogValue(override.Author), "err", logger.SanitizeLogValue(fmt.Sprint(saErr)))
	}
	// Also set AuthorID on the book row for backward compat.
	// ModifyBook: only AuthorID is written, on the row as it is under the
	// write lock, so a column another writer committed in the meantime
	// survives. The undo before-value comes from that same row.
	b, ubErr := ms.db.ModifyBook(primaryID, func(b *database.Book) error {
		undo.AuthorIDBefore = b.AuthorID
		b.AuthorID = &author.ID
		return nil
	})
	if ubErr != nil || b == nil {
		slog.Warn("combine override author ModifyBook", "id", primaryID, "err", ubErr)
	} else {
		id := author.ID
		undo.AuthorIDAfter = &id
		if lErr := database.RecordUserOverrides(ms.db, b.ID, map[string]any{
			// The CLEANED name, not the raw override: this row is
			// the lock that protects the field from later scans, so
			// it has to name the author actually linked above.
			// Recording the raw "001-147 Kevin J Anderson" would
			// lock the field to a value nothing else agrees with.
			database.FieldKeyAuthorName: cleanedAuthor,
		}); lErr != nil {
			slog.Warn("combine override author lock row", "id", b.ID, "err", lErr)
		}
	}
	return undo
}

// ensureOwnFile materializes a single-file book's FilePath as a BookFile when it
// has no BookFile rows yet (the virtual-segment model). Returns the journal
// record of what it did (FileID empty when nothing changed) and the number of
// files attached (0/1).
func (ms *Service) ensureOwnFile(b *database.Book) (CombineFileMove, int) {
	if b.FilePath == "" {
		return CombineFileMove{}, 0
	}
	if files, _ := ms.db.GetBookFiles(b.ID); len(files) > 0 {
		return CombineFileMove{}, 0 // already materialized
	}
	return ms.attachVirtualFile(b, b.ID)
}

// attachVirtualFile creates (or reattaches) a BookFile at book.FilePath owned by
// targetBookID. Reattach-safe per #1549: an existing row at that path is MOVED
// (its BookID can't be changed in place — the primary key embeds it), never
// duplicated. Returns the journal record (FileID empty when nothing changed:
// the row was already the target's) and files attached (0/1).
func (ms *Service) attachVirtualFile(b *database.Book, targetBookID string) (CombineFileMove, int) {
	existing, _ := ms.db.GetBookFileByPath(b.FilePath)
	if existing != nil {
		if existing.BookID == targetBookID {
			return CombineFileMove{}, 1
		}
		oldOwnerID := existing.BookID
		if err := ms.db.MoveBookFilesToBook([]string{existing.ID}, oldOwnerID, targetBookID); err != nil {
			slog.Warn("combine reattach existing file", "path", b.FilePath, "err", err)
			return CombineFileMove{}, 0
		}
		// Same file-move shape as the main CombineBooks loop: the row's
		// owning book id just changed, so its sync_file ino must follow.
		FollowFileMove(ms.db, oldOwnerID, targetBookID, []string{existing.ID})
		return CombineFileMove{
			FileID: existing.ID, FromBookID: oldOwnerID, FilePath: existing.FilePath,
			DiscBefore: existing.DiscNumber, TrackBefore: existing.TrackNumber,
		}, 1
	}
	bf := &database.BookFile{
		ID:       ulid.Make().String(),
		BookID:   targetBookID,
		FilePath: b.FilePath,
		Format:   strings.TrimPrefix(strings.ToLower(filepath.Ext(b.FilePath)), "."),
	}
	if b.Duration != nil {
		bf.Duration = *b.Duration
	}
	if err := ms.db.CreateBookFile(bf); err != nil {
		slog.Warn("combine create file", "path", b.FilePath, "err", err)
		return CombineFileMove{}, 0
	}
	return CombineFileMove{FileID: bf.ID, FromBookID: b.ID, FilePath: bf.FilePath, Created: true}, 1
}

// SoftDeleteBook marks a book as deleted using the MarkedForDeletion flag.
//
// It does NOT fall back to a hard delete when the update fails. It used to,
// on the theory that a zombie non-primary row was worse than no row; but a
// failing UpdateBook means the store is unhealthy, and answering that by
// running DeleteBook on the same store turns "a write failed" into "a book
// and its file rows are gone" — the wrong direction for the one function
// whose job is to keep the loser recoverable. The caller gets the error and
// decides.
func SoftDeleteBook(store BookWriter, bookID string) error {
	// ModifyBook: the two deletion columns are set on the row as it is under
	// the write lock, so a column another writer committed in the meantime is
	// not reverted by a whole-row write of a stale read. (nil, nil) is a book
	// that is already gone, which is not an error.
	if _, err := store.ModifyBook(bookID, func(b *database.Book) error {
		t := true
		now := time.Now()
		b.MarkedForDeletion = &t
		b.MarkedForDeletionAt = &now
		return nil
	}); err != nil {
		return fmt.Errorf("soft-delete %s: %w", bookID, err)
	}
	return nil
}

// IsITunesGhostPath reports whether a book's file path points at the
// iTunes media folder rather than the managed audiobook-organizer library.
func IsITunesGhostPath(p string) bool {
	if p == "" {
		return false
	}
	lower := strings.ToLower(p)
	return strings.Contains(lower, "/itunes media/") || strings.Contains(lower, "/itunes/itunes")
}

// BookCurationScore returns a coarse "how much effort has the user put into
// this entry" score. Higher means more curated.
//
// Signals, each worth one point:
//   - MetadataReviewStatus == "matched" (user explicitly accepted a match)
//   - LastWrittenAt set (tags have been written back to the file)
//   - MetadataUpdatedAt strictly newer than CreatedAt (user-visible metadata
//     field has been edited since the row was created)
func BookCurationScore(b *database.Book) int {
	score := 0
	if b.MetadataReviewStatus != nil && *b.MetadataReviewStatus == "matched" {
		score++
	}
	if b.LastWrittenAt != nil {
		score++
	}
	if b.MetadataUpdatedAt != nil && b.CreatedAt != nil && b.MetadataUpdatedAt.After(*b.CreatedAt) {
		score++
	}
	return score
}

// BookIsBetter returns true if a is a "better" primary version than b.
// Preference order (strongest first):
//  1. Organized library path over iTunes-ghost path
//  2. Higher curation score (user effort beats technical quality)
//  3. M4B over other formats
//  4. Higher bitrate
//  5. Larger file size
func BookIsBetter(a, b *database.Book) bool {
	aGhost := IsITunesGhostPath(a.FilePath)
	bGhost := IsITunesGhostPath(b.FilePath)
	if aGhost != bGhost {
		return !aGhost
	}

	aCur := BookCurationScore(a)
	bCur := BookCurationScore(b)
	if aCur != bCur {
		return aCur > bCur
	}

	aM4B := strings.EqualFold(a.Format, "m4b")
	bM4B := strings.EqualFold(b.Format, "m4b")
	if aM4B != bM4B {
		return aM4B
	}
	aBitrate := 0
	if a.Bitrate != nil {
		aBitrate = *a.Bitrate
	}
	bBitrate := 0
	if b.Bitrate != nil {
		bBitrate = *b.Bitrate
	}
	if aBitrate != bBitrate {
		return aBitrate > bBitrate
	}
	aSize := int64(0)
	if a.FileSize != nil {
		aSize = *a.FileSize
	}
	bSize := int64(0)
	if b.FileSize != nil {
		bSize = *b.FileSize
	}
	return aSize > bSize
}
