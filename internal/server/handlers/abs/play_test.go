// file: internal/server/handlers/abs/play_test.go
// version: 1.2.0
// guid: 3e5c9b17-84d0-4f26-a1b9-70c8de4531f5
// last-edited: 2026-08-12

package abs_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// playBody is the shape both target clients POST to /api/items/:id/play.
func playBody() map[string]any {
	return map[string]any{
		"deviceInfo":      map[string]any{"clientName": "conformance-capture", "deviceId": "capture-001"},
		"forceDirectPlay": true,
		"mediaPlayer":     "unknown",
	}
}

// startSession opens a play session and returns the decoded body.
func startSession(t *testing.T, h *harness, syncID, tok string) map[string]any {
	t.Helper()
	w, body := h.do(t, request{
		method: http.MethodPost, path: "/api/items/" + syncID + "/play",
		body: playBody(), headers: bearer(tok),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("play: got %d want 200: %s", w.Code, w.Body.String())
	}
	return body
}

// ── POST /api/items/:id/play ────────────────────────────────────────────────

func TestPlay_ConformsToOracle(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	// The oracle captured the session at currentTime 42 with a stored progress row.
	if err := seed.lib.SetUserPosition("u1", seed.multiID, "abs", 42); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	body := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)
	assertConformantExcept(t, "post_api_items_id_play.json", body,
		mergeAllowances(t, bookBodyAllowances(), map[string]allowance{
			// The play body carries the track list under audioTracks and the book's
			// total under a bare `duration`, so neither is reached by the shared keys.
			"duration":               {Reason: durationReason + " (summed over six tracks)", Within: 3.0},
			"audioTracks[].duration": {Reason: durationReason, Within: 0.5},
			"audioTracks[].title":    {Reason: trackTitleReason},
			"deviceInfo.deviceType": {Reason: "the oracle was captured from a wearable; we " +
				"do not derive deviceType from the User-Agent at all and report unknown. " +
				"Unlike ipAddress/userAgent this is a real gap, not a caller artifact"},
		}))
}

// TestPlay_CurrentTimeIsTrueLatestPosition pins verified requirement 16.
// AudioBooth takes max() on position at session start while IGNORING timestamps
// (SessionManager.swift:175-180), so a 0 or a session-start snapshot here silently
// rewinds the user.
func TestPlay_CurrentTimeIsTrueLatestPosition(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	if err := seed.lib.SetUserPosition("u1", seed.multiID, "abs", 4242.5); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	body := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)
	if got := body["currentTime"]; got != 4242.5 {
		t.Fatalf("currentTime = %#v, want the user's true latest position 4242.5", got)
	}
	if got := body["startTime"]; got != 4242.5 {
		t.Fatalf("startTime = %#v, want 4242.5", got)
	}
}

