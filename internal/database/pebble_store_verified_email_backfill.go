// file: internal/database/pebble_store_verified_email_backfill.go
// version: 2.6.0
// guid: b03036d5-834d-4848-93e7-d72c52242753
// last-edited: 2026-10-20

package database

import (
	"encoding/json"
	"fmt"
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

// SelfAdminConflict pairs an admin account stamped "self" with the other
// account that holds the same address proven.
type SelfAdminConflict struct {
	AdminID  string // holds the address as typed in (not verified)
	HolderID string // holds the address verified; SSO with it signs in here
}

// DowngradedDuplicate is one address's demoted legacy rows: Demoted were
// stamped "self" (typed in, not verified); HolderID holds the address
// verified, so SSO with it signs in there, or is "" when the tied candidates
// were all demoted and no account holds it verified.
type DowngradedDuplicate struct {
	HolderID string
	Demoted  []string
}

// downgradedDuplicateWarning is the boot warning for one DowngradedDuplicate.
// It names every account involved and says which one, if any, holds the
// address verified, so the operator knows whose email must not be cleared.
// No email address is logged.
func downgradedDuplicateWarning(d DowngradedDuplicate) string {
	if d.HolderID != "" {
		return fmt.Sprintf("verified email: account(s) %v were stamped as typed in, not verified: account %s holds the same address verified, and SSO with that address signs in account %s. Do NOT clear the email of account %s (the verified holder). Leave all of these accounts as they are until they are merged", d.Demoted, d.HolderID, d.HolderID, d.HolderID)
	}
	return fmt.Sprintf("verified email: account(s) %v qualified equally for the same verified address and none could be preferred, so all were stamped as typed in and no account holds it verified; an SSO login with that address reaches or creates a separate account. Leave these accounts as they are until they are merged", d.Demoted)
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
	// address another account already holds proven, each with that holder.
	// Setting the address for the admin is refused (it would be proven
	// twice); both accounts are left as they are until they are merged.
	SelfAdminsVerifiedElsewhere []SelfAdminConflict
	// DowngradedDuplicates lists, per address, the legacy rows stamped "self"
	// although their history pointed to a trusted source, because another
	// account already held, or also qualified for, the address proven
	// (demoteDuplicateProofs), with the account that holds it verified now.
	DowngradedDuplicates           []DowngradedDuplicate
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
	for _, d := range res.DowngradedDuplicates {
		authLog.Warn("%s", downgradedDuplicateWarning(d))
	}
	if err := p.classifySelfAdminsLocked(&res); err != nil {
		authLog.Warn("verified email: could not check whether the self-set admin addresses are verified elsewhere: %v", err)
	}
	if settable := slices.DeleteFunc(slices.Clone(res.StampedSelfAdmins), func(id string) bool {
		return slices.ContainsFunc(res.SelfAdminsVerifiedElsewhere, func(c SelfAdminConflict) bool { return c.AdminID == id })
	}); len(settable) > 0 {
		authLog.Warn("verified email: admin account(s) %v hold an email they set themselves, so it is not verified; an SSO login with that address reaches or creates a different account and takes the address from them. Do not set the address for them on the Users page: that makes this account its verified holder, which is wrong if the address signs in another account through SSO. Leave the account as it is; if a second account for the same person appears, leave both as they are until they are merged", settable)
	}
	for _, c := range res.SelfAdminsVerifiedElsewhere {
		authLog.Warn("verified email: admin account %s holds an email it set itself (not verified); account %s holds the same address verified, and SSO with that address signs in account %s. Do NOT clear the email of account %s (the verified holder), and do not set the address for account %s (it will be refused). Leave both accounts as they are until they are merged", c.AdminID, c.HolderID, c.HolderID, c.HolderID, c.AdminID)
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
		holder, err := p.provenHolderOtherIDLocked(util.NormalizeEmail(u.Email), id)
		if err != nil {
			return err
		}
		if holder != "" {
			res.SelfAdminsVerifiedElsewhere = append(res.SelfAdminsVerifiedElsewhere, SelfAdminConflict{AdminID: id, HolderID: holder})
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
	keyHolders := map[string]string{} // address -> the id its pre-existing key names
	for _, u := range legacy {
		source, err := p.stampEmailSource(u, invites)
		if err != nil {
			return err
		}
		sources[u.ID] = source
		lower := util.NormalizeEmail(u.Email)
		if _, seen := keyHolders[lower]; seen {
			continue
		}
		holder, err := p.emailKeyHolder(lower)
		if err != nil {
			return err
		}
		keyHolders[lower] = holder
	}
	res.DowngradedDuplicates = demoteDuplicateProofs(users, sources, keyHolders)
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

// stampRank orders the sources stamping can give a legacy row by how directly
// they prove the address: the setup key (CreateUser) is unique per address and
// written at creation, an admin-sent invite proves the inbox at accept time, a
// recorded identity proves it at link time only. invite_link and self prove
// nothing and rank below every trusted source.
var stampRank = map[string]int{
	EmailSourceSetup: 4, EmailSourceInvite: 3, EmailSourceIdP: 2,
	EmailSourceInviteLink: 1, EmailSourceSelf: 0,
}

// demoteDuplicateProofs keeps stamping from making a second proven holder.
// sources maps each legacy row's id to the source stampEmailSource chose, and
// keyHolders maps an address to the id its pre-existing idx:user:email key
// names (what main's SSO lookup returned for it), "" for none. For every
// address that would end up proven for more than one account, legacy
// candidates are demoted to "self" in place:
//   - all of them when a row that is not legacy already holds the address
//     proven (that row was proven by a write path, which outranks history);
//   - otherwise all but the single highest-ranked candidate (stampRank);
//   - on a tie for the highest rank, all but the tied candidate the key names,
//     so the account keeps the address it had on main; all of them when the
//     key names none of the tied candidates.
//
// It returns the demoted ids per address, with the account that holds the
// address verified afterwards ("" when none does), sorted by first demoted id.
func demoteDuplicateProofs(users []User, sources, keyHolders map[string]string) []DowngradedDuplicate {
	type cand struct {
		id   string
		rank int
	}
	existing := map[string]string{} // address -> the non-legacy row holding it proven
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
			if _, seen := existing[lower]; !seen {
				existing[lower] = u.ID
			}
		}
	}
	var out []DowngradedDuplicate
	for lower, cs := range candidates {
		var demoted []string
		demote := func(c cand) {
			sources[c.id] = EmailSourceSelf
			demoted = append(demoted, c.id)
		}
		if holder, ok := existing[lower]; ok {
			for _, c := range cs {
				demote(c)
			}
			slices.Sort(demoted)
			out = append(out, DowngradedDuplicate{HolderID: holder, Demoted: demoted})
			continue
		}
		if len(cs) < 2 {
			continue
		}
		top := cs[0].rank
		for _, c := range cs[1:] {
			top = max(top, c.rank)
		}
		var tied []cand
		for _, c := range cs {
			if c.rank == top {
				tied = append(tied, c)
			}
		}
		keep := ""
		if len(tied) == 1 {
			keep = tied[0].id
		} else if holder := keyHolders[lower]; holder != "" {
			for _, c := range tied {
				if c.id == holder {
					keep = holder
				}
			}
		}
		for _, c := range cs {
			if c.id != keep {
				demote(c)
			}
		}
		if len(demoted) > 0 {
			slices.Sort(demoted)
			out = append(out, DowngradedDuplicate{HolderID: keep, Demoted: demoted})
		}
	}
	slices.SortFunc(out, func(a, b DowngradedDuplicate) int { return strings.Compare(a.Demoted[0], b.Demoted[0]) })
	return out
}

// emailKeyHolder returns the id the idx:user:email key for lower names, ""
// when there is none. Caller holds inviteMu.
func (p *PebbleStore) emailKeyHolder(lower string) (string, error) {
	if lower == "" {
		return "", nil
	}
	v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + lower))
	if err == pebble.ErrNotFound {
		return "", nil
	} else if err != nil {
		return "", err
	}
	id := string(v)
	closer.Close()
	return id, nil
}

