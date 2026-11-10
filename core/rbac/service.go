// file: core/rbac/service.go
// version: 1.1.0
// guid: 9d2b7e41-5c3a-4f86-b0e9-1a6c8f3d5e27
// last-edited: 2026-11-10

package rbac

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
)

// The rest of 05-rbac §3.4's error block. ErrBadScope (permission.go),
// ErrForeignNamespace, ErrImpliesCycle and ErrUnknownScope (catalog.go) are
// declared where they were first needed.
var (
	ErrDenied          = errors.New("rbac: permission denied")
	ErrCoreGrant       = errors.New("rbac: modules may not grant core scopes")
	ErrRestrictedGrant = errors.New("rbac: DefaultGrants may not name a Sensitive, AuthRequired, Confining or *:settings.manage / *:integrations.manage scope")
	ErrWildcardInKey   = errors.New("rbac: wildcards are not allowed in API key scopes")
	ErrEscalation      = errors.New("rbac: actor lacks scopes being granted")
	ErrDominance       = errors.New("rbac: actor does not dominate the target user")
	ErrLockedRole      = errors.New("rbac: role is locked")
	ErrRoleExists      = errors.New("rbac: role exists")
	ErrVersionMismatch = errors.New("rbac: version mismatch")
	ErrLastAdmin       = errors.New("rbac: change would remove the last core:admin")
	ErrSeedRoleLocked  = errors.New("rbac: seed roles are read-only until seed-role editing lands")
	ErrUndecodableRole = errors.New("rbac: stored role cannot be decoded") // per row, from Store.ScanRoles (§6.3)
)

// Errors coined by F4-01 where 05-rbac names a refusal but no sentinel
// (seams.d/F4-01.md).
var (
	// ErrUnknownRole is a role name no stored role has: Store.GetRole of an
	// absent role, a write to one, a DefaultGrant naming a seed role nobody
	// declared, and CanAssign of an unknown role by an actor without core:admin.
	ErrUnknownRole = errors.New("rbac: unknown role")
	// ErrRoleName is a custom role name that is not custom.<slug>, or an
	// UpdateRole input that renames the role.
	ErrRoleName = errors.New("rbac: a custom role is named custom.<slug>")
	// ErrRoleInUse is DeleteRole of a role some user holds (§4.7 rule 4,
	// 409 role_in_use), or of any role when no Holders is wired.
	ErrRoleInUse = errors.New("rbac: role is assigned to users")
	// ErrNoPendingSeed is AcceptSeed of a role with nothing pending.
	ErrNoPendingSeed = errors.New("rbac: the role has no pending seed permissions")
	// ErrConfiningSeed is a seed declaration that would give a confined role
	// a non-Confining scope (§4.8, TestSeed_NoNonConfiningScopeOnConfinedRole).
	ErrConfiningSeed = errors.New("rbac: a seed role holding a Confining scope holds only Confining scopes")
	// ErrFrozen is a declaration after Freeze, or a second Freeze.
	ErrFrozen = errors.New("rbac: the catalog is frozen")
	// ErrNotFrozen is a call that needs the frozen catalog before Freeze.
	ErrNotFrozen = errors.New("rbac: the catalog is not frozen yet")
)

// DefaultCacheSize is core.rbac.cache_size's default (05-rbac §10).
const DefaultCacheSize = 4096

// maxWriteAttempts bounds the retries of a role write whose Commit failed on
// a precondition (another writer, or a user record a Holders read changed).
const maxWriteAttempts = 3

// Options is what New takes beyond its dependencies.
type Options struct {
	// CacheSize is core.rbac.cache_size: the compiled checkers kept (0 means
	// DefaultCacheSize).
	CacheSize int
	// Now is the clock of role timestamps and revocation lines; nil is
	// time.Now.
	Now func() time.Time
	// Source is the writing binary's ID, stamped on every revocation line
	// (journal.Revocation.Source; app.BinaryID).
	Source string
}

// Deps is what the kernel hands New (05-rbac §3.4). Store, Compile, Audit,
// Revocations and NewBatch are required; Holders may be nil, which refuses
// every DeleteRole and every change that takes core:admin from a role.
//
// NewBatch and Holders are coined by F4-01 (seams.d/F4-01.md): the role
// writes need a batch to stage into, and the Store interface opens none;
// the last-admin and role-in-use rules need the users who hold a role, which
// live in auth's store, and rbac never imports auth.
type Deps struct {
	Store       Store                                // core/internal/rbacstore over c/rbac/ (§6.3, R4-1)
	Compile     func(CompileInput, *Catalog) Checker // injected by the kernel: authz.Compile (COR4-12)
	Audit       audit.Logger
	OnChange    func(generation uint64)   // kernel publishes core.rbac.changed.v1 on the bus; rbac never imports events
	Revocations journal.RevocationJournal // Append(ctx, b, r) of role.unassigned / role.narrowed / role.deleted into the change's batch b before it commits (§7.1, 05-backup §5B.3.6)
	Log         *slog.Logger
	Metrics     *metrics.Scope // "mediacore.rbac" (05-metrics)
	// NewBatch opens a batch over the role keyspace; the kernel passes the
	// native store's NewBatch.
	NewBatch func() *storage.Batch
	// Holders reads role assignments for the last-admin and role-in-use
	// rules; the kernel implements it over auth's user records.
	Holders Holders
}

// Holder is one user whose role list names a role.
type Holder struct {
	UserID string
	Active bool // the user can sign in (05-authn: status active)
}

// Holders is rbac's view of role assignments, which live in auth's user
// records (05-authn §3.4).
type Holders interface {
	// HoldersOf returns every user whose role list names any of roles,
	// reading through b so that b's Commit fails (storage.ErrConflict) when
	// a user record it read changes first. That is what makes the last-admin
	// check hold against a concurrent demotion in auth (05-rbac §9).
	HoldersOf(ctx context.Context, b *storage.Batch, roles []RoleName) ([]Holder, error)
}

// CheckerInput is what a caller asks Compile for (05-rbac §3.4).
type CheckerInput struct {
	Roles       []RoleName
	Scopes      []Permission // key scopes; nil = role permissions only (sessions)
	Interactive bool         // auth.Method.Interactive() of the credential (§4.4)
}

// CompileInput is what Service.Compile hands Deps.Compile: the caller's input
// plus the named roles' patterns, resolved by Service.Compile from its role
// cache at the current Generation (unknown role IDs already skipped and
// counted). authz.Compile never reads the store or the role cache itself.
type CompileInput struct {
	CheckerInput
	RolePatterns []Permission // union of the patterns of every known role in Roles, deduplicated (core:admin included when a role holds it)
}

