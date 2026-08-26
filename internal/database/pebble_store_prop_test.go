// file: internal/database/pebble_store_prop_test.go
// version: 1.2.0
// guid: 15afe4d2-3a00-4326-be15-1e3f0b11a10e

// Black-box test package: internal/testutil/rapidgen imports the database
// package, so these property tests live in database_test (not database) to
// avoid an import cycle.
package database_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/rapidgen"
	"pgregory.net/rapid"
)

// Property-based tests for PebbleStore CRUD invariants. Each test uses a
// fresh on-disk PebbleStore per rapid.Check iteration — we create the
// t.TempDir() and open the store *inside* the rapid.Check body so every
// shrunk input lands in its own empty database.
//
// These tests cover the invariants described in Task 2 of the
// property-based testing plan (docs/superpowers/plans/2026-04-17-...):
// round-trip, update/delete correctness, uniqueness indexes,
// single-active-version guarantee, tag add/remove, session lifecycle,
// and operation-change persistence.

// newPropStore spins up a fresh PebbleStore rooted at a temp dir scoped
// to the current rapid iteration. rapid.T doesn't forward TempDir/Helper
// from testing.T, so we call os.MkdirTemp directly and register both the
// store close and the dir RemoveAll with t.Cleanup (which rapid.T *does*
// expose). Every shrunk input lands in its own fresh empty DB.
func newPropStore(t *rapid.T) *database.PebbleStore {
	dir, err := os.MkdirTemp("", "pebble-prop-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	store, err := database.NewPebbleStore(filepath.Join(dir, "db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.RemoveAll(dir)
	})
	return store
}

// TestProp_Book_RoundTrip: CreateBook → GetBookByID returns a book with
// the same user-supplied scalar fields. We check the fields that are
// not rewritten by the store (ID is assigned, timestamps are set).
func TestProp_Book_RoundTrip(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		b := rapidgen.Book(t)

		created, err := store.CreateBook(b)
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		if created.ID == "" {
			t.Fatalf("CreateBook should assign ID")
		}

		got, err := store.GetBookByID(created.ID)
		if err != nil {
			t.Fatalf("GetBookByID: %v", err)
		}
		if got == nil {
			t.Fatalf("GetBookByID returned nil for id %q", created.ID)
		}
		if got == nil {
			return
		}
		if got.Title != b.Title {
			t.Errorf("Title: got %q, want %q", got.Title, b.Title)
		}
		if got.FilePath != b.FilePath {
			t.Errorf("FilePath: got %q, want %q", got.FilePath, b.FilePath)
		}
		if got.Format != b.Format {
			t.Errorf("Format: got %q, want %q", got.Format, b.Format)
		}
	})
}

// TestProp_Book_UpdatePreservesID: after UpdateBook, the ID is unchanged
// and modified scalar fields are reflected on subsequent reads.
func TestProp_Book_UpdatePreservesID(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		created, err := store.CreateBook(rapidgen.Book(t))
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}

		// Generate a fresh book with new random fields and apply as an update.
		updated := rapidgen.Book(t)
		updated.ID = created.ID
		result, err := store.UpdateBook(created.ID, updated)
		if err != nil {
			t.Fatalf("UpdateBook: %v", err)
		}
		if result.ID != created.ID {
			t.Errorf("UpdateBook changed ID: got %q, want %q", result.ID, created.ID)
		}

		got, err := store.GetBookByID(created.ID)
		if err != nil || got == nil {
			t.Fatalf("GetBookByID after update: %v / %v", got, err)
		}
		if got.ID != created.ID {
			t.Errorf("Persisted ID changed: got %q, want %q", got.ID, created.ID)
		}
		if got.Title != updated.Title {
			t.Errorf("Title not updated: got %q, want %q", got.Title, updated.Title)
		}
		if got.Format != updated.Format {
			t.Errorf("Format not updated: got %q, want %q", got.Format, updated.Format)
		}
	})
}

// TestProp_Book_DeleteThenGetReturnsNil: after DeleteBook, GetBookByID
// returns (nil, nil).
func TestProp_Book_DeleteThenGetReturnsNil(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		created, err := store.CreateBook(rapidgen.Book(t))
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		if err := store.DeleteBook(created.ID); err != nil {
			t.Fatalf("DeleteBook: %v", err)
		}
		got, err := store.GetBookByID(created.ID)
		if err != nil {
			t.Fatalf("GetBookByID after delete returned error: %v", err)
		}
		if got != nil {
			t.Errorf("GetBookByID after delete returned non-nil: %+v", got)
		}
	})
}

