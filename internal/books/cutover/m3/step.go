// file: internal/books/cutover/m3/step.go
// version: 1.0.0
// guid: fe82b6b9-8526-49a6-ab33-4e3fd81d1af5
// last-edited: 2026-11-12

package m3

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// LogJournalRewritten is the message of the one line the step logs for each
// merge journal whose invite tokens it hashed (the journal's ID, never a token).
const LogJournalRewritten = "m3: a merge journal's invite tokens were hashed"

// Outcomes M3 counts beside the runner's "quarantined".
const (
	OutcomeImported  = "imported"          // a record staged into core's stores
	OutcomeDeleted   = "deleted"           // removed by rule: sessions, invites, seed roles, the bootstrap hash
	OutcomeIndex     = "index-dropped"     // an idx: row, derived data core rebuilds in its own form
	OutcomeRewritten = "journal-rewritten" // a merge journal with its invite tokens hashed
	OutcomeReported  = "reported"          // something the step names in its log: an unknown atom, an odd status
)

// How the step runs (05-authn §11; 05-storage §5S.3.13).
//
// Source is the identity families and their idx: children, scanned in byte
// order: apikey: first, u: last. The runner steps a chunk's items into one
// batch, so the first item of each chunk pulls the chunk's owners forward:
// every role, then every user, then the rewrites outside Source (journals,
// bootstrap hash, expiry flag), staged into the chunk's batch ahead of the
// cursor (RewritesInPlace) with their rows deleted, so the runner skips them
// when it reaches them. A key or an identity is then imported against an owner
// staged earlier in its chunk or committed by an earlier one: the importers'
// batch is indexed (storagemigrate), which is what lets an owner and its keys
// share a chunk. "Users first" is by construction: the first chunk commits
// every role and user with whatever items fit beside them, a kill after it
// leaves natives under c/auth/ and the rest of the keys in HEAD's rows, and
// the boot that resumes skips the preflight (the cutover has started) and
// imports each remaining key against its committed owner.
//
// Parallelism is sequential: tens to hundreds of identity rows, and the order
// is the point (a key follows its owner; no row is imported twice).
//
// No identity row is quarantined or dropped: a user, key or identity the
// importer refuses fails the chunk with storage.ErrCutoverBlocked (the
// defensive check of the preflight's gate); roles and the invite allow-list
// are quarantine-and-continue.
//
// One unit is the scope of one chunk's batch: a record exists in HEAD's row or
// in core's, never both, because the import and the row's delete commit
// together with the cursor.

// Step is the data migration's Step.
func (m *Migration) Step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	if err := m.begin(ctx, w); err != nil {
		return err
	}
	key := string(kv.Key)
	switch {
	case strings.HasPrefix(key, prefixKey):
		return m.stepKey(ctx, kv, w)
	case strings.HasPrefix(key, prefixIdentity):
		return m.stepIdentity(ctx, kv, w)
	case strings.HasPrefix(key, prefixInviteAllow):
		return m.stepInviteAllow(ctx, kv, w)
	case strings.HasPrefix(key, prefixInvite), strings.HasPrefix(key, prefixSession), key == bootstrapHashKey, key == bootstrapExpiresKey:
		// Deleted by rule (ownership table rows 4a, 6 and 8): an open invite is
		// re-sent, a web user signs in once, and core auth mints its own
		// bootstrap token.
		w.Count(OutcomeDeleted)
		return w.Delete(kv.Key)
	case strings.HasPrefix(key, prefixUser), strings.HasPrefix(key, prefixRole):
		// begin pulled every one of them into this chunk, with its delete
		// staged. A chunk's first item can be one of them (a store with no row
		// that sorts before the first role or user): begin has just handled it,
		// and there is nothing left to do. A row begin did not see appeared
		// after the pull, which nothing writes.
		if _, ok := m.chunkPulled[key]; ok {
			return nil
		}
		return fmt.Errorf("m3: the row %q was not pulled forward with its chunk", key)
	}
	for _, p := range []string{idxUser, idxRoleName, idxKey, idxInvite, idxOAuth, idxSessByUser} {
		if strings.HasPrefix(key, p) {
			w.Count(OutcomeIndex)
			return w.Delete(kv.Key)
		}
	}
	return fmt.Errorf("m3: the row %q is outside the step's source", key)
}

