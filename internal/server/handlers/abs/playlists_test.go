// file: internal/server/handlers/abs/playlists_test.go
// version: 1.0.0
// guid: 7e2f4a08-b165-4c39-8de2-91f0a3b74c6e
// last-edited: 2026-08-13

package abs_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// ── fake store ──────────────────────────────────────────────────────────────
//
// Helpers are prefixed abspl* because this package's tests share one namespace.
//
// NOTE ON WHAT THESE TESTS ARE. There is NO fixture for playlists — zero of the
// 28 captures request one — so unlike the rest of this suite these assert OUR
// shape rather than conforming to an oracle. They are therefore written to pin
// behaviour that is decidable without an oracle: ordering, scoping, stale-id
// handling, and the Page<T> envelope contract from §6.6.

type absplFakeStore struct {
	lists []database.UserPlaylist
	err   error
	// gotUserID records what the handler scoped the query to, so the per-user
	// scoping can be asserted directly instead of inferred from the result.
	gotUserID string
	calls     int
}

func (f *absplFakeStore) ListUserPlaylistsForUser(userID, _ string, _, _ int) ([]database.UserPlaylist, int, error) {
	f.gotUserID = userID
	f.calls++
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.lists, len(f.lists), nil
}

func withPlaylists(p abshandler.PlaylistStore) harnessOpt {
	return func(o *abshandler.Options) { o.Playlists = p }
}

// absplHarness builds the standard browse harness plus a playlist store.
func absplHarness(t *testing.T, store abshandler.PlaylistStore) (*harness, *oracleSeed, string) {
	t.Helper()
	seed := seedOracleLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withPlaylists(store))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	body := h.login(t, "oracle", "pw-pw-pw-pw")
	return h, seed, str(t, userObj(t, body), "accessToken")
}

func absplGet(t *testing.T, h *harness, tok string) map[string]any {
	t.Helper()
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/playlists",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want a JSON object", body)
	}
	return m
}

// ── the envelope contract ───────────────────────────────────────────────────

// §6.6: Page<T> needs non-optional total AND page. `{}` red-screens the Dart
// client. Asserted for BOTH the wired and the nil-store paths, because the nil
// path is the one that silently regresses when someone "simplifies" the guard.
func TestLibraryPlaylists_PageEnvelopeAlwaysHasTotalAndPage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store abshandler.PlaylistStore
	}{
		{"nil store", nil},
		{"wired but empty", &absplFakeStore{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, tok := absplHarness(t, tc.store)
			body := absplGet(t, h, tok)
			for _, key := range []string{"total", "page", "results"} {
				if _, ok := body[key]; !ok {
					t.Fatalf("%s: response is missing required key %q — Page<T> throws without it", tc.name, key)
				}
			}
			if _, ok := body["results"].([]any); !ok {
				t.Fatalf("results is %T, want an array", body["results"])
			}
		})
	}
}

// A nil store must behave EXACTLY as the previous h.EmptyPage stub did. Wiring an
// optional capability must never turn "feature absent" into a 500.
func TestLibraryPlaylists_NilStoreServesEmptyPageNotAnError(t *testing.T) {
	h, _, tok := absplHarness(t, nil)
	body := absplGet(t, h, tok)
	if n := len(body["results"].([]any)); n != 0 {
		t.Fatalf("nil store returned %d results, want 0", n)
	}
	if total, _ := body["total"].(float64); total != 0 {
		t.Fatalf("nil store reported total=%v, want 0", total)
	}
}

// ── real data ───────────────────────────────────────────────────────────────

