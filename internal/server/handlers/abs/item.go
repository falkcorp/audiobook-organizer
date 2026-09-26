// file: internal/server/handlers/abs/item.go
// version: 1.4.1
// guid: 9c8a2f60-1d75-4b38-a0e4-7f21b5c96d13
// last-edited: 2026-09-25

package abs

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/covers"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/gin-gonic/gin"
)

// absProgressSegmentID is the segment key the ABS surface stores its whole-book
// position under.
//
// The existing UserPosition keyspace is per (user, book, segment) because the app's
// own reader tracks a position per chapter/segment. ABS has exactly one whole-book
// position per item, so it gets one reserved segment id rather than pretending to be
// a segment. SetUserPosition rejects an empty segment id, so this cannot be "".
const absProgressSegmentID = "abs"

// Item handles GET /api/items/:id.
//
// `?expanded=1` and `?include=progress` are both honoured, but neither gates content
// we would otherwise withhold:
//
//   - The expanded shape is always returned. Real ABS gates the timeline behind
//     expanded=1, but the minified shape is a strict subset, and AudioBooth's play
//     path reads the item it already has — sending less would only create a second
//     shape to keep correct.
//   - userMediaProgress is emitted whenever we have a progress record, regardless of
//     ?include=progress. §1.6 item 3: some clients ignore the gate, and an
//     absent-but-known progress is indistinguishable from "never started".
func (h *Handler) Item(c *gin.Context) {
	book, ref := h.resolveItemAs(c)
	if book == nil {
		return
	}
	view, err := h.loadItemView(c.Request.Context(), book)
	if errors.Is(err, errRedirectedSyncID) {
		respondError(c, http.StatusNotFound, "library item not found")
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library item")
		return
	}
	// Render under the id the client asked with (resolveItemAs). The view is
	// this request's own copy; the book and its storage stay canonical, and
	// every URL built from the id (tracks, cover) resolves back through the
	// same redirect.
	view.SyncID = ref.RequestedID

	out := itemWithProgressDTO{libraryItemExpandedDTO: h.expandedItem(view)}
	if user, ok := servermiddleware.CurrentUser(c); ok && user != nil {
		out.UserMediaProgress = h.mediaProgress(user.ID, view)
	}
	respondJSON(c, http.StatusOK, out)
}

// mediaProgress renders the (user, item) progress record, or nil when the user has
// never started the book.
//
// Every field here is a §1.7.3 / §1.8.7 requirement, not a nicety:
//
//   - LastUpdate (ms epoch) is the single highest-value field in the protocol: omit
//     it and the server permanently loses every conflict, because clients compare it
//     against their own wall clock and TIES GO TO LOCAL. AudioBooth truncates via
//     integer /1000 and compares with strict >, so two writes inside the same second
//     compare equal and ours is discarded.
//   - Duration is always sent alongside IsFinished: `isFinished:true` with a null
//     duration sets the client's currentTime to 0 (MediaProgress.swift:137-140).
//   - Progress is a 0.0–1.0 FRACTION, not a percentage (§1.8.5 item 11).
//   - IsFinished uses the ≥2 s tolerance of §5b, not a tight epsilon: a book's three
//     legitimate durations disagree by ~52 ms, so a tight epsilon leaves a
//     fully-listened book stuck at 99% forever.
func (h *Handler) mediaProgress(userID string, v *itemView) *mediaProgressDTO {
	if h.progress == nil {
		return nil
	}
	pos := h.displayPosition("item mediaProgress", userID, v.Book.ID)
	if pos == nil {
		return nil
	}

	state, _ := h.progress.GetUserBookState(userID, v.Book.ID)
	finished := progress.IsWithinFinishedTolerance(pos.PositionSeconds, v.DurationSec)
	if state != nil && state.Status == database.UserBookStatusFinished {
		finished = true
	}

	fraction := 0.0
	if v.DurationSec > 0 {
		fraction = pos.PositionSeconds / v.DurationSec
		if fraction > 1 {
			fraction = 1
		}
	}

	started := msEpoch(pos.UpdatedAt)
	if state != nil && !state.LastActivityAt.IsZero() {
		started = msEpoch(state.LastActivityAt)
	}
	var finishedAt *int64
	if finished {
		at := msEpoch(pos.UpdatedAt)
		finishedAt = &at
	}

	return &mediaProgressDTO{
		CurrentTime:   pos.PositionSeconds,
		Duration:      v.DurationSec,
		EbookProgress: 0,
		FinishedAt:    finishedAt,
		// Must agree with userdata.go's progressRow for the same book: a client
		// that sees the book hidden on /api/me and visible on /api/items shows it
		// in Continue Listening anyway, which is the bug the flag exists to fix.
		HideFromContinueListening: state != nil && state.HideFromContinueListening,
		// The id is derived from (user, item) rather than random so a client that
		// stores it keeps matching the same row across restarts.
		ID:            userID + "-" + v.SyncID,
		IsFinished:    finished,
		LastUpdate:    msEpoch(pos.UpdatedAt),
		LibraryItemID: v.SyncID,
		MediaItemID:   v.SyncID,
		MediaItemType: "book",
		Progress:      fraction,
		StartedAt:     started,
		UserID:        userID,
	}
}

// ── GET /api/items/:id/cover ────────────────────────────────────────────────

// localCoverURLPrefix is the API path the app records in Book.CoverURL for a
// cover stored on this server (server/covers.go handleLocalCover serves it).
const localCoverURLPrefix = "/api/v1/covers/local/"

