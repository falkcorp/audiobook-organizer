// file: internal/books/store/pebble_store_guest_policy.go
// version: 0.1.0
// guid: 7b793cce-d537-4778-9ade-51e32f43c18b
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// Books' record of the Cloudflare Access guest-policy entries it added for
// guests under core identity (F4-08 B10r). Core owns the accounts and the
// invites; it knows nothing about Cloudflare, so what the sync did there is
// books' own data, in two families of its own:
//
//	guestpolicy:<normalized e-mail>    -> guestPolicyRow     one per address
//	guestpolicyout:<core invite id>    -> GuestPolicyOutcome one per accepted invite
//
// Neither is the retired inviteallow: family. That one is an SSO sign-in grant
// the identity cutover (M3) consumes row by row and imports into core's
// allow-list; a row of the new shape written there would be imported as a
// grant. These rows grant nothing: they say only "this sync added this
// address for this account", so a later disable, delete, merge or e-mail
// change knows it may take the address out again. The sync never removes an
// address someone else put there (CFAdded is false for it).
const (
	guestPolicyPrefix        = "guestpolicy:"
	guestPolicyOutcomePrefix = "guestpolicyout:"
)

// guestPolicyRow is the stored bookkeeping of one address. It holds no token
// and no secret.
type guestPolicyRow struct {
	UserID    string    `json:"user_id"`
	InviteID  string    `json:"invite_id,omitempty"`
	GrantedAt time.Time `json:"granted_at"`
	// CFAdded is true when the sync itself added the address to the guest
	// policy: only such an address is ever removed from it again.
	CFAdded bool `json:"cf_added,omitempty"`
}

// GuestPolicyGrant is what PutGuestPolicyGrant stores.
type GuestPolicyGrant struct {
	Email     string
	UserID    string
	InviteID  string
	GrantedAt time.Time
}

// GuestPolicyOutcome is what the guest-policy sync reported for one accepted
// invite: HEAD's Invite.GuestPolicy* fields, kept per core invite id because the
// invite itself is core's.
type GuestPolicyOutcome struct {
	InviteID string    `json:"invite_id"`
	UserID   string    `json:"user_id"`
	Status   string    `json:"status"`
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
}

// GuestPolicyStore is the capability behind the guest-policy bookkeeping under
// core identity. Not part of Store; resolve it with AsCapability.
type GuestPolicyStore interface {
	// GuestPolicyGrants is the bookkeeping as an InviteEmailGrantStore, so the
	// sync and the revoke run unchanged over it. Its Set and Delete take the
	// same lock as PutGuestPolicyGrant, so exactly one side of a racing add and
	// revoke sees the add (see Server.startGuestPolicySync).
	GuestPolicyGrants() InviteEmailGrantStore
	// PutGuestPolicyGrant stores the bookkeeping of g.Email for g.UserID, with
	// CFAdded false, replacing any row for the address.
	PutGuestPolicyGrant(g GuestPolicyGrant) error
	// RecordGuestPolicyOutcome stores the outcome of the sync for an invite,
	// replacing the last one.
	RecordGuestPolicyOutcome(inviteID, userID, status, detail string, at time.Time) error
	// GetGuestPolicyOutcome returns the outcome recorded for an invite, or nil.
	GetGuestPolicyOutcome(inviteID string) (*GuestPolicyOutcome, error)
	// ListGuestPolicyOutcomesSince returns every outcome recorded at or after
	// since, newest first (ties by invite id, descending, so the order is the
	// same on every read). It reads and never writes. A row that does not decode
	// is skipped, as HEAD's ListAcceptedInvitesSince skipped an invite row it
	// could not read.
	ListGuestPolicyOutcomesSince(since time.Time) ([]GuestPolicyOutcome, error)
}

var _ GuestPolicyStore = (*PebbleStore)(nil)

// GuestPolicyGrants implements GuestPolicyStore.
func (p *PebbleStore) GuestPolicyGrants() InviteEmailGrantStore { return guestPolicyRows{p} }