// Service is core's authorization service (05-rbac §3.4): the catalog, the
// roles, the seed merge and the compiled-checker cache.
type Service struct {
	deps Deps
	log  *slog.Logger
	now  func() time.Time
	src  string

	declMu   sync.Mutex
	decls    []*moduleDecl
	declErrs []error
	state    atomic.Pointer[frozenState] // nil until Freeze

	rolesMu sync.RWMutex
	roles   map[RoleName]*Role // decodable stored roles; written only under writeMu
	gen     atomic.Uint64      // bumped under rolesMu, after roles changed

	// writeMu serialises role writes: the last-admin rule reads the role set
	// from roles, so two writes must not both decide on the same view.
	writeMu sync.Mutex

	cache *checkerCache
	ctr   counters

	// beforeCommit runs after a write has staged everything and before its
	// Commit: the concurrency tests' seam. Nil outside tests.
	beforeCommit func()
}

type frozenState struct {
	cat   *Catalog
	seeds []seedDef // admin first, then by name
}

// moduleDecl is one module's declarations, kept until Freeze.
type moduleDecl struct {
	module   string
	imported []RoleName
	specs    []PermissionSpec
	seeds    []RoleSeed
}

// New builds the service. It writes nothing and reads nothing: the catalog
// is assembled by Declarer and Freeze, and the seed roles are written at P7
// by CommitSeeds (05-rbac §5).
func New(ctx context.Context, opts Options, deps Deps) (*Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var missing []string
	if deps.Store == nil {
		missing = append(missing, "Store")
	}
	if deps.Compile == nil {
		missing = append(missing, "Compile")
	}
	if deps.Audit == nil {
		missing = append(missing, "Audit")
	}
	if deps.Revocations == nil {
		missing = append(missing, "Revocations")
	}
	if deps.NewBatch == nil {
		missing = append(missing, "NewBatch")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("rbac: New needs Deps.%s", strings.Join(missing, ", Deps."))
	}
	size := opts.CacheSize
	switch {
	case size < 0:
		return nil, fmt.Errorf("rbac: cache size %d is negative", size)
	case size == 0:
		size = DefaultCacheSize
	}
	s := &Service{
		deps:  deps,
		log:   deps.Log,
		now:   opts.Now,
		src:   opts.Source,
		roles: map[RoleName]*Role{},
		cache: newCheckerCache(size),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if deps.Metrics != nil {
		if err := s.instrument(deps.Metrics); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Catalog assembly (§4.9) and Freeze (§5 item 4).

// Declarer returns the declarer of moduleID's manifest (kernel-only, P5).
// importedRoleIDs is the module's Manifest.ImportedRoleIDs: the un-namespaced
// role IDs its RoleSeeds may use (books: "fp-worker"; 06 §6.4.2, COR5-5).
// The parameter is coined by F4-01: 05-rbac §4.9 admits such a seed only when
// the manifest lists it, and the spec's Declarer(moduleID) has no input for
// the list. Every declaration is checked twice: on the call (its error is
// returned) and again at Freeze, which fails if any call failed.
func (s *Service) Declarer(moduleID string, importedRoleIDs ...RoleName) Declarer {
	s.declMu.Lock()
	defer s.declMu.Unlock()
	for _, d := range s.decls {
		if d.module == moduleID {
			for _, id := range importedRoleIDs {
				if !slices.Contains(d.imported, id) {
					d.imported = append(d.imported, id)
				}
			}
			return &declarer{s: s, d: d}
		}
	}
	d := &moduleDecl{module: moduleID, imported: slices.Clone(importedRoleIDs)}
	s.decls = append(s.decls, d)
	return &declarer{s: s, d: d}
}

type declarer struct {
	s *Service
	d *moduleDecl
}

// Permissions declares the module's scopes.
func (dc *declarer) Permissions(specs ...PermissionSpec) error {
	s := dc.s
	s.declMu.Lock()
	defer s.declMu.Unlock()
	if s.state.Load() != nil {
		return fmt.Errorf("%w: %s declared permissions after Freeze", ErrFrozen, dc.d.module)
	}
	var errs []error
	if err := checkModuleID(dc.d.module); err != nil {
		errs = append(errs, err)
	}
	for _, spec := range specs {
		if err := checkModuleSpec(dc.d, spec); err != nil {
			errs = append(errs, err)
			continue
		}
		dc.d.specs = append(dc.d.specs, spec.clone())
	}
	err := errors.Join(errs...)
	if err != nil {
		s.declErrs = append(s.declErrs, err)
	}
	return err
}

// Roles declares the module's own seed roles.
func (dc *declarer) Roles(seeds ...RoleSeed) error {
	s := dc.s
	s.declMu.Lock()
	defer s.declMu.Unlock()
	if s.state.Load() != nil {
		return fmt.Errorf("%w: %s declared roles after Freeze", ErrFrozen, dc.d.module)
	}
	var errs []error
	if err := checkModuleID(dc.d.module); err != nil {
		errs = append(errs, err)
	}
	for _, seed := range seeds {
		if err := checkRoleSeed(dc.d, seed); err != nil {
			errs = append(errs, err)
			continue
		}
		seed.Permissions = slices.Clone(seed.Permissions)
		dc.d.seeds = append(dc.d.seeds, seed)
	}
	err := errors.Join(errs...)
	if err != nil {
		s.declErrs = append(s.declErrs, err)
	}
	return err
}

// checkModuleID refuses a module ID that is no permission namespace or that
// core reserves (ReservedNamespaces): core declares its own scopes itself.
func checkModuleID(id string) error {
	if !validNamespace(id) || slices.Contains(ReservedNamespaces(), id) {
		return fmt.Errorf("%w: %q cannot declare permissions (not a module namespace)", ErrForeignNamespace, id)
	}
	return nil
}

// isCoreSeed reports one of core's seed role names.
func isCoreSeed(n RoleName) bool { return n == RoleAdmin || n == RoleEditor || n == RoleViewer }

// ownsRole reports whether d may name role n: "<module>.<name>" or one of its
// imported IDs.
func (d *moduleDecl) ownsRole(n RoleName) bool {
	if rest, ok := strings.CutPrefix(string(n), d.module+"."); ok {
		return validName(rest)
	}
	return slices.Contains(d.imported, n) && validImportedID(n)
}

// validImportedID: an imported role ID is neither a core seed name nor a
// custom role name, and carries no namespace dot.
func validImportedID(n RoleName) bool {
	return n != "" && !isCoreSeed(n) && !strings.Contains(string(n), ".")
}

// restrictedScope reports a scope no DefaultGrant may name and the seed merge
// holds for acceptance when it is added to an existing role (§4.8, §4.9).
func restrictedScope(spec PermissionSpec) bool {
	if spec.Sensitive || spec.AuthRequired || spec.Confining {
		return true
	}
	_, rest, _ := strings.Cut(string(spec.Name), ":")
	return rest == "settings.manage" || rest == "integrations.manage"
}

// checkModuleSpec is §4.9's per-spec rules that need no catalog.
func checkModuleSpec(d *moduleDecl, spec PermissionSpec) error {
	name, err := ParsePermission(string(spec.Name))
	if err != nil {
		return fmt.Errorf("%s: %w", d.module, err)
	}
	switch ns := name.Namespace(); {
	case ns == "core":
		return fmt.Errorf("%w: %s declares %s", ErrCoreGrant, d.module, name)
	case ns != d.module:
		return fmt.Errorf("%w: %s declares %s", ErrForeignNamespace, d.module, name)
	}
	var errs []error
	for _, g := range spec.DefaultGrants {
		if g != RoleEditor && g != RoleViewer && !d.ownsRole(g) {
			errs = append(errs, fmt.Errorf("%w: %s grants %s to %q (editor, viewer or the module's own roles only)", ErrForeignNamespace, d.module, name, g))
		}
	}
	if len(spec.DefaultGrants) > 0 && restrictedScope(spec) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrRestrictedGrant, name))
	}
	return errors.Join(errs...)
}

