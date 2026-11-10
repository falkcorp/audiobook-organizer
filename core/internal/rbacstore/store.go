// file: core/internal/rbacstore/store.go
// version: 1.1.0
// guid: 6f1c9a3e-28d4-4b7e-a5f0-93e1d27c4b85
// last-edited: 2026-11-10

// Package rbacstore is core's one implementation of rbac.Store and
// rbac.Importer (05-rbac §3.4, §6.3 "Native store"): roles kept in the core
// keyspace c/rbac/ at role/<name> as codec.JSON[rbac.Role](1), every field of
// the role. It implements rbac (layer L4, 03 R4) and is imported only by the
// kernel and by tests. It holds no cache and no lock: every read goes to the
// keyspace, and every write stages into the caller's batch with an Expect on
// the stored record, so the batch's Commit fails with storage.ErrConflict when
// another writer got there first.
package rbacstore

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the c/rbac/ keyspace.
const Owner storage.Owner = "core/rbac"

// roleSeg is the first key segment of every role row.
const roleSeg = "role"

// StorageSpec is the c/rbac/ spec the kernel registers for Owner: a core
// keyspace whose role rows are Serialized, so every write to one carries an
// Expect (05-storage R6-1).
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{string(rolePrefix())}}
}

// rolePrefix is the relative prefix of every role row.
func rolePrefix() []byte { return keys.Prefix(keys.Str(roleSeg)) }

// roleKey is the relative key of role name.
func roleKey(name rbac.RoleName) []byte { return keys.Join(keys.Str(roleSeg), keys.Str(string(name))) }

// roleCodec is the stored form of a role (05-rbac §6.3).
func roleCodec() codec.Codec[rbac.Role] { return codec.JSON[rbac.Role](1) }

// Options is what New takes beyond the keyspace.
type Options struct {
	// Catalog is the scope catalog ImportRole checks an imported role's
	// permissions against. Nil refuses every import (fail closed): the
	// importer cannot tell a known scope from an unknown one without it.
	Catalog *rbac.Catalog
	// Now stamps an imported role's CreatedAt and UpdatedAt when the caller
	// left them zero; nil is time.Now.
	Now func() time.Time
}

// Store is the native role store. It implements rbac.Store and rbac.Importer.
type Store struct {
	ks  storage.Keyspace
	cat *rbac.Catalog
	now func() time.Time
}

var (
	_ rbac.Store    = (*Store)(nil)
	_ rbac.Importer = (*Store)(nil)
)

// New returns the store over ks, which must be Owner's keyspace.
func New(ks storage.Keyspace, opts Options) (*Store, error) {
	if ks.Owner() != Owner {
		return nil, fmt.Errorf("rbacstore: keyspace of %q, want %q", ks.Owner(), Owner)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Store{ks: ks, cat: opts.Catalog, now: now}, nil
}

// NewBatch opens a write batch over the role keyspace. The kernel hands it to
// rbac.Service as Deps.NewBatch, so the service's role writes, their audit
// records and their revocation lines share one batch.
func (s *Store) NewBatch() *storage.Batch { return s.ks.NewBatch() }

// GetRole reads the stored role name. An absent role is an error wrapping
// rbac.ErrUnknownRole; a row that does not decode, or whose Name is not its
// key's, wraps rbac.ErrUndecodableRole.
func (s *Store) GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: empty role name", rbac.ErrUnknownRole)
	}
	raw, err := s.ks.Get(ctx, roleKey(name))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", rbac.ErrUnknownRole, name)
	}
	if err != nil {
		return nil, fmt.Errorf("rbacstore: read role %s: %w", name, err)
	}
	return decode(name, raw)
}

// ScanRoles yields every stored role in name order. A row that does not
// decode yields an error wrapping rbac.ErrUndecodableRole for that row and
// the scan goes on; any other error ends it.
func (s *Store) ScanRoles(ctx context.Context) iter.Seq2[*rbac.Role, error] {
	return func(yield func(*rbac.Role, error) bool) {
		for kv, err := range s.ks.ScanPrefix(ctx, rolePrefix()) {
			if err != nil {
				yield(nil, fmt.Errorf("rbacstore: scan roles: %w", err))
				return
			}
			name, err := nameOf(kv.Key)
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			r, err := decode(name, kv.Value)
			if !yield(r, err) {
				return
			}
		}
	}
}

