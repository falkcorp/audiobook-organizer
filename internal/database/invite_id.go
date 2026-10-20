// file: internal/database/invite_id.go
// version: 1.0.0
// guid: 098ae96c-a162-4762-b8f1-a068b0f8111f
// last-edited: 2026-10-20

package database

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/cockroachdb/pebble/v2"
)

// inviteIDDomain separates invite IDs from every other hash of a token (the
// public limiter hashes tokens too), so an ID can never be one of those keys.
const inviteIDDomain = "audiobook-organizer/invite-id/v1\x00"

// InviteID is the non-secret handle of the invite whose token is token: the
// first 128 bits of a domain-separated SHA-256 of it. The admin routes that
// revoke or resend an invite take this, never the token, so the invite list
// can name every invite without handing out the bearer secret that accepts
// it. It is derived, not stored, so invites written before it existed have
// one too; the hash cannot be turned back into the token.
func InviteID(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(inviteIDDomain + token))
	return hex.EncodeToString(sum[:16])
}

// InviteIDLookup finds an invite by its InviteID. Not on Store; resolve it
// with AsCapability.
type InviteIDLookup interface {
	// GetInviteByID returns the invite (pending, used or expired) whose
	// InviteID is id, or nil when there is none.
	GetInviteByID(id string) (*Invite, error)
}

var _ InviteIDLookup = (*PebbleStore)(nil)

// GetInviteByID implements InviteIDLookup. Invite rows are keyed by token, so
// it scans them; there are only ever a handful.
func (p *PebbleStore) GetInviteByID(id string) (*Invite, error) {
	if len(id) != 32 {
		return nil, nil
	}
	prefix := []byte("invite:")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		token := string(iter.Key()[len(prefix):])
		if InviteID(token) != id {
			continue
		}
		return p.GetInvite(token)
	}
	return nil, iter.Error()
}