// begin runs a chunk's pulled-forward work once, at the chunk's first item.
func (m *Migration) begin(ctx context.Context, w *datamigrate.StepWriter) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chunk == w {
		return nil
	}
	m.chunkUsers = map[auth.UserID][]rbac.RoleName{}
	m.chunkRoles = map[rbac.RoleName][]rbac.Permission{}
	m.chunkNames = map[string]string{}
	m.chunkPulled = map[string]struct{}{}
	outcomes, err := m.pullRoles(ctx, w)
	if err != nil {
		return err
	}
	if err := m.pullUsers(ctx, w, outcomes); err != nil {
		return err
	}
	if err := m.rewriteOutside(ctx, w); err != nil {
		return err
	}
	m.chunk = w
	return nil
}

// scan reads every row under prefix from HEAD's legacy range, cloned.
func (m *Migration) scan(ctx context.Context, prefix string) ([]storage.KV, error) {
	return scanRows(ctx, m.legacy(), prefix)
}

// scanRows reads every row under prefix through r, cloned: the identity
// families are tens to hundreds of rows.
func scanRows(ctx context.Context, r storage.Reader, prefix string) ([]storage.KV, error) {
	var out []storage.KV
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte(prefix), End: prefixEnd(prefix)}) {
		if err != nil {
			return nil, err
		}
		out = append(out, kv.Clone())
	}
	return out, nil
}

var quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// redact is a refusal of auth.CheckImport* without anything it quotes: a
// status or a scope in the text is a value, and a blocker names a row, never
// one. Those messages name a record by ID and quote every value; text from a
// store's own importer is not covered (see refused).
func redact(err error) string { return quoted.ReplaceAllString(err.Error(), `"…"`) }

// refusedClass is the reason of a blocker the step raises from an importer's
// refusal. It is a fixed class, not the importer's text: a store's refusal can
// name a value without quoting it (an OAuth subject, a username), and a
// blocker carries a key and a class, never a value. The preflight has already
// given the precise reason for every rule it can decide from HEAD's rows.
const refusedClass = "the importer rejects the row"

// refused turns an importer's refusal of an identity row into the blocker
// that stops the upgrade; any other error is the step's own failure.
func (m *Migration) refused(err error, key, family string) error {
	if errors.Is(err, auth.ErrImportRejected) {
		return m.blocked(key, family, refusedClass, fixHint)
	}
	return err
}

// ---- roles ----

