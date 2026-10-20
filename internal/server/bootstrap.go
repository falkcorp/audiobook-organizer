// file: internal/server/bootstrap.go
// version: 1.21.0
// guid: 3e7c9a12-4f6b-4d8e-b5a1-2c8f0e3d9b47
// last-edited: 2026-10-20

package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

const (
	bootstrapTokenKey   = "bootstrap_token_hash"
	bootstrapExpiresKey = "bootstrap_token_expires_at"
	bootstrapTokenTTL   = 10 * time.Minute
)

// SettingsReadWriter is the minimal store surface needed by the bootstrap subsystem.
type SettingsReadWriter interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
	DeleteSetting(key string) error
}

const (
	readonlyKeyNameSetting    = "startup_readonly_key_id"
	readonlyKeyExpiresSetting = "startup_readonly_key_expires_at"
	readonlyKeyTTL            = 24 * time.Hour
)

// InitStartupReadOnlyKey creates (or refreshes) a read-only API key on every
// startup. The key is scoped to library.view only and expires after 24 hours.
// Its raw value is written to a 0600 file at ReadOnlyKeyPath(dataDir).
// dataDir must be the already-cleaned application data directory (caller is
// responsible for sanitising the value — mirrors ConsumeBootstrapToken).
func InitStartupReadOnlyKey(store startupKeyStore, dataDir string) error {
	// Revoke any previously emitted startup read-only key so old tokens don't
	// accumulate in the database.
	if prev, err := store.GetSetting(readonlyKeyNameSetting); err == nil && prev != nil && prev.Value != "" {
		_ = store.DeleteSetting(readonlyKeyNameSetting)
		_ = store.DeleteSetting(readonlyKeyExpiresSetting)
	}

	adminUser, _, err := findOrCreateAdminUser(store)
	if err != nil || adminUser == nil {
		return fmt.Errorf("startup readonly key: find admin user: %w", err)
	}

	raw, hash, err := database.GenerateAPIKeyToken()
	if err != nil {
		return fmt.Errorf("startup readonly key: generate token: %w", err)
	}

	expiresAt := time.Now().Add(readonlyKeyTTL)
	key := &database.APIKey{
		ID:          ulid.Make().String(),
		UserID:      adminUser.ID,
		Name:        "Startup read-only key",
		Description: "Auto-generated on startup. Read-only (library.view). Expires in 24 h.",
		TokenHash:   hash,
		Scopes:      []string{string(auth.PermLibraryView)},
		Status:      "active",
		CreatedAt:   time.Now(),
		ExpiresAt:   &expiresAt,
	}

	created, err := store.CreateAPIKey(key)
	if err != nil {
		return fmt.Errorf("startup readonly key: create: %w", err)
	}

	_ = store.SetSetting(readonlyKeyNameSetting, created.ID, "string", false)
	_ = store.SetSetting(readonlyKeyExpiresSetting, fmt.Sprintf("%d", expiresAt.Unix()), "string", false)

	// Never log the raw key — logs are retained by aggregators and would grant
	// anyone with log access a live read-only credential (pen-test finding
	// CRIT-1). Instead write it to a 0600 file (like the bootstrap token) so
	// local tooling can still pick it up, and log only the non-secret ID/expiry.
	// dataDir is the server's own data directory set by the administrator at
	// deploy time — it is not derived from any HTTP request or end-user input.
	// CodeQL flags config-derived paths as potential path-injection sinks; the
	// lgtm annotation records this as an accepted false positive consistent with
	// the existing InitBootstrapToken write (dismissed as alert #952).
	keyPath := ReadOnlyKeyPath(filepath.Clean(dataDir))
	if err := os.WriteFile(keyPath, []byte(raw+"\n"), 0o600); err != nil { // lgtm[go/path-injection]
		slog.Warn("could not write read-only key file", "path", keyPath, "err", err)
	}
	slog.Info("Read-only API key created (library.view)", "key_id", created.ID, "expires_at", expiresAt.Format(time.RFC3339), "token_file", keyPath)
	return nil
}

