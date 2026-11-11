// file: core/internal/authstore/store.go
// version: 1.0.0
// guid: a7b06243-983e-42c3-9979-3aacdb3d3374
// last-edited: 2026-11-10

// Package authstore is core's one implementation of auth.Store and
// auth.Importer (05-authn §3.4, §11 "Native store"): every identity record in
// the core keyspace c/auth/, as codec.JSON[T](1) of the core type plus the
// credential hash the record needs. It implements auth (layer L4) and is
// imported only by the kernel and by tests.
//
// It holds no cache and no lock. The whole keyspace is Serialized, so every
// Set and Delete carries an Expect on its key: on the bytes the store read
// for a write derived from a record, absent for a create, and the read bytes
// plus a Delete for every Take*. A write built from a stale read therefore
// fails the batch's Commit with storage.ErrConflict instead of overwriting a
// newer record (DATA4-9, SEC6-3, DATA6-8, R6-5).
//
// Keys (relative to c/auth/, tuple-encoded by storage/keys):
//
//	user/<id>                              User + password hash
//	user/name/<fold>                       -> id
//	apikey/<id>                            APIKey + token hash
//	apikey/h/<sha256 hex>                  -> id
//	apikey/u/<user>/<id>                   (empty)
//	identity/<provider>/<subject>          ExternalIdentity
//	identity/u/<user>/<provider>/<subject> (empty)
//	inviteallow/<folded e-mail>            allow-list entry
//	session/h/<sha256 hex>                 SessionRecord + token hash
//	session/id/<publicID>                  -> sha256 hex
//	session/u/<user>/<publicID>            -> sha256 hex
//	onetime/<kind>/<sha256 hex>            OneTimeToken + token hash
//
// A record row and an index row of one family differ in segment count, which
// is how a scan tells them apart. No raw credential is ever stored: the store
// mints the token, keeps its SHA-256 and returns the raw value once.
package authstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/netip"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the c/auth/ keyspace.
const Owner storage.Owner = "core/auth"

// StorageSpec is the c/auth/ spec the kernel registers for Owner: a core
// keyspace, Serialized as a whole, so every write in it carries an Expect
// (05-storage R6-1).
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{""}}
}

// Key segments.
const (
	segUser     = "user"
	segName     = "name"
	segKey      = "apikey"
	segHash     = "h"
	segByUser   = "u"
	segIdentity = "identity"
	segAllow    = "inviteallow"
	segSession  = "session"
	segID       = "id"
	segOneTime  = "onetime"
)

// Stored forms. Each embeds the core type and adds what the store alone
// needs; HasPassword is derived from the hash on decode.
type userRecord struct {
	auth.User
	PasswordHash string `json:"password_hash"`
}

type keyRecord struct {
	auth.APIKey
	Hash string `json:"hash"`
}

type sessionRecord struct {
	auth.SessionRecord
	Hash string `json:"hash"`
}

type oneTimeRecord struct {
	auth.OneTimeToken
	Hash string `json:"hash"`
}

type allowRecord struct {
	Email string `json:"email"`
}

// Options is what New takes beyond the keyspace.
type Options struct {
	// Now stamps records whose time the caller left zero; nil is time.Now.
	Now func() time.Time
	// Entropy is the randomness of IDs; nil is crypto/rand. Credentials are
	// always minted from crypto/rand (auth.MintCredential).
	Entropy io.Reader
}

// Store is the native identity store. It implements auth.Store and
// auth.Importer.
type Store struct {
	ks      storage.Keyspace
	now     func() time.Time
	entropy io.Reader
}

var (
	_ auth.Store    = (*Store)(nil)
	_ auth.Importer = (*Store)(nil)
)

// New returns the store over ks, which must be Owner's keyspace. It writes
// nothing (DATA4-10).
func New(ks storage.Keyspace, opts Options) (*Store, error) {
	if ks.Owner() != Owner {
		return nil, fmt.Errorf("authstore: keyspace of %q, want %q", ks.Owner(), Owner)
	}
	s := &Store{ks: ks, now: opts.Now, entropy: opts.Entropy}
	if s.now == nil {
		s.now = time.Now
	}
	if s.entropy == nil {
		s.entropy = rand.Reader
	}
	return s, nil
}

// NewBatch opens an indexed write batch over the identity keyspace; the
// kernel hands it to auth.Service as Deps.NewBatch. Indexed, because one
// batch may create a user and then a record that needs it as owner (the
// bootstrap redeem creates the admin and mints its key together): the owner
// check reads through the batch's own writes only on an indexed batch.
//
// Every other read of the store is of committed state, so a batch stages at
// most one write per record: a second write to a record the same batch
// already changed would be derived from the committed bytes, not from the
// first write.
func (s *Store) NewBatch() *storage.Batch { return s.ks.NewIndexedBatch() }

// Keyspace is the identity keyspace (the kernel binds it into multi-owner
// batches, and the crash tests read it).
func (s *Store) Keyspace() storage.Keyspace { return s.ks }

// ---------------------------------------------------------------------------
// Keys.

func userKey(id auth.UserID) []byte { return keys.Join(keys.Str(segUser), keys.Str(string(id))) }
func userNameKey(fold string) []byte {
	return keys.Join(keys.Str(segUser), keys.Str(segName), keys.Str(fold))
}
func apiKeyKey(id string) []byte { return keys.Join(keys.Str(segKey), keys.Str(id)) }
func apiKeyHashKey(h string) []byte {
	return keys.Join(keys.Str(segKey), keys.Str(segHash), keys.Str(h))
}
func identityKey(p, sub string) []byte {
	return keys.Join(keys.Str(segIdentity), keys.Str(p), keys.Str(sub))
}
func allowKey(fold string) []byte { return keys.Join(keys.Str(segAllow), keys.Str(fold)) }
func sessionKey(h string) []byte {
	return keys.Join(keys.Str(segSession), keys.Str(segHash), keys.Str(h))
}
func sessionIDKey(pid string) []byte {
	return keys.Join(keys.Str(segSession), keys.Str(segID), keys.Str(pid))
}

func apiKeyUserKey(u auth.UserID, id string) []byte {
	return keys.Join(keys.Str(segKey), keys.Str(segByUser), keys.Str(string(u)), keys.Str(id))
}

func identityUserKey(u auth.UserID, p, sub string) []byte {
	return keys.Join(keys.Str(segIdentity), keys.Str(segByUser), keys.Str(string(u)), keys.Str(p), keys.Str(sub))
}

func sessionUserKey(u auth.UserID, pid string) []byte {
	return keys.Join(keys.Str(segSession), keys.Str(segByUser), keys.Str(string(u)), keys.Str(pid))
}

func oneTimeKey(k auth.OneTimeKind, h string) []byte {
	return keys.Join(keys.Str(segOneTime), keys.Str(string(k)), keys.Str(h))
}

// keyDesc describes key for an error: its family and its length, never its
// bytes. Session, API-key and one-time keys embed a credential's SHA-256,
// and an error can reach a log (the stage-12 touch's Warn), which must never
// carry a credential or its hash (STOP 3). storage.keyRef follows the same
// rule for the engine's own errors.
func keyDesc(key []byte) string {
	family := "unrecognised"
	if segs, err := keys.Split(key); err == nil && len(segs) > 0 {
		switch top := string(segs[0]); top {
		case segUser, segKey, segIdentity, segAllow, segSession, segOneTime:
			family = top
			if len(segs) > 2 {
				switch sub := string(segs[1]); sub {
				case segName, segHash, segByUser, segID,
					string(auth.OneTimeInvite), string(auth.OneTimeTempLogin), string(auth.OneTimeBootstrap):
					family += "/" + sub
				}
			}
		}
	}
	return fmt.Sprintf("%s key len=%d", family, len(key))
}

