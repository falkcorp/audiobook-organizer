// file: internal/server/absauth/absauth_test.go
// version: 1.5.0
// guid: 6e1b47a3-0d85-4c92-a731-58f0c3b6d9e2
// last-edited: 2026-11-10

package absauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func testSettings() Settings {
	return Settings{Enabled: true, JWTSecret: testSecret}
}

// ── Config / fail-closed boot ────────────────────────────────────────────────

func TestLoad_DisabledNeedsNoSecret(t *testing.T) {
	cfg, err := Load(Settings{Enabled: false})
	if err != nil {
		t.Fatalf("a disabled ABS API must not fail to load: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("cfg.Enabled should be false")
	}
}

func TestLoad_EnabledWithoutSecretFailsClosed(t *testing.T) {
	_, err := Load(Settings{Enabled: true})
	if err == nil {
		t.Fatal("ABS_API_ENABLED with no ABS_JWT_SECRET must fail closed at boot")
	}
	if !strings.Contains(err.Error(), "ABS_JWT_SECRET") {
		t.Fatalf("error should name the missing variable, got %v", err)
	}
}

func TestLoad_RejectsShortSecret(t *testing.T) {
	if _, err := Load(Settings{Enabled: true, JWTSecret: "tooshort"}); err == nil {
		t.Fatal("a secret below the minimum length must be rejected")
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(testSettings())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// §1.6 item 1 / §1.8.8 item 5: 30 DAYS, not 1h. A 1h access token logs
	// refresh-less clients out hourly.
	if cfg.AccessTTL != 30*24*time.Hour {
		t.Fatalf("default access TTL must be 30d, got %v", cfg.AccessTTL)
	}
	// The session idle TTL is 60d: strictly longer than the access TTL, so the
	// client's 401 arrives while refresh can still rescue the session.
	if cfg.RefreshTTL != 60*24*time.Hour {
		t.Fatalf("default refresh (session idle) TTL must be 60d, got %v", cfg.RefreshTTL)
	}
	if cfg.SessionMaxTTL != 0 {
		t.Fatalf("the absolute session cap must default to off, got %v", cfg.SessionMaxTTL)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("the defaults must load without warnings, got %v", cfg.Warnings)
	}
	if cfg.RefreshGrace != 10*time.Minute {
		t.Fatalf("default grace must be 10m, got %v", cfg.RefreshGrace)
	}
	if !cfg.Modes.CF || !cfg.Modes.JWT {
		t.Fatalf("default ABS_AUTH_MODES must be cf,jwt, got %+v", cfg.Modes)
	}
	// §1.8.8 item 6: >= 2.22.0 suppresses AudioBooth's nag banner.
	if cfg.ServerVersion != "2.36.0" {
		t.Fatalf("default server version must be 2.36.0, got %q", cfg.ServerVersion)
	}
	// §1.8.2: userDefaultLibraryId must be a non-null 36-char UUID String.
	if len(cfg.DefaultLibraryID) != 36 {
		t.Fatalf("default library id must be a 36-char UUID, got %q (%d)", cfg.DefaultLibraryID, len(cfg.DefaultLibraryID))
	}
}

func TestLoad_ModeParsing(t *testing.T) {
	for _, tc := range []struct {
		in       string
		cf, jwtM bool
		wantErr  bool
	}{
		{in: "cf,jwt", cf: true, jwtM: true},
		{in: "cf", cf: true},
		{in: "jwt", jwtM: true},
		{in: " JWT , CF ", cf: true, jwtM: true},
		{in: "cf,bogus", wantErr: true},
		{in: ",", wantErr: true},
	} {
		s := testSettings()
		s.AuthModes = tc.in
		cfg, err := Load(s)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ABS_AUTH_MODES=%q should be rejected", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ABS_AUTH_MODES=%q: %v", tc.in, err)
		}
		if cfg.Modes.CF != tc.cf || cfg.Modes.JWT != tc.jwtM {
			t.Fatalf("ABS_AUTH_MODES=%q → %+v, want cf=%v jwt=%v", tc.in, cfg.Modes, tc.cf, tc.jwtM)
		}
	}
}

func TestLoad_RejectsBadDurations(t *testing.T) {
	s := testSettings()
	s.AccessTokenTTL = "thirty days please"
	if _, err := Load(s); err == nil {
		t.Fatal("an unparseable ABS_ACCESS_TOKEN_TTL must fail closed, not silently default")
	}
}

func TestLoad_RejectsNonUUIDLibraryID(t *testing.T) {
	s := testSettings()
	s.DefaultLibraryID = "01JABCDEFGHJKMNPQRSTVWXYZ" // a 25/26-char ULID
	if _, err := Load(s); err == nil {
		t.Fatal("a non-36-char library id must be rejected (§1.7.1 Absorb splits ids at offset 36)")
	}
}

// ── Access token (JWT) ──────────────────────────────────────────────────────

func TestAccessToken_RoundTrip(t *testing.T) {
	cfg, err := Load(testSettings())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	tok, exp, err := cfg.MintAccessToken("user-1", "sess-1", now)
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	claims, err := cfg.ParseAccessToken(tok, time.Now())
	if err != nil {
		t.Fatalf("ParseAccessToken: %v", err)
	}
	if claims.UserID != "user-1" || claims.SessionID != "sess-1" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if !exp.Equal(now.Add(cfg.AccessTTL).Truncate(time.Second)) {
		t.Fatalf("exp %v should be now+AccessTTL truncated to the second", exp)
	}
}

