// file: internal/server/handlers/invite_identity_accepted_internal_test.go
// version: 0.1.0
// guid: 5e0a7c13-9d48-4b62-a1f5-c2d87e3b6094
// last-edited: 2026-11-22

package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// failingAccepts is an accept store whose list cannot be read.
type failingAccepts struct{ database.InviteAcceptStore }

func (failingAccepts) ListInviteAcceptsSince(time.Time) ([]database.InviteAccept, error) {
	return nil, errors.New("pebble: the family cannot be read")
}

// failingOutcomes is a guest-policy store whose outcome cannot be read.
type failingOutcomes struct{ database.GuestPolicyStore }

func (failingOutcomes) GetGuestPolicyOutcome(string) (*database.GuestPolicyOutcome, error) {
	return nil, errors.New("pebble: the outcome cannot be read")
}

// oneAccept is an accept store holding a single row.
type oneAccept struct{ database.InviteAcceptStore }

func (oneAccept) ListInviteAcceptsSince(time.Time) ([]database.InviteAccept, error) {
	return []database.InviteAccept{{InviteID: "inv-1", UserID: "u-1", Username: "alfred.pennyworth", AcceptedAt: time.Now(),
		GuestPolicyStatus: database.InviteGuestPolicyFailed}}, nil
}

// HEAD logged a failed read of the accepted list and answered [], so the pending
// half of the body still reached the admin; under core identity it is the same.
func TestAcceptedInvitesUnderIdentity_AReadErrorAnswersAnEmptyListNotNull(t *testing.T) {
	h := &UserHandler{accepts: failingAccepts{}}
	got := h.acceptedInvitesUnderIdentity()
	if got == nil || len(got) != 0 {
		t.Fatalf("a failed read answered %#v, want a non-nil empty list", got)
	}
	if got := (&UserHandler{}).acceptedInvitesUnderIdentity(); got == nil || len(got) != 0 {
		t.Fatalf("a store without the capability answered %#v, want a non-nil empty list", got)
	}
}

// An outcome that cannot be read costs its row the sync members the outcome would
// have replaced and nothing else: the accept is still listed, with the row's own.
func TestAcceptedInvitesUnderIdentity_AnUnreadableOutcomeStillListsTheAccept(t *testing.T) {
	h := &UserHandler{accepts: oneAccept{}, outcomes: failingOutcomes{}}
	got := h.acceptedInvitesUnderIdentity()
	if len(got) != 1 || got[0].Username != "alfred.pennyworth" || got[0].GuestPolicyStatus != database.InviteGuestPolicyFailed {
		t.Fatalf("got %#v", got)
	}
}

// The carry's window and the list's are one number: M3 carries exactly the accepts
// the list would still show on the day of the flip.
func TestAcceptedInviteWindow_MatchesM3sCarry(t *testing.T) {
	if m3.AcceptedInviteWindow != acceptedInviteWindow {
		t.Fatalf("m3 carries %v of accepts and the list shows %v", m3.AcceptedInviteWindow, acceptedInviteWindow)
	}
}
