// file: internal/books/cutover/identity_doubles_test.go
// version: 1.0.0
// guid: 17a3e05d-266c-4355-930b-9b2f6f7f28c8
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Doubles of core's auth and rbac importers for the M3 rig. This module cannot
// import core/internal/authstore or core/internal/rbacstore, so these are
// written to the same contract and enforce the same rules the real stores do:
// auth.CheckImport* first, a user that is committed or a username that is
// taken refused from the COMMITTED state only (so a username staged earlier in
// the same chunk collides at commit, not at stage), an owner read through the
// batch (GetForUpdate, which sees a staged owner only on an INDEXED batch),
// and for a role the name grammar, the custom origin, a non-empty permission
// set the catalog holds and never core:admin. Each write stages an Expect on
// the record's absent state, so a second import of the same record fails at
// commit. The schema-faithful run over the real stores is F4-08's.

const (
	kindUser     = "user"
	kindUserName = "uname"
	kindKey      = "key"
	kindKeyHash  = "keyhash"
	kindIdent    = "ident"
	kindAllow    = "allow"
	kindRole     = "role"
	kindEvent    = "event"
	kindSealed   = "sealed"
)

func dkey(kind string, parts ...string) []byte {
	segs := []keys.Seg{keys.Str(kind)}
	for _, p := range parts {
		segs = append(segs, keys.Str(p))
	}
	return keys.Join(segs...)
}

type storedUser struct {
	User auth.User
	Hash string
}

type storedKey struct {
	Key  auth.APIKey
	Hash string
}

// idAuth is the auth.Importer double over a keyspace.
type idAuth struct{ ks storage.Keyspace }

func (s *idAuth) committed(key []byte) ([]byte, error) {
	raw, err := s.ks.Get(context.Background(), key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return raw, err
}

// errDoubleWrite is authstore's ErrDoubleWrite: a second write to a record one
// batch already changed. It is a programming error, not a rejection of the row.
var errDoubleWrite = errors.New("a record is written twice in one batch")

func (s *idAuth) set(b *storage.Batch, key, raw []byte) error {
	b.Bind(s.ks)
	if b.Indexed() {
		// authstore's refuseRewrite: an indexed batch reads its own writes, so a
		// record this batch already staged is a second write to it (two keys
		// with one token hash, two identities with one (provider, subject)).
		switch _, err := b.GetForUpdate(context.Background(), s.ks, key); {
		case err == nil:
			return fmt.Errorf("%w: %d-byte key", errDoubleWrite, len(key))
		case !errors.Is(err, storage.ErrNotFound):
			return err
		}
	}
	if err := b.Expect(s.ks, key, nil); err != nil {
		return err
	}
	return b.In(s.ks).Set(key, raw)
}

func rejectf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", auth.ErrImportRejected, fmt.Sprintf(format, a...))
}

func (s *idAuth) ownerPresent(b *storage.Batch, id auth.UserID) error {
	b.Bind(s.ks)
	_, err := b.GetForUpdate(context.Background(), s.ks, dkey(kindUser, string(id)))
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("owner %s is not stored", id)
	}
	return err
}

func (s *idAuth) ImportUser(b *storage.Batch, u *auth.User, hash string) error {
	if err := auth.CheckImportUser(u, hash); err != nil {
		return err
	}
	if cur, err := s.committed(dkey(kindUser, string(u.ID))); err != nil || cur != nil {
		return rejectf("user %s is already stored", u.ID)
	}
	fold := auth.FoldUsername(u.Username)
	if cur, err := s.committed(dkey(kindUserName, fold)); err != nil || cur != nil {
		return rejectf("username %q is taken", u.Username)
	}
	raw, err := json.Marshal(storedUser{User: *u, Hash: hash})
	if err != nil {
		return rejectf("%v", err)
	}
	if err := s.set(b, dkey(kindUserName, fold), []byte(u.ID)); err != nil {
		return rejectf("%v", err)
	}
	return s.set(b, dkey(kindUser, string(u.ID)), raw)
}

func (s *idAuth) ImportAPIKey(b *storage.Batch, k *auth.APIKey, sum [32]byte) error {
	if err := auth.CheckImportAPIKey(k, sum); err != nil {
		return err
	}
	in := *k
	if !in.RevokedAt.IsZero() {
		in.Status = auth.KeyRevoked
	}
	if err := s.ownerPresent(b, in.Owner); err != nil {
		return rejectf("apikey %s: %v", k.ID, err)
	}
	h := hex.EncodeToString(sum[:])
	if cur, err := s.committed(dkey(kindKeyHash, h)); err != nil || cur != nil {
		return rejectf("apikey %s: the token hash is already stored", k.ID)
	}
	raw, err := json.Marshal(storedKey{Key: in, Hash: h})
	if err != nil {
		return rejectf("%v", err)
	}
	if err := s.set(b, dkey(kindKeyHash, h), []byte(in.ID)); err != nil {
		return rejectf("%v", err)
	}
	return s.set(b, dkey(kindKey, in.ID), raw)
}

