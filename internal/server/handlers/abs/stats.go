// file: internal/server/handlers/abs/stats.go
// version: 1.0.0
// guid: a71e5c04-3d68-4b29-85f0-c26d914b7e38
// last-edited: 2026-08-02

package abs

import (
	"net/http"
	"strconv"
	"strings"

	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Listening statistics.
//
// 🔴 THESE EXIST TO STOP A 404, AND THE 404 WAS NOT HARMLESS.
//
// Spec §1.8.6 said to "prefer 404" here, reasoning that the endpoints carry ~12
// non-optional fields and callers wrap them in `try?`. Both halves of that were
// wrong, and the second one is the expensive mistake:
//
//  1. ListeningStats has FOUR required fields, not twelve
//     (totalTime, days, dayOfWeek, today — recentSessions and items are optional).
//     Verified against AudioBooth's own model, not inferred.
//
//  2. `try?` swallows the ERROR, but the SIDE EFFECT already happened.
//     NetworkService.performRequest sets the server's status on every response:
//
//     guard 200...299 ~= httpResponse.statusCode else { ... updateStatus(.connectionError) }
//     await updateStatus(.connected)
//
//     ANY non-2xx flips the connection indicator to `.connectionError` — the orange
//     dot on the home screen — and the next 2xx flips it back. /api/me/listening-stats
//     is requested on every home-screen refresh, so a deliberate 404 there showed up
//     as the owner-reported "it still turns orange randomly".
//
// So these answer 200 with a SHAPE-COMPLETE, TRUTHFUL body. Truthful is the important
// word: this server does not persist listening sessions (play sessions are in-memory
// by design — see play.go), so an empty session history is the correct answer rather
// than a placeholder. Only totals we can actually substantiate are reported.
//
// Covers do NOT go through this path — the client builds cover URLs directly for
// Nuke/AsyncImage — so the ~80% of books with no cover art cannot trip the indicator.

// listeningStatsResponse matches AudioBooth's `ListeningStats` exactly.
//
// days and dayOfWeek are objects keyed by date / weekday. They are emitted EMPTY, not
// fabricated: attributing a book's whole listened total to its last-activity date
// would put invented numbers on the user's stats screen, and this server keeps no
// per-day listening history to derive them from honestly. Empty maps decode fine.
type listeningStatsResponse struct {
	Today     float64            `json:"today"`
	TotalTime float64            `json:"totalTime"`
	Days      map[string]float64 `json:"days"`
	DayOfWeek map[string]float64 `json:"dayOfWeek"`
}

// ListeningStats handles GET /api/me/listening-stats.
func (h *Handler) ListeningStats(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	total := 0.0
	if h.userData != nil {
		if seconds, err := h.userData.ListenedSeconds(user.ID); err == nil {
			total = seconds
		}
		// A read failure reports 0 rather than 5xx. A 5xx trips the SAME
		// connection-error indicator this endpoint exists to keep green, so failing
		// here would swap one cosmetic bug for the identical one.
	}

	respondJSON(c, http.StatusOK, listeningStatsResponse{
		Today:     0, // no per-day history to derive this from; see the type comment
		TotalTime: total,
		Days:      map[string]float64{},
		DayOfWeek: map[string]float64{},
	})
}

// listeningSessionsPage matches AudioBooth's `ListeningHistoryResponse`. Every field
// is required; `sessions` must be `[]` and never null.
type listeningSessionsPage struct {
	ItemsPerPage int   `json:"itemsPerPage"`
	NumPages     int   `json:"numPages"`
	Page         int   `json:"page"`
	Sessions     []any `json:"sessions"`
	Total        int   `json:"total"`
}

// ListeningSessions handles GET /api/me/listening-sessions.
//
// Always an empty page: play sessions are in-memory and deliberately not persisted
// (play.go — the durable thing is the user's POSITION, which is written through on
// every sync). An empty history is therefore the truthful answer, not a stub.
func (h *Handler) ListeningSessions(c *gin.Context) {
	if _, ok := servermiddleware.CurrentUser(c); !ok {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	respondJSON(c, http.StatusOK, listeningSessionsPage{
		ItemsPerPage: queryInt(c, "itemsPerPage", 10),
		NumPages:     0,
		Page:         queryInt(c, "page", 0),
		Sessions:     []any{},
		Total:        0,
	})
}

// itemListeningSessionsPage matches the anonymous response struct AudioBooth decodes
// for GET /api/me/item/listening-sessions/:id. Note it has NO `total` — the shapes
// differ between the two session endpoints, so they do not share a type.
type itemListeningSessionsPage struct {
	ItemsPerPage int   `json:"itemsPerPage"`
	NumPages     int   `json:"numPages"`
	Page         int   `json:"page"`
	Sessions     []any `json:"sessions"`
}

// ItemListeningSessions handles GET /api/me/item/listening-sessions/:id.
func (h *Handler) ItemListeningSessions(c *gin.Context) {
	if _, ok := servermiddleware.CurrentUser(c); !ok {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	respondJSON(c, http.StatusOK, itemListeningSessionsPage{
		ItemsPerPage: queryInt(c, "itemsPerPage", 10),
		NumPages:     0,
		Page:         queryInt(c, "page", 0),
		Sessions:     []any{},
	})
}

// yearStatsResponse matches AudioBooth's `YearStats`. The four optional pointers are
// omitted; every array must be present and non-null.
type yearStatsResponse struct {
	BooksWithCovers           []string `json:"booksWithCovers"`
	FinishedBooksWithCovers   []string `json:"finishedBooksWithCovers"`
	NumBooksFinished          int      `json:"numBooksFinished"`
	NumBooksListened          int      `json:"numBooksListened"`
	TopAuthors                []any    `json:"topAuthors"`
	TopGenres                 []any    `json:"topGenres"`
	TotalBookListeningTime    float64  `json:"totalBookListeningTime"`
	TotalListeningSessions    int      `json:"totalListeningSessions"`
	TotalListeningTime        float64  `json:"totalListeningTime"`
	TotalPodcastListeningTime float64  `json:"totalPodcastListeningTime"`
}

// YearStats handles GET /api/me/stats/year/:year.
//
// Zeroed for the same reason ListeningSessions is empty: a year breakdown needs
// per-session history this server does not keep. The `:year` parameter is accepted
// and ignored rather than validated — a client asking about a year we have no data
// for should see an empty year, not an error.
func (h *Handler) YearStats(c *gin.Context) {
	if _, ok := servermiddleware.CurrentUser(c); !ok {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	respondJSON(c, http.StatusOK, yearStatsResponse{
		BooksWithCovers:         []string{},
		FinishedBooksWithCovers: []string{},
		TopAuthors:              []any{},
		TopGenres:               []any{},
	})
}

// queryInt reads a non-negative integer query parameter, falling back to def.
// Echoing the client's own paging values keeps the response self-consistent.
func queryInt(c *gin.Context, key string, def int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}
