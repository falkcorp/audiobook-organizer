// file: internal/server/absauth/session_policy_test.go
// version: 1.1.0
// guid: cfab5f5b-f80e-4161-8910-bcc11673a1ae
// last-edited: 2026-10-19

package absauth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const day = 24 * time.Hour

// TestLoad_AccessTTLMustBeShorterThanSessionTTL is the invariant behind the
// 2026-10-19 forced-logout fix: when the access token lives as long as the
// session, the client's 401 lands at the instant the session dies and refresh can
// never rescue it. Load clamps the access TTL (never fails boot: equal values
// were the shipped default) and says so in Warnings.
func TestLoad_AccessTTLMustBeShorterThanSessionTTL(t *testing.T) {
	for _, tc := range []struct {
		name, access, refresh string
		wantAccess            time.Duration
		wantWarn              bool
	}{
		{name: "old shipped default, equal", access: "720h", refresh: "720h", wantAccess: 360 * time.Hour, wantWarn: true},
		{name: "access longer", access: "2160h", refresh: "720h", wantAccess: 360 * time.Hour, wantWarn: true},
		{name: "access set equal to new default", access: "1440h", refresh: "", wantAccess: 720 * time.Hour, wantWarn: true},
		{name: "valid explicit", access: "168h", refresh: "720h", wantAccess: 168 * time.Hour},
		{name: "new defaults", wantAccess: 30 * day},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings()
			s.AccessTokenTTL, s.RefreshTokenTTL = tc.access, tc.refresh
			cfg, err := Load(s)
			if err != nil {
				t.Fatalf("Load must not fail boot over the TTL ordering: %v", err)
			}
			if cfg.AccessTTL != tc.wantAccess {
				t.Fatalf("AccessTTL = %v, want %v", cfg.AccessTTL, tc.wantAccess)
			}
			if cfg.AccessTTL >= cfg.RefreshTTL {
				t.Fatalf("invariant broken: AccessTTL %v >= RefreshTTL %v", cfg.AccessTTL, cfg.RefreshTTL)
			}
			if got := len(cfg.Warnings) > 0; got != tc.wantWarn {
				t.Fatalf("warnings = %v, want warning=%v", cfg.Warnings, tc.wantWarn)
			}
			if tc.wantWarn && !strings.Contains(cfg.Warnings[0], "ABS_ACCESS_TOKEN_TTL") {
				t.Fatalf("warning must name the setting: %q", cfg.Warnings[0])
			}
		})
	}
}

func TestLoad_SessionMaxTTL(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: "0", want: 0},
		{raw: "0s", want: 0},
		{raw: "2160h", want: 90 * day},
		{raw: "-1h", wantErr: true},
		{raw: "a while", wantErr: true},
	} {
		s := testSettings()
		s.SessionMaxTTL = tc.raw
		cfg, err := Load(s)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ABS_SESSION_MAX_TTL=%q must be refused", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ABS_SESSION_MAX_TTL=%q: %v (zero means off and must not fail boot)", tc.raw, err)
		}
		if cfg.SessionMaxTTL != tc.want {
			t.Fatalf("ABS_SESSION_MAX_TTL=%q → %v, want %v", tc.raw, cfg.SessionMaxTTL, tc.want)
		}
	}
}

func TestSessionExpiry_SlidesAndRespectsCap(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := &Config{RefreshTTL: 60 * day}

	now := created.Add(45 * day)
	if got, want := cfg.SessionExpiry(created, now), now.Add(60*day); !got.Equal(want) {
		t.Fatalf("uncapped expiry = %v, want now+idle %v", got, want)
	}
	if cfg.SessionPastMaxLifetime(created, created.Add(10000*day)) {
		t.Fatal("with the cap off no session is ever past its max lifetime")
	}

	cfg.SessionMaxTTL = 90 * day
	if got, want := cfg.SessionExpiry(created, now), created.Add(90*day); !got.Equal(want) {
		t.Fatalf("capped expiry = %v, want createdAt+max %v", got, want)
	}
	early := created.Add(day)
	if got, want := cfg.SessionExpiry(created, early), early.Add(60*day); !got.Equal(want) {
		t.Fatalf("expiry well inside the cap = %v, want now+idle %v", got, want)
	}
	if cfg.SessionPastMaxLifetime(created, created.Add(90*day-time.Second)) {
		t.Fatal("a session one second short of the cap is not past it")
	}
	if !cfg.SessionPastMaxLifetime(created, created.Add(90*day)) {
		t.Fatal("a session at the cap is past it")
	}
}

func TestSessionExtendWindow(t *testing.T) {
	if got := (&Config{RefreshTTL: 60 * day}).SessionExtendWindow(); got != day {
		t.Fatalf("window for a 60d idle TTL = %v, want 24h", got)
	}
	if got := (&Config{RefreshTTL: time.Hour}).SessionExtendWindow(); got != 6*time.Minute {
		t.Fatalf("window for a 1h idle TTL = %v, want TTL/10 so a short TTL still slides", got)
	}
}

// TestIsAccessTokenExpired pins the audit distinction: a genuine token past its
// exp is "expired"; a forged one is never "expired", even when its exp is also in
// the past.
func TestIsAccessTokenExpired(t *testing.T) {
	cfg, err := Load(testSettings())
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tok, exp, err := cfg.MintAccessToken("u1", "s1", issued)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cfg.ParseAccessToken(tok, exp.Add(-time.Minute)); err != nil {
		t.Fatalf("token must verify before its exp on the injected clock: %v", err)
	}
	_, err = cfg.ParseAccessToken(tok, exp.Add(time.Minute))
	if err == nil || !IsAccessTokenExpired(err) {
		t.Fatalf("a genuine token past exp must report expired, got %v", err)
	}

	other := *cfg
	other.secret = []byte("ffffffffffffffffffffffffffffffff")
	_, err = other.ParseAccessToken(tok, exp.Add(time.Minute))
	if err == nil || IsAccessTokenExpired(err) {
		t.Fatalf("a wrong-secret token must be invalid, not expired, got %v", err)
	}
	if _, err := cfg.ParseAccessToken("not.a.jwt", issued); err == nil || IsAccessTokenExpired(err) {
		t.Fatalf("garbage must be invalid, not expired, got %v", err)
	}
	if IsAccessTokenExpired(nil) || IsAccessTokenExpired(jwt.ErrTokenSignatureInvalid) {
		t.Fatal("nil and signature errors are not expiry")
	}
}
