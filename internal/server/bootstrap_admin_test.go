// file: internal/server/bootstrap_admin_test.go
// version: 1.1.0
// guid: 7b2a193f-fbf5-4b5e-8135-6f8d8fc03b4b
// last-edited: 2026-10-20

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func newBootstrapAdminStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, _, err := auth.SeedRoles(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Nit 6: bootstrap must end with an ACTIVE admin it can mint a key for. A
// locked "admin" account (holding admin@localhost) used to be returned as
// the admin, so the key it minted was refused; and creating a fresh one
// collided with that row's username and address.
func TestFindOrCreateAdminUser(t *testing.T) {
	t.Run("fresh store creates admin", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		u, pw, err := findOrCreateAdminUser(s)
		if err != nil || u == nil || pw == "" || u.Username != "admin" || u.Status != "active" {
			t.Fatalf("got %+v, pw set=%v, %v", u, pw != "", err)
		}
	})
	t.Run("an active admin is reused", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		owner, err := s.CreateUser("owner", "owner@example.com", "bcrypt", "h", []string{"admin"}, "active")
		if err != nil {
			t.Fatal(err)
		}
		u, pw, err := findOrCreateAdminUser(s)
		if err != nil || u == nil || u.ID != owner.ID || pw != "" {
			t.Fatalf("got %+v, %v; want the existing admin", u, err)
		}
	})
	t.Run("a locked admin holding admin@localhost is skipped", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		old, err := s.CreateUser("admin", "admin@localhost", "bcrypt", "h", []string{"admin"}, "active")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetUserStatus(old.ID, "locked"); err != nil {
			t.Fatal(err)
		}
		u, pw, err := findOrCreateAdminUser(s)
		if err != nil || u == nil {
			t.Fatalf("bootstrap refused: %v", err)
		}
		if u.ID == old.ID || u.Status != "active" || pw == "" {
			t.Fatalf("got %+v; want a new active admin, not the locked one", u)
		}
		if again, _, err := findOrCreateAdminUser(s); err != nil || again.ID != u.ID {
			t.Fatalf("a second bootstrap must reuse the new admin: %+v, %v", again, err)
		}
	})
	t.Run("a non-admin named admin does not block", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		if _, err := s.CreateUser("admin", "admin@localhost", "bcrypt", "h", []string{"viewer"}, "active"); err != nil {
			t.Fatal(err)
		}
		u, _, err := findOrCreateAdminUser(s)
		if err != nil || u == nil || u.Username == "admin" || u.Status != "active" {
			t.Fatalf("got %+v, %v; want a new admin under a free name", u, err)
		}
	})
}

var bootstrapTestClient atomic.Int32

// postBootstrap runs handleBootstrap once against srv with body as JSON.
func postBootstrap(t *testing.T, srv *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	// A distinct client per call keeps the endpoint's own attempt limiter
	// (5 per hour per IP, package-global) out of the way.
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", bootstrapTestClient.Add(1)%250+1)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	srv.handleBootstrap(c)
	return w
}

// sandboxBootstrapStateDir points the credential directory at a temp dir so
// the handler never touches a real token file.
func sandboxBootstrapStateDir(t *testing.T) {
	t.Helper()
	config.ResetStateDirForTest()
	t.Cleanup(config.ResetStateDirForTest)
	t.Setenv(config.SecureStateDirEnv, t.TempDir())
}

func countUsers(t *testing.T, s *database.PebbleStore) int {
	t.Helper()
	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	return len(users)
}

