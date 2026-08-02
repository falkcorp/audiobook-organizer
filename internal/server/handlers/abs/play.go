// file: internal/server/handlers/abs/play.go
// version: 1.1.0
// guid: b06d4a13-5f28-4c71-9e0a-38f2c7d915e6
// last-edited: 2026-08-02

package abs

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/gin-gonic/gin"
)

// Phase 5b — playback.
//
// DIRECT PLAY ONLY. There is no transcoder and no HLS packager, and that is a design
// decision rather than a gap: both target clients play m4b/mp3 natively over HTTP
// Range, and a transcode path would add a process pool, a temp-file lifecycle and a
// whole class of "why is my battery dead" bugs for zero benefit. A client that asks
// for HLS gets a working direct-play session instead of an error (see Play).

// sessionTTL is how long an idle play session stays resolvable.
//
// Generous on purpose. §1.8.8 item 8: AudioBooth cannot detect a 404-expired session
// (it rewraps errors and loses the status code), so it never re-creates one — an
// aggressive TTL turns into a client that silently stops syncing. The session holds
// only ids and offsets, so keeping it around is cheap.
const sessionTTL = 24 * time.Hour

// playSession is one live listening session.
type playSession struct {
	ID        string
	UserID    string
	BookID    string
	SyncID    string
	StartedAt time.Time

	// Paths is the ordered on-disk path per track index (1-based), captured at
	// session start so /public/session/:id/track/:index needs no DB access and no
	// auth — it is a pure capability lookup.
	Paths []string

	// DurationSec is the session's authoritative duration: the sum of the track
	// durations (spec §5b requirement to pick ONE and use it consistently).
	DurationSec float64

	mu sync.Mutex
	// CurrentTime is the last position the client reported.
	CurrentTime float64
	// TimeListening is the running total of listened seconds. /sync's `timeListened`
	// is a DELTA we ADD; offline replay's `timeListening` is a CUMULATIVE total we
	// SET (§1.8.4 — the name trap that makes abs-shim record zero listening time).
	TimeListening float64
	UpdatedAt     time.Time
}

func (s *playSession) snapshot() (currentTime, timeListening float64, updatedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CurrentTime, s.TimeListening, s.UpdatedAt
}

// sessionRegistry holds live sessions in memory.
//
// In-memory rather than persisted, deliberately: a session is ephemeral playback
// state, and the durable thing — the user's position — is written straight through to
// the progress store on every sync. A restart therefore loses no listening position,
// only the session handle, and the /sync path is built to answer an unknown session
// id idempotently rather than 404 (§1.8.8 item 8), so a restart mid-listen is
// invisible to the client.
type sessionRegistry struct {
	mu   sync.RWMutex
	byID map[string]*playSession
	now  func() time.Time
}

func newSessionRegistry(now func() time.Time) *sessionRegistry {
	return &sessionRegistry{byID: map[string]*playSession{}, now: now}
}

func (r *sessionRegistry) put(s *playSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictExpiredLocked()
	r.byID[s.ID] = s
}

func (r *sessionRegistry) get(id string) (*playSession, bool) {
	r.mu.RLock()
	s, ok := r.byID[id]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	_, _, updatedAt := s.snapshot()
	if r.now().Sub(updatedAt) > sessionTTL {
		r.remove(id)
		return nil, false
	}
	return s, true
}

func (r *sessionRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
}

// evictExpiredLocked keeps the map from growing without bound. Called on insert
// rather than from a ticker so there is no background goroutine to leak.
func (r *sessionRegistry) evictExpiredLocked() {
	cutoff := r.now().Add(-sessionTTL)
	for id, s := range r.byID {
		if _, _, updatedAt := s.snapshot(); updatedAt.Before(cutoff) {
			delete(r.byID, id)
		}
	}
}

// SessionTimeListening exposes a session's running listened total. Test-only
// accessor: /sync answers a bare "OK" like real ABS, so the accumulation semantics of
// §1.8.4 are otherwise unobservable from outside.
func (h *Handler) SessionTimeListening(sessionID string) float64 {
	s, ok := h.sessions.get(sessionID)
	if !ok {
		return 0
	}
	_, total, _ := s.snapshot()
	return total
}

// newSessionID mints a 36-char canonical UUIDv4.
//
// The length is not cosmetic: Absorb splits compound ids at a fixed offset of 36
// (§1.7.1), and this id also travels in the UNAUTHENTICATED
// /public/session/:id/track/:index path, where its 122 bits of entropy are the only
// thing standing between a stranger and one book's audio.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ── POST /api/items/:id/play ────────────────────────────────────────────────