func TestLibraryPlaylists_ReturnsRealPlaylistsWithExpandedItems(t *testing.T) {
	// The store is populated AFTER the harness builds, because the seeded book ids
	// only exist once the harness has seeded its library.
	store := &absplFakeStore{}
	h2, seed, tok2 := absplHarness(t, store)
	store.lists = []database.UserPlaylist{{
		ID:              "01PLAYLISTULID000000000000",
		Name:            "Bedtime",
		Description:     "for the kids",
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         []string{seed.singleID},
		CreatedByUserID: "u1",
		CreatedAt:       time.UnixMilli(1785370201391),
		UpdatedAt:       time.UnixMilli(1785370201438),
	}}
	body := absplGet(t, h2, tok2)

	results := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("got %d playlists, want 1", len(results))
	}
	pl := results[0].(map[string]any)

	if got := pl["name"]; got != "Bedtime" {
		t.Fatalf("name = %#v, want \"Bedtime\"", got)
	}
	if got := pl["id"]; got != "01PLAYLISTULID000000000000" {
		t.Fatalf("id = %#v", got)
	}
	if got := pl["description"]; got != "for the kids" {
		t.Fatalf("description = %#v", got)
	}
	if got := pl["libraryId"]; got != h2.libraryID() {
		t.Fatalf("libraryId = %#v, want %q", got, h2.libraryID())
	}
	if got, _ := pl["lastUpdate"].(float64); int64(got) != 1785370201438 {
		t.Fatalf("lastUpdate = %v, want the playlist's UpdatedAt in ms", got)
	}

	items := pl["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	item := items[0].(map[string]any)

	// 🔴 libraryItemId must be the 36-char sync UUID, never the 26-char Book ULID:
	// Absorb splits compound ids by FIXED BYTE OFFSET substring(0,36) (§1.7.1), so a
	// ULID here mis-truncates into the wrong /api/me/progress path.
	libItemID, _ := item["libraryItemId"].(string)
	if len(libItemID) != 36 {
		t.Fatalf("libraryItemId = %q (%d chars), want a 36-char sync UUID", libItemID, len(libItemID))
	}
	if libItemID == seed.singleID {
		t.Fatal("libraryItemId is the raw Book id — Absorb's fixed-offset id split will mis-truncate it")
	}
	if item["episodeId"] != nil {
		t.Fatalf("episodeId = %#v, want null (this server has no podcasts)", item["episodeId"])
	}
	li, ok := item["libraryItem"].(map[string]any)
	if !ok {
		t.Fatal("item has no expanded libraryItem; a client that expands it gets nothing to show")
	}
	if li["id"] != libItemID {
		t.Fatalf("libraryItem.id = %#v, want it to match libraryItemId %q", li["id"], libItemID)
	}
}

// 🔴 ORDER IS MEANING. A playlist's order is its listening order, so the response
// must follow the playlist's own id sequence and never the store's return order.
// Seeded deliberately in the REVERSE of the library's natural order so a handler
// that forwards the store's order fails.
func TestLibraryPlaylists_PreservesPlaylistOrderNotLibraryOrder(t *testing.T) {
	store := &absplFakeStore{}
	h, seed, tok := absplHarness(t, store)
	// multi BEFORE single — the reverse of the library's own ordering, so a
	// handler that forwards GetBooksByIDs' order fails here.
	store.lists = []database.UserPlaylist{{
		ID:              "01ORDER00000000000000000000",
		Name:            "Ordered",
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         []string{seed.multiID, seed.singleID},
		CreatedByUserID: "u1",
	}}

	body := absplGet(t, h, tok)
	items := body["results"].([]any)[0].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	wantFirst := absplSyncIDFor(t, seed, seed.multiID)
	gotFirst := items[0].(map[string]any)["libraryItemId"]
	if gotFirst != wantFirst {
		t.Fatalf("items[0].libraryItemId = %#v, want the MULTI-file book (%q) — "+
			"playlist order was not preserved", gotFirst, wantFirst)
	}
}

