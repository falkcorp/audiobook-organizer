// file: internal/server/handlers/abs/collections_test.go
// version: 1.1.0
// guid: 0f5a92c3-71b8-4d26-9e04-3ca8175bd6e1
// last-edited: 2026-08-22

package abs_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// ── fake store ──────────────────────────────────────────────────────────────
//
// Helpers are prefixed abscol* because this package's tests share one namespace.
//
// Like the playlist tests, there is no oracle capture for collections — zero of
// the 28 fixtures request one — so these assert OUR shape and the decidable
// properties: the create→list round trip that was broken, the write gate,
// server-wide visibility, id translation, and the LibraryItem shape that #2496
// established is load-bearing.

type abscolFakeStore struct {
	cols []database.Collection
	err  error
	// gotType records the type filter the handler passed, so "the list is not
	// silently filtered to static" is assertable rather than inferred.
	gotType string
	creates int
	updates int
	deletes int
	// staleRead, when non-nil, is returned ONCE by the next GetCollection call
	// for its ID instead of the live row, then cleared. It exists to simulate
	// a caller whose read happened before a concurrent caller's write landed —
	// the interleave that the Collection.Version compare-and-swap exists to
	// catch — without spinning up real goroutines against gin/httptest, which
	// cannot be made to land in a deterministic order.
	staleRead *database.Collection
}

func (f *abscolFakeStore) ListCollections(t string, _, _ int) ([]database.Collection, int, error) {
	f.gotType = t
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.cols, len(f.cols), nil
}

