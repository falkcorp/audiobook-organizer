// file: internal/server/handlers/invite_identity_accepted_internal_test.go
// version: 0.1.0
// guid: 5e0a7c13-9d48-4b62-a1f5-c2d87e3b6094
// last-edited: 2026-11-22

package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// failingOutcomes is a guest-policy store whose list cannot be read.
type failingOutcomes struct{ database.GuestPolicyStore }

func (failingOutcomes) ListGuestPolicyOutcomesSince(time.Time) ([]database.GuestPolicyOutcome, error) {
	return nil, errors.New("pebble: the family cannot be read")
}

// HEAD logged a failed read of the accepted list and answered [], so the pending
// half of the body still reached the admin; under core identity it is the same.
func TestAcceptedInvitesUnderIdentity_AReadErrorAnswersAnEmptyListNotNull(t *testing.T) {
	h := &UserHandler{outcomes: failingOutcomes{}}
	got := h.acceptedInvitesUnderIdentity(context.Background(), &legacyauth.Identity{})
	if got == nil || len(got) != 0 {
		t.Fatalf("a failed read answered %#v, want a non-nil empty list", got)
	}
	if got := (&UserHandler{}).acceptedInvitesUnderIdentity(context.Background(), &legacyauth.Identity{}); got == nil || len(got) != 0 {
		t.Fatalf("a store without the capability answered %#v, want a non-nil empty list", got)
	}
}