// nameOf returns the role name of a role row's relative key.
func nameOf(key []byte) (rbac.RoleName, error) {
	segs, err := keys.Split(key)
	if err != nil || len(segs) != 2 || string(segs[0]) != roleSeg || len(segs[1]) == 0 {
		return "", fmt.Errorf("%w: key %q is not role/<name>", rbac.ErrUndecodableRole, key)
	}
	return rbac.RoleName(segs[1]), nil
}

// decode decodes the row of name, refusing a record that names another role.
func decode(name rbac.RoleName, raw []byte) (*rbac.Role, error) {
	r, err := roleCodec().Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", rbac.ErrUndecodableRole, name, err)
	}
	if r.Name != name {
		return nil, fmt.Errorf("%w: row of %s holds role %q", rbac.ErrUndecodableRole, name, r.Name)
	}
	return &r, nil
}

// PutRole stages r into b. r.Version must be the stored Version + 1, or 1
// for a role that is not stored (rbac.ErrRoleExists when it is); any other
// version wraps rbac.ErrVersionMismatch. The write carries an Expect on the
// stored bytes (absent for a create). A refused write stages nothing.
func (s *Store) PutRole(b *storage.Batch, r *rbac.Role) error {
	if b == nil {
		return errors.New("rbacstore: PutRole needs the caller's batch")
	}
	if err := checkShape(r); err != nil {
		return err
	}
	if r.Version < 1 {
		return fmt.Errorf("%w: %s: version %d, want the stored version + 1", rbac.ErrVersionMismatch, r.Name, r.Version)
	}
	raw, err := roleCodec().Encode(*r)
	if err != nil {
		return fmt.Errorf("rbacstore: encode role %s: %w", r.Name, err)
	}
	key := roleKey(r.Name)
	committed, err := s.committed(key)
	if err != nil {
		return err
	}
	switch {
	case committed == nil && r.Version != 1:
		return fmt.Errorf("%w: %s is not stored, so its first version is 1 (got %d)", rbac.ErrVersionMismatch, r.Name, r.Version)
	case committed != nil && r.Version == 1:
		return fmt.Errorf("%w: %s", rbac.ErrRoleExists, r.Name)
	case committed != nil:
		stored, err := decode(r.Name, committed)
		if err != nil {
			return err
		}
		if stored.Version != r.Version-1 {
			return fmt.Errorf("%w: %s is at version %d, the write replaces %d", rbac.ErrVersionMismatch, r.Name, stored.Version, r.Version-1)
		}
	}
	return s.stage(b, key, committed, raw)
}

// DeleteRole stages the deletion of r. r.Version must equal the stored
// Version (rbac.ErrVersionMismatch otherwise); an absent role wraps
// rbac.ErrUnknownRole. The delete carries an Expect on the stored bytes.
func (s *Store) DeleteRole(b *storage.Batch, r *rbac.Role) error {
	if b == nil {
		return errors.New("rbacstore: DeleteRole needs the caller's batch")
	}
	if r == nil || r.Name == "" {
		return errors.New("rbacstore: DeleteRole of a role with no name")
	}
	key := roleKey(r.Name)
	committed, err := s.committed(key)
	if err != nil {
		return err
	}
	if committed == nil {
		return fmt.Errorf("%w: %s", rbac.ErrUnknownRole, r.Name)
	}
	stored, err := decode(r.Name, committed)
	if err != nil {
		return err
	}
	if stored.Version != r.Version {
		return fmt.Errorf("%w: %s is at version %d, the delete names %d", rbac.ErrVersionMismatch, r.Name, stored.Version, r.Version)
	}
	return s.stage(b, key, committed, nil)
}