// TestPlay_AudioTracksOmittedNeverEmptyArray pins verified requirement 4 — the
// nastiest shape bug in the protocol. SessionManager.swift:193-194 falls back to
// local tracks via `?? updatedItem.orderedTracks`, which only fires on NIL, so an
// explicit [] DEFEATS the fallback and kills playback of a downloaded book.
func TestPlay_AudioTracksOmittedNeverEmptyArray(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	seed.lib.mu.Lock()
	seed.lib.files[seed.multiID] = nil // a book whose files are all missing
	seed.lib.mu.Unlock()

	w, body := h.do(t, request{
		method: http.MethodPost, path: "/api/items/" + mustSyncID(t, seed, seed.multiID) + "/play",
		body: playBody(), headers: bearer(tok),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("a trackless book must still open a session (local-playback fallback), got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `"audioTracks":[]`) {
		t.Fatal(`emitted "audioTracks":[] — must OMIT the key instead (§1.8.5 item 3)`)
	}
	if v, present := body["audioTracks"]; present {
		t.Fatalf("audioTracks must be absent, not %#v", v)
	}
}

// TestPlay_AudioTracksShape pins verified requirement 3: index/startOffset/duration
// are all non-optional in AudioBooth's Codable, and startOffset is CUMULATIVE float
// seconds — real ABS emits 0, 1386.057143, 2788.702041, 4309.211429. No rounding.
func TestPlay_AudioTracksShape(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	body := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)

	tracks, ok := body["audioTracks"].([]any)
	if !ok || len(tracks) != 6 {
		t.Fatalf("want 6 audioTracks, got %#v", body["audioTracks"])
	}
	var running float64
	for i, raw := range tracks {
		tr := raw.(map[string]any)
		for _, key := range []string{"index", "startOffset", "duration", "contentUrl", "mimeType", "ino"} {
			if tr[key] == nil {
				t.Fatalf("audioTracks[%d].%s must be non-null", i, key)
			}
		}
		if got := tr["index"].(float64); int(got) != i+1 {
			t.Fatalf("audioTracks[%d].index = %v, want %d (ABS indexes tracks from 1)", i, got, i+1)
		}
		if got := tr["startOffset"].(float64); got != running {
			t.Fatalf("audioTracks[%d].startOffset = %v, want cumulative %v", i, got, running)
		}
		running += tr["duration"].(float64)
	}
	// Requirement 18: ONE authoritative duration. The session duration must equal
	// the sum of the track durations exactly, which is what makes startOffset land
	// where the client seeks.
	if got := body["duration"].(float64); got != running {
		t.Fatalf("session duration %v != sum of track durations %v — mixing duration sources leaves finished books stuck at 99%%", got, running)
	}
}

// TestPlay_ContentURLShape pins verified requirement 11 / §1.7.3 item 4. Absorb
// enforces `segment + 5 != segments.length` and a mismatch throws "belongs to a
// different library item", FAILING THE ENTIRE DOWNLOAD.
func TestPlay_ContentURLShape(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	body := startSession(t, h, syncID, tok)

	for i, raw := range body["audioTracks"].([]any) {
		tr := raw.(map[string]any)
		url := tr["contentUrl"].(string)
		ino := tr["ino"].(string)
		want := fmt.Sprintf("/api/items/%s/file/%s", syncID, ino)
		if url != want {
			t.Fatalf("audioTracks[%d].contentUrl = %q, want %q ({ino} MUST be the final segment)", i, url, want)
		}
		if parts := strings.Split(url, "/"); len(parts) != 6 || parts[len(parts)-1] != ino {
			t.Fatalf("contentUrl %q has the wrong segment count/order", url)
		}
	}
}

// TestPlay_EmbeddedLibraryItemAndChapters pins §1.8.5 item 6: PlaySession requires
// id, userId, libraryItemId, currentTime, duration AND a complete embedded
// libraryItem — plus chapters at the session level (not per track).
func TestPlay_EmbeddedLibraryItemAndChapters(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	body := startSession(t, h, syncID, tok)

	for _, key := range []string{"id", "userId", "libraryItemId", "currentTime", "duration", "libraryItem", "chapters"} {
		if body[key] == nil {
			t.Fatalf("play session is missing required field %q", key)
		}
	}
	item := body["libraryItem"].(map[string]any)
	if item["id"] != syncID {
		t.Fatalf("embedded libraryItem.id = %#v, want %q", item["id"], syncID)
	}
	if _, ok := item["media"].(map[string]any)["tracks"]; !ok {
		t.Fatal("embedded libraryItem must be the EXPANDED shape (media.tracks present)")
	}
	if n := len(body["chapters"].([]any)); n != 6 {
		t.Fatalf("want 6 session chapters, got %d", n)
	}
}

// TestPlay_HLSRequestDegradesToDirectPlay pins the "no transcoding, no HLS" rule:
// a client asking for HLS must get a working direct-play session, never an error.
func TestPlay_HLSRequestDegradesToDirectPlay(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	w, body := h.do(t, request{
		method: http.MethodPost, path: "/api/items/" + mustSyncID(t, seed, seed.multiID) + "/play",
		body: map[string]any{
			"deviceInfo":         map[string]any{"clientName": "hls-probe", "deviceId": "d1"},
			"forceDirectPlay":    false,
			"forceTranscode":     true,
			"supportedMimeTypes": []string{"application/vnd.apple.mpegurl"},
			"mediaPlayer":        "html5",
		},
		headers: bearer(tok),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("an HLS request must degrade to direct play, got %d", w.Code)
	}
	if got := body["playMethod"]; got != float64(0) {
		t.Fatalf("playMethod must be 0 (direct play), got %#v", got)
	}
	if _, hls := body["hlsPlaylistUrl"]; hls {
		t.Fatal("we never serve HLS; the key must be absent so the client uses audioTracks")
	}
}

// TestPlay_UnknownItemIs404 keeps a wrong 404 from silently disabling playback and
// a wrong 200 from feeding the decoder a bogus session.
func TestPlay_UnknownItemIs404(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/api/items/00000000-0000-4000-8000-000000000000/play",
		body: playBody(), headers: bearer(tok),
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestPlay_RequiresAuth(t *testing.T) {
	h, seed, _ := newBrowseHarness(t)
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/api/items/" + mustSyncID(t, seed, seed.multiID) + "/play",
		body: playBody(),
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

// ── GET /api/items/:id/file/:ino ────────────────────────────────────────────

func TestItemFile_ServesBytesWithRange(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	body := startSession(t, h, syncID, tok)
	first := body["audioTracks"].([]any)[0].(map[string]any)
	url := first["contentUrl"].(string)

	full, _ := h.do(t, request{method: http.MethodGet, path: url, headers: bearer(tok)})
	if full.Code != http.StatusOK {
		t.Fatalf("full GET: got %d want 200", full.Code)
	}
	if full.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatal("must advertise Accept-Ranges: bytes")
	}
	size := full.Body.Len()

	partial, _ := h.do(t, request{
		method: http.MethodGet, path: url, headers: map[string]string{
			"Authorization": "Bearer " + tok, "Range": "bytes=0-9",
		},
	})
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("Range GET: got %d want 206", partial.Code)
	}
	if partial.Body.Len() != 10 {
		t.Fatalf("Range GET returned %d bytes, want 10", partial.Body.Len())
	}

	// §1.6 item 9: iOS AVPlayer issues TAIL ranges to locate `moov` when it sits
	// after `mdat`. Prefix-only Range support silently breaks m4b playback.
	suffix, _ := h.do(t, request{
		method: http.MethodGet, path: url, headers: map[string]string{
			"Authorization": "Bearer " + tok, "Range": "bytes=-16",
		},
	})
	if suffix.Code != http.StatusPartialContent {
		t.Fatalf("suffix Range: got %d want 206", suffix.Code)
	}
	if got, want := suffix.Body.Bytes(), full.Body.Bytes()[size-16:]; string(got) != string(want) {
		t.Fatalf("suffix Range returned the wrong bytes")
	}
}

// TestItemFile_DownloadVariant pins §1.8.3: AudioBooth downloads from
// /api/items/{id}/file/{ino}/download with the Authorization header.
func TestItemFile_DownloadVariant(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	body := startSession(t, h, syncID, tok)
	url := body["audioTracks"].([]any)[0].(map[string]any)["contentUrl"].(string)

	w, _ := h.do(t, request{method: http.MethodGet, path: url + "/download", headers: bearer(tok)})
	if w.Code != http.StatusOK {
		t.Fatalf("download variant: got %d want 200", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("download variant must set Content-Disposition: attachment, got %q", cd)
	}
}

// TestItemFile_AcceptsTokenQueryParam pins §1.7.2: file URLs must accept ?token=
// (api_service.dart:1397; AudioBooth's watch variant uses it too).
func TestItemFile_AcceptsTokenQueryParam(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	url := startSession(t, h, syncID, tok)["audioTracks"].([]any)[0].(map[string]any)["contentUrl"].(string)

	w, _ := h.do(t, request{method: http.MethodGet, path: url + "?token=" + tok})
	if w.Code != http.StatusOK {
		t.Fatalf("?token= auth on a file URL: got %d want 200", w.Code)
	}
}

func TestItemFile_RequiresAuthAndRejectsForeignIno(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	multi := mustSyncID(t, seed, seed.multiID)
	single := mustSyncID(t, seed, seed.singleID)
	multiURL := startSession(t, h, multi, tok)["audioTracks"].([]any)[0].(map[string]any)["contentUrl"].(string)
	ino := multiURL[strings.LastIndex(multiURL, "/")+1:]

	if w, _ := h.do(t, request{method: http.MethodGet, path: multiURL}); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated file GET: got %d want 401", w.Code)
	}
	// An ino belonging to a DIFFERENT item must not resolve — otherwise the file
	// namespace is global and item scoping is decorative.
	w, _ := h.do(t, request{
		method:  http.MethodGet,
		path:    "/api/items/" + single + "/file/" + ino,
		headers: bearer(tok),
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-item ino: got %d want 404", w.Code)
	}
}

// ── GET /public/session/:id/track/:index ────────────────────────────────────

// TestPublicSessionTrack_UnauthenticatedAndRanged pins §1.8.3: AudioBooth has NO
// contentUrl field at all and streams exclusively from this path. It must be
// public and Range-capable.
func TestPublicSessionTrack_UnauthenticatedAndRanged(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	body := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)
	sessionID := body["id"].(string)
	idx := int(body["audioTracks"].([]any)[0].(map[string]any)["index"].(float64))

	path := fmt.Sprintf("/public/session/%s/track/%d", sessionID, idx)
	w, _ := h.do(t, request{method: http.MethodGet, path: path})
	if w.Code != http.StatusOK {
		t.Fatalf("public track stream must need NO credentials, got %d", w.Code)
	}
	if w.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatal("public track stream must advertise Accept-Ranges: bytes")
	}

	partial, _ := h.do(t, request{
		method: http.MethodGet, path: path,
		headers: map[string]string{"Range": "bytes=0-31"},
	})
	if partial.Code != http.StatusPartialContent || partial.Body.Len() != 32 {
		t.Fatalf("public track Range: got %d / %d bytes, want 206 / 32", partial.Code, partial.Body.Len())
	}
}

func TestPublicSessionTrack_UnknownSessionOrIndexIs404(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	sessionID := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)["id"].(string)
	for _, path := range []string{
		"/public/session/00000000-0000-4000-8000-000000000000/track/1",
		"/public/session/" + sessionID + "/track/99",
		"/public/session/" + sessionID + "/track/notanumber",
	} {
		w, _ := h.do(t, request{method: http.MethodGet, path: path})
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d want 404", path, w.Code)
		}
	}
}

