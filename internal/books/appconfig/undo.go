// file: internal/books/appconfig/undo.go
// version: 1.0.0
// guid: 8d3f6a21-5c94-4b7e-a0d8-2e6b9f41c7a3
// last-edited: 2026-11-20

package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// ErrRollbackIncomplete is joined to the cause of a failed Update when undoing
// the steps that had already committed itself failed: the stores no longer agree
// with each other, and the error names which step could not be restored.
var ErrRollbackIncomplete = errors.New("appconfig: the failed update could not be fully undone")

// undoStep is one committed step's compensation.
type undoStep struct {
	what string
	run  func(ctx context.Context) error
}

// undoSet is what the steps of one Update would replace, read before any of
// them runs.
type undoSet struct {
	// keys is the patch that puts core's rows (and registered secret keys) back:
	// a key that was a stored override is set to that value, any other is unset
	// to fall back to its default, file or environment value. A key with a
	// restart pending is put back to its effective value: the stored-but-not-yet
	// effective value the write replaced is not readable through Lookup.
	keys coreconfig.Patch
	// secrets are the unregistered secrets the update puts or clears.
	secrets map[secrets.Name]secretPrior
	// unreadableKeys names the registered secret keys that held a value the store
	// could not decrypt: writing over one is a legitimate repair, but it cannot be
	// restored.
	unreadableKeys []string
}

type secretPrior struct {
	set   bool
	value string
	bad   bool // set, but unreadable
}

// readSecretPrior reads one secret's current state without ever returning its
// value to a caller outside this package.
func (e *Engine) readSecretPrior(ctx context.Context, n secrets.Name) secretPrior {
	v, meta, err := e.acc.Secret(ctx, n)
	switch {
	case err == nil:
		return secretPrior{set: true, value: v.Reveal()}
	case errors.Is(err, secrets.ErrNotSet) && !meta.Set:
		return secretPrior{}
	default:
		return secretPrior{set: true, bad: true}
	}
}

// snapshotForUndo reads, before the first write, what a write would replace.
func (e *Engine) snapshotForUndo(ctx context.Context, patch coreconfig.Patch, sets, clears []secretOp) (*undoSet, error) {
	u := &undoSet{
		keys:    coreconfig.Patch{Set: map[coreconfig.Path]json.RawMessage{}},
		secrets: map[secrets.Name]secretPrior{},
	}
	paths := make([]coreconfig.Path, 0, len(patch.Set)+len(patch.Unset))
	for p := range patch.Set {
		paths = append(paths, p)
	}
	paths = append(paths, patch.Unset...)
	for _, p := range paths {
		st, ok := e.acc.Lookup(p)
		if !ok {
			continue
		}
		if st.Secret != nil {
			prior := e.readSecretPrior(ctx, secrets.Name(p))
			switch {
			case prior.bad:
				u.unreadableKeys = append(u.unreadableKeys, string(p))
			case prior.set:
				raw, err := json.Marshal(prior.value)
				if err != nil {
					return nil, fmt.Errorf("appconfig: reading what %s replaces: %w", p, err)
				}
				u.keys.Set[p] = raw
			default:
				u.keys.Unset = append(u.keys.Unset, p)
			}
			continue
		}
		if st.Source == coreconfig.SourceOverride && st.Effective != nil {
			u.keys.Set[p] = st.Effective
		} else {
			u.keys.Unset = append(u.keys.Unset, p)
		}
	}
	for _, op := range append(append([]secretOp(nil), sets...), clears...) {
		u.secrets[op.name] = e.readSecretPrior(ctx, op.name)
	}
	return u, nil
}

// keysStep is the compensation of the core Apply.
func (u *undoSet) keysStep(e *Engine, as coreconfig.Actor) undoStep {
	return undoStep{what: "the core keys", run: func(ctx context.Context) error {
		if _, err := e.acc.Apply(ctx, u.keys, as); err != nil {
			return err
		}
		if len(u.unreadableKeys) > 0 {
			return fmt.Errorf("%s held a value the store could not read, which cannot be restored", strings.Join(u.unreadableKeys, ", "))
		}
		return nil
	}}
}

// secret is the compensation of one put or clear of an unregistered secret.
func (u *undoSet) secret(e *Engine, name secrets.Name, head, by string) undoStep {
	prior := u.secrets[name]
	return undoStep{what: head, run: func(ctx context.Context) error {
		switch {
		case prior.bad:
			return errors.New("it held a value the store could not read, which cannot be restored")
		case prior.set:
			_, err := e.acc.PutSecret(ctx, name, secrets.NewValue(prior.value), by)
			return err
		default:
			return e.acc.DeleteSecret(ctx, name, by)
		}
	}}
}

// compensate undoes the committed steps, newest first, and returns the cause. A
// step that cannot be undone does not stop the others: what could be restored
// is, and the error names what could not (ErrRollbackIncomplete). The context is
// detached from the request's cancellation: an abandoned request must not leave
// the stores disagreeing.
func (e *Engine) compensate(ctx context.Context, done []undoStep, cause error) error {
	ctx = context.WithoutCancel(ctx)
	var failed []string
	for i := len(done) - 1; i >= 0; i-- {
		if err := done[i].run(ctx); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", done[i].what, err))
		}
	}
	if err := e.Rebuild(ctx); err != nil {
		failed = append(failed, fmt.Sprintf("re-projecting (%v)", err))
	}
	if len(failed) == 0 {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("%w: %s", ErrRollbackIncomplete, strings.Join(failed, "; ")))
}
