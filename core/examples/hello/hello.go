// file: core/examples/hello/hello.go
// version: 1.0.2
// guid: 14139145-0b08-41cd-aef9-31413e517ae1
// last-edited: 2026-11-11

// Package hello is core's example module (06 §6.12): the smallest module that
// boots through app.New. At release R1 it has its manifest, its keyspace
// m/hello/ with the seed-names migration (storage.go) and one route, GET
// /api/hello/v1/greeting, which reports how often a name was recorded. Its
// settings section (config) and its permissions (rbac's catalog) land with
// their packages; hello.count (tasks) is registered since F2-16; the route is
// rbac.Public, listed in Manifest.PublicRoutes, until core authentication
// exists (06 §6.12.2, R2's identity flip).
package hello

import (
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

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
		// The kinds hello registers, readable while it is disabled so its
		// stored runs are held, not unknown (05-tasks 5T.3.11).
		TaskLedger: []tasks.Kind{"hello.count"},
		Storage:    storageSpec(),
	}
}

// Register records hello's route over its service, the resource kind its task
// reads and hello.count (06 §6.12.3).
func (m *Module) Register(r *module.Registrar) error {
	d := r.Deps()
	svc := &service{names: d.Storage.Keyspace.Sub("name")} // m/hello/name/<name> -> decimal count
	registerGreeting(r.HTTP(), svc)
	r.Resources("name")
	r.Tasks(countTask(svc))
	return r.Err()
}
