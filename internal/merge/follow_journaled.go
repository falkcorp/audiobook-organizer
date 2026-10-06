// file: internal/merge/follow_journaled.go
// version: 1.7.0
// guid: 6a7e0c1a-cb17-41e5-bf0f-dd8903735f64
// last-edited: 2026-10-06

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// ErrNoSyncFollower is returned by FollowAbsorbedJournaled when the store
// cannot carry sync identity or listening progress (it does not implement
// database.SyncIdentityStore). A caller whose absorbed book has progress must
// then refuse to retire that book, or the progress is lost at the purge.
var ErrNoSyncFollower = errors.New("merge: store cannot carry sync identity or listening progress")

// FollowAbsorbedJournaled carries one absorbed book's sync identity and every
// user's listening progress onto survivorID -- what CombineBooks does for each
// absorbed book -- and returns the journal entries UndoCombine needs to put it
// back: CombineAbsorbed.Progress and CombineAbsorbed.SyncRedirected.
//
// It exists for merge paths outside Service (dedup.MergeSplitBookCluster, and
// through it the chapter-group and split-book merges), which moved files and
// external IDs but never followed progress, so a reader's position on a
// soft-deleted chapter book was lost when the purge removed it.
func FollowAbsorbedJournaled(db UserProgressMerger, survivorID, absorbedID string, slice *SliceMapping, persist func([]CombineUserProgress) error) ([]CombineUserProgress, bool, error) {
	follower := database.AsSyncIdentityStore(db)
	if follower == nil {
		return nil, false, ErrNoSyncFollower
	}
	users, err := db.ListUsers()
	if err != nil {
		return nil, false, fmt.Errorf("list users: %w", err)
	}
	completePendingInvolving(db, []string{survivorID, absorbedID})
	// Per user (snapshotPair): a user whose progress cannot be read is
	// skipped and logged, their rows left where they are, and only the users
	// in the before-snapshot are followed, so no progress moves unjournaled.
	before, followable, skipped := snapshotPair(db, users, absorbedID, survivorID)
	logSkippedFollow(absorbedID, "not journaled or followed; their rows stay on the absorbed book", skipped)
	// Persist the before-snapshot BEFORE the follow drains anything, so a
	// crash inside the follow still leaves undo what it needs to put the
	// absorbed book's progress back.
	if persist != nil {
		if err := persist(buildProgressJournal(before, progressSnap{})); err != nil {
			return nil, false, fmt.Errorf("journal progress before follow: %w", err)
		}
	}
	// followOneLoser writes the pending-repair record first; users skipped
	// above keep it alive so the sweep moves them once they read cleanly. An
	// error here means the move failed AND no record holds it: the caller
	// must not retire the absorbed book.
	if followable == nil {
		followable = []database.User{} // nil would mean every user
	}
	if err := followOneLoser(db, follower, survivorID, absorbedID, followable, slice, len(skipped) > 0); err != nil {
		return buildProgressJournal(before, progressSnap{}), true, err
	}
	after, _, afterSkipped := snapshotPair(db, followable, absorbedID, survivorID)
	// A user whose after-snapshot failed has no after-state in the journal;
	// undo then leaves their survivor progress as is (safe).
	logSkippedFollow(absorbedID, "followed, but the after-snapshot failed; undo leaves their survivor progress as is", afterSkipped)
	return buildProgressJournal(before, after), true, nil
}

// SliceMapping says where an absorbed book's audio starts in the survivor's
// timeline when the absorbed book is a SLICE of the survivor (a chapter or a
// split part), not another copy of the whole book.
type SliceMapping struct {
	OffsetSeconds float64
	// Mappable is false when the offset cannot be computed (a preceding
	// file's duration is unknown); no position is carried then.
	Mappable bool
}

