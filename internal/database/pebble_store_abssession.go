// file: internal/database/pebble_store_abssession.go
// version: 1.0.0
// guid: 8c04e7b1-52a9-4d38-b6f0-3a71c9e5d284
// last-edited: 2026-07-30

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ABS-compatible auth sessions (design spec §3.3).
//
// This is a NEW, additive keyspace. It is deliberately disjoint from the existing
// browser `sess:` keyspace so the `abk_` API-key path, browser sessions, OAuth, and
// Cloudflare-Access verification keep working untouched:
//
//	abs_sess:<sessionID>                    → the ABSSession record
//	idx:abs_sess:user:<userID>:<sessionID>  → "1"          (per-user listing)
//	idx:abs_sess:token:<refreshHashHex>     → <sessionID>  (refresh-token lookup)
//
// The `idx:` prefix for indexes follows the convention already used by
// `idx:sess:user:` in pebble_store_auth.go, and keeps index keys out of the record
// prefix scan performed by DeleteExpiredABSSessions.
const (
	absSessionKeyPrefix    = "abs_sess:"
	absSessionUserIdxPfx   = "idx:abs_sess:user:"
	absSessionTokenIdxPfx  = "idx:abs_sess:token:"
	absSessionTokenIdxTmpl = absSessionTokenIdxPfx + "%s"
)

// ABSSession is one Audiobookshelf-compatible refresh-token session — one record per
// client device, per design spec §3.3.
//
// Refresh tokens themselves are NEVER persisted. Only their SHA-256 hashes are
// stored (RefreshTokenHash / PrevRefreshTokenHash), which is what the refresh path
// compares against. Seed + Generation let the auth layer *re-derive* the current
// token from the server-side HMAC secret for the idempotent grace replay of
// §3.4 step 3, so a database copy on its own can never yield a usable credential.
type ABSSession struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`

	// Seed is per-session random material. Combined with ID, Generation and the
	// server's HMAC secret (which lives only in the environment, never here) it
	// deterministically derives this session's refresh token.
	Seed string `json:"seed"`
	// Generation increments on every successful rotation.
	Generation int `json:"generation"`

	// RefreshTokenHash is the SHA-256 hex of the CURRENT refresh token.
	RefreshTokenHash string `json:"refresh_token_hash"`
	// PrevRefreshTokenHash is the SHA-256 hex of the immediately previous refresh
	// token. It stays acceptable until GraceUntil so a concurrent or replayed
	// refresh from the same device is answered idempotently instead of orphaning
	// the session (§3.4).
	PrevRefreshTokenHash string `json:"prev_refresh_token_hash,omitempty"`

	// DeviceInfo is the client-supplied device descriptor, stored verbatim as JSON
	// text so it can be echoed back on GET /api/me/sessions without re-shaping.
	DeviceInfo string `json:"device_info,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	IP         string `json:"ip,omitempty"`

	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// GraceUntil bounds how long PrevRefreshTokenHash stays acceptable.
	GraceUntil time.Time `json:"grace_until,omitempty"`

	Revoked bool `json:"revoked"`
}

// Live reports whether the session may still authenticate: not revoked and not past
// its expiry at the given instant.
func (s *ABSSession) Live(now time.Time) bool {
	if s == nil || s.Revoked {
		return false
	}
	return s.ExpiresAt.IsZero() || now.Before(s.ExpiresAt)
}

// InGrace reports whether the previous refresh token is still acceptable.
func (s *ABSSession) InGrace(now time.Time) bool {
	if s == nil || s.PrevRefreshTokenHash == "" || s.GraceUntil.IsZero() {
		return false
	}
	return now.Before(s.GraceUntil)
}

func absSessionKey(id string) []byte { return []byte(absSessionKeyPrefix + id) }
func absSessionTokenKey(h string) []byte {
	return []byte(fmt.Sprintf(absSessionTokenIdxTmpl, h))
}
func absSessionUserKey(userID, id string) []byte {
	return []byte(absSessionUserIdxPfx + userID + ":" + id)
}

