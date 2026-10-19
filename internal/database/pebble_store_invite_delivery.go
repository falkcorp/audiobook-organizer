// file: internal/database/pebble_store_invite_delivery.go
// version: 1.0.0
// guid: 6f1d8b2c-3e47-4a95-b6c0-d82e5f9a7b14
// last-edited: 2026-10-19

package database

import (
	"encoding/json"
	"errors"
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
)

// inviteEmailGrantPrefix keys the SSO grant ConsumeInvite writes for an
// accepted invite's email: inviteallow:<lowercase email> -> inviteEmailGrant.
const inviteEmailGrantPrefix = "inviteallow:"

type inviteEmailGrant struct {
	UserID    string    `json:"user_id"`
	GrantedAt time.Time `json:"granted_at"`
}

// InviteDeliveryStore is the capability behind invitation email and the
// accept-time guest-policy report. Not part of Store; resolve it with
// AsCapability. Every write takes the same lock as ConsumeInvite, so none of
// them can rewrite an invite an accept has just consumed.
type InviteDeliveryStore interface {
	// MarkInviteEmailSent records a successful send on a pending invite and
	// returns it. ErrInviteNotPending when missing, used or expired.
	MarkInviteEmailSent(token string, at time.Time) (*Invite, error)
	// RecordInviteGuestPolicy stores the guest-policy sync outcome on an
	// invite (normally a just-accepted one).
	RecordInviteGuestPolicy(token, status, detail string, at time.Time) error
	// ListAcceptedInvitesSince returns used invites accepted at or after
	// since, newest first.
	ListAcceptedInvitesSince(since time.Time) ([]Invite, error)
}

// InviteEmailGrantStore answers whether an email was granted SSO sign-in by
// an accepted invite, and for which user. Not part of Store.
type InviteEmailGrantStore interface {
	// InviteEmailGrantUserID returns the user id the grant for email names,
	// or "" when there is none.
	InviteEmailGrantUserID(email string) (string, error)
}

var (
	_ InviteDeliveryStore   = (*PebbleStore)(nil)
	_ InviteEmailGrantStore = (*PebbleStore)(nil)
)

// MarkInviteEmailSent implements InviteDeliveryStore.
func (p *PebbleStore) MarkInviteEmailSent(token string, at time.Time) (*Invite, error) {
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

// InviteEmailGrantUserID implements InviteEmailGrantStore.
func (p *PebbleStore) InviteEmailGrantUserID(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", nil
	}
	v, closer, err := p.db.Get([]byte(inviteEmailGrantPrefix + email))
	if err == pebble.ErrNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closer.Close()
	var g inviteEmailGrant
	if err := json.Unmarshal(v, &g); err != nil {
		return "", err
	}
	return g.UserID, nil
}
