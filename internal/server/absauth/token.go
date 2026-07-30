// file: internal/server/absauth/token.go
// version: 1.0.0
// guid: 9a3d5f70-4c81-42be-b0d6-7e15c8a2b943
// last-edited: 2026-07-30

package absauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessTokenType is the value of the "type" claim on an ABS access token. It is
// checked on parse so a differently-purposed token signed with the same secret can
// never be replayed as an access token.
const AccessTokenType = "access"

// ErrNotEnabled is returned when a token operation is attempted on a Config whose
// ABS API is disabled (no secret was ever loaded).
var ErrNotEnabled = errors.New("absauth: ABS API is not enabled")

// AccessClaims is the verified content of an ABS access token.
type AccessClaims struct {
	// UserID is the "sub" claim.
	UserID string
	// SessionID is the "sid" claim — the abs_sess record this token belongs to.
	SessionID string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// MintAccessToken signs a real HS256 JWT for (userID, sessionID) and returns it with
// its expiry.
//
// Real ABS access tokens are JWTs and clients read them: §1.8.8 item 5 requires that
// /auth/refresh return an accessToken that is a PARSEABLE JWT with a NUMERIC exp, so
// this must never be an opaque string. exp/iat are emitted as JSON numbers (seconds),
// which jwt/v5's NumericDate does.
func (c *Config) MintAccessToken(userID, sessionID string, now time.Time) (string, time.Time, error) {
	if c == nil || len(c.secret) == 0 {
		return "", time.Time{}, ErrNotEnabled
	}
	// Truncate to the second: JWT exp/iat are integer seconds, and returning an
	// unrounded time would disagree with what the client parses back out.
	issued := now.Truncate(time.Second)
	expires := now.Add(c.AccessTTL).Truncate(time.Second)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  userID,
		"sid":  sessionID,
		"type": AccessTokenType,
		"iat":  jwt.NewNumericDate(issued),
		"exp":  jwt.NewNumericDate(expires),
	})
	signed, err := tok.SignedString(c.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("absauth: sign access token: %w", err)
	}
	return signed, expires, nil
}

// ParseAccessToken verifies an ABS access token and returns its claims.
//
// Verification is deliberately strict:
//   - only HS256 is accepted, so the alg=none and alg-confusion bypasses are closed;
//   - exp is required and enforced;
//   - the "type" claim must be exactly "access";
//   - sub and sid must both be non-empty, so a token cannot authenticate without
//     naming a session the caller can then check for revocation.
func (c *Config) ParseAccessToken(raw string) (*AccessClaims, error) {
	if c == nil || len(c.secret) == 0 {
		return nil, ErrNotEnabled
	}
	parsed, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return c.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("absauth: parse access token: %w", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("absauth: access token has unexpected claims shape")
	}
	if t, _ := claims["type"].(string); t != AccessTokenType {
		return nil, fmt.Errorf("absauth: access token has type %q, want %q", t, AccessTokenType)
	}
	sub, _ := claims["sub"].(string)
	sid, _ := claims["sid"].(string)
	if sub == "" || sid == "" {
		return nil, errors.New("absauth: access token is missing sub or sid")
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return nil, errors.New("absauth: access token is missing exp")
	}
	out := &AccessClaims{UserID: sub, SessionID: sid, ExpiresAt: exp.Time}
	if iat, err := claims.GetIssuedAt(); err == nil && iat != nil {
		out.IssuedAt = iat.Time
	}
	return out, nil
}
