// file: core/library/scope.go
// version: 1.1.0
// guid: baa18613-7fdd-4016-b9a4-ee85102dc70c
// last-edited: 2026-11-11

package library

import (
	"context"
	"fmt"
	"iter"
	"net/url"
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

// ModuleRoot is a root as one module sees it. Root itself belongs to core: its
// FS takes the caller as an argument and its AppDir is any name, so a module
// holding a *Root could act as core or as another module. A ModuleRoot carries
// the module's owner as a field and no method takes another, and none returns a
// *Root or an unguarded driver; the owner cannot be changed from outside the
// package.
type ModuleRoot struct {
	r     *Root
	owner storage.Owner
}

// root is the wrapped root; a zero ModuleRoot has none.
func (m *ModuleRoot) root() (*Root, error) {
	if m == nil || m.r == nil {
		return nil, fmt.Errorf("%w: a ModuleRoot comes from Scope.Roots", ErrInvalidOptions)
	}
	return m.r, nil
}

// ID is the root's ID.
func (m *ModuleRoot) ID() RootID {
	if r, err := m.root(); err == nil {
		return r.ID()
	}
	return ""
}

// Spec is the normalized spec the root was registered from.
func (m *ModuleRoot) Spec() RootSpec {
	if r, err := m.root(); err == nil {
		return r.Spec()
	}
	return RootSpec{}
}

// Disabled reports why the root is disabled, or nil.
func (m *ModuleRoot) Disabled() error {
	r, err := m.root()
	if err != nil {
		return err
	}
	return r.Disabled()
}

// Usage is the capacity of the filesystem holding the root.
func (m *ModuleRoot) Usage(ctx context.Context) (Usage, error) {
	r, err := m.root()
	if err != nil {
		return Usage{}, err
	}
	return r.Usage(ctx)
}

// Mounted verifies the root's identity and sentinel (Root.Mounted).
func (m *ModuleRoot) Mounted(ctx context.Context) error {
	r, err := m.root()
	if err != nil {
		return err
	}
	return r.Mounted(ctx)
}

// FS is the confined filesystem for the module's owner: read-only unless the
// owner may write the root, ErrNotReadable when it may not read it.
func (m *ModuleRoot) FS() (RootFS, error) {
	r, err := m.root()
	if err != nil {
		return nil, err
	}
	return r.FS(m.owner)
}

// AppDir is the module's own directory under <root>/<AppDir>, named for its
// owner (path-escaped, so no owner's directory lies inside another's), created
// owner-only-writable. Only a module that may write the root gets one: on a
// root it merely reads (a download root) there is nothing to create.
func (m *ModuleRoot) AppDir(ctx context.Context) (RootFS, error) {
	r, err := m.root()
	if err != nil {
		return nil, err
	}
	if !r.m.canWrite(r, m.owner) || !r.m.enabled(m.owner) {
		return nil, fmt.Errorf("%w: %s", ErrReadOnlyRoot, r.ID())
	}
	return r.AppDir(ctx, url.PathEscape(string(m.owner)))
}

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

// Roots lists the roots the owner owns or may read, narrowed by f. Each comes
// back as a ModuleRoot: the root with the owner bound in, so what a module can
// do through it is what its owner can do and no more.
func (s *scope) Roots(f RootFilter) []*ModuleRoot {
	roots := s.m.Roots(RootFilter{Owner: s.owner, Readable: true, Role: f.Role})
	out := make([]*ModuleRoot, 0, len(roots))
	for _, r := range roots {
		if f.Owner != "" && r.Spec().Owner != f.Owner {
			continue
		}
		out = append(out, &ModuleRoot{r: r, owner: s.owner})
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