// GetCollection resolves by id with NO owner filter, exactly as the real store
// does. That is deliberate: a fake that scoped by user would hide the very
// property these tests exist to pin, which is that collections are server-wide.
func (f *abscolFakeStore) GetCollection(id string) (*database.Collection, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.staleRead != nil && f.staleRead.ID == id {
		cp := *f.staleRead
		f.staleRead = nil
		return &cp, nil
	}
	for i := range f.cols {
		if f.cols[i].ID == id {
			cp := f.cols[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *abscolFakeStore) CreateCollection(col *database.Collection) (*database.Collection, error) {
	f.creates++
	if f.err != nil {
		return nil, f.err
	}
	if col.ID == "" {
		col.ID = "col-generated"
	}
	f.cols = append(f.cols, *col)
	return col, nil
}

// UpdateCollection mirrors PebbleStore.UpdateCollection's compare-and-swap on
// Version (internal/database/pebble_store_collections.go): a write built from
// a stale read — col.Version not matching the currently-stored row's Version —
// is rejected instead of silently clobbering whatever landed since. Real
// callers reach this through the same "already in use"-style string match the
// handlers use for the CAS conflict, so the error text matters, not just its
// presence.
func (f *abscolFakeStore) UpdateCollection(col *database.Collection) error {
	f.updates++
	if f.err != nil {
		return f.err
	}
	for i := range f.cols {
		if f.cols[i].ID == col.ID {
			if col.Version != f.cols[i].Version {
				// Wrap the real sentinel, not a lookalike message: the
				// handler detects this with errors.Is, so a fake that only
				// reproduced the wording would silently stop matching and
				// this test would pass against a broken handler.
				return fmt.Errorf("collection %s: %w: expected %d, got %d",
					col.ID, database.ErrCollectionVersionConflict, f.cols[i].Version, col.Version)
			}
			col.Version = f.cols[i].Version + 1
			f.cols[i] = *col
			return nil
		}
	}
	return nil
}

func (f *abscolFakeStore) DeleteCollection(id string) error {
	f.deletes++
	if f.err != nil {
		return f.err
	}
	kept := f.cols[:0]
	for _, c := range f.cols {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.cols = kept
	return nil
}

func withCollections(s abshandler.CollectionStore) harnessOpt {
	return func(o *abshandler.Options) { o.Collections = s }
}

// abscolHarness builds the browse harness plus a collection store, and returns a
// token for a user who MAY manage collections.
//
// 🔴 THE ROLE GRANT IS THE WHOLE POINT OF THIS HELPER. seedUser gives the user
// role "viewer", and the fake store's roles map is empty by default, so the
// caller resolves to ZERO permissions. A gating test written without this would
// see 403 for the right answer and the wrong reason, and would keep passing if
// the gate were deleted — it would be measuring an unseeded fixture, not a rule.
func abscolHarness(t *testing.T, store abshandler.CollectionStore) (*harness, *oracleSeed, string) {
	t.Helper()
	seed := seedOracleLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	abscolGrantManage(t, h, "u1")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")
	return h, seed, tok
}

// abscolGrantManage gives a seeded user a role carrying PermCollectionsManage.
func abscolGrantManage(t *testing.T, h *harness, userID string) {
	t.Helper()
	h.store.roles["curators"] = &database.Role{
		ID:          "curators",
		Name:        "curators",
		Permissions: []auth.Permission{auth.PermLibraryView, auth.PermCollectionsManage},
	}
	u := h.store.users[userID]
	if u == nil {
		t.Fatalf("user %s was not seeded", userID)
	}
	u.Roles = append(u.Roles, "curators")
}

func abscolResults(t *testing.T, h *harness, tok string) []map[string]any {
	t.Helper()
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/collections",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("GET collections = %d, want 200", code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want object", body)
	}
	raw, _ := m["results"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if row, ok := r.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

// ── the reported bug ────────────────────────────────────────────────────────

// TestCollections_CreateThenListRoundTrip is the regression test for the
// user-reported failure.
//
// MEASURED IN PRODUCTION 2026-08-16 15:45:10-15:45:12: five POSTs to
// /api/collections from one client, all answered 404 "endpoint not found",
// because the route did not exist. The list half was worse than absent — it was
// h.EmptyPage, a stub returning a valid, permanently empty Page<T>, so the tab
// rendered "no collections" rather than an error.
//
// Asserting the ROUND TRIP rather than the POST's status is what makes this
// meaningful: a create that returns 200 and does not appear in the list is the
// same empty screen the user reported, reached by a different route.
func TestCollections_CreateThenListRoundTrip(t *testing.T) {
	store := &abscolFakeStore{}
	h, seed, tok := abscolHarness(t, store)

	syncID := absplSyncIDFor(t, seed, seed.singleID)

	code, body := h.doAny(t, request{
		method:  http.MethodPost,
		path:    "/api/collections",
		headers: bearer(tok),
		body: map[string]any{
			"libraryId":   h.libraryID(),
			"name":        "Tasting",
			"description": "things to try",
			"books":       []string{syncID},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /api/collections = %d, want 200\n"+
			"this route answered 404 five times in production on 2026-08-16", code)
	}
	created, _ := body.(map[string]any)
	if created["name"] != "Tasting" {
		t.Fatalf("created collection name = %v, want Tasting", created["name"])
	}
	if store.creates != 1 {
		t.Fatalf("store saw %d creates, want 1", store.creates)
	}

	rows := abscolResults(t, h, tok)
	if len(rows) != 1 {
		t.Fatalf("collection list has %d rows, want 1 — a create that does not "+
			"appear in the list is the same empty screen, reached differently", len(rows))
	}
	if rows[0]["name"] != "Tasting" {
		t.Fatalf("listed collection name = %v, want Tasting", rows[0]["name"])
	}
	// The list must ask for BOTH kinds. Passing "static" here would hide every
	// dynamic collection from the app while looking like a working list.
	if store.gotType != "" {
		t.Fatalf("list filtered on type %q; it must request both static and dynamic", store.gotType)
	}
}

// TestCollections_SyncIDsAreTranslatedToBookIDs pins the id translation.
//
// The client holds 36-char sync ids; our books are 26-char ULIDs. Storing the
// client's ids verbatim would create successfully, answer 200, and render an
// empty collection forever — the same "succeeds and shows nothing" failure this
// feature exists to remove. Asserting the STORED id is the only way to see it:
// the HTTP response looks identical either way.
func TestCollections_SyncIDsAreTranslatedToBookIDs(t *testing.T) {
	store := &abscolFakeStore{}
	h, seed, tok := abscolHarness(t, store)

	syncID := absplSyncIDFor(t, seed, seed.singleID)
	if syncID == seed.singleID {
		t.Fatal("fixture is degenerate: the sync id equals the book id, so this test cannot fail")
	}

	code, _ := h.doAny(t, request{
		method:  http.MethodPost,
		path:    "/api/collections",
		headers: bearer(tok),
		body:    map[string]any{"name": "Translated", "books": []string{syncID}},
	})
	if code != http.StatusOK {
		t.Fatalf("POST = %d, want 200", code)
	}
	if len(store.cols) != 1 {
		t.Fatalf("store holds %d collections, want 1", len(store.cols))
	}
	got := store.cols[0].BookIDs
	if len(got) != 1 || got[0] != seed.singleID {
		t.Fatalf("stored BookIDs = %v, want [%s] (the INTERNAL book id, not the sync id %s)",
			got, seed.singleID, syncID)
	}
}

// ── the write gate ──────────────────────────────────────────────────────────

// TestCollections_CreateRequiresPermission asserts the gate the user asked for:
// only an admin, or someone holding the collections permission, may create one.
//
// The single permission check covers both. adminPermissions() is All() and
// SeedRoles recomputes existing roles on every boot, so admin acquires
// collections.manage automatically — which is why there is no admin-OR-permission
// branch to test here, and why one would be dead code.
func TestCollections_CreateRequiresPermission(t *testing.T) {
	store := &abscolFakeStore{}
	seed := seedOracleLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	// Seeded WITHOUT the curators role — this user holds no permissions at all.
	h.seedUser(t, "u2", "reader", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "reader", "pw-pw-pw-pw")), "accessToken")

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"create", http.MethodPost, "/api/collections"},
		{"update", http.MethodPatch, "/api/collections/c1"},
		{"delete", http.MethodDelete, "/api/collections/c1"},
		{"add book", http.MethodPost, "/api/collections/c1/book"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := h.doAny(t, request{
				method:  tc.method,
				path:    tc.path,
				headers: bearer(tok),
				body:    map[string]any{"name": "nope", "id": "x"},
			})
			if code != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403 for a caller without %s",
					tc.method, tc.path, code, auth.PermCollectionsManage)
			}
		})
	}
	if store.creates != 0 || store.updates != 0 || store.deletes != 0 {
		t.Fatalf("an ungated call reached the store: creates=%d updates=%d deletes=%d",
			store.creates, store.updates, store.deletes)
	}
}

