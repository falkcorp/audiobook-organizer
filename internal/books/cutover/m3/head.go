// file: internal/books/cutover/m3/head.go
// version: 1.0.0
// guid: 0599be08-74fa-4bb0-be1f-d85bab4cc068
// last-edited: 2026-11-12

// Package m3 is the identity half of books' R2 cutover (03 §3.8 row M3;
// 05-authn §11, 05-rbac §6.3): HEAD's users, OAuth identities, invite
// allow-list, API keys and roles into core's auth and rbac stores, their IDs
// kept; sessions, invites and the bootstrap hash deleted; the merge journals
// rewritten once so no raw invite token survives. It is a leaf under
// internal/books/cutover: it names no books package (the cutover layer sits
// below them), so HEAD's row types are frozen copies below, and what books
// owns (the atom table, the seed roles' native sets, core's importers and
// native readers) arrives through Config.
package m3

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// HEAD's key families, as the R0 binary writes them
// (internal/books/store/pebble_store_auth.go, oauth_identity.go,
// pebble_store_invite_delivery.go). The Source of the step is the families
// below and their idx: children; every other idx: family is not identity's
// and is never touched (05-authn §11: "only the auth sub-prefixes of the
// shared idx: family move").
const (
	prefixUser        = "u:"
	prefixRole        = "role:"
	prefixKey         = "apikey:"
	prefixIdentity    = "oauthid:"
	prefixInvite      = "invite:"
	prefixInviteAllow = "inviteallow:"
	prefixSession     = "sess:"

	idxUser       = "idx:user:"
	idxRoleName   = "idx:role:name:"
	idxKey        = "idx:apikey:"
	idxInvite     = "idx:invite:username:"
	idxOAuth      = "idx:oauth:"
	idxSessByUser = "idx:sess:user:"

	idxKeyByUser  = "idx:apikey:user:"
	idxOAuthUser  = "idx:oauth:user:"
	idxOAuthSub   = "idx:oauth:sub:"
	idxUserName   = "idx:user:username:"
	idxKeyByHash  = "idx:apikey:hash:"
	idxUserByMail = "idx:user:email:"

	// JournalPrefix keys the merge journals (database.UserMergeJournalPrefix).
	JournalPrefix = "usermerge:journal:"

	// bootstrapHashKey and bootstrapExpiresKey are HEAD's bootstrap token hash
	// and its expiry (internal/server/bootstrap.go bootstrapTokenKey and
	// bootstrapExpiresKey), settings rows. 05-storage's ownership table row 8
	// retires both exact keys and has M3 delete them: core auth issues its own
	// bootstrap token (05-authn §4.6), so they are in Source. The two
	// startup_readonly_key_* rows (04-package-mapping names them with these)
	// are not: row 16 leaves them in books' read-write setting: family and
	// core never reads them.
	bootstrapHashKey    = "setting:bootstrap_token_hash"
	bootstrapExpiresKey = "setting:bootstrap_token_expires_at"
	// expiryDoneKey is HEAD's one-shot expiry stamp flag
	// (internal/server/apikey_expiry_stamp.go apiKeyExpiryStampDoneKey), a
	// settings row. It is not carried over: the stamp is this step's.
	expiryDoneKey = "setting:system:migration:apikey_expiry_stamp_v1_done"
)

// identityPrefixes are the Source families in key order: the runner scans
// them in ascending byte order, which is the order Step sees their rows.
var identityPrefixes = []string{
	prefixKey, idxKey, idxInvite, idxOAuth, idxRoleName, idxSessByUser, idxUser,
	prefixInvite, prefixInviteAllow, prefixIdentity, prefixRole, prefixSession, prefixUser,
}

// identityKeys are the Source's exact keys (ownership table row 8), in key
// order.
var identityKeys = []string{bootstrapExpiresKey, bootstrapHashKey}

// SourcePrefixes returns the prefixes of the step's Source, in key order.
func SourcePrefixes() []string { return append([]string(nil), identityPrefixes...) }

// SourceKeys returns the exact keys of the step's Source, in key order: the
// two bootstrap rows, which a cutover build declares as exact retired claims
// inside the read-write setting: family.
func SourceKeys() []string { return append([]string(nil), identityKeys...) }

// SensitivePrefixes are the Source prefixes whose quarantined rows may hold
// credentials (05-storage §5S.3.13 Sensitive; DATA-14).
func SensitivePrefixes() []string {
	return []string{prefixUser, idxUserByMail, prefixKey, prefixIdentity}
}