// coverFile resolves the on-disk cover for a book, or "" when there is none.
//
// Two places hold a book's cover on disk, and the web UI reads both:
//
//  1. {root}/covers/<bookID>.<ext>, where a downloaded or uploaded cover is
//     written (metadata.CoverPathForBook). Tried first.
//  2. The file Book.CoverURL names when it is a LOCAL cover URL,
//     "/api/v1/covers/local/<name>". Embedded art extracted at import is stored
//     as {root}/.covers/<sha256>.<ext> (metadata.ExtractCoverArt), and a merged
//     or relinked book can point at another book's id-named file. Neither is at
//     step 1's path, so before this fallback ABS answered coverPath=null and a
//     404 for a book the web UI showed with a cover (ABS-COVER-LOCAL,
//     2026-09-25). The name is resolved exactly as handleLocalCover resolves
//     it: covers.FindCoverFile under .covers then covers, after the same
//     separator and ".." rejection.
//
// A remote CoverURL (http...) is not a disk file and is never used here.
func (h *Handler) coverFile(book *database.Book) string {
	if h.coverRoot == "" || book == nil {
		return ""
	}
	if p := metadata.CoverPathForBook(h.coverRoot, book.ID); p != "" {
		return p
	}
	name, ok := localCoverName(book.CoverURL)
	if !ok {
		return ""
	}
	p, err := covers.FindCoverFile(name, h.coverRoot)
	if err != nil {
		return ""
	}
	return p
}

// localCoverName extracts the file name from a local cover URL, reporting
// ok=false for anything else: nil, a remote URL, a query string, or a name
// that could leave the covers directory.
func localCoverName(coverURL *string) (string, bool) {
	if coverURL == nil {
		return "", false
	}
	name, found := strings.CutPrefix(strings.TrimSpace(*coverURL), localCoverURLPrefix)
	if !found || name == "" || name == "." ||
		strings.ContainsAny(name, "/\\?#") || strings.Contains(name, "..") {
		return "", false
	}
	return name, true
}

// coverContentType maps a cover file extension to its MIME type. A cover served as
// application/octet-stream renders as a broken image in both clients.
func coverContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// ItemCover handles GET /api/items/:id/cover.
//
// 🔓 THIS ROUTE IS INTENTIONALLY UNAUTHENTICATED AT THE APP LAYER.
// §1.8.8 item 7 / §1.9.5: AudioBooth's widget extension does not import the API
// package and sends NO credentials at all — not a bearer, not ?token= — and it has no
// other path to cover art (Nuke's DataCache is process-local, not in the App Group,
// and neither client extracts embedded artwork). Requiring auth here means the
// home-screen widget shows a generic book icon forever.
//
// It still accepts ?token= for Absorb/CarPlay, and it stays Cloudflare-Access-gated
// at the edge in Modes B/C except for the deliberate, owner-approved cover bypass.
// The residual exposure is bounded and documented: cover images become fetchable by
// anyone who knows a 36-char item UUID — no metadata, no audio, no progress, no auth
// surface, and UUIDs are not enumerable.
//
// width/raw/format are ACCEPTED and never error (§1.8.8 item 7). See the note on
// resizing below.
func (h *Handler) ItemCover(c *gin.Context) {
	book := h.resolveItem(c)
	if book == nil {
		return
	}
	path := h.coverFile(book)
	if path == "" {
		// A 404 here is correct and harmless: both clients fall back to a placeholder.
		respondError(c, http.StatusNotFound, "cover not found")
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not resolve cover")
		return
	}

	// The image parameters are parsed and validated so a malformed one cannot reach
	// anything, but no transform is applied:
	//
	//   width=N  — DEVIATION, flagged: we do not downscale. Serving the full-size
	//              image is always decodable by both clients (they scale locally); it
	//              only costs bandwidth. Implementing real resizing needs an image
	//              codec + scaler this package does not have, and a wrong resize is
	//              far worse than none.
	//   raw=1    — already what we do: the stored file, unmodified.
	//   format=  — the stored file's real type is reported in Content-Type. Lying
	//              about the format in the header would break decoding.
	if raw := strings.TrimSpace(c.Query("width")); raw != "" {
		if _, err := strconv.Atoi(raw); err != nil {
			// Still 200 with the image: erroring on a decorative parameter would
			// blank cover art across the whole UI.
			_ = err
		}
	}

	// ServeFileWithRange gives ETag + Last-Modified + conditional-request handling,
	// so clients cache covers instead of refetching them on every grid scroll.
	if err := httputil.ServeFileWithRange(c.Writer, c.Request, abs, httputil.Options{
		ContentType: coverContentType(abs),
	}); err != nil {
		respondError(c, http.StatusNotFound, "cover not found")
		return
	}
	c.Abort()
}

// displayPosition is the stored position for a DISPLAY-only path (it writes
// nothing), so it fails OPEN, never 503: an unreadable position shows as "no
// progress" rather than failing the page. Every failure is logged at Warn with
// user_id and book_id. When some of the book's position rows did not decode,
// the readable ones are used (database.ReadablePositionsDespite), so one
// corrupt segment row does not hide the good row or drop the book from
// Continue Listening.
func (h *Handler) displayPosition(op, userID, bookID string) *database.UserPosition {
	pos, err := h.progress.GetUserPosition(userID, bookID)
	if err == nil {
		return pos
	}
	if rows, skipped, ok := database.ReadablePositionsDespite(err); ok {
		progressLog.Warn("abs: %s: user_id=%s book_id=%s: %d position row(s) undecodable, showing the %d readable: %v",
			op, logger.SanitizeLogValue(userID), logger.SanitizeLogValue(bookID), skipped, len(rows), err)
		return database.LatestPosition(rows)
	}
	progressLog.Warn("abs: %s: user_id=%s book_id=%s: position unreadable, showing no progress: %v",
		op, logger.SanitizeLogValue(userID), logger.SanitizeLogValue(bookID), err)
	return nil
}