// A playlist referencing a deleted book must DROP the entry, not emit an item with
// a null libraryItem — that shape is what red-screens a client that expands it.
func TestLibraryPlaylists_StaleBookReferenceIsDroppedNotNulled(t *testing.T) {
	store := &absplFakeStore{}
	h, seed, tok := absplHarness(t, store)
	store.lists = []database.UserPlaylist{{
		ID:              "01STALE0000000000000000000",
		Name:            "Has a ghost",
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         []string{seed.singleID, "01DELETEDBOOKID00000000000"},
		CreatedByUserID: "u1",
	}}

	body := absplGet(t, h, tok)
	items := body["results"].([]any)[0].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 — the deleted book should be dropped", len(items))
	}
	for i, raw := range items {
		it := raw.(map[string]any)
		if it["libraryItemId"] == nil || it["libraryItemId"] == "" {
			t.Fatalf("items[%d] has an empty libraryItemId", i)
		}
	}
}

// A smart playlist's membership is its LAST EVALUATION. BookIDs is populated here
// with a DIFFERENT book than MaterializedBookIDs, so a handler that reads the
// wrong field returns the wrong book rather than an empty list — a mistake an
// emptiness check would miss.
func TestLibraryPlaylists_SmartPlaylistUsesMaterializedIDs(t *testing.T) {
	store := &absplFakeStore{}
	h, seed, tok := absplHarness(t, store)
	store.lists = []database.UserPlaylist{{
		ID:                  "01SMART0000000000000000000",
		Name:                "Smart",
		Type:                database.UserPlaylistTypeSmart,
		Query:               "title:odyssey",
		BookIDs:             []string{seed.multiID},  // must be IGNORED for smart
		MaterializedBookIDs: []string{seed.singleID}, // must be USED
		CreatedByUserID:     "u1",
	}}

	body := absplGet(t, h, tok)
	items := body["results"].([]any)[0].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	want := absplSyncIDFor(t, seed, seed.singleID)
	if got := items[0].(map[string]any)["libraryItemId"]; got != want {
		t.Fatalf("smart playlist resolved to %#v, want the MATERIALIZED book %q — "+
			"the handler read BookIDs instead of MaterializedBookIDs", got, want)
	}
}

// An unmaterialized smart playlist renders empty rather than erroring: evaluating
// the query needs the Bleve index, and this read path must not depend on it.
func TestLibraryPlaylists_UnmaterializedSmartPlaylistRendersEmpty(t *testing.T) {
	store := &absplFakeStore{lists: []database.UserPlaylist{{
		ID:              "01UNMAT00000000000000000000",
		Name:            "Never evaluated",
		Type:            database.UserPlaylistTypeSmart,
		Query:           "title:odyssey",
		CreatedByUserID: "u1",
	}}}
	h, _, tok := absplHarness(t, store)

	body := absplGet(t, h, tok)
	results := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("got %d playlists, want 1 — an unevaluated smart playlist must still be listed", len(results))
	}
	items := results[0].(map[string]any)["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
}

// 🔴 CROSS-USER DISCLOSURE. Asserted on the argument the handler passed, not on
// the result: a fake that ignores userID would return the same list either way, so
// checking the response cannot distinguish a scoped query from an unscoped one.
func TestLibraryPlaylists_ScopedToTheCallingUser(t *testing.T) {
	store := &absplFakeStore{}
	h, _, tok := absplHarness(t, store)
	_ = absplGet(t, h, tok)

	if store.calls == 0 {
		t.Fatal("the playlist store was never queried")
	}
	if store.gotUserID == "" {
		t.Fatal("the handler queried playlists with an EMPTY user id — every user's " +
			"playlists would be visible to every caller")
	}
	if store.gotUserID != "u1" {
		t.Fatalf("scoped to user %q, want the calling user \"u1\"", store.gotUserID)
	}
}

// absplSyncIDFor returns the client-visible sync id for a book, via the SAME
// identity store the handler uses (withLibrary wires seed.lib as o.Identity), so
// the expected value is derived rather than hardcoded.
func absplSyncIDFor(t *testing.T, seed *oracleSeed, bookID string) string {
	t.Helper()
	id, err := seed.lib.MintOrGetSyncID(bookID)
	if err != nil {
		t.Fatalf("MintOrGetSyncID(%s): %v", bookID, err)
	}
	return id
}