// playRequest is the body both clients POST. Every field is optional: a client that
// sends nothing at all still gets a valid session.
type playRequest struct {
	DeviceInfo         map[string]any `json:"deviceInfo"`
	ForceDirectPlay    *bool          `json:"forceDirectPlay"`
	ForceTranscode     *bool          `json:"forceTranscode"`
	MediaPlayer        string         `json:"mediaPlayer"`
	SupportedMimeTypes []string       `json:"supportedMimeTypes"`
}

// Play handles POST /api/items/:id/play.
func (h *Handler) Play(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	book := h.resolveItem(c)
	if book == nil {
		return
	}

	// A malformed body is ignored rather than rejected: forceTranscode/mediaPlayer are
	// hints we do not act on anyway, and a 400 here would look to the client like the
	// book is unplayable.
	var req playRequest
	_ = c.ShouldBindJSON(&req)

	view, err := h.loadItemView(c.Request.Context(), book)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not open play session")
		return
	}

	sessionID, err := newSessionID()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not open play session")
		return
	}

	// 🔴 currentTime MUST be the user's TRUE latest position — never 0, never a
	// session-start snapshot. AudioBooth takes max() on position at session start
	// while IGNORING timestamps (SessionManager.swift:175-180), so a 0 here silently
	// rewinds the user to the beginning of the book (§1.8.7).
	currentTime := 0.0
	if h.progress != nil {
		if pos, perr := h.progress.GetUserPosition(user.ID, book.ID); perr == nil && pos != nil {
			currentTime = pos.PositionSeconds
		}
	}

	now := h.now()
	session := &playSession{
		ID:            sessionID,
		UserID:        user.ID,
		BookID:        book.ID,
		SyncID:        view.SyncID,
		StartedAt:     now,
		Paths:         trackPaths(view),
		DurationSec:   view.DurationSec,
		CurrentTime:   currentTime,
		TimeListening: 0,
		UpdatedAt:     now,
	}
	h.sessions.put(session)

	respondJSON(c, http.StatusOK, h.playSessionDTO(session, view, &req, c))
}

// trackPaths captures the 1-based track index -> path mapping. Index 0 is unused so
// the slice can be indexed directly by the client-visible track index.
func trackPaths(v *itemView) []string {
	paths := make([]string, len(v.Files)+1)
	for i := range v.Files {
		paths[i+1] = v.Files[i].File.FilePath
	}
	return paths
}

// playSessionDTO renders the session response.
func (h *Handler) playSessionDTO(s *playSession, v *itemView, req *playRequest, c *gin.Context) playSessionResponse {
	currentTime, timeListening, _ := s.snapshot()
	item := h.expandedItem(v)
	// Read the metadata off the media block we just built rather than rebuilding it:
	// displayTitle/displayAuthor/mediaMetadata must agree with the embedded
	// libraryItem exactly, or a client shows one title on the now-playing screen and
	// another in the library.
	media, ok := item.Media.(bookMediaExpandedDTO)
	if !ok {
		media = h.expandedMedia(v)
	}

	resp := playSessionResponse{
		BookID:   v.SyncID,
		Chapters: chapterDTOs(v.Chapters),
		// A cover PATH, not a URL: the client builds the URL from the item id.
		CoverPath:     h.coverPath(v.Book.ID),
		CurrentTime:   currentTime,
		Date:          s.StartedAt.Format("2006-01-02"),
		DayOfWeek:     s.StartedAt.Weekday().String(),
		DeviceInfo:    h.deviceInfoDTO(s, req, c),
		DisplayAuthor: media.Metadata.AuthorName,
		DisplayTitle:  media.Metadata.Title,
		Duration:      v.DurationSec,
		EpisodeID:     nil,
		ID:            s.ID,
		LibraryID:     h.libraryID(),
		LibraryItem:   item,
		LibraryItemID: v.SyncID,
		MediaMetadata: media.Metadata,
		MediaPlayer:   fallbackMediaPlayer(req.MediaPlayer),
		MediaType:     "book",
		// 0 == direct play. ALWAYS. There is no transcode and no HLS, and reporting
		// anything else would make a client wait for a playlist that never arrives.
		PlayMethod:    0,
		ServerVersion: h.cfg.ServerVersion,
		StartTime:     currentTime,
		StartedAt:     msEpoch(s.StartedAt),
		TimeListening: timeListening,
		UpdatedAt:     msEpoch(s.UpdatedAt),
		UserID:        s.UserID,
	}

	// 🔴 audioTracks is assigned ONLY when there are tracks. h.audioTracks returns
	// nil (not an empty slice) for a trackless book, and the field is `omitempty`, so
	// the key is OMITTED rather than emitted as []. An explicit "audioTracks": []
	// defeats AudioBooth's `?? orderedTracks` local-track fallback — which only fires
	// on nil — and KILLS PLAYBACK OF AN ALREADY-DOWNLOADED BOOK (§1.8.5 item 3).
	resp.AudioTracks = h.audioTracks(v)
	return resp
}

