// file: internal/database/pebble_store_verified_email_backfill.go
// version: 2.0.0
// guid: b03036d5-834d-4848-93e7-d72c52242753
// last-edited: 2026-10-19

package database

import (
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// Verified email, computed live.
//
// Whether a user's email is verified is decided from that user's own records
// every time it is asked (emailProvenFor): the store-set EmailSource, an
// accepted-invite SSO grant naming the user, or a linked identity of the user
// recorded with that email. Nothing is applied once and remembered, so no
// recorded state can leave an address stuck verified or stuck unverified.
//
// idx:user:email:<addr> (userEmailIndexPrefix) is only a cache of that answer,
// kept for a rollback build: older builds link an SSO login to whatever user
// that key names, without checking the user's current email, so the key must
// never name an account that has not proven the address. Every write path
// keeps it in step in its own batch, a lookup repairs the key for the address
// it looked up, and ReconcileVerifiedEmails rewrites all of them at store open.

// trustedEmailSource reports whether an EmailSource records a path that
// proves the address belongs to the account.
func trustedEmailSource(source string) bool {
	switch source {
	case EmailSourceSetup, EmailSourceAdmin, EmailSourceInvite, EmailSourceIdP:
		return true
	}
	return false
}

// emailProvenFor reports whether lower (lowercased, trimmed) is proven to be
// u's address: u holds it with a trusted EmailSource, an accepted-invite SSO
// grant for it names u, or a linked identity of u was recorded with it. A
// self-set address, an address bound by a copied invite link, and a row with
// no EmailSource (written by a build that did not know the field, until the
// next store open stamps it) prove nothing. It does not look at other
// holders; callers treat two proven holders as a conflict.
func (p *PebbleStore) emailProvenFor(u *User, lower string) (bool, error) {
	if !strings.EqualFold(strings.TrimSpace(u.Email), lower) {
		return false, nil
	}
	if trustedEmailSource(u.EmailSource) {
		return true, nil
	}
	g, err := p.GetInviteEmailGrant(lower)
	if err != nil {
		return false, err
	}
	if g != nil && g.UserID == u.ID {
		return true, nil
	}
	ids, err := p.GetOAuthIdentitiesForUser(u.ID)
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

// verifiedHolderLocked returns the one user whose current email is lower and
// proven (emailProvenFor), or nil when there is none or more than one (a
// conflict, logged without the address). Undecodable user rows are skipped:
// proof is per account, so an unreadable row cannot make another account's
// proof false, and it cannot be returned to a caller either. Caller holds
// inviteMu.
func (p *PebbleStore) verifiedHolderLocked(lower string) (*User, error) {
	users, badKeys, err := p.listUsers()
	if err != nil {
		return nil, err
	}
	if len(badKeys) > 0 {
		authLog.Warn("verified-email lookup: %d user row(s) unreadable and skipped", len(badKeys))
	}
	var found *User
	for i := range users {
		u := &users[i]
		ok, err := p.emailProvenFor(u, lower)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if found != nil {
			authLog.Warn("verified-email lookup: an address is proven for more than one user (%s, %s); left unverified for both", found.ID, u.ID)
			return nil, nil
		}
		found = u
	}
	return found, nil
}

// syncVerifiedEmailKeyLocked makes the cache key for lower name holder (nil:
// no key). Caller holds inviteMu.
func (p *PebbleStore) syncVerifiedEmailKeyLocked(lower string, holder *User) error {
	key := []byte(userEmailIndexPrefix + lower)
	v, closer, err := p.db.Get(key)
	switch {
	case err == pebble.ErrNotFound:
		if holder == nil {
			return nil
		}
		return p.db.Set(key, []byte(holder.ID), pebble.Sync)
	case err != nil:
		return err
	}
	current := string(v)
	closer.Close()
	if holder != nil && current == holder.ID {
		return nil
	}
	if holder == nil {
		return p.db.Delete(key, pebble.Sync)
	}
	return p.db.Set(key, []byte(holder.ID), pebble.Sync)
}

// ReconcileResult counts what ReconcileVerifiedEmails changed.
type ReconcileResult struct {
	// Stamped counts rows that had an email and no EmailSource, by the
	// source they were given.
	Stamped map[string]int
	// StampedSelfAdmins lists admin-role accounts stamped "self": after this,
	// an SSO login with that address takes it from them.
	StampedSelfAdmins              []string
	Kept, Added, Dropped, Conflict int // cache keys
}

// ReconcileVerifiedEmails runs at every store open: it stamps rows that have
// an email and no EmailSource, then rewrites every idx:user:email key from
// emailProvenFor. Both steps are idempotent, so there is no sentinel: a row
// rewritten by a rollback build (which drops the unknown email_source field)
// is stamped again at the next open, and a cache key a rollback build left
// is corrected.
//
// Stamping (stampEmailSource) reads the keys the previous build wrote, so it
// runs before the cache is rewritten, under the same locks (inviteMu, then
// userWriteMu).
func (p *PebbleStore) ReconcileVerifiedEmails() (ReconcileResult, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	userWriteMu.Lock()
	defer userWriteMu.Unlock()
	res := ReconcileResult{Stamped: map[string]int{}}
	if err := p.stampLegacyEmailSourcesLocked(&res); err != nil {
		return res, err
	}
	if err := p.rewriteVerifiedEmailCacheLocked(&res); err != nil {
		return res, err
	}
	if n := len(res.Stamped); n > 0 {
		authLog.Info("verified email: stamped email sources on rows from an older build: %v", res.Stamped)
	}
	if len(res.StampedSelfAdmins) > 0 {
		authLog.Warn("verified email: admin account(s) %v hold an email they set themselves; an SSO login with that address will take it from them. An admin can set it for them to keep it", res.StampedSelfAdmins)
	}
	authLog.Info("verified-email cache reconciled: %d kept, %d added, %d dropped, %d conflicted",
		res.Kept, res.Added, res.Dropped, res.Conflict)
	return res, nil
}

// stampLegacyEmailSourcesLocked gives every decodable row that has an email
// and no EmailSource the source its history shows (stampEmailSource), in one
// batch. Caller holds inviteMu and userWriteMu.
func (p *PebbleStore) stampLegacyEmailSourcesLocked(res *ReconcileResult) error {
	users, _, err := p.listUsers()
	if err != nil {
		return err
	}
	var legacy []*User
	for i := range users {
		if users[i].EmailSource == "" && strings.TrimSpace(users[i].Email) != "" {
			legacy = append(legacy, &users[i])
		}
	}
	if len(legacy) == 0 {
		return nil
	}
	invites, err := p.listInviteRows()
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	for _, u := range legacy {
		source, err := p.stampEmailSource(u, invites)
		if err != nil {
			return err
		}
		u.EmailSource = source
		data, err := json.Marshal(u)
		if err != nil {
			return err
		}
		if err := b.Set([]byte("u:"+u.ID), data, nil); err != nil {
			return err
		}
		res.Stamped[source]++
		if source == EmailSourceSelf && hasRole(u, inviteAdminRole) {
			res.StampedSelfAdmins = append(res.StampedSelfAdmins, u.ID)
		}
	}
	return b.Commit(pebble.Sync)
}

// stampEmailSource decides a legacy row's EmailSource from what older builds
// left behind, first match wins:
//  1. a linked identity of the user recorded with the address: an identity
//     provider verified it, so "idp";
//  2. a used invite that created this account (AcceptedUserID, or on rows
//     from before that field the same username and email): "invite" if that
//     accept bound a delivered address (EmailBinding "bound"), otherwise
//     "invite_link" -- older builds bound whatever address the invite named,
//     though the link may have been copied and forwarded, which proves
//     nothing about the inbox;
//  3. the idx:user:email key for the address names this user: before this
//     feature only CreateUser (setup, bootstrap, the system user) and
//     ConsumeInvite (handled by 2) wrote it, and a profile change never did,
//     so "setup". Older builds lowercased the address without trimming it, so
//     both spellings are checked;
//  4. anything else was typed in through the profile: "self".
func (p *PebbleStore) stampEmailSource(u *User, invites []Invite) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(u.Email))
	ids, err := p.GetOAuthIdentitiesForUser(u.ID)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if strings.EqualFold(strings.TrimSpace(id.Email), lower) {
			return EmailSourceIdP, nil
		}
	}
	for i := range invites {
		inv := &invites[i]
		if inv.UsedAt == nil || !strings.EqualFold(strings.TrimSpace(inv.Email), lower) {
			continue
		}
		created := inv.AcceptedUserID == u.ID ||
			(inv.AcceptedUserID == "" && strings.EqualFold(inv.Username, u.Username))
		if !created {
			continue
		}
		if inv.EmailBinding == InviteEmailBound {
			return EmailSourceInvite, nil
		}
		return EmailSourceInviteLink, nil
	}
	for _, spelling := range []string{lower, strings.ToLower(u.Email)} {
		v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + spelling))
		if err == pebble.ErrNotFound {
			continue
		} else if err != nil {
			return "", err
		}
		pointsHere := string(v) == u.ID
		closer.Close()
		if pointsHere {
			return EmailSourceSetup, nil
		}
	}
	return EmailSourceSelf, nil
}