// CreateABSSession writes a new ABS session plus its per-user and refresh-token
// indexes atomically.
func (p *PebbleStore) CreateABSSession(session *ABSSession) error {
	if session == nil {
		return fmt.Errorf("abs session: nil session")
	}
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.UserID) == "" {
		return fmt.Errorf("abs session: id and user id are required")
	}
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("abs session: marshal: %w", err)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(absSessionKey(session.ID), data, nil); err != nil {
		return err
	}
	if err := b.Set(absSessionUserKey(session.UserID, session.ID), []byte("1"), nil); err != nil {
		return err
	}
	if session.RefreshTokenHash != "" {
		if err := b.Set(absSessionTokenKey(session.RefreshTokenHash), []byte(session.ID), nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// GetABSSession loads a session by id. A missing session is (nil, nil) — the same
// contract as GetSession.
func (p *PebbleStore) GetABSSession(id string) (*ABSSession, error) {
	v, closer, err := p.db.Get(absSessionKey(id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s ABSSession
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, fmt.Errorf("abs session %s: unmarshal: %w", id, err)
	}
	return &s, nil
}

// UpdateABSSession overwrites the record and refreshes its refresh-token index
// entries. Both the current and the previous hash are indexed so a grace-window
// replay still resolves. Index entries for retired generations are pruned lazily by
// GetABSSessionByRefreshHash.
func (p *PebbleStore) UpdateABSSession(session *ABSSession) error {
	if session == nil {
		return fmt.Errorf("abs session: nil session")
	}
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("abs session: marshal: %w", err)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(absSessionKey(session.ID), data, nil); err != nil {
		return err
	}
	if err := b.Set(absSessionUserKey(session.UserID, session.ID), []byte("1"), nil); err != nil {
		return err
	}
	for _, h := range []string{session.RefreshTokenHash, session.PrevRefreshTokenHash} {
		if h == "" {
			continue
		}
		if err := b.Set(absSessionTokenKey(h), []byte(session.ID), nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// GetABSSessionByRefreshHash resolves a refresh-token hash to its session.
//
// It returns (nil, nil) when the hash is unknown OR when it has been retired — a
// hash that is neither the session's current nor its previous hash must never
// authenticate, even though its index entry was written once. Such stale index
// entries are deleted on sight so the keyspace stays bounded.
func (p *PebbleStore) GetABSSessionByRefreshHash(hash string) (*ABSSession, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, nil
	}
	key := absSessionTokenKey(hash)
	v, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sessionID := string(v)
	closer.Close()

	session, err := p.GetABSSession(sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		// Dangling index entry (record deleted out from under it).
		_ = p.db.Delete(key, pebble.Sync)
		return nil, nil
	}
	if hash != session.RefreshTokenHash && hash != session.PrevRefreshTokenHash {
		_ = p.db.Delete(key, pebble.Sync)
		return nil, nil
	}
	return session, nil
}

// ListABSSessionsForUser returns every stored ABS session for a user, newest first.
// Scoped strictly to the per-user index so one user can never observe another's
// sessions.
func (p *PebbleStore) ListABSSessionsForUser(userID string) ([]ABSSession, error) {
	prefix := []byte(absSessionUserIdxPfx + userID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	out := make([]ABSSession, 0, 4)
	for iter.First(); iter.Valid(); iter.Next() {
		id := strings.TrimPrefix(string(iter.Key()), string(prefix))
		s, err := p.GetABSSession(id)
		if err != nil || s == nil {
			continue
		}
		out = append(out, *s)
	}
	// Newest first, matching ABS's /api/me/sessions ordering.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt.After(out[i].CreatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// RevokeABSSession marks a session revoked and drops its refresh-token index
// entries so the tokens stop resolving immediately. Revoking an unknown session is
// a no-op (idempotent logout).
func (p *PebbleStore) RevokeABSSession(id string) error {
	s, err := p.GetABSSession(id)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	return p.revokeABSSessionRecord(s)
}

func (p *PebbleStore) revokeABSSessionRecord(s *ABSSession) error {
	s.Revoked = true
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("abs session: marshal: %w", err)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(absSessionKey(s.ID), data, nil); err != nil {
		return err
	}
	for _, h := range []string{s.RefreshTokenHash, s.PrevRefreshTokenHash} {
		if h == "" {
			continue
		}
		if err := b.Delete(absSessionTokenKey(h), nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// RevokeAllABSSessionsForUser implements POST /logout?allDevices=1. Returns the
// number of sessions revoked.
func (p *PebbleStore) RevokeAllABSSessionsForUser(userID string) (int, error) {
	sessions, err := p.ListABSSessionsForUser(userID)
	if err != nil {
		return 0, err
	}
	revoked := 0
	for i := range sessions {
		if sessions[i].Revoked {
			continue
		}
		if err := p.revokeABSSessionRecord(&sessions[i]); err != nil {
			return revoked, err
		}
		revoked++
	}
	return revoked, nil
}

// DeleteExpiredABSSessions removes revoked and past-expiry sessions along with both
// of their index entries. Mirrors DeleteExpiredSessions for the browser keyspace.
func (p *PebbleStore) DeleteExpiredABSSessions(now time.Time) (int, error) {
	prefix := []byte(absSessionKeyPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	batch := p.db.NewBatch()
	defer batch.Close()

	deleted := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		var s ABSSession
		if err := json.Unmarshal(iter.Value(), &s); err != nil {
			continue
		}
		if !s.Revoked && s.ExpiresAt.After(now) {
			continue
		}
		if err := batch.Delete(key, nil); err != nil {
			return deleted, err
		}
		if err := batch.Delete(absSessionUserKey(s.UserID, s.ID), nil); err != nil {
			return deleted, err
		}
		for _, h := range []string{s.RefreshTokenHash, s.PrevRefreshTokenHash} {
			if h == "" {
				continue
			}
			if err := batch.Delete(absSessionTokenKey(h), nil); err != nil {
				return deleted, err
			}
		}
		deleted++
	}
	if deleted == 0 {
		return 0, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return deleted, err
	}
	return deleted, nil
}
