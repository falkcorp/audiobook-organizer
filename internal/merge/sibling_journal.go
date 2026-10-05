// file: internal/merge/sibling_journal.go
// version: 1.2.0
// guid: 225d6301-f6b7-4f2d-8c27-1d6865ecf11c
// last-edited: 2026-10-05

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	ulid "github.com/oklog/ulid/v2"
)

// Sibling-move journal.
//
// A merge carries each live loser's version-group siblings into the merge's
// group (MergeBooks item 6). Those are books nobody named in the merge, so
// the move must be reversible from every merge path, not only from the dedup
// paths that write an AutoMergeJournalEntry. MergeBooksWithOptions therefore
// writes one SiblingMoveJournal itself, under the version-group locks and
// BEFORE its first write, whenever it is about to move a sibling; a merge that
// cannot write it is refused with nothing changed. UndoSiblingMove replays it
// backwards.
//
// WHY HERE AND NOT dedup.MergeBooksJournaled. That journal lives on the
// EmbeddingStore and needs a dedup Engine; MergeBooksJournaled refuses to merge
// without one. The callers that do not go through it (applyBookMergeReroute,
// link-as-versions, diagnostics merge_versions, maintenance BookMerger) would
// either have to be wired to the engine or refuse whenever the embedding
// store is down, and any future caller of Service would skip the journal
// again. Writing it at the chokepoint means the code that moves the siblings
// is the code that records the move.
//
// WHERE IT LIVES. Raw keys on the main store, like the combine journal:
//
//	merge:sibling-journal:<ULID> -> SiblingMoveJournal JSON
//
// WHEN AN UNDO IS REFUSED. An undo puts a sibling back only when this
// journal's merge is the latest one that moved it into IntoGroupID. Modelled
// on UndoCombine's state rules (undoPreconditions): the status decides first,
// then the current state of each row.
//
//   - undone: refused. Every sibling is already back, and replaying the
//     journal after a later merge moved one of them again would revert that
//     later merge.
//   - aborted: refused. The merge was refused after the journal was written
//     and before its first write, so it moved nothing.
//   - pending or applied: allowed, sibling by sibling. A sibling an earlier
//     undo already resolved (UndoneSiblings) is skipped. The whole undo is
//     refused when a NEWER journal that is applied or pending names one of
//     the remaining siblings with the same IntoGroupID and has not undone it:
//     that sibling is in the group because of the newer merge, not this one.
//     Pending counts as well as applied because a pending journal is a merge
//     that failed part way, and it may have moved the sibling.
const siblingJournalPrefix = "merge:sibling-journal:"

// Sibling-move journal statuses.
const (
	// SiblingJournalPending is written before the merge's first write. A
	// journal left in this state names a merge that failed or crashed part
	// way; it is still undoable, because the restore checks each sibling's
	// current group and leaves one that never moved alone. Moved records the
	// siblings whose write landed when the merge saw its own failure.
	SiblingJournalPending = "pending"
	// SiblingJournalApplied marks a merge that moved every sibling.
	SiblingJournalApplied = "applied"
	// SiblingJournalUndone marks a journal whose every sibling an undo has
	// resolved (put back, found already back, or found moved on).
	SiblingJournalUndone = "undone"
	// SiblingJournalAborted marks a merge refused after the journal was
	// written and before its first membership write. Nothing moved, so it
	// cannot be undone.
	SiblingJournalAborted = "aborted"
)

// SiblingJournalStaleAfter is how long a journal may stay pending before the
// list marks it Stale. A merge holds a journal pending for the length of its
// membership writes, which is well under a second; one still pending minutes
// later is a merge that failed or crashed part way.
const SiblingJournalStaleAfter = 10 * time.Minute

// ErrSiblingJournalNotFound is returned for an unknown journal id.
var ErrSiblingJournalNotFound = errors.New("sibling-move journal not found")

// ErrSiblingUndoRefused wraps every reason an undo is refused (see "WHEN AN
// UNDO IS REFUSED" above). Nothing is written when it is returned.
var ErrSiblingUndoRefused = errors.New("sibling-move undo refused")