// PutGuestPolicyGrant implements GuestPolicyStore.
func (p *PebbleStore) PutGuestPolicyGrant(g GuestPolicyGrant) error {
	email := coreauth.NormalizeEmail(g.Email)
	if email == "" || g.UserID == "" {
		return errors.New("guest policy grant needs an address and a user id")
	}
	data, err := json.Marshal(guestPolicyRow{UserID: g.UserID, InviteID: g.InviteID, GrantedAt: g.GrantedAt})
	if err != nil {
		return err
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	return p.db.Set([]byte(guestPolicyPrefix+email), data, pebble.Sync)
}

// RecordGuestPolicyOutcome implements GuestPolicyStore.
func (p *PebbleStore) RecordGuestPolicyOutcome(inviteID, userID, status, detail string, at time.Time) error {
	if inviteID == "" {
		return errors.New("guest policy outcome needs an invite id")
	}
	if len(detail) > 300 {
		detail = detail[:300]
	}
	data, err := json.Marshal(GuestPolicyOutcome{InviteID: inviteID, UserID: userID, Status: status, Detail: detail, At: at})
	if err != nil {
		return err
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	return p.db.Set([]byte(guestPolicyOutcomePrefix+inviteID), data, pebble.Sync)
}

// GetGuestPolicyOutcome implements GuestPolicyStore.
func (p *PebbleStore) GetGuestPolicyOutcome(inviteID string) (*GuestPolicyOutcome, error) {
	if inviteID == "" {
		return nil, nil
	}
	v, closer, err := p.db.Get([]byte(guestPolicyOutcomePrefix + inviteID))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var o GuestPolicyOutcome
	if err := json.Unmarshal(v, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListGuestPolicyOutcomesSince implements GuestPolicyStore.
func (p *PebbleStore) ListGuestPolicyOutcomesSince(since time.Time) ([]GuestPolicyOutcome, error) {
	// ';' is the byte after ':', so the bounds span exactly the family.
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(guestPolicyOutcomePrefix),
		UpperBound: []byte("guestpolicyout;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []GuestPolicyOutcome{}
	for iter.First(); iter.Valid(); iter.Next() {
		var o GuestPolicyOutcome
		if err := json.Unmarshal(iter.Value(), &o); err != nil {
			continue
		}
		if o.At.Before(since) {
			continue
		}
		out = append(out, o)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].At.Equal(out[b].At) {
			return out[a].At.After(out[b].At)
		}
		return out[a].InviteID > out[b].InviteID
	})
	return out, nil
}

// guestPolicyRows is the InviteEmailGrantStore over the guestpolicy: family.
type guestPolicyRows struct{ p *PebbleStore }

var _ InviteEmailGrantStore = guestPolicyRows{}

func (r guestPolicyRows) get(email string) (*guestPolicyRow, error) {
	v, closer, err := r.p.db.Get([]byte(guestPolicyPrefix + email))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var row guestPolicyRow
	if err := json.Unmarshal(v, &row); err != nil {
		return nil, err
	}
	return &row, nil
}

func asGrant(row *guestPolicyRow) *InviteEmailGrant {
	return &InviteEmailGrant{UserID: row.UserID, GrantedAt: row.GrantedAt, CFAdded: row.CFAdded}
}

// GetInviteEmailGrant implements InviteEmailGrantStore.
func (r guestPolicyRows) GetInviteEmailGrant(email string) (*InviteEmailGrant, error) {
	email = coreauth.NormalizeEmail(email)
	if email == "" {
		return nil, nil
	}
	row, err := r.get(email)
	if err != nil || row == nil {
		return nil, err
	}
	return asGrant(row), nil
}

// SetInviteEmailGrantCFAdded implements InviteEmailGrantStore.
func (r guestPolicyRows) SetInviteEmailGrantCFAdded(email, userID string) (bool, error) {
	email = coreauth.NormalizeEmail(email)
	r.p.inviteMu.Lock()
	defer r.p.inviteMu.Unlock()
	row, err := r.get(email)
	if err != nil {
		return false, err
	}
	if row == nil || row.UserID != userID {
		return false, nil
	}
	row.CFAdded = true
	data, err := json.Marshal(row)
	if err != nil {
		return false, err
	}
	if err := r.p.db.Set([]byte(guestPolicyPrefix+email), data, pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteInviteEmailGrant implements InviteEmailGrantStore.
func (r guestPolicyRows) DeleteInviteEmailGrant(email, userID string) (*InviteEmailGrant, error) {
	email = coreauth.NormalizeEmail(email)
	if email == "" {
		return nil, nil
	}
	r.p.inviteMu.Lock()
	defer r.p.inviteMu.Unlock()
	row, err := r.get(email)
	if err != nil || row == nil || row.UserID != userID {
		return nil, err
	}
	if err := r.p.db.Delete([]byte(guestPolicyPrefix+email), pebble.Sync); err != nil {
		return nil, err
	}
	return asGrant(row), nil
}
