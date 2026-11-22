// file: core/internal/authhttp/errors_test.go
// version: 0.1.0
// guid: 8f3c1a52-6d47-4e09-b1a8-2c5e7d90f413
// last-edited: 2026-11-22

package authhttp

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// The refusals F4-08 B10t added to core's invites reach the client as plain
// answers, not as a 500 and not in an OIDC sentence: an invite for an address
// another account holds verified is a 409 email_in_use, and an accept that the
// same holder refuses is the one link_invalid answer every bad link gets, so an
// invite's holder learns nothing about who holds an address.
func TestInviteEmailRefusals_AreMappedToPlainAnswers(t *testing.T) {
	e := newEnv(t)
	a := e.api()
	ctx := context.Background()

	create := fmt.Errorf("%w: another account holds that address verified", auth.ErrEmailInUse)
	wantErr(t, a.fail(ctx, specInviteNew(), nil, "", create), http.StatusConflict, "email_in_use")

	accept := fmt.Errorf("%w: %w", auth.ErrInviteInvalid, create)
	wantErr(t, a.linkFail(ctx, specInviteNew(), accept), http.StatusUnauthorized, "link_invalid")
}
