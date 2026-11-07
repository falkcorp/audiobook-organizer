// file: core/examples/hello/hello.go
// version: 1.0.0
// guid: 14139145-0b08-41cd-aef9-31413e517ae1
// last-edited: 2026-11-07

// Package hello is core's example module (06 §6.12): the smallest module that
// boots through app.New. At release R1's skeleton it has its manifest and one
// route, GET /api/hello/v1/greeting. Its settings section (config), keyspace
// and seed migration (storage, R1-20), the hello.count task (tasks) and its
// permissions (rbac's catalog) land with their packages; the route is
// rbac.Public, listed in Manifest.PublicRoutes, until core authentication
// exists (06 §6.12.2, R2's identity flip).
package hello

import "github.com/falkcorp/mediacore/module"

// ID is hello's module ID.
const ID module.ID = "hello"

// Module is the hello module. It holds nothing at R1.
type Module struct{}

// New returns the hello module.
func New() *Module { return &Module{} }

// Manifest is hello's static declaration (06 §6.12.1, the R1 fields).
func (m *Module) Manifest() module.Manifest {
	return module.Manifest{
		ID:             ID,
		Version:        "1.0.0",
		DisplayName:    "Hello",
		MinCoreVersion: "0.1.0", // compared with version.Version (MOD-12)
		DefaultEnabled: true,
		UI: module.UIDescriptor{
			Pages:          []string{"greeting"},
			Nav:            []string{"hello"},
			SettingsPanels: []string{"hello"},
		},
		// Until core authentication lands every native route must be
		// rbac.Public, and the public set is a reviewed manifest list (COR3-3).
		PublicRoutes: []string{"getGreeting"},
	}
}

// Register records hello's route.
func (m *Module) Register(r *module.Registrar) error {
	registerGreeting(r.HTTP())
	return r.Err()
}
