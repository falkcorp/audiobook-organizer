// file: internal/merge/sibling_journal.go
// version: 1.0.0
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
const siblingJournalPrefix = "merge:sibling-journal:"

// Sibling-move journal statuses.
const (
	// SiblingJournalPending is written before the merge's first write. A
	// journal left in this state names a merge that failed or crashed part
	// way; it is still undoable, because the restore checks each sibling's
	// current group and leaves one that never moved alone.
	SiblingJournalPending = "pending"
	// SiblingJournalApplied marks a merge that moved every sibling.
	SiblingJournalApplied = "applied"
	// SiblingJournalUndone marks a journal UndoSiblingMove reversed.
	SiblingJournalUndone = "undone"
)

// ErrSiblingJournalNotFound is returned for an unknown journal id.
var ErrSiblingJournalNotFound = errors.New("sibling-move journal not found")

// SiblingMoveJournal is the undo record for the siblings one merge moved.
type SiblingMoveJournal struct {
	ID          string         `json:"id"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	SurvivorID  string         `json:"survivor_id"`
	IntoGroupID string         `json:"into_group_id"`
	Losers      []string       `json:"losers"`
	Siblings    []MovedSibling `json:"siblings"`
	UndoneAt    *time.Time     `json:"undone_at,omitempty"`
	// Warnings names siblings an undo left where they were because they had
	// moved to some other group since the merge.
	Warnings  []string `json:"warnings,omitempty"`
	LastError string   `json:"last_error,omitempty"`
}

// SiblingRestoreResult is what RestoreMovedSiblings did.
type SiblingRestoreResult struct {
	// Restored were in the merge's group and are back in their old one.
	Restored []string `json:"restored"`
	// AlreadyBack were already in their old group (an earlier undo).
	AlreadyBack []string `json:"already_back,omitempty"`
	// MovedOn maps a sibling that has moved to a third group since the merge
	// to that group. It is left there: that move was a later decision.
	MovedOn map[string]string `json:"moved_on,omitempty"`
}

// SiblingUndoResult is the outcome of UndoSiblingMove.
type SiblingUndoResult struct {
	JournalID string `json:"journal_id"`
	SiblingRestoreResult
}

func siblingJournalKey(id string) string { return siblingJournalPrefix + id }

func (ms *Service) putSiblingJournal(j *SiblingMoveJournal) error {
	data, err := json.Marshal(j)
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
// (0 = all).
func (ms *Service) ListSiblingMoveJournals(limit int) ([]SiblingMoveJournal, error) {
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
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UndoSiblingMove puts every sibling one merge moved back in its old group
// with its old flag (RestoreMovedSiblings). It accepts a pending journal (a
// merge that failed part way) and an undone one (a repeat): the restore is
// idempotent. It does not touch the merge's named books; undoing the merge
// itself is the caller's own path (UnmergeAuto, or restoring a loser from the
// trash). On a store error the journal keeps its status and records
// LastError, and the undo can be re-run.
func (ms *Service) UndoSiblingMove(journalID string) (*SiblingUndoResult, error) {
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	j, err := ms.GetSiblingMoveJournal(journalID)
	if err != nil {
		return nil, err
	}
	res, restoreErr := ms.restoreMovedSiblings(j.IntoGroupID, j.Siblings)
	if restoreErr != nil {
		j.LastError = restoreErr.Error()
		if err := ms.putSiblingJournal(j); err != nil {
			mlog.Warn("sibling-move undo: journal %s error not recorded: %s", logger.SanitizeLogValue(j.ID), logger.SanitizeLogValue(fmt.Sprint(err)))
		}
		return nil, fmt.Errorf("undo sibling move %s: %w", j.ID, restoreErr)
	}
	now := time.Now().UTC()
	j.Status, j.UndoneAt, j.LastError = SiblingJournalUndone, &now, ""
	j.Warnings = nil
	for _, id := range slices.Sorted(maps.Keys(res.MovedOn)) {
		j.Warnings = append(j.Warnings, fmt.Sprintf("book %s moved to version group %s since the merge; left there", id, res.MovedOn[id]))
	}
	if err := ms.putSiblingJournal(j); err != nil {
		return nil, fmt.Errorf("siblings restored but sibling-move journal %s not marked undone: %w", j.ID, err)
	}
	return &SiblingUndoResult{JournalID: j.ID, SiblingRestoreResult: res}, nil
}

// RestoreMovedSiblings is the shared sibling restore: UndoSiblingMove and
// dedup's UnmergeAuto both call it. It takes the merge lock; a caller already
// holding it uses restoreMovedSiblings.
func (ms *Service) RestoreMovedSiblings(intoGroupID string, siblings []MovedSibling) (SiblingRestoreResult, error) {
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()
	return ms.restoreMovedSiblings(intoGroupID, siblings)
}

// restoreMovedSiblings puts each sibling still in intoGroupID back in its
// FromGroupID with its exact pre-merge flag pointer. Only those two fields are
// written, through ModifyBook on the fresh row, under both groups' locks. A
// sibling already back is left alone; one that moved to a third group since
// is left there and reported in MovedOn.
//
// Each group a sibling went back to then gets versionprimary.
// EnsureSinglePrimary, after the locks are released (it takes them itself):
// on a path that leaves the loser retired, the restored group has no primary
// unless one of its siblings was it, and a merge that failed part way may
// have elected one in the left group that a restored sibling's old flag now
// duplicates. A hand-off failure is logged, as handOffLeftGroups does: the
// membership is restored, and version-group-primary-repair covers the flag.
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
