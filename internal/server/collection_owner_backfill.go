// file: internal/server/collection_owner_backfill.go
// version: 1.3.0
// guid: 2f6b8d41-a3c7-4e95-b012-7d9e5c1a4f68
// last-edited: 2026-10-18

package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/search"
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
	// SkippedPerUser is how many ownerless collections were left ownerless on
	// purpose because their query uses a per-user field (see the function doc).
	SkippedPerUser int
}

var collectionOwnerLog = logger.New("server.collection-owner-backfill")

// collectionOwnerLogger is the part of a logger the backfill writes to. The
// server passes one backed by the activity log so the per-user listing reaches an
// admin-visible surface; tests pass a recording one; nil means the package logger.
type collectionOwnerLogger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// maxListedUnowned caps how many collections the one warning names; the count in
// the line is always the full count.
const maxListedUnowned = 50

// backfillCollectionOwners gives every dynamic collection with no usable owner
// (CreatedByUserID empty, "_local" when auth is on, or naming a user that no
// longer exists) the first active admin as its owner, so it has one evaluating
// user (D75) instead
// of leaving the read path to fall back on every request. When remat is non-nil
// each reassigned collection is also re-evaluated as its new owner and its
// MaterializedBookIDs refreshed; if the evaluation fails the stored set is left
// for the next materialize.
//
// A collection whose query uses a per-user field is NOT given the admin and is
// left ownerless. Handing it to the first admin would make the stored query
// evaluate as the admin and publish the admin's listening state (the members are
// served to every reader) for a query the admin never wrote, and a per-user query
// on an ownerless collection may already have been written by someone other than
// the one whose state it reads: there is no authorship history to tell. Such
// collections are listed in one warning (ids and names) on every run until an
// owner takes them (the first active admin re-saves the query, see
// CollectionHandler.UpdateCollection) or the per-user fields are removed; until
// then the collection handler refuses to evaluate them (409
// COLLECTION_OWNER_MISSING_PER_USER_QUERY) and they keep their stored members.
//
// Idempotent: after a run every dynamic collection either names an existing user
// or is a listed per-user orphan, so a second run writes nothing (it repeats the
// warning for the listed ones, on purpose). Static collections are never
// touched: a static collection has no evaluating user. Writes go through
// UpdateCollection, so Version and UpdatedAt move like any other edit.
//
// CONCURRENCY: this loops over the dynamic collections, a handful of rows, not
// the library, and each iteration is one row write plus at most one index query.
// It is deliberately sequential; a worker pool would add ordering questions
// (two workers writing the same name index) for no measurable gain.
func backfillCollectionOwners(store collectionOwnerStore, remat collectionRematerializer, log collectionOwnerLogger, authEnabled bool) (collectionOwnerBackfillResult, error) {
	var res collectionOwnerBackfillResult
	if log == nil {
		log = collectionOwnerLog
	}

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

	var orphans, perUserOrphans []database.Collection
	for i := range cols {
		c := cols[i]
		// ListCollections(dynamic) already filtered by type; the check keeps the
		// "never touches static" rule local to this function.
		if c.Type != database.CollectionTypeDynamic {
			continue
		}
		// With auth off "_local" is the single user, a legitimate owner the
		// handler evaluates as, though no user row answers to it; only with auth
		// on is it a stand-in id.
		localOwner := c.CreatedByUserID == localCallerID && !authEnabled
		unowned := c.CreatedByUserID == "" || (c.CreatedByUserID == localCallerID && authEnabled)
		if !unowned && !localOwner {
			_, ok := exists[c.CreatedByUserID]
			unowned = !ok
		}
		if !unowned {
			continue
		}
		if search.QueryUsesPerUserField(c.Query) {
			perUserOrphans = append(perUserOrphans, c)
			continue
		}
		orphans = append(orphans, c)
	}
	res.SkippedPerUser = len(perUserOrphans)
	if len(perUserOrphans) > 0 {
		log.Warn("%s", unownedPerUserWarning(perUserOrphans))
	}
	if len(orphans) == 0 {
		return res, nil
	}

	admin := auth.FirstActiveAdmin(users)
	if admin == nil {
		// These orphans have no per-user field (those were split off above), so
		// they need no evaluator: a plain query is evaluated with no user at all,
		// with or without an admin, and keeps working. Assigning an owner is only
		// bookkeeping, done once there is an admin to assign. A per-user query on
		// an orphan is the case that needs one, and it is blocked (never evaluated
		// as a stand-in) until its first active admin adopts it with adopt_owner.
		res.NoAdmin = true
		log.Warn("collection owner backfill: %d dynamic collection(s) have no usable owner and there is no active admin to assign; leaving them unowned (their queries use no per-user field, so they are still evaluated with no user and keep working)", len(orphans))
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
	log.Info("collection owner backfill: assigned %d dynamic collection(s) to first admin %s (%d re-materialized, %d skipped on a write conflict)",
		res.Reassigned, logger.SanitizeLogValue(admin.ID), res.Rematerialized, res.Conflicts)
	return res, nil
}

// unownedPerUserWarning is the one line that names every dynamic collection
// that has a per-user query and no usable owner, so an operator can decide what to
// do with each. Names are user-controlled text: they are sanitized and the list
// is capped.
func unownedPerUserWarning(cols []database.Collection) string {
	shown := cols
	if len(shown) > maxListedUnowned {
		shown = shown[:maxListedUnowned]
	}
	parts := make([]string, 0, len(shown))
	for i := range shown {
		parts = append(parts, fmt.Sprintf("%s (%s)",
			logger.SanitizeLogValue(shown[i].ID), logger.SanitizeLogValue(shown[i].Name)))
	}
	more := ""
	if len(cols) > len(shown) {
		more = fmt.Sprintf("; and %d more", len(cols)-len(shown))
	}
	return fmt.Sprintf("collection owner backfill: %d dynamic collection(s) use a per-user query field (%s) and have no usable owner, so they were left unowned and will not be evaluated as the first admin; "+
		"their stored members are unchanged. The first active admin can take ownership of each by saving its query again with adopt_owner: true, or remove the per-user field: %s%s",
		len(cols), strings.Join(search.PerUserFieldNames(), ", "), strings.Join(parts, "; "), more)
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
