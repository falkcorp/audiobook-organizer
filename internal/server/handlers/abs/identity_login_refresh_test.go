// file: internal/server/handlers/abs/identity_login_refresh_test.go
// version: 0.1.0
// guid: 91c4e7a2-5d38-4b06-8f1a-2e7b9d3c6a40
// last-edited: 2026-11-21

package abs_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// F4-08 B10b1fu: under core identity ABS password login and refresh judge the
// account (password, epoch, status) from core, never from books' store. Every
// fixture here gives core its own record of the account that differs from
// books' row (or leaves books without one, the shape after M3), so a handler
// that read books answers the opposite of core.

const (
	batmanBooksPassword = "pw-books-wayne"
	batmanCorePassword  = "pw-core-wayne"
)

var batmanCreated = time.UnixMilli(1785370133398)

// coreOnly gives core a record of one account and returns the compat. The
// account has no books row at all when withBooksRow is false (post-M3).
func coreOnly(t *testing.T, withBooksRow bool, epoch int64) (*harness, *coreLikeCompat) {
	t.Helper()
	h := newHarness(t, "cf,jwt", nil)
	if withBooksRow {
		h.seedPasswordUser(t, "u1", "bruce", batmanBooksPassword)
	}
	compat := h.underCoreIdentity(t)
	compat.core = map[string]*coreRecord{"u1": {
		status: "active", epoch: epoch, username: "bruce", password: batmanCorePassword,
		email: "bruce@example.com", created: batmanCreated,
	}}
	return h, compat
}

func (h *harness) postLogin(t *testing.T, username, password string) (int, map[string]any) {
	t.Helper()
	w, body := h.do(t, request{
		method: http.MethodPost, path: "/login",
		body: map[string]any{"username": username, "password": password},
	})
	return w.Code, body
}

func (h *harness) postRefresh(t *testing.T, token string) (int, map[string]any) {
	t.Helper()
	w, body := h.do(t, request{
		method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{"x-refresh-token": token},
	})
	return w.Code, body
}

// loginOK logs in through core and returns the refresh token.
func (h *harness) loginOK(t *testing.T, username, password string) (token string, body map[string]any) {
	t.Helper()
	code, body := h.postLogin(t, username, password)
	if code != http.StatusOK {
		t.Fatalf("precondition: login = %d %v, want 200", code, body)
	}
	return str(t, userObj(t, body), "refreshToken"), body
}

func (h *harness) userReadCount() int {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return h.store.userReads
}

func (h *harness) userWrites() int {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return len(h.store.updatedUsers)
}

func (h *harness) sessionGeneration(t *testing.T) int {
	t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.sessions) != 1 {
		t.Fatalf("precondition: %d sessions, want 1", len(h.store.sessions))
	}
	for _, s := range h.store.sessions {
		return s.Generation
	}
	return 0
}

// ── login ───────────────────────────────────────────────────────────────────

// The password is core's: the one core holds logs in, the one books holds does
// not, and books' store is neither read nor written.
func TestIdentityLogin_PasswordIsCores(t *testing.T) {
	h, compat := coreOnly(t, true, 4)
	reads := h.userReadCount()

	if code, _ := h.postLogin(t, "bruce", batmanBooksPassword); code != http.StatusUnauthorized {
		t.Fatalf("books' password under core identity = %d, want 401", code)
	}
	if got := h.sessionCount(); got != 0 {
		t.Fatalf("a refused login created %d sessions", got)
	}
	code, body := h.postLogin(t, "bruce", batmanCorePassword)
	if code != http.StatusOK {
		t.Fatalf("core's password = %d %v, want 200", code, body)
	}
	u := userObj(t, body)
	if u["id"] != "u1" || u["username"] != "bruce" || u["email"] != "bruce@example.com" {
		t.Fatalf("response user is not core's record: %v", u)
	}
	if u["createdAt"] != float64(batmanCreated.UnixMilli()) || u["isActive"] != true {
		t.Fatalf("response user does not carry core's creation time and standing: %v", u)
	}
	if got := h.sessionEpochs(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("session epochs %v, want [4]: the stamp is core's epoch", got)
	}
	if got := h.userReadCount(); got != reads {
		t.Fatalf("login read books' user rows %d times", got-reads)
	}
	if got := h.userWrites(); got != 0 {
		t.Fatalf("login wrote books' user rows %d times (a rehash is core's)", got)
	}
	if len(compat.verified()) != 2 {
		t.Fatalf("core verified %d passwords, want 2", len(compat.verified()))
	}
}

