// file: internal/server/collection_peruser_purge.go
// version: 1.0.0
// guid: 4b7e1c93-2d58-4f06-9a31-c6e8d0f5b274
// last-edited: 2026-10-25

package server

import (
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// collectionPurgeStore is the slice of the store the purge reads and writes: the
// dynamic collections and the collection update.
type collectionPurgeStore interface {
	ListCollections(collectionType string, limit, offset int) ([]database.Collection, int, error)
	UpdateCollection(col *database.Collection) error
}

// collectionPurgeResult is what one run did.
type collectionPurgeResult struct {
	// Cleared is how many per-viewer collections had a stored member list removed.
	Cleared int
	// Conflicts is how many were skipped because another writer changed the row
	// between the list and the write; the next boot picks them up.
	Conflicts int
}

var collectionPurgeLog = logger.New("server.collection-peruser-purge")

// collectionPurgeLogger is the part of a logger the purge writes to. The server
// passes one backed by the activity log; tests pass a recording one; nil means
// the package logger.
type collectionPurgeLogger interface {
	Info(msg string, args ...any)
}

// purgePerViewerCollectionMembers removes the stored member list (MaterializedBookIDs)
// from every dynamic collection whose query uses a per-user field.
//
// WHY. Before the owner decision of 2026-10-25 such a collection was evaluated as
// its CREATOR and the answer was stored on the row and served to every reader, so
// the stored list IS the creator's listening state (which books they finished, or
// played this month) sitting in a row anyone can read. Collections are now
// evaluated for the viewer and store no list for a per-user query (see the
// collection handler's file header); this clears what the old behaviour left. The
// read paths already ignore a leftover list (the native handler and the ABS surface
// both replace it with the viewer's own), so this removes the data at rest rather
// than being what keeps it from being served.
//
// Idempotent: a second run finds nothing to clear and writes nothing. Writes go
// through UpdateCollection (Version compare-and-swap), so a collection edited
// between the list and the write is skipped and picked up next boot. Static
// collections and dynamic ones without a per-user field are never touched.
//
// CONCURRENCY: this loops over the dynamic collections, a handful of rows, not the
// library, and each iteration is at most one row write. It is deliberately
// sequential; a worker pool would add ordering questions for no measurable gain.
func purgePerViewerCollectionMembers(store collectionPurgeStore, log collectionPurgeLogger) (collectionPurgeResult, error) {
	var res collectionPurgeResult
	if log == nil {
		log = collectionPurgeLog
	}
	cols, _, err := store.ListCollections(database.CollectionTypeDynamic, 0, 0)
	if err != nil {
		return res, err
	}
	for i := range cols {
		col := cols[i]
		if col.Type != database.CollectionTypeDynamic ||
			len(col.MaterializedBookIDs) == 0 ||
			!search.QueryUsesPerUserField(col.Query) {
			continue
		}
		col.MaterializedBookIDs = nil
		if uerr := store.UpdateCollection(&col); uerr != nil {
			if errors.Is(uerr, database.ErrCollectionVersionConflict) {
				res.Conflicts++
				continue
			}
			return res, uerr
		}
		res.Cleared++
	}
	if res.Cleared > 0 || res.Conflicts > 0 {
		log.Info("collection per-user purge: cleared the stored member list of %d dynamic collection(s) whose query uses a per-user field (members are now evaluated for each viewer); %d skipped on a write conflict, retried next boot",
			res.Cleared, res.Conflicts)
	}
	return res, nil
}
