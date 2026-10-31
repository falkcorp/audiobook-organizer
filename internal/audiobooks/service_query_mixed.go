// file: internal/audiobooks/service_query_mixed.go
// version: 1.0.0
// guid: 3d8e1f7a-4b92-4c65-a0e3-9f5d7b2c6a18
// last-edited: 2026-10-30

package audiobooks

import (
	"context"
	"fmt"
	"runtime"

	"github.com/blevesearch/bleve/v2"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// viewerRowsForMixedQuery returns the state rows search.MixedQuery judges a search's
// per-user terms against: every row userID has, keyed by book id.
//
// With a store that can list a viewer's rows (database.UserBookStateLister, the
// Pebble store) it is one scan. A scan that fails fails the search: unlike the
// flat post-filter's per-candidate reads, the rows decide the whole expression
// (under an OR, or a NOT, a row read as "none" changes which books MATCH, not only
// which are dropped), so there is no cheaper answer to fall back to for a library
// search than the one it could not read.
//
// A store that cannot list is read book by book over the whole index, spread over
// a bounded pool (readAllRowsByPointReads); no production store takes that path.
//
// With no user, or with config DisablePerUserSearchFilters set, there are no rows:
// the terms are judged as the zero-value state (see search.MixedQuery.Query), so
// read_status:finished matches no book and read_status:unstarted every one. That
// is the reading a book the viewer never opened gets. The flat path drops the
// per-user filters in this case, which is harmless for an AND (the term just stops
// narrowing) but has no meaning for a term inside an OR or under a NOT, where
// there is no neutral value to drop it to.
func (svc *AudiobookService) viewerRowsForMixedQuery(ctx context.Context, userID string) (map[string]*database.UserBookState, error) {
	if userID == "" || config.AppConfig.DisablePerUserSearchFilters {
		reason := "no_user_context"
		if config.AppConfig.DisablePerUserSearchFilters {
			reason = "disabled_by_config"
		}
		perUserSearchLog.Warn("search: per-user terms under an OR or NOT judged against an empty state, no user context: reason=%s", reason)
		return nil, nil
	}
	if lister, ok := database.AsCapability[database.UserBookStateLister](svc.store); ok {
		rows, err := lister.ListUserBookStates(ctx, userID)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			perUserSearchLog.Error("per-user state read failed for a search with a per-user term under OR/NOT: user_id=%s err=%s",
				logger.SanitizeLogValue(userID), logger.SanitizeLogValue(err.Error()))
			return nil, fmt.Errorf("read the viewer's book states: %w", err)
		}
		return rows, nil
	}
	return svc.readAllRowsByPointReads(ctx, userID)
}

// readAllRowsByPointReads reads one state row per indexed book, over at most
// runtime.NumCPU() workers each writing only its own slots, and returns the books
// that have a row. A book whose row cannot be read is evaluated as having none, with
// a warning: the flat post-filter's rule for an unreadable row (see
// idsMatchingPerUser).
func (svc *AudiobookService) readAllRowsByPointReads(ctx context.Context, userID string) (map[string]*database.UserBookState, error) {
	ids, err := svc.allIndexedIDs(ctx)
	if err != nil {
		return nil, err
	}
	found := make([]*database.UserBookState, len(ids))
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	const chunk = 256
	for lo := 0; lo < len(ids); lo += chunk {
		hi := min(lo+chunk, len(ids))
		g.Go(func() error {
			for i := lo; i < hi; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				state, stateErr := svc.store.GetUserBookState(userID, ids[i])
				if stateErr != nil {
					perUserSearchLog.Warn("search: per-user state read failed; evaluating zero-value state: book_id=%s err=%s",
						logger.SanitizeLogValue(ids[i]), logger.SanitizeLogValue(stateErr.Error()))
					continue
				}
				found[i] = state
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	rows := make(map[string]*database.UserBookState)
	for i, id := range ids {
		if found[i] != nil {
			rows[id] = found[i]
		}
	}
	return rows, nil
}

// allIndexedIDs returns the id of every document in the search index. The first
// pass asks for the document count, which no query can exceed; if documents were
// indexed since, the index reports the larger total and it asks again, a bounded
// number of times, so a short list is never returned as if it were complete.
func (svc *AudiobookService) allIndexedIDs(ctx context.Context) ([]string, error) {
	n, err := svc.searchIndex.DocCount()
	if err != nil {
		return nil, fmt.Errorf("bleve doc count: %w", err)
	}
	window := max(int(n), 1)
	for range 4 {
		hits, total, err := svc.searchIndex.SearchNativeIDsContext(ctx, bleve.NewMatchAllQuery(), 0, window)
		if err != nil {
			return nil, fmt.Errorf("bleve search: %w", err)
		}
		if uint64(len(hits)) >= total {
			ids := make([]string, len(hits))
			for i, h := range hits {
				ids[i] = h.BookID
			}
			return ids, nil
		}
		window = int(total)
	}
	return nil, fmt.Errorf("bleve search: the index kept growing while its ids were listed")
}
