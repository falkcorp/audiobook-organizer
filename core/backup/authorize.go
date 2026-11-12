// file: core/backup/authorize.go
// version: 1.0.0
// guid: d6a3f8e1-4b92-4c07-b5e0-9a1c7d2f3e58
// last-edited: 2026-11-12

package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRestoreNotAuthorized: the caller asked for restore options that need
// proof of who is asking, and gave none. The options are Foreign, ReplaceConfig,
// AcceptUnreadableSecrets and AllowWeakVerify; each of them plants or accepts
// something an ordinary restore does not, so each needs the interactive owner
// proof the restore route establishes (05-backup §5B.3.8), and Foreign needs
// core:admin as well.
var ErrRestoreNotAuthorized = errors.New("backup: this restore option needs an interactive owner proof")

// RestoreProof is what the caller of StageRestore or RevertLastRestore
// established about the person who asked. The zero value is "nothing proven":
// an ordinary restore needs no proof from this package (the caller's own gate
// said who may restore at all), but every privileged option does.
//
// Only a caller that has itself enforced the route's guards sets it: the
// restore routes of core/internal/backuphttp, after Guards.Interactive and
// Guards.Owner (or the 05-authn §4.8 fallback). The core.backup.stage-restore
// task cannot carry an interactive owner proof, so it never sets one and its
// privileged options are refused here, in the one place every caller passes.
type RestoreProof struct {
	Owner bool // an interactive credential with the owner's assurance (or the §4.8 fallback)
	Admin bool // the person holds core:admin
}

type proofKey struct{}

// WithRestoreProof returns a context that carries p for the Service's gate.
func WithRestoreProof(ctx context.Context, p RestoreProof) context.Context {
	return context.WithValue(ctx, proofKey{}, p)
}

// RestoreProofFrom is the proof ctx carries, the zero value when none.
func RestoreProofFrom(ctx context.Context) RestoreProof {
	p, _ := ctx.Value(proofKey{}).(RestoreProof)
	return p
}

// privileged names the options of o that need proof, in a fixed order.
func privileged(o RestoreOptions) []string {
	var out []string
	if o.Foreign {
		out = append(out, "foreign")
	}
	if o.ReplaceConfig {
		out = append(out, "replace_config")
	}
	if o.AcceptUnreadableSecrets {
		out = append(out, "accept_unreadable_secrets")
	}
	if o.AllowWeakVerify {
		out = append(out, "allow_weak_verify")
	}
	return out
}

// authorizeOptions is the one gate on restore options. It runs before the
// backup resource is taken or anything is read, so a refused caller learns
// nothing about the archives. Fail closed: an option without its proof is
// refused, whoever the caller is.
func authorizeOptions(ctx context.Context, o RestoreOptions) error {
	opts := privileged(o)
	if len(opts) == 0 {
		return nil
	}
	p := RestoreProofFrom(ctx)
	if !p.Owner {
		return fmt.Errorf("%w: %s", ErrRestoreNotAuthorized, strings.Join(opts, ", "))
	}
	if o.Foreign && !p.Admin {
		return fmt.Errorf("%w: foreign also needs core:admin", ErrRestoreNotAuthorized)
	}
	return nil
}
