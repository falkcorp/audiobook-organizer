// file: internal/books/cutover/m3/mapping.go
// version: 1.0.0
// guid: f249ba05-6014-44f4-93b5-2891a530a57c
// last-edited: 2026-11-12

package m3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// This file is the mapping of 05-authn §11's table and 05-rbac §6.1–§6.3: pure
// functions of a HEAD row and the Config, so Step and Preflight apply one rule.

// ---- roles ----

type verdictKind int

const (
	verdictSeed       verdictKind = iota // a seed role: its row is deleted, core's seed supplies it
	verdictImport                        // a custom role: imported as custom.<slug>
	verdictQuarantine                    // a role the import cannot take: quarantined
)

// roleVerdict is what becomes of one HEAD role row. Deciding it is a function of
// the row, the atom table and the native roles already stored, so the step
// that imports the role and the step that rewrites a user's role list agree.
type roleVerdict struct {
	kind    verdictKind
	name    rbac.RoleName     // the native name (a seed's is its ID)
	perms   []rbac.Permission // verdictImport: the native permissions, ambient included
	unknown []string          // atoms outside the table: they grant nothing
	reason  string            // verdictQuarantine: a fixed class, never a value
}

// slugOf is the slug of a HEAD role ID: lower-cased, every byte outside
// [a-z0-9_-] a '-', "role-" ahead of an ID that does not start with a letter
// (a custom role's ID is a ULID, which starts with a digit), and a long ID cut
// to 23 bytes plus '-' and 8 hex digits of the ID's SHA-256, so two long IDs
// never meet. "" when the ID has nothing to slug.
func slugOf(id string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	s := strings.Trim(sb.String(), "-_")
	if s == "" {
		return ""
	}
	if s[0] < 'a' || s[0] > 'z' {
		s = "role-" + s
	}
	if len(s) > 32 {
		sum := sha256.Sum256([]byte(id))
		s = strings.TrimRight(s[:23], "-_") + "-" + hex.EncodeToString(sum[:4])
	}
	return s
}

// nativeRoleName is the custom role name of a HEAD role ID, "" when it has none.
func nativeRoleName(id string) rbac.RoleName {
	slug := slugOf(id)
	if slug == "" {
		return ""
	}
	name := rbac.RoleName("custom." + slug)
	if rbac.ValidateCustomRoleName(name) != nil {
		return ""
	}
	return name
}

// scopesOf is the union of the native scopes of atoms, deduplicated in table
// order (the implication closure is the catalog's, at check time), and the
// atoms the table does not know, in the order given. A repeated atom counts once.
func (m *Migration) scopesOf(atoms []string) (scopes []rbac.Permission, unknown []string) {
	for _, name := range m.names {
		if slices.Contains(atoms, name) {
			for _, s := range m.atoms[name] {
				if !slices.Contains(scopes, s) {
					scopes = append(scopes, s)
				}
			}
		}
	}
	for _, a := range atoms {
		if _, ok := m.atoms[a]; !ok && !slices.Contains(unknown, a) {
			unknown = append(unknown, a)
		}
	}
	return scopes, unknown
}

// verdictOf decides a decoded role row. taken reports whether the native name
// is already in use (an earlier role of this run, or a role a committed chunk
// imported); a role whose name is taken is quarantined, never merged into the
// other.
func (m *Migration) verdictOf(h *headRole, taken func(rbac.RoleName) bool) roleVerdict {
	if s, ok := m.seeds[h.ID]; ok {
		return roleVerdict{kind: verdictSeed, name: rbac.RoleName(s.ID)}
	}
	name := nativeRoleName(h.ID)
	if name == "" {
		return roleVerdict{kind: verdictQuarantine, reason: "the role's ID cannot be named custom.<slug>"}
	}
	if taken != nil && taken(name) {
		return roleVerdict{kind: verdictQuarantine, name: name, reason: "the role's native name is already taken"}
	}
	scopes, unknown := m.scopesOf(h.Permissions)
	if len(h.Permissions) > 0 && len(scopes) == 0 {
		return roleVerdict{kind: verdictQuarantine, name: name, unknown: unknown, reason: "every atom of the role is unknown"}
	}
	perms, err := rbac.WithAmbient(m.cfg.Catalog, scopes, m.cfg.Ambient...)
	if err != nil {
		return roleVerdict{kind: verdictQuarantine, name: name, unknown: unknown, reason: "the role's scopes are refused by the catalog"}
	}
	return roleVerdict{kind: verdictImport, name: name, perms: perms, unknown: unknown}
}