// ReadOnlyKeyPath returns the path to the on-disk plaintext read-only API key file.
// filepath.Clean sanitizes the dataDir before joining so a misconfigured path
// cannot escape the intended directory (CodeQL: path-injection).
func ReadOnlyKeyPath(dataDir string) string {
	return filepath.Join(filepath.Clean(dataDir), ".readonly-key")
}

// bootstrapMu prevents two concurrent exchange attempts from both succeeding.
var bootstrapMu sync.Mutex

// BootstrapTokenPath returns the path to the on-disk plaintext bootstrap token file.
// filepath.Clean sanitizes the dataDir before joining (CodeQL: path-injection).
func BootstrapTokenPath(dataDir string) string {
	return filepath.Join(filepath.Clean(dataDir), ".bootstrap-token")
}

// InitBootstrapToken generates a fresh bootstrap token on every startup.
// The token expires after bootstrapTokenTTL (10 min). A restart is required
// to get a new one — so an unexpected restart is visible in the logs.
func InitBootstrapToken(store SettingsReadWriter, dataDir string) error {
	// Always replace — each restart gets a fresh 10-minute window.
	_ = store.DeleteSetting(bootstrapTokenKey)
	_ = store.DeleteSetting(bootstrapExpiresKey)

	raw, hash, err := generateBootstrapToken()
	if err != nil {
		return fmt.Errorf("bootstrap: generate token: %w", err)
	}

	expiresAt := time.Now().Add(bootstrapTokenTTL)
	if err := store.SetSetting(bootstrapTokenKey, hash, "string", false); err != nil {
		return fmt.Errorf("bootstrap: persist token hash: %w", err)
	}
	if err := store.SetSetting(bootstrapExpiresKey, fmt.Sprintf("%d", expiresAt.Unix()), "string", false); err != nil {
		return fmt.Errorf("bootstrap: persist token expiry: %w", err)
	}

	tokenPath := BootstrapTokenPath(dataDir)
	if err := os.WriteFile(tokenPath, []byte(raw+"\n"), 0600); err != nil {
		slog.Info("WARNING could not write token file", "tokenPath", tokenPath, "err", err)
	}

	// Never log the raw token — anyone with log access could exchange it for a
	// full-privilege API key (pen-test finding CRIT-1). The raw value lives only
	// in the 0600 token file above; log its path + expiry so operators (and the
	// server-bootstrap tooling) know where to read it.
	slog.Info("Emergency access token written", "token_file", tokenPath, "expires_at", expiresAt.Format(time.RFC3339))
	slog.Info("Token expires in 10 minutes. Read it from the token_file above, then POST /api/v1/auth/bootstrap to exchange for an API key. Restart required to generate a new token.")
	return nil
}

// errBootstrapTokenNotBurned is returned when a valid token could not be
// deleted after it was used: the caller must not hand out a credential for a
// token that would still work a second time.
var errBootstrapTokenNotBurned = errors.New("bootstrap: token was valid but could not be deleted")

// RedeemBootstrapToken is the one place a bootstrap token is checked and spent.
// Under bootstrapMu it:
//
//  1. validates plaintext without consuming it: the stored hash must exist,
//     the stored expiry must exist, parse and lie in the future (a missing or
//     unreadable expiry is an invalid token, not one that never expires), and
//     the hashes must match under a constant-time compare;
//  2. runs onValid, only for a valid token. If onValid fails, the token is
//     NOT consumed and (true, err) is returned, so a failed admin creation
//     does not burn the one-time token;
//  3. consumes the token: deletes the hash (which must succeed, or
//     errBootstrapTokenNotBurned is returned), then the expiry and the
//     on-disk file (best effort; without the hash they are inert).
//
// The three steps hold one mutex, so two concurrent requests cannot both pass
// step 1 for the same token, and nothing in onValid runs for an invalid token.
// The store has no transaction spanning a user row and a setting, so the
// atomicity is per process: a crash between steps 2 and 3 leaves the work of
// onValid done and the token still valid until it expires (10 minutes), and a
// retry with it finds the admin onValid created instead of making another.
//
// An invalid, missing, consumed or expired token is (false, nil): a 401, not a
// 500 (pen-test finding HIGH-4a).
func RedeemBootstrapToken(store SettingsReadWriter, dataDir, plaintext string, onValid func() error) (bool, error) {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	valid, err := checkBootstrapTokenLocked(store, dataDir, plaintext)
	if err != nil || !valid {
		return false, err
	}
	if onValid != nil {
		if err := onValid(); err != nil {
			return true, err
		}
	}
	if err := store.DeleteSetting(bootstrapTokenKey); err != nil {
		return true, fmt.Errorf("%w: %w", errBootstrapTokenNotBurned, err)
	}
	_ = store.DeleteSetting(bootstrapExpiresKey)
	_ = os.Remove(BootstrapTokenPath(dataDir))
	return true, nil
}