// ---------------------------------------------------------------------------
// Low-level reads and staged writes.

// committed reads key's committed value: nil when absent. The Store's writes
// carry no context, so the read takes a background one: one point read of
// the local engine, not cancellable work.
func (s *Store) committed(key []byte) ([]byte, error) {
	raw, err := s.ks.Get(context.Background(), key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", auth.ErrStoreUnavailable, keyDesc(key), err)
	}
	return raw, nil
}

// get reads key for a lookup: auth.ErrNotFound when absent.
func (s *Store) get(ctx context.Context, key []byte) ([]byte, error) {
	raw, err := s.ks.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, auth.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrStoreUnavailable, err)
	}
	return raw, nil
}

// set binds the keyspace, records Expect(key, want) (nil: absent) and stages
// key = raw.
func (s *Store) set(b *storage.Batch, key, want, raw []byte) error {
	b.Bind(s.ks)
	if err := b.Expect(s.ks, key, want); err != nil {
		return fmt.Errorf("authstore: expect %s: %w", keyDesc(key), err)
	}
	if err := b.In(s.ks).Set(key, raw); err != nil {
		return fmt.Errorf("authstore: stage %s: %w", keyDesc(key), err)
	}
	return nil
}

// del stages the deletion of key with an Expect on want, the bytes read.
func (s *Store) del(b *storage.Batch, key, want []byte) error {
	b.Bind(s.ks)
	if err := b.Expect(s.ks, key, want); err != nil {
		return fmt.Errorf("authstore: expect %s: %w", keyDesc(key), err)
	}
	if err := b.In(s.ks).Delete(key); err != nil {
		return fmt.Errorf("authstore: stage delete %s: %w", keyDesc(key), err)
	}
	return nil
}

// delCommitted deletes key when it is present, expecting its committed value.
func (s *Store) delCommitted(b *storage.Batch, key []byte) error {
	raw, err := s.committed(key)
	if err != nil || raw == nil {
		return err
	}
	return s.del(b, key, raw)
}

func needBatch(b *storage.Batch, op string) error {
	if b == nil {
		return fmt.Errorf("authstore: %s needs the caller's batch", op)
	}
	return nil
}

// newID is a ULID (48 bits of Unix milliseconds, 80 random bits, Crockford
// base32), so IDs sort by creation time.
func (s *Store) newID() (string, error) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	if _, err := io.ReadFull(s.entropy, b[6:]); err != nil {
		return "", fmt.Errorf("authstore: ID entropy: %w", err)
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(s.now().UnixMilli()))
	copy(b[:6], ts[2:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}

// segs splits a scanned key, or reports false for bytes Join cannot have made.
func segs(key []byte) ([]keys.Seg, bool) {
	ss, err := keys.Split(key)
	return ss, err == nil
}

// ---------------------------------------------------------------------------
// Users.

func validStatus(st auth.UserStatus) bool {
	switch st {
	case auth.StatusActive, auth.StatusDisabled, auth.StatusPending, auth.StatusSystem:
		return true
	}
	return false
}

func encodeUser(u *auth.User, hash string) ([]byte, error) {
	rec := userRecord{User: *u, PasswordHash: hash}
	rec.HasPassword = hash != ""
	return codec.JSON[userRecord](1).Encode(rec)
}

// decodeUser decodes the row of id. A row that does not decode, names
// another user or holds an unknown status is ErrUndecodableRecord (§6: the
// record is treated as restricted).
func decodeUser(id auth.UserID, raw []byte) (*auth.User, string, error) {
	rec, err := codec.JSON[userRecord](1).Decode(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%w: user %s: %v", auth.ErrUndecodableRecord, id, err)
	}
	if rec.ID != id || id == "" {
		return nil, "", fmt.Errorf("%w: row of user %s holds user %q", auth.ErrUndecodableRecord, id, rec.ID)
	}
	if !validStatus(rec.Status) {
		return nil, "", fmt.Errorf("%w: user %s has status %q", auth.ErrUndecodableRecord, id, rec.Status)
	}
	u := rec.User
	u.HasPassword = rec.PasswordHash != ""
	return &u, rec.PasswordHash, nil
}

// PutUser stages u, keeping the stored password hash (auth.UserStore).
func (s *Store) PutUser(b *storage.Batch, u *auth.User) error {
	return s.putUser(b, u, nil)
}

// SetPassword stages u with hash as its password ("" = none).
func (s *Store) SetPassword(b *storage.Batch, u *auth.User, hash string) error {
	return s.putUser(b, u, &hash)
}

func (s *Store) putUser(b *storage.Batch, u *auth.User, hash *string) error {
	if err := needBatch(b, "PutUser"); err != nil {
		return err
	}
	if u == nil || u.ID == "" || strings.TrimSpace(u.Username) == "" {
		return fmt.Errorf("%w: a user needs an ID and a username", auth.ErrBadInput)
	}
	if !validStatus(u.Status) {
		return fmt.Errorf("%w: user status %q", auth.ErrBadInput, u.Status)
	}
	if hash != nil && *hash != "" {
		if a := password.AlgoOf(*hash); a != password.AlgoArgon2id && a != password.AlgoBcrypt {
			return fmt.Errorf("%w: password hash is not argon2id or bcrypt", auth.ErrBadInput)
		}
	}
	key := userKey(u.ID)
	cur, err := s.committed(key)
	if err != nil {
		return err
	}
	var stored *auth.User
	storedHash := ""
	switch {
	case cur == nil && u.Version != 1:
		return fmt.Errorf("%w: user %s is not stored, so its first version is 1 (got %d)", storage.ErrConflict, u.ID, u.Version)
	case cur != nil:
		if stored, storedHash, err = decodeUser(u.ID, cur); err != nil {
			return err
		}
		if stored.Version != u.Version-1 {
			return fmt.Errorf("%w: user %s is at version %d, the write replaces %d", storage.ErrConflict, u.ID, stored.Version, u.Version-1)
		}
	}
	if hash != nil {
		storedHash = *hash
	}
	raw, err := encodeUser(u, storedHash)
	if err != nil {
		return fmt.Errorf("authstore: encode user %s: %w", u.ID, err)
	}
	newFold := auth.FoldUsername(u.Username)
	oldFold := ""
	if stored != nil {
		oldFold = auth.FoldUsername(stored.Username)
	}
	if newFold != oldFold {
		taken, err := s.committed(userNameKey(newFold))
		if err != nil {
			return err
		}
		if taken != nil && string(taken) != string(u.ID) {
			return fmt.Errorf("%w: username %q", auth.ErrUserExists, u.Username)
		}
		if oldFold != "" {
			if err := s.delCommitted(b, userNameKey(oldFold)); err != nil {
				return err
			}
		}
		if err := s.set(b, userNameKey(newFold), taken, []byte(u.ID)); err != nil {
			return err
		}
	}
	return s.set(b, key, cur, raw)
}

// DeleteUser stages the deletion of u and its username index.
func (s *Store) DeleteUser(b *storage.Batch, u *auth.User) error {
	if err := needBatch(b, "DeleteUser"); err != nil {
		return err
	}
	if u == nil || u.ID == "" {
		return fmt.Errorf("%w: DeleteUser of a user with no ID", auth.ErrBadInput)
	}
	key := userKey(u.ID)
	cur, err := s.committed(key)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: user %s", auth.ErrNotFound, u.ID)
	}
	stored, _, err := decodeUser(u.ID, cur)
	if err != nil {
		return err
	}
	if stored.Version != u.Version {
		return fmt.Errorf("%w: user %s is at version %d, the delete names %d", storage.ErrConflict, u.ID, stored.Version, u.Version)
	}
	if err := s.delCommitted(b, userNameKey(auth.FoldUsername(stored.Username))); err != nil {
		return err
	}
	return s.del(b, key, cur)
}

