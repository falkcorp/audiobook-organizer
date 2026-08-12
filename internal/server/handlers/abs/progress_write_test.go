// file: internal/server/handlers/abs/progress_write_test.go
// version: 1.2.0
// guid: 2b7f4e91-8a05-4c63-b1d8-70e396a5cf42
// last-edited: 2026-08-12

package abs_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// Phase 6 write half. These tests run against the REAL UserDataProvider wired over
// the in-memory library, not against fakeUserData, because the property under test is
// that a write is visible to the very next read in the exact shape the client
// compares against. A fake provider would pass while the two disagreed.

// ── fake capabilities the real provider needs ───────────────────────────────

// ListUserPositionsSince backs MediaProgress's single user-keyed scan.
func (f *fakeLibrary) ListUserPositionsSince(userID string, since time.Time) ([]database.UserPosition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []database.UserPosition{}
	for _, pos := range f.positions {
		if pos == nil || pos.UserID != userID || !pos.UpdatedAt.After(since) {
			continue
		}
		out = append(out, *pos)
	}
	return out, nil
}

// GetSyncIDForBook is the read half of the sync-identity slice.
func (f *fakeLibrary) GetSyncIDForBook(bookID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.syncIDs[bookID]
	return id, ok, nil
}

// fakeBookmarks is an in-memory BookmarkStore keyed exactly like the Pebble one:
// (user, item, canonical time). Keying by the canonical time rather than the raw
// float is the whole point — "100" and "100.0" must address one row.
type fakeBookmarks struct {
	rows map[string]progress.Bookmark
	err  error
}

func newFakeBookmarks() *fakeBookmarks {
	return &fakeBookmarks{rows: map[string]progress.Bookmark{}}
}

func (f *fakeBookmarks) key(userID, itemID string, t float64) string {
	return userID + "|" + itemID + "|" + progress.CanonicalTimeKey(t)
}

func (f *fakeBookmarks) CreateBookmark(b progress.Bookmark) error {
	if f.err != nil {
		return f.err
	}
	k := f.key(b.UserID, b.ItemID, b.TimeSec)
	// Upsert preserving CreatedAt, matching pebble_store_bookmarks.go.
	if prev, ok := f.rows[k]; ok {
		b.CreatedAt = prev.CreatedAt
	} else {
		b.CreatedAt = time.Now().UnixMilli()
	}
	b.UpdatedAt = time.Now().UnixMilli()
	f.rows[k] = b
	return nil
}

func (f *fakeBookmarks) ListBookmarks(userID, itemID string) ([]progress.Bookmark, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []progress.Bookmark{}
	for _, b := range f.rows {
		if b.UserID == userID && b.ItemID == itemID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBookmarks) ListBookmarksForUser(userID string) ([]progress.Bookmark, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []progress.Bookmark{}
	for _, b := range f.rows {
		if b.UserID == userID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBookmarks) UpdateBookmarkTitle(userID, itemID string, timeSec float64, title string) error {
	if f.err != nil {
		return f.err
	}
	k := f.key(userID, itemID, timeSec)
	b, ok := f.rows[k]
	if !ok {
		return errNoSuchBookmark
	}
	b.Title = title
	f.rows[k] = b
	return nil
}

func (f *fakeBookmarks) DeleteBookmark(userID, itemID string, timeSec float64) error {
	if f.err != nil {
		return f.err
	}
	delete(f.rows, f.key(userID, itemID, timeSec))
	return nil
}

var errNoSuchBookmark = errString("no such bookmark")

type errString string

func (e errString) Error() string { return string(e) }

// ── harness ─────────────────────────────────────────────────────────────────

type writeHarness struct {
	*harness
	seed      *oracleSeed
	bookmarks *fakeBookmarks
	token     string
	userID    string
	syncID    string // the single-file book
	bookID    string
}

// newWriteHarness wires the REAL provider over the seeded library.
func newWriteHarness(t *testing.T) *writeHarness {
	t.Helper()
	seed := seedOracleLibrary(t)
	bm := newFakeBookmarks()
	provider, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress: seed.lib, Bookmarks: bm, Identity: seed.lib, Library: seed.lib,
	})
	if err != nil {
		t.Fatalf("NewUserData: %v", err)
	}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(provider),
		func(o *abshandler.Options) { o.Bookmarks = bm })
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	syncID, err := seed.lib.MintOrGetSyncID(seed.singleID)
	if err != nil {
		t.Fatalf("MintOrGetSyncID: %v", err)
	}
	return &writeHarness{
		harness: h, seed: seed, bookmarks: bm, token: tok,
		userID: "u1", syncID: syncID, bookID: seed.singleID,
	}
}