// nativeRole is the rbac.Role an import stages for h.
func nativeRole(h *headRole, v roleVerdict) *rbac.Role {
	title := strings.TrimSpace(h.Name)
	if title == "" {
		title = h.ID
	}
	return &rbac.Role{
		Name: v.name, Title: title, Description: h.Description, Origin: rbac.OriginCustom,
		Added: slices.Clone(v.perms), CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt, Version: 1,
	}
}

// ---- users ----

// userStatus is HEAD's UserIsActive applied to a status (trimmed, folded,
// empty counts as active): active stays active, anything else is disabled, and
// the second result names the status when it was not "inactive" (SEC-11).
func userStatus(raw string) (auth.UserStatus, string) {
	norm := strings.ToLower(strings.TrimSpace(raw))
	switch norm {
	case "", "active":
		return auth.StatusActive, ""
	case "inactive":
		return auth.StatusDisabled, ""
	}
	return auth.StatusDisabled, clip(norm, maxStatusLen)
}

// trustedEmailSource is HEAD's trustedEmailSource: the sources that prove the
// address (pebble_store_verified_email_backfill.go). HEAD's reconcile stamps
// a row that has an address and no source at every store open, so on the
// pre-freeze release's store every row with an address has one; a row without
// is unverified, as HEAD reads it until its next open.
func trustedEmailSource(source string) bool {
	switch source {
	case "setup", "admin", "invite", "idp":
		return true
	}
	return false
}

// mapUser is the native user of a HEAD row and the password hash to store.
// roles are the native role names its HEAD role IDs became. A blanked hash
// under a password algorithm other than "oauth" requires a reset (a restore or
// a factory credential reset blanked it); an account that never had an
// algorithm (the system user) has no password to reset.
func mapUser(h *headUser, roles []rbac.RoleName) (auth.User, string, string) {
	status, reported := userStatus(h.Status)
	u := auth.User{
		ID:                    auth.UserID(h.ID),
		Username:              h.Username,
		Email:                 h.Email,
		EmailVerified:         auth.NormalizeEmail(h.Email) != "" && trustedEmailSource(h.EmailSource),
		HasPassword:           h.PasswordHash != "",
		Roles:                 roles,
		Status:                status,
		CredentialEpoch:       h.CredentialEpoch,
		PasswordResetRequired: h.PasswordHash == "" && h.PasswordHashAlgo != "" && h.PasswordHashAlgo != "oauth",
		CreatedAt:             h.CreatedAt,
		UpdatedAt:             h.UpdatedAt,
		Version:               1,
	}
	return u, h.PasswordHash, reported
}

// ---- keys ----

// keyStatus is the status of a HEAD key (trimmed, lower-cased first): revoked,
// or any row with revoked_at set, is revoked; inactive is disabled; "" and
// active are active (HEAD admits both, middleware/auth.go); anything else is
// disabled and named (reported by key ID).
func keyStatus(raw string, revokedAt *time.Time) (status auth.KeyStatus, odd string) {
	norm := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case norm == "revoked" || (revokedAt != nil && !revokedAt.IsZero()):
		return auth.KeyRevoked, ""
	case norm == "inactive":
		return auth.KeyDisabled, ""
	case norm == "" || norm == "active":
		return auth.KeyActive, ""
	}
	return auth.KeyDisabled, clip(norm, maxStatusLen)
}

// keyScopes is an imported key's native scopes (05-rbac §6.3): the effective
// set at the cutover, i.e. the native scopes of its atoms (an empty list, HEAD's
// "all the owner's role permissions", is the owner's set) intersected with the
// owner's native role set as a role-only checker compiles it, plus the ambient
// scopes unless that set is Confined. Only when the owner's roles hold
// core:admin does a key holding every atom, or with an empty list, get
// core:admin and every row's scopes. An atom outside the table grants nothing
// and is returned.
func (m *Migration) keyScopes(atoms []string, owner []rbac.Permission) (scopes []rbac.Permission, unknown []string, err error) {
	known, unknown := m.scopesOf(atoms)
	admin := rbac.GrantsAdmin(owner)
	every := len(atoms) > 0
	for _, n := range m.names {
		if !slices.Contains(atoms, n) {
			every = false
			break
		}
	}
	var base []rbac.Permission
	switch {
	case admin && (len(atoms) == 0 || every):
		base = append([]rbac.Permission{rbac.CoreAdmin}, m.every...)
	case len(atoms) == 0:
		base = m.expand(owner)
	default:
		base = known
	}
	if !admin {
		held := m.closure(m.expand(owner))
		base = slices.DeleteFunc(slices.Clone(base), func(s rbac.Permission) bool { return !slices.Contains(held, s) })
	}
	out, err := rbac.WithAmbient(m.cfg.Catalog, base, m.cfg.Ambient...)
	return out, unknown, err
}