// GuardUser stages an Expect that u is stored at u.Version.
func (s *Store) GuardUser(b *storage.Batch, u *auth.User) error {
	if err := needBatch(b, "GuardUser"); err != nil {
		return err
	}
	if u == nil || u.ID == "" {
		return fmt.Errorf("%w: GuardUser of a user with no ID", auth.ErrBadInput)
	}
	key := userKey(u.ID)
	cur, err := s.committed(key)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: user %s is gone", storage.ErrConflict, u.ID)
	}
	stored, _, err := decodeUser(u.ID, cur)
	if err != nil {
		return err
	}
	if stored.Version != u.Version {
		return fmt.Errorf("%w: user %s is at version %d, guarded %d", storage.ErrConflict, u.ID, stored.Version, u.Version)
	}
	b.Bind(s.ks)
	return b.Expect(s.ks, key, cur)
}

// GetUser reads user id.
func (s *Store) GetUser(ctx context.Context, id auth.UserID) (*auth.User, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty user ID", auth.ErrNotFound)
	}
	raw, err := s.get(ctx, userKey(id))
	if err != nil {
		return nil, err
	}
	u, _, err := decodeUser(id, raw)
	return u, err
}

// PasswordHash returns the stored PHC hash of id ("" when none).
func (s *Store) PasswordHash(ctx context.Context, id auth.UserID) (string, error) {
	raw, err := s.get(ctx, userKey(id))
	if err != nil {
		return "", err
	}
	_, h, err := decodeUser(id, raw)
	return h, err
}

// UserByUsername reads the user whose folded username is fold.
func (s *Store) UserByUsername(ctx context.Context, fold string) (*auth.User, error) {
	if fold == "" {
		return nil, fmt.Errorf("%w: empty username", auth.ErrNotFound)
	}
	id, err := s.get(ctx, userNameKey(fold))
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, auth.UserID(id))
}

