// file: internal/server/handlers/abs/browse_client_sort.go
// version: 1.0.0
// guid: 0b122ca1-3d17-4eb2-b2dd-5707ab882ef2
// last-edited: 2026-09-19
//
// ── client sorts the store cannot push down ─────────────────────────────────
//
// AudioBooth's book Sort By menu offers keys that are not Book fields at all:
// the requesting user's progress (three keys), a shuffle, and the author in
// "Last, First" form. The store sorts by Book fields only, so these used to
// fall through absSortField to "" and the page came back in title order behind
// a 200. They are ordered here instead.
//
// 🔴 THE WHOLE VISIBLE SET IS ORDERED BEFORE PAGINATION. Ordering only the
// fetched page looks right on page 0 of a short list and is wrong everywhere
// else: page 1 re-serves books from page 0 and skips others. The cost is one
// summary pass over the visible library -- the same pass buildSeriesBooks and
// contributorDTOs already make -- and only for these rarely-chosen sorts.
// Unlike those two it is NOT cached, so each such request pays a full
// visible-summary pass (~16.5k rows on prod) plus, for authorNameLF, one
// batched author lookup over the same ids. Caching the ordered id list per
// (key, user) is the next step if these sorts show up in latency.

package abs

import (
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Canonical handler-side book sort keys, as absHandlerSort returns them.
const (
	absSortAuthorLF         = "authorNameLF"
	absSortProgress         = "progress"
	absSortProgressFinished = "progress.finishedAt"
	absSortProgressCreated  = "progress.createdAt"
	absSortRandom           = "random"
)

// absHandlerSort recognises the client sort keys this file orders. It returns
// "" for every other key, which then goes through absSortField to the store.
//
// The progress keys are matched on the WHOLE key, not the last dotted segment
// the way absSortField matches: "progress.createdAt" reduced to "createdAt"
// would be indistinguishable from a book creation-date sort.
func absHandlerSort(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	switch key {
	case "progress":
		return absSortProgress
	case "progress.finishedat":
		return absSortProgressFinished
	case "progress.createdat":
		return absSortProgressCreated
	case "random":
		return absSortRandom
	}
	if i := strings.LastIndex(key, "."); i >= 0 {
		key = key[i+1:]
	}
	if key == "authornamelf" {
		return absSortAuthorLF
	}
	return ""
}

// clientSortKey is one book's value for a handler-side sort. has=false means
// the book has no value (no progress row, never finished, no author); those
// books trail the ordered ones in BOTH directions, in their incoming order
// (title order on /items, the group order on a drill-down), which is
// what a user sorting by "recently listened" expects to see first.
type clientSortKey struct {
	has bool
	num int64
	str string
}

// clientSortedItems serves GET /items for a handler-side sort key.
func (h *Handler) clientSortedItems(c *gin.Context, key string, p pageParams, resp *itemsPageResponse) {
	// Title order is the base: stable sorting below keeps it as the tie-break
	// and as the order of the books that have no value for the key.
	base := absItemFilterBase()
	base.SortBy = "title"
	summaries, err := h.visibleBookSummaries(base)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list library items")
		return
	}
	ids := make([]string, len(summaries))
	for i := range summaries {
		ids[i] = summaries[i].ID
	}

	ids, err = h.orderIDsByClientSort(c, key, ids, c.Query("desc") == "1")
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not sort library items")
		return
	}

	resp.Total = len(ids)
	start := min(p.Offset, len(ids))
	end := min(start+p.Limit, len(ids))
	h.renderItemPage(c, ids[start:end], resp)
}