// checkRoleSeed is §4.9's per-seed rules that need no catalog.
func checkRoleSeed(d *moduleDecl, seed RoleSeed) error {
	var errs []error
	if !d.ownsRole(seed.Name) {
		errs = append(errs, fmt.Errorf("%w: %s declares seed role %q (want %s.<name> or an ImportedRoleIDs entry)", ErrForeignNamespace, d.module, seed.Name, d.module))
	}
	for _, p := range seed.Permissions {
		pat, err := ParsePattern(string(p))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s seed %s: %w", d.module, seed.Name, err))
			continue
		}
		switch ns := pat.Namespace(); {
		case ns == "core":
			errs = append(errs, fmt.Errorf("%w: %s seed %s holds %s", ErrCoreGrant, d.module, seed.Name, pat))
		case ns != d.module:
			errs = append(errs, fmt.Errorf("%w: %s seed %s holds %s", ErrForeignNamespace, d.module, seed.Name, pat))
		}
	}
	return errors.Join(errs...)
}

// Freeze validates every declaration (§4.9), builds the catalog with core's
// fixed scopes, computes the seed of every seed role (§4.8) and loads the
// stored roles into the role cache, then bumps the generation. It writes
// nothing: CommitSeeds writes the seed rows at P7. Every problem is returned,
// joined; on error the service stays unfrozen.
func (s *Service) Freeze(ctx context.Context) error {
	s.declMu.Lock()
	defer s.declMu.Unlock()
	if s.state.Load() != nil {
		return ErrFrozen
	}
	errs := slices.Clone(s.declErrs)
	specs := CoreScopes()
	for _, d := range s.decls {
		specs = append(specs, d.specs...)
	}
	cat, err := NewCatalog(specs...)
	if err != nil {
		errs = append(errs, err)
	}
	var seeds []seedDef
	if cat != nil {
		seeds, err = buildSeeds(cat, s.decls)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("rbac: freeze: %w", err)
	}
	roles, err := s.loadRoles(ctx)
	if err != nil {
		return err
	}
	s.rolesMu.Lock()
	s.roles = roles
	s.gen.Add(1)
	s.rolesMu.Unlock()
	s.state.Store(&frozenState{cat: cat, seeds: seeds})
	return nil
}

// loadRoles reads every stored role. An undecodable row is skipped and
// logged: it grants nothing (fail closed) and one bad row never stops the
// boot (05-rbac §6.3); any other error does.
func (s *Service) loadRoles(ctx context.Context) (map[RoleName]*Role, error) {
	out := map[RoleName]*Role{}
	for r, err := range s.deps.Store.ScanRoles(ctx) {
		if errors.Is(err, ErrUndecodableRole) {
			s.log.Warn("rbac: skipping a stored role that does not decode; it grants nothing", "error", err)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rbac: load roles: %w", err)
		}
		if r == nil {
			continue
		}
		out[r.Name] = r.clone()
	}
	return out, nil
}

// frozen returns the frozen state, or ErrNotFrozen.
func (s *Service) frozen() (*frozenState, error) {
	st := s.state.Load()
	if st == nil {
		return nil, ErrNotFrozen
	}
	return st, nil
}

// Catalog returns the frozen catalog; nil before Freeze.
func (s *Service) Catalog() *Catalog {
	if st := s.state.Load(); st != nil {
		return st.cat
	}
	return nil
}

// Generation is bumped by Freeze and by every committed role change; a
// compiled checker is cached under the generation it was built at.
func (s *Service) Generation() uint64 { return s.gen.Load() }

// ---------------------------------------------------------------------------
// Roles CRUD (§4.7).

// Roles yields every stored role from the store, in name order; an
// undecodable row yields ErrUndecodableRole for that row only.
func (s *Service) Roles(ctx context.Context) iter.Seq2[*Role, error] {
	return s.deps.Store.ScanRoles(ctx)
}

// Role reads one stored role (ErrUnknownRole when absent).
func (s *Service) Role(ctx context.Context, id RoleName) (*Role, error) {
	return s.deps.Store.GetRole(ctx, id)
}