// ScanUsers yields every stored user in ID order; an undecodable row yields
// ErrUndecodableRecord for that row and the scan goes on.
func (s *Store) ScanUsers(ctx context.Context) iter.Seq2[*auth.User, error] {
	return func(yield func(*auth.User, error) bool) {
		for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(segUser))) {
			if err != nil {
				yield(nil, fmt.Errorf("%w: scan users: %w", auth.ErrStoreUnavailable, err))
				return
			}
			ss, ok := segs(kv.Key)
			if ok && len(ss) == 3 && string(ss[1]) == segName {
				continue // the username index
			}
			if !ok || len(ss) != 2 {
				if !yield(nil, fmt.Errorf("%w: %s is not user/<id>", auth.ErrUndecodableRecord, keyDesc(kv.Key))) {
					return
				}
				continue
			}
			u, _, err := decodeUser(auth.UserID(ss[1]), kv.Value)
			if !yield(u, err) {
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Sessions.

func decodeSession(raw []byte) (*sessionRecord, error) {
	rec, err := codec.JSON[sessionRecord](1).Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: session: %v", auth.ErrUndecodableRecord, err)
	}
	if rec.PublicID == "" || rec.UserID == "" || rec.Hash == "" {
		return nil, fmt.Errorf("%w: session record without its public ID, user or hash", auth.ErrUndecodableRecord)
	}
	return &rec, nil
}

// IssueSession mints an mcs_ token and stages the session record and its
// indexes. It fills s.PublicID (when empty) and s.Version.
func (s *Store) IssueSession(b *storage.Batch, rec *auth.SessionRecord) (string, error) {
	if err := needBatch(b, "IssueSession"); err != nil {
		return "", err
	}
	if rec == nil || rec.UserID == "" {
		return "", fmt.Errorf("%w: a session needs a user", auth.ErrBadInput)
	}
	if rec.ExpiresAt.IsZero() {
		return "", fmt.Errorf("%w: a session needs an expiry", auth.ErrBadInput)
	}
	if rec.PublicID == "" {
		id, err := s.newID()
		if err != nil {
			return "", err
		}
		rec.PublicID = id
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = s.now().UTC()
	}
	rec.Version = 1
	token, err := auth.MintCredential(auth.KindSession)
	if err != nil {
		return "", err
	}
	h := auth.HashHex(token)
	raw, err := codec.JSON[sessionRecord](1).Encode(sessionRecord{SessionRecord: *rec, Hash: h})
	if err != nil {
		return "", fmt.Errorf("authstore: encode session: %w", err)
	}
	if err := s.set(b, sessionKey(h), nil, raw); err != nil {
		return "", err
	}
	if err := s.set(b, sessionIDKey(rec.PublicID), nil, []byte(h)); err != nil {
		return "", err
	}
	if err := s.set(b, sessionUserKey(rec.UserID, rec.PublicID), nil, []byte(h)); err != nil {
		return "", err
	}
	return token, nil
}

// SessionByToken looks a session up by the SHA-256 of token.
func (s *Store) SessionByToken(ctx context.Context, token string) (*auth.SessionRecord, error) {
	if token == "" {
		return nil, auth.ErrNotFound
	}
	raw, err := s.get(ctx, sessionKey(auth.HashHex(token)))
	if err != nil {
		return nil, err
	}
	rec, err := decodeSession(raw)
	if err != nil {
		return nil, err
	}
	return &rec.SessionRecord, nil
}

// sessionByPID reads the record of public ID pid with its raw bytes.
func (s *Store) sessionByPID(ctx context.Context, pid string) (*sessionRecord, []byte, error) {
	if pid == "" {
		return nil, nil, auth.ErrNotFound
	}
	h, err := s.get(ctx, sessionIDKey(pid))
	if err != nil {
		return nil, nil, err
	}
	raw, err := s.get(ctx, sessionKey(string(h)))
	if err != nil {
		return nil, nil, err
	}
	rec, err := decodeSession(raw)
	if err != nil {
		return nil, nil, err
	}
	if rec.PublicID != pid {
		return nil, nil, fmt.Errorf("%w: session index %s names session %s", auth.ErrUndecodableRecord, pid, rec.PublicID)
	}
	return rec, raw, nil
}

// SessionByPublicID reads session pid.
func (s *Store) SessionByPublicID(ctx context.Context, pid string) (*auth.SessionRecord, error) {
	rec, _, err := s.sessionByPID(ctx, pid)
	if err != nil {
		return nil, err
	}
	return &rec.SessionRecord, nil
}

// ScanUserSessions yields u's sessions in public-ID order.
func (s *Store) ScanUserSessions(ctx context.Context, u auth.UserID) iter.Seq2[*auth.SessionRecord, error] {
	return func(yield func(*auth.SessionRecord, error) bool) {
		if u == "" {
			return
		}
		prefix := keys.Prefix(keys.Str(segSession), keys.Str(segByUser), keys.Str(string(u)))
		for kv, err := range s.ks.ScanPrefix(ctx, prefix) {
			if err != nil {
				yield(nil, fmt.Errorf("%w: scan sessions: %w", auth.ErrStoreUnavailable, err))
				return
			}
			raw, err := s.get(ctx, sessionKey(string(kv.Value)))
			if errors.Is(err, auth.ErrNotFound) {
				continue // an index row whose session is gone
			}
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			rec, err := decodeSession(raw)
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if !yield(&rec.SessionRecord, nil) {
				return
			}
		}
	}
}

// deleteSessionRows stages the deletion of rec's three rows.
func (s *Store) deleteSessionRows(b *storage.Batch, rec *sessionRecord, raw []byte) error {
	if err := s.del(b, sessionKey(rec.Hash), raw); err != nil {
		return err
	}
	if err := s.delCommitted(b, sessionIDKey(rec.PublicID)); err != nil {
		return err
	}
	return s.delCommitted(b, sessionUserKey(rec.UserID, rec.PublicID))
}

// DeleteSession stages the deletion of rec; an absent session stages
// nothing.
func (s *Store) DeleteSession(b *storage.Batch, rec *auth.SessionRecord) error {
	if err := needBatch(b, "DeleteSession"); err != nil {
		return err
	}
	if rec == nil {
		return nil
	}
	cur, raw, err := s.sessionByPID(context.Background(), rec.PublicID)
	if errors.Is(err, auth.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.deleteSessionRows(b, cur, raw)
}

// TouchSession sets LastSeen = at on the current record of pid; it stages
// nothing for an absent, expired or epoch-stale record, or one whose user is
// not active (DATA6-8).
func (s *Store) TouchSession(b *storage.Batch, pid string, at time.Time) error {
	if err := needBatch(b, "TouchSession"); err != nil {
		return err
	}
	ctx := context.Background()
	rec, raw, err := s.sessionByPID(ctx, pid)
	if errors.Is(err, auth.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !at.Before(rec.ExpiresAt) || !at.After(rec.LastSeen) {
		return nil
	}
	u, err := s.GetUser(ctx, rec.UserID)
	if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrUndecodableRecord) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.Status != auth.StatusActive || u.CredentialEpoch != rec.CredentialEpoch {
		return nil
	}
	next := *rec
	next.LastSeen = at
	out, err := codec.JSON[sessionRecord](1).Encode(next)
	if err != nil {
		return fmt.Errorf("authstore: encode session: %w", err)
	}
	return s.set(b, sessionKey(rec.Hash), raw, out)
}

// RestampSession stages rec with CredentialEpoch epoch.
func (s *Store) RestampSession(b *storage.Batch, rec *auth.SessionRecord, epoch int64) error {
	if err := needBatch(b, "RestampSession"); err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("%w: RestampSession of no session", auth.ErrBadInput)
	}
	cur, raw, err := s.sessionByPID(context.Background(), rec.PublicID)
	if err != nil {
		return err
	}
	next := *cur
	next.CredentialEpoch = epoch
	next.Version++
	out, err := codec.JSON[sessionRecord](1).Encode(next)
	if err != nil {
		return fmt.Errorf("authstore: encode session: %w", err)
	}
	return s.set(b, sessionKey(cur.Hash), raw, out)
}

// RevokeSignInsBefore deletes every session and one-time token created
// before t (zero t: all) and returns how many it staged.
func (s *Store) RevokeSignInsBefore(b *storage.Batch, t time.Time) (int, error) {
	if err := needBatch(b, "RevokeSignInsBefore"); err != nil {
		return 0, err
	}
	ctx := context.Background()
	n := 0
	before := func(c time.Time) bool { return t.IsZero() || c.Before(t) }
	for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(segSession), keys.Str(segHash))) {
		if err != nil {
			return n, fmt.Errorf("%w: scan sessions: %w", auth.ErrStoreUnavailable, err)
		}
		rec, err := decodeSession(kv.Value)
		if err != nil {
			// An undecodable session can authenticate no one; delete the row.
			if err := s.del(b, kv.Clone().Key, kv.Value); err != nil {
				return n, err
			}
			n++
			continue
		}
		if !before(rec.CreatedAt) {
			continue
		}
		if err := s.deleteSessionRows(b, rec, kv.Clone().Value); err != nil {
			return n, err
		}
		n++
	}
	for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(segOneTime))) {
		if err != nil {
			return n, fmt.Errorf("%w: scan one-time tokens: %w", auth.ErrStoreUnavailable, err)
		}
		kv = kv.Clone()
		if rec, err := decodeOneTime(kv.Value); err == nil && !before(rec.CreatedAt) {
			continue
		}
		if err := s.del(b, kv.Key, kv.Value); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// API keys.

func decodeKey(id string, raw []byte) (*keyRecord, error) {
	rec, err := codec.JSON[keyRecord](1).Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: apikey %s: %v", auth.ErrUndecodableRecord, id, err)
	}
	if rec.ID != id || id == "" || rec.Owner == "" || rec.Hash == "" {
		return nil, fmt.Errorf("%w: row of apikey %s holds key %q", auth.ErrUndecodableRecord, id, rec.ID)
	}
	// A stored key is never empty (R3-1): a record with no scope would compile
	// as a session (nil scopes = the owner's whole role set), so it is refused
	// as damaged rather than read (§6, seams.d/F4-09.md).
	if len(rec.Scopes) == 0 {
		return nil, fmt.Errorf("%w: apikey %s has no scope", auth.ErrUndecodableRecord, id)
	}
	if !validKeyStatus(rec.Status) {
		return nil, fmt.Errorf("%w: apikey %s has status %q", auth.ErrUndecodableRecord, id, rec.Status)
	}
	if !rec.RevokedAt.IsZero() && rec.Status != auth.KeyRevoked {
		return nil, fmt.Errorf("%w: apikey %s has revoked_at set and status %q", auth.ErrUndecodableRecord, id, rec.Status)
	}
	return &rec, nil
}

func validKeyStatus(st auth.KeyStatus) bool {
	switch st {
	case auth.KeyActive, auth.KeyDisabled, auth.KeyNeedsConfirmation, auth.KeyRevoked:
		return true
	}
	return false
}

// checkScopes refuses an empty list, a wildcard or a malformed scope.
func checkScopes(ps []rbac.Permission) error {
	if len(ps) == 0 {
		return errors.New("an API key holds at least one scope")
	}
	for _, p := range ps {
		if _, err := rbac.ParsePermission(string(p)); err != nil {
			return fmt.Errorf("scope %q: %w", p, err)
		}
	}
	return nil
}

// ownerPresent reads the owner's user row through b (GetForUpdate), so the
// batch fails if the owner is deleted before it commits. An owner staged
// earlier in the same batch is found only when b is indexed (NewBatch).
func (s *Store) ownerPresent(b *storage.Batch, u auth.UserID) error {
	if u == "" {
		return errors.New("no owner")
	}
	b.Bind(s.ks)
	_, err := b.GetForUpdate(context.Background(), s.ks, userKey(u))
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("owner %s is not stored", u)
	}
	return err
}

