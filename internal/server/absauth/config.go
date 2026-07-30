// file: internal/server/absauth/config.go
// version: 1.0.0
// guid: c2f0a951-7b34-46de-8a07-1d59b3e6c8f2
// last-edited: 2026-07-30

// Package absauth is the pure auth core for the Audiobookshelf-compatible API
// (design spec §3). It owns the resolved configuration, access-token (JWT) minting
// and verification, opaque refresh-token derivation, password hashing, the
// login/refresh throttle, and the auth audit log.
//
// It deliberately has NO gin and NO database dependency so every rule below is unit
// testable: the gin middleware lives in internal/server/middleware and the HTTP
// handlers in internal/server/handlers/abs.
//
// SECURITY MODEL (do not weaken):
//   - The ABS surface is FAIL-CLOSED. Unlike the fail-open Cloudflare-Access
//     middleware on /api/v1, an invalid Cf-Access-Jwt-Assertion on the ABS surface is
//     a hard 401 and a non-allowlisted identity is a hard 403 — never a pass-through
//     (spec §3.0.1). A fail-open path here would be an authentication bypass.
//   - ABS_JWT_SECRET is required whenever the ABS API is enabled and the server fails
//     to boot without it. It is never auto-generated: an ephemeral secret would
//     silently invalidate every client's stored token on restart.
//   - Refresh tokens are HMAC-derived from that secret, so no copy of the database
//     alone can produce a usable credential.
package absauth

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultAccessTTL is 30 DAYS, not one hour.
	//
	// §1.6 item 1 / §1.8.8 item 5: a short access token logs out every client that
	// does not implement refresh (and abs-shim, a known-working ABS server, ships no
	// refresh route at all and uses 30d). A 1h token would log refresh-less clients
	// out hourly, which is the single most common ABS-compatibility complaint.
	DefaultAccessTTL = 30 * 24 * time.Hour
	// DefaultRefreshTTL matches the access TTL (spec §3.2).
	DefaultRefreshTTL = 30 * 24 * time.Hour
	// DefaultRefreshGrace is how long a rotated-out refresh token keeps working so a
	// concurrent or replayed refresh from the same device is answered idempotently
	// instead of orphaning the session (spec §3.4).
	DefaultRefreshGrace = 10 * time.Minute

	// DefaultServerVersion is what /status and serverSettings.version report.
	// §1.8.8 item 6: >= 2.22.0 suppresses AudioBooth's "update your server" banner.
	// We only claim a version whose gating features we implement.
	DefaultServerVersion = "2.36.0"

	// DefaultLibraryID is the placeholder library identity reported as
	// userDefaultLibraryId until Phase 3 owns real libraries.
	//
	// §1.8.2 is a LOGIN BLOCKER: AudioBooth decodes userDefaultLibraryId
	// NON-optionally, so a null value makes the app unable to log in at all. A known
	// reference implementation (abs-shim) emits null here; we must not. It is also a
	// 36-char UUID, never a 26-char ULID, because Absorb splits compound ids at a
	// fixed offset of 36 (§1.7.1).
	DefaultLibraryID = "b5e3a5b2-a76e-471f-b18b-915e4716d053"

	// minSecretLen is the floor for ABS_JWT_SECRET. HS256 keys shorter than the
	// 256-bit hash output weaken the MAC.
	minSecretLen = 32

	// libraryIDLen is the exact length Absorb's fixed-offset id split requires.
	libraryIDLen = 36

	modeCF  = "cf"
	modeJWT = "jwt"
)

// Settings is the raw, string-shaped configuration as it arrives from viper/env.
// Load turns it into a validated Config.
type Settings struct {
	// Enabled is ABS_API_ENABLED. Default OFF: with the flag unset no ABS route is
	// registered and the server behaves exactly as it did before Phase 1.
	Enabled bool
	// AuthModes is ABS_AUTH_MODES, a comma-separated subset of {cf,jwt}. Empty means
	// the default "cf,jwt". Both resolvers are always built and tested; this only
	// gates which are active, so an operator can harden to CF-only once WARP is in
	// place, or run JWT-only for LAN testing without Cloudflare (spec §3.0.1).
	AuthModes string
	// JWTSecret is ABS_JWT_SECRET. Required when Enabled.
	JWTSecret string
	// The three durations accept any time.ParseDuration string. Empty means default.
	AccessTokenTTL  string
	RefreshTokenTTL string
	RefreshGrace    string
	// ServerVersion and DefaultLibraryID override the constants above.
	ServerVersion    string
	DefaultLibraryID string
}