func (w *writeHarness) req(t *testing.T, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	rec, decoded := w.do(t, request{method: method, path: path, body: body, headers: bearer(w.token)})
	return rec.Code, decoded, rec.Body.String()
}

// rowID is the mediaProgress row id our read half emits: "<userID>-<syncID>".
func (w *writeHarness) rowID() string { return w.userID + "-" + w.syncID }

func (w *writeHarness) patch(t *testing.T, body any) {
	t.Helper()
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, body)
	if code != http.StatusOK {
		t.Fatalf("PATCH progress = %d %s", code, raw)
	}
}

func (w *writeHarness) getRow(t *testing.T) map[string]any {
	t.Helper()
	code, body, raw := w.req(t, http.MethodGet, "/api/me/progress/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET progress = %d %s", code, raw)
	}
	return body
}

func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s must be a number, got %#v", key, m[key])
	}
	return v
}

// ── GET /api/me/progress — §1.8.1 completeness ──────────────────────────────

// TestMediaProgressList_ReturnsEveryStoredRow pins §1.8.1 on the endpoint that is
// easiest to overlook: the body is a bare wrapper, not a user object, but AudioBooth
// DELETES every local progress row absent from it just the same.
func TestMediaProgressList_ReturnsEveryStoredRow(t *testing.T) {
	w := newWriteHarness(t)
	if _, err := w.seed.lib.MintOrGetSyncID(w.seed.multiID); err != nil {
		t.Fatalf("mint: %v", err)
	}
	for _, id := range []string{w.seed.singleID, w.seed.multiID} {
		if err := w.seed.lib.SetUserPosition("u1", id, "abs", 120); err != nil {
			t.Fatalf("SetUserPosition: %v", err)
		}
	}

	code, body, raw := w.req(t, http.MethodGet, "/api/me/progress", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	rows, ok := body["mediaProgress"].([]any)
	if !ok {
		t.Fatalf("mediaProgress must be an array, got %#v", body["mediaProgress"])
	}
	if len(rows) != 2 {
		t.Fatalf("mediaProgress has %d rows, want 2 — a short list makes the client DELETE the missing books", len(rows))
	}
}

// TestMediaProgressList_ProviderErrorIs5xxNotEmptyList is the data-loss guard: a
// read failure must never be reported as "you have no progress", because the client
// acts on that by deleting its own copy.
func TestMediaProgressList_ProviderErrorIs5xxNotEmptyList(t *testing.T) {
	seed := seedOracleLibrary(t)
	// The provider must be healthy for /login (which carries the same payload) and
	// is broken only afterwards, so the failure under test is the read, not the auth.
	broken := &fakeUserData{}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(broken))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	broken.err = errString("store down")

	rec, body := h.do(t, request{method: http.MethodGet, path: "/api/me/progress", headers: bearer(tok)})
	if rec.Code < 500 {
		t.Fatalf("got %d, want 5xx — a 200 with an empty list destroys the user's positions", rec.Code)
	}
	if rows, present := body["mediaProgress"]; present {
		t.Fatalf("error response must not carry a mediaProgress array, got %#v", rows)
	}
}

// ── GET /api/me/progress/:id ────────────────────────────────────────────────

