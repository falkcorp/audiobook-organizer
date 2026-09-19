// file: internal/server/handlers/abs/browse_filters.go
// version: 1.1.0
// guid: bb2cd357-d7f1-4d95-a61d-676c82fdc680
// last-edited: 2026-09-19

package abs

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ── ?filter=<group>.<base64 value> → the matching VISIBLE book ids ─────────
//
// ONE resolver for every surface that filters by group: /items (filteredItems)
// and the series tab (LibrarySeries). Keeping it in one place is what stops the
// tile and the drill-down from disagreeing.
//
// 🔴 WHAT WAS BROKEN (item-6 B1, 2026-09-19). Only series, authors and narrators
// were implemented; every other group the filter picker offers — genres, tags,
// languages, publishers, publishedDecades, progress — fell into the fail-empty
// default, so picking one showed "no books". Measured on prod: filter
// genres.<b64 "Fiction"> answered total 0 while search reported Fiction with
// numItems 1.
//
// Every attribute group matches on the SAME derivation the item mapper renders
// (splitList for genres, yearString for the decade, the raw Language/Publisher),
// so a book shown with a value is exactly a book the filter for that value finds.

// filterGroupStatus reports how a group resolved.
type filterGroupStatus int

const (
	filterResolved filterGroupStatus = iota
	// filterBadValue: the group is known but the value cannot name anything
	// (a non-numeric series id, an unknown progress state).
	filterBadValue
	// filterUnknownGroup: this server does not implement the group.
	filterUnknownGroup
)

// Progress filter values, as upstream ABS and AudioBooth spell them.
const (
	absProgressFinished    = "finished"
	absProgressInProgress  = "in-progress"
	absProgressNotStarted  = "not-started"
	absProgressNotFinished = "not-finished"
)

// filterGroupBookIDs resolves one decoded filter to book ids. For series the
// order is reading order; for every other group it is the visible-summary order
// (callers apply any requested sort themselves). The returned slice may be a
// shared cached grouping and must not be modified.
func (h *Handler) filterGroupBookIDs(c *gin.Context, group, value string) ([]string, filterGroupStatus, error) {
	value = strings.TrimSpace(value)
	switch group {
	case "series":
		seriesID, err := strconv.Atoi(value)
		if err != nil {
			return nil, filterBadValue, nil
		}
		bySeries, err := h.seriesBooksCached()
		if err != nil {
			return nil, filterResolved, err
		}
		// The SAME grouping the series list renders from, already in reading order.
		return bySeries[seriesID].bookIDs, filterResolved, nil
	case "authors":
		// The value is the author ID the /authors list published (strconv.Itoa of
		// the store's int id), not a name.
		authorID, err := strconv.Atoi(value)
		if err != nil {
			return nil, filterBadValue, nil
		}
		idx, err := h.contributorsCached(c.Request.Context())
		if err != nil {
			return nil, filterResolved, err
		}
		return idx.authorBooks[authorID], filterResolved, nil
	case "narrators":
		// Narrators are addressed by NAME: narratorID is base64 of the name, so the
		// decoded token is the name. Verified against the live client, which sent
		// narrators.<id from /narrators> and prod logged the decoded name.
		idx, err := h.contributorsCached(c.Request.Context())
		if err != nil {
			return nil, filterResolved, err
		}
		return idx.narratorBooks[value], filterResolved, nil
	case "genres":
		return h.visibleBooksMatching(func(b *visibleBookAttrs) bool { return genreMatches(b.genre, value) })
	case "languages":
		return h.visibleBooksMatching(func(b *visibleBookAttrs) bool {
			return b.language != nil && strings.EqualFold(strings.TrimSpace(*b.language), value)
		})
	case "publishers":
		return h.visibleBooksMatching(func(b *visibleBookAttrs) bool {
			return b.publisher != nil && strings.EqualFold(strings.TrimSpace(*b.publisher), value)
		})
	case "publishedDecades", "decades":
		decade, err := strconv.Atoi(value)
		if err != nil {
			return nil, filterBadValue, nil
		}
		return h.visibleBooksMatching(func(b *visibleBookAttrs) bool {
			return b.year != 0 && b.year/10*10 == decade
		})
	case "tags":
		return h.visibleTaggedBooks(value)
	case "progress":
		return h.progressFilterBookIDs(c, value)
	}
	return nil, filterUnknownGroup, nil
}

// genreMatches reports whether a stored genre field carries value. The item
// mapper renders genres as splitList(Genre) (comma-separated parts), while
// /filterdata offers the RAW stored string, so a combined value like
// "Fantasy, Adventure" can be offered as a single choice. Accepting either the
// whole field or any part keeps both entry points consistent with what is shown.
func genreMatches(raw *string, value string) bool {
	if raw == nil || value == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(*raw), value) {
		return true
	}
	for _, part := range splitList(raw) {
		if strings.EqualFold(part, value) {
			return true
		}
	}
	return false
}

