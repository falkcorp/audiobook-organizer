// file: internal/database/user_merge.go
// version: 1.0.0
// guid: 5d0a7e3c-91b4-4f26-8c1d-e7a2b6f40935
// last-edited: 2026-10-22

package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// The storage half of the admin "merge user B into user A" tool
// (internal/usermerge). This file owns every per-user key layout the merge
// has to read or move, so the key builders and the index maintenance live next
// to the code that writes those keys and never in a handler. The planning
// (which row wins a conflict, what the digest covers) is in internal/usermerge;
// here are the raw scan, the compare-and-set batch, and the two field-scoped
// writes to the user rows themselves.
//
// Resolve UserMerger with AsCapability. It is deliberately not on Store (the
// Store width baseline only goes down), and a store without it cannot merge.

// UserMergeJournalPrefix keys the merge journals: usermerge:journal:<id> -> the
// JSON internal/usermerge writes. The journal is never pruned (it is small and
// is the only way back).
const UserMergeJournalPrefix = "usermerge:journal:"

// ErrUserMergeConflict is what ApplyUserMergeOps wraps when a key no longer
// holds the value the plan was built from (nor the value the plan would write):
// something else wrote it. Nothing is written for the batch.
var ErrUserMergeConflict = errors.New("database: a key changed since the user merge was planned")

// UserMergeConflictError names the key that changed.
type UserMergeConflictError struct {
	Key    string
	Family string
}

func (e *UserMergeConflictError) Error() string {
	return fmt.Sprintf("%s: key %q (%s)", ErrUserMergeConflict, e.Key, e.Family)
}

// Unwrap lets errors.Is(err, ErrUserMergeConflict) match.
func (e *UserMergeConflictError) Unwrap() error { return ErrUserMergeConflict }

// UserMergeOp is one raw key write of a user merge: the full prior value
// (Before, or BeforeAbsent for "the key did not exist") and either the new
// value (After) or a delete. The before-image is what undo restores and what
// the digest's before-hash covers.
type UserMergeOp struct {
	Key          string `json:"key"`
	Family       string `json:"family"`
	Reason       string `json:"reason,omitempty"`
	Before       []byte `json:"before,omitempty"`
	BeforeAbsent bool   `json:"before_absent,omitempty"`
	After        []byte `json:"after,omitempty"`
	Delete       bool   `json:"delete,omitempty"`
}

// UserMergeValueHash is the digest unit for one side of an op: the sha256 of
// the value, or the fixed token "absent". Present-but-empty and absent differ.
func UserMergeValueHash(value []byte, present bool) string {
	if !present {
		return "absent"
	}
	sum := sha256.Sum256(append([]byte("v:"), value...))
	return hex.EncodeToString(sum[:])
}

// BeforeHash is UserMergeValueHash of the op's before-image.
func (o UserMergeOp) BeforeHash() string { return UserMergeValueHash(o.Before, !o.BeforeAbsent) }

// AfterHash is UserMergeValueHash of the value the op leaves behind.
func (o UserMergeOp) AfterHash() string { return UserMergeValueHash(o.After, !o.Delete) }

// UserDataRow is one raw key and value.
type UserDataRow struct {
	Key   string
	Value []byte
}

// UserData is every row one user owns, by family, exactly as stored. Values
// are raw so the merge never drops a field this build does not know about.
type UserData struct {
	UserID string

	// Keyed by the user id inside the key.
	BookStates       []UserDataRow // ubs:<u>:<book>
	Positions        []UserDataRow // upos:<u>:<book>:<segment>
	Bookmarks        []UserDataRow // bookmark:<u>:<item>:<time>
	SyncAliasUses    []UserDataRow // sync_alias_use:<u>:<alias>
	SyncAliasSeeded  []UserDataRow // sync_alias_use_seeded:<u> (0 or 1 row)
	Preferences      []UserDataRow // pref:<u>:<key>
	PlaybackProgress []UserDataRow // playp:<u>:<n>
	PlaybackEvents   []UserDataRow // playe:<u>:<n>:<ts>
	ListenStats      []UserDataRow // stats:user:listen_seconds:<u> (0 or 1 row)

	// Primary rows reached through the per-user index.
	OAuthIdentities []UserDataRow // oauthid:<id>, from idx:oauth:user:<u>:
	APIKeys         []UserDataRow // apikey:<id>, from idx:apikey:user:<u>:

	// Primary rows found by a scan of the whole family for an owner field.
	Playlists    []UserDataRow // upl:<id> whose created_by_user_id is the user
	Collections  []UserDataRow // col:<id> whose created_by_user_id is the user
	Invites      []UserDataRow // invite:<token> whose created_by_user_id is the user
	InviteGrants []UserDataRow // inviteallow:<email> whose user_id is the user

	// Unreadable names rows a family scan could not classify (undecodable, or
	// an index entry whose primary row is missing). They are reported, never
	// skipped silently.
	Unreadable []string
}