// IssueAPIKey mints an mck_ key and stages k. It fills k.ID (when empty),
// k.HashPrefix, k.Version and k.CreatedAt (when zero).
func (s *Store) IssueAPIKey(b *storage.Batch, k *auth.APIKey) (string, error) {
	if err := needBatch(b, "IssueAPIKey"); err != nil {
		return "", err
	}
	if k == nil {
		return "", fmt.Errorf("%w: nil key", auth.ErrBadInput)
	}
	if err := checkScopes(k.Scopes); err != nil {
		return "", fmt.Errorf("%w: %w", auth.ErrBadInput, err)
	}
	if k.Status != auth.KeyActive {
		return "", fmt.Errorf("%w: a new key is active", auth.ErrBadInput)
	}
	if k.ExpiresAt.IsZero() {
		return "", fmt.Errorf("%w: a new key has an expiry", auth.ErrBadInput)
	}
	if err := s.ownerPresent(b, k.Owner); err != nil {
		return "", fmt.Errorf("%w: %w", auth.ErrBadInput, err)
	}
	raw, err := auth.MintCredential(auth.KindAPIKey)
	if err != nil {
		return "", err
	}
	if err := s.stageNewKey(b, k, auth.HashHex(raw), false); err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Store) stageNewKey(b *storage.Batch, k *auth.APIKey, h string, imported bool) error {
	if k.ID == "" {
		id, err := s.newID()
		if err != nil {
			return err
		}
		k.ID = id
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = s.now().UTC()
	}
	k.Imported = imported
	k.HashPrefix = h[:8]
	k.Version = 1
	if cur, err := s.committed(apiKeyKey(k.ID)); err != nil {
		return err
	} else if cur != nil {
		return fmt.Errorf("%w: apikey %s exists", auth.ErrBadInput, k.ID)
	}
	if cur, err := s.committed(apiKeyHashKey(h)); err != nil {
		return err
	} else if cur != nil {
		return fmt.Errorf("%w: a key with this hash exists", auth.ErrBadInput)
	}
	raw, err := codec.JSON[keyRecord](1).Encode(keyRecord{APIKey: *k, Hash: h})
	if err != nil {
		return fmt.Errorf("authstore: encode apikey: %w", err)
	}
	if err := s.set(b, apiKeyKey(k.ID), nil, raw); err != nil {
		return err
	}
	if err := s.set(b, apiKeyHashKey(h), nil, []byte(k.ID)); err != nil {
		return err
	}
	return s.set(b, apiKeyUserKey(k.Owner, k.ID), nil, []byte{})
}

// PutAPIKey stages k's status, expiry, revocation time and name over the
// stored record; every other field is the stored one. k.Version must be the
// stored Version + 1. A revoked key stays revoked: a write that would move a
// stored revoked key to any other status, or clear its revocation time, is
// refused with auth.ErrKeyRevoked (§4.4, early fix 3; core checks first).
func (s *Store) PutAPIKey(b *storage.Batch, k *auth.APIKey) error {
	if err := needBatch(b, "PutAPIKey"); err != nil {
		return err
	}
	if k == nil || k.ID == "" {
		return fmt.Errorf("%w: PutAPIKey of a key with no ID", auth.ErrBadInput)
	}
	if !validKeyStatus(k.Status) {
		return fmt.Errorf("%w: key status %q", auth.ErrBadInput, k.Status)
	}
	key := apiKeyKey(k.ID)
	cur, err := s.committed(key)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: apikey %s", auth.ErrNotFound, k.ID)
	}
	rec, err := decodeKey(k.ID, cur)
	if err != nil {
		return err
	}
	if rec.Version != k.Version-1 {
		return fmt.Errorf("%w: apikey %s is at version %d, the write replaces %d", storage.ErrConflict, k.ID, rec.Version, k.Version-1)
	}
	if rec.Status == auth.KeyRevoked && (k.Status != auth.KeyRevoked || k.RevokedAt.IsZero()) {
		return fmt.Errorf("%w: apikey %s", auth.ErrKeyRevoked, k.ID)
	}
	next := *rec
	next.Status = k.Status
	next.ExpiresAt = k.ExpiresAt
	next.Name = k.Name
	next.RevokedAt = k.RevokedAt
	next.Version = k.Version
	if next.Status == auth.KeyRevoked && next.RevokedAt.IsZero() {
		next.RevokedAt = s.now().UTC()
	}
	if next.Status != auth.KeyRevoked && !next.RevokedAt.IsZero() {
		return fmt.Errorf("%w: revoked_at set on a key that is not revoked", auth.ErrBadInput)
	}
	raw, err := codec.JSON[keyRecord](1).Encode(next)
	if err != nil {
		return fmt.Errorf("authstore: encode apikey: %w", err)
	}
	return s.set(b, key, cur, raw)
}

// TouchAPIKey records a key's last use; it stages nothing for an absent key
// or one that is not active or has expired, and never touches status or
// revocation time.
func (s *Store) TouchAPIKey(b *storage.Batch, id string, at time.Time, ip netip.Addr) error {
	if err := needBatch(b, "TouchAPIKey"); err != nil {
		return err
	}
	key := apiKeyKey(id)
	cur, err := s.committed(key)
	if err != nil || cur == nil {
		return err
	}
	rec, err := decodeKey(id, cur)
	if errors.Is(err, auth.ErrUndecodableRecord) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.Status != auth.KeyActive || !rec.RevokedAt.IsZero() || !at.Before(rec.ExpiresAt) {
		return nil
	}
	next := *rec
	next.LastUsedAt = at
	next.LastUsedIP = ip
	next.UseCount++
	raw, err := codec.JSON[keyRecord](1).Encode(next)
	if err != nil {
		return fmt.Errorf("authstore: encode apikey: %w", err)
	}
	return s.set(b, key, cur, raw)
}

// APIKeyByToken looks a key up by the SHA-256 of its raw value (a native mck_
// key or an imported one).
func (s *Store) APIKeyByToken(ctx context.Context, raw string) (*auth.APIKey, error) {
	if raw == "" {
		return nil, auth.ErrNotFound
	}
	id, err := s.get(ctx, apiKeyHashKey(auth.HashHex(raw)))
	if err != nil {
		return nil, err
	}
	return s.APIKeyByID(ctx, string(id))
}

// APIKeyByID reads key id.
func (s *Store) APIKeyByID(ctx context.Context, id string) (*auth.APIKey, error) {
	if id == "" {
		return nil, auth.ErrNotFound
	}
	raw, err := s.get(ctx, apiKeyKey(id))
	if err != nil {
		return nil, err
	}
	rec, err := decodeKey(id, raw)
	if err != nil {
		return nil, err
	}
	return &rec.APIKey, nil
}

