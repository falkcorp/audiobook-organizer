// file: core/config/importer.go
// version: 1.0.1
// guid: 2c9586e1-b071-417e-a1e0-e58dc0945411
// last-edited: 2026-11-10

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/storage"
)

// storeImporter is the store's Importer (05-config §1.3.3, §1.12.2).
type storeImporter struct{ s *Store }

// Importer returns config's write side for a one-way cutover step. It is
// valid after Load, when every enabled section is registered, and stages
// through the override store SetImportTarget handed over.
func (s *Store) Importer() Importer { return storeImporter{s: s} }

// importKey resolves p for the importer: registered, owned, and a key the
// DB layer may hold.
func (s *Store) importKey(p Path) (ckey, error) {
	if !s.loaded {
		return ckey{}, errors.New("config: the Importer is valid after Load")
	}
	ck, ok := s.keyFor(p)
	switch {
	case !ok || s.passthrough(p):
		return ckey{}, fmt.Errorf("%w: %s is not a registered key", ErrUnknownKey, p)
	case ck.k.secret:
		return ckey{}, fmt.Errorf("%w: %s is a secret key: secrets go through secrets.Importer (05-config §2.3)", ErrInvalid, p)
	case ck.k.protect == ProtectOperator:
		return ckey{}, fmt.Errorf("%w: %s is an operator-only key: never stored", ErrInvalid, p)
	case ck.k.reload == ReloadBoot:
		return ckey{}, fmt.Errorf("%w: %s is a boot key: its stored value lives in the boot record, not in c/config/", ErrInvalid, p)
	}
	return ck, nil
}

// Stage validates v against p's schema and type and stages p's c/config/
// row into b, binding c/config/ to b. A value equal to p's default stages
// nothing (C4). An invalid value is returned as the *FieldError itself, which
// the step quarantines. Nothing is written until the caller commits b.
func (i storeImporter) Stage(b *storage.Batch, p Path, v json.RawMessage, by string) error {
	s := i.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	ck, err := s.importKey(p)
	if err != nil {
		return err
	}
	c, err := ck.k.canonical(p, v)
	if err != nil {
		if fe, ok := errors.AsType[*FieldError](err); ok {
			return fe
		}
		return &FieldError{Path: p, Err: err}
	}
	if slices.Equal(c, ck.def()) {
		return nil
	}
	if s.importTarget == nil {
		return errors.New("config: the Importer has no override store: the kernel calls SetImportTarget first")
	}
	if b == nil {
		return errors.New("config: Importer.Stage needs the step's batch")
	}
	ov := Override{Path: p, Value: c, At: s.now(), By: by}
	return s.importTarget.Stage(context.Background(), b, []Override{ov}, nil)
}

// Default returns p's registered default, so a step can report what a
// quarantined key falls back to.
func (i storeImporter) Default(p Path) (json.RawMessage, error) {
	s := i.s
	s.mu.Lock()
	defer s.mu.Unlock()
	ck, err := s.importKey(p)
	if err != nil {
		return nil, err
	}
	return slices.Clone(ck.def()), nil
}
