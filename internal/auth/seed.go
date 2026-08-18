// file: internal/auth/seed.go
// version: 1.2.0
// guid: 2e8f4a1d-7c3b-4f60-b9d5-1c6e0f2b9a57
// last-edited: 2026-08-18
//
// Seed roles — idempotent upsert of the three canonical roles
// (admin, editor, viewer) with their permission sets. Called at
// server startup per spec 3.7.
//
// Seed roles are defined in code, not config. Adding a new
// permission here automatically grants it to admin on the next
// startup — the seed logic recomputes permission sets every boot so
// existing admins can't be out-of-date with the codebase.

package auth

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seedStore is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 2 methods. It was an
// inline anonymous interface of database.* embeds — 13 methods — repeated
// at 1 parameter, invisible to any line-oriented tool.
type seedStore interface {
	GetUserByUsername(username string) (*database.User, error)
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*database.User, error)
}

const (
	SeedRoleAdmin  = "admin"
	SeedRoleEditor = "editor"
	SeedRoleViewer = "viewer"
)

// adminPermissions returns every permission constant. Admin always
// has all of them — computed fresh each call so that adding a new
// permission to the source file is automatically picked up.
func adminPermissions() []Permission {
	return All()
}

// editorPermissions returns the editor role's permissions: everything
// except the three admin-level management permissions.
func editorPermissions() []Permission {
	return []Permission{
		PermLibraryView,
		PermLibraryEditMetadata,
		PermLibraryDelete,
		PermLibraryOrganize,
		PermScanTrigger,
		PermPlaylistsCreate,
		PermRequestsCreate,
		PermRequestsApprove,
	}
}

// viewerPermissions returns the viewer role's permissions: read-only
// library access plus the ability to file requests.
func viewerPermissions() []Permission {
	return []Permission{
		PermLibraryView,
		PermRequestsCreate,
	}
}

// SeedRoles ensures the three canonical roles (admin, editor, viewer)
// exist in the store with their current permission sets. Safe to call
// repeatedly — existing role permissions are updated on every call
// so a deploy that adds a new permission constant automatically
// reaches admin's effective set.
//
// Returns the number of roles created + updated.
func SeedRoles(store database.RoleStore) (created, updated int, err error) {
	if store == nil {
		return 0, 0, fmt.Errorf("seed roles: store is nil")
	}
	specs := []struct {
		id, name, description string
		perms                 []Permission
	}{
		{SeedRoleAdmin, "admin", "Full access — manage users, integrations, and library", adminPermissions()},
		{SeedRoleEditor, "editor", "Library editor — can view, edit metadata, organize, scan", editorPermissions()},
		{SeedRoleViewer, "viewer", "Read-only library access", viewerPermissions()},
	}
	for _, s := range specs {
		existing, lookupErr := store.GetRoleByID(s.id)
		if lookupErr != nil {
			return created, updated, fmt.Errorf("lookup role %s: %w", s.id, lookupErr)
		}
		if existing == nil {
			if _, cerr := store.CreateRole(&database.Role{
				ID:          s.id,
				Name:        s.name,
				Description: s.description,
				Permissions: s.perms,
				IsSeed:      true,
			}); cerr != nil {
				return created, updated, fmt.Errorf("create role %s: %w", s.id, cerr)
			}
			created++
			continue
		}
		if samePermSet(existing.Permissions, s.perms) && existing.Description == s.description && existing.IsSeed {
			continue
		}
		existing.Permissions = s.perms
		existing.Description = s.description
		existing.IsSeed = true
		if uerr := store.UpdateRole(existing); uerr != nil {
			return created, updated, fmt.Errorf("update role %s: %w", s.id, uerr)
		}
		updated++
	}
	return created, updated, nil
}

// SystemUserID is the ID of the _system pseudo-user. Used to
// attribute operations triggered by background tasks (scanners,
// maintenance, etc.) rather than a real human user.
const SystemUserID = "_system"

// SeedSystemUser creates the _system pseudo-user if absent.
// It has no password and cannot log in — it exists solely as an
// audit attribution target.
func SeedSystemUser(store seedStore) error {
	if store == nil {
		return nil
	}
	existing, err := store.GetUserByUsername(SystemUserID)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	_, err = store.CreateUser(SystemUserID, "_system@local", "", "", nil, "system")
	return err
}

// samePermSet reports whether two permission slices contain the same
// set of values, independent of order.
func samePermSet(a, b []Permission) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[Permission]struct{}, len(a))
	for _, p := range a {
		seen[p] = struct{}{}
	}
	for _, p := range b {
		if _, ok := seen[p]; !ok {
			return false
		}
	}
	return true
}