// ScanAPIKeys yields owner's keys ("" = every key) in ID order.
func (s *Store) ScanAPIKeys(ctx context.Context, owner auth.UserID) iter.Seq2[*auth.APIKey, error] {
	return func(yield func(*auth.APIKey, error) bool) {
		if owner != "" {
			prefix := keys.Prefix(keys.Str(segKey), keys.Str(segByUser), keys.Str(string(owner)))
			for kv, err := range s.ks.ScanPrefix(ctx, prefix) {
				if err != nil {
					yield(nil, fmt.Errorf("%w: scan keys: %w", auth.ErrStoreUnavailable, err))
					return
				}
				ss, ok := segs(kv.Key)
				if !ok || len(ss) != 4 {
					continue
				}
				k, err := s.APIKeyByID(ctx, string(ss[3]))
				if errors.Is(err, auth.ErrNotFound) {
					continue
				}
				if !yield(k, err) {
					return
				}
			}
			return
		}
		for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(segKey))) {
			if err != nil {
				yield(nil, fmt.Errorf("%w: scan keys: %w", auth.ErrStoreUnavailable, err))
				return
			}
			ss, ok := segs(kv.Key)
			if !ok || len(ss) != 2 {
				continue // an index row
			}
			rec, err := decodeKey(string(ss[1]), kv.Value)
			var k *auth.APIKey
			if rec != nil {
				k = &rec.APIKey
			}
			if !yield(k, err) {
				return
			}
		}
	}
}

// DeleteAPIKey stages the deletion of k and its indexes.
func (s *Store) DeleteAPIKey(b *storage.Batch, k *auth.APIKey) error {
	if err := needBatch(b, "DeleteAPIKey"); err != nil {
		return err
	}
	if k == nil || k.ID == "" {
		return fmt.Errorf("%w: DeleteAPIKey of a key with no ID", auth.ErrBadInput)
	}
	cur, err := s.committed(apiKeyKey(k.ID))
	if err != nil || cur == nil {
		return err
	}
	rec, err := decodeKey(k.ID, cur)
	if err != nil {
		return err
	}
	if err := s.delCommitted(b, apiKeyHashKey(rec.Hash)); err != nil {
		return err
	}
	if err := s.delCommitted(b, apiKeyUserKey(rec.Owner, rec.ID)); err != nil {
		return err
	}
	return s.del(b, apiKeyKey(k.ID), cur)
}

// ---------------------------------------------------------------------------
// One-time tokens.

func kindPrefix(k auth.OneTimeKind) (auth.CredentialKind, bool) {
	switch k {
	case auth.OneTimeInvite:
		return auth.KindInvite, true
	case auth.OneTimeTempLogin:
		return auth.KindTempLogin, true
	case auth.OneTimeBootstrap:
		return auth.KindBootstrap, true
	}
	return "", false
}

func decodeOneTime(raw []byte) (*oneTimeRecord, error) {
	rec, err := codec.JSON[oneTimeRecord](1).Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: one-time token: %v", auth.ErrUndecodableRecord, err)
	}
	if rec.ID == "" || rec.Hash == "" {
		return nil, fmt.Errorf("%w: one-time token without its ID or hash", auth.ErrUndecodableRecord)
	}
	if _, ok := kindPrefix(rec.Kind); !ok {
		return nil, fmt.Errorf("%w: one-time token of kind %q", auth.ErrUndecodableRecord, rec.Kind)
	}
	return &rec, nil
}

// IssueOneTime mints a token of t.Kind (mci_, mct_ or mcb_) and stages t.
// It fills t.ID (when empty) and t.CreatedAt (when zero).
func (s *Store) IssueOneTime(b *storage.Batch, t *auth.OneTimeToken) (string, error) {
	if err := needBatch(b, "IssueOneTime"); err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("%w: nil one-time token", auth.ErrBadInput)
	}
	ck, ok := kindPrefix(t.Kind)
	if !ok {
		return "", fmt.Errorf("%w: one-time token kind %q", auth.ErrBadInput, t.Kind)
	}
	if t.ExpiresAt.IsZero() {
		return "", fmt.Errorf("%w: a one-time token has an expiry", auth.ErrBadInput)
	}
	if t.ID == "" {
		id, err := s.newID()
		if err != nil {
			return "", err
		}
		t.ID = id
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = s.now().UTC()
	}
	token, err := auth.MintCredential(ck)
	if err != nil {
		return "", err
	}
	h := auth.HashHex(token)
	raw, err := codec.JSON[oneTimeRecord](1).Encode(oneTimeRecord{OneTimeToken: *t, Hash: h})
	if err != nil {
		return "", fmt.Errorf("authstore: encode one-time token: %w", err)
	}
	if err := s.set(b, oneTimeKey(t.Kind, h), nil, raw); err != nil {
		return "", err
	}
	return token, nil
}

// TakeOneTime reads the token of kind and stages Expect(key, seen) plus its
// Delete: of two concurrent takes exactly one commits (§7). An absent token
// is auth.ErrNotFound. The record is returned even when expired: the caller
// refuses it and commits the delete (prune on read, §4.3).
func (s *Store) TakeOneTime(b *storage.Batch, kind auth.OneTimeKind, token string) (*auth.OneTimeToken, error) {
	if err := needBatch(b, "TakeOneTime"); err != nil {
		return nil, err
	}
	if _, ok := kindPrefix(kind); !ok || token == "" {
		return nil, auth.ErrNotFound
	}
	key := oneTimeKey(kind, auth.HashHex(token))
	cur, err := s.committed(key)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, auth.ErrNotFound
	}
	if err := s.del(b, key, cur); err != nil {
		return nil, err
	}
	rec, err := decodeOneTime(cur)
	if err != nil {
		return nil, err
	}
	if rec.Kind != kind {
		return nil, fmt.Errorf("%w: one-time token row of kind %s holds %s", auth.ErrUndecodableRecord, kind, rec.Kind)
	}
	return &rec.OneTimeToken, nil
}

// LookupOneTime reads a token without spending it.
func (s *Store) LookupOneTime(ctx context.Context, kind auth.OneTimeKind, token string) (*auth.OneTimeToken, error) {
	if _, ok := kindPrefix(kind); !ok || token == "" {
		return nil, auth.ErrNotFound
	}
	raw, err := s.get(ctx, oneTimeKey(kind, auth.HashHex(token)))
	if err != nil {
		return nil, err
	}
	rec, err := decodeOneTime(raw)
	if err != nil {
		return nil, err
	}
	return &rec.OneTimeToken, nil
}

// ScanOneTime yields the tokens of kind ("" = every kind).
func (s *Store) ScanOneTime(ctx context.Context, kind auth.OneTimeKind) iter.Seq2[*auth.OneTimeToken, error] {
	return func(yield func(*auth.OneTimeToken, error) bool) {
		prefix := keys.Prefix(keys.Str(segOneTime))
		if kind != "" {
			prefix = keys.Prefix(keys.Str(segOneTime), keys.Str(string(kind)))
		}
		for kv, err := range s.ks.ScanPrefix(ctx, prefix) {
			if err != nil {
				yield(nil, fmt.Errorf("%w: scan one-time tokens: %w", auth.ErrStoreUnavailable, err))
				return
			}
			rec, err := decodeOneTime(kv.Value)
			var t *auth.OneTimeToken
			if rec != nil {
				t = &rec.OneTimeToken
			}
			if !yield(t, err) {
				return
			}
		}
	}
}