// TestCollections_ReadingIsNotGated is the negative control for the test above.
//
// Without it, a handler that 403'd EVERY collection route would pass the gating
// test while making the feature unusable for ordinary users — the gate would be
// "verified" by an instrument that cannot tell a correct gate from a broken one.
func TestCollections_ReadingIsNotGated(t *testing.T) {
	store := &abscolFakeStore{cols: []database.Collection{
		{ID: "c1", Name: "Shared", Type: database.CollectionTypeStatic},
	}}
	seed := seedOracleLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u2", "reader", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "reader", "pw-pw-pw-pw")), "accessToken")

	if rows := abscolResults(t, h, tok); len(rows) != 1 {
		t.Fatalf("a user without manage permission saw %d collections, want 1 — "+
			"reads are open, only writes are gated", len(rows))
	}
	code, _ := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/collections/c1",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("GET /api/collections/c1 = %d, want 200 for any authenticated user", code)
	}
}

// ── server-wide, NOT per-user ───────────────────────────────────────────────

// TestCollections_AreVisibleToEveryUser pins the deliberate divergence from
// playlists.
//
// playlists.go 404s a playlist whose CreatedByUserID is not the caller, and that
// is correct there. Copying it here — the obvious thing to do, since this file
// is modelled on that one — would hide most collections from most users while
// looking like a security fix. Collections are server-wide by product decision:
// CreatedByUserID is attribution, never an access decision.
func TestCollections_AreVisibleToEveryUser(t *testing.T) {
	store := &abscolFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Made By Someone Else", Type: database.CollectionTypeStatic,
		CreatedByUserID: "somebody-else",
	}}}
	seed := seedOracleLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u2", "otheruser", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "otheruser", "pw-pw-pw-pw")), "accessToken")

	rows := abscolResults(t, h, tok)
	if len(rows) != 1 {
		t.Fatalf("user saw %d collections, want 1 — collections are server-wide; "+
			"an ownership filter copied from playlists.go would produce exactly this failure",
			len(rows))
	}
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/collections/c1",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("GET another user's collection = %d, want 200 (server-wide)", code)
	}
	if m, _ := body.(map[string]any); m["userId"] != "somebody-else" {
		t.Fatalf("userId = %v, want somebody-else — it is attribution, and must survive", m["userId"])
	}
}

// ── shape ───────────────────────────────────────────────────────────────────

