// file: internal/books/identity_abs_mck_boot_test.go
// version: 0.1.0
// guid: 4e8b1c7d-92a6-4f30-b5d1-6a3f0e9c28b4
// last-edited: 2026-11-22

package books

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// A key minted after the flip on the ABS surface (F4-08 B10z). HEAD's ABS
// surface took every API key, and every key HEAD minted was "abk_". Under the
// flip a new key is core's "mck_" and an M3-imported one keeps "abk_"; until
// B10z the surface's key gate read "abk_" only, so an mck_ key that core's
// /api/v1 admitted was handed to the ABS access-token parser and refused as
// token-invalid. These cells hold an mck_ key to HEAD's verdicts for a key on
// the ABS surface, beside an M3-imported abk_ key of the same boot, and check
// the key is still judged by core (Compat.AuthenticateKey, CheckCredentialFor)
// and never parsed as an ABS JWT.

// absAttempts captures the ABS surface's auth-attempt records for the rest of
// the test. The log is a process-wide value; the test is not parallel.
type absAttempts struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (a *absAttempts) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buf.Write(p)
}

// since returns the records written after mark, and the new mark.
func (a *absAttempts) since(mark int) ([]map[string]any, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw := a.buf.Bytes()[mark:]
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, a.buf.Len()
}

func (a *absAttempts) mark() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buf.Len()
}

func captureABSAttempts(t *testing.T) *absAttempts {
	t.Helper()
	a := &absAttempts{}
	prev := absauth.AttemptLog.Log
	absauth.AttemptLog.Log = slog.New(slog.NewJSONHandler(a, nil))
	t.Cleanup(func() { absauth.AttemptLog.Log = prev })
	return a
}

// refusedAs asserts that the requests since mark recorded exactly one ABS
// refusal, with the mode and reason given.
func refusedAs(t *testing.T, a *absAttempts, mark int, mode, reason string) {
	t.Helper()
	recs, _ := a.since(mark)
	var got []string
	for _, r := range recs {
		if r["outcome"] == "success" {
			continue
		}
		got = append(got, r["mode"].(string)+"/"+r["reason"].(string))
	}
	require.Equal(t, []string{mode + "/" + reason}, got, "the ABS surface's audited refusal")
}

// absToken is GET /api/me's legacy token field for the key raw.
func absToken(t *testing.T, r *runningApp, raw string) string {
	t.Helper()
	rec := withKey(r, http.MethodGet, "/api/me", raw)
	require.Equal(t, http.StatusOK, rec.Code, bodyOf(rec))
	var body struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), bodyOf(rec))
	return body.Token
}