// orderIDsByClientSort returns a NEW slice holding ids ordered for a
// handler-side sort key; ids itself is never modified. That matters on the
// ?filter= path, whose ids are the cached series/contributor groupings shared
// by every request -- sorting them in place would reorder every later one.
// Books with no value for the key keep their incoming order (title order on
// /items, the group's own order on a drill-down).
func (h *Handler) orderIDsByClientSort(c *gin.Context, key string, ids []string, desc bool) ([]string, error) {
	out := append([]string(nil), ids...)
	switch key {
	case absSortRandom:
		// A fresh shuffle per request. Paging through a random order therefore
		// reshuffles between pages; ABS itself orders by RANDOM() per query, so
		// this matches the server the client was written against.
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out, nil
	case absSortAuthorLF:
		keys, err := h.authorLFKeys(c, out)
		if err != nil {
			return nil, err
		}
		return orderByClientKey(out, keys, desc), nil
	default:
		return orderByClientKey(out, h.progressKeys(c, key), desc), nil
	}
}

// renderItemPage loads, renders and appends the given book ids to resp in the
// order given. GetBooksByIDs makes no ordering promise, so the order is
// restored from ids rather than taken from the store.
func (h *Handler) renderItemPage(c *gin.Context, ids []string, resp *itemsPageResponse) {
	books, err := h.library.GetBooksByIDs(ids)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	sort.SliceStable(books, func(a, b int) bool { return pos[books[a].ID] < pos[books[b].ID] })
	views, err := h.loadItemViews(c.Request.Context(), books)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build library items")
		return
	}
	for i := range views {
		resp.Results = append(resp.Results, h.minifiedItem(&views[i]))
	}
	respondJSON(c, http.StatusOK, *resp)
}

