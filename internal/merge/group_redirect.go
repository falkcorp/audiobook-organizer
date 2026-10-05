// file: internal/merge/group_redirect.go
// version: 1.0.0
// guid: 6a0f4c2e-9b1d-4e57-8a3c-2d7e5f1b9c40
// last-edited: 2026-10-05

package merge

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Group redirect: where a group a merge emptied went.
//
// A merge unites each live loser's version group G into the merge's group H
// (MergeBooks item 6): every LIVE member of G moves. A member of G that was
// already in the trash does not: GetBooksByVersionGroup cannot see it, and no
// accessor lists a group's soft-deleted rows. Restored later, it came back
// into G, a group with no live member, apart from the rest of its family.
//
// The merge therefore records G -> H before its first write, and
// RestoreFromTrash follows it (TrashRestoreGroup): a row restored into a
// group the merge emptied lands in the group that group went to.
//
// WHY A REDIRECT AND NOT MOVING THE TRASHED ROWS TOO. Moving them needs a
// new Store method to list a group's soft-deleted members (the
// book:versiongroup: index has a known-incomplete fallback, so reading it
// raw is not a census), it rewrites rows in the trash (a book_ver snapshot
// each), and the merge's pre-write guards would then run on trashed rows, so a
// stale provisional file on a book in the trash could block a merge. The
// redirect writes one small key per emptied group and touches no book.
//
// WHEN IT IS FOLLOWED. All of: the row is in the trash and still in G; G has
// no live member (an undo, or anything else that put a live book back in G,
// makes G a family again); H has a live member; and the row went into the
// trash no later than the redirect was written (a row trashed afterwards was
// put back in G and trashed from there on purpose). Chains are followed while
// each hop still has no live member, up to maxGroupRedirectHops.
//
// LIFECYCLE. Written for every group a merge empties, siblings or not, in the
// merge's pre-write step: a merge that cannot write it is refused. Removed
// when that merge is refused before its first write, when a sibling undo puts
// a sibling back in G, and when dedup's UnmergeAuto puts the loser back
// (ClearGroupRedirect). Only a redirect still pointing at the same H is
// removed, so a newer merge's redirect for G survives.
//
//	merge:group-redirect:<G> -> GroupRedirect JSON
const groupRedirectPrefix = "merge:group-redirect:"

const maxGroupRedirectHops = 8

// GroupRedirect records that a merge emptied FromGroupID into IntoGroupID.
type GroupRedirect struct {
	FromGroupID string    `json:"from_group_id"`
	IntoGroupID string    `json:"into_group_id"`
	SurvivorID  string    `json:"survivor_id"`
	Losers      []string  `json:"losers"`
	CreatedAt   time.Time `json:"created_at"`
}

func groupRedirectKey(gid string) string { return groupRedirectPrefix + gid }

type rawGetter interface {
	GetRaw(key string) ([]byte, error)
}

type rawDeleter interface {
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
}

func readGroupRedirect(store any, gid string) (*GroupRedirect, error) {
	g, ok := database.AsCapability[rawGetter](store)
	if !ok || strings.TrimSpace(gid) == "" {
		return nil, nil
	}
	data, err := g.GetRaw(groupRedirectKey(gid))
	if err != nil {
		return nil, fmt.Errorf("read group redirect %s: %w", gid, err)
	}
	if data == nil {
		return nil, nil
	}
	var r GroupRedirect
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode group redirect %s: %w", gid, err)
	}
	return &r, nil
}

// putGroupRedirects writes one redirect per group in leftLosers (group ->
// the participants that left it) into intoGroupID. It stops at the first
// failure and returns the groups written so far, for removal.
func (ms *Service) putGroupRedirects(leftLosers map[string][]string, intoGroupID, survivorID string) ([]string, error) {
	now := time.Now().UTC()
	var written []string
	for _, gid := range slices.Sorted(maps.Keys(leftLosers)) {
		data, err := json.Marshal(GroupRedirect{
			FromGroupID: gid, IntoGroupID: intoGroupID, SurvivorID: survivorID,
			Losers: slices.Clone(leftLosers[gid]), CreatedAt: now,
		})
		if err != nil {
			return written, fmt.Errorf("marshal group redirect %s: %w", gid, err)
		}
		if err := ms.db.SetRaw(groupRedirectKey(gid), data); err != nil {
			return written, fmt.Errorf("write group redirect %s: %w", gid, err)
		}
		written = append(written, gid)
	}
	return written, nil
}

