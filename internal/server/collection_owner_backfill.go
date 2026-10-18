// file: internal/server/collection_owner_backfill.go
// version: 1.0.0
// guid: 2f6b8d41-a3c7-4e95-b012-7d9e5c1a4f68
// last-edited: 2026-10-18

package server

import (
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
)

// localCallerID is the no-auth fallback caller id (CallingUserID). It is not a
// user row, so a collection created with auth off carries it as its owner and
// no user answers to it.
const localCallerID = "_local"

// collectionOwnerStore is the slice of the store the owner backfill reads and
// writes: the dynamic collections, the user list, and the collection update.
type collectionOwnerStore interface {
	ListCollections(collectionType string, limit, offset int) ([]database.Collection, int, error)
	UpdateCollection(col *database.Collection) error
	ListUsers() ([]database.User, error)
}

// collectionRematerializer evaluates a dynamic collection as ownerID. A non-nil
// error (typically playlist.ErrSearchIndexUnavailable while the index is closed
// or rebuilding) leaves the stored MaterializedBookIDs as they were.
type collectionRematerializer func(col *database.Collection, ownerID string) ([]string, error)

// collectionOwnerBackfillResult is what one run did.
type collectionOwnerBackfillResult struct {
	// Reassigned is how many dynamic collections got a new owner.
	Reassigned int
	// Rematerialized is how many of those also had their member list refreshed.
	Rematerialized int
	// Conflicts is how many were skipped because another writer changed the row
	// between the list and the write; the next boot picks them up.
	Conflicts int
	// NoAdmin is true when collections needed an owner but there is no active
	// admin to give them.
	NoAdmin bool
}

var collectionOwnerLog = logger.New("server.collection-owner-backfill")

// backfillCollectionOwners gives every dynamic collection with no usable owner
// (CreatedByUserID empty, "_local", or naming a user that no longer exists) the
// first active admin as its owner, so it has one evaluating user (D75) instead
// of leaving the read path to fall back on every request. When remat is non-nil
// each reassigned collection is also re-evaluated as its new owner and its
// MaterializedBookIDs refreshed; if the evaluation fails the stored set is left
// for the next materialize.
//
// Idempotent: after a run every dynamic collection names an existing user, so a
// second run finds nothing and writes nothing. Static collections are never
// touched: a static collection has no evaluating user. Writes go through
// UpdateCollection, so Version and UpdatedAt move like any other edit.
//
// CONCURRENCY: this loops over the dynamic collections, a handful of rows, not
// the library, and each iteration is one row write plus at most one index query.
// It is deliberately sequential; a worker pool would add ordering questions
// (two workers writing the same name index) for no measurable gain.
func backfillCollectionOwners(store collectionOwnerStore, remat collectionRematerializer) (collectionOwnerBackfillResult, error) {
	var res collectionOwnerBackfillResult

	cols, _, err := store.ListCollections(database.CollectionTypeDynamic, 0, 0)
	if err != nil {
		return res, err
	}
	if len(cols) == 0 {
		return res, nil
	}
	users, err := store.ListUsers()
	if err != nil {
		return res, err
	}
	exists := make(map[string]struct{}, len(users))
	for i := range users {
		exists[users[i].ID] = struct{}{}
	}

	var orphans []database.Collection
	for i := range cols {
		c := cols[i]
		// ListCollections(dynamic) already filtered by type; the check keeps the
		// "never touches static" rule local to this function.
		if c.Type != database.CollectionTypeDynamic {
			continue
		}
		if c.CreatedByUserID == "" || c.CreatedByUserID == localCallerID {
			orphans = append(orphans, c)
			continue
		}
		if _, ok := exists[c.CreatedByUserID]; !ok {
			orphans = append(orphans, c)
		}
	}
	if len(orphans) == 0 {
		return res, nil
	}

	admin := auth.FirstActiveAdmin(users)
	if admin == nil {
		res.NoAdmin = true
		collectionOwnerLog.Warn("collection owner backfill: %d dynamic collection(s) have no usable owner and there is no active admin to assign; leaving them (reads fall back to the first admin once one exists)", len(orphans))
		return res, nil
	}

	for i := range orphans {
		col := orphans[i]
		col.CreatedByUserID = admin.ID
		refreshed := false
		if remat != nil {
			if ids, rerr := remat(&col, admin.ID); rerr == nil {
				col.MaterializedBookIDs = ids
				refreshed = true
			}
		}
		if uerr := store.UpdateCollection(&col); uerr != nil {
			if errors.Is(uerr, database.ErrCollectionVersionConflict) {
				res.Conflicts++
				continue
			}
			return res, uerr
		}
		res.Reassigned++
		if refreshed {
			res.Rematerialized++
		}
	}
	collectionOwnerLog.Info("collection owner backfill: assigned %d dynamic collection(s) to first admin %s (%d re-materialized, %d skipped on a write conflict)",
		res.Reassigned, logger.SanitizeLogValue(admin.ID), res.Rematerialized, res.Conflicts)
	return res, nil
}

// collectionRematerializer builds the rematerializer the server passes: the
// same evaluator the collection handler and smart playlists use, against the
// live search index. It returns playlist.ErrSearchIndexUnavailable until the
// index is open and complete, which backfillCollectionOwners treats as "leave
// the stored members".
func (s *Server) collectionRematerializer() collectionRematerializer {
	return func(col *database.Collection, ownerID string) ([]string, error) {
		return playlist.EvaluateSmartPlaylist(
			s.storeForWiring(), s.SearchIndex(),
			col.Query, col.SortJSON, col.Limit, ownerID,
		)
	}
}