// ── POST /api/session/:id/sync and /close ───────────────────────────────────

// TestSessionSync_ConformsToOracle — real ABS answers a bare "OK".
func TestSessionSync_ConformsToOracle(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	sessionID := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)["id"].(string)

	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/api/session/" + sessionID + "/sync",
		body:    map[string]any{"currentTime": 12.5, "duration": 9975.48, "timeListened": 10},
		headers: bearer(tok),
	})
	assertNonJSONConformant(t, "post_api_session_id_sync.json", w.Code, w.Body.String())
}

func TestSessionClose_ConformsToOracle(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	sessionID := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)["id"].(string)

	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/api/session/" + sessionID + "/close",
		headers: bearer(tok),
	})
	assertNonJSONConformant(t, "post_api_session_id_close.json", w.Code, w.Body.String())
}

// TestSessionSync_TimeListenedIsADeltaTimeListeningIsCumulative pins §1.8.4, the
// name trap that silently zeroes all listening time. `timeListened` (past tense,
// on /sync) is a DELTA the server ADDS; `timeListening` (gerund, on offline
// replay) is a CUMULATIVE total. abs-shim reads the wrong key and records zero.
func TestSessionSync_TimeListenedIsADeltaTimeListeningIsCumulative(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	sessionID := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)["id"].(string)
	sync := func(body map[string]any) {
		t.Helper()
		w, _ := h.do(t, request{
			method: http.MethodPost, path: "/api/session/" + sessionID + "/sync",
			body: body, headers: bearer(tok),
		})
		if w.Code != http.StatusOK {
			t.Fatalf("sync: got %d want 200", w.Code)
		}
	}

	sync(map[string]any{"currentTime": 10, "timeListened": 10})
	sync(map[string]any{"currentTime": 20, "timeListened": 10})
	if got := h.handler.SessionTimeListening(sessionID); got != 20 {
		t.Fatalf("two timeListened:10 deltas must total 20, got %v", got)
	}

	// The cumulative key SETS rather than adds, so replaying it is idempotent.
	sync(map[string]any{"currentTime": 30, "timeListening": 25})
	sync(map[string]any{"currentTime": 30, "timeListening": 25})
	if got := h.handler.SessionTimeListening(sessionID); got != 25 {
		t.Fatalf("timeListening is cumulative and idempotent; want 25, got %v", got)
	}
}