// TestMediaProgressGet_AcceptsBothIDForms is the oracle finding of 2026-08-02:
// real ABS keys this route by the mediaProgress ROW id, while a client that came
// from a browse response holds the libraryItemId. Both must resolve, or
// reset-progress silently does nothing for whichever form the client happens to use.
func TestMediaProgressGet_AcceptsBothIDForms(t *testing.T) {
	w := newWriteHarness(t)
	if err := w.seed.lib.SetUserPosition("u1", w.bookID, "abs", 42); err != nil {
		t.Fatalf("SetUserPosition: %v", err)
	}

	for name, id := range map[string]string{
		"libraryItemId":   w.syncID,
		"mediaProgressID": w.rowID(),
	} {
		t.Run(name, func(t *testing.T) {
			code, body, raw := w.req(t, http.MethodGet, "/api/me/progress/"+id, nil)
			if code != http.StatusOK {
				t.Fatalf("got %d %s", code, raw)
			}
			// A BARE object, not wrapped — matched to the oracle capture.
			if body["libraryItemId"] != w.syncID {
				t.Fatalf("libraryItemId = %#v, want %q", body["libraryItemId"], w.syncID)
			}
			if got := num(t, body, "currentTime"); got != 42 {
				t.Fatalf("currentTime = %v, want 42", got)
			}
		})
	}
}

// TestMediaProgressGet_UnstartedBookIs404 matches real ABS, which 404s a book the
// user has never opened.
func TestMediaProgressGet_UnstartedBookIs404(t *testing.T) {
	w := newWriteHarness(t)
	code, _, _ := w.req(t, http.MethodGet, "/api/me/progress/"+w.syncID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

// TestMediaProgressGet_ProviderErrorIs5xxNot404 keeps "we could not read it" and
// "there is nothing there" distinguishable. A 404 the server is not sure about reads
// to the client as "no progress", and acting on that costs the user their place.
func TestMediaProgressGet_ProviderErrorIs5xxNot404(t *testing.T) {
	seed := seedOracleLibrary(t)
	syncID, err := seed.lib.MintOrGetSyncID(seed.singleID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	broken := &fakeUserData{}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(broken))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	broken.err = errString("store down")

	rec, _ := h.do(t, request{method: http.MethodGet, path: "/api/me/progress/" + syncID, headers: bearer(tok)})
	if rec.Code < 500 {
		t.Fatalf("got %d, want 5xx", rec.Code)
	}
}

// ── PATCH /api/me/progress/:id ──────────────────────────────────────────────

func TestMediaProgressPatch_StoresPositionAndIsReadableBack(t *testing.T) {
	w := newWriteHarness(t)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID,
		map[string]any{"currentTime": 300.0, "duration": 9975.0, "isFinished": false})
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", code, raw)
	}
	// Real ABS answers plain-text "OK" here, not JSON.
	if raw != "OK" {
		t.Fatalf("PATCH body = %q, want %q (oracle shape)", raw, "OK")
	}
	if got := num(t, w.getRow(t), "currentTime"); got != 300 {
		t.Fatalf("currentTime = %v, want 300", got)
	}
}

// TestMediaProgressPatch_HonoursAnExplicitBackwardsSeek pins §5 rule 2, which is
// deliberately NOT forward-only: "a genuinely newer update is honoured even when it
// seeks backwards — only a STALE update is position-gated."
//
// This endpoint is the user dragging the scrubber, and refusing it would snap the
// book forward again under their finger. The multi-device clobber §5 rule 3 guards
// against is a different path — POST /api/session/:id/sync, which merges through
// MergeIncoming and IS forward-only (see TestSessionSync_* and policy_test.go).
//
// The reason this endpoint can trust the write is that clients do not push a stale
// position here: AudioBooth compares lastUpdate with strict > and ADOPTS the server's
// value when the server is newer (§1.8.7), so it only ever PATCHes a position it
// believes is the most recent one.
func TestMediaProgressPatch_HonoursAnExplicitBackwardsSeek(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 5000.0})
	w.patch(t, map[string]any{"currentTime": 10.0})

	if got := num(t, w.getRow(t), "currentTime"); got != 10 {
		t.Fatalf("currentTime = %v, want 10 — an explicit PATCH is a user seek and must apply", got)
	}
}

