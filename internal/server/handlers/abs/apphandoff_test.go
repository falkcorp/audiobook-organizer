// file: internal/server/handlers/abs/apphandoff_test.go
// version: 0.1.0
// guid: e41b7c08-3a95-4d2e-b6f1-0a8d52c97e34
// last-edited: 2026-11-20

package abs_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// Books' hand-off for core's OIDC callback (05-authn section 4.7), driven the way
// core drives it (Allowed, then Mint) and redeemed through HEAD's real
// redemption, the handler behind GET /auth/openid/callback. The store user is
// seeded at epoch 3 so a code stamped with a default epoch cannot redeem.

const (
	handoffRedirect = "audiobooth://oauth"
	handoffUserID   = "batman-01"
	handoffEpoch    = int64(3)
)

// handoffRig is an ABS harness holding one active user at handoffEpoch plus the
// matching core-side record core's callback would resolve.
type handoffRig struct {
	*harness
	core *coreauth.User
	hand coreauth.OIDCAppHandoff
}

func newHandoffRig(t *testing.T) *handoffRig {
	t.Helper()
	h := newOIDCHarness(t)
	h.seedUser(t, handoffUserID, "bruce", "bruce@example.com", "pw-pw-pw-pw-1")
	h.setUser(handoffUserID, func(u *database.User) { u.CredentialEpoch = handoffEpoch })
	return &handoffRig{
		harness: h,
		core: &coreauth.User{
			ID: handoffUserID, Username: "bruce", Email: "bruce@example.com",
			Status: coreauth.StatusActive, CredentialEpoch: handoffEpoch,
		},
		hand: abshandler.NewAppHandoff(),
	}
}

func (h *harness) setUser(id string, mutate func(*database.User)) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	mutate(h.store.users[id])
}

// mint returns the code the hand-off's location carries, and the location.
func (r *handoffRig) mint(t *testing.T, challenge, state string) (code string, location string) {
	t.Helper()
	loc, err := r.hand.Mint(context.Background(), r.core, "bruce@example.com", challenge, handoffRedirect, state)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("location %q does not parse: %v", loc, err)
	}
	return u.Query().Get("code"), loc
}

func TestAppHandoff_AllowedIsHEADsExactMatchList(t *testing.T) {
	h := abshandler.NewAppHandoff()
	for _, uri := range []string{handoffRedirect} {
		if !h.Allowed(uri) {
			t.Errorf("Allowed(%q) = false, want true", uri)
		}
	}
	for _, uri := range []string{
		"", "audiobooth://oauth.evil.example", "audiobooth://oauth/", "audiobooth://oauth?x=1",
		"audiobooth://robin@oauth", "AUDIOBOOTH://oauth", "https://evil.example/cb", "audiobooth://",
	} {
		if h.Allowed(uri) {
			t.Errorf("Allowed(%q) = true, want false (exact match only)", uri)
		}
	}
	// The operator's override replaces the default list, as it does for the
	// ABS openid route: the same list, not a second opinion.
	t.Setenv(abshandler.OIDCRedirectURIsEnvVar, "gotham://oauth")
	if !h.Allowed("gotham://oauth") || h.Allowed(handoffRedirect) {
		t.Fatalf("override: Allowed(gotham)=%v Allowed(default)=%v, want true false", h.Allowed("gotham://oauth"), h.Allowed(handoffRedirect))
	}
}

func TestAppHandoff_MintedCodeRedeemsOnceAtTheABSCallback(t *testing.T) {
	r := newHandoffRig(t)
	code, loc := r.mint(t, s256(oidcVerifier), "robin-state")
	if code == "" || !strings.HasPrefix(loc, handoffRedirect+"?") {
		t.Fatalf("location %q must be the app scheme carrying a code", loc)
	}
	if got := r.sessionCount(); got != 0 {
		t.Fatalf("minting created %d sessions, want 0", got)
	}

	w := r.redeem(t, code, oidcVerifier, "")
	if w.Code != http.StatusOK {
		t.Fatalf("first redemption = %d %s, want 200", w.Code, w.Body.String())
	}
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.User.ID != handoffUserID {
		t.Fatalf("session is for user %q (err %v), want %q", body.User.ID, err, handoffUserID)
	}
	if got := r.sessionCount(); got != 1 {
		t.Fatalf("sessions after redemption = %d, want 1", got)
	}

	w = r.redeem(t, code, oidcVerifier, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("second redemption = %d %s, want 401", w.Code, w.Body.String())
	}
	if got := r.sessionCount(); got != 1 {
		t.Fatalf("a second redemption created a session (%d)", got)
	}
}