// CreateRole creates the custom role in.Name (custom.<slug>) holding
// in.Permissions (patterns allowed, each in the catalog). The actor must hold
// every permission (§4.7 rule 1, see holds). Audited core.role.create in the
// same batch.
func (s *Service) CreateRole(ctx context.Context, actor Checker, in RoleInput) (*Role, error) {
	st, err := s.frozen()
	if err != nil {
		return nil, err
	}
	if err := ValidateCustomRoleName(in.Name); err != nil {
		return nil, err
	}
	perms := normalize(in.Permissions)
	if err := CheckRolePermissions(st.cat, perms); err != nil {
		return nil, err
	}
	if err := s.escalation(ctx, st.cat, actor, in.Name, perms); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	r := &Role{
		Name: in.Name, Title: in.Title, Description: in.Description, Origin: OriginCustom,
		Added: perms, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	err = s.write(ctx, func(b *storage.Batch) ([]*roleChange, error) {
		if err := s.deps.Store.PutRole(b, r); err != nil {
			return nil, err
		}
		if err := s.record(ctx, b, "core.role.create", in.Name, map[string]any{"permissions": permStrings(perms)}); err != nil {
			return nil, err
		}
		return []*roleChange{{name: in.Name, role: r}}, nil
	})
	if err != nil {
		return nil, err
	}
	return r.clone(), nil
}

// UpdateRole replaces a custom role's title, description and permissions
// (§4.7). Seed roles answer ErrSeedRoleLocked and admin ErrLockedRole. The
// actor must hold every permission of both the stored and the new set: an
// edit acts on every holder of the role, so removing a scope is checked like
// granting it, as CanAssign checks removal (§4.7 rule 2). A narrowing
// journals role.narrowed and, when the role stops granting core:admin, must
// leave an active admin (ErrLastAdmin).
func (s *Service) UpdateRole(ctx context.Context, actor Checker, id RoleName, in RoleInput, ifVersion int64) (*Role, error) {
	st, err := s.frozen()
	if err != nil {
		return nil, err
	}
	if in.Name != "" && in.Name != id {
		return nil, fmt.Errorf("%w: %s cannot be renamed to %s", ErrRoleName, id, in.Name)
	}
	perms := normalize(in.Permissions)
	if err := CheckRolePermissions(st.cat, perms); err != nil {
		return nil, err
	}
	var out *Role
	err = s.write(ctx, func(b *storage.Batch) ([]*roleChange, error) {
		cur, err := s.editable(ctx, id, ifVersion)
		if err != nil {
			return nil, err
		}
		old := cur.Permissions()
		if err := s.escalation(ctx, st.cat, actor, id, union(old, perms)); err != nil {
			return nil, err
		}
		next := cur.clone()
		next.Title, next.Description, next.Added = in.Title, in.Description, perms
		next.UpdatedAt = s.now().UTC()
		next.Version = cur.Version + 1
		if err := s.checkLastAdmin(ctx, b, map[RoleName][]Permission{id: perms}); err != nil {
			return nil, err
		}
		removed := minus(old, perms)
		if len(removed) > 0 {
			if err := s.revoke(ctx, b, journal.Revocation{Kind: journal.RoleNarrowed, Subject: roleRef(id), Detail: permStrings(removed)}); err != nil {
				return nil, err
			}
		}
		if err := s.deps.Store.PutRole(b, next); err != nil {
			return nil, err
		}
		details := map[string]any{"added": permStrings(minus(perms, old)), "removed": permStrings(removed)}
		if err := s.record(ctx, b, "core.role.update", id, details); err != nil {
			return nil, err
		}
		out = next
		return []*roleChange{{name: id, role: next}}, nil
	})
	if err != nil {
		return nil, err
	}
	return out.clone(), nil
}

// DeleteRole deletes an unassigned custom role (§4.7 rule 4: a role some
// user holds answers ErrRoleInUse; reassignment is not part of this
// signature). The actor must hold every permission of the role. Journals
// role.deleted and audits core.role.delete in the same batch.
func (s *Service) DeleteRole(ctx context.Context, actor Checker, id RoleName, ifVersion int64) error {
	st, err := s.frozen()
	if err != nil {
		return err
	}
	return s.write(ctx, func(b *storage.Batch) ([]*roleChange, error) {
		cur, err := s.editable(ctx, id, ifVersion)
		if err != nil {
			return nil, err
		}
		if err := s.escalation(ctx, st.cat, actor, id, cur.Permissions()); err != nil {
			return nil, err
		}
		if s.deps.Holders == nil {
			return nil, fmt.Errorf("%w: %s: no role-holder source is wired, so the role cannot be shown unassigned", ErrRoleInUse, id)
		}
		hs, err := s.deps.Holders.HoldersOf(ctx, b, []RoleName{id})
		if err != nil {
			return nil, fmt.Errorf("rbac: read the holders of %s: %w", id, err)
		}
		if len(hs) > 0 {
			return nil, fmt.Errorf("%w: %s is held by %d user(s)", ErrRoleInUse, id, len(hs))
		}
		if err := s.revoke(ctx, b, journal.Revocation{Kind: journal.RoleDeleted, Subject: roleRef(id)}); err != nil {
			return nil, err
		}
		if err := s.deps.Store.DeleteRole(b, cur); err != nil {
			return nil, err
		}
		if err := s.record(ctx, b, "core.role.delete", id, map[string]any{"permissions": permStrings(cur.Permissions())}); err != nil {
			return nil, err
		}
		return []*roleChange{{name: id}}, nil
	})
}

// editable reads the role a write changes and refuses what may not be
// edited: an absent role, admin (ErrLockedRole), a seed role
// (ErrSeedRoleLocked) and a stale version (ErrVersionMismatch).
func (s *Service) editable(ctx context.Context, id RoleName, ifVersion int64) (*Role, error) {
	cur, err := s.deps.Store.GetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case cur.Locked || id == RoleAdmin:
		return nil, fmt.Errorf("%w: %s", ErrLockedRole, id)
	case cur.Origin != OriginCustom:
		return nil, fmt.Errorf("%w: %s", ErrSeedRoleLocked, id)
	case cur.Version != ifVersion:
		return nil, fmt.Errorf("%w: %s is at version %d, not %d", ErrVersionMismatch, id, cur.Version, ifVersion)
	}
	return cur, nil
}

// ---------------------------------------------------------------------------
// Escalation, assignment and dominance (§4.7 rules 1, 2 and 5).

// holds reports whether actor holds the role pattern p (§4.7 rule 1: "the
// actor must hold the pattern itself or core:admin"). A concrete scope is
// actor.Has(p). A wildcard needs actor.Has(p) or actor.Has(core:admin), and
// also every scope it expands to now, so the Sensitive rule (§4.4: no
// Sensitive scope for a non-interactive principal, core:admin included)
// applies to what the pattern grants. A malformed pattern is never held. The
// rule for Checker.Has of a pattern that authz.Compile keeps: true only when
// the principal holds that pattern or a wider one (seams.d/F4-01.md).
func holds(cat *Catalog, actor Checker, p Permission) bool {
	pat, err := ParsePattern(string(p))
	if err != nil {
		return false
	}
	if !pat.IsWildcard() {
		return actor.Has(pat)
	}
	if !actor.Has(pat) && !actor.Has(CoreAdmin) {
		return false
	}
	for _, c := range cat.Expand(pat) {
		if !actor.Has(c) {
			return false
		}
	}
	return true
}