// followSliceFor is mergeUserProgressFor for an absorbed book that is a SLICE of the
// survivor (a chapter / split part). Whole-book dedup merges compare
// ProgressPct across two different-length copies of one book and carry the
// further one's state, Finished and its iTunes play-count mark included. For a
// slice that is wrong: finishing chapter 2 is not finishing the book, and
// carrying that Finished would also suppress the next real iTunes play count.
// So here:
//
//   - the survivor's state (Finished included) is never replaced; a survivor
//     with no state gets an in-progress one, never Finished;
//   - no play-count mark is carried;
//   - the absorbed book's latest position is mapped into the survivor's
//     timeline (slice offset + position) and written only when it is further
//     than the survivor's own latest position, and only when Mappable;
//   - the rest of the whole-book rule still applies (user_state_merge.go):
//     last played (LastActivityAt) is the later of the two and
//     HideFromContinueListening is kept if either side has it. Bookmarks are
//     copied by moveLoserState like any merge.
//
// The absorbed side is drained as FollowMerge does. Nothing is lost: the
// caller journaled the absorbed book's state and positions beforehand, and
// UndoCombine writes them back.
func followSliceFor(db userPositionStore, userID, survivorID, absorbedID string, slice SliceMapping) error {
	loserState, err := db.GetUserBookState(userID, absorbedID)
	if err != nil {
		return fmt.Errorf("get absorbed state: %w", err)
	}
	loserPositions, err := db.ListUserPositionsForBook(userID, absorbedID)
	if err != nil {
		return fmt.Errorf("list absorbed positions: %w", err)
	}
	if !hasCarryableState(loserState, loserPositions) {
		return nil
	}
	var loserLatest *database.UserPosition
	for i := range loserPositions {
		if loserLatest == nil || loserPositions[i].UpdatedAt.After(loserLatest.UpdatedAt) {
			loserLatest = &loserPositions[i]
		}
	}
	survPositions, err := db.ListUserPositionsForBook(userID, survivorID)
	if err != nil {
		return fmt.Errorf("list survivor positions: %w", err)
	}
	var survLatest *database.UserPosition
	for i := range survPositions {
		if survLatest == nil || survPositions[i].UpdatedAt.After(survLatest.UpdatedAt) {
			survLatest = &survPositions[i]
		}
	}
	if slice.Mappable && loserLatest != nil {
		mapped := slice.OffsetSeconds + loserLatest.PositionSeconds
		if survLatest == nil || mapped > survLatest.PositionSeconds {
			seg := loserLatest.SegmentID
			if survLatest != nil {
				seg = survLatest.SegmentID
			}
			if err := carryPosition(db, userID, survivorID, database.UserPosition{SegmentID: seg, PositionSeconds: mapped, UpdatedAt: loserLatest.UpdatedAt}); err != nil {
				return fmt.Errorf("carry mapped position: %w", err)
			}
		}
	}
	survState, err := db.GetUserBookState(userID, survivorID)
	if err != nil {
		return fmt.Errorf("get survivor state: %w", err)
	}
	if survState != nil && loserState != nil {
		// Last played = max, hide = OR (the parts of the whole-book rule a
		// slice keeps). Status, FinishedAt and progress stay the survivor's.
		upd := *survState
		changed := false
		if loserState.LastActivityAt.After(upd.LastActivityAt) {
			upd.LastActivityAt = loserState.LastActivityAt
			changed = true
		}
		if loserState.HideFromContinueListening && !upd.HideFromContinueListening {
			upd.HideFromContinueListening = true
			changed = true
		}
		if changed {
			if err := db.SetUserBookState(&upd); err != nil {
				return fmt.Errorf("carry last-played/hide onto survivor: %w", err)
			}
		}
	}
	if survState == nil && loserState != nil && loserState.Status != "" {
		started := *loserState
		started.BookID = survivorID
		started.Status = database.UserBookStatusInProgress
		started.StatusManual = false
		started.FinishedAt = nil
		started.ProgressPct = 0
		started.TotalListenedSeconds = 0
		started.LastSegmentID = ""
		if err := db.SetUserBookState(&started); err != nil {
			return fmt.Errorf("start survivor state: %w", err)
		}
	}
	if len(loserPositions) > 0 {
		if err := db.ClearUserPositions(userID, absorbedID); err != nil {
			return fmt.Errorf("clear absorbed positions: %w", err)
		}
	}
	if loserState != nil {
		if err := db.SetUserBookState(drainedUserState(*loserState)); err != nil {
			return fmt.Errorf("drain absorbed state: %w", err)
		}
	}
	return nil
}

// BookHasUserProgress reports whether any user has a book state or a stored
// position on bookID.
func BookHasUserProgress(db UserProgressMerger, bookID string) (bool, error) {
	users, err := db.ListUsers()
	if err != nil {
		return false, fmt.Errorf("list users: %w", err)
	}
	states, positions, err := snapshotProgress(db, users, bookID)
	if err != nil {
		return false, err
	}
	return len(states) > 0 || len(positions) > 0, nil
}