// rewriteVerifiedEmailCacheLocked rewrites every idx:user:email key from
// emailProvenFor: one key per address proven for exactly one user, none for
// an address proven for two (logged without the address). A key naming an
// undecodable user row is left alone: nothing can be decided about that
// account until its row is readable. Caller holds inviteMu.
func (p *PebbleStore) rewriteVerifiedEmailCacheLocked(res *ReconcileResult) error {
	users, badKeys, err := p.listUsers()
	if err != nil {
		return err
	}
	undecodable := make(map[string]bool, len(badKeys))
	for _, k := range badKeys {
		undecodable[strings.TrimPrefix(k, "u:")] = true
	}
	desired := map[string]string{}
	conflicted := map[string]bool{}
	for i := range users {
		u := &users[i]
		lower := strings.ToLower(strings.TrimSpace(u.Email))
		if lower == "" {
			continue
		}
		ok, err := p.emailProvenFor(u, lower)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if other, dup := desired[lower]; dup && other != u.ID {
			conflicted[lower] = true
			continue
		}
		desired[lower] = u.ID
	}
	for lower := range conflicted {
		delete(desired, lower)
		res.Conflict++
		authLog.Warn("verified-email cache: an address is proven for more than one user; left unverified for all of them")
	}

	b := p.db.NewBatch()
	defer b.Close()
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(userEmailIndexPrefix),
		UpperBound: []byte(userEmailIndexPrefix + "\xff"),
	})
	if err != nil {
		return err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		lower := strings.TrimPrefix(string(iter.Key()), userEmailIndexPrefix)
		id := string(iter.Value())
		if want, ok := desired[lower]; ok && want == id {
			res.Kept++
			delete(desired, lower)
			continue
		}
		if undecodable[id] && !conflicted[lower] {
			continue
		}
		if err := b.Delete(append([]byte(nil), iter.Key()...), nil); err != nil {
			iter.Close()
			return err
		}
		res.Dropped++
	}
	if err := iter.Close(); err != nil {
		return err
	}
	for lower, id := range desired {
		if err := b.Set([]byte(userEmailIndexPrefix+lower), []byte(id), nil); err != nil {
			return err
		}
		res.Added++
	}
	return b.Commit(pebble.Sync)
}

// listInviteRows returns every decodable invite row (used or not).
func (p *PebbleStore) listInviteRows() ([]Invite, error) {
	prefix := []byte("invite:")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []Invite
	for iter.First(); iter.Valid(); iter.Next() {
		var inv Invite
		if err := json.Unmarshal(iter.Value(), &inv); err != nil {
			authLog.Warn("verified email: skipping an undecodable invite row")
			continue
		}
		out = append(out, inv)
	}
	return out, iter.Error()
}

func hasRole(u *User, role string) bool {
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}