// TestSessionSync_PersistsForwardProgress checks the §5 rule that matters most for
// the mission: a sync advances the stored position so the next session resumes
// there instead of at 0.
func TestSessionSync_PersistsForwardProgress(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	sessionID := startSession(t, h, syncID, tok)["id"].(string)

	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/api/session/" + sessionID + "/sync",
		body:    map[string]any{"currentTime": 1234.5, "timeListened": 20},
		headers: bearer(tok),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("sync: got %d want 200", w.Code)
	}
	pos, err := seed.lib.GetUserPosition("u1", seed.multiID)
	if err != nil || pos == nil {
		t.Fatalf("sync must persist a resume position, got %v / %v", pos, err)
	}
	if pos.PositionSeconds != 1234.5 {
		t.Fatalf("stored position = %v, want 1234.5", pos.PositionSeconds)
	}
	// Requirement 17: always send duration alongside isFinished. A later play
	// session must resume at the persisted position, never 0.
	if got := startSession(t, h, syncID, tok)["currentTime"]; got != 1234.5 {
		t.Fatalf("a new session must resume at 1234.5, got %#v", got)
	}
}

// TestSessionSync_UnknownSessionIsIdempotent200 pins §1.8.8 item 8: AudioBooth
// cannot detect a 404-expired session (it rewraps errors and loses the status
// code), so it will never re-create one. A 404 here strands the client forever.
func TestSessionSync_UnknownSessionIsIdempotent200(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	for _, suffix := range []string{"/sync", "/close"} {
		w, _ := h.do(t, request{
			method: http.MethodPost, path: "/api/session/00000000-0000-4000-8000-000000000000" + suffix,
			body:    map[string]any{"currentTime": 5, "timeListened": 5},
			headers: bearer(tok),
		})
		if w.Code != http.StatusOK {
			t.Fatalf("%s on an unknown session: got %d want an idempotent 200", suffix, w.Code)
		}
		if w.Body.Len() == 0 {
			t.Fatalf("%s: an empty 200 body is fatal to these decoders (§1.8.6)", suffix)
		}
	}
}

