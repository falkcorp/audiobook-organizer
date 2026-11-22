// file: internal/books/guest_policy_wire.go
// version: 0.1.0
// guid: 8d3f6a12-0b59-4c7e-a4d8-1e9b27c5f063
// last-edited: 2026-11-22

package books

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/module"
)

// registerGuestPolicyLifecycle hands core the one auth.Lifecycle books has
// (F4-08 B10r), so the Cloudflare Access guest policy follows core's account
// events (internal/server/guest_policy_identity.go). Register calls it once, on an
// identity boot, after the server exists: the callbacks run on the goroutine of
// whatever request commits an account change, and use the server's background
// context and its wait group.
//
// A nil handle is a refusal and never a skip: without the hook a disabled,
// deleted or merged guest would keep their Cloudflare entry with nothing to say so.
// (newLegacyIdentity refuses the same boot earlier, with the rest of the identity
// handles; this is the second place so the call cannot be made through a nil.)
func registerGuestPolicyLifecycle(d module.Deps, srv *server.Server) error {
	if d.AuthLifecycle == nil {
		return errGuestPolicyLifecycle
	}
	if err := d.AuthLifecycle(srv.GuestPolicyLifecycle()); err != nil {
		return fmt.Errorf("books: registering the guest-policy lifecycle: %w", err)
	}
	return nil
}

// errGuestPolicyLifecycle is Register's refusal when core handed no lifecycle
// handle under the identity flip.
var errGuestPolicyLifecycle = errors.New("books: the identity flip is on but core handed no lifecycle handle (Deps.AuthLifecycle): " +
	"a guest's Cloudflare entry would outlive their account")