func TestAppHandoff_LocationEchoesTheAppsState(t *testing.T) {
	r := newHandoffRig(t)
	_, loc := r.mint(t, s256(oidcVerifier), "joker & riddler=1")
	u, _ := url.Parse(loc)
	if got := u.Query().Get("state"); got != "joker & riddler=1" {
		t.Fatalf("state = %q, want the app's state echoed", got)
	}
	_, loc = r.mint(t, s256(oidcVerifier), "")
	if strings.Contains(loc, "state") {
		t.Fatalf("location %q carries a state the app never sent", loc)
	}
}

func TestAppHandoff_MintRefusesAnUnlistedRedirect(t *testing.T) {
	r := newHandoffRig(t)
	for _, uri := range []string{"https://evil.example/cb", "audiobooth://oauth.evil.example", ""} {
		loc, err := r.hand.Mint(context.Background(), r.core, "bruce@example.com", s256(oidcVerifier), uri, "s")
		if err == nil || loc != "" {
			t.Errorf("Mint(redirect %q) = %q, %v; want a refusal and no location", uri, loc, err)
		}
	}
}

func TestAppHandoff_MintRefusesAChallengeThatIsNotS256Shaped(t *testing.T) {
	r := newHandoffRig(t)
	for name, challenge := range map[string]string{
		"empty":           "",
		"short":           "plain",
		"plain verifier":  oidcVerifier, // 56 characters: a "plain" challenge is the verifier itself
		"44 characters":   s256(oidcVerifier) + "A",
		"bad alphabet":    strings.Repeat("a", 42) + "+",
		"padded base64":   s256(oidcVerifier)[:42] + "=",
		"whitespace only": strings.Repeat(" ", 43),
	} {
		loc, err := r.hand.Mint(context.Background(), r.core, "bruce@example.com", challenge, handoffRedirect, "s")
		if err == nil || loc != "" {
			t.Errorf("%s: Mint = %q, %v; want a refusal and no location", name, loc, err)
		}
	}
}

// The method is core's to refuse (Mint is never told it). What the hand-off
// guarantees is that redemption always hashes the verifier: a code minted for a
// 43-character "plain" challenge cannot be redeemed with the verifier itself.
func TestAppHandoff_APlainStyleChallengeCannotBeRedeemed(t *testing.T) {
	r := newHandoffRig(t)
	plain := strings.Repeat("p", 43)
	code, _ := r.mint(t, plain, "s")
	if w := r.redeem(t, code, plain, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("redeeming with the plain verifier = %d %s, want 401", w.Code, w.Body.String())
	}
	if got := r.sessionCount(); got != 0 {
		t.Fatalf("a refused redemption created %d sessions", got)
	}
}

func TestAppHandoff_WrongVerifierIsRefused(t *testing.T) {
	r := newHandoffRig(t)
	code, _ := r.mint(t, s256(oidcVerifier), "s")
	if w := r.redeem(t, code, oidcVerifier+"-x", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong verifier = %d, want 401", w.Code)
	}
	if got := r.sessionCount(); got != 0 {
		t.Fatalf("a refused redemption created %d sessions", got)
	}
}

func TestAppHandoff_CodeIsBoundToItsRedirect(t *testing.T) {
	r := newHandoffRig(t)
	code, _ := r.mint(t, s256(oidcVerifier), "s")
	w := r.redeem(t, code, oidcVerifier, "&redirect_uri="+url.QueryEscape("gotham://oauth"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("redemption with another redirect_uri = %d, want 401", w.Code)
	}
}

func TestAppHandoff_CodeFromBeforeAnEpochBumpIsRefused(t *testing.T) {
	r := newHandoffRig(t)
	code, _ := r.mint(t, s256(oidcVerifier), "s")
	r.setUser(handoffUserID, func(u *database.User) { u.CredentialEpoch++ })
	w := r.redeem(t, code, oidcVerifier, "")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "sign in again") {
		t.Fatalf("redemption after an epoch bump = %d %s, want 401 credentials changed", w.Code, w.Body.String())
	}
	if got := r.sessionCount(); got != 0 {
		t.Fatalf("a refused redemption created %d sessions", got)
	}
}