// fallbackMediaPlayer mirrors what real ABS reports when the client sends nothing.
func fallbackMediaPlayer(raw string) string {
	if raw = strings.TrimSpace(raw); raw != "" {
		return raw
	}
	return "unknown"
}

// deviceInfoDTO echoes the client's deviceInfo, filling in the fields real ABS adds.
// Values are echoed rather than validated: this block is diagnostic, and rejecting a
// client's self-description would fail a session for no protocol reason.
func (h *Handler) deviceInfoDTO(s *playSession, req *playRequest, c *gin.Context) map[string]any {
	out := map[string]any{
		"clientName":    "",
		"clientVersion": h.cfg.ServerVersion,
		"deviceId":      "",
		"deviceName":    "",
		"deviceType":    "unknown",
		"id":            s.ID,
		"ipAddress":     c.ClientIP(),
		"manufacturer":  "",
		"model":         "",
		"sdkVersion":    "",
		"userId":        s.UserID,
	}
	for k, v := range req.DeviceInfo {
		out[k] = v
	}
	return out
}

// ── POST /api/session/:id/sync and /close ───────────────────────────────────

// syncRequest is the /sync body.
//
// BOTH listened-time keys are accepted, and they mean DIFFERENT things (§1.8.4):
//
//	timeListened  (past tense, sent on /sync)          = a DELTA to ADD
//	timeListening (gerund, sent on offline replay)     = a CUMULATIVE total to SET
//
// abs-shim reads `timeListening` on /sync and therefore records ZERO listening time
// from both clients. Reading only one key here would reproduce that bug exactly.
//
// Clients do NOT send isFinished or progress on this path — the server computes them
// (§1.8.7).
type syncRequest struct {
	CurrentTime   *float64 `json:"currentTime"`
	Duration      *float64 `json:"duration"`
	TimeListened  *float64 `json:"timeListened"`
	TimeListening *float64 `json:"timeListening"`
}

// SessionSync handles POST /api/session/:id/sync.
func (h *Handler) SessionSync(c *gin.Context) {
	h.applySessionUpdate(c)
}

// SessionClose handles POST /api/session/:id/close. It applies any final sync in the
// body, then drops the session.
func (h *Handler) SessionClose(c *gin.Context) {
	id := h.applySessionUpdate(c)
	if id != "" {
		h.sessions.remove(id)
	}
}

// applySessionUpdate is the shared /sync and /close body. It always answers 200 with
// a non-empty body and returns the session id it acted on ("" when it acted on none).
//
// 🔴 AN UNKNOWN SESSION ID IS AN IDEMPOTENT 200, NOT A 404.
// §1.8.8 item 8: AudioBooth cannot detect a 404-expired session — it rewraps errors
// and loses the status code — so it will never re-create one, and a 404 strands the
// client silently forever. §1.7.3 item 2 makes the same point for offline replay: a
// 4xx WEDGES THE REPLAY QUEUE PERMANENTLY. So this endpoint ignores what it cannot
// place and reports success.
func (h *Handler) applySessionUpdate(c *gin.Context) string {
	// Answered before anything else can fail, so no error path can produce an empty
	// 200 — fatal for these decoders (§1.8.6).
	defer respondPlainOK(c)

	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		return ""
	}
	var req syncRequest
	_ = c.ShouldBindJSON(&req)

	sessionID := strings.TrimSpace(c.Param("id"))
	session, found := h.sessions.get(sessionID)
	if !found {
		return ""
	}
	// Ownership is enforced but NOT reported: answering 403/404 here would both leak
	// that the session exists and trip the "client can never recover" trap above.
	if session.UserID != user.ID {
		return ""
	}

	session.mu.Lock()
	if req.CurrentTime != nil && *req.CurrentTime >= 0 {
		session.CurrentTime = *req.CurrentTime
	}
	if req.TimeListened != nil {
		// DELTA -> ADD.
		session.TimeListening = progress.AddListenedDelta(session.TimeListening, *req.TimeListened)
	}
	if req.TimeListening != nil {
		// CUMULATIVE -> SET. Idempotent: replaying the same value twice is a no-op,
		// which is what makes an offline backlog safe to re-send.
		session.TimeListening = progress.SetListenedCumulative(session.TimeListening, *req.TimeListening)
	}
	session.UpdatedAt = h.now()
	newPosition := session.CurrentTime
	session.mu.Unlock()

	h.persistProgress(session, newPosition, req.Duration)
	return sessionID
}