// TestSessionSync_OtherUsersSessionIsRejected — session ids are the only thing
// guarding the public track path, so ownership must be enforced on the write path.
func TestSessionSync_OtherUsersSessionIsRejected(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	sessionID := startSession(t, h, mustSyncID(t, seed, seed.multiID), tok)["id"].(string)

	h.seedUser(t, "u2", "intruder", "", "pw-pw-pw-pw")
	other := str(t, userObj(t, h.login(t, "intruder", "pw-pw-pw-pw")), "accessToken")

	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/api/session/" + sessionID + "/sync",
		body:    map[string]any{"currentTime": 9999, "timeListened": 10},
		headers: bearer(other),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("must not leak session existence; got %d want an idempotent 200", w.Code)
	}
	pos, _ := seed.lib.GetUserPosition("u1", seed.multiID)
	if pos != nil && pos.PositionSeconds == 9999 {
		t.Fatal("another user's sync wrote our progress")
	}
}

// assertNonJSONConformant compares a plain-text response against a fixture whose
// body the capture script recorded as {"__non_json_body__": "..."} because real ABS
// answered text/plain rather than JSON.
func assertNonJSONConformant(t *testing.T, fixture string, code int, body string) {
	t.Helper()
	f := mustLoadFixture(t, fixture)
	if code != f.Response.Status {
		t.Fatalf("%s: got status %d want %d", fixture, code, f.Response.Status)
	}
	obj, ok := f.Response.Body.(map[string]any)
	if !ok {
		t.Fatalf("%s: expected a recorded non-JSON body wrapper, got %#v", fixture, f.Response.Body)
	}
	want, ok := obj["__non_json_body__"].(string)
	if !ok {
		t.Fatalf("%s: fixture is JSON after all; use assertConformant", fixture)
	}
	if strings.TrimSpace(body) != strings.TrimSpace(want) {
		t.Fatalf("%s: body = %q, want %q (real ABS answers a bare %q here)", fixture, body, want, want)
	}
}
