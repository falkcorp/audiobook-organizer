// file: core/internal/rbachttp/types.go
// version: 1.0.0
// guid: 110752ad-0955-49d7-8da8-c9784c09a89f
// last-edited: 2026-11-10

package rbachttp

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The wire types. rbac.Role carries no JSON tags and holds seed bookkeeping
// (SeedDigest, SeedConfined, SeedPermissions) that is not an API output, so
// the routes answer with these views: snake_case names (05-http-api §3.4) and
// nothing the Roles page does not need. Their names carry the "Rbac" prefix
// because an OpenAPI component is named <module>.<TypeName> and every core
// capability shares the "core" owner.

// Bounds on what a request may carry. 05-rbac names none; they keep a role's
// text to what a list row can show and a permission list to the size of the
// catalog (a role names each scope once). The body cap (maxBody) is the outer
// bound.
const (
	maxTitleRunes       = 128
	maxDescriptionRunes = 1024
	maxPermissions      = 512
	maxPermissionLen    = 128
	maxNameLen          = 128
)

// RbacRole is one role as the routes answer (05-rbac §3.3). Permissions is the
// role's effective set (patterns allowed); PendingSeed is what a release added
// that waits for POST /roles/{name}/accept-seed (§4.8), always empty on a
// custom role. Version is what a write names in If-Match: the typed handlers
// cannot set an ETag header, so the version is in the body.
type RbacRole struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Origin      string    `json:"origin"` // "seed" or "custom"
	Locked      bool      `json:"locked"` // admin: no edit, no delete
	Permissions []string  `json:"permissions"`
	PendingSeed []string  `json:"pending_seed"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RbacRoleCreate is the body of POST /roles. Name must be custom.<slug>.
type RbacRoleCreate struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// RbacRolePatch is the body of PATCH /roles/{name}: a field the body omits
// keeps its stored value, and permissions replaces the whole set. A rename is
// not a field: a role's name is its identity.
type RbacRolePatch struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// view renders r. A role of an origin this binary does not know reads as
// "unknown" and holds nothing (Role.Permissions).
func view(r *rbac.Role) RbacRole {
	origin := "unknown"
	switch r.Origin {
	case rbac.OriginSeed:
		origin = "seed"
	case rbac.OriginCustom:
		origin = "custom"
	}
	return RbacRole{
		Name: string(r.Name), Title: r.Title, Description: r.Description, Origin: origin, Locked: r.Locked,
		Permissions: strs(r.Permissions()), PendingSeed: strs(r.PendingSeed), Version: r.Version,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// strs is ps as strings, never nil (the wire has [] for none).
func strs(ps []rbac.Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// perms is ss as permissions.
func perms(ss []string) []rbac.Permission {
	if len(ss) == 0 {
		return nil
	}
	out := make([]rbac.Permission, len(ss))
	for i, s := range ss {
		out[i] = rbac.Permission(s)
	}
	return out
}

// ---- requests -------------------------------------------------------------

type listReq struct {
	httpapi.PageParams
}

type roleReq struct {
	Name string `path:"name"`
}

type createReq struct {
	Body RbacRoleCreate `body:"json"`
}

// Validate checks the shape of a create body. Whether the name is a valid
// custom.<slug> and whether each permission is in the catalog are the
// service's to say (ErrRoleName, ErrBadScope, ErrUnknownScope); this checks
// only what the service does not: a name and a title are present, and the
// sizes are bounded.
func (r *createReq) Validate() error {
	var fs []httpapi.FieldError
	if strings.TrimSpace(r.Body.Name) == "" {
		fs = append(fs, httpapi.Field("/body/name", "required", "a role needs a name"))
	} else if len(r.Body.Name) > maxNameLen {
		fs = append(fs, httpapi.Field("/body/name", "too_long", "must be at most "+strconv.Itoa(maxNameLen)+" bytes"))
	}
	if strings.TrimSpace(r.Body.Title) == "" {
		fs = append(fs, httpapi.Field("/body/title", "required", "a role needs a title"))
	}
	fs = append(fs, textFields(&r.Body.Title, &r.Body.Description)...)
	fs = append(fs, permFields(r.Body.Permissions)...)
	return invalid(fs)
}

type patchReq struct {
	Name    string        `path:"name"`
	IfMatch string        `header:"If-Match"`
	Body    RbacRolePatch `body:"json"`
}

// Validate checks the shape of a patch body: at least one field, the same
// bounds as a create, and a title that is not blanked.
func (r *patchReq) Validate() error {
	b := r.Body
	if b.Title == nil && b.Description == nil && b.Permissions == nil {
		return invalid([]httpapi.FieldError{httpapi.Field("/body", "required", "name at least one of title, description, permissions")})
	}
	var fs []httpapi.FieldError
	if b.Title != nil && strings.TrimSpace(*b.Title) == "" {
		fs = append(fs, httpapi.Field("/body/title", "required", "a role needs a title"))
	}
	fs = append(fs, textFields(b.Title, b.Description)...)
	if b.Permissions != nil {
		fs = append(fs, permFields(*b.Permissions)...)
	}
	return invalid(fs)
}

// versionReq is a write that names the role and the version it saw: DELETE
// and POST accept-seed.
type versionReq struct {
	Name    string `path:"name"`
	IfMatch string `header:"If-Match"`
}

// textFields bounds a title and a description (nil: not present).
func textFields(title, description *string) []httpapi.FieldError {
	var fs []httpapi.FieldError
	if title != nil && utf8.RuneCountInString(*title) > maxTitleRunes {
		fs = append(fs, httpapi.Field("/body/title", "too_long", "must be at most "+strconv.Itoa(maxTitleRunes)+" characters"))
	}
	if description != nil && utf8.RuneCountInString(*description) > maxDescriptionRunes {
		fs = append(fs, httpapi.Field("/body/description", "too_long", "must be at most "+strconv.Itoa(maxDescriptionRunes)+" characters"))
	}
	return fs
}

// permFields bounds a permission list. The grammar and the catalog are the
// service's (CheckRolePermissions); an entry too long to be either is refused
// here so it is never echoed or stored.
func permFields(ps []string) []httpapi.FieldError {
	if len(ps) > maxPermissions {
		return []httpapi.FieldError{httpapi.Field("/body/permissions", "too_many", "must name at most "+strconv.Itoa(maxPermissions)+" permissions")}
	}
	for i, p := range ps {
		if p == "" || len(p) > maxPermissionLen {
			return []httpapi.FieldError{httpapi.Field("/body/permissions/"+strconv.Itoa(i), "invalid_scope", "is not a permission")}
		}
	}
	return nil
}

// invalid is fs as one 422, or nil for none.
func invalid(fs []httpapi.FieldError) error {
	if len(fs) == 0 {
		return nil
	}
	return httpapi.Invalid(fs...)
}
