// file: internal/server/handlers/abs/browse_filters.go
// version: 1.0.0
// guid: bb2cd357-d7f1-4d95-a61d-676c82fdc680
// last-edited: 2026-09-19

package abs

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
		return h.visibleBooksMatching(func(b *database.BookCore) bool { return genreMatches(b.Genre, value) })
	case "languages":
		return h.visibleBooksMatching(func(b *database.BookCore) bool {
			return b.Language != nil && strings.EqualFold(strings.TrimSpace(*b.Language), value)
		})
	case "publishers":
		return h.visibleBooksMatching(func(b *database.BookCore) bool {
			return b.Publisher != nil && strings.EqualFold(strings.TrimSpace(*b.Publisher), value)
		})
	case "publishedDecades", "decades":
		decade, err := strconv.Atoi(value)
		if err != nil {
			return nil, filterBadValue, nil
		}
		return h.visibleBooksMatching(func(b *database.BookCore) bool {
			year := bookYear(b.AudiobookReleaseYear, b.PrintYear)
			return year != 0 && year/10*10 == decade
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

// visibleIDs returns the ids of every book the ABS surface shows, in summary
// order. Same predicate as /items (absItemFilterBase), so a filter can never
// surface a book the library tab hides.
func (h *Handler) visibleIDs() ([]string, error) {
	summaries, err := h.visibleBookSummaries(absItemFilterBase())
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(summaries))
	for i := range summaries {
		ids[i] = summaries[i].ID
	}
	return ids, nil
}

// visibleBooksMatching walks the BookCore projection once (chunked, like
// visibleBookSummaries) and returns the visible books match accepts.
//
// This is a projection walk with a trivial predicate per row — no per-item DB
// read, network call or hashing — so it is a plain loop rather than a worker
// pool. Filters on these groups are rare user actions, not polled endpoints, so
// the result is not cached.
func (h *Handler) visibleBooksMatching(match func(b *database.BookCore) bool) ([]string, filterGroupStatus, error) {
	visible, err := h.visibleIDs()
	if err != nil {
		return nil, filterResolved, err
	}
	matched := make(map[string]struct{})
	const chunk = 5000
	for offset := 0; ; offset += chunk {
		page, err := h.library.GetAllBooksCore(chunk, offset)
		if err != nil {
			return nil, filterResolved, err
		}
		for i := range page {
			if match(&page[i]) {
				matched[page[i].ID] = struct{}{}
			}
		}
		if len(page) < chunk {
			break
		}
	}
	return keepIDs(visible, func(id string) bool { _, ok := matched[id]; return ok }), filterResolved, nil
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
			buf, merr := json.Marshal(raw)
			if merr != nil {
				continue
			}
			var row progressFilterRow
			if uerr := json.Unmarshal(buf, &row); uerr != nil || row.LibraryItemID == "" {
				continue
			}
			bookID := h.bookIDForSyncID(row.LibraryItemID)
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
