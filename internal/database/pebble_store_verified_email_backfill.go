// file: internal/database/pebble_store_verified_email_backfill.go
// version: 2.3.0
// guid: b03036d5-834d-4848-93e7-d72c52242753
// last-edited: 2026-10-20

package database

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Verified email, computed live.
//
// Whether a user's email is verified is decided from that user's own records
// every time it is asked (emailProven): the EmailSource the store stamped
// when the address was written. Only the writing path proves anything, so a
// self-service change can never inherit proof from older records.
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

// emailProven reports whether lower (normalized) is proven to be u's
// address: u holds it and its EmailSource is one the store stamps only on a
// path that proves control of the address (trustedEmailSource: setup, admin,
// invite, idp). Nothing else counts, whatever the row is linked to: a self-set
// address stays unproven even when the user has a linked identity recorded
// with that address, or an invite grant for it, from before. Those records
// say the user controlled the address once; they say nothing about who typed
// it in now, and counting them let a self-service change create a second
// proven holder. An IdP login that confirms the address re-stamps it idp
// (MarkUserEmailVerified), which is the way a self-set address becomes
// proven. A row with no EmailSource (written by a build that did not know the
// field, until the next store open stamps it), "self" and "invite_link" prove
// nothing. It does not look at other holders; callers treat two proven
// holders as a conflict.
func emailProven(u *User, lower string) bool {
	return lower != "" && util.NormalizeEmail(u.Email) == lower && trustedEmailSource(u.EmailSource)
}

// verifiedHolderLocked returns the one user whose current email is lower and
// proven (emailProven), or nil when there is none or more than one (a
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
		if !emailProven(u, lower) {
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
	StampedSelfAdmins []string
	// SelfAdminsVerifiedElsewhere is the subset of StampedSelfAdmins whose
	// address another account already holds proven. Setting the address for
	// them is refused (it would be proven twice); the duplicate account has
	// to be cleared or merged instead.
	SelfAdminsVerifiedElsewhere []string
	// DowngradedDuplicates lists legacy rows stamped "self" although their
	// history pointed to a trusted source, because another account already
	// held, or also qualified for, the address proven (demoteDuplicateProofs).
	DowngradedDuplicates           []string
	Kept, Added, Dropped, Conflict int // cache keys
}

// ReconcileVerifiedEmails runs at every store open: it stamps rows that have
// an email and no EmailSource, then rewrites every idx:user:email key from
// emailProven. Both steps are idempotent, so there is no sentinel: a row
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
	if len(res.DowngradedDuplicates) > 0 {
		authLog.Warn("verified email: account(s) %v were stamped as typed in, not verified, because another account holds or also qualified for the same address verified; clear the duplicate or merge the accounts", res.DowngradedDuplicates)
	}
	if err := p.classifySelfAdminsLocked(&res); err != nil {
		authLog.Warn("verified email: could not check whether the self-set admin addresses are verified elsewhere: %v", err)
	}
	if settable := slices.DeleteFunc(slices.Clone(res.StampedSelfAdmins), func(id string) bool {
		return slices.Contains(res.SelfAdminsVerifiedElsewhere, id)
	}); len(settable) > 0 {
		authLog.Warn("verified email: admin account(s) %v hold an email they set themselves; an SSO login with that address will take it from them. An admin can set it for them on the Users page to keep it", settable)
	}
	if len(res.SelfAdminsVerifiedElsewhere) > 0 {
		authLog.Warn("verified email: admin account(s) %v hold an email they set themselves that another account already has verified; SSO with that address reaches the other account. Setting it for them will be refused: clear the address from the duplicate account, or merge the two accounts", res.SelfAdminsVerifiedElsewhere)
	}
	authLog.Info("verified-email cache reconciled: %d kept, %d added, %d dropped, %d conflicted",
		res.Kept, res.Added, res.Dropped, res.Conflict)
	return res, nil
}