// UserMergeKeys are the key layouts the merge reads. Exported builders, not
// strings repeated in internal/usermerge.
func UserBookStateKey(userID, bookID string) string { return "ubs:" + userID + ":" + bookID }

// UserBookStateStatusKey is the status index entry of one (user, book, status).
func UserBookStateStatusKey(userID, status, bookID string) string {
	return "idx:ubs:status:" + userID + ":" + status + ":" + bookID
}

// UserBookStateStatusKeys is every status index key a (user, book) could have:
// the same set DeleteUserBookState clears.
func UserBookStateStatusKeys(userID, bookID string) []string {
	statuses := []string{UserBookStatusUnstarted, UserBookStatusInProgress, UserBookStatusFinished, UserBookStatusAbandoned}
	out := make([]string, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, UserBookStateStatusKey(userID, st, bookID))
	}
	return out
}

// UserPositionKey is one segment's position row.
func UserPositionKey(userID, bookID, segmentID string) string {
	return "upos:" + userID + ":" + bookID + ":" + segmentID
}

// UserPositionBookPrefix is the prefix of every position row of one (user, book).
func UserPositionBookPrefix(userID, bookID string) string {
	return "upos:" + userID + ":" + bookID + ":"
}

// OAuthUserIndexKey is the per-user index entry of one linked identity.
func OAuthUserIndexKey(userID, identityID string) string {
	return "idx:oauth:user:" + userID + ":" + identityID
}

// APIKeyUserIndexKey is the per-user index entry of one API key.
func APIKeyUserIndexKey(userID, keyID string) string {
	return "idx:apikey:user:" + userID + ":" + keyID
}

// userScopedPrefixes are the families whose key is <prefix><user>:<rest>.
var userScopedPrefixes = []string{
	"ubs:", "upos:", "bookmark:", "sync_alias_use:", "pref:", "playp:", "playe:",
	"idx:oauth:user:", "idx:apikey:user:",
}

// userScopedExact are the families whose key is <prefix><user> and nothing more.
var userScopedExact = []string{"sync_alias_use_seeded:", "stats:user:listen_seconds:"}

// RekeyUserKey returns key with the user id from replaced by to, for a key of
// any per-user family above. ok is false for a key that is not from's.
func RekeyUserKey(key, from, to string) (string, bool) {
	for _, p := range userScopedPrefixes {
		if rest, found := strings.CutPrefix(key, p+from+":"); found {
			return p + to + ":" + rest, true
		}
	}
	for _, p := range userScopedExact {
		if key == p+from {
			return p + to, true
		}
	}
	return "", false
}

// RewriteJSONField returns value with the top-level string field set to to, and
// every other field byte-for-byte as it was (a field this build does not know
// survives). The old value of the field is returned too so a caller can check
// it was the expected user.
func RewriteJSONField(value []byte, field, to string) (out []byte, old string, err error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return nil, "", fmt.Errorf("decode: %w", err)
	}
	if raw, ok := m[field]; ok {
		_ = json.Unmarshal(raw, &old)
	}
	enc, err := json.Marshal(to)
	if err != nil {
		return nil, "", err
	}
	m[field] = enc
	out, err = json.Marshal(m)
	return out, old, err
}

// JSONStringField reads one top-level string field of a JSON object ("" when
// it is absent), or an error when value is not an object.
func JSONStringField(value []byte, field string) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(value, &m); err != nil {
		return "", err
	}
	var s string
	if raw, ok := m[field]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s, nil
}

// UserIdentityFields is the part of a user row the merge changes.
type UserIdentityFields struct {
	Roles            []string `json:"roles"`
	Email            string   `json:"email"`
	EmailSource      string   `json:"email_source,omitempty"`
	MergedIntoUserID string   `json:"merged_into_user_id,omitempty"`
}