// stampEmailSource decides a legacy row's EmailSource from what older builds
// left behind. Every rule is checked and the row gets the BEST source it
// qualifies for (stampRank): a row that has both the setup key and a recorded
// identity is "setup", not "idp". Stopping at the first match ranked such a
// row as low as an identity-only row, so two accounts of one person could tie
// and both lose the address.
//   - a used invite that created this account (AcceptedUserID, or on rows
//     from before that field the same username and email) gives "invite" only
//     when that accept bound a delivered address (EmailBinding "bound") on
//     the admin path (SSOGrant "granted", or the invite's creator holds the
//     admin role now), the same rule ConsumeInvite applies today; otherwise
//     "invite_link", which proves nothing about the inbox (a copied link can
//     be forwarded, and a non-admin creator could accept it themselves).
//     Older accepts also wrote the idx:user:email key for the invite's
//     address, so when such an invite exists the key is that accept's write,
//     not CreateUser's, and does not count as "setup";
//   - otherwise, the idx:user:email key for the address names this user:
//     "setup". This trusts a bare key, which holds for every key a released
//     build wrote. On origin/main (670c8d7c5) CreateUser is the only writer
//     that fires: its callers are first-run setup
//     (internal/server/handlers/auth.go:360), bootstrap
//     (internal/server/bootstrap.go:445), the system user
//     (internal/auth/seed.go:150) and IdP auto-create
//     (internal/oauth/resolve.go:81, an IdP-verified address). Main's
//     ConsumeInvite also writes it for Invite.Email
//     (pebble_store_auth.go:869), but main's only CreateInvite caller
//     (internal/server/handlers/user.go:95) never sets Email, so that write
//     never fires; UpdateUser never wrote the key. Older builds lowercased
//     the address without trimming it, so both spellings are checked;
//   - a linked identity of the user recorded with the address: an identity
//     provider verified it, so "idp";
//   - anything else was typed in through the profile: "self".
func (p *PebbleStore) stampEmailSource(u *User, invites []Invite) (string, error) {
	lower := util.NormalizeEmail(u.Email)
	best := EmailSourceSelf
	consider := func(source string) {
		if stampRank[source] > stampRank[best] {
			best = source
		}
	}
	invited := false
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
		invited = true
		source := EmailSourceInviteLink
		if inv.EmailBinding == InviteEmailBound {
			if inv.SSOGrant == InviteSSOGranted {
				source = EmailSourceInvite
			} else {
				creator, err := p.GetUserByID(inv.CreatedByUserID)
				if err != nil {
					return "", err
				}
				if inviteCreatorMayGrant(creator) {
					source = EmailSourceInvite
				}
			}
		}
		consider(source)
	}
	if !invited {
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
				return EmailSourceSetup, nil // the top rank: nothing can beat it
			}
		}
	}
	ids, err := p.GetOAuthIdentitiesForUser(u.ID)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if util.NormalizeEmail(id.Email) == lower {
			consider(EmailSourceIdP)
		}
	}
	return best, nil
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