// TestAccessToken_ExpIsNumericAndTypeIsAccess pins §1.8.8 item 5: AudioBooth
// requires accessToken to be a parseable JWT with a NUMERIC exp.
func TestAccessToken_ExpIsNumericAndTypeIsAccess(t *testing.T) {
	cfg, err := Load(testSettings())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tok, _, err := cfg.MintAccessToken("user-1", "sess-1", time.Now())
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("access token must be a three-part JWT, got %d parts", len(parts))
	}
	raw, err := jwt.NewParser().DecodeSegment(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if _, ok := payload["exp"].(float64); !ok {
		t.Fatalf("exp must be a JSON number, got %T (%v)", payload["exp"], payload["exp"])
	}
	if _, ok := payload["iat"].(float64); !ok {
		t.Fatalf("iat must be a JSON number, got %T", payload["iat"])
	}
	if payload["type"] != "access" {
		t.Fatalf(`type claim must be "access", got %v`, payload["type"])
	}
	if payload["sub"] != "user-1" || payload["sid"] != "sess-1" {
		t.Fatalf("sub/sid claims wrong: %v", payload)
	}
}

func TestAccessToken_RejectsWrongSecret(t *testing.T) {
	cfg, _ := Load(testSettings())
	other := testSettings()
	other.JWTSecret = "ffffffffffffffffffffffffffffffff"
	otherCfg, _ := Load(other)

	tok, _, err := otherCfg.MintAccessToken("user-1", "sess-1", time.Now())
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	if _, err := cfg.ParseAccessToken(tok, time.Now()); err == nil {
		t.Fatal("a token signed with a different secret must not verify")
	}
}

func TestAccessToken_RejectsExpired(t *testing.T) {
	s := testSettings()
	s.AccessTokenTTL = "1s"
	cfg, err := Load(s)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tok, _, err := cfg.MintAccessToken("user-1", "sess-1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	if _, err := cfg.ParseAccessToken(tok, time.Now()); err == nil {
		t.Fatal("an expired access token must be rejected")
	}
}

// TestAccessToken_RejectsAlgNone is the classic JWT bypass: an attacker strips the
// signature and sets alg=none.
func TestAccessToken_RejectsAlgNone(t *testing.T) {
	cfg, _ := Load(testSettings())
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub":  "user-1",
		"sid":  "sess-1",
		"type": "access",
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	tok, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	if _, err := cfg.ParseAccessToken(tok, time.Now()); err == nil {
		t.Fatal("alg=none must be rejected — this would be a full authentication bypass")
	}
}

// TestAccessToken_RejectsRefreshTypeClaim stops a refresh-shaped JWT being replayed
// as an access token.
func TestAccessToken_RejectsWrongType(t *testing.T) {
	cfg, _ := Load(testSettings())
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "user-1",
		"sid":  "sess-1",
		"type": "refresh",
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := cfg.ParseAccessToken(tok, time.Now()); err == nil {
		t.Fatal(`a JWT with type != "access" must be rejected`)
	}
}

// ── Refresh tokens ──────────────────────────────────────────────────────────

func TestRefreshToken_ShapeAndDeterminism(t *testing.T) {
	cfg, _ := Load(testSettings())
	seed, err := NewRefreshSeed()
	if err != nil {
		t.Fatalf("NewRefreshSeed: %v", err)
	}
	tok := cfg.DeriveRefreshToken("sess-1", seed, 1)
	if !strings.HasPrefix(tok, RefreshTokenPrefix) {
		t.Fatalf("refresh token must carry the %q prefix, got %q", RefreshTokenPrefix, tok)
	}
	if got := cfg.DeriveRefreshToken("sess-1", seed, 1); got != tok {
		t.Fatal("derivation must be deterministic so a grace replay can return the already-minted token")
	}
	if got := cfg.DeriveRefreshToken("sess-1", seed, 2); got == tok {
		t.Fatal("a new generation must produce a different token")
	}
	if got := cfg.DeriveRefreshToken("sess-2", seed, 1); got == tok {
		t.Fatal("two different sessions must never share a refresh token")
	}
	// Opaque: no session id or seed recoverable from the token body.
	body := strings.TrimPrefix(tok, RefreshTokenPrefix)
	if strings.Contains(body, "sess-1") || strings.Contains(body, seed) {
		t.Fatalf("refresh token leaks its inputs: %q", tok)
	}
}

func TestRefreshToken_DependsOnServerSecret(t *testing.T) {
	cfg, _ := Load(testSettings())
	other := testSettings()
	other.JWTSecret = "ffffffffffffffffffffffffffffffff"
	otherCfg, _ := Load(other)
	seed, _ := NewRefreshSeed()
	if cfg.DeriveRefreshToken("s", seed, 1) == otherCfg.DeriveRefreshToken("s", seed, 1) {
		t.Fatal("refresh tokens must not be derivable without the server secret")
	}
}

func TestNewRefreshSeed_IsRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		s, err := NewRefreshSeed()
		if err != nil {
			t.Fatalf("NewRefreshSeed: %v", err)
		}
		if seen[s] {
			t.Fatalf("duplicate seed %q", s)
		}
		seen[s] = true
	}
}

func TestHashRefreshToken(t *testing.T) {
	h1 := HashRefreshToken("abr_aaa")
	if h1 == "" || h1 == "abr_aaa" {
		t.Fatalf("hash must not be empty or the plaintext, got %q", h1)
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d", len(h1))
	}
	if HashRefreshToken("abr_aaa") != h1 {
		t.Fatal("hash must be stable")
	}
	if HashRefreshToken("abr_bbb") == h1 {
		t.Fatal("distinct tokens must hash differently")
	}
}