// TestProp_BookVersion_SingleActiveInvariant: for N random versions
// created for the same book, at most one ends up with Status=active
// according to GetBookVersionsByBookID.
//
// The store rejects a second active-at-create, so we drive the invariant
// by creating a random mix of statuses and then verifying the
// active-pointer index matches the stored rows.
func TestProp_BookVersion_SingleActiveInvariant(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		bookID := "b-" + rapid.StringMatching(`[a-z0-9]{8}`).Draw(t, "book_id")

		n := rapid.IntRange(1, 8).Draw(t, "n_versions")
		activeAttempts := 0
		for range n {
			v := rapidgen.BookVersion(t, bookID)
			if _, err := store.CreateBookVersion(v); err != nil {
				// The store rejects second-active — that's the invariant
				// under test, not a bug.
				continue
			}
			if v.Status == database.BookVersionStatusActive {
				activeAttempts++
			}
		}

		// Invariant: the live row set has at most one active version per book.
		versions, err := store.GetBookVersionsByBookID(bookID)
		if err != nil {
			t.Fatalf("GetBookVersionsByBookID: %v", err)
		}
		activeCount := 0
		for _, v := range versions {
			if v.Status == database.BookVersionStatusActive {
				activeCount++
			}
		}
		if activeCount > 1 {
			t.Errorf("single-active invariant violated: %d active versions for book %s", activeCount, bookID)
		}

		// If any active was successfully written, GetActiveVersionForBook
		// must return the unique one. If none, it must return nil.
		got, err := store.GetActiveVersionForBook(bookID)
		if err != nil {
			t.Fatalf("GetActiveVersionForBook: %v", err)
		}
		if activeCount == 1 && got == nil {
			t.Errorf("expected active version, got nil")
		}
		if activeCount == 0 && got != nil {
			t.Errorf("expected no active version, got %+v", got)
		}
		_ = activeAttempts // silence unused-by-path warnings under shrinking
	})
}

// TestProp_UserPlaylist_NameUniqueness: creating two distinct playlists
// with the same (case-insensitive) name — the second create fails.
func TestProp_UserPlaylist_NameUniqueness(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		pl1 := rapidgen.UserPlaylist(t)
		if _, err := store.CreateUserPlaylist(pl1); err != nil {
			t.Fatalf("CreateUserPlaylist 1: %v", err)
		}

		// A second playlist with a fresh ID but the same name must fail.
		pl2 := rapidgen.UserPlaylist(t)
		pl2.Name = pl1.Name
		pl2.ID = "" // let the store assign a new ID so the dup is not self-match
		if _, err := store.CreateUserPlaylist(pl2); err == nil {
			t.Errorf("expected duplicate-name create to fail for name %q", pl1.Name)
		}
	})
}

// TestProp_User_UsernameUniqueness: creating two users with the same
// (case-insensitive) username — the second create fails.
func TestProp_User_UsernameUniqueness(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		username, email1, hash1 := rapidgen.User(t)
		_, email2Base, hash2 := rapidgen.User(t)

		if _, err := store.CreateUser(username, email1, "argon2id", hash1, []string{"viewer"}, "active"); err != nil {
			t.Fatalf("CreateUser 1: %v", err)
		}

		// Second user: same username, different email (so the email uniqueness
		// check doesn't short-circuit the username check).
		email2 := "alt-" + email2Base
		if _, err := store.CreateUser(username, email2, "argon2id", hash2, []string{"viewer"}, "active"); err == nil {
			t.Errorf("expected duplicate-username create to fail for %q", username)
		}
	})
}

// TestProp_Tag_AddRemoveRoundtrip: AddBookTag then GetBookTags contains
// the tag; RemoveBookTag then GetBookTags no longer contains it.
func TestProp_Tag_AddRemoveRoundtrip(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		created, err := store.CreateBook(rapidgen.Book(t))
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		tag := rapidgen.Tag(t)

		if err := store.AddBookTag(created.ID, tag); err != nil {
			t.Fatalf("AddBookTag: %v", err)
		}
		tags, err := store.GetBookTags(created.ID)
		if err != nil {
			t.Fatalf("GetBookTags after add: %v", err)
		}
		if !containsString(tags, tag) {
			t.Errorf("tag %q not found after add; have %v", tag, tags)
		}

		if err := store.RemoveBookTag(created.ID, tag); err != nil {
			t.Fatalf("RemoveBookTag: %v", err)
		}
		tags, err = store.GetBookTags(created.ID)
		if err != nil {
			t.Fatalf("GetBookTags after remove: %v", err)
		}
		if containsString(tags, tag) {
			t.Errorf("tag %q still present after remove; have %v", tag, tags)
		}
	})
}