// DeleteOneTime stages the deletion of the token of kind with public ID id.
func (s *Store) DeleteOneTime(b *storage.Batch, kind auth.OneTimeKind, id string) error {
	if err := needBatch(b, "DeleteOneTime"); err != nil {
		return err
	}
	if _, ok := kindPrefix(kind); !ok || id == "" {
		return fmt.Errorf("%w: one-time token %s/%s", auth.ErrNotFound, kind, id)
	}
	prefix := keys.Prefix(keys.Str(segOneTime), keys.Str(string(kind)))
	for kv, err := range s.ks.ScanPrefix(context.Background(), prefix) {
		if err != nil {
			return fmt.Errorf("%w: scan one-time tokens: %w", auth.ErrStoreUnavailable, err)
		}
		rec, err := decodeOneTime(kv.Value)
		if err != nil || rec.ID != id {
			continue
		}
		kv = kv.Clone()
		return s.del(b, kv.Key, kv.Value)
	}
	return fmt.Errorf("%w: one-time token %s/%s", auth.ErrNotFound, kind, id)
}

// ---------------------------------------------------------------------------
// External identities and the invite allow-list.

func decodeIdentity(raw []byte) (*auth.ExternalIdentity, error) {
	id, err := codec.JSON[auth.ExternalIdentity](1).Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: identity: %v", auth.ErrUndecodableRecord, err)
	}
	if id.Provider == "" || id.Subject == "" || id.UserID == "" {
		return nil, fmt.Errorf("%w: identity without provider, subject or user", auth.ErrUndecodableRecord)
	}
	return &id, nil
}

func validIdentity(id *auth.ExternalIdentity) error {
	if id == nil || id.Provider == "" || id.Subject == "" || id.UserID == "" {
		return fmt.Errorf("%w: an identity needs a provider, a subject and a user", auth.ErrBadInput)
	}
	if strings.Contains(id.Provider, "/") {
		return fmt.Errorf("%w: provider %q holds a slash", auth.ErrBadInput, id.Provider)
	}
	return nil
}

// PutIdentity stages id, moving its user index when the link changes user.
func (s *Store) PutIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	if err := needBatch(b, "PutIdentity"); err != nil {
		return err
	}
	if err := validIdentity(id); err != nil {
		return err
	}
	if err := s.ownerPresent(b, id.UserID); err != nil {
		return fmt.Errorf("%w: %w", auth.ErrBadInput, err)
	}
	return s.stageIdentity(b, id)
}

func (s *Store) stageIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	key := identityKey(id.Provider, id.Subject)
	cur, err := s.committed(key)
	if err != nil {
		return err
	}
	if cur != nil {
		old, err := decodeIdentity(cur)
		if err == nil && old.UserID != id.UserID {
			if err := s.delCommitted(b, identityUserKey(old.UserID, id.Provider, id.Subject)); err != nil {
				return err
			}
		}
	}
	if id.CreatedAt.IsZero() {
		id.CreatedAt = s.now().UTC()
	}
	raw, err := codec.JSON[auth.ExternalIdentity](1).Encode(*id)
	if err != nil {
		return fmt.Errorf("authstore: encode identity: %w", err)
	}
	if err := s.set(b, key, cur, raw); err != nil {
		return err
	}
	ukey := identityUserKey(id.UserID, id.Provider, id.Subject)
	ucur, err := s.committed(ukey)
	if err != nil {
		return err
	}
	return s.set(b, ukey, ucur, []byte{})
}

// DeleteIdentity stages the deletion of id's link and its user index.
func (s *Store) DeleteIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	if err := needBatch(b, "DeleteIdentity"); err != nil {
		return err
	}
	if id == nil || id.Provider == "" || id.Subject == "" {
		return fmt.Errorf("%w: DeleteIdentity needs a provider and a subject", auth.ErrBadInput)
	}
	key := identityKey(id.Provider, id.Subject)
	cur, err := s.committed(key)
	if err != nil || cur == nil {
		return err
	}
	if old, err := decodeIdentity(cur); err == nil {
		if err := s.delCommitted(b, identityUserKey(old.UserID, id.Provider, id.Subject)); err != nil {
			return err
		}
	}
	return s.del(b, key, cur)
}

// IdentityBySubject reads the link of (provider, subject).
func (s *Store) IdentityBySubject(ctx context.Context, provider, subject string) (*auth.ExternalIdentity, error) {
	if provider == "" || subject == "" {
		return nil, auth.ErrNotFound
	}
	raw, err := s.get(ctx, identityKey(provider, subject))
	if err != nil {
		return nil, err
	}
	return decodeIdentity(raw)
}

// ScanIdentities yields user's links ("" = every link).
func (s *Store) ScanIdentities(ctx context.Context, user auth.UserID) iter.Seq2[*auth.ExternalIdentity, error] {
	return func(yield func(*auth.ExternalIdentity, error) bool) {
		if user != "" {
			prefix := keys.Prefix(keys.Str(segIdentity), keys.Str(segByUser), keys.Str(string(user)))
			for kv, err := range s.ks.ScanPrefix(ctx, prefix) {
				if err != nil {
					yield(nil, fmt.Errorf("%w: scan identities: %w", auth.ErrStoreUnavailable, err))
					return
				}
				ss, ok := segs(kv.Key)
				if !ok || len(ss) != 5 {
					continue
				}
				id, err := s.IdentityBySubject(ctx, string(ss[3]), string(ss[4]))
				if errors.Is(err, auth.ErrNotFound) {
					continue
				}
				if err == nil && id.UserID != user {
					continue // a stale index row
				}
				if !yield(id, err) {
					return
				}
			}
			return
		}
		for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(segIdentity))) {
			if err != nil {
				yield(nil, fmt.Errorf("%w: scan identities: %w", auth.ErrStoreUnavailable, err))
				return
			}
			ss, ok := segs(kv.Key)
			if !ok || len(ss) != 3 {
				continue
			}
			id, err := decodeIdentity(kv.Value)
			if !yield(id, err) {
				return
			}
		}
	}
}