// Inactive at the epoch the code carries: no bump, so only the status check
// can refuse.
func TestAppHandoff_InactiveUserIsRefusedAtRedemption(t *testing.T) {
	r := newHandoffRig(t)
	code, _ := r.mint(t, s256(oidcVerifier), "s")
	r.setUser(handoffUserID, func(u *database.User) { u.Status = "locked" })
	w := r.redeem(t, code, oidcVerifier, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("redemption for an inactive user = %d %s, want 403", w.Code, w.Body.String())
	}
	if got := r.sessionCount(); got != 0 {
		t.Fatalf("a refused redemption created %d sessions", got)
	}
}

// A core user HEAD's store does not hold redeems nothing (fail closed). This
// also pins that redemption resolves the user from HEAD's store, which is the
// flip's open point (see the PR description).
func TestAppHandoff_UserMissingFromTheStoreIsRefusedAtRedemption(t *testing.T) {
	r := newHandoffRig(t)
	r.core.ID = "ghost-01"
	code, _ := r.mint(t, s256(oidcVerifier), "s")
	if w := r.redeem(t, code, oidcVerifier, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("redemption for an unknown user = %d %s, want 401", w.Code, w.Body.String())
	}
}

func TestAppHandoff_MintRefusesAUserWhoMayNotSignIn(t *testing.T) {
	r := newHandoffRig(t)
	ctx := context.Background()
	cases := map[string]*coreauth.User{
		"nil user":         nil,
		"empty id":         {Status: coreauth.StatusActive},
		"disabled":         {ID: handoffUserID, Status: coreauth.StatusDisabled, CredentialEpoch: handoffEpoch},
		"pending":          {ID: handoffUserID, Status: coreauth.StatusPending, CredentialEpoch: handoffEpoch},
		"system":           {ID: handoffUserID, Status: coreauth.StatusSystem, CredentialEpoch: handoffEpoch},
		"no status":        {ID: handoffUserID, CredentialEpoch: handoffEpoch},
		"sign-in restrict": {ID: handoffUserID, Status: coreauth.StatusActive, CredentialEpoch: handoffEpoch, SignInRestricted: true},
	}
	for name, u := range cases {
		loc, err := r.hand.Mint(ctx, u, "bruce@example.com", s256(oidcVerifier), handoffRedirect, "s")
		if err == nil || loc != "" {
			t.Errorf("%s: Mint = %q, %v; want a refusal and no location", name, loc, err)
		}
	}
}

// logCapture records every slog record, so the audit line HEAD writes at
// redemption (absauth.AttemptLog, which logs through slog.Default) can be read.
type logCapture struct {
	mu   sync.Mutex
	rows []string
}

func (*logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool { b.WriteString(" " + a.Key + "=" + a.Value.String()); return true })
	l.mu.Lock()
	l.rows = append(l.rows, b.String())
	l.mu.Unlock()
	return nil
}
func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(string) slog.Handler      { return l }

// The e-mail core hands to Mint is the one the redemption's sign-in record
// carries (HEAD's issueSession audit line).
func TestAppHandoff_EmailReachesTheSignInRecord(t *testing.T) {
	r := newHandoffRig(t)
	lc := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(lc))
	t.Cleanup(func() { slog.SetDefault(prev) })

	loc, err := r.hand.Mint(context.Background(), r.core, "alfred@example.com", s256(oidcVerifier), handoffRedirect, "s")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	u, _ := url.Parse(loc)
	if w := r.redeem(t, u.Query().Get("code"), oidcVerifier, ""); w.Code != http.StatusOK {
		t.Fatalf("redemption = %d %s", w.Code, w.Body.String())
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, row := range lc.rows {
		if strings.Contains(row, "action=login") && strings.Contains(row, "email=alfred@example.com") {
			return
		}
	}
	t.Fatalf("no login record carries the e-mail passed to Mint; rows: %q", lc.rows)
}
