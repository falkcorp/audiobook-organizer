// file: core/internal/rbacstore/store.go
// version: 1.6.0
// guid: 6f1c9a3e-28d4-4b7e-a5f0-93e1d27c4b85
// last-edited: 2026-11-19

// Package rbacstore is core's one implementation of rbac.Store and
// rbac.Importer (05-rbac §3.4, §6.3 "Native store"): roles kept in the core
// keyspace c/rbac/ at role/<name> as codec.JSON[rbac.Role](1), every field of
// the role, plus one counter at generation that every role write bumps in its
// own batch (rbac.RoleGuarder). It implements rbac (layer L4, 03 R4) and is imported only by the
// kernel and by tests. It holds no cache and no lock: every read goes to the
// keyspace, and every write stages into the caller's batch with an Expect on
// the stored record, so the batch's Commit fails with storage.ErrConflict when
// another writer got there first. Its one piece of state is the import seal
// (rbac.ImportSealer): rbac.Service.Freeze seals it, and from then on
// ImportRole refuses, because a role imported after the service loaded its
// role cache would be stored but invisible until a restart.
package rbacstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sync/atomic"
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

// genSeg is the one segment of the role-set generation row, outside role/ so
// ScanRoles never meets it.
const genSeg = "generation"

// claimSeg is the one segment of the role-claim counter, beside the
// generation row for the same reason: it is not a role.
const claimSeg = "claims"

// StorageSpec is the c/rbac/ spec the kernel registers for Owner: a core
// keyspace whose role rows and generation row are Serialized, so every write
// to one carries an Expect (05-storage R6-1), and an Expect on the generation
// is allowed (rbac.RoleGuarder).
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{string(rolePrefix()), string(generationKey()), string(claimKey())}}
}

// generationKey is the relative key of the role-set generation: a big-endian
// uint64 that every role write raises by one. Absent is generation 0.
func generationKey() []byte { return keys.Join(keys.Str(genSeg)) }

// claimKey is the relative key of the role-claim counter: a big-endian uint64
// that every ClaimRoleSet raises by one. It is separate from the generation so
// that a claim does not conflict with a last-admin decision, which guards the
// generation and does not care who was given a role. Absent is 0.
func claimKey() []byte { return keys.Join(keys.Str(claimSeg)) }

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
	// sealed is set by SealImports and never cleared: once the service is
	// frozen, every ImportRole is refused.
	sealed atomic.Bool
}

var (
	_ rbac.Store        = (*Store)(nil)
	_ rbac.Importer     = (*Store)(nil)
	_ rbac.ImportSealer = (*Store)(nil)
	_ rbac.RoleGuarder  = (*Store)(nil)
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

// SealImports makes every later ImportRole refuse with an error wrapping
// rbac.ErrImportRejected and rbac.ErrFrozen. rbac.Service.Freeze calls it;
// it cannot be undone.
func (s *Store) SealImports() { s.sealed.Store(true) }

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

// RoleSetGeneration reads the committed role-set generation (rbac.RoleGuarder):
// how many role writes have committed, 0 for none. A caller reads it before it
// reads any role definition it will decide on.
func (s *Store) RoleSetGeneration(ctx context.Context) (uint64, error) {
	raw, err := s.ks.Get(ctx, generationKey())
	if errors.Is(err, storage.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("rbacstore: read the role-set generation: %w", err)
	}
	return decodeGeneration(raw)
}

// GuardRoleSet stages into b an Expect that the role-set generation is still
// generation (rbac.RoleGuarder). b is the caller's batch, which belongs to
// another owner (auth's last-admin rule guards the role definitions it
// reads); the Expect binds this keyspace into it, so any role write that
// commits after generation was read, a create, an edit, a delete or an
// import, fails the caller's Commit with storage.ErrConflict. Nothing is
// written. Generation 0 expects the row absent, which is what an unwritten
// role set is.
//
// The first guard of a batch wins. A caller that decides twice in one batch
// (SetRoles runs the last-admin rule and then claims the roles it adds) reads
// the generation twice, and the second read can be newer than the first when a
// role write landed between them; an Expect staged for the newer value would
// replace the older one, and the decisions made at the older value would lose
// their guard. So the precondition is recorded the way a read-modify-write
// records it (storage.Batch.GetForUpdate keeps the first read's), and a
// generation that is not the committed one now is refused on the spot with
// storage.ErrConflict, which every caller's retry loop already treats as "decide
// again".
func (s *Store) GuardRoleSet(b *storage.Batch, generation uint64) error {
	if b == nil {
		return errors.New("rbacstore: GuardRoleSet needs the caller's batch")
	}
	b.Bind(s.ks)
	cur, err := s.generationThrough(b)
	if err != nil {
		return err
	}
	if cur != generation {
		return fmt.Errorf("%w: the role set moved from generation %d to %d since it was read", storage.ErrConflict, generation, cur)
	}
	return nil
}

// ClaimRoleSet is GuardRoleSet followed by a bump of the role-claim counter
// in the same batch (rbac.RoleGuarder). A user write that gives a user a role
// calls it: the guard makes the write conflict with any role write that
// committed after the caller decided the role exists, and the bump makes a
// DeleteRole that guarded the claim counter (GuardRoleClaims) before it read
// the role's holders and found none conflict with this write in turn, so the
// two cannot both commit whichever lands first. The bump changes no role and
// not the generation, so a claim does not fail a last-admin decision.
func (s *Store) ClaimRoleSet(b *storage.Batch, generation uint64) error {
	if err := s.GuardRoleSet(b, generation); err != nil {
		return err
	}
	cur, err := counterThrough(s, b, claimKey())
	if err != nil {
		return err
	}
	if err := b.In(s.ks).Set(claimKey(), encodeGeneration(cur+1)); err != nil {
		return fmt.Errorf("rbacstore: stage the role-claim counter: %w", err)
	}
	return nil
}

// GuardRoleClaims stages into b an Expect that no ClaimRoleSet has committed
// since this call (rbac.RoleGuarder): it records the claim counter's committed
// value as of now. DeleteRole calls it before it reads a role's holders, so a
// claim of that role that commits after the read, one the holders could not
// show, fails the delete's Commit. Nothing is written.
func (s *Store) GuardRoleClaims(b *storage.Batch) error {
	if b == nil {
		return errors.New("rbacstore: GuardRoleClaims needs the caller's batch")
	}
	b.Bind(s.ks)
	_, err := counterThrough(s, b, claimKey())
	return err
}

// generationThrough reads the generation row through b (counterThrough).
func (s *Store) generationThrough(b *storage.Batch) (uint64, error) {
	return counterThrough(s, b, generationKey())
}

// counterThrough reads the counter row at key through b, recording the Expect
// on the committed value if b has none for it yet (and keeping the one it has).
// An absent row is 0.
func counterThrough(s *Store, b *storage.Batch, key []byte) (uint64, error) {
	view, err := b.GetForUpdate(context.Background(), s.ks, key)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("rbacstore: read counter %q through the batch: %w", key, err)
	}
	return decodeGeneration(view)
}

