// file: internal/server/absauth/absauth_test.go
// version: 1.0.0
// guid: 6e1b47a3-0d85-4c92-a731-58f0c3b6d9e2
// last-edited: 2026-07-30

package absauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
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
	if cfg.RefreshTTL != 30*24*time.Hour {
		t.Fatalf("default refresh TTL must be 30d, got %v", cfg.RefreshTTL)
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
	claims, err := cfg.ParseAccessToken(tok)
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
	if _, err := cfg.ParseAccessToken(tok); err == nil {
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
	if _, err := cfg.ParseAccessToken(tok); err == nil {
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
	if _, err := cfg.ParseAccessToken(tok); err == nil {
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
	if _, err := cfg.ParseAccessToken(tok); err == nil {
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
	for i := 0; i < 50; i++ {
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

// ── Passwords: argon2id for new, bcrypt verify + rehash for existing ────────

func TestPassword_Argon2idRoundTrip(t *testing.T) {
	algo, hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if algo != AlgoArgon2id {
		t.Fatalf("new passwords must be argon2id, got %q", algo)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash must be PHC-encoded argon2id, got %q", hash)
	}
	ok, rehash := VerifyPassword(algo, hash, "correct horse battery staple")
	if !ok {
		t.Fatal("the correct password must verify")
	}
	if rehash {
		t.Fatal("an argon2id hash must not ask for a rehash")
	}
	if ok, _ := VerifyPassword(algo, hash, "wrong"); ok {
		t.Fatal("a wrong password must not verify")
	}
}

func TestPassword_Argon2idSaltsDiffer(t *testing.T) {
	_, h1, _ := HashPassword("same")
	_, h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (per-hash salt)")
	}
}

func TestPassword_BcryptVerifiesAndAsksForRehash(t *testing.T) {
	raw, err := bcrypt.GenerateFromPassword([]byte("legacy-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	ok, rehash := VerifyPassword(AlgoBcrypt, string(raw), "legacy-pw")
	if !ok {
		t.Fatal("an existing bcrypt user must still be able to log in")
	}
	if !rehash {
		t.Fatal("a successful bcrypt login must request a rehash to argon2id")
	}
	if ok, _ := VerifyPassword(AlgoBcrypt, string(raw), "nope"); ok {
		t.Fatal("wrong password must not verify under bcrypt either")
	}
}

// TestPassword_EmptyAlgoFallsBackToBcrypt covers rows written before the algo
// column was populated.
func TestPassword_EmptyAlgoFallsBackToBcrypt(t *testing.T) {
	raw, _ := bcrypt.GenerateFromPassword([]byte("legacy-pw"), bcrypt.MinCost)
	ok, rehash := VerifyPassword("", string(raw), "legacy-pw")
	if !ok || !rehash {
		t.Fatalf("empty algo should verify as bcrypt and request rehash (ok=%v rehash=%v)", ok, rehash)
	}
}

// TestPassword_CredentiallessUserCannotLogIn is the security-critical case: a
// JIT-provisioned Cloudflare-Access user has algo "oauth" and an EMPTY hash. It must
// be impossible to log in as that user with any password, including "".
func TestPassword_CredentiallessUserCannotLogIn(t *testing.T) {
	for _, algo := range []string{AlgoOAuth, "", AlgoArgon2id, AlgoBcrypt} {
		for _, pw := range []string{"", "anything", "$argon2id$"} {
			if ok, _ := VerifyPassword(algo, "", pw); ok {
				t.Fatalf("a user with an empty password hash must never authenticate (algo=%q pw=%q)", algo, pw)
			}
		}
	}
	if ok, _ := VerifyPassword(AlgoOAuth, "some-non-empty-value", "some-non-empty-value"); ok {
		t.Fatal(`a user whose algo is "oauth" must never authenticate by password`)
	}
}

func TestPassword_RejectsMalformedArgonHash(t *testing.T) {
	for _, h := range []string{
		"$argon2id$v=19$m=65536,t=3,p=2$notbase64$alsonot",
		"$argon2id$v=19$badparams$c2FsdA$aGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"garbage",
	} {
		if ok, _ := VerifyPassword(AlgoArgon2id, h, "pw"); ok {
			t.Fatalf("malformed argon2id hash %q must not verify", h)
		}
	}
}

// ── Throttle: per-IP hard, per-account soft, never a hard account lock ──────

func TestThrottle_PerIPHardBlockAfterBudget(t *testing.T) {
	tr := NewThrottle()
	tr.SetSleep(func(time.Duration) {})
	for i := 0; i < MaxFailuresPerIP; i++ {
		if tr.IPBlocked("1.2.3.4") {
			t.Fatalf("blocked too early at failure %d", i)
		}
		tr.RecordFailure("acct", "1.2.3.4")
	}
	if !tr.IPBlocked("1.2.3.4") {
		t.Fatal("the source IP must be blocked once it exhausts its failure budget")
	}
	if tr.IPBlocked("5.6.7.8") {
		t.Fatal("a different source IP must not be blocked")
	}
}

// TestThrottle_NeverHardLocksAnAccount pins §1.9.4 item 3 and the HIGH-3 finding: a
// client's legitimate retry loop (or a third party hammering a known username) must
// never deny the real user access from a clean source IP.
func TestThrottle_NeverHardLocksAnAccount(t *testing.T) {
	tr := NewThrottle()
	tr.SetSleep(func(time.Duration) {})
	for i := 0; i < MaxFailuresPerIP*5; i++ {
		tr.RecordFailure("victim", "9.9.9.9")
	}
	if tr.IPBlocked("victim") {
		t.Fatal("the account id must not be treated as an IP key")
	}
	if tr.IPBlocked("10.0.0.1") {
		t.Fatal("the real user's own source IP must be unaffected — accounts are never hard-locked")
	}
}

func TestThrottle_SoftDelayIsProgressiveAndCapped(t *testing.T) {
	tr := NewThrottle()
	tr.SetSleep(func(time.Duration) {})
	var last time.Duration
	for i := 0; i < 40; i++ {
		d := tr.RecordFailure("acct", "2.2.2.2")
		if d > SoftMaxDelay {
			t.Fatalf("soft delay %v exceeded the cap %v", d, SoftMaxDelay)
		}
		if d < last {
			t.Fatalf("soft delay went backwards: %v then %v", last, d)
		}
		last = d
	}
	if last == 0 {
		t.Fatal("expected a non-zero soft delay after many failures")
	}
}

func TestThrottle_ClearResetsBothCounters(t *testing.T) {
	tr := NewThrottle()
	tr.SetSleep(func(time.Duration) {})
	for i := 0; i < MaxFailuresPerIP; i++ {
		tr.RecordFailure("acct", "3.3.3.3")
	}
	if !tr.IPBlocked("3.3.3.3") {
		t.Fatal("expected block")
	}
	tr.Clear("acct", "3.3.3.3")
	if tr.IPBlocked("3.3.3.3") {
		t.Fatal("a successful login must clear the source IP's failure budget")
	}
	if d := tr.RecordFailure("acct", "3.3.3.3"); d != 0 {
		t.Fatalf("account soft counter should have been cleared, got delay %v", d)
	}
}

func TestThrottle_ConcurrentUseIsRaceFree(t *testing.T) {
	tr := NewThrottle()
	tr.SetSleep(func(time.Duration) {})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				tr.RecordFailure("acct", "4.4.4.4")
				_ = tr.IPBlocked("4.4.4.4")
				tr.Clear("acct", "4.4.4.4")
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