func (s *idAuth) ImportIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	if err := auth.CheckImportIdentity(id); err != nil {
		return err
	}
	if err := s.ownerPresent(b, id.UserID); err != nil {
		return rejectf("identity of provider %s: %v", id.Provider, err)
	}
	// authstore's ImportIdentity: a (provider, subject) that is already stored.
	if cur, err := s.committed(dkey(kindIdent, id.Provider, id.Subject)); err != nil || cur != nil {
		return rejectf("an identity of provider %s with this subject is already stored", id.Provider)
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return rejectf("%v", err)
	}
	return s.set(b, dkey(kindIdent, id.Provider, id.Subject), raw)
}

func (s *idAuth) ImportInviteAllow(b *storage.Batch, email string) error {
	fold := auth.NormalizeEmail(email)
	if fold == "" {
		return rejectf("empty e-mail")
	}
	cur, err := s.committed(dkey(kindAllow, fold))
	if err != nil {
		return rejectf("%v", err)
	}
	if bytes.Equal(cur, []byte(fold)) {
		return nil
	}
	return s.set(b, dkey(kindAllow, fold), []byte(fold))
}

func (s *idAuth) GetUser(ctx context.Context, id auth.UserID) (*auth.User, error) {
	raw, err := s.ks.Get(ctx, dkey(kindUser, string(id)))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, auth.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var su storedUser
	if err := json.Unmarshal(raw, &su); err != nil {
		return nil, err
	}
	return &su.User, nil
}

// idRoles is the rbac.Importer double.
type idRoles struct {
	ks  storage.Keyspace
	cat *rbac.Catalog
}

func (s *idRoles) ImportRole(b *storage.Batch, r *rbac.Role) error {
	reject := func(why error) error { return fmt.Errorf("%w: %s: %w", rbac.ErrImportRejected, r.Name, why) }
	if err := rbac.ValidateCustomRoleName(r.Name); err != nil {
		return reject(err)
	}
	if r.Origin != rbac.OriginCustom || len(r.SeedPermissions) > 0 || r.SeedDigest != "" || r.Locked {
		return reject(errors.New("an imported role is a plain custom role"))
	}
	if len(r.Added) == 0 {
		return reject(errors.New("an imported role holds at least one permission"))
	}
	if err := rbac.CheckRolePermissions(s.cat, r.Added); err != nil {
		return reject(err)
	}
	if rbac.GrantsAdmin(r.Added) {
		return reject(errors.New("core:admin is never imported"))
	}
	key := dkey(kindRole, string(r.Name))
	if cur, err := s.ks.Get(context.Background(), key); err == nil && cur != nil {
		return reject(rbac.ErrRoleExists)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return reject(err)
	}
	b.Bind(s.ks)
	if err := b.Expect(s.ks, key, nil); err != nil {
		return reject(err)
	}
	return b.In(s.ks).Set(key, raw)
}

func (s *idRoles) GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	raw, err := s.ks.Get(ctx, dkey(kindRole, string(name)))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", rbac.ErrUnknownRole, name)
	}
	if err != nil {
		return nil, err
	}
	var r rbac.Role
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// idAudit is the audit.Logger double: RecordIn stages one row in the chunk's
// batch, so a crash that loses the chunk loses its audit rows with it.
type idAudit struct {
	ks storage.Keyspace
}

func (a *idAudit) RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error {
	raw, err := json.Marshal(struct {
		Action, Target string
		Details        map[string]any
	}{e.Action, e.Target.ID, e.Details})
	if err != nil {
		return err
	}
	b.Bind(a.ks)
	return b.In(a.ks).Set(dkey(kindEvent, e.Action, e.Target.ID), raw)
}

func (a *idAudit) Record(context.Context, audit.Event) error { return errors.New("not used by M3") }
func (a *idAudit) Emit(context.Context, audit.Event)         {}

// idSealer is the storage.QuarantineSealer double: Seal stages the sealed row's
// plaintext in the chunk's batch, so a crash that loses the chunk loses the
// sealed copy with its quarantine record (the real adapter over the keyring's
// importer stages the same way). The record of a sealed row carries only the
// secret's name.
type idSealer struct {
	ks storage.Keyspace
}

func (s *idSealer) Seal(_ context.Context, b *storage.Batch, name string, raw []byte) error {
	b.Bind(s.ks)
	return b.In(s.ks).Set(dkey(kindSealed, name), bytes.Clone(raw))
}

// dumpDouble reads every row of ks into a map keyed by its decoded segments.
func dumpDouble(t testing.TB, ctx context.Context, ks storage.Keyspace) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for kv, err := range ks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		out[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	return out
}
