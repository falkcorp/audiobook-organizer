// file: internal/database/pebble_store_invite_delivery.go
// version: 1.3.0
// guid: 6f1d8b2c-3e47-4a95-b6c0-d82e5f9a7b14
// last-edited: 2026-10-19

package database

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ErrInviteEmailTaken is returned when an invite's email already belongs to a
// user. Accepting it would repoint the email index at the new account.
var ErrInviteEmailTaken = errors.New("a user with that email already exists")

// ErrInviteNotPending is returned by the invite-delivery writes for an invite
// that is missing, used or expired.
var ErrInviteNotPending = errors.New("invite is not pending")

// Guest-policy sync outcomes recorded on an accepted invite.
const (
	InviteGuestPolicyAdded          = "added"
	InviteGuestPolicyAlreadyPresent = "already_present"
	InviteGuestPolicyFailed         = "failed"
	InviteGuestPolicyNotConfigured  = "not_configured"
	// InviteGuestPolicySkipped: no SSO grant (email not delivered, or the
	// creator is not an admin), so Cloudflare was not touched.
	InviteGuestPolicySkipped = "skipped"
	// InviteGuestPolicyPending is written just before Cloudflare is called.
	// One that stays pending means the server stopped mid-update: check the
	// policy by hand.
	InviteGuestPolicyPending = "pending"
	// InviteGuestPolicyRevoked: the access was revoked before the sync
	// reached Cloudflare, so Cloudflare was not touched.
	InviteGuestPolicyRevoked = "revoked"
	// InviteGuestPolicyRemoved: the sync added the email, found the access
	// had been revoked while it was doing so, and removed the email again.
	InviteGuestPolicyRemoved = "removed"
)

// Invite.EmailBinding and Invite.SSOGrant values.
const (
	InviteEmailBound           = "bound"
	InviteEmailNotEmailed      = "not_emailed"
	InviteEmailCreatorNotAdmin = "creator_not_admin" // emailed, but by someone without the admin role: not bound
	InviteSSOGranted           = "granted"
	InviteSSOCreatorNotAdmin   = "creator_not_admin"
)

// inviteAdminRole is the role an invite's creator must hold for the accept to
// grant SSO sign-in: the same role middleware.RequireAdmin requires.
const inviteAdminRole = "admin"

// inviteEmailDelivered reports whether the invitation was emailed to exactly
// the invite's address, so that holding the token proves control of it.
func inviteEmailDelivered(inv *Invite) bool {
	return inv.Email != "" && inv.EmailSentAt != nil &&
		strings.EqualFold(strings.TrimSpace(inv.EmailSentTo), strings.TrimSpace(inv.Email))
}

// inviteCreatorMayGrant reports whether an invite's creator may give the
// invitee SSO sign-in: the creator must still exist, be active and hold the
// admin role at accept time (a demotion after inviting counts).
func inviteCreatorMayGrant(creator *User) bool {
	return creator != nil && creator.ID != "" && creator.Status == "active" &&
		slices.Contains(creator.Roles, inviteAdminRole)
}

// inviteEmailGrantPrefix keys the SSO grant ConsumeInvite writes for an
// accepted invite's email: inviteallow:<lowercase email> -> InviteEmailGrant.
const inviteEmailGrantPrefix = "inviteallow:"

// InviteEmailGrant is an SSO sign-in grant for one email, written by accept.
type InviteEmailGrant struct {
	UserID      string    `json:"user_id"`
	InviteToken string    `json:"invite_token,omitempty"`
	GrantedAt   time.Time `json:"granted_at"`
	// CFAdded is true when the accept-time sync itself added the email to the
	// Cloudflare guest policy. Only such an email is ever removed from the
	// policy again: one that was already there was put there by hand.
	CFAdded bool `json:"cf_added,omitempty"`
}

// InviteDeliveryStore is the capability behind invitation email and the
// accept-time guest-policy report. Not part of Store; resolve it with
// AsCapability. Every write takes the same lock as ConsumeInvite, so none of
// them can rewrite an invite an accept has just consumed.
type InviteDeliveryStore interface {
	// MarkInviteEmailSent records a successful send to address `to` on a
	// pending invite and returns it. ErrInviteNotPending when missing, used or
	// expired.
	MarkInviteEmailSent(token, to string, at time.Time) (*Invite, error)
	// RecordInviteGuestPolicy stores the guest-policy sync outcome on an
	// invite (normally a just-accepted one).
	RecordInviteGuestPolicy(token, status, detail string, at time.Time) error
	// ListAcceptedInvitesSince returns used invites accepted at or after
	// since, newest first.
	ListAcceptedInvitesSince(since time.Time) ([]Invite, error)
}