// pullRoles imports every custom role, deletes the seed roles' rows and
// quarantines the roles it cannot take (a role is never a blocker), and
// returns the verdict of every role row by HEAD ID.
func (m *Migration) pullRoles(ctx context.Context, w *datamigrate.StepWriter) (map[string]roleVerdict, error) {
	rows, err := m.scan(ctx, prefixRole)
	if err != nil {
		return nil, err
	}
	b := w.Batch()
	outcomes := map[string]roleVerdict{}
	taken := func(name rbac.RoleName) bool {
		if _, ok := m.chunkRoles[name]; ok {
			return true
		}
		_, err := m.cfg.Native.GetRole(ctx, name)
		return err == nil
	}
	for _, kv := range rows {
		key := string(kv.Key)
		m.chunkPulled[key] = struct{}{}
		h, derr := decodeRole(key, kv.Value)
		if derr != nil {
			m.log.Warn("m3: a role row does not decode and is quarantined", "step", m.cfg.StepID, "key", key, "reason", derr.Error())
			if err := w.Quarantine(kv.Key, kv.Value, "the role row does not decode: "+derr.Error()); err != nil {
				return nil, err
			}
			continue
		}
		v := m.verdictOf(h, taken)
		if len(v.unknown) > 0 {
			w.Count(OutcomeReported)
			m.log.Warn("m3: a role names atoms outside the atom table; they grant nothing", "step", m.cfg.StepID, "role", h.ID, "atoms", boundedAtoms(v.unknown))
		}
		switch v.kind {
		case verdictSeed:
			w.Count(OutcomeDeleted)
			if err := w.Delete(kv.Key); err != nil {
				return nil, err
			}
		case verdictQuarantine:
			m.log.Warn("m3: a role is quarantined", "step", m.cfg.StepID, "role", h.ID, "reason", v.reason)
			if err := w.Quarantine(kv.Key, kv.Value, v.reason); err != nil {
				return nil, err
			}
		default:
			switch err := m.cfg.Roles.ImportRole(b, nativeRole(h, v)); {
			case errors.Is(err, rbac.ErrImportRejected):
				v = roleVerdict{kind: verdictQuarantine, name: v.name, reason: "rbac refuses the role"}
				m.log.Warn("m3: a role is quarantined", "step", m.cfg.StepID, "role", h.ID, "reason", v.reason)
				if err := w.Quarantine(kv.Key, kv.Value, v.reason); err != nil {
					return nil, err
				}
			case err != nil:
				return nil, err
			default:
				if err := m.auditRole(ctx, b, v.name); err != nil {
					return nil, err
				}
				m.chunkRoles[v.name] = v.perms
				w.Count(OutcomeImported)
				if err := w.Delete(kv.Key); err != nil {
					return nil, err
				}
			}
		}
		outcomes[h.ID] = v
	}
	return outcomes, nil
}

// auditRole records core.role.create (detail imported: true) in the chunk's batch.
func (m *Migration) auditRole(ctx context.Context, b *storage.Batch, name rbac.RoleName) error {
	if m.cfg.Audit == nil {
		return nil
	}
	return m.cfg.Audit.RecordIn(ctx, b, audit.Event{
		Action: "core.role.create", Outcome: audit.Success,
		Target:  entity.Ref{Module: "core", Kind: "role", ID: string(name)},
		Details: map[string]any{"imported": true},
	})
}

// roleList maps a HEAD user's role IDs to native names. A seed role keeps its
// ID; a custom role becomes the name it was imported under; an entry naming a
// role that was quarantined, or that no row defines, is dropped.
func roleList(ids []string, outcomes map[string]roleVerdict, seeds map[string]SeedRole) (roles []rbac.RoleName, dropped int) {
	for _, id := range ids {
		var name rbac.RoleName
		if s, ok := seeds[id]; ok {
			name = rbac.RoleName(s.ID)
		} else if v, ok := outcomes[id]; ok && v.kind == verdictImport {
			name = v.name
		}
		switch {
		case name == "":
			dropped++
		case !containsRole(roles, name):
			roles = append(roles, name)
		}
	}
	return roles, dropped
}

func containsRole(rs []rbac.RoleName, r rbac.RoleName) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// ---- users ----