// bookYear mirrors yearString: the audiobook release year, else the print year;
// 0 means unknown.
func bookYear(release, print *int) int {
	if release != nil && *release != 0 {
		return *release
	}
	if print != nil {
		return *print
	}
	return 0
}

// visibleBookAttrs is one visible book's filterable attributes, projected once.
type visibleBookAttrs struct {
	id        string
	genre     *string
	language  *string
	publisher *string
	year      int
}

// visibleAttrIndex is the cached projection: every visible book, in summary
// order, with the attributes the filters test. Immutable once published.
type visibleAttrIndex struct {
	books []visibleBookAttrs
	ids   []string
}

// absAttrIndexTTL matches the series grouping: filters see library changes
// within the same window the series and contributor tabs do.
const absAttrIndexTTL = absSeriesBooksCacheTTL

// visibleAttrs returns the cached visible-book attribute index, building it on
// a miss (concurrent misses share one build).
//
// 🔴 WHY CACHED (review W6): uncached, every filter request — and every PAGE of
// a filtered list, since the client pages through it — walked the visible
// summaries AND the whole BookCore projection of the library. The index is
// built from one such pass and then serves every group and every page from
// memory until it expires.
func (h *Handler) visibleAttrs() (*visibleAttrIndex, error) {
	h.attrIndexMu.Lock()
	idx, at := h.attrIndex, h.attrIndexAt
	h.attrIndexMu.Unlock()
	if idx != nil && h.now().Sub(at) < absAttrIndexTTL {
		return idx, nil
	}
	v, err, _ := h.attrIndexSF.Do("attr-index", func() (any, error) {
		built, err := h.buildVisibleAttrs()
		if err != nil {
			return nil, err
		}
		h.attrIndexMu.Lock()
		h.attrIndex, h.attrIndexAt = built, h.now()
		h.attrIndexMu.Unlock()
		return built, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*visibleAttrIndex), nil
}

// buildVisibleAttrs makes one visible-summary pass and one chunked BookCore
// walk. The per-row work is a map lookup and a copy of four fields — no DB,
// network or hashing per item — so it is a plain loop, not a worker pool.
func (h *Handler) buildVisibleAttrs() (*visibleAttrIndex, error) {
	summaries, err := h.visibleBookSummaries(absItemFilterBase())
	if err != nil {
		return nil, err
	}
	pos := make(map[string]int, len(summaries))
	idx := &visibleAttrIndex{
		books: make([]visibleBookAttrs, len(summaries)),
		ids:   make([]string, len(summaries)),
	}
	for i := range summaries {
		pos[summaries[i].ID] = i
		idx.books[i].id = summaries[i].ID
		idx.ids[i] = summaries[i].ID
	}
	const chunk = 5000
	for offset := 0; ; offset += chunk {
		page, err := h.library.GetAllBooksCore(chunk, offset)
		if err != nil {
			return nil, err
		}
		for i := range page {
			j, ok := pos[page[i].ID]
			if !ok {
				continue
			}
			b := &idx.books[j]
			b.genre, b.language, b.publisher = page[i].Genre, page[i].Language, page[i].Publisher
			b.year = bookYear(page[i].AudiobookReleaseYear, page[i].PrintYear)
		}
		if len(page) < chunk {
			break
		}
	}
	return idx, nil
}

// visibleIDs returns the ids of every book the ABS surface shows, in summary
// order (from the cached index). Same predicate as /items (absItemFilterBase),
// so a filter can never surface a book the library tab hides. The slice is
// shared and must not be modified.
func (h *Handler) visibleIDs() ([]string, error) {
	idx, err := h.visibleAttrs()
	if err != nil {
		return nil, err
	}
	return idx.ids, nil
}

// visibleBooksMatching returns the visible books match accepts, from the index.
func (h *Handler) visibleBooksMatching(match func(b *visibleBookAttrs) bool) ([]string, filterGroupStatus, error) {
	idx, err := h.visibleAttrs()
	if err != nil {
		return nil, filterResolved, err
	}
	out := make([]string, 0)
	for i := range idx.books {
		if match(&idx.books[i]) {
			out = append(out, idx.books[i].id)
		}
	}
	return out, filterResolved, nil
}

// visibleTaggedBooks resolves a tag through the tag index. Note the item DTO
// renders `tags: []` and /filterdata offers no tags today, so no client picker
// currently produces this filter; it is answered from the real tag index so
// that a client that sends one gets the right books rather than none.
func (h *Handler) visibleTaggedBooks(tag string) ([]string, filterGroupStatus, error) {
	if tag == "" {
		return nil, filterBadValue, nil
	}
	tagged, err := h.library.GetBooksByTag(tag)
	if err != nil {
		return nil, filterResolved, err
	}
	set := make(map[string]struct{}, len(tagged))
	for _, id := range tagged {
		set[id] = struct{}{}
	}
	visible, err := h.visibleIDs()
	if err != nil {
		return nil, filterResolved, err
	}
	return keepIDs(visible, func(id string) bool { _, ok := set[id]; return ok }), filterResolved, nil
}

// progressFilterRow is the subset of a mediaProgress row the filter reads.
type progressFilterRow struct {
	LibraryItemID string  `json:"libraryItemId"`
	IsFinished    bool    `json:"isFinished"`
	CurrentTime   float64 `json:"currentTime"`
	Progress      float64 `json:"progress"`
}

// progressFilterBookIDs implements ABS's per-user progress filter:
//
//	finished      isFinished
//	in-progress   started (currentTime or progress > 0) and not finished
//	not-started   no started-or-finished progress row
//	not-finished  not finished (includes not-started)
//
// The user's progress list comes from the same complete provider /api/me uses.
// Unlike a sort, a filter must NOT degrade silently on a provider error: an empty
// "finished" shelf would read as "you have finished nothing", so it is an error.
func (h *Handler) progressFilterBookIDs(c *gin.Context, state string) ([]string, filterGroupStatus, error) {
	switch state {
	case absProgressFinished, absProgressInProgress, absProgressNotStarted, absProgressNotFinished:
	default:
		return nil, filterBadValue, nil
	}
	visible, err := h.visibleIDs()
	if err != nil {
		return nil, filterResolved, err
	}
	finished := map[string]struct{}{}
	started := map[string]struct{}{}
	user, ok := servermiddleware.CurrentUser(c)
	if ok && user != nil && h.userData != nil {
		rows, err := h.userData.MediaProgress(user.ID)
		if err != nil {
			return nil, filterResolved, err
		}
		for _, raw := range rows {
			var row progressFilterRow
			bookID := ""
			if dto, typed := raw.(mediaProgressDTO); typed && dto.bookID != "" {
				// The production provider's rows carry the book they were
				// rendered from: no JSON round trip, no sync-keyspace lookup.
				row = progressFilterRow{LibraryItemID: dto.LibraryItemID, IsFinished: dto.IsFinished,
					CurrentTime: dto.CurrentTime, Progress: dto.Progress}
				bookID = dto.bookID
			} else {
				// Any other provider: decode the wire shape and resolve the id.
				buf, merr := json.Marshal(raw)
				if merr != nil {
					continue
				}
				if uerr := json.Unmarshal(buf, &row); uerr != nil || row.LibraryItemID == "" {
					continue
				}
				id, rerr := h.bookIDForSyncID(row.LibraryItemID)
				if rerr != nil {
					return nil, filterResolved, rerr
				}
				bookID = id
			}
			if bookID == "" {
				continue
			}
			if row.IsFinished {
				finished[bookID] = struct{}{}
			} else if row.CurrentTime > 0 || row.Progress > 0 {
				started[bookID] = struct{}{}
			}
		}
	}
	in := func(m map[string]struct{}) func(string) bool {
		return func(id string) bool { _, ok := m[id]; return ok }
	}
	switch state {
	case absProgressFinished:
		return keepIDs(visible, in(finished)), filterResolved, nil
	case absProgressInProgress:
		return keepIDs(visible, in(started)), filterResolved, nil
	case absProgressNotStarted:
		return keepIDs(visible, func(id string) bool { return !in(finished)(id) && !in(started)(id) }), filterResolved, nil
	default: // not-finished
		return keepIDs(visible, func(id string) bool { return !in(finished)(id) }), filterResolved, nil
	}
}

// keepIDs returns the ids keep accepts, in order, as a new slice.
func keepIDs(ids []string, keep func(string) bool) []string {
	out := make([]string, 0)
	for _, id := range ids {
		if keep(id) {
			out = append(out, id)
		}
	}
	return out
}

// logUnresolvedFilter records a filter answered with an empty page. The log is
// how the next group to implement gets chosen: no fixture carries these, so
// watching the client send them is the only oracle.
func logUnresolvedFilter(surface, group, value string, status filterGroupStatus) {
	reason := "unimplemented filter group"
	if status == filterBadValue {
		reason = "filter value does not name anything in its group"
	}
	clientSortLog.Warn("abs: %s filter served an empty page: reason=%s group=%s value=%s",
		surface, reason, logger.SanitizeLogValue(group), logger.SanitizeLogValue(value))
}