// UserRetirement describes the two user-row changes of a merge. Before is what
// each row holds now (checked, so a concurrent edit is a refusal, not an
// overwrite); After is what it ends with. The same value is journaled and
// handed to UnretireMergedUser, which restores a field only while the row
// still holds After.
type UserRetirement struct {
	SurvivorID     string             `json:"survivor_id"`
	AbsorbedID     string             `json:"absorbed_id"`
	SurvivorBefore UserIdentityFields `json:"survivor_before"`
	SurvivorAfter  UserIdentityFields `json:"survivor_after"`
	AbsorbedBefore UserIdentityFields `json:"absorbed_before"`
	AbsorbedAfter  UserIdentityFields `json:"absorbed_after"`
	MergedAt       time.Time          `json:"merged_at"`
}

// ErrUserMergeRetireRefused is returned by RetireMergedUser for a state it will
// not merge over: a row changed since the plan, the absorbed account is not
// locked, either is already merged, or the address would be proven twice.
var ErrUserMergeRetireRefused = errors.New("database: user merge refused")

// UserMerger is the storage capability behind the admin user merge. Not on
// Store; resolve it with AsCapability.
type UserMerger interface {
	// ScanUserData reads every row userID owns, by family.
	ScanUserData(userID string) (*UserData, error)
	// ApplyUserMergeOps writes ops and the journal in one batch. Each op is
	// checked first: the key must still hold the op's before-image, or already
	// hold what the op writes (a resumed chunk), otherwise nothing is written
	// and the error wraps ErrUserMergeConflict. journalKey "" writes no journal.
	ApplyUserMergeOps(ops []UserMergeOp, journalKey string, journal []byte) error
	// RetireMergedUser changes the two user rows and the verified-email key.
	RetireMergedUser(r UserRetirement) error
	// UnretireMergedUser reverses RetireMergedUser field by field and returns
	// the fields it left alone because they changed since.
	UnretireMergedUser(r UserRetirement) (conflicts []string, err error)
}

var _ UserMerger = (*PebbleStore)(nil)

// userMergeApplyMu serializes ApplyUserMergeOps, so two applies (a resume
// racing a first run) cannot interleave their check and their commit.
var userMergeApplyMu sync.Mutex