// pullUsers imports every user row of HEAD, deleting each row with its import.
func (m *Migration) pullUsers(ctx context.Context, w *datamigrate.StepWriter, outcomes map[string]roleVerdict) error {
	rows, err := m.scan(ctx, prefixUser)
	if err != nil {
		return err
	}
	b := w.Batch()
	for _, kv := range rows {
		key := string(kv.Key)
		m.chunkPulled[key] = struct{}{}
		h, derr := decodeUser(key, kv.Value)
		if derr != nil {
			return m.blocked(key, prefixUser, "the row does not decode: "+derr.Error(), fixHint)
		}
		roles, dropped := roleList(h.Roles, outcomes, m.seeds)
		u, hash, reported := mapUser(h, roles)
		fold := auth.FoldUsername(u.Username)
		if other, dup := m.chunkNames[fold]; dup && fold != "" {
			return m.blocked(key, prefixUser, "the importer rejects the row: the username is held by user "+other, fixHint)
		}
		if err := m.cfg.Auth.ImportUser(b, &u, hash); err != nil {
			return m.refused(err, key, prefixUser)
		}
		if err := w.Delete(kv.Key); err != nil {
			return err
		}
		w.Count(OutcomeImported)
		m.chunkUsers[u.ID] = roles
		m.chunkNames[fold] = h.ID
		if reported != "" {
			w.Count(OutcomeReported)
			m.log.Warn("m3: a user's status is not one HEAD defines; imported disabled", "step", m.cfg.StepID, "user", h.ID, "status", reported)
		}
		if dropped > 0 {
			w.Count(OutcomeReported)
			m.log.Warn("m3: role list entries naming a quarantined or undefined role were dropped", "step", m.cfg.StepID, "user", h.ID, "dropped", dropped)
		}
	}
	return nil
}

// ownerRoles is the native role list of an owner: the user this chunk staged,
// or one an earlier chunk committed. ok is false when the user is in neither.
func (m *Migration) ownerRoles(ctx context.Context, id auth.UserID) ([]rbac.RoleName, bool, error) {
	if roles, ok := m.chunkUsers[id]; ok {
		return roles, true, nil
	}
	u, err := m.cfg.Native.GetUser(ctx, id)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return u.Roles, true, nil
}

