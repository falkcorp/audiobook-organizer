// file: core/library/oracle.go
// version: 1.0.0
// guid: 807f6dc1-f000-426c-8703-c39268bc03c3
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"strings"
)

// PathSetOracle is a ProtectionOracle and a SubtreeOracle over an explicit list
// of protected paths: what a seeding client reports (the content paths of the
// torrents it seeds). A download adapter (05-download) wraps its list in one and
// registers it, instead of implementing ProtectedUnder itself.
//
// A listed path protects itself and, when it is a directory, everything under it.
// Protected(p) is true when p is, or lies under, a listed path. ProtectedUnder(dir)
// adds the other direction a directory Move or Quarantine needs: it is true when
// a listed path lies under dir (the move would carry it away). Only paths in the
// same root are compared. The list is asked on every call, so it is always the
// client's current one; a list that cannot be read answers "unknown" (loaded
// false), which refuses the move.
type PathSetOracle struct {
	list func(ctx context.Context) ([]Path, error)
}

// NewPathSetOracle is the oracle over list. A nil list is always unknown.
func NewPathSetOracle(list func(ctx context.Context) ([]Path, error)) *PathSetOracle {
	return &PathSetOracle{list: list}
}

var _ SubtreeOracle = (*PathSetOracle)(nil)

// Protected implements ProtectionOracle.
func (o *PathSetOracle) Protected(ctx context.Context, p Path) (protected, loaded bool, err error) {
	return o.answer(ctx, p, false)
}

// ProtectedUnder implements SubtreeOracle.
func (o *PathSetOracle) ProtectedUnder(ctx context.Context, dir Path) (protected, loaded bool, err error) {
	return o.answer(ctx, dir, true)
}

func (o *PathSetOracle) answer(ctx context.Context, p Path, subtree bool) (bool, bool, error) {
	if o == nil || o.list == nil {
		return false, false, errors.New("library: the path-set oracle has no list")
	}
	paths, err := o.list(ctx)
	if err != nil {
		return false, false, err
	}
	for _, e := range paths {
		if e.Root != p.Root {
			continue
		}
		if relWithin(p.Rel, e.Rel) || (subtree && relWithin(e.Rel, p.Rel)) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// relWithin reports whether rel is base or lies under it, by whole path components
// ("a/bc" is not under "a/b"). The root itself ("") contains everything.
func relWithin(rel, base string) bool {
	return base == "" || rel == base || strings.HasPrefix(rel, base+"/")
}
