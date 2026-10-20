// file: internal/database/pebble_store_verified_email_backfill.go
// version: 1.3.0
// guid: b03036d5-834d-4848-93e7-d72c52242753
// last-edited: 2026-10-19

package database

import (
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// verifiedEmailIndexKey records that idx:user:email has been rebuilt as the
// verified-email index (see the email keys in pebble_store_auth.go) and holds
// the operator-trusted address list that rebuild applied, one lowercase
// address per line. v3: v1 (never deployed) trusted any key matching its
// user's current email; v2 (never deployed) had no operator list.
const verifiedEmailIndexKey = "system:backfill:user_verified_email_v3"

// OperatorEmailTrust feeds the operator's trusted addresses (the owner email
// and OAUTH_ALLOWED_EMAILS, which only the operator can set: environment,
// config file, or a proven owner session) into the verified-email index. Not
// on Store; resolve it with AsCapability.
type OperatorEmailTrust interface {
	// SetOperatorTrustedEmails installs the function that reads the current
	// list. It is read on every verified-email lookup, so a change to the
	// list is applied before the next lookup answers.
	SetOperatorTrustedEmails(list func() []string)
	// EnsureVerifiedEmailIndex rebuilds the index from provenance the first
	// time (no sentinel), and afterwards re-applies only the addresses added
	// to or removed from the operator list since the last run. A no-op until
	// SetOperatorTrustedEmails has been called.
	EnsureVerifiedEmailIndex() error
}

var _ OperatorEmailTrust = (*PebbleStore)(nil)

// VerifiedEmailBackfillResult counts what a rebuild or re-apply changed.
type VerifiedEmailBackfillResult struct {
	Full     bool // a full rebuild (no sentinel yet), not a list re-apply
	Dropped  int  // keys with no provenance (or pointing at the wrong user), deleted
	Kept     int  // keys that were already right
	Added    int  // provenance-backed keys that were missing, written
	Conflict int  // addresses proven for more than one user; left unkeyed
}

// normalizeOperatorEmails lowercases, trims, de-duplicates and sorts.
func normalizeOperatorEmails(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

func operatorSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, e := range list {
		set[e] = true
	}
	return set
}

// SetOperatorTrustedEmails implements OperatorEmailTrust.
func (p *PebbleStore) SetOperatorTrustedEmails(list func() []string) {
	p.operatorEmails.Store(&list)
}

// currentOperatorEmails returns the normalized operator list, and false when
// no list function is installed.
func (p *PebbleStore) currentOperatorEmails() ([]string, bool) {
	fn := p.operatorEmails.Load()
	if fn == nil || *fn == nil {
		return nil, false
	}
	return normalizeOperatorEmails((*fn)()), true
}

// EnsureVerifiedEmailIndex implements OperatorEmailTrust.
func (p *PebbleStore) EnsureVerifiedEmailIndex() error {
	list, ok := p.currentOperatorEmails()
	if !ok {
		return nil
	}
	canon := strings.Join(list, "\n")
	if applied := p.operatorApplied.Load(); applied != nil && *applied == canon {
		return nil
	}
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	v, closer, err := p.db.Get([]byte(verifiedEmailIndexKey))
	switch {
	case err == pebble.ErrNotFound:
		if _, err := p.rebuildVerifiedEmailIndexLocked(list); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		prior := string(v)
		closer.Close()
		if prior != canon {
			var old []string
			if prior != "" {
				old = strings.Split(prior, "\n")
			}
			if _, err := p.reapplyOperatorEmailsLocked(old, list); err != nil {
				return err
			}
		}
	}
	p.operatorApplied.Store(&canon)
	return nil
}

// rebuildVerifiedEmailIndexLocked rebuilds idx:user:email from provenance
// only. Caller holds inviteMu.
//
// A user's current email is verified when, and only when, there is a record
// of how it was proven (emailProvenFor): an accepted-invite SSO grant naming
// that user, a linked OAuth identity of that user recorded with that email,
// or the address being on the operator's trusted list while that user is the
// active holder and did not set the address themselves. Every other key is dropped, including keys CreateUser wrote
// at setup or bootstrap for addresses the operator does not list: no
// provenance is stored for those, and a key that merely matches the current
// email proves nothing, since before this build anyone could have squatted an
// address. People who already sign in through SSO are unaffected: ResolveUser
// finds them by their (provider, subject) link before it reads this index.
//
// An address proven for more than one user gets no key (logged without the
// address). An undecodable user row keeps its keys, and while any row is
// undecodable the operator list proves nothing, because an unreadable second
// holder of a listed address cannot be ruled out.
func (p *PebbleStore) rebuildVerifiedEmailIndexLocked(operator []string) (VerifiedEmailBackfillResult, error) {
	res := VerifiedEmailBackfillResult{Full: true}
	users, badKeys, err := p.listUsers()
	if err != nil {
		return res, err
	}
	undecodable := make(map[string]bool, len(badKeys))
	for _, k := range badKeys {
		undecodable[strings.TrimPrefix(k, "u:")] = true
	}
	opSet := operatorSet(operator)
	if len(badKeys) > 0 && len(opSet) > 0 {
		authLog.Warn("verified-email rebuild: %d user row(s) unreadable; the operator list proves nothing until they are fixed", len(badKeys))
		opSet = nil
	}

	// desired: address -> the one user with provenance for it.
	desired := map[string]string{}
	conflicted := map[string]bool{}
	for i := range users {
		u := &users[i]
		lower := strings.ToLower(strings.TrimSpace(u.Email))
		if lower == "" {
			continue
		}
		proven, err := p.emailProvenFor(u, lower, opSet)
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
		if undecodable[id] && !conflicted[lower] {
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
	if err := b.Set([]byte(verifiedEmailIndexKey), []byte(strings.Join(operator, "\n")), nil); err != nil {
		return res, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return res, err
	}
	authLog.Info("verified-email index rebuilt from provenance: %d kept, %d added, %d dropped, %d conflicted",
		res.Kept, res.Added, res.Dropped, res.Conflict)
	return res, nil
}

// reapplyOperatorEmailsLocked brings the index in line with a changed
// operator list. Caller holds inviteMu.
//   - A removed address keeps its key only if its holder still has other
//     provenance; otherwise the key is dropped, so trust does not outlive the
//     list entry that gave it.
//   - An added address gets a key for its one holder that the list proves
//     (active, not self-set; see emailProvenFor). More than one proven
//     holder, any other holder with proof of its own, a key already naming
//     another account, or any unreadable user row is a conflict: nothing is
//     written or overwritten, and a Warn is logged without the address.
func (p *PebbleStore) reapplyOperatorEmailsLocked(old, current []string) (VerifiedEmailBackfillResult, error) {
	var res VerifiedEmailBackfillResult
	oldSet, curSet := operatorSet(old), operatorSet(current)
	users, badKeys, err := p.listUsers()
	if err != nil {
		return res, err
	}
	byID := make(map[string]*User, len(users))
	for i := range users {
		byID[users[i].ID] = &users[i]
	}
	b := p.db.NewBatch()
	defer b.Close()

	for addr := range oldSet {
		if curSet[addr] {
			continue
		}
		v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + addr))
		if err == pebble.ErrNotFound {
			continue
		} else if err != nil {
			return res, err
		}
		holder := byID[string(v)]
		closer.Close()
		keep := false
		if holder != nil && strings.EqualFold(strings.TrimSpace(holder.Email), addr) {
			if keep, err = p.emailProvenFor(holder, addr, nil); err != nil {
				return res, err
			}
		}
		if keep {
			res.Kept++
			continue
		}
		if err := b.Delete([]byte(userEmailIndexPrefix+addr), nil); err != nil {
			return res, err
		}
		res.Dropped++
	}

	for addr := range curSet {
		if oldSet[addr] {
			continue
		}
		if len(badKeys) > 0 {
			authLog.Warn("verified-email index: an operator-listed address was not applied: %d user row(s) unreadable", len(badKeys))
			res.Conflict++
			continue
		}
		// The same rules as the full rebuild: the list proves the address for
		// its one active, not-self-set holder; any other holder with proof of
		// its own, or a key already naming someone else, is a conflict.
		var holders []*User
		for i := range users {
			if strings.EqualFold(strings.TrimSpace(users[i].Email), addr) {
				holders = append(holders, &users[i])
			}
		}
		var eligible []*User
		for _, h := range holders {
			ok, err := p.emailProvenFor(h, addr, curSet)
			if err != nil {
				return res, err
			}
			if ok {
				eligible = append(eligible, h)
			}
		}
		if len(eligible) == 0 {
			continue
		}
		conflict := len(eligible) > 1
		chosen := eligible[0]
		for _, h := range holders {
			if conflict || h.ID == chosen.ID {
				continue
			}
			ok, err := p.emailProvenFor(h, addr, nil)
			if err != nil {
				return res, err
			}
			conflict = ok
		}
		if !conflict {
			if v, closer, err := p.db.Get([]byte(userEmailIndexPrefix + addr)); err == nil {
				conflict = string(v) != chosen.ID
				closer.Close()
				if !conflict {
					res.Kept++
					continue
				}
			} else if err != pebble.ErrNotFound {
				return res, err
			}
		}
		if conflict {
			authLog.Warn("verified-email index: an operator-listed address has more than one proven holder, or is already verified for another account; left as it was")
			res.Conflict++
			continue
		}
		if err := b.Set([]byte(userEmailIndexPrefix+addr), []byte(chosen.ID), nil); err != nil {
			return res, err
		}
		res.Added++
	}
	if err := b.Set([]byte(verifiedEmailIndexKey), []byte(strings.Join(current, "\n")), nil); err != nil {
		return res, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return res, err
	}
	authLog.Info("verified-email index: operator list changed: %d added, %d dropped, %d kept, %d conflicted",
		res.Added, res.Dropped, res.Kept, res.Conflict)
	return res, nil
}

// emailProvenFor reports whether the store holds a record proving lower is
// u's address: an invite grant naming u, a linked OAuth identity of u
// recorded with that email, or lower being on operator (the operator's
// trusted list) while u is active and did not type the address in
// themselves (EmailSource is not "self"). A row written before EmailSource
// existed ("") is accepted for the operator list: before this build no
// self-service path wrote the verified-email index, so the operator listing
// an address a legacy account holds is the operator vouching for that
// account. Each such acceptance is logged with the user id. operator may be
// nil. It does not check for other holders; callers treat two proven holders
// as a conflict.
func (p *PebbleStore) emailProvenFor(u *User, lower string, operator map[string]bool) (bool, error) {
	if operator[lower] && u.Status == "active" && u.EmailSource != EmailSourceSelf {
		if u.EmailSource == "" {
			authLog.Info("verified-email: operator list accepted for a legacy account with no email source (user %s)", u.ID)
		}
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

// RerunVerifiedEmailBackfillForTest runs the full rebuild with the given
// operator list, as the first start of an upgraded store would, whatever the
// sentinel says. Tests in other packages use it; an in-memory store cannot be
// reopened.
func (p *PebbleStore) RerunVerifiedEmailBackfillForTest(operator []string) (VerifiedEmailBackfillResult, error) {
	p.inviteMu.Lock()
	defer p.inviteMu.Unlock()
	list := normalizeOperatorEmails(operator)
	res, err := p.rebuildVerifiedEmailIndexLocked(list)
	if err == nil {
		canon := strings.Join(list, "\n")
		p.operatorApplied.Store(&canon)
	}
	return res, err
}
