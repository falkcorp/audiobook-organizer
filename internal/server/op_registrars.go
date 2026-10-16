// file: internal/server/op_registrars.go
// version: 1.1.0
// guid: e1f2a3b4-c5d6-7e8f-9a0b-1c2d3e4f5a6b
// last-edited: 2026-10-16

// op_registrars holds the adapted v2 registration path: each op file calls
// addOpRegistrar in an init() function, and registerAll (server.go) runs the
// slice at startup after the maintenance plugin and before the native
// bundles of opscatalog.All. New v2 ops never require touching server.go.
//
// This path stays until the port waves move every v2 op into a native
// bundle (05-PR15 removes the adapter). Native ops v3 defs do not use it:
// a package exports Ops(Deps) ops.Bundle and is listed in opscatalog.All.

package server

import opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

// opRegistrar is a function that registers one or more OperationDefs with the
// UOS-02 registry.
type opRegistrar func(s *Server, reg *opsregistry.Registry) error

var opRegistrars []opRegistrar

// addOpRegistrar appends a registration function to the global slice.
// Call from init() in any server package file to wire up a new op without
// modifying server.go.
func addOpRegistrar(fn opRegistrar) {
	opRegistrars = append(opRegistrars, fn)
}
