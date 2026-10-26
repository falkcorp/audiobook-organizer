// file: internal/playlist/evaluator_collection_test.go
// version: 1.1.1
// guid: e8b2d5a7-1f43-4c96-9a08-b7d3c6e4f215
// last-edited: 2026-10-26

package playlist

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// A dynamic collection is evaluated for the VIEWER (owner decision 2026-10-25,
// superseding D75's owner evaluation): the per-user query fields read the
// state rows of the user id the evaluator is given, and the collection handler
// passes the requesting user. These tests pin the evaluator half of that: for
// the same query and the same index, the members follow the user id and
// nothing else, and an empty user id (no viewer) matches nothing.

// collectionFixture seeds four synthetic books into a real store and a complete
// Bleve index, and per-user state for alice and bob. Activity dates are
// relative to now so the relative-date case does not rot.
func collectionFixture(t *testing.T) (*database.PebbleStore, *search.BleveIndex, time.Time) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	if err := idx.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}

	for _, id := range []string{"b1", "b2", "b3", "b4"} {
		if _, err := store.CreateBook(&database.Book{ID: id, Title: "Synthetic " + id, FilePath: "/tmp/" + id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := idx.IndexBook(search.BookDocument{BookID: id, Title: "Synthetic " + id, Author: "Test Author"}); err != nil {
			t.Fatalf("index %s: %v", id, err)
		}
	}

	now := time.Now()
	day := func(n int) time.Time { return now.AddDate(0, 0, -n) }
	states := []database.UserBookState{
		{UserID: "alice", BookID: "b1", Status: database.UserBookStatusFinished, LastActivityAt: day(10)},
		{UserID: "alice", BookID: "b2", Status: database.UserBookStatusFinished, LastActivityAt: day(40)},
		{UserID: "alice", BookID: "b3", Status: database.UserBookStatusInProgress, LastActivityAt: day(5)},
		{UserID: "bob", BookID: "b2", Status: database.UserBookStatusFinished, LastActivityAt: day(3)},
		{UserID: "bob", BookID: "b4", Status: database.UserBookStatusFinished, LastActivityAt: day(60)},
	}
	for i := range states {
		if err := store.SetUserBookState(&states[i]); err != nil {
			t.Fatalf("state: %v", err)
		}
	}
	return store, idx, now
}

func evalAs(t *testing.T, store *database.PebbleStore, idx *search.BleveIndex, query, userID string) []string {
	t.Helper()
	got, err := EvaluateSmartPlaylist(store, idx, query, "", 0, userID)
	if err != nil {
		t.Fatalf("evaluate %q as %s: %v", query, userID, err)
	}
	return got
}

func TestEvaluateCollection_MembersFollowTheEvaluatingUser(t *testing.T) {
	store, idx, now := collectionFixture(t)

	cases := []struct {
		name, query string
		alice, bob  []string
	}{
		{"finished", "read_status:finished", []string{"b1", "b2"}, []string{"b2", "b4"}},
		{"unstarted includes books with no row", "read_status:unstarted", []string{"b4"}, []string{"b1", "b3"}},
		{"absolute last_played", "last_played:>" + now.AddDate(0, 0, -20).Format("2006-01-02"),
			[]string{"b1", "b3"}, []string{"b2"}},
		{"relative last_played", "last_played:>-30d", []string{"b1", "b3"}, []string{"b2"}},
		{"relative range", "last_played:-30d..now", []string{"b1", "b3"}, []string{"b2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAlice := evalAs(t, store, idx, tc.query, "alice")
			gotBob := evalAs(t, store, idx, tc.query, "bob")
			if !slices.Equal(gotAlice, tc.alice) {
				t.Errorf("as alice: got %v, want %v", gotAlice, tc.alice)
			}
			if !slices.Equal(gotBob, tc.bob) {
				t.Errorf("as bob: got %v, want %v", gotBob, tc.bob)
			}
			// The same evaluation twice is the same set: nothing about the
			// caller, the clock or the order of calls leaks in.
			if again := evalAs(t, store, idx, tc.query, "alice"); !slices.Equal(again, gotAlice) {
				t.Errorf("alice's second evaluation differs: %v vs %v", again, gotAlice)
			}
		})
	}
}

// A user with no state rows at all has nothing finished and everything
// unstarted: a viewer who never listened must not error or silently match
// everything that is not theirs.
func TestEvaluateCollection_UserWithNoStateRows(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	if got := evalAs(t, store, idx, "read_status:finished", "carol"); len(got) != 0 {
		t.Errorf("finished for a user with no rows: got %v, want none", got)
	}
	if got := evalAs(t, store, idx, "read_status:unstarted", "carol"); !slices.Equal(got, []string{"b1", "b2", "b3", "b4"}) {
		t.Errorf("unstarted for a user with no rows: got %v, want all four", got)
	}
}

// NO VIEWER MATCHES NOTHING for a per-user rule. Before the guard an empty
// user id read no state row, the nil state was evaluated as the zero value, and
// read_status:unstarted, progress_pct:<50 and every negated filter matched the
// whole library: a system evaluation answered "everything" instead of "nothing".
func TestEvaluateCollection_NoViewerMatchesNothingForPerUserRules(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	queries := []string{
		"read_status:finished",
		"read_status:unstarted",
		"-read_status:finished",
		"progress_pct:<50",
		"last_played:>-30d",
		"-last_played:*",
		"read_status:(finished|unstarted)",
		"Synthetic read_status:unstarted",
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			got := evalAs(t, store, idx, q, "")
			if len(got) != 0 {
				t.Errorf("no viewer, %q: got %v, want none", q, got)
			}
			if got == nil {
				t.Errorf("no viewer, %q: got nil, want an empty non-nil slice", q)
			}
		})
	}
}

// A rule that reads no user state keeps its behaviour with no viewer: the guard
// is about per-user predicates only.
func TestEvaluateCollection_NoViewerStillEvaluatesRulesWithoutUserState(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	if got := evalAs(t, store, idx, "Synthetic", ""); !slices.Equal(got, []string{"b1", "b2", "b3", "b4"}) {
		t.Errorf("no viewer, text query: got %v, want all four", got)
	}
}