// TestPlaySession_StartsFromTheServersTrueLatestPosition is where the multi-device
// clobber is ACTUALLY prevented, and it is worth being precise about, because the
// obvious guess is wrong.
//
// Neither PATCH /api/me/progress/:id nor POST /api/session/:id/sync carries a
// client-supplied timestamp, so incoming is always "newer" and §5 rule 3's
// forward-only branch never fires on either. That is deliberate rather than a hole:
// both endpoints report a LIVE, present-tense user action, and refusing a backwards
// position on them would make scrubbing backwards impossible — the book would snap
// forward again under the user's finger.
//
// The stale device is stopped one step earlier instead. A device that was offline
// opens a session first, and §1.8.7 has AudioBooth take max(local, session.currentTime)
// while IGNORING timestamps — so as long as this endpoint reports the server's TRUE
// latest position, the stale device is pulled forward and never has a behind position
// to sync in the first place. That property is what this test pins.
//
// Forward-only merging still guards the one path with genuinely untrustworthy
// timestamps, offline replay, via progress.MergeOfflineReplay (policy_test.go).
func TestPlaySession_StartsFromTheServersTrueLatestPosition(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 5000.0})

	code, play, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("play = %d %s", code, raw)
	}
	if got := num(t, play, "currentTime"); got != 5000 {
		t.Fatalf("session currentTime = %v, want 5000 — a 0 or a stale value here silently "+
			"rewinds the listener to the start of the book (§1.8.7)", got)
	}
	if got := num(t, play, "startTime"); got != 5000 {
		t.Fatalf("session startTime = %v, want 5000", got)
	}
}

// 🔴 TestMediaProgressPatch_HideOnlyDoesNotRewindPosition is the trap a naive
// implementation falls into: `{"hideFromContinueListening": true}` carries no
// currentTime, and a non-pointer decode reads that absence as 0, storing a rewind to
// the start of the book. This is the exact body the remove-from-continue-listening
// path sends, so getting it wrong loses the position of every book the user hides.
func TestMediaProgressPatch_HideOnlyDoesNotRewindPosition(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 4321.0})
	w.patch(t, map[string]any{"hideFromContinueListening": true})

	row := w.getRow(t)
	if got := num(t, row, "currentTime"); got != 4321 {
		t.Fatalf("currentTime = %v, want 4321 — a hide-only PATCH must not touch the position", got)
	}
	if row["hideFromContinueListening"] != true {
		t.Fatalf("hideFromContinueListening = %#v, want true", row["hideFromContinueListening"])
	}
}

// TestMediaProgressPatch_ReopeningFinishedBookResetsPosition covers §5 rule 4 /
// MergeExplicit: ABS allows re-opening a finished book, and doing so starts it over.
func TestMediaProgressPatch_ReopeningFinishedBookResetsPosition(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 9975.0, "isFinished": true})
	if row := w.getRow(t); row["isFinished"] != true {
		t.Fatalf("isFinished = %#v, want true", row["isFinished"])
	}

	w.patch(t, map[string]any{"isFinished": false})
	row := w.getRow(t)
	if row["isFinished"] != false {
		t.Fatalf("isFinished = %#v, want false after re-open", row["isFinished"])
	}
	if got := num(t, row, "currentTime"); got != 0 {
		t.Fatalf("currentTime = %v, want 0 after re-opening a finished book", got)
	}
}

// TestMediaProgressPatch_FinishedAlwaysCarriesDuration pins §1.8.7's null-duration
// trap: isFinished:true with a zero duration sets the CLIENT's currentTime to 0.
func TestMediaProgressPatch_FinishedAlwaysCarriesDuration(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 9975.0, "isFinished": true})

	row := w.getRow(t)
	if row["isFinished"] == true && num(t, row, "duration") <= 0 {
		t.Fatal("isFinished:true with a zero duration ZEROES the client's saved position")
	}
}

// ── PATCH /api/me/progress/batch/update ─────────────────────────────────────

// TestMediaProgressBatchUpdate_AppliesEveryElement also proves the route itself
// resolves: gin has to route the static "batch" segment alongside the ":id" wildcard
// at the same depth, and a mis-dispatch would land this body in the single-item
// handler with id="batch".
func TestMediaProgressBatchUpdate_AppliesEveryElement(t *testing.T) {
	w := newWriteHarness(t)
	multiSync, err := w.seed.lib.MintOrGetSyncID(w.seed.multiID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/batch/update", []map[string]any{
		{"libraryItemId": w.syncID, "currentTime": 111.0},
		{"libraryItemId": multiSync, "currentTime": 222.0},
		{"libraryItemId": "00000000-0000-4000-8000-000000000000", "currentTime": 333.0},
	})
	if code != http.StatusOK {
		t.Fatalf("batch = %d %s", code, raw)
	}
	if got := num(t, w.getRow(t), "currentTime"); got != 111 {
		t.Fatalf("single-file currentTime = %v, want 111", got)
	}
	code, body, _ := w.req(t, http.MethodGet, "/api/me/progress/"+multiSync, nil)
	if code != http.StatusOK {
		t.Fatalf("multi GET = %d", code)
	}
	if got := num(t, body, "currentTime"); got != 222 {
		t.Fatalf("multi-file currentTime = %v, want 222", got)
	}
}