// A user books has no row for (the shape after M3) logs in, and its session
// then refreshes.
func TestIdentityLogin_UserAbsentFromBooksLogsInAndRefreshes(t *testing.T) {
	h, _ := coreOnly(t, false, 2)
	if h.userCount() != 0 {
		t.Fatal("precondition: books holds no users")
	}
	refresh, _ := h.loginOK(t, "bruce", batmanCorePassword)

	code, body := h.postRefresh(t, refresh)
	if code != http.StatusOK {
		t.Fatalf("refresh of a user books lacks = %d %v, want 200", code, body)
	}
	if u := userObj(t, body); u["id"] != "u1" || u["username"] != "bruce" {
		t.Fatalf("refresh user is not core's: %v", u)
	}
	if got := h.userReadCount(); got != 0 {
		t.Fatalf("login+refresh read books' user rows %d times", got)
	}
}

// Core disables the account (books still says active): a correct password is
// 403 and charges nothing, and no session is minted.
func TestIdentityLogin_DisabledInCoreIs403AndUncharged(t *testing.T) {
	h, compat := coreOnly(t, true, 0)
	compat.setCore("u1", func(r *coreRecord) { r.status = "disabled" })
	for range coreauth.DefaultMaxFailuresPerSource + 2 {
		code, body := h.postLogin(t, "bruce", batmanCorePassword)
		if code != http.StatusForbidden || !strings.Contains(fmt.Sprint(body["error"]), "not active") {
			t.Fatalf("disabled in core = %d %v, want 403 account is not active (never 429: not a credential failure)", code, body)
		}
	}
	if got := h.sessionCount(); got != 0 {
		t.Fatalf("a refused login created %d sessions", got)
	}
}

// Books disables or moves the account but core does not: still admitted.
func TestIdentityLogin_BooksRowIsNotConsulted(t *testing.T) {
	h, _ := coreOnly(t, true, 0)
	h.store.mu.Lock()
	h.store.users["u1"].Status = "locked"
	h.store.users["u1"].CredentialEpoch = 9
	h.store.mu.Unlock()
	h.loginOK(t, "bruce", batmanCorePassword)
	if got := h.sessionEpochs(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("session epochs %v, want [0] (core's epoch, not books' 9)", got)
	}
}

// Sign-in restricted in core is 403, an unknown name is 401 like a wrong
// password.
func TestIdentityLogin_ResetRequiredAndUnknownName(t *testing.T) {
	h, compat := coreOnly(t, true, 0)
	compat.setCore("u1", func(r *coreRecord) { r.resetRequired = true })
	if code, _ := h.postLogin(t, "bruce", batmanCorePassword); code != http.StatusForbidden {
		t.Fatalf("reset required = %d, want 403", code)
	}
	wrong, wrongBody := h.postLogin(t, "bruce", "not-the-password")
	unknown, unknownBody := h.postLogin(t, "alfred", batmanCorePassword)
	if wrong != http.StatusUnauthorized || unknown != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, unknown name = %d, want 401 both", wrong, unknown)
	}
	if fmt.Sprint(wrongBody) != fmt.Sprint(unknownBody) {
		t.Fatalf("unknown name answers %v, wrong password %v: the body must not tell them apart", unknownBody, wrongBody)
	}
	if got := h.sessionCount(); got != 0 {
		t.Fatalf("refused logins created %d sessions", got)
	}
}

