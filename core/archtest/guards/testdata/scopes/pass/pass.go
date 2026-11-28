// file: core/archtest/guards/testdata/scopes/pass/pass.go
// version: 1.0.0
// guid: 9d2c6f18-3e47-4b05-a7c1-5e8b0d3f6a92
// last-edited: 2026-11-28

// Package pass holds what CoreScopesOnly must leave alone.
package pass

import (
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

const coreNS = "core"

var core = rbac.AllOf("core:users.view", "core:admin")
var coreTyped rbac.Permission = "core:system.manage"
var viaConcat = rbac.Need(coreNS + ":roles.view")
var forbidden = httpapi.Forbidden("core:audit.view")
var star = rbac.Permission("*")
var noColon = rbac.Permission("self")
var parsed, _ = rbac.ParsePermission("core:users.view")

// A string that merely looks like a scope is not a Permission.
var title = "gotham:title"
var specTitle = rbac.PermissionSpec{Name: "core:x.y", Title: "gotham:cave"}

// A map keyed by string names no Permission key.
var byName = map[string]rbac.Permission{"gotham:key": "core:users.view"}

// A scope read at run time is a documented gap, see the gap fixture.
func dynamic(name string) rbac.Permission { return rbac.Permission(name) }