// expand is the concrete scopes the catalog holds under perms (patterns allowed).
func (m *Migration) expand(perms []rbac.Permission) []rbac.Permission {
	var out []rbac.Permission
	for _, p := range perms {
		for _, c := range m.cfg.Catalog.Expand(p) {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// closure is scopes plus every scope they imply, transitively.
func (m *Migration) closure(scopes []rbac.Permission) []rbac.Permission {
	out := slices.Clone(scopes)
	for i := 0; i < len(out); i++ {
		spec, ok := m.cfg.Catalog.Lookup(out[i])
		if !ok {
			continue
		}
		for _, imp := range spec.Implies {
			if !slices.Contains(out, imp) {
				out = append(out, imp)
			}
		}
	}
	return out
}

// errNoTokenHash is a key whose stored hash is not 64 hex digits.
var errNoTokenHash = errors.New("the key's token hash is not a SHA-256")

// tokenHash decodes HEAD's stored hash (hex of the SHA-256 of the raw key).
func tokenHash(s string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != len(out) {
		return out, errNoTokenHash
	}
	copy(out[:], raw)
	if out == ([32]byte{}) {
		return out, errNoTokenHash
	}
	return out, nil
}

// keyNotes are the facts about a mapped key the step reports.
type keyNotes struct {
	oddStatus string // a status HEAD does not define, imported disabled
	stamped   bool   // a live key with no expiry got the one-shot stamp
	unknown   []string
}

// mapKey is the native key of a HEAD row with its scopes already decided, and
// the SHA-256 it verifies by.
func (m *Migration) mapKey(h *headKey, scopes []rbac.Permission) (auth.APIKey, [32]byte, keyNotes, error) {
	var notes keyNotes
	hash, err := tokenHash(h.TokenHash)
	if err != nil {
		return auth.APIKey{}, hash, notes, err
	}
	status, odd := keyStatus(h.Status, h.RevokedAt)
	notes.oddStatus = odd
	k := auth.APIKey{
		ID: h.ID, Owner: auth.UserID(h.UserID), Name: h.Name, Scopes: scopes, Status: status,
		CreatedAt: h.CreatedAt, UseCount: h.UseCount, Imported: true, Version: 1,
	}
	if h.LastUsedAt != nil {
		k.LastUsedAt = *h.LastUsedAt
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(h.LastUsedIP)); err == nil {
		k.LastUsedIP = ip
	}
	if h.ExpiresAt != nil {
		k.ExpiresAt = *h.ExpiresAt
	}
	if status == auth.KeyRevoked {
		if h.RevokedAt != nil && !h.RevokedAt.IsZero() {
			k.RevokedAt = *h.RevokedAt
		} else {
			k.RevokedAt = m.cfg.Now().UTC()
		}
	} else if k.ExpiresAt.IsZero() {
		k.ExpiresAt = m.cfg.Now().UTC().Add(m.cfg.KeyTTL)
		notes.stamped = true
	}
	return k, hash, notes, nil
}

// ---- identities ----

func mapIdentity(h *headIdentity) *auth.ExternalIdentity {
	return &auth.ExternalIdentity{Provider: h.Provider, Subject: h.Subject, UserID: auth.UserID(h.UserID), Email: h.Email, CreatedAt: h.CreatedAt}
}

// ---- owners ----

// ownerPerms is the native permission patterns of the roles a user holds: a
// seed role's set, a custom role's imported permissions. lookup resolves a
// custom role (a role of this chunk, or one a committed chunk imported). A
// custom role that lookup cannot resolve is an error, never an empty grant: a
// user's role list names only roles this step imported (roleList drops the
// rest), so a role that is nowhere means the step's own bookkeeping is wrong,
// and an owner set silently missing a role would narrow every key it owns.
func (m *Migration) ownerPerms(ctx context.Context, roles []rbac.RoleName, lookup func(context.Context, rbac.RoleName) ([]rbac.Permission, bool, error)) ([]rbac.Permission, error) {
	var out []rbac.Permission
	for _, r := range roles {
		if s, ok := m.seeds[string(r)]; ok {
			out = append(out, s.Permissions...)
			continue
		}
		perms, ok, err := lookup(ctx, r)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("m3: the custom role %s of a user is neither imported by this chunk nor stored", r)
		}
		out = append(out, perms...)
	}
	return out, nil
}