// checkBootstrapTokenLocked reports whether plaintext is the live bootstrap
// token. It never consumes a valid token; it does clear an expired one. The
// caller holds bootstrapMu.
func checkBootstrapTokenLocked(store SettingsReadWriter, dataDir, plaintext string) (bool, error) {
	setting, err := store.GetSetting(bootstrapTokenKey)
	if err != nil {
		// A consumed (deleted) token surfaces as ErrSettingNotFound — that's an
		// invalid token, not a server fault.
		if errors.Is(err, database.ErrSettingNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("bootstrap: read token hash: %w", err)
	}
	if setting == nil || setting.Value == "" {
		return false, nil
	}

	// Expiry before any hash work. InitBootstrapToken always writes both
	// settings, so a missing or unreadable expiry is treated as expired:
	// reading it as "no expiry" would make the token live forever.
	expSetting, err := store.GetSetting(bootstrapExpiresKey)
	if err != nil && !errors.Is(err, database.ErrSettingNotFound) {
		return false, fmt.Errorf("bootstrap: read token expiry: %w", err)
	}
	var expUnix int64
	if expSetting != nil {
		expUnix, _ = strconv.ParseInt(strings.TrimSpace(expSetting.Value), 10, 64)
	}
	if expUnix <= 0 || time.Now().Unix() > expUnix {
		slog.Info("Token exchange attempted but token has expired or has no expiry (restart required to generate a new one)")
		_ = store.DeleteSetting(bootstrapTokenKey)
		_ = store.DeleteSetting(bootstrapExpiresKey)
		_ = os.Remove(BootstrapTokenPath(dataDir))
		return false, nil
	}

	got := hashBootstrapToken(plaintext)
	if subtle.ConstantTimeCompare([]byte(got), []byte(setting.Value)) != 1 {
		return false, nil
	}
	return true, nil
}

func generateBootstrapToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("read random bytes: %w", err)
	}
	raw = "abbs_" + base64.RawURLEncoding.EncodeToString(buf)
	hash = hashBootstrapToken(raw)
	return raw, hash, nil
}

func hashBootstrapToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// --- rate-limit state for the bootstrap endpoint: see bootstrap_rate_limit.go ---

// --- handler ---

type bootstrapRequest struct {
	Token   string `json:"token"`
	KeyName string `json:"key_name"`
}

