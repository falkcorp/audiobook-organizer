// file: core/rbac/store.go
// version: 1.2.0
// guid: 0b8f4d2e-61c7-4a95-8e3b-7d2c5a19f640
// last-edited: 2026-11-10

package rbac

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// Origin is where a role came from (05-rbac §3.3).
type Origin uint8

const (
	OriginSeed   Origin = iota + 1 // contributed by core or a module manifest
	OriginCustom                   // created by an operator
)

// Role is one stored role (05-rbac §3.3). The native store keeps it at
// c/rbac/role/<name> as codec.JSON[Role](1), every field below.
type Role struct {
	Name        RoleName
	Title       string
	Description string
	Origin      Origin
	// Seed bookkeeping (Origin == OriginSeed): the last seed applied (§4.8).
	SeedPermissions []Permission
	SeedDigest      string       // sha256 of the sorted SeedPermissions
	PendingSeed     []Permission // restricted seed additions held for admin acceptance (§4.8, SEC-25)
	Added           []Permission // custom roles: the role's whole set (patterns allowed); on seed roles the operator diff is deferred-build (§3.6)
	Locked          bool         // "admin": immutable, undeletable, outside the seed merge (§4.8)
	// Confined is seed bookkeeping coined by F4-01 (not in §3.4): sticky
	// confinement. The merge sets it when a release's seed for the role is
	// confining (it names a concrete Confining scope, or is Confined through
	// a wildcard), and only an AcceptSeed that lands a non-Confining scope
	// clears it. While it is set the merge holds every addition that is not a
	// concrete Confining scope. It is a stored fact rather than a reading of
	// the current permissions against the current catalog, so a release that
	// empties the role, stops declaring its worker scope or flips that scope
	// to non-Confining cannot unconfine it, and a plain role whose scopes a
	// release renames is not mistaken for a confined one. Never set on a
	// custom role.
	Confined  bool
	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int64
	// Removed and Tombstone are deferred-build members (§3.6).
}

// Permissions returns the role's effective patterns: SeedPermissions for a
// seed role, Added for a custom role. PendingSeed is not effective until
// accepted (§4.8). A role of any other Origin (the zero value included) has
// no effective permission: an unrecognised record grants nothing. The result
// is a copy.
func (r *Role) Permissions() []Permission {
	if r == nil {
		return nil
	}
	switch r.Origin {
	case OriginSeed:
		return slices.Clone(r.SeedPermissions)
	case OriginCustom:
		return slices.Clone(r.Added)
	default:
		return nil
	}
}

// clone returns a deep copy of r, so a cached role is never shared with a
// caller.
func (r *Role) clone() *Role {
	if r == nil {
		return nil
	}
	c := *r
	c.SeedPermissions = slices.Clone(r.SeedPermissions)
	c.PendingSeed = slices.Clone(r.PendingSeed)
	c.Added = slices.Clone(r.Added)
	return &c
}

// RoleInput is what CreateRole and UpdateRole take (05-rbac §3.3).
type RoleInput struct {
	Name        RoleName // custom.<slug> on create
	Title       string
	Description string
	Permissions []Permission // desired effective set; the service computes Added/Removed for seed roles
}

// Store is the role store (resolution R4-1). It is SDK, frozen by the
// API-stability gate, because other apps and coretest build on it. Core's
// implementation, core/internal/rbacstore, keeps roles in c/rbac/ (§6.3
// "Native store") and is the only implementation the kernel builds. Core
// enforces its invariants above the interface (escalation, last admin);
// rbactest.StoreSuite is the conformance suite. Every write takes the
// caller's *storage.Batch, never a copy (COR2-12), and stages an Expect on
// the role's Version.
//
// The version contract every implementation keeps (rbactest.StoreSuite
// checks it): GetRole and ScanRoles return the stored Version; PutRole writes
// r as given, and r.Version must be the stored Version + 1, or 1 for a role
// that does not exist yet (ErrRoleExists when it does, ErrVersionMismatch
// otherwise); DeleteRole needs r.Version equal to the stored Version. Both
// stage an Expect on the stored record, so a concurrent writer makes the
// batch's Commit fail with storage.ErrConflict. A refused write stages
// nothing. GetRole of an absent role returns an error wrapping
// ErrUnknownRole.
type Store interface {
	GetRole(ctx context.Context, name RoleName) (*Role, error)
	ScanRoles(ctx context.Context) iter.Seq2[*Role, error] // an undecodable stored role yields ErrUndecodableRole for that role only
	PutRole(b *storage.Batch, r *Role) error
	DeleteRole(b *storage.Batch, r *Role) error
}

// Importer is how an app's cutover step brings the custom roles it held
// before core into the native store, once (ABO: step M3 of R2, §6.3).
// core/internal/rbacstore implements it and the kernel hands it to the step
// as part of module.Importers (03 §3.3, 06 §6.4.2); rbactest.StoreSuite
// carries its cases. The step maps the app's
// permission strings to native scopes itself (ABO: §6.1's table, books' code),
// so core holds no app permission string. ImportRole stores r as a custom
// role (OriginCustom, Name custom.<slug>) with its native permissions and
// refuses (ErrImportRejected) an unknown scope or a set no catalog role could
// hold; seed roles are not imported, P7's seed commit writes them (§4.8).
type Importer interface {
	ImportRole(b *storage.Batch, r *Role) error
}