// orderByClientKey stably orders ids by keys: books with a value first (by
// value, reversed when desc), then books without one in their incoming order.
func orderByClientKey(ids []string, keys map[string]clientSortKey, desc bool) []string {
	sort.SliceStable(ids, func(a, b int) bool {
		ka, kb := keys[ids[a]], keys[ids[b]]
		if ka.has != kb.has {
			return ka.has
		}
		if !ka.has {
			return false
		}
		// Each key type fills exactly one of str/num (authorNameLF: str; the
		// progress keys: num), so comparing str first and num second never
		// mixes the two.
		var cmp int
		switch {
		case ka.str != kb.str:
			cmp = strings.Compare(ka.str, kb.str)
		case ka.num < kb.num:
			cmp = -1
		case ka.num > kb.num:
			cmp = 1
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
	return ids
}

// authorLFKeys builds each book's authorNameLF sort value from the SAME
// junction rows and the SAME lastFirstJoin the item's media.metadata.authorNameLF
// is rendered from (mapper.go metadata), so the order matches the value the
// client displays. The store's "author" sort is first-name-first and cannot
// serve this: "Iain M. Banks" sorts under I there and under B here.
func (h *Handler) authorLFKeys(c *gin.Context, ids []string) (map[string]clientSortKey, error) {
	byBook, err := h.library.GetAuthorsByBookIDs(c.Request.Context(), ids)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]clientSortKey, len(byBook))
	for bookID, authors := range byBook {
		names := make([]string, 0, len(authors))
		for _, a := range authors {
			if n := strings.TrimSpace(a.Name); n != "" {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			continue
		}
		keys[bookID] = clientSortKey{has: true, str: strings.ToLower(lastFirstJoin(names))}
	}
	return keys, nil
}

// progressSortRow is the subset of a rendered mediaProgress row the progress
// sorts read. Decoded from the rendered row rather than type-asserted, so the
// sort reads exactly the values GET /api/me serves for the same book.
type progressSortRow struct {
	LibraryItemID string `json:"libraryItemId"`
	LastUpdate    int64  `json:"lastUpdate"`
	StartedAt     int64  `json:"startedAt"`
	FinishedAt    *int64 `json:"finishedAt"`
}

// progressKeys maps book id -> the requesting user's progress value for key.
//
// ⚠️ One ResolveSyncItem store read per progress row, sequentially. The rows
// are the user's started books (tens to low hundreds), not the library, and
// IdentityStore has no batch resolve; a batch method there is the fix if a
// heavy listener's progress sort shows up in latency.
//
// Field choice mirrors real ABS's book filters: `progress` orders by the
// progress row's last update, `progress.createdAt` by when it was started, and
// `progress.finishedAt` by when it was finished (books never finished have no
// value). The user's progress list is one call -- UserDataProvider guarantees
// it is complete -- so this never reads a position per library book.
//
// Any failure degrades to "no values" (title order) with a warning: a sort is
// presentation, and a 500 here would blank the library tab.
func (h *Handler) progressKeys(c *gin.Context, key string) map[string]clientSortKey {
	keys := map[string]clientSortKey{}
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil || h.userData == nil {
		return keys
	}
	rows, err := h.userData.MediaProgress(user.ID)
	if err != nil {
		slog.Warn("abs: progress sort: user progress unavailable, serving title order",
			"sort", key, "err", err)
		return keys
	}
	for _, raw := range rows {
		buf, merr := json.Marshal(raw)
		if merr != nil {
			continue
		}
		var row progressSortRow
		if uerr := json.Unmarshal(buf, &row); uerr != nil || row.LibraryItemID == "" {
			continue
		}
		var v clientSortKey
		switch key {
		case absSortProgress:
			v = clientSortKey{has: true, num: row.LastUpdate}
		case absSortProgressCreated:
			v = clientSortKey{has: true, num: row.StartedAt}
		case absSortProgressFinished:
			if row.FinishedAt == nil {
				continue
			}
			v = clientSortKey{has: true, num: *row.FinishedAt}
		}
		item, rerr := h.identity.ResolveSyncItem(row.LibraryItemID)
		if rerr != nil || item == nil || item.CurrentBookID == "" {
			continue
		}
		keys[item.CurrentBookID] = v
	}
	return keys
}

// ── series ──────────────────────────────────────────────────────────────────

// sortSeries orders the full series list for the client's SortBy key before
// the handler paginates it. Name order (nameIgnorePrefix, then id) is both the
// default and the tie-break for every other key, so each order is total and
// pages partition the set exactly.
//
// Keys are AudioBooth's SeriesService.SortBy raw values. Series rows carry no
// timestamps of their own, so the date keys are derived from the series' served
// books (seriesBooksBuilt): addedAt = the first book's CreatedAt (when the
// series first appeared in the library), lastBookAdded = the newest book's
// CreatedAt, lastBookUpdated = the newest book UpdatedAt. numBooks is the served
// book count -- the same number the row's numBooks reports.
func sortSeries(series []database.Series, sortBy string, desc bool, bySeries map[int]seriesBooksBuilt) {
	byName := func(i, j int) bool {
		li, lj := ignorePrefix(series[i].Name), ignorePrefix(series[j].Name)
		if li != lj {
			return li < lj
		}
		return series[i].ID < series[j].ID
	}
	var value func(s database.Series) int64
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "numbooks":
		value = func(s database.Series) int64 { return int64(len(bySeries[s.ID].bookIDs)) }
	case "totalduration":
		value = func(s database.Series) int64 { return int64(bySeries[s.ID].totalDuration) }
	case "addedat":
		value = func(s database.Series) int64 { return bySeries[s.ID].firstAddedMs }
	case "lastbookadded":
		value = func(s database.Series) int64 { return bySeries[s.ID].lastAddedMs }
	case "lastbookupdated":
		value = func(s database.Series) int64 { return bySeries[s.ID].lastUpdatedMs }
	case "random":
		// Per request, like the book shuffle (see orderIDsByClientSort), so
		// this is the ONE key whose pages do not partition the set: each page
		// request reshuffles. That is what ABS does too (RANDOM() per query).
		rand.Shuffle(len(series), func(i, j int) { series[i], series[j] = series[j], series[i] })
		return
	}
	sort.SliceStable(series, byName)
	if value != nil {
		// Stable over the name order: equal values keep name order in BOTH
		// directions, so desc does not also reverse the tie-break.
		sort.SliceStable(series, func(i, j int) bool {
			vi, vj := value(series[i]), value(series[j])
			if desc {
				return vi > vj
			}
			return vi < vj
		})
		return
	}
	if desc {
		for i, j := 0, len(series)-1; i < j; i, j = i+1, j-1 {
			series[i], series[j] = series[j], series[i]
		}
	}
}
