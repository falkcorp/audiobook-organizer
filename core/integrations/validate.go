// file: core/integrations/validate.go
// version: 1.0.1
// guid: 05883661-d0f7-454b-b1c0-94be20c84612
// last-edited: 2026-11-19

package integrations

import (
	"fmt"
	"maps"
)

// SettingsValidator is a kind's own rule over an instance's settings, the
// part of validation the adapter's schema cannot express (the ai kind's
// "no wildcard capability tick, no unknown capability a Put adds"). The
// Registry runs it on every write of an instance of that kind, so no caller
// has to remember to (F7-03 shipped Router.ValidateEndpoint and nothing
// called it).
//
// settings is the effective non-secret layer the write would serve (the
// stored layer with the operator overlay applied); stored is what the row
// being replaced serves now (its stored layer with the operator overlay
// applied, so a value an operator locks is in it), nil for a create. A rule
// that grandfathers a value the row already holds reads it from stored. Both
// are the Registry's own copies; the validator must not retain or modify them. adapter names the
// instance's adapter, so a kind with one adapter-specific rule can tell.
//
// Any error is a refusal. The Registry makes it an ErrInvalid (a 422 on the
// instance routes) while keeping the validator's own error in the chain, so a
// validator needs no knowledge of this package's errors.
type SettingsValidator func(adapter AdapterID, settings, stored map[string]any) error

// SetValidator installs kind's validator. A kind has at most one: a second
// is ErrInvalid, because two owners of one rule is a wiring bug and silently
// replacing the first would drop its rule. It is valid at any time, before or
// after Load, because the owner of a kind's rule (ai's Router) is built from
// the Registry and so cannot exist before it.
//
// The validator runs on Put, on Test and, through ImportValidator, on an
// import. It never runs on the serve path: a row already stored is served
// whatever a validator would say of it (M1.4 step 2), so installing one
// refuses no existing instance.
func (r *Registry) SetValidator(kind Kind, v SettingsValidator) error {
	switch {
	case !kind.Valid():
		return fmt.Errorf("%w: a validator for kind %q, which is not one of ai, download, indexer, metadata, notify", ErrInvalid, string(kind))
	case v == nil:
		return fmt.Errorf("%w: a nil validator for kind %s", ErrInvalid, kind)
	}
	r.vmu.Lock()
	defer r.vmu.Unlock()
	if r.validators[kind] != nil {
		return fmt.Errorf("%w: kind %s already has a settings validator", ErrInvalid, kind)
	}
	if r.validators == nil {
		r.validators = map[Kind]SettingsValidator{}
	}
	r.validators[kind] = v
	return nil
}

// validatorFor is kind's validator, nil for none.
func (r *Registry) validatorFor(kind Kind) SettingsValidator {
	r.vmu.RLock()
	defer r.vmu.RUnlock()
	return r.validators[kind]
}

// runValidator applies kind's validator, if any. Every refusal wraps
// ErrInvalid and the validator's own error.
func runValidator(v SettingsValidator, adapter AdapterID, settings, stored map[string]any) error {
	if v == nil {
		return nil
	}
	if err := v(adapter, maps.Clone(settings), maps.Clone(stored)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// validateImport applies the kind's validator to an imported record. A record
// whose adapter this binary does not register, or whose adapter is of another
// kind than its ref, is not judged: the Registry keeps and reports it
// (M1.4 step 2). The validator sees the non-secret fields only, and no stored
// row (an import is create-only).
func (r *Registry) validateImport(c InstanceConfig) error {
	r.mu.Lock()
	a := r.adapters[c.Adapter]
	r.mu.Unlock()
	if a == nil || a.desc.meta.Kind != c.Ref.Kind {
		return nil
	}
	v := r.validatorFor(c.Ref.Kind)
	if v == nil {
		return nil
	}
	plain := make(map[string]any, len(c.Settings))
	for f, val := range c.Settings {
		if !a.fs.isSecret(f) {
			plain[f] = val
		}
	}
	return runValidator(v, c.Adapter, plain, nil)
}
