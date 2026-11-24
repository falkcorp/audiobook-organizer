// file: internal/server/gate_trace.go
// version: 0.1.0
// guid: 1be9e139-d0c4-49da-b97d-0f1e0bbf8081
// last-edited: 2026-11-22

package server

import "github.com/gin-gonic/gin"

// gateKind names one kind of route-level gate a gin chain can carry, as the
// gate constructors below the hook describe themselves.
type gateKind string

const (
	// gateAuth is requireAuth: the request must be authenticated.
	gateAuth gateKind = "auth"
	// gatePerm is s.perm(atom): the caller must hold the atom.
	gatePerm gateKind = "perm"
	// gateAdmin is adminRole / adminGate: the caller must be an admin, of the
	// admin route kind (read-only introspection or always-authenticated).
	gateAdmin gateKind = "admin"
	// gateCred is credGuardMiddleware: the credential-change guard; a
	// conditional one guards only some requests.
	gateCred gateKind = "cred"
	// gateOwner is ownerGateWhenOwnerSet.
	gateOwner gateKind = "owner"
)

// gateInfo is what a gate constructor says about the gate it builds.
type gateInfo struct {
	Kind gateKind
	// Atom is the HEAD permission atom of a gatePerm gate.
	Atom string
	// Admin is the admin route kind of a gateAdmin gate.
	Admin adminRouteKind
	// Conditional is true for a gateCred gate that guards only some requests
	// (credRouteWhen).
	Conditional bool
}

// gateTraceForTest, when set, is called by every route-level gate constructor
// with a description of the gate it would build, and the chain gets what it
// returns in place of the real gate. A test that wants every gate a route's
// chain carries, with the gate's own parameters (which atom, which admin
// kind), installs a recorder here before the engine is built
// (legacy_manifest_gin_gates_test.go); the constructors run once, at wiring.
// Nil in production, where it costs one comparison per constructed gate.
var gateTraceForTest func(gateInfo) gin.HandlerFunc