// persistProgress writes the session's position through to the durable progress
// store, applying the §5 conflict policy.
//
// Forward-only against the stored value (§5 rule 3): a stale device that woke up
// BEHIND the server can never rewind newer progress, while one that listened FURTHER
// while offline still advances the position. That specific clobber is what would cost
// the owner their place in a book, which is the whole reason this project exists.
//
// A failure here is logged by the caller's audit trail but never surfaced: the
// endpoint has already promised 200, and a client that treats a sync failure as fatal
// would stop syncing altogether.
func (h *Handler) persistProgress(s *playSession, position float64, clientDuration *float64) {
	if h.progress == nil || position <= 0 {
		return
	}

	stored := 0.0
	if pos, err := h.progress.GetUserPosition(s.UserID, s.BookID); err == nil && pos != nil {
		stored = pos.PositionSeconds
	}

	// Pick ONE authoritative duration and use it consistently (§5b / requirement 18):
	// the session's sum-of-tracks. A client-reported duration is only a fallback for a
	// book we have no file durations for at all — mixing the two is what leaves a
	// finished book stuck at 99%.
	duration := s.DurationSec
	if duration <= 0 && clientDuration != nil {
		duration = *clientDuration
	}

	now := h.now()
	serverProgress := progress.Progress{
		CurrentTime: stored,
		Duration:    duration,
		UpdatedAtMs: now.Add(-time.Second).UnixMilli(),
	}
	incoming := progress.Progress{
		CurrentTime: position,
		Duration:    duration,
		UpdatedAtMs: now.UnixMilli(),
	}
	merged, accepted := progress.MergeIncoming(serverProgress, incoming)
	if !accepted {
		return
	}

	if err := h.progress.SetUserPosition(s.UserID, s.BookID, absProgressSegmentID, merged.CurrentTime); err != nil {
		return
	}

	status := database.UserBookStatusInProgress
	if merged.IsFinished {
		status = database.UserBookStatusFinished
	}
	pct := 0
	if duration > 0 {
		pct = int(merged.CurrentTime / duration * 100)
		if pct > 100 {
			pct = 100
		}
	}
	_, timeListening, _ := s.snapshot()
	// READ-MODIFY-WRITE, not a fresh literal. A fresh literal silently resets every
	// field this path does not set, and two of them are USER INTENT rather than
	// derived state: HideFromContinueListening (the user removed this book from
	// Continue Listening) and StatusManual (the user pinned a read status by hand).
	// A sync fires roughly every 20 s of listening, so constructing a literal here
	// un-hides and un-pins within seconds of the user's choice.
	_ = h.updateUserBookState(s.UserID, s.BookID, func(state *database.UserBookState) {
		state.Status = status
		state.LastActivityAt = now
		state.LastSegmentID = absProgressSegmentID
		state.TotalListenedSeconds = timeListening
		state.ProgressPct = pct
	})
}

// updateUserBookState read-modify-writes the (user, book) state row.
//
// Every ABS write path goes through here rather than through SetUserBookState
// directly, so a caller that only means to move the playhead cannot drop a field it
// never thought about. mutate receives either the stored row or a zero-valued one
// with the keys already set, so it never has to branch on existence.
func (h *Handler) updateUserBookState(userID, bookID string, mutate func(*database.UserBookState)) error {
	if h.progress == nil {
		return nil
	}
	state, err := h.progress.GetUserBookState(userID, bookID)
	if err != nil || state == nil {
		// A read error is treated as "no row yet" rather than propagated: losing the
		// previous StatusManual/hide flag is bad, but refusing to record the user's
		// new position is worse, and this path has already promised the client 200.
		state = &database.UserBookState{UserID: userID, BookID: bookID}
	}
	mutate(state)
	state.UserID, state.BookID = userID, bookID
	return h.progress.SetUserBookState(state)
}

// respondPlainOK answers exactly what real ABS answers on /sync and /close: HTTP 200
// with the bare text "OK".
//
// Matched to the oracle rather than "improved" into JSON. The body is non-empty (an
// empty 200 is fatal for a typed endpoint, §1.8.6) and neither client decodes it, so
// deviating from the captured fixture would buy nothing and risk something.
func respondPlainOK(c *gin.Context) {
	c.String(http.StatusOK, "OK")
}