// TestCollections_BooksAreLibraryItems applies the #2496 lesson before it can
// recur.
//
// The series list shipped `books[]` entries carrying six ad-hoc fields and none
// of media/media.metadata/mediaType/path. A typed client decodes books as
// [LibraryItem] as a unit, so ONE undecodable entry discards the entire
// response — which is why 23 of 50 series with real books rendered as
// "No Series Found" while the endpoint returned HTTP 200.
func TestCollections_BooksAreLibraryItems(t *testing.T) {
	seed := seedOracleLibrary(t)
	store := &abscolFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Shaped", Type: database.CollectionTypeStatic,
		BookIDs: []string{seed.singleID, seed.multiID},
	}}}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")

	rows := abscolResults(t, h, tok)
	if len(rows) != 1 {
		t.Fatalf("got %d collections, want 1", len(rows))
	}
	books, _ := rows[0]["books"].([]any)
	if len(books) == 0 {
		t.Fatal("collection served no books")
	}
	for i, b := range books {
		if !absBookIsLibraryItem(b) {
			t.Errorf("book %d is not a decodable ABS LibraryItem: %#v\n"+
				"one bad entry discards the WHOLE response in a typed client", i, b)
		}
	}
}

// TestCollections_NumBooksMatchesBooksServed pins self-consistency.
//
// Measured on the series route 2026-08-16: 9 of 50 rows reported numBooks >= 1
// while carrying books: [], because members are dropped after the count is
// taken. A row that reports a count it cannot list forces the client to guess
// which half to believe. Here the count is len(books) served, by construction —
// this test is what keeps it that way.
func TestCollections_NumBooksMatchesBooksServed(t *testing.T) {
	seed := seedOracleLibrary(t)
	store := &abscolFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Half Stale", Type: database.CollectionTypeStatic,
		// One real member and one referring to a book that no longer exists.
		BookIDs: []string{seed.singleID, "01DELETEDBOOKID0000000000"},
	}}}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")

	rows := abscolResults(t, h, tok)
	books, _ := rows[0]["books"].([]any)
	num, _ := rows[0]["numBooks"].(float64)
	if int(num) != len(books) {
		t.Fatalf("numBooks=%d but %d books served — the stale member was dropped "+
			"after the count was taken", int(num), len(books))
	}
	if len(books) != 1 {
		t.Fatalf("served %d books, want 1 (the stale reference must be dropped, "+
			"not emitted as a partial object)", len(books))
	}
}

// ── dynamic collections ─────────────────────────────────────────────────────

// TestCollections_DynamicServesMaterializedMembers pins the read-path rule.
//
// A dynamic collection's membership is its LAST EVALUATION, not its query.
// Evaluating here would make a read path depend on the Bleve index being open,
// and an unevaluated collection would then fail the whole library tab instead of
// rendering empty. playlistDTO already made this call for smart playlists; this
// mirrors it rather than re-deciding it.
func TestCollections_DynamicServesMaterializedMembers(t *testing.T) {
	seed := seedOracleLibrary(t)
	store := &abscolFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Recent", Type: database.CollectionTypeDynamic,
		Query: "added:>2020",
		// BookIDs is deliberately populated with the WRONG book: a dynamic
		// collection must ignore it entirely. If the handler reads BookIDs this
		// test fails with the multi book instead of the single one.
		BookIDs:             []string{seed.multiID},
		MaterializedBookIDs: []string{seed.singleID},
	}}}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")

	rows := abscolResults(t, h, tok)
	books, _ := rows[0]["books"].([]any)
	if len(books) != 1 {
		t.Fatalf("dynamic collection served %d books, want 1", len(books))
	}
	first, _ := books[0].(map[string]any)
	if first["id"] != absplSyncIDFor(t, seed, seed.singleID) {
		t.Fatalf("dynamic collection served the wrong book (%v) — it must serve its "+
			"MATERIALIZED members, not BookIDs", first["id"])
	}
}