// ── DELETE /api/me/progress/:id ─────────────────────────────────────────────

func TestMediaProgressDelete_ResetsProgress(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 4321.0})

	// Addressed by the ROW id, which is what a client that read /api/me hands back.
	code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", code, raw)
	}
	if raw != "OK" {
		t.Fatalf("DELETE body = %q, want %q", raw, "OK")
	}
	if code, _, _ := w.req(t, http.MethodGet, "/api/me/progress/"+w.syncID, nil); code != http.StatusNotFound {
		t.Fatalf("GET after reset = %d, want 404", code)
	}
}

func TestMediaProgressDelete_NothingToResetIs404(t *testing.T) {
	w := newWriteHarness(t)
	if code, _, _ := w.req(t, http.MethodDelete, "/api/me/progress/"+w.syncID, nil); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

// ── remove-from-continue-listening ──────────────────────────────────────────

// TestRemoveFromContinueListening_HidesAndAnswersNonEmptyBody covers both halves of
// the reported bug: the route existing at all, and §1.8.6/spec:318's requirement
// that its body be NON-EMPTY (an empty 200 is fatal to these decoders).
func TestRemoveFromContinueListening_HidesAndAnswersNonEmptyBody(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})

	code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/remove-from-continue-listening", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	if raw == "" {
		t.Fatal("empty 200 body — fatal for these decoders (§1.8.6)")
	}
	row := w.getRow(t)
	if row["hideFromContinueListening"] != true {
		t.Fatalf("hideFromContinueListening = %#v, want true", row["hideFromContinueListening"])
	}
	if got := num(t, row, "currentTime"); got != 600 {
		t.Fatalf("currentTime = %v, want 600 — hiding must not discard the position", got)
	}
}

// 🔴 TestSessionSync_DoesNotClearHideFlag is the regression test for the clobber the
// new field would otherwise inherit: persistProgress used to construct a FRESH
// UserBookState on every sync, resetting every field it did not set. A sync fires
// roughly every 20 s of listening, so a literal there un-hides a book within seconds
// of the user hiding it.
func TestSessionSync_DoesNotClearHideFlag(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})
	code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/remove-from-continue-listening", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("hide = %d %s", code, raw)
	}

	// Open a real play session and sync it, exactly as a client would.
	code, play, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("play = %d %s", code, raw)
	}
	sessionID, _ := play["id"].(string)
	if sessionID == "" {
		t.Fatalf("play session has no id: %#v", play)
	}
	code, _, raw = w.req(t, http.MethodPost, "/api/session/"+sessionID+"/sync",
		map[string]any{"currentTime": 900.0, "timeListened": 300.0})
	if code != http.StatusOK {
		t.Fatalf("sync = %d %s", code, raw)
	}

	row := w.getRow(t)
	if row["hideFromContinueListening"] != true {
		t.Fatal("a playback sync cleared hideFromContinueListening — the user's choice must survive it")
	}
	if got := num(t, row, "currentTime"); got != 900 {
		t.Fatalf("currentTime = %v, want 900 (the sync must still advance the position)", got)
	}
}

