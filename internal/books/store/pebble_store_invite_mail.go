// file: internal/books/store/pebble_store_invite_mail.go
// version: 0.1.0
// guid: 5b2f9d70-4c18-4e63-8a07-d1e6b3c9f245
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// inviteMailPrefix keys books' delivery record of a core invite:
// invitemail:<core invite id> -> InviteMailRecord. It sorts outside the
// "invite:" range ListAcceptedInvitesSince scans.
const inviteMailPrefix = "invitemail:"

// InviteMailRecord is what books remembers about a core invite's e-mail
// (F4-08 B10t): only what core does not hold. It is UI and resend data and
// nothing more. It holds no token, and it is not a gate: whether accepting the
// invite binds its address is core's BindEmailOnAccept, never this record.
type InviteMailRecord struct {
	// InviteID is core's public invite ID, the key.
	InviteID string `json:"invite_id"`
	// To is the address the invitation was mailed to.
	To string `json:"to"`
	// SentAt is when the last send of this invite succeeded.
	SentAt time.Time `json:"sent_at"`
	// ExpiresAt is the invite's expiry. It only lets a sweep drop the record of
	// an invite that can no longer be resent.
	ExpiresAt time.Time `json:"expires_at"`
}

// InviteMailStore is the capability behind books' delivery record of core
// invites. Not part of Store; resolve it with AsCapability.
type InviteMailStore interface {
	// PutInviteMail stores rec under rec.InviteID, replacing any.
	PutInviteMail(rec InviteMailRecord) error
	// GetInviteMail returns the record of invite id, or nil when there is none.
	GetInviteMail(id string) (*InviteMailRecord, error)
	// ListInviteMail returns every record.
	ListInviteMail() ([]InviteMailRecord, error)
	// DeleteInviteMail removes the record of invite id; a missing one is not an
	// error.
	DeleteInviteMail(id string) error
}

var _ InviteMailStore = (*PebbleStore)(nil)

// PutInviteMail implements InviteMailStore.
func (p *PebbleStore) PutInviteMail(rec InviteMailRecord) error {
	if rec.InviteID == "" {
		return errors.New("invite mail record needs an invite id")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(inviteMailPrefix+rec.InviteID), data, pebble.Sync)
}

// GetInviteMail implements InviteMailStore.
func (p *PebbleStore) GetInviteMail(id string) (*InviteMailRecord, error) {
	if id == "" {
		return nil, nil
	}
	v, closer, err := p.db.Get([]byte(inviteMailPrefix + id))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var rec InviteMailRecord
	if err := json.Unmarshal(v, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListInviteMail implements InviteMailStore. An undecodable record is skipped.
func (p *PebbleStore) ListInviteMail() ([]InviteMailRecord, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(inviteMailPrefix),
		UpperBound: []byte(inviteMailPrefix + "~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []InviteMailRecord
	for iter.First(); iter.Valid(); iter.Next() {
		var rec InviteMailRecord
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, iter.Error()
}

// DeleteInviteMail implements InviteMailStore.
func (p *PebbleStore) DeleteInviteMail(id string) error {
	if id == "" {
		return nil
	}
	return p.db.Delete([]byte(inviteMailPrefix+id), pebble.Sync)
}