// classifySelfAdminsLocked fills res.SelfAdminsVerifiedElsewhere: the admins
// stamped "self" whose address another account holds proven. Caller holds
// inviteMu.
func (p *PebbleStore) classifySelfAdminsLocked(res *ReconcileResult) error {
	for _, id := range res.StampedSelfAdmins {
		u, err := p.GetUserByID(id)
		if err != nil {
			return err
		}
		if u == nil {
			continue
		}
		elsewhere, err := p.provenHolderOtherLocked(util.NormalizeEmail(u.Email), id)
		if err != nil {
			return err
		}
		if elsewhere {
			res.SelfAdminsVerifiedElsewhere = append(res.SelfAdminsVerifiedElsewhere, id)
		}
	}
	return nil
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
	sources := make(map[string]string, len(legacy))
	for _, u := range legacy {
		source, err := p.stampEmailSource(u, invites)
		if err != nil {
			return err
		}
		sources[u.ID] = source
	}
	res.DowngradedDuplicates = demoteDuplicateProofs(users, sources)
	b := p.db.NewBatch()
	defer b.Close()
	for _, u := range legacy {
		source := sources[u.ID]
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

// stampRank orders the trusted sources stamping can give a legacy row by how
// directly they prove the address: the setup key (CreateUser) is unique per
// address and written at creation, an admin-sent invite proves the inbox at
// accept time, a recorded identity proves it at link time only.
var stampRank = map[string]int{EmailSourceSetup: 3, EmailSourceInvite: 2, EmailSourceIdP: 1}

// demoteDuplicateProofs keeps stamping from making a second proven holder.
// sources maps each legacy row's id to the source stampEmailSource chose; for
// every address that would end up proven for more than one account, legacy
// candidates are demoted to "self" in place: all of them when a row that is
// not legacy already holds the address proven (that row was proven by a
// write path, which outranks history), otherwise all but a single
// highest-ranked candidate (stampRank), or all of them on a tie. It returns
// the demoted ids.
func demoteDuplicateProofs(users []User, sources map[string]string) []string {
	type cand struct {
		id   string
		rank int
	}
	existing := map[string]bool{}
	candidates := map[string][]cand{}
	for i := range users {
		u := &users[i]
		lower := util.NormalizeEmail(u.Email)
		if lower == "" {
			continue
		}
		if src, legacy := sources[u.ID]; legacy {
			if trustedEmailSource(src) {
				candidates[lower] = append(candidates[lower], cand{u.ID, stampRank[src]})
			}
		} else if emailProven(u, lower) {
			existing[lower] = true
		}
	}
	var demoted []string
	demote := func(c cand) {
		sources[c.id] = EmailSourceSelf
		demoted = append(demoted, c.id)
	}
	for lower, cs := range candidates {
		if existing[lower] {
			for _, c := range cs {
				demote(c)
			}
			continue
		}
		if len(cs) < 2 {
			continue
		}
		best, tie := cs[0], false
		for _, c := range cs[1:] {
			switch {
			case c.rank > best.rank:
				best, tie = c, false
			case c.rank == best.rank:
				tie = true
			}
		}
		for _, c := range cs {
			if tie || c.id != best.id {
				demote(c)
			}
		}
	}
	slices.Sort(demoted)
	return demoted
}

// stampEmailSource decides a legacy row's EmailSource from what older builds
// left behind, first match wins:
//  1. a linked identity of the user recorded with the address: an identity
//     provider verified it, so "idp";
//  2. a used invite that created this account (AcceptedUserID, or on rows
//     from before that field the same username and email): "invite" only
//     when that accept bound a delivered address (EmailBinding "bound") on
//     the admin path (SSOGrant "granted", or the invite's creator holds the
//     admin role now), the same rule ConsumeInvite applies today; otherwise
//     "invite_link", which proves nothing about the inbox (a copied link can
//     be forwarded, and a non-admin creator could accept it themselves);
//  3. the idx:user:email key for the address names this user: "setup". This
//     trusts a bare key, which holds for every key a released build wrote.
//     On origin/main (670c8d7c5) CreateUser is the only writer that fires:
//     its callers are first-run setup (internal/server/handlers/auth.go:360),
//     bootstrap (internal/server/bootstrap.go:445), the system user
//     (internal/auth/seed.go:150) and IdP auto-create
//     (internal/oauth/resolve.go:81, an IdP-verified address). Main's
//     ConsumeInvite also writes it for Invite.Email
//     (pebble_store_auth.go:869), but main's only CreateInvite caller
//     (internal/server/handlers/user.go:95) never sets Email, so that write
//     never fires; UpdateUser never wrote the key. States only this branch
//     can produce (say, a used invite deleted after a bound accept) never
//     shipped. Older builds lowercased the address without trimming it, so
//     both spellings are checked;
//  4. anything else was typed in through the profile: "self".
func (p *PebbleStore) stampEmailSource(u *User, invites []Invite) (string, error) {
	lower := util.NormalizeEmail(u.Email)
	ids, err := p.GetOAuthIdentitiesForUser(u.ID)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if util.NormalizeEmail(id.Email) == lower {
			return EmailSourceIdP, nil
		}
	}
	for i := range invites {
		inv := &invites[i]
		if inv.UsedAt == nil || util.NormalizeEmail(inv.Email) != lower {
			continue
		}
		created := inv.AcceptedUserID == u.ID ||
			(inv.AcceptedUserID == "" && strings.EqualFold(inv.Username, u.Username))
		if !created {
			continue
		}
		if inv.EmailBinding == InviteEmailBound {
			if inv.SSOGrant == InviteSSOGranted {
				return EmailSourceInvite, nil
			}
			creator, err := p.GetUserByID(inv.CreatedByUserID)
			if err != nil {
				return "", err
			}
			if inviteCreatorMayGrant(creator) {
				return EmailSourceInvite, nil
			}
		}
		return EmailSourceInviteLink, nil
	}
	for _, spelling := range []string{lower, strings.ToLower(strings.TrimSpace(u.Email)), strings.ToLower(u.Email)} {
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
// emailProven: one key per address proven for exactly one user, none for
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
		lower := util.NormalizeEmail(u.Email)
		if lower == "" {
			continue
		}
		if !emailProven(u, lower) {
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
