// file: internal/server/handlers/abs/authorize_test.go
// version: 1.0.0
// guid: 2f6c8d31-94b7-4e05-a8c2-51db307ea9f6
// last-edited: 2026-08-01

package abs_test

import (
	"errors"
	"net/http"
	"testing"
)

// authorizeHarness sets up a logged-in user and returns the harness plus a bearer.
func authorizeHarness(t *testing.T, ud *fakeUserData) (*harness, string) {
	t.Helper()
	h := newHarness(t, "cf,jwt", nil, withUserData(ud))
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	access := str(t, userObj(t, h.login(t, "owner", "pw-pw-pw-pw")), "accessToken")
	return h, access
}

func progressRow(id string) any {
	return map[string]any{"libraryItemId": id, "currentTime": 10.0, "progress": 0.5}
}

// TestAuthorizeReturnsCompleteProgressList is the §1.8.1 data-loss guard.
//
// AudioBooth DELETES local progress rows absent from this array, and it calls
// /api/authorize on foreground. A short list here erases the user's place in every
// book it omits, silently, on every app launch.
func TestAuthorizeReturnsCompleteProgressList(t *testing.T) {
	ud := &fakeUserData{
		progress:  []any{progressRow("item-1"), progressRow("item-2"), progressRow("item-3")},
		bookmarks: []any{},
	}
	h, access := authorizeHarness(t, ud)

	w, body := h.do(t, request{method: http.MethodPost, path: "/api/authorize",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}

	user := userObj(t, body)
	rows, ok := user["mediaProgress"].([]any)
	if !ok {
		t.Fatalf("mediaProgress missing or not an array: %v", user["mediaProgress"])
	}
	if len(rows) != 3 {
		t.Fatalf("mediaProgress has %d rows, want all 3 — a short list DELETES the "+
			"user's progress for every omitted book (§1.8.1)", len(rows))
	}
}

// TestAuthorizeUserDataFailureIsErrorNotEmptyList: when the progress list cannot be
// read we must fail loudly. A 200 carrying [] is indistinguishable to the client from
// "you have no progress anywhere", which it then makes true.
func TestAuthorizeUserDataFailureIsErrorNotEmptyList(t *testing.T) {
	// Log in while the provider is healthy, THEN break it. Constructing the harness
	// with the error already set would fail at /login (which carries the identical
	// §1.8.1 guard) and never exercise authorize at all.
	ud := &fakeUserData{progress: []any{progressRow("item-1")}, bookmarks: []any{}}
	h, access := authorizeHarness(t, ud)
	ud.err = errors.New("store unavailable")

	w, body := h.do(t, request{method: http.MethodPost, path: "/api/authorize",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code < 500 {
		t.Fatalf("got %d, want 5xx: a 200 with an empty list destroys user progress", w.Code)
	}
	if _, present := body["user"]; present {
		t.Fatalf("error response must not carry a user payload: %v", body)
	}
}

// TestAuthorizeRequiresAuthentication: it re-validates a credential, so it must demand
// one. Unauthenticated callers get 401, never a populated body.
func TestAuthorizeRequiresAuthentication(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withUserData(&fakeUserData{progress: []any{}, bookmarks: []any{}}))

	w, _ := h.do(t, request{method: http.MethodPost, path: "/api/authorize"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

// TestAuthorizeUserDefaultLibraryIDIsNonNull is the §1.8.2 login blocker: AudioBooth
// decodes userDefaultLibraryId as a non-optional String, so null makes it unable to
// log in at all.
func TestAuthorizeUserDefaultLibraryIDIsNonNull(t *testing.T) {
	h, access := authorizeHarness(t, &fakeUserData{progress: []any{}, bookmarks: []any{}})

	_, body := h.do(t, request{method: http.MethodPost, path: "/api/authorize",
		headers: map[string]string{"Authorization": "Bearer " + access}})

	id, ok := body["userDefaultLibraryId"].(string)
	if !ok || id == "" {
		t.Fatalf("userDefaultLibraryId = %v, want a non-empty String (§1.8.2)", body["userDefaultLibraryId"])
	}
}

// TestAuthorizeDoesNotMintANewSession: authorize VALIDATES a credential, it does not
// issue one. Minting per call would add a session row on every app foreground.
func TestAuthorizeDoesNotMintANewSession(t *testing.T) {
	h, access := authorizeHarness(t, &fakeUserData{progress: []any{}, bookmarks: []any{}})

	before, err := h.store.ListABSSessionsForUser("u1")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	for i := 0; i < 3; i++ {
		if w, _ := h.do(t, request{method: http.MethodPost, path: "/api/authorize",
			headers: map[string]string{"Authorization": "Bearer " + access}}); w.Code != http.StatusOK {
			t.Fatalf("call %d: got %d", i, w.Code)
		}
	}
	after, err := h.store.ListABSSessionsForUser("u1")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("sessions grew %d -> %d across 3 authorize calls; it must not mint sessions",
			len(before), len(after))
	}
}
