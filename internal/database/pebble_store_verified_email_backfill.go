// file: internal/database/pebble_store_verified_email_backfill.go
// version: 1.0.0
// guid: b03036d5-834d-4848-93e7-d72c52242753
// last-edited: 2026-10-19

package database

import (
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// verifiedEmailBackfillKey marks the one-time repair of idx:user:email as the
// verified-email index (see the email keys in pebble_store_auth.go).
const verifiedEmailBackfillKey = "system:backfill:user_verified_email_v1_done"

// VerifiedEmailBackfillResult counts what backfillVerifiedEmailIndex changed.
type VerifiedEmailBackfillResult struct {
	Skipped      bool // the sentinel was already set
	StaleDropped int  // keys whose user is gone or no longer has that address
	IdPAdded     int  // current emails a linked identity verified, newly keyed
}

// backfillVerifiedEmailIndex brings idx:user:email to its verified-email
// meaning, once (sentinel verifiedEmailBackfillKey; re-running it is also
// harmless). It runs at open.
//
// Before this build, only CreateUser (setup, bootstrap, the seeded system
// user, IdP auto-create) and ConsumeInvite (the admin-typed invite email)
// wrote the key, and UpdateUser never did, so every key that still matches its
// user's current email came from an admin or an identity provider and stays
// verified: nothing that links today stops linking. The repair:
//   - drops each key whose user no longer exists or no longer has that address
//     (an address the user changed away from is not theirs to be linked by);
//   - adds a key for a user's current email when one of their linked OAuth
//     identities was recorded with that email (the IdP verified it) and no
//     key exists for it yet.
//
// A user row that does not decode keeps its keys: absence of proof is not
// proof the address is stale.
func (p *PebbleStore) backfillVerifiedEmailIndex() (VerifiedEmailBackfillResult, error) {
	var res VerifiedEmailBackfillResult
	if _, closer, err := p.db.Get([]byte(verifiedEmailBackfillKey)); err == nil {
		closer.Close()
		res.Skipped = true
		return res, nil
	} else if err != pebble.ErrNotFound {
		return res, err
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()

	users, badKeys, err := p.listUsers()
	if err != nil {
		return res, err
	}
	byID := make(map[string]*User, len(users))
	for i := range users {
		byID[users[i].ID] = &users[i]
	}
	undecodable := make(map[string]bool, len(badKeys))
	for _, k := range badKeys {
		undecodable[strings.TrimPrefix(k, "u:")] = true
	}

	b := p.db.NewBatch()
	defer b.Close()
	keyed := map[string]bool{}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(userEmailIndexPrefix),
		UpperBound: []byte(userEmailIndexPrefix + "\xff"),
	})
	if err != nil {
		return res, err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		lower := strings.TrimPrefix(string(iter.Key()), userEmailIndexPrefix)
		id := string(iter.Value())
		u := byID[id]
		if undecodable[id] || (u != nil && strings.EqualFold(strings.TrimSpace(u.Email), lower)) {
			keyed[lower] = true
			continue
		}
		if err := b.Delete(append([]byte(nil), iter.Key()...), nil); err != nil {
			iter.Close()
			return res, err
		}
		res.StaleDropped++
	}
	if err := iter.Close(); err != nil {
		return res, err
	}

	for _, u := range users {
		lower := strings.ToLower(strings.TrimSpace(u.Email))
		if lower == "" || keyed[lower] {
			continue
		}
		ids, err := p.GetOAuthIdentitiesForUser(u.ID)
		if err != nil {
			return res, err
		}
		for _, id := range ids {
			if strings.EqualFold(strings.TrimSpace(id.Email), lower) {
				if err := b.Set([]byte(userEmailIndexPrefix+lower), []byte(u.ID), nil); err != nil {
					return res, err
				}
				keyed[lower] = true
				res.IdPAdded++
				break
			}
		}
	}
	if err := b.Set([]byte(verifiedEmailBackfillKey), []byte("1"), nil); err != nil {
		return res, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return res, err
	}
	authLog.Info("verified-email index repaired: %d stale key(s) dropped, %d IdP-verified email(s) added", res.StaleDropped, res.IdPAdded)
	return res, nil
}