// Core's verdicts keep HEAD's statuses and a transient failure never reads as
// a dead credential. A bad password is charged to the handler's own failure
// throttle (the budget then answers 429); no other refusal is.
func TestIdentityLogin_CoreVerdictsMapToStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		status  int
		retry   bool
		charged bool
	}{
		"invalid credentials": {coreauth.ErrInvalidCredentials, http.StatusUnauthorized, false, true},
		"inactive":            {coreauth.ErrInactiveUser, http.StatusForbidden, false, false},
		"reset required":      {coreauth.ErrResetRequired, http.StatusForbidden, false, false},
		"throttled":           {coreauth.ErrThrottled, http.StatusTooManyRequests, true, false},
		"hashing busy":        {coreauth.ErrBusy, http.StatusServiceUnavailable, true, false},
		"store unavailable":   {coreauth.ErrStoreUnavailable, http.StatusInternalServerError, false, false},
		"unclassified":        {errors.New("batcave fire"), http.StatusInternalServerError, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, compat := coreOnly(t, true, 0)
			compat.verifyFailure = fmt.Errorf("core: %w", tc.err)
			for range coreauth.DefaultMaxFailuresPerSource {
				code, _ := h.postLogin(t, "bruce", batmanCorePassword)
				if code != tc.status && !(tc.charged && code == http.StatusTooManyRequests) {
					t.Fatalf("status = %d, want %d", code, tc.status)
				}
			}
			w, _ := h.do(t, request{
				method: http.MethodPost, path: "/login",
				body: map[string]any{"username": "bruce", "password": batmanCorePassword},
			})
			if tc.charged {
				if w.Code != http.StatusTooManyRequests {
					t.Fatalf("after %d bad passwords = %d, want the handler's 429", coreauth.DefaultMaxFailuresPerSource, w.Code)
				}
			} else if w.Code != tc.status {
				t.Fatalf("an uncharged refusal repeated = %d, want %d still", w.Code, tc.status)
			}
			if tc.retry && w.Code == tc.status && w.Header().Get("Retry-After") == "" {
				t.Fatal("a throttled or busy refusal must carry Retry-After")
			}
			if got := h.sessionCount(); got != 0 {
				t.Fatalf("a refused login created %d sessions", got)
			}
		})
	}
}

// A nil user with no error is a failure, never a session for nobody.
func TestIdentityLogin_CoreAnswersNoUserIs500(t *testing.T) {
	h, compat := coreOnly(t, true, 0)
	compat.verifyNil = true
	if code, _ := h.postLogin(t, "bruce", batmanCorePassword); code != http.StatusInternalServerError {
		t.Fatalf("a nil user from core = %d, want 500", code)
	}
	if got := h.sessionCount(); got != 0 {
		t.Fatalf("created %d sessions", got)
	}
}

// Core's throttle keys on the caller as the mount resolved it.
func TestIdentityLogin_CoreIsAskedWithTheIdentitysClient(t *testing.T) {
	h, compat := coreOnly(t, true, 0)
	h.loginOK(t, "bruce", batmanCorePassword)
	calls := compat.verified()
	if len(calls) != 1 || calls[0].client.UserAgent != "batarang" || calls[0].username != "bruce" || calls[0].password != batmanCorePassword {
		t.Fatalf("core was asked %+v, want one call with the identity's client and the submitted credentials", calls)
	}
}

// Without an identity (today's prod path) login reads books' own row: its
// hash decides, its epoch stamps the session, its rehash write happens, and
// no compat handle exists to be asked.
func TestIdentityLogin_WithoutAnIdentityBooksDecides(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "bruce", batmanBooksPassword)
	h.store.mu.Lock()
	h.store.users["u1"].CredentialEpoch = 6
	h.store.mu.Unlock()
	reads := h.userReadCount()

	h.loginOK(t, "bruce", batmanBooksPassword)
	if h.userReadCount() == reads {
		t.Fatal("HEAD's login did not read books' user row")
	}
	if got := h.sessionEpochs(); len(got) != 1 || got[0] != 6 {
		t.Fatalf("session epochs %v, want [6] (books' epoch)", got)
	}
	if code, _ := h.postLogin(t, "bruce", batmanCorePassword); code != http.StatusUnauthorized {
		t.Fatalf("a password books does not hold = %d, want 401", code)
	}
}

// ── refresh ─────────────────────────────────────────────────────────────────

