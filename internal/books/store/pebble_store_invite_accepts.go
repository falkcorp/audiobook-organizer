// file: internal/books/store/pebble_store_invite_accepts.go
// version: 0.1.0
// guid: 2d6f81c4-9a37-4b5e-8c10-f3a7e92d5b68
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// inviteAcceptPrefix keys books' record of an accepted invite:
// acceptedinvite:<invite id> -> InviteAccept. The id is core's public invite id
// for an accept made under core identity, and HEAD's own derived InviteID for one
// the identity cutover (M3) carried over; the two never collide (a ULID and 128
// bits of hex). The family sorts outside "invite:", "inviteallow:" and
// "invitemail:", so no scan of those reaches it.
const inviteAcceptPrefix = "acceptedinvite:"

// InviteAccept is what the Users page's accepted-invites list shows for one
// accepted invite (HEAD's acceptedInviteView), kept by books because under core
// identity the invite is core's and is gone once accepted. It is the accept's own
// record and is written once, when core reports the accept; what the guest-policy
// sync did afterwards is a separate family (GuestPolicyOutcome), so a sync that
// fails or never runs never costs the accept its row.
//
// It holds no token and no secret: an invite's token is never an input here (HEAD's
// carried rows are keyed by InviteID, a one-way hash of it).
type InviteAccept struct {
	// InviteID is the key.
	InviteID string `json:"invite_id"`
	// UserID is the account the accept created.
	UserID string `json:"user_id"`
	// Username is the account's name as the accept created it.
	Username string `json:"username"`
	// Email is the address the invite named, normalized; empty when it named
	// none. The account holds it verified only when EmailBinding is
	// InviteEmailBound.
	Email string `json:"email,omitempty"`
	// AcceptedAt is when the accept happened: core's own time for it (the created
	// account's CreatedAt, taken from the one clock the accept's batch uses), and
	// for a carried row HEAD's UsedAt. The list's window and its order key on it.
	AcceptedAt time.Time `json:"accepted_at"`
	// EmailBinding and SSOGrant are HEAD's Invite.EmailBinding and Invite.SSOGrant
	// values (InviteEmail* and InviteSSO*): what the accept did with Email.
	EmailBinding string `json:"email_binding,omitempty"`
	SSOGrant     string `json:"sso_grant,omitempty"`
	// DisplacedUserIDs are the accounts that had typed Email into their profile
	// and lost it to the accept.
	DisplacedUserIDs []string `json:"displaced_user_ids,omitempty"`
	// GuestPolicy* are what HEAD recorded on the invite about the guest-policy
	// sync. They are set only on a row M3 carried, because a pre-flip accept has
	// no outcome row of its own (GuestPolicyOutcome is keyed by core invite id and
	// written by the sync); an accept made under core identity leaves them empty
	// and the list reads that sync's outcome row instead.
	GuestPolicyStatus string     `json:"guest_policy_status,omitempty"`
	GuestPolicyDetail string     `json:"guest_policy_detail,omitempty"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at,omitempty"`
}

// InviteAcceptStore is the capability behind books' accepted-invite rows under
// core identity. Not part of Store; resolve it with AsCapability.
type InviteAcceptStore interface {
	// RecordInviteAccept stores a under a.InviteID, replacing any row for it. It
	// refuses a row with no invite id, user id or accept time: a row with no time
	// could never be listed, so it is refused rather than written unlistable.
	RecordInviteAccept(a InviteAccept) error
	// ListInviteAcceptsSince returns the rows accepted at or after since, newest
	// first (ties by invite id, descending, so the order is the same on every
	// read). A row that does not decode is skipped. The result is never nil.
	ListInviteAcceptsSince(since time.Time) ([]InviteAccept, error)
}

var _ InviteAcceptStore = (*PebbleStore)(nil)

// RecordInviteAccept implements InviteAcceptStore.
func (p *PebbleStore) RecordInviteAccept(a InviteAccept) error {
	switch {
	case a.InviteID == "":
		return errors.New("invite accept needs an invite id")
	case a.UserID == "":
		return errors.New("invite accept needs the accepted user's id")
	case a.AcceptedAt.IsZero():
		return errors.New("invite accept needs its accept time")
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(inviteAcceptPrefix+a.InviteID), data, pebble.Sync)
}

// ListInviteAcceptsSince implements InviteAcceptStore.
func (p *PebbleStore) ListInviteAcceptsSince(since time.Time) ([]InviteAccept, error) {
	// ';' is the byte after ':', so the bounds span exactly the family.
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(inviteAcceptPrefix),
		UpperBound: []byte("acceptedinvite;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []InviteAccept{}
	for iter.First(); iter.Valid(); iter.Next() {
		var a InviteAccept
		if err := json.Unmarshal(iter.Value(), &a); err != nil {
			continue
		}
		if a.AcceptedAt.Before(since) {
			continue
		}
		out = append(out, a)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AcceptedAt.Equal(out[j].AcceptedAt) {
			return out[i].AcceptedAt.After(out[j].AcceptedAt)
		}
		return out[i].InviteID > out[j].InviteID
	})
	return out, nil
}