// InviteAllowed reports whether email is on the invite allow-list.
func (s *Store) InviteAllowed(ctx context.Context, email string) (bool, error) {
	fold := auth.NormalizeEmail(email)
	if fold == "" {
		return false, nil
	}
	_, err := s.get(ctx, allowKey(fold))
	if errors.Is(err, auth.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ---------------------------------------------------------------------------
// Merge.

// StageMerge rewrites the owner of every record of plan from plan.Source to
// plan.Target. Each row's write is derived from the bytes the plan read
// (row.Seen) and carries Expect(key, row.Seen), so a record changed since the
// plan was built fails the commit with storage.ErrConflict (DATA10-9). An
// identity row's ID is "<provider>/<subject>"; an allow-list row (no owner)
// is only expected unchanged.
func (s *Store) StageMerge(b *storage.Batch, plan auth.UserMergePlan) error {
	if err := needBatch(b, "StageMerge"); err != nil {
		return err
	}
	if plan.Source == "" || plan.Target == "" || plan.Source == plan.Target {
		return fmt.Errorf("%w: a merge needs two distinct users", auth.ErrBadInput)
	}
	if err := s.ownerPresent(b, plan.Target); err != nil {
		return fmt.Errorf("%w: %w", auth.ErrBadInput, err)
	}
	for _, row := range plan.Rows {
		if err := s.mergeRow(b, plan, row); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) mergeRow(b *storage.Batch, plan auth.UserMergePlan, row auth.MergeRow) error {
	switch row.Kind {
	case auth.MergeAPIKey:
		rec, err := decodeKey(row.ID, row.Seen)
		if err != nil {
			return err
		}
		if rec.Owner != plan.Source {
			return fmt.Errorf("%w: apikey %s is not the source user's", auth.ErrBadInput, row.ID)
		}
		next := *rec
		next.Owner = plan.Target
		next.Version++
		raw, err := codec.JSON[keyRecord](1).Encode(next)
		if err != nil {
			return fmt.Errorf("authstore: encode apikey: %w", err)
		}
		if err := s.set(b, apiKeyKey(row.ID), row.Seen, raw); err != nil {
			return err
		}
		if err := s.delCommitted(b, apiKeyUserKey(plan.Source, row.ID)); err != nil {
			return err
		}
		return s.set(b, apiKeyUserKey(plan.Target, row.ID), nil, []byte{})
	case auth.MergeIdentity:
		provider, subject, ok := strings.Cut(row.ID, "/")
		if !ok || provider == "" || subject == "" {
			return fmt.Errorf("%w: identity merge row %q is not <provider>/<subject>", auth.ErrBadInput, row.ID)
		}
		id, err := decodeIdentity(row.Seen)
		if err != nil {
			return err
		}
		if id.UserID != plan.Source || id.Provider != provider || id.Subject != subject {
			return fmt.Errorf("%w: identity %s is not the source user's", auth.ErrBadInput, row.ID)
		}
		next := *id
		next.UserID = plan.Target
		raw, err := codec.JSON[auth.ExternalIdentity](1).Encode(next)
		if err != nil {
			return fmt.Errorf("authstore: encode identity: %w", err)
		}
		if err := s.set(b, identityKey(provider, subject), row.Seen, raw); err != nil {
			return err
		}
		if err := s.delCommitted(b, identityUserKey(plan.Source, provider, subject)); err != nil {
			return err
		}
		return s.set(b, identityUserKey(plan.Target, provider, subject), nil, []byte{})
	case auth.MergeInviteAllow:
		b.Bind(s.ks)
		return b.Expect(s.ks, allowKey(auth.NormalizeEmail(row.ID)), row.Seen)
	}
	return fmt.Errorf("%w: merge row kind %q", auth.ErrBadInput, row.Kind)
}

// ---------------------------------------------------------------------------
// Importer.

func reject(format string, a ...any) error {
	return fmt.Errorf("%w: %s", auth.ErrImportRejected, fmt.Sprintf(format, a...))
}

// ImportUser stores u with its stored password hash as it is and its
// credential epoch (§3.4). It refuses (auth.ErrImportRejected, staging
// nothing it can avoid) a user without an ID or username, an unknown status,
// a hash that is neither argon2id nor bcrypt nor empty, a user already
// stored, and a username another user holds.
func (s *Store) ImportUser(b *storage.Batch, u *auth.User, passwordHash string) error {
	if b == nil {
		return reject("no batch")
	}
	if u == nil || u.ID == "" || strings.TrimSpace(u.Username) == "" {
		return reject("a user needs an ID and a username")
	}
	if !validStatus(u.Status) {
		return reject("user %s has status %q", u.ID, u.Status)
	}
	if passwordHash != "" {
		if a := password.AlgoOf(passwordHash); a != password.AlgoArgon2id && a != password.AlgoBcrypt {
			return reject("user %s: password hash is not argon2id or bcrypt", u.ID)
		}
	}
	if cur, err := s.committed(userKey(u.ID)); err != nil {
		return reject("%v", err)
	} else if cur != nil {
		return reject("user %s is already stored", u.ID)
	}
	fold := auth.FoldUsername(u.Username)
	if taken, err := s.committed(userNameKey(fold)); err != nil {
		return reject("%v", err)
	} else if taken != nil {
		return reject("username %q is taken", u.Username)
	}
	in := *u
	if in.Version < 1 {
		in.Version = 1
	}
	now := s.now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = in.CreatedAt
	}
	raw, err := encodeUser(&in, passwordHash)
	if err != nil {
		return reject("encode user %s: %v", u.ID, err)
	}
	if err := s.set(b, userNameKey(fold), nil, []byte(in.ID)); err != nil {
		return reject("%v", err)
	}
	if err := s.set(b, userKey(in.ID), nil, raw); err != nil {
		return reject("%v", err)
	}
	return nil
}

// ImportAPIKey stores k under tokenSHA256, the hash the app's row holds. It
// refuses a key without an ID, with no scope or a malformed or wildcard
// scope, with an unknown status, without an expiry (unless revoked: the
// cutover stamps every live never-expiring key), or whose owner is not
// stored. A key whose revocation time is set is stored revoked, whatever
// status it came with (early fix 3's rule: the most restrictive reading).
func (s *Store) ImportAPIKey(b *storage.Batch, k *auth.APIKey, tokenSHA256 [32]byte) error {
	if b == nil {
		return reject("no batch")
	}
	if k == nil || k.ID == "" {
		return reject("a key needs an ID")
	}
	if err := checkScopes(k.Scopes); err != nil {
		return reject("apikey %s: %v", k.ID, err)
	}
	if !validKeyStatus(k.Status) {
		return reject("apikey %s has status %q", k.ID, k.Status)
	}
	if tokenSHA256 == ([32]byte{}) {
		return reject("apikey %s has no token hash", k.ID)
	}
	in := *k
	if !in.RevokedAt.IsZero() {
		in.Status = auth.KeyRevoked
	}
	if in.Status == auth.KeyRevoked && in.RevokedAt.IsZero() {
		in.RevokedAt = s.now().UTC()
	}
	if in.Status != auth.KeyRevoked && in.ExpiresAt.IsZero() {
		return reject("apikey %s has no expiry", k.ID)
	}
	if err := s.ownerPresent(b, in.Owner); err != nil {
		return reject("apikey %s: %v", k.ID, err)
	}
	if err := s.stageNewKey(b, &in, hex.EncodeToString(tokenSHA256[:]), true); err != nil {
		return reject("apikey %s: %v", k.ID, err)
	}
	return nil
}

// ImportIdentity stores a link whose user is stored.
func (s *Store) ImportIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	if b == nil {
		return reject("no batch")
	}
	if err := validIdentity(id); err != nil {
		return reject("%v", err)
	}
	if err := s.ownerPresent(b, id.UserID); err != nil {
		return reject("identity %s/%s: %v", id.Provider, id.Subject, err)
	}
	if cur, err := s.committed(identityKey(id.Provider, id.Subject)); err != nil {
		return reject("%v", err)
	} else if cur != nil {
		return reject("identity %s/%s is already stored", id.Provider, id.Subject)
	}
	if err := s.stageIdentity(b, id); err != nil {
		return reject("%v", err)
	}
	return nil
}

// ImportInviteAllow stores email on the invite allow-list (folded).
func (s *Store) ImportInviteAllow(b *storage.Batch, email string) error {
	if b == nil {
		return reject("no batch")
	}
	fold := auth.NormalizeEmail(email)
	if fold == "" {
		return reject("empty e-mail")
	}
	key := allowKey(fold)
	cur, err := s.committed(key)
	if err != nil {
		return reject("%v", err)
	}
	raw, err := codec.JSON[allowRecord](1).Encode(allowRecord{Email: fold})
	if err != nil {
		return reject("%v", err)
	}
	if cur != nil && bytes.Equal(cur, raw) {
		return nil
	}
	if err := s.set(b, key, cur, raw); err != nil {
		return reject("%v", err)
	}
	return nil
}
