// file: internal/database/pebble_store_abssession.go
// version: 1.5.0
// guid: 8c04e7b1-52a9-4d38-b6f0-3a71c9e5d284
// last-edited: 2026-10-19

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
//
// A zero ExpiresAt is NOT live. Every writer stamps an expiry (login sets it,
// refresh and use slide it), so a zero value is a malformed row, and with sliding
// expiry and no absolute cap by default, reading it as "never expires" would hand
// out a credential that can never die. The expiry sweep reads it the same way
// (absSessionSweepable) and deletes such a row.
func (s *ABSSession) Live(now time.Time) bool {
	if s == nil || s.Revoked || s.ExpiresAt.IsZero() {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// InGrace reports whether the previous refresh token is still acceptable.
func (s *ABSSession) InGrace(now time.Time) bool {
	if s == nil || s.PrevRefreshTokenHash == "" || s.GraceUntil.IsZero() {
		return false
	}
	return now.Before(s.GraceUntil)
}

// ErrABSSessionRevoked is returned by UpdateABSSession when the stored session has
// been revoked (or deleted) since the caller read it. Revocation is one-way: a
// refresh that read the row before a concurrent logout must not write it back live.
var ErrABSSessionRevoked = errors.New("abs session: revoked or deleted")

// ErrABSSessionStale is returned by UpdateABSSession when the caller's copy is not
// based on the stored row: another writer rotated the session since it was read.
// Writing it would put an older generation back and orphan the refresh token the
// other writer just handed to the device.
var ErrABSSessionStale = errors.New("abs session: stale write (rotated since read)")

// absSessionWriteMu serializes EVERY mutation of the abs_sess keyspace:
// CreateABSSession, UpdateABSSession, ExtendABSSession, RevokeABSSession,
// RevokeAllABSSessionsForUser and each delete in DeleteExpiredABSSessions. Pebble
// has no compare-and-swap, so without it two writers that each read the row and
// write a whole copy back lose each other's changes. The ones that matter:
//
//   - the bearer-path ExpiresAt extension writing back a row a refresh rotated in
//     between, which un-rotates it and logs the device out as refresh-token-unknown;
//   - any writer writing back a row a logout revoked in between, which un-revokes it;
//   - a writer writing back a row the expiry sweep deleted in between, which
//     resurrects it as a live record with no index entries, invisible to
//     revoke-all and to the sweep itself.
//
// Every writer re-reads the stored row under the lock and refuses when it is gone,
// revoked or (for an extension) expired; the sweep re-reads each candidate under
// the lock and deletes it only if it is still revoked or expired.
//
// One process-wide mutex rather than per-session locks: these writes happen at
// login, refresh, logout and at most about once a day per session from use, so
// contention is negligible, and a single lock cannot be taken in two orders.
// Package-level because PebbleStore is opened once per process (and in tests, a
// shared lock only serializes, it never deadlocks: no holder calls back into it).
var absSessionWriteMu sync.Mutex

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
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
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
//
// It refuses with ErrABSSessionRevoked, writing nothing, when the STORED row is
// revoked or gone: the caller read it before a concurrent logout, and writing its
// copy back would un-revoke the session and re-index its refresh tokens.
//
// It refuses with ErrABSSessionStale when the copy is not derived from the stored
// row, which is a compare-and-swap on the token chain without a separate version
// field. Only two writes are legal:
//
//   - same generation, same current hash: a touch (grace replay, LastUsedAt);
//   - generation+1 whose previous hash is the stored current hash: a rotation of
//     the row as it is now.
//
// Anything else (an older generation, or a rotation computed from a row that has
// since rotated again) is last-writer-wins on the token chain and would orphan the
// refresh token the other writer just issued.
func (p *PebbleStore) UpdateABSSession(session *ABSSession) error {
	if session == nil {
		return fmt.Errorf("abs session: nil session")
	}
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
	stored, err := p.GetABSSession(session.ID)
	if err != nil {
		return err
	}
	if stored == nil || stored.Revoked {
		return ErrABSSessionRevoked
	}
	if !absSessionUpdateFollows(stored, session) {
		return ErrABSSessionStale
	}
	return p.writeABSSessionLocked(session)
}

// absSessionUpdateFollows reports whether next is a legal successor of stored:
// see UpdateABSSession.
func absSessionUpdateFollows(stored, next *ABSSession) bool {
	switch next.Generation {
	case stored.Generation:
		return next.RefreshTokenHash == stored.RefreshTokenHash
	case stored.Generation + 1:
		return next.PrevRefreshTokenHash == stored.RefreshTokenHash
	default:
		return false
	}
}

// writeABSSessionLocked writes the record together with its per-user and
// refresh-token index entries in ONE batch, so no write can ever leave a record
// without the indexes revoke-all and the refresh lookup depend on. The caller holds
// absSessionWriteMu and has already re-read the stored row.
func (p *PebbleStore) writeABSSessionLocked(session *ABSSession) error {
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

// ExtendABSSession pushes a live session's ExpiresAt forward to expiresAt and
// stamps LastUsedAt = now, and changes nothing else. It is the write behind
// "authenticated use keeps a session alive" and is called from the bearer path on
// requests that may run in parallel with a refresh or a logout of the same session.
//
// The whole decision is made under absSessionWriteMu against a fresh read, never
// against the caller's copy, so it can neither un-rotate a refresh that landed in
// between nor un-revoke a logout. It writes only when ALL hold, and otherwise
// reports (false, nil):
//
//   - the session exists, is not revoked and is still live at now (an expired
//     session is dead; use must not resurrect it);
//   - its ExpiresAt is set (zero means "never expires" and must not be shortened);
//   - expiresAt is at least minAdvance past the stored ExpiresAt. This is the write
//     throttle: a burst of parallel cover requests produces one write, and a capped
//     session whose target has stopped moving produces none.
//
// The record is rewritten in the same batch as its index entries (unchanged
// values, so this is idempotent), so even a bug elsewhere cannot leave an extended
// record without its indexes.
func (p *PebbleStore) ExtendABSSession(id string, now, expiresAt time.Time, minAdvance time.Duration) (bool, error) {
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
	s, err := p.GetABSSession(id)
	if err != nil {
		return false, err
	}
	if s == nil || !s.Live(now) || s.ExpiresAt.IsZero() {
		return false, nil
	}
	if expiresAt.Sub(s.ExpiresAt) < minAdvance || !expiresAt.After(s.ExpiresAt) {
		return false, nil
	}
	s.ExpiresAt = expiresAt
	s.LastUsedAt = now
	if err := p.writeABSSessionLocked(s); err != nil {
		return false, err
	}
	return true, nil
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
	if session == nil || (hash != session.RefreshTokenHash && hash != session.PrevRefreshTokenHash) {
		// Dangling (record deleted) or retired (neither current nor previous).
		p.pruneStaleABSTokenIndex(key, hash, sessionID)
		return nil, nil
	}
	return session, nil
}

// pruneStaleABSTokenIndex deletes a refresh-token index entry that no longer
// resolves, re-checking under absSessionWriteMu so it can never remove an entry a
// concurrent write has just made valid. (Retired hashes never become valid again,
// since each generation's token is derived once and generations only increase,
// but the re-check makes that an enforced property rather than an argument.)
func (p *PebbleStore) pruneStaleABSTokenIndex(key []byte, hash, sessionID string) {
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
	v, closer, err := p.db.Get(key)
	if err != nil {
		return
	}
	current := string(v)
	closer.Close()
	if current != sessionID {
		return
	}
	s, err := p.GetABSSession(sessionID)
	if err != nil {
		return
	}
	if s != nil && (hash == s.RefreshTokenHash || hash == s.PrevRefreshTokenHash) {
		return
	}
	_ = p.db.Delete(key, pebble.Sync)
}

// BackfillABSSessionExpiry raises the ExpiresAt of every live, non-revoked session
// to at least floor(CreatedAt), and never lowers one. It returns how many rows it
// raised. Idempotent: once applied, a second run changes nothing.
//
// It exists for rows written before sliding expiry, whose ExpiresAt is login+30d,
// the same instant as their access token. Sliding expiry only moves ExpiresAt on a
// refresh or an authenticated request, so a device left unused across its 30-day
// mark would still have been logged out exactly as before. Raising those rows to
// CreatedAt plus the idle TTL gives the expired access token a live session to
// refresh against. LastUsedAt is deliberately not touched: nothing was used.
//
// Each row is re-read and rewritten under absSessionWriteMu, record and indexes in
// one batch, like every other ABS session write.
func (p *PebbleStore) BackfillABSSessionExpiry(now time.Time, floor func(createdAt time.Time) time.Time) (int, error) {
	prefix := []byte(absSessionKeyPrefix)
	var ids []string
	if err := forEachKeyInRange(p.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		ids = append(ids, strings.TrimPrefix(string(key), absSessionKeyPrefix))
		return nil
	}); err != nil {
		return 0, err
	}
	raised := 0
	for _, id := range ids {
		ok, err := p.raiseABSSessionExpiry(id, now, floor)
		if err != nil {
			return raised, err
		}
		if ok {
			raised++
		}
	}
	return raised, nil
}

func (p *PebbleStore) raiseABSSessionExpiry(id string, now time.Time, floor func(time.Time) time.Time) (bool, error) {
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
	s, err := p.GetABSSession(id)
	if err != nil || s == nil {
		// An unreadable row is left for the sweep; a vanished one needs nothing.
		return false, nil
	}
	if !s.Live(now) || s.ExpiresAt.IsZero() || s.CreatedAt.IsZero() {
		return false, nil
	}
	target := floor(s.CreatedAt)
	if !target.After(s.ExpiresAt) || !target.After(now) {
		return false, nil
	}
	s.ExpiresAt = target
	if err := p.writeABSSessionLocked(s); err != nil {
		return false, err
	}
	return true, nil
}

// ListABSSessionsForUser returns every stored ABS session for a user, newest first.
// Scoped strictly to the per-user index so one user can never observe another's
// sessions.
func (p *PebbleStore) ListABSSessionsForUser(userID string) ([]ABSSession, error) {
	prefix := []byte(absSessionUserIdxPfx + userID + ":")
	// A missing record (the getter's (nil, nil)) is a stale index entry and is
	// skipped. An unreadable member is NOT skipped silently: the readable
	// sessions come back together with an *UnreadableMembersError naming it, so
	// a listing can show what it has and say what it is missing, while any
	// caller that checks err != nil first still fails. An index-scan error is a
	// hard failure with nil rows. Until 2026-09-12 every member error was
	// skipped and the scan's own read error was never checked, so an unreadable
	// member came back as a short list with a nil error.
	out := make([]ABSSession, 0, 4)
	unreadable := &UnreadableMembersError{Op: "ListABSSessionsForUser " + userID}
	if err := forEachKeyInRange(p.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		id := strings.TrimPrefix(string(key), string(prefix))
		s, err := p.GetABSSession(id)
		if err != nil {
			unreadable.add(id, fmt.Errorf("reading session %s: %w", id, err))
			return nil
		}
		if s != nil {
			out = append(out, *s)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Newest first, matching ABS's /api/me/sessions ordering.
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].CreatedAt.After(out[b].CreatedAt)
	})
	return out, unreadable.orNil()
}

// RevokeABSSession marks a session revoked and drops its refresh-token index
// entries so the tokens stop resolving immediately. Revoking an unknown session is
// a no-op (idempotent logout).
func (p *PebbleStore) RevokeABSSession(id string) error {
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
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
//
// This is a security action, so it is best-effort per session, never
// all-or-nothing: every session that can be read is revoked even when another
// cannot be read, cannot be revoked, or the index scan fails partway. Every
// such failure is joined into the returned error (unreadable members as an
// *UnreadableMembersError), so the caller always learns the revoke was
// partial. It walks the index itself rather than calling
// ListABSSessionsForUser because a scan error there discards the rows it had
// already read, and here those rows must still be revoked.
func (p *PebbleStore) RevokeAllABSSessionsForUser(userID string) (int, error) {
	prefix := []byte(absSessionUserIdxPfx + userID + ":")
	unreadable := &UnreadableMembersError{Op: "RevokeAllABSSessionsForUser " + userID}
	var errs []error
	revoked := 0
	scanErr := forEachKeyInRange(p.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		id := strings.TrimPrefix(string(key), string(prefix))
		// Per-session read+revoke under the write lock (see absSessionWriteMu), so
		// a concurrent extension or refresh cannot write a live copy back over it.
		absSessionWriteMu.Lock()
		defer absSessionWriteMu.Unlock()
		s, err := p.GetABSSession(id)
		if err != nil {
			unreadable.add(id, fmt.Errorf("reading session %s: %w", id, err))
			return nil
		}
		if s == nil || s.Revoked {
			return nil
		}
		if err := p.revokeABSSessionRecord(s); err != nil {
			errs = append(errs, fmt.Errorf("RevokeAllABSSessionsForUser %s: revoking session %s: %w", userID, id, err))
			return nil
		}
		revoked++
		return nil
	})
	if scanErr != nil {
		errs = append(errs, fmt.Errorf("RevokeAllABSSessionsForUser %s: %w", userID, scanErr))
	}
	if u := unreadable.orNil(); u != nil {
		errs = append(errs, u)
	}
	return revoked, errors.Join(errs...)
}

// absSweepChunk bounds how many candidates DeleteExpiredABSSessions re-checks and
// deletes per hold of absSessionWriteMu, so a large sweep never stalls login,
// refresh or logout for longer than one small batch commit.
const absSweepChunk = 128

// absSweepAfterScanHook, when set, runs after DeleteExpiredABSSessions has
// collected its candidates and before it re-checks them under the lock. Test-only:
// it is how a test interleaves an extension between the scan and the delete. An
// atomic pointer so setting it from a test can never race a sweep that a
// background ticker started.
var absSweepAfterScanHook atomic.Pointer[func()]

// DeleteExpiredABSSessions removes revoked and past-expiry sessions along with both
// of their index entries. Mirrors DeleteExpiredSessions for the browser keyspace.
//
// It is two-phase. The scan only nominates candidates; each one is then re-read
// under absSessionWriteMu and deleted only if it is STILL revoked or expired at
// now, because an extension or refresh may have slid it between the scan and the
// delete. The record and its indexes go in one batch per chunk, under the same
// lock every writer takes, so a writer can neither resurrect a row the sweep just
// deleted nor have a row it just extended deleted out from under it.
func (p *PebbleStore) DeleteExpiredABSSessions(now time.Time) (int, error) {
	prefix := []byte(absSessionKeyPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return 0, err
	}
	var candidates []string
	for iter.First(); iter.Valid(); iter.Next() {
		var s ABSSession
		if err := json.Unmarshal(iter.Value(), &s); err != nil {
			continue
		}
		if absSessionSweepable(&s, now) {
			candidates = append(candidates, s.ID)
		}
	}
	iterErr := iter.Error()
	if cerr := iter.Close(); iterErr == nil {
		iterErr = cerr
	}
	if iterErr != nil {
		return 0, iterErr
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	if hook := absSweepAfterScanHook.Load(); hook != nil {
		(*hook)()
	}

	deleted := 0
	for start := 0; start < len(candidates); start += absSweepChunk {
		end := min(start+absSweepChunk, len(candidates))
		n, err := p.deleteABSSessionsIfSweepable(candidates[start:end], now)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// absSessionSweepable is the sweep's deletion rule: revoked, or expired at now.
// A zero ExpiresAt counts as expired, matching Live, which treats it as not live.
func absSessionSweepable(s *ABSSession, now time.Time) bool {
	return s.Revoked || s.ExpiresAt.IsZero() || !s.ExpiresAt.After(now)
}

// deleteABSSessionsIfSweepable re-reads each id under absSessionWriteMu and
// deletes the record plus its indexes, in one batch, only if it is still sweepable.
func (p *PebbleStore) deleteABSSessionsIfSweepable(ids []string, now time.Time) (int, error) {
	absSessionWriteMu.Lock()
	defer absSessionWriteMu.Unlock()
	batch := p.db.NewBatch()
	defer batch.Close()
	deleted := 0
	for _, id := range ids {
		s, err := p.GetABSSession(id)
		if err != nil {
			// An unreadable row cannot be judged; leave it for a later sweep rather
			// than delete something that may be live.
			continue
		}
		if s == nil || !absSessionSweepable(s, now) {
			continue
		}
		if err := batch.Delete(absSessionKey(s.ID), nil); err != nil {
			return 0, err
		}
		if err := batch.Delete(absSessionUserKey(s.UserID, s.ID), nil); err != nil {
			return 0, err
		}
		for _, h := range []string{s.RefreshTokenHash, s.PrevRefreshTokenHash} {
			if h == "" {
				continue
			}
			if err := batch.Delete(absSessionTokenKey(h), nil); err != nil {
				return 0, err
			}
		}
		deleted++
	}
	if deleted == 0 {
		return 0, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return deleted, nil
}