// Core's epoch moves after the session was minted: refused 401, the session
// is not rotated. Books' own epoch is not what is compared.
func TestIdentityRefresh_EpochIsCores(t *testing.T) {
	h, compat := coreOnly(t, true, 3)
	refresh, _ := h.loginOK(t, "bruce", batmanCorePassword)
	compat.setCore("u1", func(r *coreRecord) { r.epoch++ })
	gen := h.sessionGeneration(t)

	code, body := h.postRefresh(t, refresh)
	if code != http.StatusUnauthorized || !strings.Contains(fmt.Sprint(body["error"]), "Session not found") {
		t.Fatalf("refresh after core's epoch moved = %d %v, want 401 Session not found", code, body)
	}
	if got := h.sessionGeneration(t); got != gen {
		t.Fatalf("a refused refresh rotated the session (%d -> %d)", gen, got)
	}
	if calls := compat.recorded(); len(calls) != 1 || calls[0].epoch != 3 || calls[0].user != "u1" || calls[0].client.UserAgent != "batarang" {
		t.Fatalf("core was asked %+v, want one call for u1 at the session's epoch 3 with the identity's client", calls)
	}
}

// Core disables the account: 403, no rotation.
func TestIdentityRefresh_StatusIsCores(t *testing.T) {
	h, compat := coreOnly(t, true, 0)
	refresh, _ := h.loginOK(t, "bruce", batmanCorePassword)
	compat.setCore("u1", func(r *coreRecord) { r.status = "disabled" })
	gen := h.sessionGeneration(t)

	code, body := h.postRefresh(t, refresh)
	if code != http.StatusForbidden || !strings.Contains(fmt.Sprint(body["error"]), "not active") {
		t.Fatalf("refresh for a user core disabled = %d %v, want 403", code, body)
	}
	if got := h.sessionGeneration(t); got != gen {
		t.Fatalf("a refused refresh rotated the session (%d -> %d)", gen, got)
	}
}

// Books' row moves, core's does not: admitted, and the session rotates.
func TestIdentityRefresh_BooksRowIsNotConsulted(t *testing.T) {
	h, _ := coreOnly(t, true, 3)
	refresh, _ := h.loginOK(t, "bruce", batmanCorePassword)
	h.store.mu.Lock()
	h.store.users["u1"].Status = "locked"
	h.store.users["u1"].CredentialEpoch = 40
	h.store.mu.Unlock()
	reads := h.userReadCount()
	gen := h.sessionGeneration(t)

	if code, body := h.postRefresh(t, refresh); code != http.StatusOK {
		t.Fatalf("refresh = %d %v, want 200: books' row is not the account's record under core identity", code, body)
	}
	if got := h.sessionGeneration(t); got != gen+1 {
		t.Fatalf("session generation %d -> %d, want one rotation", gen, got)
	}
	if got := h.userReadCount(); got != reads {
		t.Fatalf("refresh read books' user rows %d times", got-reads)
	}
}

// Every core refusal keeps a client-safe status and writes nothing. A user core
// cannot find is ErrInactiveUser from the real compat (403, HEAD answered 401
// session-user-missing); the fake's own answer for a missing record is
// ErrInvalidCredentials (401); both are pinned. The default is a 5xx, never a
// 401: a transient failure must not log a phone out.
func TestIdentityRefresh_CoreRefusalsMapToStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		retry  bool
	}{
		"stale epoch":         {coreauth.ErrStaleEpoch, http.StatusUnauthorized, false},
		"no such user":        {coreauth.ErrInvalidCredentials, http.StatusUnauthorized, false},
		"inactive":            {coreauth.ErrInactiveUser, http.StatusForbidden, false},
		"reset required":      {coreauth.ErrResetRequired, http.StatusForbidden, false},
		"throttled":           {coreauth.ErrThrottled, http.StatusTooManyRequests, true},
		"store unavailable":   {coreauth.ErrStoreUnavailable, http.StatusInternalServerError, false},
		"an unclassified one": {errors.New("batcave fire"), http.StatusInternalServerError, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, compat := coreOnly(t, true, 0)
			refresh, _ := h.loginOK(t, "bruce", batmanCorePassword)
			compat.failure = fmt.Errorf("core: %w", tc.err)
			gen := h.sessionGeneration(t)

			w, _ := h.do(t, request{
				method: http.MethodPost, path: "/auth/refresh",
				headers: map[string]string{"x-refresh-token": refresh},
			})
			if w.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if tc.retry && w.Header().Get("Retry-After") == "" {
				t.Fatal("a throttled refusal must carry Retry-After")
			}
			if got := h.sessionGeneration(t); got != gen {
				t.Fatalf("a refused refresh rotated the session (%d -> %d)", gen, got)
			}
		})
	}
}

