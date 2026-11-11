// file: core/config/importer.go
// version: 1.0.4
// guid: 2c9586e1-b071-417e-a1e0-e58dc0945411
// last-edited: 2026-11-11

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// storeImporter is the store's Importer (05-config §1.3.3, §1.12.2).
type storeImporter struct{ s *Store }

// Importer returns config's write side for a one-way cutover step. It is
// valid after Load, when every enabled section is registered, and stages
// through the override store SetImportTarget handed over. Stage is refused
// once Attach has run (05-config §1.3.3): it checks no protection and writes
// no audit record, which is right only for a cutover step before P4d.
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
// nothing (C4), except on a FailClosed key. An invalid value is returned as
// the *FieldError itself, which the step quarantines. Nothing is written
// until the caller commits b.
func (i storeImporter) Stage(b *storage.Batch, p Path, v json.RawMessage, by string) error {
	s := i.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return errClosed
	case s.attached:
		return fmt.Errorf("config: the Importer stages only before Attach; %s is written through Apply now, with its protection checks and audit record", p)
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
	// A value equal to the default is not staged (C4), except on a FailClosed
	// key: there the stored default is what keeps a weaker operator value,
	// set now or later, from taking effect (05-config §1.3.5). R0's
	// enable_auth=true becomes core.auth.mode "required", the default; left
	// unstaged, a stale <APP>_CORE_AUTH_MODE=disabled would open the server.
	if slices.Equal(c, ck.def()) && ck.k.failOrder == nil {
		return nil
	}
	if s.importTarget == nil {
		return errors.New("config: the Importer has no override store: the kernel calls SetImportTarget first")
	}
	if b == nil {
		return errors.New("config: Importer.Stage needs the step's batch")
	}
	ctx := context.Background()
	if len(ck.k.entrySecrets) > 0 {
		if err := s.importClears(ctx, b, ck, c, by); err != nil {
			return err
		}
	}
	ov := Override{Path: p, Value: c, At: s.now(), By: by}
	return s.importTarget.Stage(ctx, b, []Override{ov}, nil)
}

// importClears stages into b the deletion of the secrets of every entry of
// ck's stored row that next (the list being imported) drops or rebinds, as
// Apply does (entryClears). It deletes nothing for an entry next adds: a
// cutover step imports an entry's secret through secrets.Importer, and must
// import the list before or with that secret (seams.d/F3-03.md).
func (s *Store) importClears(ctx context.Context, b *storage.Batch, ck ckey, next json.RawMessage, by string) error {
	var old json.RawMessage
	for ov, err := range s.importTarget.Load(ctx) {
		if err != nil {
			return fmt.Errorf("config: read the stored %s: %w", ck.p, err)
		}
		if ov.Path == ck.p {
			old = ov.Value
		}
	}
	was, now := ck.k.entryBindings(old), ck.k.entryBindings(next)
	var writes []secrets.Write
	for _, id := range ck.k.entryIDsOf(old) {
		if is, ok := now[id]; ok && is == was[id] {
			continue
		}
		for _, d := range ck.k.entrySecrets {
			writes = append(writes, secrets.Write{Name: secrets.Name(d.path(id)), Delete: true, By: by})
		}
	}
	if len(writes) == 0 {
		return nil
	}
	if s.importSecrets == nil {
		return fmt.Errorf("config: importing %s drops or rebinds stored entries whose secrets must be deleted, and the Importer has no secret stager: the kernel calls SetImportSecrets first", ck.p)
	}
	for _, w := range writes {
		if err := s.importSecrets.Stage(ctx, b, w); err != nil {
			return fmt.Errorf("config: delete the secret %s: %w", w.Name, err)
		}
	}
	return nil
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