// InviteEmailGrantStore reads and ends the SSO grants accepted invites write.
// Not part of Store.
type InviteEmailGrantStore interface {
	// GetInviteEmailGrant returns the grant for email, or nil.
	GetInviteEmailGrant(email string) (*InviteEmailGrant, error)
	// SetInviteEmailGrantCFAdded marks the grant for email as having been
	// added to the Cloudflare guest policy by the sync, if it still names
	// userID, and reports whether it did. false means the grant was revoked
	// (deleted) in the meantime, and the caller, which just added the email,
	// must remove it again: the revoke saw CFAdded=false and left it alone.
	// It takes the same lock as DeleteInviteEmailGrant, so exactly one of the
	// two sees the add.
	SetInviteEmailGrantCFAdded(email, userID string) (stillGranted bool, err error)
	// DeleteInviteEmailGrant removes the grant for email when it names userID
	// and returns what was removed (nil when there was nothing to remove).
	DeleteInviteEmailGrant(email, userID string) (*InviteEmailGrant, error)
}

var (
	_ InviteDeliveryStore   = (*PebbleStore)(nil)
	_ InviteEmailGrantStore = (*PebbleStore)(nil)
)

// MarkInviteEmailSent implements InviteDeliveryStore.
func (p *PebbleStore) MarkInviteEmailSent(token, to string, at time.Time) (*Invite, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	inv, err := p.GetInvite(token)
	if err != nil {
		return nil, err
	}
	if inv == nil || inv.UsedAt != nil || at.After(inv.ExpiresAt) {
		return nil, ErrInviteNotPending
	}
	inv.EmailSentAt = &at
	inv.EmailSentTo = strings.ToLower(strings.TrimSpace(to))
	data, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}
	if err := p.db.Set([]byte("invite:"+token), data, pebble.Sync); err != nil {
		return nil, err
	}
	return inv, nil
}

// RecordInviteGuestPolicy implements InviteDeliveryStore.
func (p *PebbleStore) RecordInviteGuestPolicy(token, status, detail string, at time.Time) error {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	inv, err := p.GetInvite(token)
	if err != nil {
		return err
	}
	if inv == nil {
		return ErrInviteNotPending
	}
	if len(detail) > 300 {
		detail = detail[:300]
	}
	inv.GuestPolicyStatus, inv.GuestPolicyDetail, inv.GuestPolicyAt = status, detail, &at
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("invite:"+token), data, pebble.Sync)
}

// ListAcceptedInvitesSince implements InviteDeliveryStore.
func (p *PebbleStore) ListAcceptedInvitesSince(since time.Time) ([]Invite, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("invite:"),
		UpperBound: []byte("invite:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []Invite
	for iter.First(); iter.Valid(); iter.Next() {
		var i Invite
		if err := json.Unmarshal(iter.Value(), &i); err != nil {
			continue
		}
		if i.UsedAt == nil || i.UsedAt.Before(since) {
			continue
		}
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UsedAt.After(*out[b].UsedAt) })
	return out, nil
}

// GetInviteEmailGrant implements InviteEmailGrantStore.
func (p *PebbleStore) GetInviteEmailGrant(email string) (*InviteEmailGrant, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, nil
	}
	v, closer, err := p.db.Get([]byte(inviteEmailGrantPrefix + email))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var g InviteEmailGrant
	if err := json.Unmarshal(v, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// SetInviteEmailGrantCFAdded implements InviteEmailGrantStore.
func (p *PebbleStore) SetInviteEmailGrantCFAdded(email, userID string) (bool, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	g, err := p.GetInviteEmailGrant(email)
	if err != nil {
		return false, err
	}
	if g == nil || g.UserID != userID {
		return false, nil
	}
	g.CFAdded = true
	data, err := json.Marshal(g)
	if err != nil {
		return false, err
	}
	if err := p.db.Set([]byte(inviteEmailGrantPrefix+strings.ToLower(strings.TrimSpace(email))), data, pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteInviteEmailGrant implements InviteEmailGrantStore.
func (p *PebbleStore) DeleteInviteEmailGrant(email, userID string) (*InviteEmailGrant, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	g, err := p.GetInviteEmailGrant(email)
	if err != nil || g == nil || g.UserID != userID {
		return nil, err
	}
	if err := p.db.Delete([]byte(inviteEmailGrantPrefix+strings.ToLower(strings.TrimSpace(email))), pebble.Sync); err != nil {
		return nil, err
	}
	return g, nil
}