// TestABSKeys_AKeyMintedAfterTheFlipIsAKeyOnTheABSSurface is one boot over a
// HEAD store (M3 imports its keys) with the ABS surface on in its token mode.
// bruce (admin) mints mck_ keys through core's route.
func TestABSKeys_AKeyMintedAfterTheFlipIsAKeyOnTheABSSurface(t *testing.T) {
	var h headKeys
	fx := newFlipFixture(t, seedHEAD(t, &h), "")
	r := bootOverHEADStore(t, fx)
	bruce := loginCoreAdmin(t, r, wayne, h.password)
	log := captureABSAttempts(t)

	// The same reach as the imported one-atom key h.limited (HEAD's
	// "library.view" on bruce's key).
	viewKey, _ := bruce.mintKey("batarang", []string{"books:view", "core:tasks.view"})
	require.True(t, strings.HasPrefix(viewKey, "mck_"))

	t.Run("an mck_ key is admitted on /api/v1 and on ABS by Bearer and ?token=, as HEAD admitted a key", func(t *testing.T) {
		for name, raw := range map[string]string{"imported abk_": h.limited.raw, "minted mck_": viewKey} {
			require.Equalf(t, http.StatusOK, withKey(r, http.MethodGet, "/api/v1/auth/me", raw).Code, "%s on /api/v1", name)
			rec := withKey(r, http.MethodGet, "/api/me", raw)
			require.Equalf(t, http.StatusOK, rec.Code, "%s by Bearer on ABS: %s", name, bodyOf(rec))
			rec = coreDo(t, r, http.MethodGet, "/api/me?token="+raw, nil, nil, "")
			require.Equalf(t, http.StatusOK, rec.Code, "%s by ?token= on ABS: %s", name, bodyOf(rec))
		}
		// The ABS identity is the key's: the surface records key mode.
		mark := log.mark()
		require.Equal(t, http.StatusOK, withKey(r, http.MethodGet, "/api/me", viewKey).Code)
		recs, _ := log.since(mark)
		for _, rec := range recs {
			require.NotEqual(t, servermiddleware.ABSModeJWT, rec["mode"], "an mck_ key was judged as an ABS access token: %v", rec)
		}
	})

	t.Run("an mck_ key is never parsed as an ABS JWT", func(t *testing.T) {
		// GET /api/me echoes the caller's ABS access token and otherwise mints
		// one; HEAD never echoed a key there. An mck_ key read as an access
		// token would come back verbatim.
		for name, raw := range map[string]string{"imported abk_": h.limited.raw, "minted mck_": viewKey} {
			tok := absToken(t, r, raw)
			require.NotEqualf(t, raw, tok, "%s: GET /api/me echoed the key as the caller's ABS access token", name)
			require.Falsef(t, strings.HasPrefix(tok, "mck_") || strings.HasPrefix(tok, "abk_"), "%s: the token field carries a key: %s", name, tok)
		}
	})

	t.Run("an fp-worker-confined mck_ key is refused on ABS by core's credential check", func(t *testing.T) {
		worker, _ := bruce.mintKey("riddler-worker", []string{"books:fingerprint.worker"})
		mark := log.mark()
		rec := withKey(r, http.MethodGet, "/api/me", worker)
		// HEAD refused a worker-only key on the ABS surface with 403.
		require.Equal(t, http.StatusForbidden, rec.Code, bodyOf(rec))
		refusedAs(t, log, mark, servermiddleware.ABSModeAPIKey, "confined-credential")
	})

	t.Run("a revoked mck_ key is refused", func(t *testing.T) {
		raw, id := bruce.mintKey("stolen-batarang", []string{"books:view", "core:tasks.view"})
		require.Equal(t, http.StatusOK, withKey(r, http.MethodGet, "/api/me", raw).Code)
		del := coreDo(t, r, http.MethodDelete, "/api/core/v1/me/api-keys/"+id, nil, bruce.cookie, "")
		require.Equal(t, http.StatusNoContent, del.Code, bodyOf(del))
		mark := log.mark()
		rec := withKey(r, http.MethodGet, "/api/me", raw)
		require.Equal(t, http.StatusUnauthorized, rec.Code, bodyOf(rec))
		refusedAs(t, log, mark, servermiddleware.ABSModeAPIKey, "credential-revoked")
		require.Equal(t, http.StatusUnauthorized, withKey(r, http.MethodGet, "/api/v1/auth/me", raw).Code)
	})

	t.Run("an expired mck_ key is refused", func(t *testing.T) {
		rec := coreDo(t, r, http.MethodPost, "/api/core/v1/me/api-keys",
			map[string]any{"name": "old-batmobile-2", "scopes": []string{"books:view", "core:tasks.view"}, "expires_in": "1s"}, bruce.cookie, "")
		require.Equal(t, http.StatusCreated, rec.Code, bodyOf(rec))
		var res struct {
			Key string `json:"key"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		time.Sleep(1500 * time.Millisecond)
		mark := log.mark()
		got := withKey(r, http.MethodGet, "/api/me", res.Key)
		require.Equal(t, http.StatusUnauthorized, got.Code, bodyOf(got))
		refusedAs(t, log, mark, servermiddleware.ABSModeAPIKey, "credential-expired")
		require.Equal(t, http.StatusUnauthorized, withKey(r, http.MethodGet, "/api/v1/auth/me", res.Key).Code)
	})
}