// ---- HEAD's rows, frozen ----

// headUser is store.User as the R0 binary stores it.
type headUser struct {
	ID               string    `json:"id"`
	Username         string    `json:"username"`
	Email            string    `json:"email"`
	PasswordHashAlgo string    `json:"password_hash_algo"`
	PasswordHash     string    `json:"password_hash"`
	Roles            []string  `json:"roles"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Version          int       `json:"version"`
	CredentialEpoch  int64     `json:"credential_epoch,omitempty"`
	EmailSource      string    `json:"email_source,omitempty"`
}

// headRole is store.Role.
type headRole struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	IsSeed      bool      `json:"is_seed,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Version     int       `json:"version"`
}

// headKey is store.APIKey.
type headKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"token_hash"`
	Scopes     []string   `json:"scopes"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	UseCount   int64      `json:"use_count"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// headIdentity is store.OAuthIdentity.
type headIdentity struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Subject   string    `json:"subject"`
	Email     string    `json:"email"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

// headInviteGrant is store.InviteEmailGrant: the row's key carries the folded
// e-mail, the value what the accept recorded (its raw invite token among it,
// which the import drops).
type headInviteGrant struct {
	UserID      string    `json:"user_id"`
	InviteToken string    `json:"invite_token,omitempty"`
	GrantedAt   time.Time `json:"granted_at"`
}

// ---- decoding ----

// rowError is a row that does not decode, named by a fixed class: the text of
// a JSON error can quote the row, and a blocker never carries a value.
type rowError string

func (e rowError) Error() string { return string(e) }

const (
	errNotJSON   rowError = "the row is not a JSON object"
	errBadShape  rowError = "the row has a field of the wrong type"
	errIDKey     rowError = "the row names a different ID than its key"
	errNoID      rowError = "the row has no ID"
	errEmptyKey  rowError = "the key names no ID"
	errBadHash   rowError = "the key's token hash is not a SHA-256"
	errBadSource rowError = "the row has no usable source"
)

// decodeJSON decodes raw into v and classifies a failure.
func decodeJSON(raw []byte, v any) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed[0] != '{' {
		return errNotJSON
	}
	if err := json.Unmarshal(raw, v); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return errNotJSON
		}
		return errBadShape
	}
	return nil
}

// idOf is the ID a primary key names: the key without its prefix.
func idOf(key, prefix string) string { return strings.TrimPrefix(key, prefix) }

func decodeUser(key string, raw []byte) (*headUser, error) {
	id := idOf(key, prefixUser)
	if id == "" {
		return nil, errEmptyKey
	}
	var u headUser
	if err := decodeJSON(raw, &u); err != nil {
		return nil, err
	}
	switch {
	case u.ID == "":
		return nil, errNoID
	case u.ID != id:
		return nil, errIDKey
	}
	return &u, nil
}

func decodeRole(key string, raw []byte) (*headRole, error) {
	id := idOf(key, prefixRole)
	if id == "" {
		return nil, errEmptyKey
	}
	var r headRole
	if err := decodeJSON(raw, &r); err != nil {
		return nil, err
	}
	switch {
	case r.ID == "":
		return nil, errNoID
	case r.ID != id:
		return nil, errIDKey
	}
	return &r, nil
}

func decodeKey(key string, raw []byte) (*headKey, error) {
	id := idOf(key, prefixKey)
	if id == "" {
		return nil, errEmptyKey
	}
	var k headKey
	if err := decodeJSON(raw, &k); err != nil {
		return nil, err
	}
	switch {
	case k.ID == "":
		return nil, errNoID
	case k.ID != id:
		return nil, errIDKey
	}
	return &k, nil
}

func decodeIdentity(key string, raw []byte) (*headIdentity, error) {
	id := idOf(key, prefixIdentity)
	if id == "" {
		return nil, errEmptyKey
	}
	var i headIdentity
	if err := decodeJSON(raw, &i); err != nil {
		return nil, err
	}
	switch {
	case i.ID == "":
		return nil, errNoID
	case i.ID != id:
		return nil, errIDKey
	}
	return &i, nil
}

func decodeInviteGrant(raw []byte) (*headInviteGrant, error) {
	var g headInviteGrant
	if err := decodeJSON(raw, &g); err != nil {
		return nil, err
	}
	return &g, nil
}