// TestSessionSync_DoesNotClearStatusManual covers the same clobber for the flag that
// was ALREADY being reset before this change: a hand-pinned read status was silently
// un-pinned by the next sync.
func TestSessionSync_DoesNotClearStatusManual(t *testing.T) {
	w := newWriteHarness(t)
	if err := w.seed.lib.SetUserBookState(&database.UserBookState{
		UserID: "u1", BookID: w.bookID, Status: database.UserBookStatusFinished, StatusManual: true,
	}); err != nil {
		t.Fatalf("SetUserBookState: %v", err)
	}

	code, play, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("play = %d %s", code, raw)
	}
	sessionID, _ := play["id"].(string)
	if code, _, raw := w.req(t, http.MethodPost, "/api/session/"+sessionID+"/sync",
		map[string]any{"currentTime": 120.0}); code != http.StatusOK {
		t.Fatalf("sync = %d %s", code, raw)
	}

	state, err := w.seed.lib.GetUserBookState("u1", w.bookID)
	if err != nil || state == nil {
		t.Fatalf("GetUserBookState: %v / %#v", err, state)
	}
	if !state.StatusManual {
		t.Fatal("a playback sync cleared StatusManual — a hand-pinned status must survive it")
	}
}

// ── bookmarks ───────────────────────────────────────────────────────────────

func TestBookmarks_CreateListUpdateDelete(t *testing.T) {
	w := newWriteHarness(t)

	code, created, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 100, "title": "a good bit"})
	if code != http.StatusOK {
		t.Fatalf("create = %d %s", code, raw)
	}
	if created["title"] != "a good bit" || created["libraryItemId"] != w.syncID {
		t.Fatalf("created bookmark = %#v", created)
	}

	code, listed, raw := w.req(t, http.MethodGet, "/api/me/bookmarks/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, raw)
	}
	rows, _ := listed["bookmarks"].([]any)
	if len(rows) != 1 {
		t.Fatalf("bookmarks = %d rows, want 1", len(rows))
	}

	code, updated, raw := w.req(t, http.MethodPatch, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 100, "title": "renamed"})
	if code != http.StatusOK {
		t.Fatalf("update = %d %s", code, raw)
	}
	if updated["title"] != "renamed" {
		t.Fatalf("updated title = %#v, want %q", updated["title"], "renamed")
	}

	code, _, raw = w.req(t, http.MethodDelete, "/api/me/item/"+w.syncID+"/bookmark/100", nil)
	if code != http.StatusOK {
		t.Fatalf("delete = %d %s", code, raw)
	}
	if raw != "OK" {
		t.Fatalf("delete body = %q, want %q", raw, "OK")
	}
	_, listed, _ = w.req(t, http.MethodGet, "/api/me/bookmarks/"+w.syncID, nil)
	if rows, _ := listed["bookmarks"].([]any); len(rows) != 0 {
		t.Fatalf("bookmarks = %d rows after delete, want 0", len(rows))
	}
}

// TestBookmarks_IntAndFloatTimeAddressTheSameRow is the Int-vs-Double requirement
// exercised through the HTTP layer rather than only at the storage layer: AudioBooth
// sends `time` as an Int in the DELETE path segment and round-trips it as a Double in
// bodies. If the two do not collide, a bookmark becomes undeletable.
func TestBookmarks_IntAndFloatTimeAddressTheSameRow(t *testing.T) {
	w := newWriteHarness(t)
	if code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 12.0, "title": "twelve"}); code != http.StatusOK {
		t.Fatalf("create = %d %s", code, raw)
	}
	// Deleted with the INTEGER path form.
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/item/"+w.syncID+"/bookmark/12", nil); code != http.StatusOK {
		t.Fatalf("delete by int path = %d %s — \"12\" must address the bookmark created at 12.0", code, raw)
	}
}

func TestBookmarks_DeleteMissingIs404(t *testing.T) {
	w := newWriteHarness(t)
	if code, _, _ := w.req(t, http.MethodDelete, "/api/me/item/"+w.syncID+"/bookmark/999", nil); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

func TestBookmarks_UpdateMissingIs404(t *testing.T) {
	w := newWriteHarness(t)
	code, _, _ := w.req(t, http.MethodPatch, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 999, "title": "nope"})
	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 — update must not implicitly create a bookmark", code)
	}
}

// TestBookmarks_ListErrorIs5xxNotEmptyList keeps "could not read" distinguishable
// from "there are none", same discipline as the progress list.
func TestBookmarks_ListErrorIs5xxNotEmptyList(t *testing.T) {
	w := newWriteHarness(t)
	w.bookmarks.err = errString("store down")
	code, body, _ := w.req(t, http.MethodGet, "/api/me/bookmarks/"+w.syncID, nil)
	if code < 500 {
		t.Fatalf("got %d, want 5xx", code)
	}
	if _, present := body["bookmarks"]; present {
		t.Fatal("error response must not carry a bookmarks array")
	}
}

