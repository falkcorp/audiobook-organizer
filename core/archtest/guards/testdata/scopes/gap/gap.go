// file: core/archtest/guards/testdata/scopes/gap/gap.go
// version: 1.0.0
// guid: c8a41e07-5b92-4f36-8d1a-27e0b9c4f5d3
// last-edited: 2026-11-28

// Package gap builds a foreign scope at run time, which go/types has no
// constant value for: CoreScopesOnly cannot see it, and says so in its doc
// comment. The scan finds nothing here, pinned so the doc cannot drift.
package gap

import (
	"fmt"

	"github.com/falkcorp/mediacore/rbac"
)

var space = "gotham"

var viaSprintf = rbac.Need(rbac.Permission(fmt.Sprintf("%s:siren", "gotham")))
var viaVariable = rbac.Need(rbac.Permission(space + ":watchtower"))
var table = map[string]string{"alarm": "gotham:alarm"}
var viaTable = rbac.Need(rbac.Permission(table["alarm"]))