// Security review 2026-10-20: the admin used to be found or created BEFORE
// the token was checked, so a POST with any non-empty token created an admin.
// Nothing may be created without the live token, and the live token creates
// the admin exactly once.
func TestHandleBootstrap_CreatesNothingWithoutAValidToken(t *testing.T) {
	sandboxBootstrapStateDir(t)
	s := newBootstrapAdminStore(t)
	srv := &Server{store: s}
	seedBootstrapToken(s, "abbs_live")

	for name, body := range map[string]any{
		"no token":    map[string]string{},
		"blank token": map[string]string{"token": "   "},
		"wrong token": map[string]string{"token": "abbs_wrong"},
	} {
		w := postBootstrap(t, srv, body)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 400 or 401; body %s", name, w.Code, w.Body.String())
		}
		if n := countUsers(t, s); n != 0 {
			t.Fatalf("%s: %d user(s) created without a valid token", name, n)
		}
	}
	// The wrong attempts did not burn the live token.
	if _, err := s.GetSetting(bootstrapTokenKey); err != nil {
		t.Fatalf("a rejected attempt consumed the live token: %v", err)
	}

	w := postBootstrap(t, srv, map[string]string{"token": "abbs_live"})
	if w.Code != http.StatusOK {
		t.Fatalf("valid token: status %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Username          string `json:"username"`
			GeneratedPassword string `json:"generated_password"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Username != "admin" || resp.Data.GeneratedPassword == "" {
		t.Fatalf("got username %q, password set %v; want a new admin with a password", resp.Data.Username, resp.Data.GeneratedPassword != "")
	}
	if n := countUsers(t, s); n != 1 {
		t.Fatalf("%d users after one valid exchange, want 1", n)
	}

	// Replaying the spent token creates nothing more.
	if w := postBootstrap(t, srv, map[string]string{"token": "abbs_live"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed token: status %d, want 401", w.Code)
	}
	if n := countUsers(t, s); n != 1 {
		t.Fatalf("%d users after a replay, want 1", n)
	}
}

// An expired token, or one with no stored expiry, creates nothing.
func TestHandleBootstrap_ExpiredOrUndatedTokenCreatesNothing(t *testing.T) {
	sandboxBootstrapStateDir(t)
	for name, exp := range map[string]string{
		"expired":    fmt.Sprintf("%d", time.Now().Add(-time.Minute).Unix()),
		"no expiry":  "",
		"unreadable": "soon",
	} {
		s := newBootstrapAdminStore(t)
		_ = s.SetSetting(bootstrapTokenKey, hashBootstrapToken("abbs_live"), "string", false)
		if exp != "" {
			_ = s.SetSetting(bootstrapExpiresKey, exp, "string", false)
		}
		w := postBootstrap(t, &Server{store: s}, map[string]string{"token": "abbs_live"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401", name, w.Code)
		}
		if n := countUsers(t, s); n != 0 {
			t.Fatalf("%s: %d user(s) created", name, n)
		}
	}
}

// failingDeleteStore refuses to delete the token hash.
type failingDeleteStore struct{ *fakeSettingsStore }

func (f failingDeleteStore) DeleteSetting(key string) error {
	if key == bootstrapTokenKey {
		return errors.New("disk full")
	}
	return f.fakeSettingsStore.DeleteSetting(key)
}

func TestRedeemBootstrapToken_Steps(t *testing.T) {
	t.Run("onValid does not run for a wrong token", func(t *testing.T) {
		st := newFakeSettingsStore()
		seedBootstrapToken(st, "abbs_live")
		ran := false
		valid, err := RedeemBootstrapToken(st, t.TempDir(), "abbs_wrong", func() error { ran = true; return nil })
		if valid || err != nil || ran {
			t.Fatalf("valid=%v err=%v ran=%v; want false, nil, false", valid, err, ran)
		}
	})
	t.Run("a failed onValid keeps the token", func(t *testing.T) {
		st := newFakeSettingsStore()
		seedBootstrapToken(st, "abbs_live")
		boom := errors.New("create failed")
		valid, err := RedeemBootstrapToken(st, t.TempDir(), "abbs_live", func() error { return boom })
		if !valid || !errors.Is(err, boom) {
			t.Fatalf("valid=%v err=%v; want true, the onValid error", valid, err)
		}
		if _, err := st.GetSetting(bootstrapTokenKey); err != nil {
			t.Fatal("the token was consumed although onValid failed")
		}
		if valid, err := RedeemBootstrapToken(st, t.TempDir(), "abbs_live", nil); !valid || err != nil {
			t.Fatalf("retry: valid=%v err=%v", valid, err)
		}
	})
	t.Run("a token that cannot be deleted is an error", func(t *testing.T) {
		st := failingDeleteStore{newFakeSettingsStore()}
		seedBootstrapToken(st, "abbs_live")
		_, err := RedeemBootstrapToken(st, t.TempDir(), "abbs_live", nil)
		if !errors.Is(err, errBootstrapTokenNotBurned) {
			t.Fatalf("err = %v, want errBootstrapTokenNotBurned", err)
		}
	})
	t.Run("an expired token is cleared", func(t *testing.T) {
		st := newFakeSettingsStore()
		dir := t.TempDir()
		_ = st.SetSetting(bootstrapTokenKey, hashBootstrapToken("abbs_live"), "string", false)
		_ = st.SetSetting(bootstrapExpiresKey, fmt.Sprintf("%d", time.Now().Add(-time.Second).Unix()), "string", false)
		_ = os.WriteFile(BootstrapTokenPath(dir), []byte("abbs_live\n"), 0o600)
		if valid, err := RedeemBootstrapToken(st, dir, "abbs_live", nil); valid || err != nil {
			t.Fatalf("valid=%v err=%v", valid, err)
		}
		if _, err := st.GetSetting(bootstrapTokenKey); err == nil {
			t.Fatal("expired token hash left in place")
		}
		if _, err := os.Stat(BootstrapTokenPath(dir)); !os.IsNotExist(err) {
			t.Fatal("expired token file left in place")
		}
	})
}

// The generated admin password carries at least 128 bits from crypto/rand,
// drawn uniformly from its alphabet.
func TestGenerateAdminPassword(t *testing.T) {
	if len(adminPasswordAlphabet) != 32 {
		t.Fatalf("alphabet has %d symbols; the v&31 mapping is unbiased only for 32", len(adminPasswordAlphabet))
	}
	if adminPasswordBits < adminPasswordMinBits {
		t.Fatalf("%d bits, want at least %d", adminPasswordBits, adminPasswordMinBits)
	}
	symbols := adminPasswordGroups * adminPasswordGroupLen
	if got := float64(symbols) * math.Log2(float64(len(adminPasswordAlphabet))); got < adminPasswordMinBits {
		t.Fatalf("entropy %.1f bits, want at least %d", got, adminPasswordMinBits)
	}
	wantLen := symbols + adminPasswordGroups - 1

	const samples = 4000
	counts := map[rune]int{}
	seen := map[string]bool{}
	for range samples {
		pw, err := generateAdminPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != wantLen {
			t.Fatalf("len(%q) = %d, want %d", pw, len(pw), wantLen)
		}
		groups := strings.Split(pw, "-")
		if len(groups) != adminPasswordGroups {
			t.Fatalf("%q has %d groups, want %d", pw, len(groups), adminPasswordGroups)
		}
		for _, g := range groups {
			if len(g) != adminPasswordGroupLen {
				t.Fatalf("%q: group %q has length %d", pw, g, len(g))
			}
			for _, r := range g {
				if !strings.ContainsRune(adminPasswordAlphabet, r) {
					t.Fatalf("%q: symbol %q not in the alphabet", pw, r)
				}
				counts[r]++
			}
		}
		if seen[pw] {
			t.Fatalf("duplicate password %q in %d samples", pw, samples)
		}
		seen[pw] = true
	}
	// Uniformity: every symbol appears, and a chi-square over 32 buckets
	// (31 degrees of freedom) stays under 70, which a uniform source exceeds
	// with probability about 1 in 10,000.
	total := float64(samples * symbols)
	expected := total / 32
	chi := 0.0
	for _, r := range adminPasswordAlphabet {
		d := float64(counts[r]) - expected
		chi += d * d / expected
	}
	if len(counts) != 32 || chi > 70 {
		t.Fatalf("symbol distribution not uniform: %d symbols seen, chi-square %.1f", len(counts), chi)
	}
}