// TestProp_Session_CreateRevoke: CreateSession returns a session we can
// GetSession back, and RevokeSession flips Revoked to true.
func TestProp_Session_CreateRevoke(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		username, email, hash := rapidgen.User(t)
		u, err := store.CreateUser(username, email, "argon2id", hash, []string{"viewer"}, "active")
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}

		ip := "127.0.0.1"
		ua := "rapid-agent"
		ttl := time.Duration(rapid.IntRange(1, 3600).Draw(t, "ttl_sec")) * time.Second

		sess, err := store.CreateSession(u.ID, ip, ua, ttl)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if sess.ID == "" {
			t.Fatalf("CreateSession must assign ID")
		}

		got, err := store.GetSession(sess.ID)
		if err != nil || got == nil {
			t.Fatalf("GetSession: %+v / %v", got, err)
		}
		if got.UserID != u.ID {
			t.Errorf("Session.UserID: got %q, want %q", got.UserID, u.ID)
		}
		if got.Revoked {
			t.Errorf("new session must not be revoked")
		}

		if err := store.RevokeSession(sess.ID); err != nil {
			t.Fatalf("RevokeSession: %v", err)
		}
		got, err = store.GetSession(sess.ID)
		if err != nil || got == nil {
			t.Fatalf("GetSession post-revoke: %+v / %v", got, err)
		}
		if !got.Revoked {
			t.Errorf("session must be revoked after RevokeSession")
		}
	})
}

// TestProp_OperationChange_Persistence: after CreateOperationChange,
// GetOperationChanges for the same operation contains the new change.
func TestProp_OperationChange_Persistence(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		opID := "op-" + rapid.StringMatching(`[a-z0-9]{10}`).Draw(t, "op_id")
		bookID := "book-" + rapid.StringMatching(`[a-z0-9]{10}`).Draw(t, "book_id")

		n := rapid.IntRange(1, 5).Draw(t, "n_changes")
		wantIDs := make(map[string]struct{}, n)
		for range n {
			change := rapidgen.OperationChange(t, opID, bookID)
			if err := store.CreateOperationChange(change); err != nil {
				t.Fatalf("CreateOperationChange: %v", err)
			}
			if change.ID == "" {
				t.Fatalf("CreateOperationChange must assign ID")
			}
			wantIDs[change.ID] = struct{}{}
		}

		got, err := store.GetOperationChanges(opID)
		if err != nil {
			t.Fatalf("GetOperationChanges: %v", err)
		}
		if len(got) != n {
			t.Errorf("len(GetOperationChanges) = %d, want %d", len(got), n)
		}
		gotIDs := make(map[string]struct{}, len(got))
		for _, c := range got {
			gotIDs[c.ID] = struct{}{}
			if c.OperationID != opID {
				t.Errorf("OperationID: got %q, want %q", c.OperationID, opID)
			}
			if c.BookID != bookID {
				t.Errorf("BookID: got %q, want %q", c.BookID, bookID)
			}
		}
		for id := range wantIDs {
			if _, ok := gotIDs[id]; !ok {
				t.Errorf("created change id %q missing from GetOperationChanges", id)
			}
		}
	})
}

// TestProp_ListUsers_ContainsCreatedUser: after CreateUser, ListUsers
// returns a slice that includes the new user's ID.
func TestProp_ListUsers_ContainsCreatedUser(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		store := newPropStore(t)
		username, email, hash := rapidgen.User(t)
		u, err := store.CreateUser(username, email, "argon2id", hash, []string{"viewer"}, "active")
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}

		users, err := store.ListUsers()
		if err != nil {
			t.Fatalf("ListUsers: %v", err)
		}
		var found bool
		for _, listed := range users {
			if listed.ID == u.ID {
				found = true
				if listed.Username != username {
					t.Errorf("listed username %q, want %q", listed.Username, username)
				}
				break
			}
		}
		if !found {
			t.Errorf("created user %q not found in ListUsers (len=%d)", u.ID, len(users))
		}
	})
}

// containsString returns true if needle appears in haystack. Used by the
// tag round-trip test.
func containsString(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