// ErrImportRejected is ImportRole's refusal; nothing was staged.
var ErrImportRejected = errors.New("rbac: imported role breaks a role store invariant")

// ImportSealer is implemented by a role store whose Importer half refuses
// every import once the service is frozen (coined by F4-01). The cutover
// steps run at P4b, before Service.Freeze at P5 (06 §6.5.1), and Freeze loads
// the role cache, so a role imported later would sit in the store unseen by
// Compile, CanAssign and Dominates until a restart. Freeze calls SealImports
// on Deps.Store when it implements this interface; after that, ImportRole
// returns an error wrapping ErrImportRejected and ErrFrozen and stages
// nothing. The kernel must hand the same store instance to Deps.Store and to
// the cutover as Importers.Roles.
type ImportSealer interface {
	SealImports()
}

// customPrefix is the namespace of operator-created role names.
const customPrefix = "custom."

// ValidateCustomRoleName checks a custom role name: "custom." followed by a
// slug of 1 to 32 characters, a lowercase letter first, then lowercase
// letters, digits, '_' and '-' (coined by F4-01: 05-rbac names the
// "custom.<slug>" form without a slug grammar; '-' is allowed because HEAD's
// role IDs, which the cutover slugs, use it). Every seed role name fails it:
// core's seeds have no dot, module seeds are "<moduleID>.<name>" and the
// reserved namespace "custom" is no module ID (ReservedNamespaces).
func ValidateCustomRoleName(name RoleName) error {
	slug, ok := strings.CutPrefix(string(name), customPrefix)
	if !ok || !validSlug(slug) {
		return fmt.Errorf("%w: %q", ErrRoleName, name)
	}
	return nil
}

// validSlug reports [a-z][a-z0-9_-]{0,31}.
func validSlug(s string) bool {
	if len(s) < 1 || len(s) > 32 || !isLower(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isLower(s[i]) && !isDigit(s[i]) && s[i] != '_' && s[i] != '-' {
			return false
		}
	}
	return true
}

// CheckRolePermissions checks the patterns a role may hold against the
// catalog (05-rbac §4.2, §4.9): each parses as a pattern (ErrBadScope), and
// each names something the catalog holds now, a concrete scope it declares or
// a wildcard that expands to at least one (ErrUnknownScope). A nil catalog
// refuses every set. Every problem is returned, joined.
func CheckRolePermissions(cat *Catalog, perms []Permission) error {
	if cat == nil {
		return fmt.Errorf("%w: no catalog to check the role's permissions against", ErrUnknownScope)
	}
	var errs []error
	for _, p := range perms {
		if _, err := ParsePattern(string(p)); err != nil {
			errs = append(errs, err)
			continue
		}
		if len(cat.Expand(p)) == 0 {
			errs = append(errs, fmt.Errorf("%w: %s", ErrUnknownScope, p))
		}
	}
	return errors.Join(errs...)
}

// Covers reports whether the role pattern covers the concrete scope: the
// matching rule of 05-rbac §4.2 without a catalog. A concrete pattern covers
// itself, "<ns>:*" covers every scope of ns, and "<ns>:<action>.*" covers the
// bare action and every sub of it. A malformed pattern or scope covers
// nothing and is covered by nothing. Catalog.Expand and GrantsAdmin use this
// one predicate, and the compiled checker (F4-09) must use it too, so none of
// them can disagree about what a pattern grants.
func Covers(pattern, scope Permission) bool {
	pat, err := ParsePattern(string(pattern))
	if err != nil {
		return false
	}
	s, err := ParsePermission(string(scope))
	if err != nil {
		return false
	}
	switch {
	case !pat.IsWildcard():
		return pat == s
	case strings.HasSuffix(string(pat), ":*"):
		return s.Namespace() == pat.Namespace()
	default: // "<ns>:<action>.*"
		return s.Namespace() == pat.Namespace() && s.Action() == pat.Action()
	}
}

// GrantsAdmin reports whether a pattern set grants core:admin: some pattern
// Covers it (core:admin, core:* or core:admin.*). The last-admin rule, the
// importer's SEC-12 refusal and Confined all use it; auth's last-admin rule
// (F4-02) and authz (F4-09) must too.
func GrantsAdmin(perms []Permission) bool {
	for _, p := range perms {
		if Covers(p, CoreAdmin) {
			return true
		}
	}
	return false
}

// normalize returns perms sorted with duplicates removed, as a fresh slice;
// nil for an empty set.
func normalize(perms []Permission) []Permission {
	if len(perms) == 0 {
		return nil
	}
	out := slices.Clone(perms)
	slices.Sort(out)
	return slices.Compact(out)
}