// bumpGeneration stages the generation row of b one above its committed
// value, with an Expect on that value. Every role write calls it, so the
// bump commits with the write and a role-set guard (GuardRoleSet) sees every
// one. Two role writes in one batch (a seed or restore batch) both derive the
// row from the committed value, keep the first read's Expect, and stage the
// same bump.
func (s *Store) bumpGeneration(b *storage.Batch) error {
	cur, err := s.generationThrough(b)
	if err != nil {
		return err
	}
	if err := b.In(s.ks).Set(generationKey(), encodeGeneration(cur+1)); err != nil {
		return fmt.Errorf("rbacstore: stage the role-set generation: %w", err)
	}
	return nil
}

// encodeGeneration is the stored form of a generation: 8 bytes, big endian.
func encodeGeneration(g uint64) []byte { return binary.BigEndian.AppendUint64(nil, g) }

// decodeGeneration reads a stored generation; a row of another length is an
// error, never generation 0.
func decodeGeneration(raw []byte) (uint64, error) {
	if len(raw) != 8 {
		return 0, fmt.Errorf("rbacstore: the role-set generation row holds %d bytes, want 8", len(raw))
	}
	return binary.BigEndian.Uint64(raw), nil
}

// ImportRole stores r as a custom role (05-rbac §3.4, §6.3): Origin
// OriginCustom, Version 1, Added the sorted, de-duplicated r.Added. It refuses
// with an error wrapping rbac.ErrImportRejected, staging nothing:
//
//   - any import once SealImports has run (the error also wraps
//     rbac.ErrFrozen): the cutover runs before rbac.Service.Freeze;
//   - a name that is not custom.<slug> (every seed role's name is not);
//   - an Origin other than OriginCustom, or seed bookkeeping (SeedPermissions,
//     SeedDigest, PendingSeed, Locked, SeedConfined) on the record;
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
	if s.sealed.Load() {
		return fmt.Errorf("%w: the role service is frozen: %w", rbac.ErrImportRejected, rbac.ErrFrozen)
	}
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
	if len(r.SeedPermissions) > 0 || r.SeedDigest != "" || len(r.PendingSeed) > 0 || r.Locked || r.SeedConfined {
		return reject(errors.New("an imported custom role carries no seed bookkeeping (seed permissions, digest, pending set, SeedConfined) and is not locked"))
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

// ErrDoubleWrite is a second write to a role one batch already changed (the
// write is derived from the committed bytes, so it would replace the first
// without having seen it). Only a batch that reads its own writes can tell; a
// programming error, not a conflict, never retried.
var ErrDoubleWrite = errors.New("rbacstore: a record is written twice in one batch")

// stage binds the role keyspace to b, records the Expect (want nil: the key
// must be absent), stages the write (raw nil: a delete) and bumps the role-set
// generation (rbac.RoleGuarder), the one place every role write goes through.
// On an indexed batch (the kernel's re-apply batch is one) it refuses a second
// write to key.
func (s *Store) stage(b *storage.Batch, key, want, raw []byte) error {
	b.Bind(s.ks)
	if err := b.Expect(s.ks, key, want); err != nil {
		return fmt.Errorf("rbacstore: expect %q: %w", key, err)
	}
	if b.Indexed() {
		view, err := b.GetForUpdate(context.Background(), s.ks, key)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			view = nil
		case err != nil:
			return fmt.Errorf("rbacstore: read %q through the batch: %w", key, err)
		}
		if !bytes.Equal(view, want) {
			// This batch's own write, or a commit that landed since the caller read
			// (the commit's Expect reports that as a conflict and the caller retries).
			now, err := s.committed(key)
			if err != nil {
				return err
			}
			if bytes.Equal(now, want) {
				return fmt.Errorf("%w: %q", ErrDoubleWrite, key)
			}
		}
	}
	w := b.In(s.ks)
	if raw == nil {
		if err := w.Delete(key); err != nil {
			return fmt.Errorf("rbacstore: stage delete %q: %w", key, err)
		}
	} else if err := w.Set(key, raw); err != nil {
		return fmt.Errorf("rbacstore: stage %q: %w", key, err)
	}
	return s.bumpGeneration(b)
}

// sortedUnique returns ps sorted without duplicates, as a fresh slice.
func sortedUnique(ps []rbac.Permission) []rbac.Permission {
	out := slices.Clone(ps)
	slices.Sort(out)
	return slices.Compact(out)
}
