// file: core/secrets/scope.go
// version: 1.0.1
// guid: 260601d3-3d42-4bb6-bd05-ec43f4e9b477
// last-edited: 2026-11-10

package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Scope is the handle a module receives as module.Deps.Secrets (06 §6.4.4):
// every method refuses a name outside its prefix "modules.<id>." with
// ErrOutOfScope. Integration-instance secrets are never reachable through a
// Scope. Core subsystems get a Scope with prefix "core.<subsystem>.". The
// zero Scope, and a Scope built from an invalid prefix, refuse every name.
type Scope struct {
	s      *Store
	prefix string
	err    error // an invalid prefix: every method returns it
}

// validScopePrefix reports whether prefix is "modules.<seg>." or
// "core.<seg>." with seg in the name grammar.
func validScopePrefix(prefix string) bool {
	body, ok := strings.CutSuffix(prefix, ".")
	if !ok {
		return false
	}
	root, seg, ok := strings.Cut(body, ".")
	if !ok || (root != "modules" && root != "core") {
		return false
	}
	return validNameSegment(seg)
}

// Scope returns the handle for prefix: "modules.<id>." or
// "core.<subsystem>." (kernel only).
func (s *Store) Scope(prefix string) Scope {
	if !validScopePrefix(prefix) {
		return Scope{s: s, prefix: prefix, err: fmt.Errorf("%w: scope prefix %q is neither a modules.<id>. nor a core.<subsystem>. prefix", ErrOutOfScope, prefix)}
	}
	return Scope{s: s, prefix: prefix}
}

// Prefix is the scope's prefix.
func (sc Scope) Prefix() string { return sc.prefix }

// check refuses a name outside the scope (or any name of an invalid scope).
func (sc Scope) check(n Name) error {
	switch {
	case sc.s == nil:
		return fmt.Errorf("%w: the zero Scope", ErrOutOfScope)
	case sc.err != nil:
		return sc.err
	case len(n) <= len(sc.prefix) || !strings.HasPrefix(string(n), sc.prefix):
		return fmt.Errorf("%w: %s is not under %s", ErrOutOfScope, n, sc.prefix)
	}
	return nil
}

// Get is Store.Get for a name under the prefix.
func (sc Scope) Get(ctx context.Context, n Name) (Value, Meta, error) {
	if err := sc.check(n); err != nil {
		sc.count(opGet, err)
		return Value{}, Meta{Name: n}, err
	}
	return sc.s.Get(ctx, n)
}

// Meta is Store.Meta for a name under the prefix.
func (sc Scope) Meta(ctx context.Context, n Name) (Meta, error) {
	if err := sc.check(n); err != nil {
		return Meta{Name: n}, err
	}
	return sc.s.Meta(ctx, n)
}

// Put is Store.Put for a name under the prefix.
func (sc Scope) Put(ctx context.Context, n Name, v Value, by string) (Meta, error) {
	if err := sc.check(n); err != nil {
		sc.count(opPut, err)
		return Meta{Name: n}, err
	}
	return sc.s.Put(ctx, n, v, by)
}

// Delete is Store.Delete for a name under the prefix.
func (sc Scope) Delete(ctx context.Context, n Name, by string) error {
	if err := sc.check(n); err != nil {
		sc.count(opDelete, err)
		return err
	}
	return sc.s.Delete(ctx, n, by)
}

// DeriveKey is Store.DeriveKey with info prefixed by the scope, so two
// scopes never derive the same key.
func (sc Scope) DeriveKey(info string) ([]byte, error) {
	if err := sc.checkInfo(info); err != nil {
		return nil, err
	}
	return sc.s.DeriveKey(sc.prefix + info)
}

// DeriveKeyFor is Store.DeriveKeyFor with info prefixed by the scope.
func (sc Scope) DeriveKeyFor(kid, info string) ([]byte, error) {
	if err := sc.checkInfo(info); err != nil {
		return nil, err
	}
	return sc.s.DeriveKeyFor(kid, sc.prefix+info)
}

// checkInfo refuses an invalid scope and an empty info.
func (sc Scope) checkInfo(info string) error {
	switch {
	case sc.s == nil:
		return fmt.Errorf("%w: the zero Scope", ErrOutOfScope)
	case sc.err != nil:
		return sc.err
	case info == "":
		return errors.New("secrets: DeriveKey needs an info string")
	}
	return nil
}

// count records a refused operation on the store's counters.
func (sc Scope) count(op string, err error) {
	if sc.s != nil {
		sc.s.ctr.op(op, err)
	}
}