// Modes records which identity resolvers are active.
type Modes struct {
	// CF enables step 1 of §3.0.1: a verified Cf-Access-Jwt-Assertion (Mode C/A).
	CF bool
	// JWT enables step 2 of §3.0.1: our own bearer access token (Mode B).
	JWT bool
}

// Config is the validated ABS auth configuration.
type Config struct {
	Enabled          bool
	Modes            Modes
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	RefreshGrace     time.Duration
	ServerVersion    string
	DefaultLibraryID string

	// secret is the HMAC key for access tokens and refresh-token derivation. Kept
	// unexported so it cannot be logged by a %+v on Config.
	secret []byte
}

// Load validates raw settings into a Config.
//
// It FAILS CLOSED: when the ABS API is enabled, a missing or too-short
// ABS_JWT_SECRET, an unparseable duration, an unknown auth mode, or a
// wrong-length default library id all return an error rather than quietly falling
// back to a default. The caller must refuse to boot on error.
//
// When the ABS API is disabled the only guarantee is that Load never errors, so a
// server with no ABS configuration at all starts normally.
func Load(s Settings) (*Config, error) {
	if !s.Enabled {
		return &Config{Enabled: false}, nil
	}

	secret := strings.TrimSpace(s.JWTSecret)
	if secret == "" {
		return nil, fmt.Errorf("absauth: ABS_JWT_SECRET is required when ABS_API_ENABLED is set " +
			"(refusing to boot: an auto-generated secret would invalidate every client token on restart)")
	}
	if len(secret) < minSecretLen {
		return nil, fmt.Errorf("absauth: ABS_JWT_SECRET must be at least %d characters, got %d", minSecretLen, len(secret))
	}

	modes, err := parseModes(s.AuthModes)
	if err != nil {
		return nil, err
	}

	accessTTL, err := parseDuration("ABS_ACCESS_TOKEN_TTL", s.AccessTokenTTL, DefaultAccessTTL)
	if err != nil {
		return nil, err
	}
	refreshTTL, err := parseDuration("ABS_REFRESH_TOKEN_TTL", s.RefreshTokenTTL, DefaultRefreshTTL)
	if err != nil {
		return nil, err
	}
	grace, err := parseDuration("ABS_REFRESH_GRACE", s.RefreshGrace, DefaultRefreshGrace)
	if err != nil {
		return nil, err
	}

	version := strings.TrimSpace(s.ServerVersion)
	if version == "" {
		version = DefaultServerVersion
	}

	libraryID := strings.TrimSpace(s.DefaultLibraryID)
	if libraryID == "" {
		libraryID = DefaultLibraryID
	}
	if len(libraryID) != libraryIDLen {
		return nil, fmt.Errorf("absauth: ABS_DEFAULT_LIBRARY_ID must be a %d-char UUID (Absorb splits ids at a fixed offset of %d), got %d chars",
			libraryIDLen, libraryIDLen, len(libraryID))
	}

	return &Config{
		Enabled:          true,
		Modes:            modes,
		AccessTTL:        accessTTL,
		RefreshTTL:       refreshTTL,
		RefreshGrace:     grace,
		ServerVersion:    version,
		DefaultLibraryID: libraryID,
		secret:           []byte(secret),
	}, nil
}

func parseModes(raw string) (Modes, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Modes{CF: true, JWT: true}, nil
	}
	var m Modes
	for _, part := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case modeCF:
			m.CF = true
		case modeJWT:
			m.JWT = true
		case "":
			continue
		default:
			return Modes{}, fmt.Errorf("absauth: ABS_AUTH_MODES contains unknown mode %q (want a comma-separated subset of %q,%q)", part, modeCF, modeJWT)
		}
	}
	if !m.CF && !m.JWT {
		return Modes{}, fmt.Errorf("absauth: ABS_AUTH_MODES=%q enables no resolver — every request would 401", raw)
	}
	return m, nil
}

func parseDuration(name, raw string, def time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("absauth: %s=%q is not a valid duration: %w", name, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("absauth: %s must be positive, got %v", name, d)
	}
	return d, nil
}

// ModeNames returns the active mode names, for logging and /status.
func (c *Config) ModeNames() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, 2)
	if c.Modes.CF {
		out = append(out, modeCF)
	}
	if c.Modes.JWT {
		out = append(out, modeJWT)
	}
	return out
}
