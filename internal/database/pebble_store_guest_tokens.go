// file: internal/database/pebble_store_guest_tokens.go
// version: 1.0.0
// guid: 2c7e9a40-5b1d-4e83-9f06-d4a8b3c1e725
// last-edited: 2026-10-23

package database

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// guestServiceTokenPrefix keys one record per guest:
// guestcftoken:<user id> -> GuestServiceToken. The record is the server's only
// memory of a per-guest Cloudflare service token: it holds the token's id and a
// SHA-256 of its client secret, never the secret and never the client id, so a
// database dump cannot be replayed against Cloudflare.
const guestServiceTokenPrefix = "guestcftoken:"

// GuestServiceToken statuses.
const (
	// GuestTokenPending: written before Cloudflare is called. One that stays
	// pending means the server stopped mid-mint: look for a token named
	// aorg-guest-<user id> in the Cloudflare dashboard.
	GuestTokenPending = "pending"
	// GuestTokenMinted: Cloudflare made the token and its id is recorded; it is
	// not yet in the books policy, so it admits nothing.
	GuestTokenMinted = "minted"
	// GuestTokenActive: minted and in the books policy.
	GuestTokenActive = "active"
	// GuestTokenRevoking: a revoke has started. It is written under the store
	// lock before Cloudflare is called, so a mint still in flight sees it and
	// deletes the token it just made.
	GuestTokenRevoking = "revoking"
	// GuestTokenFailed: the mint (or adding it to the policy) failed and the
	// token was cleaned up; Detail says why, for the admin.
	GuestTokenFailed = "failed"
	// GuestTokenRevoked: deleted at Cloudflare and removed from the policy.
	GuestTokenRevoked = "revoked"
	// GuestTokenRevokeFailed: a revoke started and Cloudflare did not confirm
	// it; the admin can retry. Detail says why.
	GuestTokenRevokeFailed = "revoke_failed"
)

// ErrGuestServiceTokenExists is returned by CreateGuestServiceToken when the
// user already has a record.
var ErrGuestServiceTokenExists = errors.New("guest service token record already exists")

// ErrGuestServiceTokenNotFound is returned by UpdateGuestServiceToken for a
// user with no record.
var ErrGuestServiceTokenNotFound = errors.New("guest service token record not found")

// GuestServiceToken is one guest's Cloudflare service token record.
type GuestServiceToken struct {
	UserID  string `json:"user_id"`
	TokenID string `json:"token_id,omitempty"`
	// Name is the Cloudflare token name, aorg-guest-<user id>.
	Name string `json:"name"`
	// SecretHash is the hex SHA-256 of the client secret.
	SecretHash string `json:"secret_hash,omitempty"`
	Status     string `json:"status"`
	// Detail is the admin-facing reason for a failed status (capped).
	Detail string `json:"detail,omitempty"`
	// PolicyAdded is true once the token's rule is in the books policy, so a
	// revoke knows to take it out.
	PolicyAdded  bool       `json:"policy_added,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokeReason string     `json:"revoke_reason,omitempty"`
}

// GuestServiceTokenStore holds per-guest Cloudflare service token records.
// Not part of Store; resolve it with AsCapability. Every write takes the same
// lock as ConsumeInvite and the SSO grant writes.
type GuestServiceTokenStore interface {
	// CreateGuestServiceToken writes rec (UserID required). It fails with
	// ErrGuestServiceTokenExists when the user already has a record.
	CreateGuestServiceToken(rec GuestServiceToken) error
	// GetGuestServiceToken returns the user's record, or nil.
	GetGuestServiceToken(userID string) (*GuestServiceToken, error)
	// ListGuestServiceTokens returns every record, newest first.
	ListGuestServiceTokens() ([]GuestServiceToken, error)
	// UpdateGuestServiceToken applies fn to the user's record under the store
	// lock and saves the result (UpdatedAt is set, Detail capped). fn's error
	// aborts the write and is returned. ErrGuestServiceTokenNotFound when
	// there is no record. Callers use this to make "did my record survive a
	// concurrent revoke" a single atomic check.
	UpdateGuestServiceToken(userID string, fn func(*GuestServiceToken) error) (*GuestServiceToken, error)
}

var _ GuestServiceTokenStore = (*PebbleStore)(nil)

const guestTokenDetailMax = 300

func (p *PebbleStore) readGuestServiceToken(userID string) (*GuestServiceToken, error) {
	v, closer, err := p.db.Get([]byte(guestServiceTokenPrefix + userID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var g GuestServiceToken
	if err := json.Unmarshal(v, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func (p *PebbleStore) writeGuestServiceToken(g *GuestServiceToken) error {
	if len(g.Detail) > guestTokenDetailMax {
		g.Detail = g.Detail[:guestTokenDetailMax]
	}
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(guestServiceTokenPrefix+g.UserID), data, pebble.Sync)
}

// CreateGuestServiceToken implements GuestServiceTokenStore.
func (p *PebbleStore) CreateGuestServiceToken(rec GuestServiceToken) error {
	if rec.UserID == "" {
		return errors.New("guest service token: user id required")
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	existing, err := p.readGuestServiceToken(rec.UserID)
	if err != nil {
		return err
	}
	if existing != nil {
		return ErrGuestServiceTokenExists
	}
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	return p.writeGuestServiceToken(&rec)
}

// GetGuestServiceToken implements GuestServiceTokenStore.
func (p *PebbleStore) GetGuestServiceToken(userID string) (*GuestServiceToken, error) {
	if userID == "" {
		return nil, nil
	}
	return p.readGuestServiceToken(userID)
}

// ListGuestServiceTokens implements GuestServiceTokenStore.
func (p *PebbleStore) ListGuestServiceTokens() ([]GuestServiceToken, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(guestServiceTokenPrefix),
		UpperBound: []byte(guestServiceTokenPrefix + "~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []GuestServiceToken{}
	for iter.First(); iter.Valid(); iter.Next() {
		var g GuestServiceToken
		if err := json.Unmarshal(iter.Value(), &g); err != nil {
			continue
		}
		out = append(out, g)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, iter.Error()
}

// UpdateGuestServiceToken implements GuestServiceTokenStore.
func (p *PebbleStore) UpdateGuestServiceToken(userID string, fn func(*GuestServiceToken) error) (*GuestServiceToken, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	g, err := p.readGuestServiceToken(userID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, ErrGuestServiceTokenNotFound
	}
	if err := fn(g); err != nil {
		return nil, err
	}
	g.UserID = userID // a callback cannot move the record
	g.UpdatedAt = time.Now().UTC()
	if err := p.writeGuestServiceToken(g); err != nil {
		return nil, err
	}
	return g, nil
}