// SiblingMoveJournal is the undo record for the siblings one merge moved.
type SiblingMoveJournal struct {
	ID          string         `json:"id"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	SurvivorID  string         `json:"survivor_id"`
	IntoGroupID string         `json:"into_group_id"`
	Losers      []string       `json:"losers"`
	Siblings    []MovedSibling `json:"siblings"`
	// Moved names the siblings whose move landed. Set when the merge
	// finishes, and when it fails part way, so the record says what actually
	// moved; Siblings stays the full plan, because a write that reported an
	// error may still have landed and the undo checks each row anyway.
	Moved []string `json:"moved,omitempty"`
	// UndoneSiblings are the siblings an undo has resolved. An undo of one
	// loser's entry (UndoSiblingMoveForLoser) resolves only that loser's
	// siblings; the journal is undone once every sibling is here.
	UndoneSiblings []string   `json:"undone_siblings,omitempty"`
	UndoneAt       *time.Time `json:"undone_at,omitempty"`
	// Warnings names siblings an undo left where they were because they had
	// moved to some other group since the merge.
	Warnings  []string `json:"warnings,omitempty"`
	LastError string   `json:"last_error,omitempty"`
	// Stale is set by ListSiblingMoveJournals on a journal still pending
	// SiblingJournalStaleAfter after it was written. Never stored.
	Stale bool `json:"stale,omitempty"`
	// FlagHolderID is the book that took the group's primary flag when it is
	// not SurvivorID (Result.GroupPrimaryID). The losers' user state and sync
	// redirect followed it, one StateFollows entry per loser.
	FlagHolderID string        `json:"flag_holder_id,omitempty"`
	StateFollows []StateFollow `json:"state_follows,omitempty"`
}

// StateFollow is one loser's journaled user-state follow onto the flag
// holder (FollowAbsorbedJournaled): what RestoreFollowedProgress needs to put
// it back.
type StateFollow struct {
	LoserID        string                `json:"loser_id"`
	HolderID       string                `json:"holder_id"`
	SyncRedirected bool                  `json:"sync_redirected"`
	Progress       []CombineUserProgress `json:"progress,omitempty"`
	// Undone is set once an undo has reversed this follow.
	Undone bool `json:"undone,omitempty"`
}

// followOntoFlagHolder carries each loser's user state and sync redirect onto
// j.FlagHolderID, journaling each follow on j before it drains anything
// (FollowAbsorbedJournaled's persist hook), so an undo can reverse it. A store
// with no sync layer cannot journal the follow; it then falls back to the
// unjournaled FollowMerge onto the flag holder, logged. The caller holds
// mergeSerializeMu.
func (ms *Service) followOntoFlagHolder(j *SiblingMoveJournal, losers []string) error {
	var errs []error
	for _, loser := range losers {
		idx := len(j.StateFollows)
		j.StateFollows = append(j.StateFollows, StateFollow{LoserID: loser, HolderID: j.FlagHolderID})
		progress, redirected, err := FollowAbsorbedJournaled(ms.db, j.FlagHolderID, loser, nil, func(before []CombineUserProgress) error {
			j.StateFollows[idx].Progress = before
			j.StateFollows[idx].SyncRedirected = true
			return ms.putSiblingJournal(j)
		})
		if errors.Is(err, ErrNoSyncFollower) {
			j.StateFollows = j.StateFollows[:idx]
			mlog.Warn("merge: store has no sync layer; loser %s state follows flag holder %s unjournaled",
				logger.SanitizeLogValue(loser), logger.SanitizeLogValue(j.FlagHolderID))
			if ferr := FollowMerge(ms.db, ms.syncFollower, j.FlagHolderID, []string{loser}); ferr != nil {
				errs = append(errs, ferr)
			}
			continue
		}
		if progress != nil {
			j.StateFollows[idx].Progress = progress
		}
		j.StateFollows[idx].SyncRedirected = redirected
		if err != nil {
			errs = append(errs, fmt.Errorf("follow loser %s onto flag holder %s: %w", loser, j.FlagHolderID, err))
		}
	}
	if err := ms.putSiblingJournal(j); err != nil {
		// The before-snapshots are already stored (persist hook), which is
		// what an undo needs to put each loser's state back; without the
		// after-snapshots it leaves the flag holder's own progress as is.
		mlog.Warn("merge: sibling-move journal %s: state-follow after-snapshots not recorded: %s",
			logger.SanitizeLogValue(j.ID), logger.SanitizeLogValue(fmt.Sprint(err)))
	}
	return errors.Join(errs...)
}

// undoStateFollows reverses the journaled follows pick selects
// (RestoreFollowedProgress: each loser's state and positions back on the
// loser, the flag holder's back to what it was unless the user has listened
// there since, the redirect cleared). With refollow, each loser still retired
// afterwards then has its state followed onto the survivor, which is where it
// would have gone had the flag stayed there: a retired loser must not keep
// state the purge would drop. It returns the warnings and the first error.
func (ms *Service) undoStateFollows(j *SiblingMoveJournal, pick func(StateFollow) bool, refollow bool) ([]string, error) {
	var warnings []string
	for i := range j.StateFollows {
		f := &j.StateFollows[i]
		if f.Undone || !pick(*f) {
			continue
		}
		w, err := RestoreFollowedProgress(ms.db, f.HolderID, f.LoserID, f.SyncRedirected, f.Progress)
		warnings = append(warnings, w...)
		if err != nil {
			return warnings, fmt.Errorf("put loser %s's state back from %s: %w", f.LoserID, f.HolderID, err)
		}
		f.Undone = true
		if !refollow {
			continue
		}
		b, err := ms.db.GetBookByID(f.LoserID)
		if err != nil {
			return warnings, fmt.Errorf("read loser %s: %w", f.LoserID, err)
		}
		if b != nil && b.IsSoftDeleted() {
			if err := FollowMerge(ms.db, ms.syncFollower, j.SurvivorID, []string{f.LoserID}); err != nil {
				return warnings, fmt.Errorf("follow loser %s onto survivor %s: %w", f.LoserID, j.SurvivorID, err)
			}
		}
	}
	return warnings, nil
}

// SiblingRestoreResult is what restoreMovedSiblings did.
type SiblingRestoreResult struct {
	// Restored were in the merge's group and are back in their old one.
	Restored []string `json:"restored"`
	// AlreadyBack were already in their old group (an earlier undo, or a
	// pending journal's sibling that never moved).
	AlreadyBack []string `json:"already_back,omitempty"`
	// MovedOn maps a sibling that has moved to a third group since the merge
	// to that group. It is left there: that move was a later decision.
	MovedOn map[string]string `json:"moved_on,omitempty"`
}

// SiblingUndoResult is the outcome of UndoSiblingMove.
type SiblingUndoResult struct {
	JournalID string `json:"journal_id"`
	// Status is the journal's status after this undo: undone once every
	// sibling is resolved, otherwise what it was.
	Status string `json:"status"`
	SiblingRestoreResult
}

func siblingJournalKey(id string) string { return siblingJournalPrefix + id }

func (ms *Service) putSiblingJournal(j *SiblingMoveJournal) error {
	stored := *j
	stored.Stale = false
	data, err := json.Marshal(&stored)
	if err != nil {
		return fmt.Errorf("marshal sibling-move journal %s: %w", j.ID, err)
	}
	if err := ms.db.SetRaw(siblingJournalKey(j.ID), data); err != nil {
		return fmt.Errorf("write sibling-move journal %s: %w", j.ID, err)
	}
	return nil
}

func newSiblingJournal(survivorID, intoGroupID string, losers []string, siblings []MovedSibling) *SiblingMoveJournal {
	return &SiblingMoveJournal{
		ID:          ulid.Make().String(),
		Status:      SiblingJournalPending,
		CreatedAt:   time.Now().UTC(),
		SurvivorID:  survivorID,
		IntoGroupID: intoGroupID,
		Losers:      losers,
		Siblings:    siblings,
	}
}

// GetSiblingMoveJournal returns the journal with the given id, or
// ErrSiblingJournalNotFound.
func (ms *Service) GetSiblingMoveJournal(id string) (*SiblingMoveJournal, error) {
	if id == "" || strings.ContainsAny(id, ":/") {
		return nil, ErrSiblingJournalNotFound
	}
	data, err := ms.db.GetRaw(siblingJournalKey(id))
	if err != nil {
		return nil, fmt.Errorf("read sibling-move journal %s: %w", id, err)
	}
	if data == nil {
		return nil, ErrSiblingJournalNotFound
	}
	var j SiblingMoveJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("decode sibling-move journal %s: %w", id, err)
	}
	return &j, nil
}

// ListSiblingMoveJournals returns journals newest-first, capped at limit
// (0 = all), with Stale set on each pending one older than
// SiblingJournalStaleAfter.
func (ms *Service) ListSiblingMoveJournals(limit int) ([]SiblingMoveJournal, error) {
	out, err := ms.scanSiblingJournals()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range out {
		out[i].Stale = out[i].Status == SiblingJournalPending && now.Sub(out[i].CreatedAt) > SiblingJournalStaleAfter
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// scanSiblingJournals reads every journal, newest-first. An undecodable row
// is logged and skipped.
func (ms *Service) scanSiblingJournals() ([]SiblingMoveJournal, error) {
	rows, err := ms.db.ScanPrefix(siblingJournalPrefix)
	if err != nil {
		return nil, fmt.Errorf("list sibling-move journals: %w", err)
	}
	out := make([]SiblingMoveJournal, 0, len(rows))
	for _, r := range rows {
		var j SiblingMoveJournal
		if err := json.Unmarshal(r.Value, &j); err != nil {
			mlog.Warn("sibling-move journal: skipping undecodable row key=%s err=%s", logger.SanitizeLogValue(r.Key), logger.SanitizeLogValue(fmt.Sprint(err)))
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID > out[k].ID })
	return out, nil
}

// UndoSiblingMove puts every sibling one merge moved back in its old group
// with its old flag (restoreMovedSiblings), subject to the refusals in "WHEN
// AN UNDO IS REFUSED" above. It does not touch the merge's named books;
// undoing the merge itself is the caller's own path (UnmergeAuto, or
// restoring a loser from the trash). On a store error the journal keeps its
// status, records LastError and the siblings that were resolved, and the
// undo can be re-run.
func (ms *Service) UndoSiblingMove(journalID string) (*SiblingUndoResult, error) {
	return ms.undoSiblingJournal(journalID, "")
}

// UndoSiblingMoveForLoser is UndoSiblingMove limited to the siblings that left
// with loserID (MovedSibling.WithLosers). dedup's UnmergeAuto undoes one
// loser at a time and calls this, so undoing one loser neither restores
// another loser's siblings nor marks the journal undone while that other
// loser's siblings are still in the merge's group. When every one of
// loserID's siblings is already resolved (an earlier UndoSiblingMove, say) it
// does nothing and succeeds, whatever the journal's status: there is nothing
// left for it to replay.
func (ms *Service) UndoSiblingMoveForLoser(journalID, loserID string) (*SiblingUndoResult, error) {
	if loserID == "" {
		return nil, fmt.Errorf("%w: no loser named", ErrSiblingUndoRefused)
	}
	return ms.undoSiblingJournal(journalID, loserID)
}

// undoSiblingJournal undoes the journal's siblings: all of them when loserID
// is empty, else those that left with loserID.
func (ms *Service) undoSiblingJournal(journalID, loserID string) (*SiblingUndoResult, error) {
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	j, err := ms.GetSiblingMoveJournal(journalID)
	if err != nil {
		return nil, err
	}
	var todo []MovedSibling
	for _, sib := range j.Siblings {
		if (loserID == "" || slices.Contains(sib.WithLosers, loserID)) && !slices.Contains(j.UndoneSiblings, sib.BookID) {
			todo = append(todo, sib)
		}
	}
	// The state follows this undo reverses. One loser's undo (UnmergeAuto,
	// which brings the loser back) reverses that loser's follow. A whole
	// undo reverses the follows onto a flag holder it moves back out of the
	// merge's group, and refollows those losers onto the survivor; a flag
	// holder that stays in the group (a reused-group member) keeps them.
	followsTodo := 0
	pickFollow := func(f StateFollow) bool {
		if loserID != "" {
			return f.LoserID == loserID
		}
		return slices.ContainsFunc(todo, func(s MovedSibling) bool { return s.BookID == f.HolderID })
	}
	for _, f := range j.StateFollows {
		if !f.Undone && pickFollow(f) {
			followsTodo++
		}
	}
	switch j.Status {
	case SiblingJournalUndone:
		if loserID != "" && len(todo) == 0 && followsTodo == 0 {
			return &SiblingUndoResult{JournalID: j.ID, Status: j.Status}, nil
		}
		if loserID == "" {
			return nil, fmt.Errorf("%w: journal %s is already undone; replaying it could revert a later merge", ErrSiblingUndoRefused, j.ID)
		}
	case SiblingJournalAborted:
		return nil, fmt.Errorf("%w: journal %s is aborted; its merge moved nothing", ErrSiblingUndoRefused, j.ID)
	case SiblingJournalPending, SiblingJournalApplied:
	default:
		return nil, fmt.Errorf("%w: journal %s has unknown status %q", ErrSiblingUndoRefused, j.ID, j.Status)
	}
	if err := ms.requireNoNewerSiblingMove(j, todo); err != nil {
		return nil, err
	}
	followWarnings, followErr := ms.undoStateFollows(j, pickFollow, loserID == "")
	j.Warnings = append(j.Warnings, followWarnings...)
	if followErr != nil {
		j.LastError = followErr.Error()
		if err := ms.putSiblingJournal(j); err != nil {
			mlog.Warn("sibling-move undo: journal %s error not recorded: %s", logger.SanitizeLogValue(j.ID), logger.SanitizeLogValue(fmt.Sprint(err)))
		}
		return nil, fmt.Errorf("undo sibling move %s: %w", j.ID, followErr)
	}
	res, restoreErr := ms.restoreMovedSiblings(j.IntoGroupID, todo)
	j.UndoneSiblings = append(j.UndoneSiblings, res.Restored...)
	j.UndoneSiblings = append(j.UndoneSiblings, res.AlreadyBack...)
	for _, id := range slices.Sorted(maps.Keys(res.MovedOn)) {
		j.UndoneSiblings = append(j.UndoneSiblings, id)
		j.Warnings = append(j.Warnings, fmt.Sprintf("book %s moved to version group %s since the merge; left there", id, res.MovedOn[id]))
	}
	// A group every one of whose siblings is back is no longer one the merge
	// emptied; drop its trash-restore redirect (group_redirect.go).
	ms.clearRestoredGroupRedirects(j, res.Restored)
	if restoreErr != nil {
		j.LastError = restoreErr.Error()
		if err := ms.putSiblingJournal(j); err != nil {
			mlog.Warn("sibling-move undo: journal %s error not recorded: %s", logger.SanitizeLogValue(j.ID), logger.SanitizeLogValue(fmt.Sprint(err)))
		}
		return nil, fmt.Errorf("undo sibling move %s: %w", j.ID, restoreErr)
	}
	j.LastError = ""
	if siblingJournalFullyUndone(j) {
		now := time.Now().UTC()
		j.Status, j.UndoneAt = SiblingJournalUndone, &now
	}
	if err := ms.putSiblingJournal(j); err != nil {
		return nil, fmt.Errorf("siblings restored but sibling-move journal %s not updated: %w", j.ID, err)
	}
	return &SiblingUndoResult{JournalID: j.ID, Status: j.Status, SiblingRestoreResult: res}, nil
}

func siblingJournalFullyUndone(j *SiblingMoveJournal) bool {
	for _, sib := range j.Siblings {
		if !slices.Contains(j.UndoneSiblings, sib.BookID) {
			return false
		}
	}
	return true
}

// requireNoNewerSiblingMove refuses an undo when a newer applied or pending
// journal moved one of todo's siblings into the same group and has not undone
// it: that sibling is where it is because of the newer merge.
func (ms *Service) requireNoNewerSiblingMove(j *SiblingMoveJournal, todo []MovedSibling) error {
	if len(todo) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, s := range todo {
		want[s.BookID] = true
	}
	all, err := ms.scanSiblingJournals()
	if err != nil {
		return fmt.Errorf("check later sibling moves before undoing %s: %w", j.ID, err)
	}
	for _, n := range all {
		if n.ID <= j.ID || n.IntoGroupID != j.IntoGroupID {
			continue
		}
		if n.Status != SiblingJournalApplied && n.Status != SiblingJournalPending {
			continue
		}
		for _, s := range n.Siblings {
			if want[s.BookID] && !slices.Contains(n.UndoneSiblings, s.BookID) {
				return fmt.Errorf("%w: book %s was moved into version group %s again by the later merge journal %s; undo that one first",
					ErrSiblingUndoRefused, s.BookID, j.IntoGroupID, n.ID)
			}
		}
	}
	return nil
}

// restoreMovedSiblings puts each sibling still in intoGroupID back in its
// FromGroupID with its exact pre-merge flag pointer. Only those two fields are
// written, through ModifyBook on the fresh row, under both groups' locks. A
// sibling already back is left alone; one that moved to a third group since
// is left there and reported in MovedOn. The caller holds mergeSerializeMu.
//
// Each group a sibling went back to, and intoGroupID itself when anything was
// restored, then gets versionprimary.EnsureSinglePrimary after the locks are
// released (it takes them itself). The left group needs it because on a path
// that leaves the loser retired it has no primary unless one of its siblings
// was it, and a merge that failed part way may have elected one there that a
// restored sibling's old flag now duplicates. The merge's own group needs it
// because a restored sibling may have been that group's primary (the merge
// elects an organized sibling when the survivor is not organized), which
// leaves the group with none. A hand-off failure is logged, as
// handOffLeftGroups does: the membership is restored, and
// version-group-primary-repair covers the flag.
func (ms *Service) restoreMovedSiblings(intoGroupID string, siblings []MovedSibling) (SiblingRestoreResult, error) {
	res := SiblingRestoreResult{}
	touched := map[string]bool{}
	var errs []error
	for _, sib := range siblings {
		outcome, movedOn, err := ms.restoreOneSibling(intoGroupID, sib)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("sibling %s: %w", sib.BookID, err))
		case outcome == "restored":
			res.Restored = append(res.Restored, sib.BookID)
			touched[sib.FromGroupID] = true
		case outcome == "already":
			res.AlreadyBack = append(res.AlreadyBack, sib.BookID)
		default:
			if res.MovedOn == nil {
				res.MovedOn = map[string]string{}
			}
			res.MovedOn[sib.BookID] = movedOn
		}
	}
	if len(res.Restored) > 0 && strings.TrimSpace(intoGroupID) != "" {
		touched[intoGroupID] = true
	}
	handOffLeftGroups(ms.db, touched)
	if len(errs) > 0 {
		return res, errors.Join(errs...)
	}
	return res, nil
}

func (ms *Service) restoreOneSibling(intoGroupID string, sib MovedSibling) (outcome, movedOn string, err error) {
	unlock := versionprimary.LockGroups(sib.FromGroupID, intoGroupID)
	defer unlock()
	stored, err := ms.db.ModifyBook(sib.BookID, func(b *database.Book) error {
		cur := ""
		if b.VersionGroupID != nil {
			cur = strings.TrimSpace(*b.VersionGroupID)
		}
		switch cur {
		case strings.TrimSpace(sib.FromGroupID):
			outcome = "already"
			return database.ErrSkipBookWrite
		case strings.TrimSpace(intoGroupID):
		default:
			outcome, movedOn = "moved_on", cur
			return database.ErrSkipBookWrite
		}
		from := sib.FromGroupID
		b.VersionGroupID = &from
		if sib.WasPrimary == nil {
			b.IsPrimaryVersion = nil
		} else {
			was := *sib.WasPrimary
			b.IsPrimaryVersion = &was
		}
		outcome = "restored"
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if stored == nil {
		return "", "", fmt.Errorf("book not found")
	}
	return outcome, movedOn, nil
}