// ClearGroupRedirect removes fromGroupID's redirect if it still points at
// intoGroupID. Best-effort: a failure is logged. A redirect left behind is
// followed only while fromGroupID has no live member, and only for rows
// trashed before it was written.
func ClearGroupRedirect(store any, fromGroupID, intoGroupID string) {
	d, ok := database.AsCapability[rawDeleter](store)
	if !ok {
		return
	}
	r, err := readGroupRedirect(store, fromGroupID)
	if err != nil {
		mlog.Warn("group redirect %s not cleared: %s", logger.SanitizeLogValue(fromGroupID), logger.SanitizeLogValue(fmt.Sprint(err)))
		return
	}
	if r == nil || r.IntoGroupID != intoGroupID {
		return
	}
	if err := d.DeleteRaw(groupRedirectKey(fromGroupID)); err != nil {
		mlog.Warn("group redirect %s -> %s not cleared: %s", logger.SanitizeLogValue(fromGroupID),
			logger.SanitizeLogValue(intoGroupID), logger.SanitizeLogValue(fmt.Sprint(err)))
	}
}

// clearRestoredGroupRedirects removes the redirect of each group a sibling
// was just put back in: that group has a live member again.
func (ms *Service) clearRestoredGroupRedirects(j *SiblingMoveJournal, restored []string) {
	done := map[string]bool{}
	for _, sib := range j.Siblings {
		if slices.Contains(restored, sib.BookID) && !done[sib.FromGroupID] {
			done[sib.FromGroupID] = true
			ClearGroupRedirect(ms.db, sib.FromGroupID, j.IntoGroupID)
		}
	}
}

// trashRestoreTarget is the group a trashed row in gid should be restored
// into instead of gid, or "" to restore it where it is (see "WHEN IT IS
// FOLLOWED" above). It reads without locks; RestoreFromTrash locks the target
// and calls it again under the locks before acting on it.
func trashRestoreTarget(store TrashRestoreStore, row *database.Book) (string, error) {
	if row == nil || row.VersionGroupID == nil {
		return "", nil
	}
	gid := strings.TrimSpace(*row.VersionGroupID)
	if gid == "" {
		return "", nil
	}
	target := ""
	seen := map[string]bool{gid: true}
	cur := gid
	for hop := 0; hop < maxGroupRedirectHops; hop++ {
		live, err := liveMembersExcept(store, cur, row.ID)
		if err != nil {
			return "", err
		}
		if live > 0 {
			break
		}
		r, err := readGroupRedirect(store, cur)
		if err != nil {
			return "", err
		}
		if r == nil || r.IntoGroupID == "" || seen[r.IntoGroupID] {
			break
		}
		if hop == 0 && row.MarkedForDeletionAt != nil && row.MarkedForDeletionAt.After(r.CreatedAt) {
			// Trashed after the merge emptied its group: it was put back
			// in gid and trashed from there.
			return "", nil
		}
		seen[r.IntoGroupID] = true
		cur = r.IntoGroupID
		target = cur
	}
	if target == "" {
		return "", nil
	}
	live, err := liveMembersExcept(store, target, row.ID)
	if err != nil {
		return "", err
	}
	if live == 0 {
		return "", nil
	}
	return target, nil
}

func liveMembersExcept(store TrashRestoreStore, gid, id string) (int, error) {
	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return 0, fmt.Errorf("read version group %s: %w", gid, err)
	}
	n := 0
	for i := range members {
		if members[i].ID != id && !database.IsInTrash(&members[i]) {
			n++
		}
	}
	return n, nil
}