// roleLookup is the native permissions of a custom role: one this chunk
// imported, or one a committed chunk did. A role that is in neither grants
// nothing.
func (m *Migration) roleLookup(ctx context.Context, name rbac.RoleName) ([]rbac.Permission, bool, error) {
	if perms, ok := m.chunkRoles[name]; ok {
		return perms, true, nil
	}
	r, err := m.cfg.Native.GetRole(ctx, name)
	switch {
	case errors.Is(err, rbac.ErrUnknownRole):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return r.Permissions(), true, nil
}

// ---- keys, identities, the invite allow-list ----

func (m *Migration) stepKey(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	key := string(kv.Key)
	h, derr := decodeKey(key, kv.Value)
	if derr != nil {
		return m.blocked(key, prefixKey, "the row does not decode: "+derr.Error(), fixHint)
	}
	roles, ok, err := m.ownerRoles(ctx, auth.UserID(h.UserID))
	switch {
	case err != nil:
		return err
	case !ok:
		return m.blocked(key, prefixKey, "the key's owner is not stored", fixHint)
	}
	owner, err := m.ownerPerms(ctx, roles, m.roleLookup)
	if err != nil {
		return err
	}
	scopes, unknown, err := m.keyScopes(h.Scopes, owner)
	if err != nil {
		return m.blocked(key, prefixKey, "the key's scopes are refused by the catalog", fixHint)
	}
	k, hash, notes, err := m.mapKey(h, scopes)
	if err != nil {
		return m.blocked(key, prefixKey, err.Error(), fixHint)
	}
	notes.unknown = unknown
	if err := m.cfg.Auth.ImportAPIKey(w.Batch(), &k, hash); err != nil {
		return m.refused(err, key, prefixKey)
	}
	if err := w.Delete(kv.Key); err != nil {
		return err
	}
	w.Count(OutcomeImported)
	m.reportKey(w, h.ID, notes)
	return nil
}

// reportKey names what an imported key did that its owner may not expect.
func (m *Migration) reportKey(w *datamigrate.StepWriter, id string, n keyNotes) {
	if n.oddStatus != "" {
		w.Count(OutcomeReported)
		m.log.Warn("m3: a key's status is not one HEAD defines; imported disabled", "step", m.cfg.StepID, "key", id, "status", n.oddStatus)
	}
	if len(n.unknown) > 0 {
		w.Count(OutcomeReported)
		m.log.Warn("m3: a key names atoms outside the atom table; they grant nothing", "step", m.cfg.StepID, "key", id, "atoms", boundedAtoms(n.unknown))
	}
	if n.stamped {
		m.log.Info("m3: a key with no expiry got the one-shot expiry stamp", "step", m.cfg.StepID, "key", id)
	}
}

func (m *Migration) stepIdentity(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	key := string(kv.Key)
	h, derr := decodeIdentity(key, kv.Value)
	if derr != nil {
		return m.blocked(key, prefixIdentity, "the row does not decode: "+derr.Error(), fixHint)
	}
	switch _, ok, err := m.ownerRoles(ctx, auth.UserID(h.UserID)); {
	case err != nil:
		return err
	case !ok:
		return m.blocked(key, prefixIdentity, "the identity's user is not stored", fixHint)
	}
	if err := m.cfg.Auth.ImportIdentity(w.Batch(), mapIdentity(h)); err != nil {
		return m.refused(err, key, prefixIdentity)
	}
	if err := w.Delete(kv.Key); err != nil {
		return err
	}
	w.Count(OutcomeImported)
	return nil
}

// stepInviteAllow imports an allow-list row (its key is the folded address).
// A row that does not decode or that the importer refuses is quarantined: the
// allow-list is not an identity.
func (m *Migration) stepInviteAllow(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	key := string(kv.Key)
	if _, derr := decodeInviteGrant(kv.Value); derr != nil {
		m.log.Warn("m3: an invite allow-list row does not decode and is quarantined", "step", m.cfg.StepID, "reason", derr.Error())
		return w.Quarantine(kv.Key, kv.Value, "the allow-list row does not decode: "+derr.Error())
	}
	switch err := m.cfg.Auth.ImportInviteAllow(w.Batch(), strings.TrimPrefix(key, prefixInviteAllow)); {
	case errors.Is(err, auth.ErrImportRejected):
		m.log.Warn("m3: an invite allow-list row is refused by the importer and quarantined", "step", m.cfg.StepID)
		return w.Quarantine(kv.Key, kv.Value, "the importer rejects the allow-list row")
	case err != nil:
		return err
	}
	if err := w.Delete(kv.Key); err != nil {
		return err
	}
	w.Count(OutcomeImported)
	return nil
}

// ---- outside Source ----

// rewriteOutside runs the rewrites of rows that are not in Source (ownership
// table rows 16 and 19): each merge journal once with its tokens hashed, and
// HEAD's one-shot expiry flag deleted (the stamp is this step's). Each is
// idempotent: a journal with no raw token is not written, and a key that is
// gone is not deleted.
func (m *Migration) rewriteOutside(ctx context.Context, w *datamigrate.StepWriter) error {
	rows, err := m.scan(ctx, JournalPrefix)
	if err != nil {
		return err
	}
	for _, kv := range rows {
		out, changed, err := rewriteJournal(kv.Value)
		if err != nil {
			// The preflight refuses a journal that does not decode; one that
			// stopped decoding since is left as it is and reported.
			w.Count(OutcomeReported)
			m.log.Warn("m3: a merge journal does not decode and is kept as it is", "step", m.cfg.StepID, "key", string(kv.Key), "reason", err.Error())
			continue
		}
		if !changed {
			continue
		}
		if err := w.Set(kv.Key, out); err != nil {
			return err
		}
		w.Count(OutcomeRewritten)
		m.log.Info(LogJournalRewritten, "step", m.cfg.StepID, "journal", strings.TrimPrefix(string(kv.Key), JournalPrefix))
	}
	switch _, err := m.legacy().Get(ctx, []byte(expiryDoneKey)); {
	case errors.Is(err, storage.ErrNotFound):
		return nil
	case err != nil:
		return err
	}
	if err := w.Delete([]byte(expiryDoneKey)); err != nil {
		return err
	}
	w.Count(OutcomeDeleted)
	return nil
}