// escalation is §4.7 rule 1 for a role write: the actor holds every
// permission in perms. A refusal is audited as a denial (async).
func (s *Service) escalation(ctx context.Context, cat *Catalog, actor Checker, role RoleName, perms []Permission) error {
	if actor == nil {
		return fmt.Errorf("%w: no actor", ErrEscalation)
	}
	for _, p := range perms {
		if !holds(cat, actor, p) {
			s.deps.Audit.Emit(ctx, audit.Event{
				Action: "core.permission.deny", Outcome: audit.Denied, Permission: string(p),
				Target: roleRef(role), Reason: "scope_escalation",
			})
			return fmt.Errorf("%w: %s on role %s", ErrEscalation, p, role)
		}
	}
	return nil
}

// CanAssign reports whether actor may give or take away roles (actor must hold
// every scope of every role named, or core:admin). Removal is checked like
// assignment (§4.7 rule 2).
//
// Each scope is checked with holds, so core:admin's reach is the checker's
// (§4.4): an admin passes every role except one holding a Sensitive scope
// when the admin's credential is not interactive. A role name the service
// does not know is refused (ErrEscalation wrapping ErrUnknownRole) unless the
// actor holds core:admin: assigning a name nobody defined would grant the
// user whatever role is created under it later.
func (s *Service) CanAssign(actor Checker, roles []RoleName) error {
	return s.covers(actor, roles, ErrEscalation, true)
}

// Dominates reports whether actor may act on a user whose roles are target
// (actor holds every scope target holds, or core:admin). auth.Service calls it
// for every action on another user's credentials, status or roles (§4.7 rule 5).
//
// Scopes are checked as CanAssign checks them. An unknown role of the target
// grants nothing (it is skipped at Compile), so it asks nothing of the actor.
func (s *Service) Dominates(actor Checker, target []RoleName) error {
	return s.covers(actor, target, ErrDominance, false)
}

func (s *Service) covers(actor Checker, roles []RoleName, refusal error, refuseUnknown bool) error {
	if actor == nil {
		return fmt.Errorf("%w: no actor", refusal)
	}
	st, err := s.frozen()
	if err != nil {
		return fmt.Errorf("%w: %w", refusal, err)
	}
	s.rolesMu.RLock()
	defer s.rolesMu.RUnlock()
	for _, name := range roles {
		r := s.roles[name]
		if r == nil {
			if refuseUnknown && !actor.Has(CoreAdmin) {
				return fmt.Errorf("%w: %w: %s", refusal, ErrUnknownRole, name)
			}
			continue
		}
		for _, p := range r.Permissions() {
			if !holds(st.cat, actor, p) {
				return fmt.Errorf("%w: role %s holds %s", refusal, name, p)
			}
		}
	}
	return nil
}

// checkLastAdmin is the last-admin rule (§4.6): a change that takes
// core:admin from some role must leave at least one active user holding a
// role that grants it, when one held it before. changes maps a role to its
// new pattern set (nil: deleted). The holders are read through b, so a
// concurrent demotion in auth makes the Commit fail and the write retry.
// Without Holders the rule cannot be shown to hold and the change is refused.
func (s *Service) checkLastAdmin(ctx context.Context, b *storage.Batch, changes map[RoleName][]Permission) error {
	s.rolesMu.RLock()
	var before, after []RoleName
	for name, r := range s.roles {
		if GrantsAdmin(r.Permissions()) {
			before = append(before, name)
		}
		perms, changed := changes[name]
		if !changed {
			perms = r.Permissions()
		}
		if GrantsAdmin(perms) {
			after = append(after, name)
		}
	}
	s.rolesMu.RUnlock()
	for name, perms := range changes {
		if _, stored := s.roleCached(name); !stored && GrantsAdmin(perms) {
			after = append(after, name)
		}
	}
	lost := false
	for _, name := range before {
		if !slices.Contains(after, name) {
			lost = true
			break
		}
	}
	if !lost {
		return nil
	}
	if s.deps.Holders == nil {
		return fmt.Errorf("%w: no role-holder source is wired, so the change cannot be shown to keep an active admin", ErrLastAdmin)
	}
	had, err := s.activeHolders(ctx, b, before)
	if err != nil {
		return err
	}
	keep, err := s.activeHolders(ctx, b, after)
	if err != nil {
		return err
	}
	if had > 0 && keep == 0 {
		return ErrLastAdmin
	}
	return nil
}

// roleCached reports the cached role name.
func (s *Service) roleCached(name RoleName) (*Role, bool) {
	s.rolesMu.RLock()
	defer s.rolesMu.RUnlock()
	r, ok := s.roles[name]
	return r, ok
}

// activeHolders counts the distinct active users holding any of roles.
func (s *Service) activeHolders(ctx context.Context, b *storage.Batch, roles []RoleName) (int, error) {
	if len(roles) == 0 {
		return 0, nil
	}
	slices.Sort(roles)
	hs, err := s.deps.Holders.HoldersOf(ctx, b, roles)
	if err != nil {
		return 0, fmt.Errorf("rbac: read the holders of %v: %w", roles, err)
	}
	seen := map[string]bool{}
	for _, h := range hs {
		if h.Active && h.UserID != "" {
			seen[h.UserID] = true
		}
	}
	return len(seen), nil
}

// ---------------------------------------------------------------------------
// The write path.

// roleChange is one role a committed write changed; role nil is a delete.
type roleChange struct {
	name RoleName
	role *Role
}

// write runs stage in a fresh batch and commits it, under writeMu. A Commit
// that fails on a precondition (storage.ErrConflict: another writer changed
// a role or a user record this write read) runs stage again, up to
// maxWriteAttempts, so the retry decides on the new state; the last conflict
// is returned wrapped in ErrVersionMismatch. After the commit the role cache
// takes the changes, the generation is bumped and Deps.OnChange called.
func (s *Service) write(ctx context.Context, stage func(b *storage.Batch) ([]*roleChange, error)) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for attempt := 1; ; attempt++ {
		changes, err := s.attempt(ctx, stage)
		if errors.Is(err, storage.ErrConflict) && attempt < maxWriteAttempts {
			continue
		}
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf("%w: %w", ErrVersionMismatch, err)
		}
		if err != nil {
			return err
		}
		s.apply(changes)
		return nil
	}
}

// attempt is one try of write: stage, then Commit.
func (s *Service) attempt(ctx context.Context, stage func(b *storage.Batch) ([]*roleChange, error)) (changes []*roleChange, err error) {
	b := s.deps.NewBatch()
	if b == nil {
		return nil, errors.New("rbac: Deps.NewBatch returned no batch")
	}
	defer func() {
		if cerr := b.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("rbac: close batch: %w", cerr)
		}
	}()
	changes, err = stage(b)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return nil, err
	}
	return changes, nil
}