// Core's refusal of a stale epoch charges the refresh failure budget as HEAD
// does; its 5xx does not.
func TestIdentityRefresh_StaleEpochChargesTheBudgetButAStoreErrorDoesNot(t *testing.T) {
	h, compat := coreOnly(t, true, 0)
	refresh, _ := h.loginOK(t, "bruce", batmanCorePassword)

	compat.failure = coreauth.ErrStoreUnavailable
	for range coreauth.DefaultMaxFailuresPerSource + 2 {
		if code, _ := h.postRefresh(t, refresh); code != http.StatusInternalServerError {
			t.Fatalf("store down = %d, want 500 every time (never charged)", code)
		}
	}
	compat.failure = coreauth.ErrStaleEpoch
	var last int
	for range coreauth.DefaultMaxFailuresPerSource + 1 {
		last, _ = h.postRefresh(t, refresh)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d stale-epoch refusals = %d, want the budget's 429", coreauth.DefaultMaxFailuresPerSource+1, last)
	}
}

// Without an identity refresh reads books' row: its epoch refuses a session,
// its status refuses with 403, and an intact row rotates.
func TestIdentityRefresh_WithoutAnIdentityBooksDecides(t *testing.T) {
	t.Run("books' epoch moves: 401 credentials-changed", func(t *testing.T) {
		h := newHarness(t, "cf,jwt", nil)
		h.seedPasswordUser(t, "u1", "bruce", batmanBooksPassword)
		refresh, _ := h.loginOK(t, "bruce", batmanBooksPassword)
		h.bumpAllCredentialEpochs()
		if code, _ := h.postRefresh(t, refresh); code != http.StatusUnauthorized {
			t.Fatalf("refresh after books' epoch moved = %d, want 401", code)
		}
	})
	t.Run("books' row inactive: 403", func(t *testing.T) {
		h := newHarness(t, "cf,jwt", nil)
		h.seedPasswordUser(t, "u1", "bruce", batmanBooksPassword)
		refresh, _ := h.loginOK(t, "bruce", batmanBooksPassword)
		h.store.mu.Lock()
		h.store.users["u1"].Status = "locked"
		h.store.mu.Unlock()
		if code, _ := h.postRefresh(t, refresh); code != http.StatusForbidden {
			t.Fatalf("refresh for a locked books row = %d, want 403", code)
		}
	})
	t.Run("books' row absent: 401 session-user-missing", func(t *testing.T) {
		h := newHarness(t, "cf,jwt", nil)
		h.seedPasswordUser(t, "u1", "bruce", batmanBooksPassword)
		refresh, _ := h.loginOK(t, "bruce", batmanBooksPassword)
		h.store.mu.Lock()
		delete(h.store.users, "u1")
		h.store.mu.Unlock()
		if code, _ := h.postRefresh(t, refresh); code != http.StatusUnauthorized {
			t.Fatalf("refresh for a user books no longer has = %d, want 401", code)
		}
	})
	t.Run("intact: rotates", func(t *testing.T) {
		h := newHarness(t, "cf,jwt", nil)
		h.seedPasswordUser(t, "u1", "bruce", batmanBooksPassword)
		refresh, _ := h.loginOK(t, "bruce", batmanBooksPassword)
		reads := h.userReadCount()
		if code, _ := h.postRefresh(t, refresh); code != http.StatusOK {
			t.Fatalf("refresh = %d, want 200", code)
		}
		if h.userReadCount() == reads {
			t.Fatal("HEAD's refresh did not read books' user row")
		}
	})
}
