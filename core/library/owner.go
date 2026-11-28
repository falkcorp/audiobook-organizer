// file: core/library/owner.go
// version: 0.1.0
// guid: ba98612f-569c-475e-b1b7-60715c96ebf0
// last-edited: 2026-11-22

package library

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/storage"
)

// Owner is library's owner in the storage claim ledger: the owner of the
// c/library/ keyspace that holds the transfer journal and the quarantine
// records (Options.Keyspace). It is not CoreOwner. CoreOwner is the caller
// identity core's own code acts as in a Scope ("core"); Owner is a storage
// owner ("core/library"), the one the kernel appends StorageSpec() under when
// the F7 flip opens the store.
const Owner storage.Owner = "core/library"

// StorageSpec is the c/library/ spec the kernel registers for Owner: a core
// keyspace and nothing else (no claims, no serialized prefixes, no
// migrations). The journal and the quarantine records commit through the
// keyspace's own batches, so they need no Expect discipline of a Serialized
// prefix.
func StorageSpec() storage.Spec { return storage.Spec{Keyspace: true} }

// RoleRequirement is one library-root role a module needs
// (module.Manifest.Library, 05-library 5L.4 item 2). The kernel compares the
// list with the registered roots and reports an unmet requirement as a setup
// task in the UI; it never fails boot on one, because a fresh install has no
// roots yet. The spec names the type and gives no fields; these are the ones
// F7-09 B3-b coined, mirroring the capability requirement of the same manifest
// (06 Required, Why).
type RoleRequirement struct {
	// Role is the kind of root the module needs.
	Role Role
	// Required says the module cannot do its job without such a root; false is
	// a root that only improves what the module offers. Either way an unmet
	// requirement is a setup task, never a boot failure.
	Required bool
	// Why is the text the setup task shows for this requirement. It is
	// admin-facing text a module author wrote, never data read from a root.
	Why string
}

// ErrBadRoleRequirement is wrapped by every RoleRequirement.Validate failure.
var ErrBadRoleRequirement = errors.New("library: invalid role requirement")

// Validate reports why r cannot be a manifest entry: an unknown role, or no
// text for the setup task to show.
func (r RoleRequirement) Validate() error {
	switch {
	case r.Role < RoleLibrary || r.Role > RoleExternal:
		return fmt.Errorf("%w: role %d is not library, import, download or external", ErrBadRoleRequirement, r.Role)
	case strings.TrimSpace(r.Why) == "":
		return fmt.Errorf("%w: %s needs a Why, the text the setup task shows", ErrBadRoleRequirement, r.Role)
	}
	return nil
}
