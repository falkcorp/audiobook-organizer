// file: internal/database/pebble_store_verified_email_backfill.go
// version: 1.1.0
// guid: b03036d5-834d-4848-93e7-d72c52242753
// last-edited: 2026-10-19

package database

import (
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// verifiedEmailBackfillKey marks the one-time rebuild of idx:user:email as the
// verified-email index (see the email keys in pebble_store_auth.go). v2: v1
// (never deployed) trusted every key matching its user's current email, which
// would have grandfathered an address squatted before the rebuild.
const verifiedEmailBackfillKey = "system:backfill:user_verified_email_v2_done"

// VerifiedEmailBackfillResult counts what backfillVerifiedEmailIndex changed.
type VerifiedEmailBackfillResult struct {
	Skipped  bool // the sentinel was already set
	Dropped  int  // keys with no provenance (or pointing at the wrong user), deleted
	Kept     int  // keys that were already right
	Added    int  // provenance-backed keys that were missing, written
	Conflict int  // addresses proven for more than one user; left unkeyed
}

// backfillVerifiedEmailIndex rebuilds idx:user:email from provenance only,
// once (sentinel verifiedEmailBackfillKey; re-running it is also harmless). It
// runs at open.
//
// A user's current email is verified when, and only when, there is a record
// of how it was proven:
//   - an accepted-invite SSO grant (inviteallow:<email>) naming that user,
//     which exists only when the invitation was emailed to the address; or
//   - a linked OAuth identity for that user recorded with that email (the
//     identity provider verified it when the link was made).
//
// Every other key is dropped, including keys CreateUser wrote at setup or
// bootstrap: the store records no provenance for those, and a key that merely
// matches the current email proves nothing, since before this build anyone
// could have squatted an address. Nobody who already signs in through SSO is
// affected: ResolveUser finds them by their (provider, subject) link first and
// never consults the email index for them. Going forward CreateUser still
// records its email as verified.
//
// A user row that does not decode keeps its keys: absence of proof is not
// proof the address is unproven.
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
	undecodable := make(map[string]bool, len(badKeys))
	for _, k := range badKeys {
		undecodable[strings.TrimPrefix(k, "u:")] = true
	}

	// desired: address -> the one user with provenance for it.
	desired := map[string]string{}
	conflicted := map[string]bool{}
	for _, u := range users {
		lower := strings.ToLower(strings.TrimSpace(u.Email))
		if lower == "" {
			continue
		}
		proven, err := p.emailProvenFor(u.ID, lower)
		if err != nil {
			return res, err
		}
		if !proven {
			continue
		}
		if other, ok := desired[lower]; ok && other != u.ID {
			conflicted[lower] = true
			continue
		}
		desired[lower] = u.ID
	}
	for lower := range conflicted {
		delete(desired, lower)
		res.Conflict++
		authLog.Warn("verified-email rebuild: an address is proven for more than one user; left unverified for all of them")
	}

	b := p.db.NewBatch()
	defer b.Close()
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
		if want, ok := desired[lower]; ok && want == id {
			res.Kept++
			delete(desired, lower)
			continue
		}
		if undecodable[id] {
			continue
		}
		if err := b.Delete(append([]byte(nil), iter.Key()...), nil); err != nil {
			iter.Close()
			return res, err
		}
		res.Dropped++
	}
	if err := iter.Close(); err != nil {
		return res, err
	}
	for lower, id := range desired {
		if err := b.Set([]byte(userEmailIndexPrefix+lower), []byte(id), nil); err != nil {
			return res, err
		}
		res.Added++
	}
	if err := b.Set([]byte(verifiedEmailBackfillKey), []byte("1"), nil); err != nil {
		return res, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return res, err
	}
	authLog.Info("verified-email index rebuilt from provenance: %d kept, %d added, %d dropped, %d conflicted",
		res.Kept, res.Added, res.Dropped, res.Conflict)
	return res, nil
}

// emailProvenFor reports whether the store holds a record proving lower is
// userID's address: an invite grant naming userID, or a linked OAuth identity
// of userID recorded with that email.
func (p *PebbleStore) emailProvenFor(userID, lower string) (bool, error) {
	g, err := p.GetInviteEmailGrant(lower)
	if err != nil {
		return false, err
	}
	if g != nil && g.UserID == userID {
		return true, nil
	}
	ids, err := p.GetOAuthIdentitiesForUser(userID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if strings.EqualFold(strings.TrimSpace(id.Email), lower) {
			return true, nil
		}
	}
	return false, nil
}

// RerunVerifiedEmailBackfillForTest clears the sentinel and runs the rebuild,
// as the first open of an upgraded store would. Tests in other packages use
// it; an in-memory store cannot be reopened.
func (p *PebbleStore) RerunVerifiedEmailBackfillForTest() (VerifiedEmailBackfillResult, error) {
	if err := p.db.Delete([]byte(verifiedEmailBackfillKey), pebble.Sync); err != nil {
		return VerifiedEmailBackfillResult{}, err
	}
	return p.backfillVerifiedEmailIndex()
}