// ── conformance against the real ABS 2.36.0 captures ────────────────────────

// These diff our bodies against fixtures captured from the reference server on
// 2026-08-02, field by field. A missing field is the highest-severity finding on
// this surface: a client that hard-requires it fails its whole decode, and for a
// progress body that failure looks to the user like the book losing its place.
//
// Values are checked too as of 2026-08-12 — except where a call site below is still
// on assertConformantPending. Wrong values are not a harmless second tier here: a
// progress body with the right keys and a wrong currentTime does not fail a decode,
// it silently sends the listener to the wrong place in the book.

func TestMediaProgressGet_ConformsToOracle(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 42.0, "duration": 9975.48, "isFinished": false})
	assertConformantExcept(t, "get_api_me_progress_id.json", w.getRow(t), map[string]allowance{
		"duration": {Reason: durationReason, Within: 0.5},
		"progress": {Reason: progressReason},
	})
}

func TestMediaProgressList_ConformsToOracle(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 42.0, "duration": 9975.48, "isFinished": false})
	code, body, raw := w.req(t, http.MethodGet, "/api/me/progress", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	assertConformantExcept(t, "get_api_me_progress.json", body, map[string]allowance{
		"mediaProgress[].duration": {Reason: durationReason, Within: 0.5},
		"mediaProgress[].progress": {Reason: progressReason},
	})
}

func TestBookmarkCreate_ConformsToOracle(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 100, "title": "conformance bookmark"})
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	assertConformant(t, "post_api_me_item_id_bookmark.json", body)
}

func TestBookmarkUpdate_ConformsToOracle(t *testing.T) {
	w := newWriteHarness(t)
	if code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 100, "title": "conformance bookmark"}); code != http.StatusOK {
		t.Fatalf("create = %d %s", code, raw)
	}
	code, body, raw := w.req(t, http.MethodPatch, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 100, "title": "conformance bookmark"})
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	assertConformant(t, "patch_api_me_item_id_bookmark.json", body)
}

func TestBookmarkList_ConformsToOracle(t *testing.T) {
	w := newWriteHarness(t)
	if code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 100, "title": "conformance bookmark"}); code != http.StatusOK {
		t.Fatalf("create = %d %s", code, raw)
	}
	code, body, raw := w.req(t, http.MethodGet, "/api/me/bookmarks/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	assertConformant(t, "get_api_me_bookmarks_id.json", body)
}

// ── cross-cutting ───────────────────────────────────────────────────────────

// TestProgressWrites_RejectUnauthenticated proves the whole write surface sits behind
// the fail-closed identity middleware rather than only the read half.
func TestProgressWrites_RejectUnauthenticated(t *testing.T) {
	w := newWriteHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/progress"},
		{http.MethodGet, "/api/me/progress/" + w.syncID},
		{http.MethodPatch, "/api/me/progress/" + w.syncID},
		{http.MethodPatch, "/api/me/progress/batch/update"},
		{http.MethodDelete, "/api/me/progress/" + w.syncID},
		{http.MethodPost, "/api/me/item/" + w.syncID + "/remove-from-continue-listening"},
		{http.MethodGet, "/api/me/bookmarks/" + w.syncID},
		{http.MethodPost, "/api/me/item/" + w.syncID + "/bookmark"},
		{http.MethodPatch, "/api/me/item/" + w.syncID + "/bookmark"},
		{http.MethodDelete, "/api/me/item/" + w.syncID + "/bookmark/1"},
	} {
		rec, _ := w.do(t, request{method: tc.method, path: tc.path, body: map[string]any{}})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// TestProgressWrites_CannotAddressAnotherUsersRow pins the id parse. The row-id form
// is "<userID>-<syncID>", and the prefix stripped is always the AUTHENTICATED
// caller's own — so constructing another user's row id cannot reach their data.
func TestProgressWrites_CannotAddressAnotherUsersRow(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 555.0})

	// u2's row id for the same book. It must not resolve for u1's credentials.
	code, _, _ := w.req(t, http.MethodGet, "/api/me/progress/u2-"+w.syncID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 for another user's row id", code)
	}
}
