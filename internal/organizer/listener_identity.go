// file: internal/organizer/listener_identity.go
// version: 1.0.0
// guid: 78f69da0-7954-44c1-84b5-9d2415376bf5
// last-edited: 2026-10-30

package organizer

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// Compile-time proof that the concrete store satisfies the capability
// followListenerIdentity resolves at run time. The server hands every service
// the one store object it was given (server.NewServer), so this is the
// production store; the assertion turns drift in merge.UserProgressMerger into
// a build failure instead of a run-time Warn that moves nothing.
var _ merge.UserProgressMerger = (*database.PebbleStore)(nil)

// followListenerIdentity carries the source book's ABS sync identity (the
// durable libraryItemId clients hold), its per-sync-file `ino`s and every
// user's listening state (book state, positions, bookmarks) onto the organized
// copy that is about to replace it, so a book a user is partway through stays
// on Continue Listening and keeps its place instead of starting at zero on a
// new id.
//
// WHY unconditionally, and not behind the scanner's "is the predecessor's file
// gone" check (scanner.followSyncIdentityOnVersionLink): there the original
// may be a genuine second copy that stays visible. Here the original is always
// demoted to organized_source and non-primary one step later, so it is never
// the row a user is shown. When an incumbent keeps the group's primary the
// source was already hidden and its identity is dormant; moving it onto the
// copy changes nothing visible, and it is what keeps the identity with the
// copy if the hand-off later crowns it.
//
// The store is resolved here, with database.AsCapability, rather than widening
// Store (merge.UserProgressMerger is ~20 methods) or adding a hook every
// construction of Service would have to remember to wire: organizer.NewService
// is built in several places (audiobooks, metafetch) and a missed wiring would
// silently drop the identity. A store that cannot be resolved (a test double)
// logs and moves nothing.
//
// Failure policy: logged, never fatal, like the scanner's. By this point the
// files are copied and the rows written; failing the organize would orphan
// both for a repair the repair ticker completes anyway. merge records a
// durable pending-repair record before moving progress, so a failed move is
// finished later; a failed identity repoint leaves identity and progress
// together on the source, which is consistent. The check below only makes the
// one case a person must act on (identity still on the source) visible in the
// organize operation's own log, since merge reports through slog.
//
// LOCKS. Called with the version-group key (VersionGroupLocker) and the
// membership locks (versionprimary.LockPlannedGroups) held. merge takes
// syncRepointMu, then a user-state stripe pair, then the copy's book write
// stripe (carryPlayCountMark). Nothing that holds syncRepointMu or a
// user-state stripe takes a version-group lock (merge's own paths take the
// merge lock, then the group locks, then the stripes), so no cycle exists.
func (orgSvc *Service) followListenerIdentity(sourceID, copyID string, sourceFiles []database.BookFile, copyFiles []*database.BookFile, log logger.Logger) {
	um, ok := database.AsCapability[merge.UserProgressMerger](orgSvc.db)
	if !ok {
		log.Warn("organize: store cannot move listening state; sync identity and progress of %s stay on it, not on the organized copy %s", sourceID, copyID)
		return
	}

	// Each copied row got a NEW id, built in the source rows' order, so the
	// pairing is by index. A row without an id on either side has no ino to
	// carry.
	var fileIDs map[string]string
	if len(copyFiles) > 0 {
		fileIDs = make(map[string]string, len(copyFiles))
		for i := range copyFiles {
			if i >= len(sourceFiles) {
				break
			}
			if sourceFiles[i].ID != "" && copyFiles[i].ID != "" {
				fileIDs[sourceFiles[i].ID] = copyFiles[i].ID
			}
		}
	}

	merge.FollowVersionHandover(um, sourceID, copyID, fileIDs)

	if ids := database.AsSyncIdentityStore(orgSvc.db); ids != nil {
		if syncID, still, err := ids.GetSyncIDForBook(sourceID); err != nil {
			log.Warn("organize: cannot confirm sync identity of %s moved to the organized copy %s: %v", sourceID, copyID, err)
		} else if still {
			log.Error("organize: sync identity %s stayed on %s and did NOT move to the organized copy %s; clients keep resolving to the demoted source (see the sync-identity follow error in the server log)", syncID, sourceID, copyID)
		}
	}
}