// handleBootstrap exchanges a one-time bootstrap token for a full-privilege API key.
// Wired to a public (unauthenticated) route.
func (s *Server) handleBootstrap(c *gin.Context) {
	// Behind the Cloudflare tunnel c.ClientIP() is the tunnel's address for
	// everyone, which would put every caller in one bucket and let one attacker
	// lock the owner out of recovery. trustedClientIP recovers the real visitor
	// from CF-Connecting-IP only when that header can be believed (the peer is
	// the configured tunnel connector, or the request carries a verified Access
	// assertion and the peer is this host). When it cannot, the endpoint is NOT
	// left unlimited, because it is high-value: all such requests share the one
	// untrusted-local key, never a key taken from an unproven header. ip keeps
	// the attributed (or local) address for logging.
	ip, attributed := trustedClientIP(c, clientTrust{isLocalPeer: remoteAddrIsLocal, connectors: tunnelConnectorPeers()})
	ip = strings.TrimSpace(ip)

	if !bootstrapLimits.allow(bootstrapRateKey(ip, attributed)) {
		httputil.RespondWithError(c, http.StatusTooManyRequests, "too many bootstrap attempts — try again later", "TOO_MANY_REQUESTS")
		return
	}

	var req bootstrapRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		httputil.RespondWithBadRequest(c, "token is required")
		return
	}

	store := s.storeForWiring()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// The SAME constant the write side uses. These two derivations were
	// independent copies of filepath.Dir(database_path); had either one been
	// updated alone, the token would have been written to one directory and
	// looked for -- and deleted from -- another, which fails as a 401 with the
	// file sitting right there.
	dataDir := config.SecureStateDir()

	// The token is validated first and nothing is created for an invalid one:
	// until 2026-10-20 the admin was found or created before the token was
	// checked, so any unauthenticated POST with a non-empty token could create
	// an admin account. The admin step runs inside RedeemBootstrapToken, after
	// validation and before consumption, so a creation failure still does not
	// burn the one-time token.
	var (
		adminUser         *database.User
		generatedPassword string
	)
	valid, err := RedeemBootstrapToken(store, dataDir, req.Token, func() error {
		u, pw, err := findOrCreateAdminUser(store)
		if err != nil {
			return err
		}
		if u == nil {
			return errors.New("no admin user found or created")
		}
		adminUser, generatedPassword = u, pw
		return nil
	})
	if err != nil {
		if valid && adminUser == nil {
			slog.Info("find/create admin error ip err", "ip", logger.SanitizeLogValue(ip), "err", err)
			httputil.RespondWithInternalError(c, "failed to find or create admin user")
			return
		}
		slog.Info("bootstrap token error ip err", "ip", logger.SanitizeLogValue(ip), "err", err)
		httputil.RespondWithInternalError(c, "internal error")
		return
	}
	if !valid {
		time.Sleep(500 * time.Millisecond)
		httputil.RespondWithUnauthorized(c, "invalid bootstrap token")
		return
	}

	keyName := strings.TrimSpace(req.KeyName)
	if keyName == "" {
		keyName = "Bootstrap recovery key"
	}

	raw, hash, err := database.GenerateAPIKeyToken()
	if err != nil {
		slog.Info("generate api key error ip err", "ip", logger.SanitizeLogValue(ip), "err", err)
		httputil.RespondWithInternalError(c, "failed to generate API key")
		return
	}

	scopes := auth.All()

	// Bootstrap-issued keys are full-scope admin credentials and must always
	// expire (SEC-1/PROC-6): 8h by default, never more than 24h, and a bad
	// configured value falls back to the default rather than "never expire".
	// The resolver's warning is logged once at startup
	// (logBootstrapKeyTTLConfig), not on every exchange.
	cfgSnap := config.Snapshot()
	ttl, _ := cfgSnap.ResolveBootstrapKeyTTL()
	expiresAt := time.Now().Add(ttl)

	key := &database.APIKey{
		ID:          ulid.Make().String(),
		UserID:      adminUser.ID,
		Name:        keyName,
		Description: "Generated via bootstrap token exchange",
		TokenHash:   hash,
		Scopes:      scopes,
		Status:      "active",
		CreatedAt:   time.Now(),
		ExpiresAt:   &expiresAt,
	}

	created, err := store.CreateAPIKey(key)
	if err != nil {
		slog.Info("create api key error user ip err", "adminUser", adminUser.ID, "ip", logger.SanitizeLogValue(ip), "err", err)
		httputil.RespondWithInternalError(c, "failed to create API key")
		return
	}

	slog.Info("Token consumed new API key created user key_id ip", "adminUser", adminUser.Username, "created", created.ID, "ip", logger.SanitizeLogValue(ip))

	type bootstrapResp struct {
		APIKey            string     `json:"api_key"`
		KeyID             string     `json:"key_id"`
		UserID            string     `json:"user_id"`
		Username          string     `json:"username"`
		Scopes            []string   `json:"scopes"`
		Message           string     `json:"message"`
		ExpiresAt         *time.Time `json:"expires_at,omitempty"`
		GeneratedPassword string     `json:"generated_password,omitempty"`
		PasswordMessage   string     `json:"password_message,omitempty"`
	}
	rsp := bootstrapResp{
		APIKey:    raw,
		KeyID:     created.ID,
		UserID:    adminUser.ID,
		Username:  adminUser.Username,
		Scopes:    scopes,
		Message:   "Bootstrap token consumed. This key will not be shown again.",
		ExpiresAt: created.ExpiresAt,
	}
	if generatedPassword != "" {
		rsp.GeneratedPassword = generatedPassword
		rsp.PasswordMessage = "Admin account created. Change this password after logging in."
		slog.Info("Created admin user — save the generated_password from this response", "adminUser", adminUser.Username)
	}
	httputil.RespondWithOK(c, rsp)
}