// TestCollections_DynamicMembershipIsNotEditable pins the write-path rule.
//
// Accepting a book list for a dynamic collection would store a set the next
// evaluation silently discards: a write that returns 200 and does not persist.
// 409 tells the user why instead.
func TestCollections_DynamicMembershipIsNotEditable(t *testing.T) {
	seed := seedOracleLibrary(t)
	store := &abscolFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Recent", Type: database.CollectionTypeDynamic, Query: "added:>2020",
	}}}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	abscolGrantManage(t, h, "u1")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")

	code, _ := h.doAny(t, request{
		method:  http.MethodPost,
		path:    "/api/collections/c1/book",
		headers: bearer(tok),
		body:    map[string]any{"id": absplSyncIDFor(t, seed, seed.singleID)},
	})
	if code != http.StatusConflict {
		t.Fatalf("adding a book to a dynamic collection = %d, want 409 — accepting it "+
			"would store members the next evaluation discards", code)
	}
	if store.updates != 0 {
		t.Fatalf("the rejected edit still wrote to the store (%d updates)", store.updates)
	}
}

// TestAddBookToCollection_ConcurrentAdds_SecondGets409 pins the CAS this task
// added to PebbleStore.UpdateCollection (internal/database/pebble_store_collections.go)
// at the HTTP boundary this handler actually exercises.
//
// AddBookToCollection has no locking of its own: it reads the collection via
// lookupCollection, mutates BookIDs in memory, and writes it back — a classic
// read-modify-write. Two requests racing on the same collection, where the
// second one's read happened before the first one's write landed, must not
// both succeed: the second must be rejected (409), not silently accept and
// clobber the first request's change. A test that only checks the version
// counter incremented would pass even if both writes silently landed one after
// another — it would not prove the CONFLICTING write was ever rejected. This
// test instead asserts on the actual outcome that matters: one write wins
// (200, its book present), the other is refused (409, its book absent), and no
// request silently lost its change.
//
// Real goroutines through gin/httptest have no guaranteed interleave, so the
// race is engineered explicitly via abscolFakeStore.staleRead: request B's
// GetCollection is made to return the pre-A snapshot, exactly reproducing "B
// read before A wrote" without depending on scheduler timing.
func TestAddBookToCollection_ConcurrentAdds_SecondGets409(t *testing.T) {
	seed := seedOracleLibrary(t)
	initial := database.Collection{
		ID: "c1", Name: "Road Trip", Type: database.CollectionTypeStatic, Version: 1,
	}
	store := &abscolFakeStore{cols: []database.Collection{initial}}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	abscolGrantManage(t, h, "u1")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")

	// Caller A: reads the collection at Version 1, adds seed.singleID, and
	// writes it back. This must succeed and bump the stored Version to 2.
	codeA, _ := h.doAny(t, request{
		method:  http.MethodPost,
		path:    "/api/collections/c1/book",
		headers: bearer(tok),
		body:    map[string]any{"id": absplSyncIDFor(t, seed, seed.singleID)},
	})
	if codeA != http.StatusOK {
		t.Fatalf("caller A: POST = %d, want 200", codeA)
	}
	if store.cols[0].Version != 2 {
		t.Fatalf("after caller A, store.cols[0].Version = %d, want 2", store.cols[0].Version)
	}

	// Caller B: engineer its read to be the PRE-A snapshot (Version 1, no
	// books) — this is the stale read a genuinely concurrent second request
	// would have gotten had it read before A's write landed.
	stale := initial
	store.staleRead = &stale

	codeB, bodyB := h.doAny(t, request{
		method:  http.MethodPost,
		path:    "/api/collections/c1/book",
		headers: bearer(tok),
		body:    map[string]any{"id": absplSyncIDFor(t, seed, seed.multiID)},
	})
	if codeB != http.StatusConflict {
		t.Fatalf("caller B: POST = %d, want 409 — a write built from a stale read must not "+
			"silently succeed and clobber caller A's change; body: %v", codeB, bodyB)
	}

	// The decisive assertion: the store holds caller A's change and ONLY
	// caller A's change. A version counter that merely incremented would not
	// prove this — it would pass even if both writes had silently landed
	// sequentially, one clobbering the other with no rejection at all.
	final := store.cols[0]
	if len(final.BookIDs) != 1 || final.BookIDs[0] != seed.singleID {
		t.Fatalf("final BookIDs = %v, want exactly [%s] (caller A's book only — "+
			"caller B's rejected write must not have been applied)", final.BookIDs, seed.singleID)
	}
	if final.Version != 2 {
		t.Fatalf("final Version = %d, want 2 (unchanged by caller B's rejected write)", final.Version)
	}
	if store.updates != 2 {
		t.Fatalf("store.updates = %d, want 2 (one accepted write from A, one rejected attempt from B)",
			store.updates)
	}
}
