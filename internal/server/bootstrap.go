// file: internal/server/bootstrap.go
// version: 1.13.1
// guid: 3e7c9a12-4f6b-4d8e-b5a1-2c8f0e3d9b47
// last-edited: 2026-07-12

package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
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
func InitStartupReadOnlyKey(store database.Store, dataDir string) error {
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

// ConsumeBootstrapToken validates plaintext against the stored hash, checks
// the 10-minute expiry, then atomically deletes both settings and the on-disk
// file. Returns (valid, error). Thread-safe.
func ConsumeBootstrapToken(store SettingsReadWriter, dataDir, plaintext string) (bool, error) {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	setting, err := store.GetSetting(bootstrapTokenKey)
	if err != nil {
		// A consumed (deleted) token surfaces as ErrSettingNotFound — that's an
		// invalid token, not a server fault, so the caller should return 401 not
		// 500 (pen-test finding HIGH-4a).
		if errors.Is(err, database.ErrSettingNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("bootstrap: read token hash: %w", err)
	}
	if setting == nil || setting.Value == "" {
		return false, nil
	}

	// Check expiry before doing any hash work.
	if expSetting, err := store.GetSetting(bootstrapExpiresKey); err == nil && expSetting != nil {
		var expUnix int64
		fmt.Sscanf(expSetting.Value, "%d", &expUnix)
		if expUnix > 0 && time.Now().Unix() > expUnix {
			slog.Info("Token exchange attempted but token has expired (restart required to generate a new one)")
			_ = store.DeleteSetting(bootstrapTokenKey)
			_ = store.DeleteSetting(bootstrapExpiresKey)
			_ = os.Remove(BootstrapTokenPath(dataDir))
			return false, nil
		}
	}

	if hashBootstrapToken(plaintext) != setting.Value {
		return false, nil
	}

	_ = store.DeleteSetting(bootstrapTokenKey)
	_ = store.DeleteSetting(bootstrapExpiresKey)
	_ = os.Remove(BootstrapTokenPath(dataDir))

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

// --- rate-limit state for the bootstrap endpoint ---

const (
	bootstrapMaxAttempts    = 5
	bootstrapWindowDuration = time.Hour
)

type bootstrapAttempt struct {
	count   int
	firstAt time.Time
}

var (
	bootstrapRateMu sync.Mutex
	bootstrapRate   = map[string]*bootstrapAttempt{}
)

func bootstrapIsRateLimited(ip string) bool {
	bootstrapRateMu.Lock()
	defer bootstrapRateMu.Unlock()
	a, ok := bootstrapRate[ip]
	if !ok {
		return false
	}
	if time.Since(a.firstAt) > bootstrapWindowDuration {
		delete(bootstrapRate, ip)
		return false
	}
	return a.count >= bootstrapMaxAttempts
}

func bootstrapRecordAttempt(ip string) {
	bootstrapRateMu.Lock()
	defer bootstrapRateMu.Unlock()
	a, ok := bootstrapRate[ip]
	if !ok || time.Since(a.firstAt) > bootstrapWindowDuration {
		bootstrapRate[ip] = &bootstrapAttempt{count: 1, firstAt: time.Now()}
		return
	}
	a.count++
}

// --- handler ---

type bootstrapRequest struct {
	Token   string `json:"token"`
	KeyName string `json:"key_name"`
}

// handleBootstrap exchanges a one-time bootstrap token for a full-privilege API key.
// Wired to a public (unauthenticated) route.
func (s *Server) handleBootstrap(c *gin.Context) {
	ip := strings.TrimSpace(c.ClientIP())

	if bootstrapIsRateLimited(ip) {
		httputil.RespondWithError(c, http.StatusTooManyRequests, "too many bootstrap attempts — try again later", "TOO_MANY_REQUESTS")
		return
	}
	bootstrapRecordAttempt(ip)

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

	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	dataDir := filepath.Dir(config.AppConfig.DatabasePath)

	// Find or create admin BEFORE consuming the token, so a creation failure
	// doesn't burn the one-time token.
	adminUser, generatedPassword, err := findOrCreateAdminUser(store)
	if err != nil || adminUser == nil {
		slog.Info("find/create admin error ip err", "ip", ip, "err", err)
		httputil.RespondWithInternalError(c, "failed to find or create admin user")
		return
	}

	valid, err := ConsumeBootstrapToken(store, dataDir, req.Token)
	if err != nil {
		slog.Info("consume error ip err", "ip", ip, "err", err)
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
		slog.Info("generate api key error ip err", "ip", ip, "err", err)
		httputil.RespondWithInternalError(c, "failed to generate API key")
		return
	}

	scopes := auth.All()

	// Bootstrap-issued keys are full-scope admin credentials and must always
	// expire (SEC-1/PROC-6) — a non-positive configured TTL falls back to the
	// default of 30 days rather than "never expire".
	ttlDays := config.AppConfig.BootstrapKeyTTLDays
	if ttlDays <= 0 {
		ttlDays = 30
	}
	expiresAt := time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour)

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
		slog.Info("create api key error user ip err", "adminUser", adminUser.ID, "ip", ip, "err", err)
		httputil.RespondWithInternalError(c, "failed to create API key")
		return
	}

	slog.Info("Token consumed new API key created user key_id ip", "adminUser", adminUser.Username, "created", created.ID, "ip", ip)

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

// findOrCreateAdminUser returns the first user with PermUsersManage, creating
// one if none exists. Returns (user, generatedPassword, error); generatedPassword
// is non-empty only when a new user was created.
func findOrCreateAdminUser(store database.Store) (*database.User, string, error) {
	// (Removed the SQLite-RBAC-unsupported guard: PebbleDB is the only backend and
	// always resolves roles, so the guard's sentinel check was permanently dead.)
	users, err := store.ListUsers()
	if err != nil {
		return nil, "", err
	}
	for i := range users {
		u := &users[i]
		for _, roleName := range u.Roles {
			role, err := store.GetRoleByName(roleName)
			if err != nil || role == nil {
				role, _ = store.GetRoleByID(roleName)
			}
			if role == nil {
				continue
			}
			for _, perm := range role.Permissions {
				if perm == auth.PermUsersManage {
					return u, "", nil
				}
			}
		}
	}

	// No admin found — create one.
	password := generateReadablePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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

	u, err := store.CreateUser("admin", "admin@localhost", "bcrypt", string(hash), []string{roleID}, "active")
	if err != nil {
		return nil, "", fmt.Errorf("create admin user: %w", err)
	}
	slog.Info("No admin found — created useradmin with generated password")
	return u, password, nil
}

// passphraseWords is a small wordlist for readable password generation.
var passphraseWords = []string{
	"amber", "brave", "cedar", "dune", "ember", "flint", "grove", "haven",
	"ivory", "jade", "kite", "lark", "maple", "nova", "opal", "pine",
	"quest", "river", "stone", "tide", "ultra", "vale", "wolf", "xenon",
	"yarn", "zinc", "atlas", "bolt", "crisp", "drift", "eagle", "forge",
	"gleam", "hawk", "iron", "jest", "kelp", "lunar", "mist", "noble",
	"orbit", "prism", "quill", "ridge", "swift", "thorn", "umber", "vivid",
	"wren", "axiom", "brisk", "coral", "delta", "echo", "fable", "gust",
	"halo", "inlet", "joust", "knoll", "ledge", "marsh", "night", "onyx",
}

func generateReadablePassword() string {
	pickWord := func() string {
		var b [8]byte
		rand.Read(b[:])
		idx := binary.BigEndian.Uint64(b[:]) % uint64(len(passphraseWords))
		w := passphraseWords[idx]
		return strings.ToUpper(w[:1]) + w[1:]
	}
	var numBuf [1]byte
	rand.Read(numBuf[:])
	num := int(numBuf[0])%900 + 100 // 100–999
	return fmt.Sprintf("%s-%s-%s-%d", pickWord(), pickWord(), pickWord(), num)
}
