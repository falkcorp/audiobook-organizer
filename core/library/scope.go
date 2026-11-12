// file: core/library/scope.go
// version: 1.0.2
// guid: baa18613-7fdd-4016-b9a4-ee85102dc70c
// last-edited: 2026-11-11

package library

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/storage"
)

// scopeHost is what a Scope needs of the manager beyond Manager: the rights
// check and the owner's pending transfers. The value New returns implements it.
type scopeHost interface {
	Manager
	mayRead(r *Root, caller storage.Owner) bool
	hiddenFrom(caller storage.Owner, r *Root, p Path) bool
	absFor(caller storage.Owner, p Path) (string, error)
	PendingTransfers(ctx context.Context, owner storage.Owner) ([]TransferRecord, error)
}

func (m *manager) mayRead(r *Root, caller storage.Owner) bool { return m.canRead(r, caller) }

var _ scopeHost = (*manager)(nil)

// scope is the Scope: a manager with one owner bound in. The owner is a field
// of this value and no method takes another, so a module holding a Scope cannot
// act as a different owner.
type scope struct {
	m     scopeHost
	owner storage.Owner
}

// NewScope binds owner to m: the Scope the kernel hands the module that owner
// names (module.Deps.Library). m must be the Manager New returned.
func NewScope(m Manager, owner storage.Owner) (Scope, error) {
	h, ok := m.(scopeHost)
	if !ok {
		return nil, fmt.Errorf("%w: NewScope needs the Manager that library.New returned", ErrInvalidOptions)
	}
	if owner == "" {
		return nil, fmt.Errorf("%w: a Scope needs an owner", ErrInvalidOptions)
	}
	return &scope{m: h, owner: owner}, nil
}

func (s *scope) Owner() storage.Owner { return s.owner }

// Roots lists the roots the owner owns or may read, narrowed by f.
func (s *scope) Roots(f RootFilter) []*Root {
	out := s.m.Roots(RootFilter{Owner: s.owner, Readable: true, Role: f.Role})
	if f.Owner != "" {
		out = slices.DeleteFunc(out, func(r *Root) bool { return r.Spec().Owner != f.Owner })
	}
	return out
}

func (s *scope) FS(id RootID) (RootFS, error) {
	r, err := s.m.Root(id)
	if err != nil {
		return nil, err
	}
	return r.FS(s.owner)
}

// readable is the root p names, if the owner may read it.
func (s *scope) readable(p Path) error {
	r, err := s.m.Root(p.Root)
	if err != nil {
		return err
	}
	if !s.m.mayRead(r, s.owner) {
		return fmt.Errorf("%w: %s", ErrNotReadable, p.Root)
	}
	if s.m.hiddenFrom(s.owner, r, p) {
		return fmt.Errorf("%w: %s", ErrReservedPath, p)
	}
	return nil
}

func (s *scope) Resolve(abs string) (Path, error) {
	p, err := s.m.Resolve(abs)
	if err != nil {
		return Path{}, err
	}
	if err := s.readable(p); err != nil {
		return Path{}, err
	}
	return p, nil
}

func (s *scope) Abs(p Path) (string, error) { return s.m.absFor(s.owner, p) }

func (s *scope) Transfer(ctx context.Context, src, dst Path, opts TransferOptions) (TransferResult, error) {
	return s.m.Transfer(ctx, s.owner, src, dst, opts)
}

func (s *scope) TransferIn(ctx context.Context, b *storage.Batch, src, dst Path, opts TransferOptions) (TransferResult, error) {
	return s.m.TransferIn(ctx, b, s.owner, src, dst, opts)
}

func (s *scope) PendingTransfers(ctx context.Context) ([]TransferRecord, error) {
	return s.m.PendingTransfers(ctx, s.owner)
}

func (s *scope) Hash(ctx context.Context, p Path) (Digest, error) {
	if err := s.readable(p); err != nil {
		return Digest{}, err
	}
	return s.m.Hash(ctx, p)
}

func (s *scope) Quarantine(ctx context.Context, p Path, reason string) (QuarantineID, error) {
	return s.m.Quarantine(ctx, s.owner, p, reason)
}

func (s *scope) Release(ctx context.Context, id QuarantineID, to *Path) (Path, error) {
	return s.m.Release(ctx, s.owner, id, to)
}

func (s *scope) Watch(ctx context.Context, id RootID, opts WatchOptions) (iter.Seq[[]Event], error) {
	if err := s.readable(Path{Root: id}); err != nil {
		return nil, err
	}
	return s.m.Watch(ctx, id, opts)
}

// Mapper is the manager's mapper narrowed to the roots the owner may read: a
// mapping into another owner's root is not offered and does not resolve.
func (s *scope) Mapper() PathMapper { return scopedMapper{in: s.m.Mapper(), s: s} }

type scopedMapper struct {
	in PathMapper
	s  *scope
}

func (sm scopedMapper) ToLocal(source, remote string) (Path, error) {
	p, err := sm.in.ToLocal(source, remote)
	if err != nil {
		return Path{}, err
	}
	if err := sm.s.readable(p); err != nil {
		return Path{}, err
	}
	return p, nil
}

func (sm scopedMapper) ToRemote(source string, p Path) (string, error) {
	if err := sm.s.readable(p); err != nil {
		return "", err
	}
	return sm.in.ToRemote(source, p)
}

func (sm scopedMapper) Mappings(source string) []Mapping {
	return slices.DeleteFunc(sm.in.Mappings(source), func(mp Mapping) bool { return sm.s.readable(mp.Local) != nil })
}

// PendingTransfers lists owner's TransferIn rows that finished their file work
// and whose caller batch never committed, oldest first.
func (m *manager) PendingTransfers(ctx context.Context, owner storage.Owner) ([]TransferRecord, error) {
	rows, err := m.listTransfers(ctx)
	var out []TransferRecord
	for _, r := range rows {
		if r.State == stDone && r.In && r.Owner == owner {
			out = append(out, r.record())
		}
	}
	slices.SortFunc(out, func(a, b TransferRecord) int { return strings.Compare(a.ID, b.ID) })
	return out, err
}