// ImportRole stores r as a custom role (05-rbac §3.4, §6.3): Origin
// OriginCustom, Version 1, Added the sorted, de-duplicated r.Added. It refuses
// with an error wrapping rbac.ErrImportRejected, staging nothing:
//
//   - a name that is not custom.<slug> (every seed role's name is not);
//   - an Origin other than OriginCustom, or seed bookkeeping (SeedPermissions,
//     SeedDigest, PendingSeed, Locked) on the record;
//   - an empty permission set, which no imported role holds (the cutover
//     quarantines a role with no known atom, §6.3);
//   - a malformed pattern or a scope the catalog does not hold (and every
//     import when the store has no catalog);
//   - any pattern that covers core:admin (core:admin, core:* or core:admin.*,
//     rbac.GrantsAdmin): the cutover maps a role holding every atom to the
//     union of §6.1's rows, never core:admin (SEC-12), so an import granting
//     it is a broken mapping, not an admin role;
//   - a role that is already stored.
func (s *Store) ImportRole(b *storage.Batch, r *rbac.Role) error {
	if b == nil {
		return fmt.Errorf("%w: no batch", rbac.ErrImportRejected)
	}
	if r == nil {
		return fmt.Errorf("%w: nil role", rbac.ErrImportRejected)
	}
	reject := func(why error) error { return fmt.Errorf("%w: %s: %w", rbac.ErrImportRejected, r.Name, why) }
	if err := rbac.ValidateCustomRoleName(r.Name); err != nil {
		return reject(err)
	}
	if r.Origin != rbac.OriginCustom {
		return reject(errors.New("an imported role is a custom role (OriginCustom)"))
	}
	if len(r.SeedPermissions) > 0 || r.SeedDigest != "" || len(r.PendingSeed) > 0 || r.Locked {
		return reject(errors.New("an imported custom role carries no seed bookkeeping and is not locked"))
	}
	if len(r.Added) == 0 {
		return reject(errors.New("an imported role holds at least one permission"))
	}
	if err := rbac.CheckRolePermissions(s.cat, r.Added); err != nil {
		return reject(err)
	}
	if rbac.GrantsAdmin(r.Added) {
		return reject(fmt.Errorf("%s is never imported (SEC-12)", rbac.CoreAdmin))
	}
	now := s.now().UTC()
	in := rbac.Role{
		Name:        r.Name,
		Title:       r.Title,
		Description: r.Description,
		Origin:      rbac.OriginCustom,
		Added:       sortedUnique(r.Added),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		Version:     1,
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = in.CreatedAt
	}
	raw, err := roleCodec().Encode(in)
	if err != nil {
		return reject(err)
	}
	key := roleKey(in.Name)
	committed, err := s.committed(key)
	if err != nil {
		return reject(err)
	}
	if committed != nil {
		return reject(rbac.ErrRoleExists)
	}
	if err := s.stage(b, key, nil, raw); err != nil {
		return reject(err)
	}
	return nil
}

// checkShape refuses a record PutRole cannot store as a role.
func checkShape(r *rbac.Role) error {
	if r == nil || r.Name == "" {
		return errors.New("rbacstore: PutRole of a role with no name")
	}
	switch r.Origin {
	case rbac.OriginSeed:
	case rbac.OriginCustom:
		if err := rbac.ValidateCustomRoleName(r.Name); err != nil {
			return err
		}
	default:
		return fmt.Errorf("rbacstore: role %s has origin %d, want seed or custom", r.Name, r.Origin)
	}
	return nil
}

// committed reads the committed value of key: nil when absent. The Store
// interface's writes carry no context, so the read takes a background one: it
// is one point read of the local engine, not cancellable work.
func (s *Store) committed(key []byte) ([]byte, error) {
	raw, err := s.ks.Get(context.Background(), key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rbacstore: read %q: %w", key, err)
	}
	return raw, nil
}

// stage binds the role keyspace to b, records the Expect (want nil: the key
// must be absent) and stages the write (raw nil: a delete).
func (s *Store) stage(b *storage.Batch, key, want, raw []byte) error {
	b.Bind(s.ks)
	if err := b.Expect(s.ks, key, want); err != nil {
		return fmt.Errorf("rbacstore: expect %q: %w", key, err)
	}
	w := b.In(s.ks)
	if raw == nil {
		if err := w.Delete(key); err != nil {
			return fmt.Errorf("rbacstore: stage delete %q: %w", key, err)
		}
		return nil
	}
	if err := w.Set(key, raw); err != nil {
		return fmt.Errorf("rbacstore: stage %q: %w", key, err)
	}
	return nil
}

// sortedUnique returns ps sorted without duplicates, as a fresh slice.
func sortedUnique(ps []rbac.Permission) []rbac.Permission {
	out := slices.Clone(ps)
	slices.Sort(out)
	return slices.Compact(out)
}