// apply installs committed changes into the role cache, bumps the generation
// and reports it. Nothing to apply changes nothing.
func (s *Service) apply(changes []*roleChange) {
	if len(changes) == 0 {
		return
	}
	s.rolesMu.Lock()
	for _, c := range changes {
		if c.role == nil {
			delete(s.roles, c.name)
			continue
		}
		s.roles[c.name] = c.role.clone()
	}
	gen := s.gen.Add(1)
	s.rolesMu.Unlock()
	if s.deps.OnChange != nil {
		s.deps.OnChange(gen)
	}
}

// revoke appends r to the revocation journal into b (05-backup §5B.3.6). An
// Append failure is the one fail-open path by contract: the change still
// commits and a gap is staged instead, because a disk problem never blocks a
// security action. A failed Gap fails the change.
func (s *Service) revoke(ctx context.Context, b *storage.Batch, r journal.Revocation) error {
	r.At = s.now().UTC()
	r.Source = s.src
	if err := s.deps.Revocations.Append(ctx, b, r); err != nil {
		s.log.Warn("rbac: revocation journal append failed; recording a gap", "kind", string(r.Kind), "subject", r.Subject.ID, "error", err)
		if gerr := s.deps.Revocations.Gap(b, r.At); gerr != nil {
			return fmt.Errorf("rbac: journal %s: append failed (%v) and the gap could not be staged: %w", r.Kind, err, gerr)
		}
	}
	return nil
}

// record stages the audit record of a role change into b (sync, same batch).
func (s *Service) record(ctx context.Context, b *storage.Batch, action string, role RoleName, details map[string]any) error {
	err := s.deps.Audit.RecordIn(ctx, b, audit.Event{
		Action: action, Outcome: audit.Success, Target: roleRef(role), Details: details,
	})
	if err != nil {
		return fmt.Errorf("rbac: audit %s: %w", action, err)
	}
	return nil
}

// roleRef is the entity reference of a role.
func roleRef(name RoleName) entity.Ref {
	return entity.Ref{Module: "core", Kind: "role", ID: string(name)}
}

// permStrings returns ps as strings (audit details and journal lines).
func permStrings(ps []Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// union is a ∪ b, sorted and de-duplicated.
func union(a, b []Permission) []Permission {
	return normalize(append(slices.Clone(a), b...))
}

// minus is a \ b, sorted and de-duplicated.
func minus(a, b []Permission) []Permission {
	var out []Permission
	for _, p := range a {
		if !slices.Contains(b, p) {
			out = append(out, p)
		}
	}
	return normalize(out)
}

// ---------------------------------------------------------------------------
// Compile and the checker cache (§4.4, §9).

// Compile builds a principal's checker (cached by role-set + scopes +
// interactive + generation) through Deps.Compile. Unknown role IDs are
// skipped and counted.
//
// It fails closed without calling Deps.Compile, returning a checker that
// holds nothing, before Freeze (there is no catalog) and for key scopes that
// break §4.5 (an empty non-nil list, a wildcard or a malformed scope). A nil
// checker from Deps.Compile is replaced by the same deny-all checker and not
// cached.
func (s *Service) Compile(in CheckerInput) Checker {
	st := s.state.Load()
	if st == nil {
		return denyAll{}
	}
	if in.Scopes != nil && !scopesWellFormed(in.Scopes) {
		return denyAll{}
	}
	s.rolesMu.RLock()
	gen := s.gen.Load()
	key := cacheKey(in, gen)
	if c, ok := s.cache.get(key); ok {
		s.rolesMu.RUnlock()
		s.ctr.hits.Add(1)
		return c
	}
	var patterns []Permission
	unknown := 0
	for _, name := range in.Roles {
		r := s.roles[name]
		if r == nil {
			unknown++
			continue
		}
		patterns = append(patterns, r.Permissions()...)
	}
	s.rolesMu.RUnlock()
	s.ctr.misses.Add(1)
	if unknown > 0 {
		s.ctr.unknownRoles.Add(uint64(unknown))
	}
	ci := CompileInput{
		Roles: slices.Clone(in.Roles), Scopes: slices.Clone(in.Scopes), Interactive: in.Interactive,
		RolePatterns: normalize(patterns),
	}
	c := s.deps.Compile(ci, st.cat)
	if c == nil {
		return denyAll{}
	}
	s.cache.put(key, c)
	return c
}

// scopesWellFormed reports a key-scope list §4.5 allows in shape: non-empty,
// every entry a concrete scope (never a wildcard). Whether each is in the
// catalog is authz's to judge (an unknown scope is held by no one).
func scopesWellFormed(scopes []Permission) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, p := range scopes {
		if _, err := ParsePermission(string(p)); err != nil {
			return false
		}
	}
	return true
}

// CheckKeyScopes checks an API key's scope list (05-rbac §4.2, §4.5): at
// least one scope (ErrBadScope), no wildcard (ErrWildcardInKey), each a
// concrete scope (ErrBadScope) the catalog declares (ErrUnknownScope). A nil
// catalog refuses every list. Every problem is returned, joined. auth calls
// it when a key is minted (05-authn §4.4).
func CheckKeyScopes(cat *Catalog, scopes []Permission) error {
	if len(scopes) == 0 {
		return fmt.Errorf("%w: an API key holds at least one scope", ErrBadScope)
	}
	if cat == nil {
		return fmt.Errorf("%w: no catalog to check the key's scopes against", ErrUnknownScope)
	}
	var errs []error
	for _, p := range scopes {
		if pat, err := ParsePattern(string(p)); err == nil && pat.IsWildcard() {
			errs = append(errs, fmt.Errorf("%w: %s", ErrWildcardInKey, p))
			continue
		}
		if _, err := ParsePermission(string(p)); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, ok := cat.Lookup(p); !ok {
			errs = append(errs, fmt.Errorf("%w: %s", ErrUnknownScope, p))
		}
	}
	return errors.Join(errs...)
}

// Confined reports whether a set of patterns is confined (§4.4, §4.10): it
// expands to at least one concrete scope, every one of them Confining, and it
// does not grant core:admin (an admin is never confined). It is the rule the
// identity cutover applies to an imported key's effective set and to an
// imported role before it adds the ambient scopes (§6.3), and the rule
// authz.Compile's Confined() keeps.
func Confined(cat *Catalog, perms []Permission) bool {
	if cat == nil || GrantsAdmin(perms) {
		return false
	}
	n := 0
	for _, p := range perms {
		for _, c := range cat.Expand(p) {
			spec, _ := cat.Lookup(c)
			if !spec.Confining {
				return false
			}
			n++
		}
	}
	return n > 0
}

