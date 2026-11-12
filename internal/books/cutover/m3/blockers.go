// file: internal/books/cutover/m3/blockers.go
// version: 1.0.0
// guid: da34a205-c945-4289-9e76-f79b441ab51b
// last-edited: 2026-11-12

package m3

import (
	"context"
	"errors"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// The cutover preflight (05-storage §5S.3.13 "Cutover blockers", D13): the
// rows that must stop the upgrade instead of being quarantined. It reads HEAD's
// rows through a Reader that cannot write, before any store of core's exists
// to stage into, so "the importer's validation without staging" is the
// importer's own record rules (auth.CheckImport*, which authstore calls too)
// applied to the mapped record, plus what a store would decide from the rows
// it holds, decided here from HEAD's rows: an owner that is stored, a username
// that is free, an (provider, subject) pair no earlier identity holds and a
// token hash no earlier key holds. HEAD can hold the last two (neither
// CreateOAuthIdentity nor CreateAPIKey checks for a twin, and a concurrent
// first sign-in creates a second identity), and the importers refuse both: a
// row the preflight let through would fail the step at every boot, with
// nothing for `storage cutover-blockers` to list. A blocker names the row by
// key and the reason by a fixed class, never by a value; a duplicate names
// the row that holds the value by its ID.

// JournalFamily is the family name of a merge-journal blocker. A merge in
// progress is finished or undone on the pre-freeze release, so
// `storage cutover-blockers` neither fixes nor deletes it.
const JournalFamily = JournalPrefix

// MergeFix is the operator's way out of a merge in progress.
const MergeFix = "finish or undo the merge on the pre-freeze release, then install this release again"

// Preflight lists every blocking row of r, in family order (users, keys,
// identities, merge journals), each family in key order. It writes nothing.
func (m *Migration) Preflight(ctx context.Context, r storage.Reader) ([]datamigrate.Blocker, error) {
	var out []datamigrate.Blocker
	add := func(key, family, reason, fix string) {
		out = append(out, datamigrate.Blocker{Step: m.cfg.StepID, Key: []byte(key), Family: family, Reason: reason, Fix: fix})
	}
	roleRows, err := scanRows(ctx, r, prefixRole)
	if err != nil {
		return nil, err
	}
	outcomes := map[string]roleVerdict{}
	byName := map[rbac.RoleName][]rbac.Permission{}
	// taken is the step's rule (pullRoles): a name an earlier role of this pass
	// holds. The step also asks core's role store, which holds none of these
	// names before the cutover starts, and an importer's own refusal of a role
	// (a quarantine) is not known from HEAD's rows; both leave this set as the
	// step's, never larger.
	taken := func(name rbac.RoleName) bool { _, ok := byName[name]; return ok }
	for _, kv := range roleRows {
		// A role that does not decode is quarantined by the step, never a blocker.
		if h, derr := decodeRole(string(kv.Key), kv.Value); derr == nil {
			v := m.verdictOf(h, taken)
			outcomes[h.ID] = v
			if v.kind == verdictImport {
				byName[v.name] = v.perms
			}
		}
	}
	lookup := func(_ context.Context, name rbac.RoleName) ([]rbac.Permission, bool, error) {
		perms, ok := byName[name]
		return perms, ok, nil
	}

	userRows, err := scanRows(ctx, r, prefixUser)
	if err != nil {
		return nil, err
	}
	users := map[string][]rbac.RoleName{}
	holders := map[string]string{} // folded username -> the user ID that holds it
	for _, kv := range userRows {
		key := string(kv.Key)
		h, derr := decodeUser(key, kv.Value)
		if derr != nil {
			add(key, prefixUser, "the row does not decode: "+derr.Error(), fixHint)
			continue
		}
		roles, _ := roleList(h.Roles, outcomes, m.seeds)
		u, hash, _ := mapUser(h, roles)
		if err := auth.CheckImportUser(&u, hash); err != nil {
			add(key, prefixUser, "the importer rejects the row: "+redact(err), fixHint)
			continue
		}
		fold := auth.FoldUsername(u.Username)
		if other, dup := holders[fold]; dup {
			add(key, prefixUser, "the importer rejects the row: the username is held by user "+other, fixHint)
			continue
		}
		holders[fold] = h.ID
		users[h.ID] = roles
	}

	keyRows, err := scanRows(ctx, r, prefixKey)
	if err != nil {
		return nil, err
	}
	holdersByHash := map[[32]byte]string{} // token hash -> the key ID that holds it
	for _, kv := range keyRows {
		key := string(kv.Key)
		h, derr := decodeKey(key, kv.Value)
		if derr != nil {
			add(key, prefixKey, "the row does not decode: "+derr.Error(), fixHint)
			continue
		}
		roles, ok := users[h.UserID]
		if !ok {
			add(key, prefixKey, "the key's owner is not stored", fixHint)
			continue
		}
		owner, err := m.ownerPerms(ctx, roles, lookup)
		if err != nil {
			return nil, err
		}
		scopes, _, err := m.keyScopes(h.Scopes, owner)
		if err != nil {
			add(key, prefixKey, "the key's scopes are refused by the catalog", fixHint)
			continue
		}
		k, hash, _, err := m.mapKey(h, scopes)
		if err != nil {
			add(key, prefixKey, err.Error(), fixHint)
			continue
		}
		if err := auth.CheckImportAPIKey(&k, hash); err != nil {
			add(key, prefixKey, "the importer rejects the row: "+redact(err), fixHint)
			continue
		}
		// authstore's stageNewKey refuses a hash that is already stored, and a
		// twin staged in the same chunk is a double write.
		if other, dup := holdersByHash[hash]; dup {
			add(key, prefixKey, "the importer rejects the row: the token hash is held by key "+other, fixHint)
			continue
		}
		holdersByHash[hash] = h.ID
	}

	identRows, err := scanRows(ctx, r, prefixIdentity)
	if err != nil {
		return nil, err
	}
	holdersByIdentity := map[[2]string]string{} // (provider, subject) -> the identity ID that holds it
	for _, kv := range identRows {
		key := string(kv.Key)
		h, derr := decodeIdentity(key, kv.Value)
		if derr != nil {
			add(key, prefixIdentity, "the row does not decode: "+derr.Error(), fixHint)
			continue
		}
		id := mapIdentity(h)
		if err := auth.CheckImportIdentity(id); err != nil {
			add(key, prefixIdentity, "the importer rejects the row: "+redact(err), fixHint)
			continue
		}
		if _, ok := users[h.UserID]; !ok {
			add(key, prefixIdentity, "the identity's user is not stored", fixHint)
			continue
		}
		// authstore's ImportIdentity refuses a (provider, subject) pair that is
		// already stored, and a twin staged in the same chunk is a double write.
		pair := [2]string{id.Provider, id.Subject}
		if other, dup := holdersByIdentity[pair]; dup {
			add(key, prefixIdentity, "the importer rejects the row: its provider and subject are held by identity "+other, fixHint)
			continue
		}
		holdersByIdentity[pair] = h.ID
	}

	journals, err := scanRows(ctx, r, JournalPrefix)
	if err != nil {
		return nil, err
	}
	for _, kv := range journals {
		key := string(kv.Key)
		j, derr := decodeJournal(kv.Value)
		switch {
		case derr != nil:
			add(key, JournalFamily, "the journal does not decode, so a merge in progress cannot be ruled out: "+derr.Error(), MergeFix)
		case j.status == "applying" || j.status == "undoing":
			add(key, JournalFamily, "a user merge is in progress ("+j.status+")", MergeFix)
		}
	}
	return out, nil
}

// primaryFamily is the identity family a blocker's key belongs to, "" for a
// key fix and delete do not act on.
func primaryFamily(key []byte) string {
	for _, p := range []string{prefixUser, prefixKey, prefixIdentity} {
		if strings.HasPrefix(string(key), p) && len(key) > len(p) {
			return p
		}
	}
	return ""
}

// FixBlocker stages the operator's repaired value as the row key names, when
// the value is a row of that family naming that key's ID. Whether the row then
// converts is the preflight's to say: the caller runs it over the state the
// fix leaves and commits only if the key is no longer named
// (core/kernel's cutover-blockers command).
func (m *Migration) FixBlocker(ctx context.Context, b *storage.Batch, key, value []byte) error {
	var derr error
	switch primaryFamily(key) {
	case prefixUser:
		_, derr = decodeUser(string(key), value)
	case prefixKey:
		_, derr = decodeKey(string(key), value)
	case prefixIdentity:
		_, derr = decodeIdentity(string(key), value)
	default:
		return errors.New("m3: only a user, an API key or an OAuth identity row can be fixed")
	}
	if derr != nil {
		return errors.New("m3: the replacement is not a row of that family naming that key: " + derr.Error())
	}
	return b.Set(key, value)
}

// DeleteBlocker stages the deletion of the row key names and the rows that go
// with it, and returns the IDs it removed. A user takes its keys, its OAuth
// identities and their index rows; a key or an identity takes its own. The
// books rows that still name a removed user are the referential pass's to count
// (Verify's OrphanedRefs), never a failure.
func (m *Migration) DeleteBlocker(ctx context.Context, b *storage.Batch, key []byte) ([]string, error) {
	fam := primaryFamily(key)
	if fam == "" {
		return nil, errors.New("m3: only a user, an API key or an OAuth identity row can be deleted")
	}
	id := strings.TrimPrefix(string(key), fam)
	var ids []string
	switch fam {
	case prefixKey:
		ids = append(ids, id)
		if err := m.deleteKeyRows(ctx, b, id); err != nil {
			return nil, err
		}
	case prefixIdentity:
		ids = append(ids, id)
		if err := m.deleteIdentityRows(ctx, b, id); err != nil {
			return nil, err
		}
	default:
		ids = append(ids, id)
		if err := m.deleteUserRows(ctx, b, id, &ids); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
