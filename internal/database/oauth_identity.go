// file: internal/database/oauth_identity.go
// version: 1.1.0
// guid: b3e9c1a7-5f42-4d80-9c16-0a7e2b6d4f38
// last-edited: 2026-10-20

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// OAuthIdentity links an external identity-provider account (GitHub, Google, or a
// Cloudflare Access identity) to a local User. A User may have several linked
// identities (one per provider). Lookups are by (Provider, Subject) — the provider's
// stable, immutable account id — never by email, which can change.
type OAuthIdentity struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"` // "github" | "google" | "cfaccess"
	Subject   string    `json:"subject"`  // provider's stable account id (GitHub numeric id, Google "sub")
	Email     string    `json:"email"`    // email at link time (informational; matching is by subject)
	UserID    string    `json:"user_id"`  // the linked local User.ID
	CreatedAt time.Time `json:"created_at"`
}

// oauthSubjectKey is the secondary-index key that maps (provider, subject) → identity id.
func oauthSubjectKey(provider, subject string) string {
	return "idx:oauth:sub:" + provider + ":" + subject
}

// CreateOAuthIdentity persists a new linked identity with primary key
// oauthid:<id> plus a (provider, subject) lookup index and a per-user index.
func (p *PebbleStore) CreateOAuthIdentity(identity *OAuthIdentity) (*OAuthIdentity, error) {
	b := p.db.NewBatch()
	defer b.Close()
	if err := stageOAuthIdentity(b, identity); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return identity, nil
}

// stageOAuthIdentity validates identity, fills its ID and CreatedAt when
// unset, and stages its three keys in b.
func stageOAuthIdentity(b *GatedBatch, identity *OAuthIdentity) error {
	if identity == nil {
		return fmt.Errorf("create oauth identity: nil identity")
	}
	if identity.Provider == "" || identity.Subject == "" || identity.UserID == "" {
		return fmt.Errorf("create oauth identity: provider, subject, and user_id are required")
	}
	if identity.ID == "" {
		id, err := newULID()
		if err != nil {
			return err
		}
		identity.ID = id
	}
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = time.Now()
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if err := b.Set([]byte("oauthid:"+identity.ID), data, nil); err != nil {
		return err
	}
	if err := b.Set([]byte(oauthSubjectKey(identity.Provider, identity.Subject)), []byte(identity.ID), nil); err != nil {
		return err
	}
	return b.Set([]byte("idx:oauth:user:"+identity.UserID+":"+identity.ID), []byte("1"), nil)
}

// VerifiedEmailIdentityLinker links an SSO identity to the account that holds
// its email proven, deciding and writing under one lock. Not on Store;
// resolve it with AsCapability.
type VerifiedEmailIdentityLinker interface {
	// LinkIdentityToVerifiedEmailHolder finds the one account holding email
	// proven (as GetUserByVerifiedEmail does) and, in the same locked
	// section, creates identity linked to it (identity.UserID is set here).
	// It returns that account, or (nil, nil) when no account holds the
	// address proven, in which case nothing is written. Deciding and writing
	// separately would let the holder change its email, or another account
	// become a second proven holder, between the lookup and the link.
	LinkIdentityToVerifiedEmailHolder(identity *OAuthIdentity, email string) (*User, error)
}

var _ VerifiedEmailIdentityLinker = (*PebbleStore)(nil)

// LinkIdentityToVerifiedEmailHolder implements VerifiedEmailIdentityLinker.
// It holds inviteMu, which every email writer takes, for the lookup and the
// write.
func (p *PebbleStore) LinkIdentityToVerifiedEmailHolder(identity *OAuthIdentity, email string) (*User, error) {
	lower := util.NormalizeEmail(email)
	if lower == "" || identity == nil {
		return nil, nil
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	holder, err := p.verifiedHolderLocked(lower)
	if err != nil || holder == nil {
		return nil, err
	}
	identity.UserID = holder.ID
	b := p.db.NewBatch()
	defer b.Close()
	if err := stageOAuthIdentity(b, identity); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	if err := p.syncVerifiedEmailKeyLocked(lower, holder); err != nil {
		authLog.Warn("verified-email cache: repairing a key failed: %v", err)
	}
	return holder, nil
}

// GetOAuthIdentityByProviderSubject resolves a linked identity by its provider and
// stable subject id. Returns (nil, nil) when no identity is linked.
func (p *PebbleStore) GetOAuthIdentityByProviderSubject(provider, subject string) (*OAuthIdentity, error) {
	idBytes, closer, err := p.db.Get([]byte(oauthSubjectKey(provider, subject)))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(idBytes)
	closer.Close()

	v, closer2, err := p.db.Get([]byte("oauthid:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil // dangling index; treat as absent
	}
	if err != nil {
		return nil, err
	}
	defer closer2.Close()
	var oi OAuthIdentity
	if err := json.Unmarshal(v, &oi); err != nil {
		return nil, err
	}
	return &oi, nil
}

// GetOAuthIdentitiesForUser lists every linked identity for a user (for an account
// settings page). Iterates the per-user index prefix.
func (p *PebbleStore) GetOAuthIdentitiesForUser(userID string) ([]OAuthIdentity, error) {
	prefix := []byte("idx:oauth:user:" + userID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []OAuthIdentity
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		id := string(key[len(prefix):])
		v, closer, gerr := p.db.Get([]byte("oauthid:" + id))
		if gerr == pebble.ErrNotFound {
			continue
		}
		if gerr != nil {
			return nil, gerr
		}
		var oi OAuthIdentity
		uerr := json.Unmarshal(v, &oi)
		closer.Close()
		if uerr != nil {
			return nil, uerr
		}
		out = append(out, oi)
	}
	return out, nil
}