// WithAmbient is §6.3's ambient rule: perms plus ambient when perms is not
// Confined, perms unchanged when it is (a worker-confined key or role never
// gains the ambient scopes, whoever owns it; R10-2, SEC10-1). The result is
// sorted and de-duplicated. ABO's cutover passes core:self, core:metrics.read
// and books:reading as ambient; core holds no app scope.
//
// Confined can only be decided against a catalog that knows every pattern,
// so WithAmbient refuses (fail closed) rather than guess: a nil catalog, an
// empty perms (§6.3 gives no key or role an empty list), a malformed pattern
// (ErrBadScope) or one that matches no declared scope (ErrUnknownScope), and
// an ambient entry that is not a concrete declared scope. Without these, a
// worker key carrying one unknown or typoed pattern next to its Confining
// scopes would still read as confined, and a nil catalog would read every
// key as not confined and hand a worker key the ambient scopes.
func WithAmbient(cat *Catalog, perms []Permission, ambient ...Permission) ([]Permission, error) {
	if cat == nil {
		return nil, fmt.Errorf("rbac: ambient: %w: no catalog to decide confinement against", ErrUnknownScope)
	}
	if len(perms) == 0 {
		return nil, fmt.Errorf("rbac: ambient: %w: an empty permission set (no key or role has one)", ErrUnknownScope)
	}
	errs := []error{CheckRolePermissions(cat, perms)}
	for _, a := range ambient {
		if _, err := ParsePermission(string(a)); err != nil {
			errs = append(errs, fmt.Errorf("ambient scope: %w", err))
			continue
		}
		if _, ok := cat.Lookup(a); !ok {
			errs = append(errs, fmt.Errorf("%w: ambient scope %s", ErrUnknownScope, a))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("rbac: ambient: %w", err)
	}
	if Confined(cat, perms) {
		return normalize(perms), nil
	}
	return union(perms, ambient), nil
}

// cacheKey encodes (sorted role IDs, key-scoped flag, sorted scopes,
// interactive, generation) injectively: every part is length-prefixed, and a
// nil scope list (a session) is told apart from an empty one.
func cacheKey(in CheckerInput, gen uint64) string {
	var sb strings.Builder
	part := func(s string) {
		sb.WriteString(strconv.Itoa(len(s)))
		sb.WriteByte(':')
		sb.WriteString(s)
	}
	roles := slices.Clone(in.Roles)
	slices.Sort(roles)
	roles = slices.Compact(roles)
	sb.WriteString("r" + strconv.Itoa(len(roles)) + ";")
	for _, r := range roles {
		part(string(r))
	}
	if in.Scopes == nil {
		sb.WriteString("s-;")
	} else {
		scopes := normalize(in.Scopes)
		sb.WriteString("s" + strconv.Itoa(len(scopes)) + ";")
		for _, p := range scopes {
			part(string(p))
		}
	}
	if in.Interactive {
		sb.WriteString("i1;")
	} else {
		sb.WriteString("i0;")
	}
	sb.WriteString("g" + strconv.FormatUint(gen, 10))
	return sb.String()
}

// denyAll is the checker of a principal that holds nothing. It is confined,
// so no rule that admits by default can admit it either.
type denyAll struct{}

func (denyAll) Has(Permission) bool        { return false }
func (denyAll) Satisfies(Requirement) bool { return false }
func (denyAll) Confined() bool             { return true }
func (denyAll) Concrete() []Permission     { return nil }

// cacheShards is the number of independently locked LRU shards.
const cacheShards = 16

// checkerCache is the sharded LRU of compiled checkers (§9). A generation
// bump makes old entries unreachable; they age out.
type checkerCache struct {
	shards [cacheShards]cacheShard
}

type cacheShard struct {
	mu    sync.Mutex
	cap   int
	items map[string]*list.Element
	order *list.List // front: most recently used
}

type cacheEntry struct {
	key string
	c   Checker
}

func newCheckerCache(size int) *checkerCache {
	per := max((size+cacheShards-1)/cacheShards, 1)
	c := &checkerCache{}
	for i := range c.shards {
		c.shards[i] = cacheShard{cap: per, items: map[string]*list.Element{}, order: list.New()}
	}
	return c
}

// shard picks key's shard by its 32-bit FNV-1a hash.
func (c *checkerCache) shard(key string) *cacheShard {
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return &c.shards[h%cacheShards]
}

func (c *checkerCache) get(key string) (Checker, bool) {
	sh := c.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	el, ok := sh.items[key]
	if !ok {
		return nil, false
	}
	sh.order.MoveToFront(el)
	return el.Value.(*cacheEntry).c, true
}

func (c *checkerCache) put(key string, ch Checker) {
	sh := c.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if el, ok := sh.items[key]; ok {
		el.Value.(*cacheEntry).c = ch
		sh.order.MoveToFront(el)
		return
	}
	sh.items[key] = sh.order.PushFront(&cacheEntry{key: key, c: ch})
	for sh.order.Len() > sh.cap {
		last := sh.order.Back()
		sh.order.Remove(last)
		delete(sh.items, last.Value.(*cacheEntry).key)
	}
}

// ---------------------------------------------------------------------------
// Context plumbing and in-handler checks (§3.4, §4.10).

type checkerKey struct{}

type serviceKey struct{}

// WithChecker binds c into ctx; auth's middleware calls it (and
// audit.WithActor) when it binds a principal. A nil c binds no checker.
func WithChecker(ctx context.Context, c Checker) context.Context {
	return context.WithValue(ctx, checkerKey{}, c)
}

// CheckerFrom returns the checker bound into ctx.
func CheckerFrom(ctx context.Context) (Checker, bool) {
	c, ok := ctx.Value(checkerKey{}).(Checker)
	return c, ok && c != nil
}

// Bind is WithChecker plus this service as the recorder of Require's
// denials: a denial is then audited core.permission.deny (async, coalesced)
// and counted in rbac_denials_total. Coined by F4-01: the package-level
// Require has no receiver to reach the audit log through. auth's middleware
// calls Bind in place of WithChecker.
func (s *Service) Bind(ctx context.Context, c Checker) context.Context {
	return context.WithValue(WithChecker(ctx, c), serviceKey{}, s)
}

// In-handler checks (dynamic permissions). No checker in ctx → false / ErrDenied.
// Require emits a denial audit event (actor from audit.ActorFrom(ctx)) and
// returns ErrDenied wrapped with the permission.
//
// Can holds only when the bound checker both Has(s) and Satisfies(Need(s)),
// so a confined principal is judged by the requirement rule (§4.10) and a
// checker that is lax on one method is still bound by the other. A malformed
// or wildcard s is never held.
func Can(ctx context.Context, s Permission) bool {
	r := Need(s)
	if r.IsZero() {
		return false
	}
	c, ok := CheckerFrom(ctx)
	if !ok {
		return false
	}
	return c.Has(s) && c.Satisfies(r)
}

// Require is Can that returns ErrDenied wrapped with s, and records the
// denial through the service Bind bound (an unbound context still denies).
func Require(ctx context.Context, s Permission) error {
	if Can(ctx, s) {
		return nil
	}
	denied(ctx, s)
	return fmt.Errorf("%w: %s", ErrDenied, s)
}

// RequireAll requires every one of s. No scope, or a malformed one, denies:
// an empty requirement is never satisfied.
func RequireAll(ctx context.Context, s ...Permission) error {
	if AllOf(s...).IsZero() {
		denied(ctx, "")
		return fmt.Errorf("%w: no valid permission named", ErrDenied)
	}
	for _, p := range s {
		if err := Require(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// denied records a denial when a service is bound.
func denied(ctx context.Context, p Permission) {
	s, ok := ctx.Value(serviceKey{}).(*Service)
	if !ok || s == nil {
		return
	}
	s.ctr.denial(s.Catalog(), p)
	s.deps.Audit.Emit(ctx, audit.Event{
		Action: "core.permission.deny", Outcome: audit.Denied, Permission: string(p), Reason: "permission_denied",
	})
}

// ---------------------------------------------------------------------------
// Metrics (§11).

// denialKey is one series of rbac_denials_total.
type denialKey struct{ owner, scope string }

// counters are the service's own tallies; the instruments read them when the
// registry is collected, so the check path records only an increment.
type counters struct {
	hits, misses, unknownRoles atomic.Uint64

	mu      sync.Mutex
	denials map[denialKey]uint64
}

// denial counts one denial of p. Labels stay catalog-bounded: a scope the
// catalog does not hold is counted as owner "unknown", scope "unknown".
func (c *counters) denial(cat *Catalog, p Permission) {
	k := denialKey{owner: "unknown", scope: "unknown"}
	if cat != nil {
		if _, ok := cat.Lookup(p); ok {
			k = denialKey{owner: p.Namespace(), scope: string(p)}
		}
	}
	c.mu.Lock()
	if c.denials == nil {
		c.denials = map[denialKey]uint64{}
	}
	c.denials[k]++
	c.mu.Unlock()
}

func (c *counters) snapshotDenials() map[denialKey]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[denialKey]uint64, len(c.denials))
	maps.Copy(out, c.denials)
	return out
}

// instrument registers the service's instruments on sc, the core "rbac"
// scope (labels owner, scope, result, origin):
//
//	<ns>_rbac_denials_total{owner,scope}
//	<ns>_rbac_checker_cache_total{result}
//	<ns>_rbac_unknown_roles_total
//	<ns>_rbac_generation
//	<ns>_rbac_roles{origin}
//	<ns>_rbac_pending_seed_roles
func (s *Service) instrument(sc *metrics.Scope) error {
	m, prefix := sc.Meter(), sc.Prefix()
	denials, err := m.Int64ObservableCounter(prefix+".denials",
		metric.WithDescription("Permission denials recorded by rbac.Require, by owning namespace and scope (05-rbac §11)."))
	if err != nil {
		return fmt.Errorf("rbac: denials counter: %w", err)
	}
	cache, err := m.Int64ObservableCounter(prefix+".checker_cache",
		metric.WithDescription("Compiled-checker cache lookups, by result (hit, miss)."))
	if err != nil {
		return fmt.Errorf("rbac: cache counter: %w", err)
	}
	unknown, err := m.Int64ObservableCounter(prefix+".unknown_roles",
		metric.WithDescription("Role IDs on a principal that name no stored role; they grant nothing."))
	if err != nil {
		return fmt.Errorf("rbac: unknown roles counter: %w", err)
	}
	gen, err := m.Int64ObservableGauge(prefix+".generation",
		metric.WithDescription("The role generation: bumped by Freeze and every committed role change."))
	if err != nil {
		return fmt.Errorf("rbac: generation gauge: %w", err)
	}
	roles, err := m.Int64ObservableGauge(prefix+".roles",
		metric.WithDescription("Stored roles, by origin (seed, custom)."))
	if err != nil {
		return fmt.Errorf("rbac: roles gauge: %w", err)
	}
	pending, err := m.Int64ObservableGauge(prefix+".pending_seed_roles",
		metric.WithDescription("Seed roles with restricted seed additions waiting for acceptance."))
	if err != nil {
		return fmt.Errorf("rbac: pending seed gauge: %w", err)
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for k, n := range s.ctr.snapshotDenials() {
			o.ObserveInt64(denials, int64(n), metric.WithAttributes(attribute.String("owner", k.owner), attribute.String("scope", k.scope)))
		}
		o.ObserveInt64(cache, int64(s.ctr.hits.Load()), metric.WithAttributes(attribute.String("result", "hit")))
		o.ObserveInt64(cache, int64(s.ctr.misses.Load()), metric.WithAttributes(attribute.String("result", "miss")))
		o.ObserveInt64(unknown, int64(s.ctr.unknownRoles.Load()))
		o.ObserveInt64(gen, int64(s.gen.Load()))
		seedN, customN, pendingN := s.roleCounts()
		o.ObserveInt64(roles, seedN, metric.WithAttributes(attribute.String("origin", "seed")))
		o.ObserveInt64(roles, customN, metric.WithAttributes(attribute.String("origin", "custom")))
		o.ObserveInt64(pending, pendingN)
		return nil
	}, denials, cache, unknown, gen, roles, pending)
	if err != nil {
		return fmt.Errorf("rbac: metrics callback: %w", err)
	}
	return nil
}

// roleCounts counts the cached roles by origin and the seed roles with
// pending additions.
func (s *Service) roleCounts() (seed, custom, pending int64) {
	s.rolesMu.RLock()
	defer s.rolesMu.RUnlock()
	for _, r := range s.roles {
		switch r.Origin {
		case OriginSeed:
			seed++
			if len(r.PendingSeed) > 0 {
				pending++
			}
		case OriginCustom:
			custom++
		}
	}
	return seed, custom, pending
}
