// file: internal/playlist/evaluator.go
// version: 1.2.0
// guid: 9c2d5f1e-6b4a-4a70-b8c5-3d7e0f1b9a68
// last-edited: 2026-07-10
//
// Smart playlist query evaluator (spec 3.4 task 2).
//
// A smart playlist carries a DSL query string plus an optional
// SortJSON and Limit directive. Evaluating the playlist produces
// the ordered list of book IDs that currently match. The result is
// also cached onto the playlist row (MaterializedBookIDs) so the
// iTunes push worker doesn't need the index online to flush pending
// playlists.
//
// The evaluator prefers the Bleve index when available: it yields
// full text/relevance support and handles ranges natively. Per-user
// fields (read_status, progress_pct, last_played) are split off by
// the translator into a PerUserFilter slice and applied here with
// Store lookups since Bleve only holds library-global state.
//
// When the Bleve index is nil (e.g. during early startup) the
// evaluator returns an error — iTunes sync and the HTTP GET path
// both retry after the index opens.

package playlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// PlaylistSort is one ordering directive inside a smart playlist's
// SortJSON. The parsed form of the playlist's SortJSON field.
type PlaylistSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"` // "asc" | "desc"
}

// defaultEvalPageSize is the Bleve result window when a smart
// playlist has no Limit set. Large enough to cover the library
// while still keeping memory bounded.
const defaultEvalPageSize = 10000

// ErrSearchIndexUnavailable indicates the Bleve index is not open.
// Callers can retry once the server has finished its startup phase.
var ErrSearchIndexUnavailable = errors.New("search index not yet available")

// playlistEvalStore is the narrow slice of database.Store that the
// playlist evaluator actually needs: BookReader for sort enrichment
// (GetBookByID) and UserPositionStore for per-user filter lookups
// (GetUserBookState). Declared as a file-local composite so the
// entry point's dependency surface is inspectable in one place.
type playlistEvalStore interface {
	database.BookReader
	database.UserPositionStore
}

// EvaluateSmartPlaylist parses the playlist query, runs it against
// the Bleve index, applies any per-user post-filters, sorts, caps
// to Limit, and returns the ordered list of matching book IDs.
//
// store is required for per-user filter lookups + sort enrichment.
// idx is the Bleve index; nil yields ErrSearchIndexUnavailable.
// userID is the user the playlist evaluates for — per-user filters
// read that user's state rows.
func EvaluateSmartPlaylist(
	store playlistEvalStore,
	idx *search.BleveIndex,
	query string,
	sortJSON string,
	limit int,
	userID string,
) ([]string, error) {
	if idx == nil {
		return nil, ErrSearchIndexUnavailable
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}

	ast, err := search.ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("parse query: %w", err)
	}
	bleveQ, perUserFilters, err := search.Translate(ast)
	if err != nil {
		return nil, fmt.Errorf("translate query: %w", err)
	}

	// Pull a wide window so we can sort+limit in-memory. If a Limit
	// is set we still fetch defaultEvalPageSize worth to give the
	// post-filter + sort pass freedom to reshuffle.
	hits, _, err := idx.SearchNative(bleveQ, 0, defaultEvalPageSize)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.BookID)
	}

	ids = applyPerUserFilters(store, ids, perUserFilters, userID)

	ids, err = sortBookIDs(store, ids, sortJSON)
	if err != nil {
		return nil, fmt.Errorf("sort: %w", err)
	}

	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// applyPerUserFilters walks the candidate book IDs and drops any
// whose per-user state doesn't satisfy every filter. Filters for
// fields the user never wrote are treated as "no match" (the user
// hasn't engaged with the book, so e.g. `read_status:finished`
// doesn't match an unstarted book). Matching itself delegates to
// search.MatchPerUserFilters — the single exported source of truth
// shared with searchWithBleve (INIT-4 T2).
func applyPerUserFilters(
	store database.UserPositionStore,
	ids []string,
	filters []search.PerUserFilter,
	userID string,
) []string {
	if len(filters) == 0 {
		return ids
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		state, _ := store.GetUserBookState(userID, id)
		if search.MatchPerUserFilters(state, filters) {
			kept = append(kept, id)
		}
	}
	return kept
}

// sortBookIDs reorders ids per the playlist's SortJSON directives.
// When SortJSON is empty or invalid, ids are sorted by ID string for
// deterministic output (ULIDs sort in insertion order). Unknown fields
// are skipped so a partly-broken sort spec still produces a stable result.
func sortBookIDs(store database.BookReader, ids []string, sortJSON string) ([]string, error) {
	if len(ids) < 2 {
		return ids, nil
	}
	if strings.TrimSpace(sortJSON) == "" {
		out := make([]string, len(ids))
		copy(out, ids)
		sort.Strings(out)
		return out, nil
	}
	var directives []PlaylistSort
	if err := json.Unmarshal([]byte(sortJSON), &directives); err != nil {
		// Fallback to deterministic ID sort on parse error.
		out := make([]string, len(ids))
		copy(out, ids)
		sort.Strings(out)
		return out, fmt.Errorf("parse sort_json: %w", err)
	}
	if len(directives) == 0 {
		out := make([]string, len(ids))
		copy(out, ids)
		sort.Strings(out)
		return out, nil
	}

	type loaded struct {
		id   string
		book *database.Book
	}
	rows := make([]loaded, 0, len(ids))
	for _, id := range ids {
		b, _ := store.GetBookByID(id)
		rows = append(rows, loaded{id: id, book: b})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		for _, d := range directives {
			c := compareBookField(rows[i].book, rows[j].book, d.Field)
			if c == 0 {
				continue
			}
			if strings.EqualFold(d.Direction, "desc") {
				return c > 0
			}
			return c < 0
		}
		// Tiebreaker: sort by ID for deterministic output.
		return rows[i].id < rows[j].id
	})

	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.id
	}
	return out, nil
}

// compareBookField returns -1/0/+1 for a/b on the named Book field.
// Missing books are treated as "less than" present books so they
// cluster at one end rather than breaking the sort mid-list.
func compareBookField(a, b *database.Book, field string) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	switch strings.ToLower(field) {
	case "title":
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "year":
		return intCmp(derefInt(a.PrintYear), derefInt(b.PrintYear))
	case "audiobook_year":
		return intCmp(derefInt(a.AudiobookReleaseYear), derefInt(b.AudiobookReleaseYear))
	case "rating":
		return intCmp(derefInt(a.ITunesRating), derefInt(b.ITunesRating))
	case "duration":
		return intCmp(derefInt(a.Duration), derefInt(b.Duration))
	case "date_added", "added":
		return timeCmp(derefTime(a.CreatedAt), derefTime(b.CreatedAt))
	case "date_modified", "modified":
		return timeCmp(derefTime(a.UpdatedAt), derefTime(b.UpdatedAt))
	case "itunes_last_played":
		return timeCmp(derefTime(a.ITunesLastPlayed), derefTime(b.ITunesLastPlayed))
	case "itunes_play_count":
		return intCmp(derefInt(a.ITunesPlayCount), derefInt(b.ITunesPlayCount))
	default:
		return 0
	}
}

func derefTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func intCmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func timeCmp(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	default:
		return 0
	}
}