// findOrCreateAdminUser returns the first ACTIVE user with PermUsersManage,
// creating one if none exists. Returns (user, generatedPassword, error);
// generatedPassword is non-empty only when a new user was created.
//
// A locked or otherwise inactive admin is skipped: a key minted for it is
// refused at authentication, so returning it made bootstrap look successful
// and then fail. The new admin takes the first free name of "admin",
// "admin-2", ... (an inactive or demoted "admin" row may still hold
// "admin"), and no email: the fixed address "admin@localhost" collided with
// the row an earlier bootstrap left, and an address nobody receives mail at
// would otherwise be recorded as a verified one (EmailSource setup).
func findOrCreateAdminUser(store adminBootstrapStore) (*database.User, string, error) {
	// (Removed the SQLite-RBAC-unsupported guard: PebbleDB is the only backend and
	// always resolves roles, so the guard's sentinel check was permanently dead.)
	users, err := store.ListUsers()
	if err != nil {
		return nil, "", err
	}
	taken := make(map[string]bool, len(users))
	for i := range users {
		u := &users[i]
		taken[strings.ToLower(u.Username)] = true
		if u.Status != "active" {
			continue
		}
		for _, roleName := range u.Roles {
			role, err := store.GetRoleByName(roleName)
			if err != nil || role == nil {
				role, _ = store.GetRoleByID(roleName)
			}
			if role == nil {
				continue
			}
			if slices.Contains(role.Permissions, auth.PermUsersManage) {
				return u, "", nil
			}
		}
	}

	// No admin found — create one.
	password, err := generateAdminPassword()
	if err != nil {
		return nil, "", fmt.Errorf("generate admin password: %w", err)
	}
	algo, hash, err := absauth.HashPassword(password)
	if err != nil {
		return nil, "", fmt.Errorf("hash password: %w", err)
	}

	// Use the "admin" role if it exists, otherwise assign all permissions directly
	// via a synthetic role name that the seeder would have created.
	adminRole, _ := store.GetRoleByName("admin")
	roleID := "admin"
	if adminRole != nil {
		roleID = adminRole.ID
	}

	username := "admin"
	for n := 2; taken[username]; n++ {
		if n > 100 {
			return nil, "", fmt.Errorf("create admin user: no free name among admin, admin-2 ... admin-100")
		}
		username = fmt.Sprintf("admin-%d", n)
	}
	u, err := store.CreateUser(username, "", algo, hash, []string{roleID}, "active")
	if err != nil {
		return nil, "", fmt.Errorf("create admin user: %w", err)
	}
	slog.Info("No active admin found — created an admin user with a generated password", "username", username)
	return u, password, nil
}

// Generated admin passwords: adminPasswordGroups groups of
// adminPasswordGroupLen symbols from adminPasswordAlphabet, joined by '-'.
// The alphabet has 32 symbols (RFC 4648 base32), so each symbol carries
// exactly 5 bits and the low 5 bits of a uniformly random byte select one
// with no modulo bias (256 is a multiple of 32). 7 groups of 4 symbols give
// 28 symbols = 140 bits from crypto/rand, above the 128-bit floor. The
// previous three-word passphrase had about 28 bits (64^3 x 900) and its
// trailing number was biased by "% 900" on one byte.
const (
	adminPasswordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	adminPasswordGroups   = 7
	adminPasswordGroupLen = 4
	adminPasswordMinBits  = 128
)

// adminPasswordBits is the entropy of one generated password.
const adminPasswordBits = adminPasswordGroups * adminPasswordGroupLen * 5

// generateAdminPassword returns a new random admin password (see the
// constants above). It fails only when crypto/rand does.
func generateAdminPassword() (string, error) {
	n := adminPasswordGroups * adminPasswordGroupLen
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(n + adminPasswordGroups - 1)
	for i, v := range buf {
		if i > 0 && i%adminPasswordGroupLen == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(adminPasswordAlphabet[v&31])
	}
	return b.String(), nil
}