// ScanUserData implements UserMerger.
func (p *PebbleStore) ScanUserData(userID string) (*UserData, error) {
	if userID == "" || strings.Contains(userID, ":") {
		return nil, fmt.Errorf("user merge: invalid user id %q", userID)
	}
	d := &UserData{UserID: userID}
	scan := func(prefix string, into *[]UserDataRow) error {
		return forEachKeyInRange(p.db, []byte(prefix), prefixUpperBound([]byte(prefix)), func(k, v []byte) error {
			*into = append(*into, UserDataRow{Key: string(k), Value: bytes.Clone(v)})
			return nil
		})
	}
	for _, s := range []struct {
		prefix string
		into   *[]UserDataRow
	}{
		{"ubs:" + userID + ":", &d.BookStates},
		{"upos:" + userID + ":", &d.Positions},
		{"bookmark:" + userID + ":", &d.Bookmarks},
		{"sync_alias_use:" + userID + ":", &d.SyncAliasUses},
		{"pref:" + userID + ":", &d.Preferences},
		{"playp:" + userID + ":", &d.PlaybackProgress},
		{"playe:" + userID + ":", &d.PlaybackEvents},
	} {
		if err := scan(s.prefix, s.into); err != nil {
			return nil, err
		}
	}
	for _, s := range []struct {
		key  string
		into *[]UserDataRow
	}{
		{"sync_alias_use_seeded:" + userID, &d.SyncAliasSeeded},
		{"stats:user:listen_seconds:" + userID, &d.ListenStats},
	} {
		v, err := p.GetRaw(s.key)
		if err != nil {
			return nil, err
		}
		if v != nil {
			*s.into = append(*s.into, UserDataRow{Key: s.key, Value: v})
		}
	}

	// Identities and keys: the per-user index names the primary rows.
	for _, s := range []struct {
		indexPrefix, primaryPrefix string
		into                       *[]UserDataRow
	}{
		{"idx:oauth:user:" + userID + ":", "oauthid:", &d.OAuthIdentities},
		{"idx:apikey:user:" + userID + ":", "apikey:", &d.APIKeys},
	} {
		var ids []string
		if err := forEachKeyInRange(p.db, []byte(s.indexPrefix), prefixUpperBound([]byte(s.indexPrefix)), func(k, _ []byte) error {
			ids = append(ids, strings.TrimPrefix(string(k), s.indexPrefix))
			return nil
		}); err != nil {
			return nil, err
		}
		for _, id := range ids {
			v, err := p.GetRaw(s.primaryPrefix + id)
			if err != nil {
				return nil, err
			}
			if v == nil {
				d.Unreadable = append(d.Unreadable, s.indexPrefix+id+" (primary row missing)")
				continue
			}
			*s.into = append(*s.into, UserDataRow{Key: s.primaryPrefix + id, Value: v})
		}
	}

	// Families with no per-user index: scan every row for the owner field.
	// These are small (playlists, collections, invites, grants), not
	// library-scale, so the scan stays sequential.
	for _, s := range []struct {
		prefix, field string
		into          *[]UserDataRow
	}{
		{"upl:", "created_by_user_id", &d.Playlists},
		{"col:", "created_by_user_id", &d.Collections},
		{"invite:", "created_by_user_id", &d.Invites},
		{inviteEmailGrantPrefix, "user_id", &d.InviteGrants},
	} {
		if err := forEachKeyInRange(p.db, []byte(s.prefix), prefixUpperBound([]byte(s.prefix)), func(k, v []byte) error {
			owner, err := JSONStringField(v, s.field)
			if err != nil {
				d.Unreadable = append(d.Unreadable, string(k)+" (undecodable)")
				return nil
			}
			if owner == userID {
				*s.into = append(*s.into, UserDataRow{Key: string(k), Value: bytes.Clone(v)})
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// ApplyUserMergeOps implements UserMerger.
func (p *PebbleStore) ApplyUserMergeOps(ops []UserMergeOp, journalKey string, journal []byte) error {
	userMergeApplyMu.Lock()
	defer userMergeApplyMu.Unlock()
	b := p.db.NewBatch()
	defer func() { _ = b.Close() }()
	for i := range ops {
		op := &ops[i]
		cur, err := p.GetRaw(op.Key)
		if err != nil {
			return err
		}
		present := cur != nil
		switch {
		case UserMergeValueHash(cur, present) == op.BeforeHash():
			if op.Delete {
				if err := b.Delete([]byte(op.Key), nil); err != nil {
					return err
				}
			} else if err := b.Set([]byte(op.Key), op.After, nil); err != nil {
				return err
			}
		case UserMergeValueHash(cur, present) == op.AfterHash():
			// Already what the op writes: a chunk resumed after a crash.
		default:
			return &UserMergeConflictError{Key: op.Key, Family: op.Family}
		}
	}
	if journalKey != "" {
		if err := b.Set([]byte(journalKey), journal, nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func sortedStrings(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func sameStringSet(a, b []string) bool { return slices.Equal(sortedStrings(a), sortedStrings(b)) }

func (f UserIdentityFields) matchesUser(u *User) bool {
	return sameStringSet(f.Roles, u.Roles) && util.NormalizeEmail(f.Email) == util.NormalizeEmail(u.Email) &&
		f.EmailSource == u.EmailSource && f.MergedIntoUserID == u.MergedIntoUserID
}

func (f UserIdentityFields) applyTo(u *User) {
	u.Roles = slices.Clone(f.Roles)
	u.Email = f.Email
	u.EmailSource = f.EmailSource
	u.MergedIntoUserID = f.MergedIntoUserID
}

// otherProvenHolderLocked reports whether any user outside except holds lower
// proven. Caller holds inviteMu.
func (p *PebbleStore) otherProvenHolderLocked(lower string, except ...string) (bool, error) {
	if lower == "" {
		return false, nil
	}
	users, _, err := p.listUsers()
	if err != nil {
		return false, err
	}
	for i := range users {
		if slices.Contains(except, users[i].ID) {
			continue
		}
		if emailProven(&users[i], lower) {
			return true, nil
		}
	}
	return false, nil
}

// stageEmailKey stages the verified-email key for lower: set to holderID when
// holderID is not "", otherwise deleted only while it still names ifPointsTo.
func (p *PebbleStore) stageEmailKey(b *GatedBatch, lower, holderID, ifPointsTo string) error {
	if lower == "" {
		return nil
	}
	key := []byte(userEmailIndexPrefix + lower)
	if holderID != "" {
		return b.Set(key, []byte(holderID), nil)
	}
	v, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	pointsHere := string(v) == ifPointsTo
	closer.Close()
	if pointsHere {
		return b.Delete(key, nil)
	}
	return nil
}

// RetireMergedUser implements UserMerger. Under inviteMu then userWriteMu (the
// order UpdateUser takes them) it re-reads both rows, checks them against
// r's before-images, and in one batch rewrites the survivor's roles and email,
// clears the absorbed account's email, stamps its MergedIntoUserID and
// MergedAt, and moves the verified-email key. Passwords, status and credential
// epochs are not touched: the absorbed account was locked before this (the
// merge's first step) and stays locked. Running it again on an account already
// retired with the same After values is a no-op, so a resumed merge is safe.
func (p *PebbleStore) RetireMergedUser(r UserRetirement) error {
	if r.SurvivorID == "" || r.AbsorbedID == "" || r.SurvivorID == r.AbsorbedID {
		return fmt.Errorf("%w: two different accounts are required", ErrUserMergeRetireRefused)
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	surv, err := p.GetUserByID(r.SurvivorID)
	if err != nil {
		return err
	}
	abs, err := p.GetUserByID(r.AbsorbedID)
	if err != nil {
		return err
	}
	if surv == nil {
		return fmt.Errorf("%w: %s", ErrUserNotFound, r.SurvivorID)
	}
	if abs == nil {
		return fmt.Errorf("%w: %s", ErrUserNotFound, r.AbsorbedID)
	}
	if r.SurvivorAfter.matchesUser(surv) && r.AbsorbedAfter.matchesUser(abs) {
		return nil // already retired
	}
	switch {
	case !r.SurvivorBefore.matchesUser(surv):
		return fmt.Errorf("%w: the surviving account changed since the merge was planned", ErrUserMergeRetireRefused)
	case !r.AbsorbedBefore.matchesUser(abs):
		return fmt.Errorf("%w: the absorbed account changed since the merge was planned", ErrUserMergeRetireRefused)
	case abs.Status != "locked":
		return fmt.Errorf("%w: the absorbed account must be locked first", ErrUserMergeRetireRefused)
	case surv.MergedIntoUserID != "":
		return fmt.Errorf("%w: the surviving account is itself merged away", ErrUserMergeRetireRefused)
	}
	survLower := util.NormalizeEmail(r.SurvivorAfter.Email)
	if trustedEmailSource(r.SurvivorAfter.EmailSource) {
		if taken, err := p.otherProvenHolderLocked(survLower, r.SurvivorID, r.AbsorbedID); err != nil {
			return err
		} else if taken {
			return fmt.Errorf("%w: %w", ErrUserMergeRetireRefused, ErrUserEmailTaken)
		}
	}
	now := time.Now()
	oldSurvLower, oldAbsLower := util.NormalizeEmail(surv.Email), util.NormalizeEmail(abs.Email)
	r.SurvivorAfter.applyTo(surv)
	r.AbsorbedAfter.applyTo(abs)
	abs.MergedIntoUserID = r.SurvivorID
	mergedAt := r.MergedAt
	if mergedAt.IsZero() {
		mergedAt = now
	}
	abs.MergedAt = &mergedAt
	surv.UpdatedAt, abs.UpdatedAt = now, now
	survData, err := json.Marshal(surv)
	if err != nil {
		return err
	}
	absData, err := json.Marshal(abs)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.Set([]byte("u:"+surv.ID), survData, nil); err != nil {
		return err
	}
	if err := b.Set([]byte("u:"+abs.ID), absData, nil); err != nil {
		return err
	}
	// Keys for addresses the two rows give up, only while they name that row.
	if oldSurvLower != "" && oldSurvLower != survLower {
		if err := p.stageEmailKey(b, oldSurvLower, "", surv.ID); err != nil {
			return err
		}
	}
	if oldAbsLower != "" && oldAbsLower != survLower {
		if err := p.stageEmailKey(b, oldAbsLower, "", abs.ID); err != nil {
			return err
		}
	}
	if survLower != "" && trustedEmailSource(surv.EmailSource) {
		if err := p.stageEmailKey(b, survLower, surv.ID, ""); err != nil {
			return err
		}
	} else if survLower != "" {
		// An untrusted address is not verified: its key must not name anyone.
		if err := p.stageEmailKey(b, survLower, "", abs.ID); err != nil {
			return err
		}
		if err := p.stageEmailKey(b, survLower, "", surv.ID); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// UnretireMergedUser implements UserMerger. A field goes back to its
// before-image only while the row still holds the after-image: the survivor's
// roles, the survivor's email, the absorbed account's email. Anything that
// changed since is returned in conflicts and left as it is. The absorbed
// account's MergedIntoUserID and MergedAt are always cleared, and its status is
// left alone (the caller sets it with SetUserStatus, which bumps the epoch; the
// epoch is never lowered).
func (p *PebbleStore) UnretireMergedUser(r UserRetirement) ([]string, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	surv, err := p.GetUserByID(r.SurvivorID)
	if err != nil {
		return nil, err
	}
	abs, err := p.GetUserByID(r.AbsorbedID)
	if err != nil {
		return nil, err
	}
	if surv == nil || abs == nil {
		return nil, fmt.Errorf("%w: %s / %s", ErrUserNotFound, r.SurvivorID, r.AbsorbedID)
	}
	var conflicts []string
	oldSurvLower, oldAbsLower := util.NormalizeEmail(surv.Email), util.NormalizeEmail(abs.Email)

	// Survivor: roles and email are independent fields.
	if sameStringSet(surv.Roles, r.SurvivorAfter.Roles) {
		surv.Roles = slices.Clone(r.SurvivorBefore.Roles)
	} else if !sameStringSet(surv.Roles, r.SurvivorBefore.Roles) {
		conflicts = append(conflicts, "surviving account's roles changed after the merge; left as they are")
	}
	if util.NormalizeEmail(surv.Email) == util.NormalizeEmail(r.SurvivorAfter.Email) && surv.EmailSource == r.SurvivorAfter.EmailSource {
		surv.Email, surv.EmailSource = r.SurvivorBefore.Email, r.SurvivorBefore.EmailSource
	} else if util.NormalizeEmail(surv.Email) != util.NormalizeEmail(r.SurvivorBefore.Email) {
		conflicts = append(conflicts, "surviving account's email changed after the merge; left as it is")
	}

	// Absorbed: the email goes back only when the survivor does not now hold
	// that address proven (two proven holders is the state uniqueness forbids).
	if abs.Email == "" && abs.EmailSource == "" {
		restore := true
		bl := util.NormalizeEmail(r.AbsorbedBefore.Email)
		if bl != "" && trustedEmailSource(r.AbsorbedBefore.EmailSource) {
			if emailProven(surv, bl) {
				restore = false
				conflicts = append(conflicts, "the absorbed account's email is now held verified by the surviving account; not restored")
			} else if taken, err := p.otherProvenHolderLocked(bl, r.AbsorbedID, r.SurvivorID); err != nil {
				return nil, err
			} else if taken {
				restore = false
				conflicts = append(conflicts, "the absorbed account's email is now held verified by another account; not restored")
			}
		}
		if restore {
			abs.Email, abs.EmailSource = r.AbsorbedBefore.Email, r.AbsorbedBefore.EmailSource
		}
	} else if util.NormalizeEmail(abs.Email) != util.NormalizeEmail(r.AbsorbedBefore.Email) {
		conflicts = append(conflicts, "absorbed account's email was set after the merge; left as it is")
	}
	abs.MergedIntoUserID, abs.MergedAt = "", nil

	now := time.Now()
	surv.UpdatedAt, abs.UpdatedAt = now, now
	survData, err := json.Marshal(surv)
	if err != nil {
		return nil, err
	}
	absData, err := json.Marshal(abs)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.Set([]byte("u:"+surv.ID), survData, nil); err != nil {
		return nil, err
	}
	if err := b.Set([]byte("u:"+abs.ID), absData, nil); err != nil {
		return nil, err
	}
	// Keys: drop each address a row gave up, then write the ones the rows hold
	// verified now.
	newSurvLower, newAbsLower := util.NormalizeEmail(surv.Email), util.NormalizeEmail(abs.Email)
	if oldSurvLower != "" && oldSurvLower != newSurvLower {
		if err := p.stageEmailKey(b, oldSurvLower, "", surv.ID); err != nil {
			return nil, err
		}
	}
	if oldAbsLower != "" && oldAbsLower != newAbsLower {
		if err := p.stageEmailKey(b, oldAbsLower, "", abs.ID); err != nil {
			return nil, err
		}
	}
	for _, u := range []*User{abs, surv} { // survivor last: it wins a shared key
		if l := util.NormalizeEmail(u.Email); l != "" && trustedEmailSource(u.EmailSource) {
			if err := p.stageEmailKey(b, l, u.ID, ""); err != nil {
				return nil, err
			}
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return conflicts, nil
}
