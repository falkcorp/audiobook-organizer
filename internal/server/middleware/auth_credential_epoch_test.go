// file: internal/server/middleware/auth_credential_epoch_test.go
// version: 1.0.0
// guid: b905c23d-19d7-45cf-ba5f-e3b1513bd206
// last-edited: 2026-10-30

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func epochTestRouter(store database.Store) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

func pingWith(r *gin.Engine, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func credentialWriter(t *testing.T, store database.Store) database.UserCredentialWriter {
	t.Helper()
	w, ok := database.AsCapability[database.UserCredentialWriter](store)
	if !ok {
		t.Fatal("store has no UserCredentialWriter")
	}
	return w
}

// A session stamped with the epoch of an earlier credential state is refused on
// its next request, exactly like an expired session: revoked, the same 401 and
// the same body a client already handles. Nothing but the stamp comparison does
// this: the row is not revoked by the test before the request.
func TestRequireAuth_RefusesSessionFromBeforeACredentialChange(t *testing.T) {
	store, _, viewerToken := setupAuthTestStore(t)
	r := epochTestRouter(store)
	viewerSession, _ := store.GetSession(viewerToken)

	if w := pingWith(r, viewerToken); w.Code != http.StatusOK {
		t.Fatalf("precondition: a current session must authenticate, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := credentialWriter(t, store).BumpUserCredentialEpoch(viewerSession.UserID); err != nil {
		t.Fatal(err)
	}

	w := pingWith(r, viewerToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a session from before the credential change: status = %d, want 401: %s", w.Code, w.Body.String())
	}
	after, _ := store.GetSession(viewerToken)
	if after == nil || !after.Revoked {
		t.Error("the refused session must be revoked, as an expired one is")
	}

	// Indistinguishable from an expired session to the client.
	expired, err := store.CreateSession(viewerSession.UserID, "127.0.0.1", "test", -time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	we := pingWith(r, expired.ID)
	if we.Code != w.Code || we.Body.String() != w.Body.String() {
		t.Errorf("a refused epoch mismatch must look exactly like an expired session:\n  mismatch: %d %s\n  expired:  %d %s",
			w.Code, w.Body.String(), we.Code, we.Body.String())
	}
}

// A session carrying the user's current epoch authenticates; one carrying any
// other value, higher or lower, is refused (fails closed both ways).
func TestRequireAuth_EpochStampMustMatchExactly(t *testing.T) {
	store, _, viewerToken := setupAuthTestStore(t)
	r := epochTestRouter(store)
	userID := func() string { s, _ := store.GetSession(viewerToken); return s.UserID }()
	writer := credentialWriter(t, store)
	if _, err := writer.BumpUserCredentialEpoch(userID); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.BumpUserCredentialEpoch(userID); err != nil { // epoch is now 2
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		epoch int64
		want  int
	}{
		{"current epoch", 2, http.StatusOK},
		{"older epoch", 1, http.StatusUnauthorized},
		{"no stamp (reads as 0) for a user above 0", 0, http.StatusUnauthorized},
		{"newer epoch than the user has", 3, http.StatusUnauthorized},
	} {
		sess, err := store.CreateSession(userID, "127.0.0.1", "test", time.Hour, tc.epoch)
		if err != nil {
			t.Fatal(err)
		}
		if w := pingWith(r, sess.ID); w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d: %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

// Migration choice: a session with no stamp (every session issued before the
// stamp existed) is current for a user whose epoch is still 0, so deploying this
// signs nobody out who never changed their credentials.
func TestRequireAuth_UnstampedSessionSurvivesForAnUnchangedAccount(t *testing.T) {
	store, _, viewerToken := setupAuthTestStore(t) // sessions created with epoch 0, users at epoch 0
	if w := pingWith(epochTestRouter(store), viewerToken); w.Code != http.StatusOK {
		t.Fatalf("an unstamped session of a never-changed account must still work, got %d: %s", w.Code, w.Body.String())
	}
}

// Deactivation then reactivation: a session from before the deactivation does
// not come back to life when the account is unlocked, even though the account is
// active again and nothing revoked the row.
func TestRequireAuth_ReactivationDoesNotReviveAnOldSession(t *testing.T) {
	store, _, viewerToken := setupAuthTestStore(t)
	r := epochTestRouter(store)
	userID := func() string { s, _ := store.GetSession(viewerToken); return s.UserID }()
	writer := credentialWriter(t, store)
	if _, err := writer.SetUserStatus(userID, "locked"); err != nil {
		t.Fatal(err)
	}
	if w := pingWith(r, viewerToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("a locked account's session: %d, want 401", w.Code)
	}
	// Put the row back to un-revoked to prove the epoch, not the revoke, refuses it.
	revived, err := store.CreateSession(userID, "127.0.0.1", "test", time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SetUserStatus(userID, "active"); err != nil {
		t.Fatal(err)
	}
	if w := pingWith(r, revived.ID); w.Code != http.StatusUnauthorized {
		t.Fatalf("a pre-reactivation session must stay dead after the account is active again, got %d", w.Code)
	}
	fresh, err := store.CreateSession(userID, "127.0.0.1", "test", time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if w := pingWith(r, fresh.ID); w.Code != http.StatusOK {
		t.Fatalf("a sign-in after reactivation must work, got %d: %s", w.Code, w.Body.String())
	}
}
