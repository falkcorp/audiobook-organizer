// file: core/rbac/catalog.go
// version: 1.0.0
// guid: 239479e1-c77a-4506-ae57-ad37fc75cc21
// last-edited: 2026-11-10

package rbac

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
)

// Errors NewCatalog returns, joined with errors.Join so one boot attempt
// reports every problem. ErrForeignNamespace and ErrImpliesCycle are members
// of 05-rbac §3.4's error block; the rest of the block lands with the service
// (F4-01), which must not redeclare these.
var (
	// ErrForeignNamespace is a spec whose Implies names a scope outside its own
	// namespace (05-rbac §4.3, §4.9).
	ErrForeignNamespace = errors.New("rbac: scope outside declarer namespace")
	// ErrImpliesCycle is an implication closure that is not acyclic.
	ErrImpliesCycle = errors.New("rbac: implication cycle")
	// ErrUnknownScope is an Implies entry no spec in the catalog declares.
	ErrUnknownScope = errors.New("rbac: unknown scope")
	// ErrDuplicatePermission is two specs with the same Name (05-rbac §4.9,
	// "Permission uniqueness across catalog").
	ErrDuplicatePermission = errors.New("rbac: permission declared twice")
	// ErrConfiningImplication is a Confining scope that implies a scope or is
	// implied by one (05-rbac §4.9).
	ErrConfiningImplication = errors.New("rbac: confining scope takes part in implication")
)

// PermissionSpec is declared in a module manifest (06-app-sdk Manifest.Permissions)
// or by core. This file owns the type; 06 and 08 use these names (COR-16).
type PermissionSpec struct {
	Name          Permission // concrete, in the declarer's namespace
	Title         string     // UI label (Users/Roles page)
	Description   string
	Class         PermissionClass // ClassAction (zero value) or ClassView; drives the task-start rule (§4.10)
	DefaultGrants []RoleName      // seed roles that receive it by default: core "editor"/"viewer" or the declarer's own roles; merged by §4.8, restricted by §4.9
	Implies       []Permission    // same namespace only (core may imply its own); closure computed, cycles rejected
	Sensitive     bool            // never in a key-scope preset; a check of it fails for a non-interactive principal, core:admin included (§4.4)
	AuthRequired  bool            // never held by the auth-disabled principal (§4.11, 05-authn §4.11)
	Confining     bool            // a principal holding only Confining scopes is confined to requirements naming them (§4.10)
	Warn          bool            // UI-only: show a warning when granting it (the facade's arr.request, 08)
	Deprecated    Permission      // non-empty: the replacement; still valid, flagged in UI
}

// clone returns s with its slices copied, so a caller's mutation never reaches
// a catalog and a catalog never hands out its own backing arrays.
func (s PermissionSpec) clone() PermissionSpec {
	s.DefaultGrants = slices.Clone(s.DefaultGrants)
	s.Implies = slices.Clone(s.Implies)
	return s
}

// PermissionClass is a scope's class.
type PermissionClass uint8

const (
	ClassAction PermissionClass = iota // changes state or starts work
	ClassView                          // read-only
)

// RoleSeed is a module's own seed role (in addition to DefaultGrants into core roles).
type RoleSeed struct {
	Name        RoleName // "<ns>.<name>"; an un-namespaced ID only if listed in the module's Manifest.ImportedRoleIDs (books: "fp-worker"; 06 §6.4.2, COR5-5)
	Title       string
	Description string
	Permissions []Permission // patterns allowed (roles may hold wildcards), own namespace only
}

// Declarer is what the kernel uses to feed a module's manifest into the catalog.
type Declarer interface {
	Permissions(specs ...PermissionSpec) error
	Roles(seeds ...RoleSeed) error
}

// Catalog is the set of declared scopes. It is immutable once NewCatalog
// returns it: every accessor is safe for concurrent use and hands out copies.
type Catalog struct {
	specs map[Permission]PermissionSpec
	order []Permission // every name, sorted: iteration order is deterministic
}