// UserStateReader is what reading every user's listening state on one book
// needs.
type UserStateReader interface {
	ListUsers() ([]database.User, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
}

// BookHasCarryableUserState reports whether any user still has listening
// state on bookID that a merge follow would carry: a stored position, a
// book state with a status, progress, listened time, segment or hide flag
// (hasCarryableState), or a bookmark no live book has yet (owedBookmarks:
// a bookmark under bookID's own sync id that the book its id redirects to
// does not hold at that time). Unlike BookHasUserProgress it does not count
// the drained row a completed follow leaves on a merge loser, nor the
// bookmarks a follow already copied to the survivor, so a loser whose state
// moved reads false. An error means the answer is unknown.
//
// The automatic hard deletes refuse a book this reports true for, because
// deleting it would drop state that never reached a live book (a follow that
// failed with no repair record, a follow still pending repair, a duplicate no
// merge ever followed): audiobooks.PurgeSoftDeletedBooks,
// reconcile.CleanupDuplicateVersionGroups and the iTunes regroup apply. The
// iTunes clone rollback carries the clone's state to its source first and
// uses this to confirm the move finished.
//
// The hard deletes a user asks for through the UI or API are guarded the
// same way (owner decision 2026-10-05): the single-book delete / "Purge
// now" (audiobooks DeleteAudiobook) and the batch hard delete
// (batch/service.go) carry the state to a version Audiobookshelf lists and
// then delete, or refuse (HardDeleteKeepingUserState); the owner drops
// state on purpose only through "Discard progress and purge"
// (DiscardUserStateThenHardDelete). NOT guarded: the diagnostics CLI's
// confirmed delete of invalid records (cmd/diagnostics.go, an operator's
// repair of rows too broken to list), and the rollbacks that delete a row a
// request just created (organizer, the versions handlers), which no user
// can have listened to.
func BookHasCarryableUserState(db UserStateReader, bookID string) (bool, error) {
	probe, err := NewUserStateProbe(db)
	if err != nil {
		return false, err
	}
	return probe.Has(bookID)
}

// UserStateProbe is BookHasCarryableUserState for a loop over many books: it
// lists the users once, when it is made, instead of once per book. A user
// created after that is not seen, so make one per pass, not per process.
type UserStateProbe struct {
	db    UserStateReader
	users []database.User
}

// NewUserStateProbe lists db's users once. It fails closed on a user row it
// cannot decode: when the store can say so (database.StrictUserLister,
// resolved through decorators), such a row is an error, not a user silently
// left out, because the probe's "no state" would then be a guess for that
// user and the caller is about to hard-delete on it.
func NewUserStateProbe(db UserStateReader) (*UserStateProbe, error) {
	var users []database.User
	var err error
	if strict, ok := database.AsCapability[database.StrictUserLister](db); ok {
		users, err = strict.ListUsersStrict()
	} else {
		users, err = db.ListUsers()
	}
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return &UserStateProbe{db: db, users: users}, nil
}

// Has reports whether any of the probe's users has carryable state on bookID
// (see BookHasCarryableUserState). An error means the answer is unknown.
func (p *UserStateProbe) Has(bookID string) (bool, error) {
	for _, u := range p.users {
		if u.ID == "" {
			continue
		}
		st, err := p.db.GetUserBookState(u.ID, bookID)
		if err != nil {
			return false, fmt.Errorf("read progress state user=%s book=%s: %w", u.ID, bookID, err)
		}
		pos, err := p.db.ListUserPositionsForBook(u.ID, bookID)
		if err != nil {
			return false, fmt.Errorf("read positions user=%s book=%s: %w", u.ID, bookID, err)
		}
		if hasCarryableState(st, pos) {
			return true, nil
		}
	}
	return p.owedBookmarks(bookID)
}

// ErrBookmarkCheckRedirectBroken is returned (wrapping
// database.ErrSyncRedirectChainBroken) by UserStateProbe.Has and so by
// BookHasCarryableUserState when the book's sync id's merge redirect chain
// is dangling, cyclic or too long, so whether it holds bookmarks no live book
// has cannot be told. Fail closed: the hard deletes refuse the book. Unlike
// an I/O error this is PERMANENT -- every nightly purge refuses the book
// again until the redirect is repaired -- so it carries its own reason for
// the owner instead of the generic "cannot read listening state".
var ErrBookmarkCheckRedirectBroken = errors.New("bookmarks cannot be checked: the book's sync id redirect chain is broken (dangling, cyclic or too long); permanent until the redirect is repaired, retrying will not help")

// ErrBookmarkCheckAliasLimit is returned (wrapping database.ErrSyncAliasLimit)
// like ErrBookmarkCheckRedirectBroken when the book's sync id has more merged
// aliases than the alias cap, so the aliases' bookmarks cannot all be
// checked. Also permanent until the alias graph shrinks or the cap is raised.
var ErrBookmarkCheckAliasLimit = errors.New("bookmarks cannot be checked: the book's sync id has more merged aliases than the alias cap; permanent until the alias graph is repaired, retrying will not help")

// classifySyncGraphErr wraps a permanent sync-graph error from
// ResolveSyncItem or ListSyncAliases in its owner-facing sentinel, keeping
// the database error in the chain; any other error is wrapped as given.
func classifySyncGraphErr(err error, what string) error {
	switch {
	case errors.Is(err, database.ErrSyncRedirectChainBroken):
		return fmt.Errorf("%w: %s: %w", ErrBookmarkCheckRedirectBroken, what, err)
	case errors.Is(err, database.ErrSyncAliasLimit):
		return fmt.Errorf("%w: %s: %w", ErrBookmarkCheckAliasLimit, what, err)
	default:
		return fmt.Errorf("%s: %w", what, err)
	}
}

// owedBookmarks reports whether any of the probe's users has a bookmark
// that only bookID still makes reachable. Bookmarks are copied, never moved,
// by a merge or a carry (bookmark_copy.go): each book keeps its own rows and
// its sync id redirects to the survivor's. So, with own = bookID's sync id
// and canonical = what own resolves to:
//
//   - own not redirected (canonical == own): every bookmark on own is the
//     only copy, and counts;
//   - redirected: a bookmark on own counts only when canonical lacks a
//     bookmark at that time (progress.CanonicalTimeKey) -- the copy did not
//     happen or did not finish;
//   - either way, a bookmark under an ALIAS of own (a book merged into this
//     one earlier, whose id redirects here) counts when canonical lacks it:
//     a merge from before bookmarks were copied (2026-09-26) left it
//     reachable only through this book's id, and deleting this book would
//     strand it. A carry copies aliases' bookmarks too (CopyAliasBookmarks
//     walks the aliases of the survivor, which then include these).
//
// A store with no bookmark or sync-identity keyspace, or a book never given
// a sync id, has none. Any read error is returned (fail closed: the caller
// is deciding whether a hard delete is safe); a broken redirect chain or an
// alias graph over the cap is returned as ErrBookmarkCheckRedirectBroken /
// ErrBookmarkCheckAliasLimit so the refusal says why it will not clear.
func (p *UserStateProbe) owedBookmarks(bookID string) (bool, error) {
	bs := database.AsBookmarkStore(p.db)
	ids := database.AsSyncIdentityStore(p.db)
	if bs == nil || ids == nil {
		return false, nil
	}
	own, found, err := ids.GetSyncIDForBook(bookID)
	if err != nil {
		return false, fmt.Errorf("read sync id of %s: %w", bookID, err)
	}
	if !found || own == "" {
		return false, nil
	}
	canonical := own
	item, err := ids.ResolveSyncItem(own)
	if err != nil {
		return false, classifySyncGraphErr(err, fmt.Sprintf("resolve sync id %s of %s", own, bookID))
	}
	if item != nil && item.SyncID != "" {
		canonical = item.SyncID
	}
	aliases, err := ids.ListSyncAliases(own)
	if err != nil {
		return false, classifySyncGraphErr(err, fmt.Sprintf("list aliases of sync id %s of %s", own, bookID))
	}
	for _, u := range p.users {
		if u.ID == "" {
			continue
		}
		have, err := bs.ListBookmarks(u.ID, canonical)
		if err != nil {
			return false, fmt.Errorf("read bookmarks user=%s on %s: %w", u.ID, canonical, err)
		}
		if canonical == own && len(have) > 0 {
			return true, nil
		}
		present := make(map[string]bool, len(have))
		for i := range have {
			present[progress.CanonicalTimeKey(have[i].TimeSec)] = true
		}
		for _, src := range append([]string{own}, aliases...) {
			if src == "" || src == canonical {
				continue
			}
			marks, err := bs.ListBookmarks(u.ID, src)
			if err != nil {
				return false, fmt.Errorf("read bookmarks user=%s on %s (book %s): %w", u.ID, src, bookID, err)
			}
			for i := range marks {
				if !present[progress.CanonicalTimeKey(marks[i].TimeSec)] {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// logSkippedFollow logs each user skipped by a journaled follow.
func logSkippedFollow(absorbedID, what string, skipped map[string]error) {
	for u, err := range skipped {
		mlog.Warn("merge-follow: progress of user=%s on absorbed=%s %s: %v",
			logger.SanitizeLogValue(u), logger.SanitizeLogValue(absorbedID), what, err)
	}
}