// NewCatalog validates specs and returns the catalog holding them. Every
// problem found is returned, joined with errors.Join, so errors.Is matches any
// of them:
//
//   - each Name parses as a concrete Permission (ErrBadScope);
//   - no Name is declared twice (ErrDuplicatePermission);
//   - each Implies entry is a concrete scope (ErrBadScope) in the spec's own
//     namespace (ErrForeignNamespace) that some spec declares (ErrUnknownScope);
//   - the implication closure is acyclic (ErrImpliesCycle);
//   - a Confining scope implies nothing and is implied by nothing
//     (ErrConfiningImplication).
//
// DefaultGrants, seed merge and module declarations are checked and applied by
// the service that wraps NewCatalog (F4-01), not here.
func NewCatalog(specs ...PermissionSpec) (*Catalog, error) {
	c := &Catalog{specs: make(map[Permission]PermissionSpec, len(specs))}
	var errs []error
	for _, s := range specs {
		name, err := ParsePermission(string(s.Name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := c.specs[name]; dup {
			errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicatePermission, name))
			continue
		}
		c.specs[name] = s.clone()
		c.order = append(c.order, name)
	}
	slices.Sort(c.order)

	graphOK := len(errs) == 0
	for _, name := range c.order {
		s := c.specs[name]
		if s.Confining && len(s.Implies) > 0 {
			errs = append(errs, fmt.Errorf("%w: %s implies %v", ErrConfiningImplication, name, s.Implies))
			graphOK = false
		}
		for _, imp := range s.Implies {
			target, err := ParsePermission(string(imp))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s implies: %w", name, err))
				graphOK = false
				continue
			}
			if target.Namespace() != name.Namespace() {
				errs = append(errs, fmt.Errorf("%w: %s implies %s", ErrForeignNamespace, name, target))
				graphOK = false
				continue
			}
			ts, ok := c.specs[target]
			if !ok {
				errs = append(errs, fmt.Errorf("%w: %s implies %s", ErrUnknownScope, name, target))
				graphOK = false
				continue
			}
			if ts.Confining {
				errs = append(errs, fmt.Errorf("%w: %s is implied by %s", ErrConfiningImplication, target, name))
				graphOK = false
			}
		}
	}
	// A cycle is only meaningful over a graph whose edges all resolved.
	if graphOK {
		if cycle := c.findCycle(); cycle != nil {
			errs = append(errs, fmt.Errorf("%w: %s", ErrImpliesCycle, cyclePath(cycle)))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// findCycle returns one implication cycle (first scope repeated at the end),
// or nil. It visits scopes in name order, so the result is deterministic.
func (c *Catalog) findCycle() []Permission {
	const (
		unseen = iota
		onPath
		done
	)
	state := make(map[Permission]int, len(c.order))
	var path []Permission
	var visit func(p Permission) []Permission
	visit = func(p Permission) []Permission {
		state[p] = onPath
		path = append(path, p)
		for _, next := range c.specs[p].Implies {
			switch state[next] {
			case onPath:
				i := slices.Index(path, next)
				return append(slices.Clone(path[i:]), next)
			case unseen:
				if cycle := visit(next); cycle != nil {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		state[p] = done
		return nil
	}
	for _, p := range c.order {
		if state[p] == unseen {
			if cycle := visit(p); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

// cyclePath renders a cycle as "a -> b -> a".
func cyclePath(ps []Permission) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return strings.Join(out, " -> ")
}

// Lookup returns the spec declared for the concrete scope s. A wildcard is
// never found: Expand takes patterns.
func (c *Catalog) Lookup(s Permission) (PermissionSpec, bool) {
	spec, ok := c.specs[s]
	return spec.clone(), ok
}

// All yields every declared spec, ordered by name.
func (c *Catalog) All() iter.Seq[PermissionSpec] {
	return c.filter(func(Permission) bool { return true })
}

// Namespace yields every spec declared in namespace ns, ordered by name.
func (c *Catalog) Namespace(ns string) iter.Seq[PermissionSpec] {
	return c.filter(func(p Permission) bool { return p.Namespace() == ns })
}

func (c *Catalog) filter(keep func(Permission) bool) iter.Seq[PermissionSpec] {
	return func(yield func(PermissionSpec) bool) {
		for _, name := range c.order {
			if !keep(name) {
				continue
			}
			if !yield(c.specs[name].clone()) {
				return
			}
		}
	}
}

// Expand turns a pattern into the concrete scopes the catalog currently holds,
// ordered by name (05-rbac §4.2): "<ns>:*" is every scope of the namespace,
// "<ns>:<action>.*" every sub of the action and the bare action, and a
// concrete scope is itself when declared. A string that is not a valid
// pattern, or that matches nothing, expands to nil.
func (c *Catalog) Expand(p Permission) []Permission {
	pat, err := ParsePattern(string(p))
	if err != nil {
		return nil
	}
	var match func(Permission) bool
	switch {
	case !pat.IsWildcard():
		if _, ok := c.specs[pat]; ok {
			return []Permission{pat}
		}
		return nil
	case strings.HasSuffix(string(pat), ":*"):
		match = func(s Permission) bool { return s.Namespace() == pat.Namespace() }
	default: // "<ns>:<action>.*"
		match = func(s Permission) bool {
			return s.Namespace() == pat.Namespace() && s.Action() == pat.Action()
		}
	}
	var out []Permission
	for _, name := range c.order {
		if match(name) {
			out = append(out, name)
		}
	}
	return out
}

// CoreScopes returns every core:* permission 05-rbac names, with its class and
// flags (§6.1 "New permissions with no atom", the atom table's core scopes,
// §4.8's editor/viewer seeds, 05-library §5L.3), as a fresh slice on every
// call (R5). The kernel declares all of them whichever core subsystems are
// wired (05-rbac §5 item 3, IMPL-4), so a seed role, a stored grant or a
// manifest requirement never names a scope the catalog lacks. It leaves out
// the two scopes §6.1's notes drop (the event stream and task start are gated
// elsewhere).
//
// core:admin implies every scope by rule (§4.3), not by an Implies list, and
// the locked admin role sits outside the seed merge (§4.8), so it has no
// DefaultGrants. No core scope is Confining.
func CoreScopes() []PermissionSpec {
	seeds := func() []RoleName { return []RoleName{RoleEditor, RoleViewer} }
	return []PermissionSpec{
		{
			Name: "core:admin", Title: "Administrator",
			Description:  "Every permission; not held by the auth-disabled principal",
			AuthRequired: true,
		},
		{
			Name: "core:self", Title: "Own account",
			Description:   "View and change one's own profile, sessions and API keys",
			DefaultGrants: seeds(),
		},
		{
			Name: "core:roles.view", Title: "View roles",
			Description: "List roles and their permissions",
			Class:       ClassView,
		},
		{
			Name: "core:roles.manage", Title: "Manage roles",
			Description: "Create, edit and delete custom roles",
			Sensitive:   true,
		},
		{
			Name: "core:users.view", Title: "View users",
			Description: "List users and read their profiles",
			Class:       ClassView,
		},
		{
			Name: "core:users.manage", Title: "Manage users",
			Description: "Create, edit, disable and delete users and assign their roles",
			Implies:     []Permission{"core:users.view"},
		},
		{
			Name: "core:audit.view", Title: "View audit log",
			Description:  "Read the audit trail, which holds user names, addresses and key IDs",
			Class:        ClassView,
			AuthRequired: true,
		},
		{
			Name: "core:system.status", Title: "System status",
			Description:   "Readiness and maintenance-banner tokens only",
			Class:         ClassView,
			DefaultGrants: seeds(),
		},
		{
			Name: "core:system.view", Title: "View system detail",
			Description: "Status detail, system information and cache introspection",
			Class:       ClassView,
		},
		{
			Name: "core:system.manage", Title: "Manage the system",
			Description:  "Resets, logs and storage administration",
			AuthRequired: true,
		},
		{
			Name: "core:backup.restore", Title: "Restore backups",
			Description:  "Restore the instance from a backup",
			Sensitive:    true,
			AuthRequired: true,
		},
		{
			Name: "core:integrations.read", Title: "View integrations",
			Description: "List and read integration instances, never their secret values",
			Class:       ClassView,
		},
		{
			Name: "core:integrations.manage", Title: "Manage integrations",
			Description: "Create, edit and delete integration instances",
			Implies:     []Permission{"core:integrations.read"},
		},
		{
			Name: "core:tasks.view", Title: "View tasks",
			Description:   "List tasks and read their runs",
			Class:         ClassView,
			DefaultGrants: seeds(),
		},
		{
			Name: "core:tasks.manage", Title: "Manage tasks",
			Description: "Edit task schedules and cancel runs",
		},
		{
			Name: "core:metrics.read", Title: "Read metrics",
			Description:   "Read the metrics endpoint",
			Class:         ClassView,
			DefaultGrants: seeds(),
		},
		{
			Name: "core:library.manage", Title: "Manage library locations",
			Description: "Add, edit and remove library roots and import paths",
		},
		{
			Name: "core:library.browse", Title: "Browse the filesystem",
			Description: "Browse server directories when choosing a library or import path",
		},
		{
			Name: "core:settings.manage", Title: "Manage settings",
			Description: "Change core settings",
		},
	}
}
