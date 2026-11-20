// file: core/backup/authorize.go
// version: 1.0.1
// guid: d6a3f8e1-4b92-4c07-b5e0-9a1c7d2f3e58
// last-edited: 2026-11-12

package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRestoreNotAuthorized: the caller asked to stage a restore or a revert and
// gave no proof of who is asking. 05-backup §5B.3.4 names the API phase "task
// core.backup.stage-restore (POST /api/core/v1/backups/{id}/restore, permission
// core:backup.restore (Sensitive, AuthRequired, 05-rbac), credential-change
// route with owner proof", and §5B.3.8 gives POST /restore/revert the same
// "core:backup.restore (Sensitive, AuthRequired) + Guards.Interactive +
// Guards.Owner". So every staging needs the interactive owner proof the
// restore routes establish, a plain one included, and a foreign restore needs
// core:admin as well.
var ErrRestoreNotAuthorized = errors.New("backup: staging a restore needs an interactive owner proof")

// RestoreProof is what the caller of StageRestore or RevertLastRestore
// established about the person who asked. The zero value is "nothing proven",
// and nothing proven stages nothing: holding core:backup.restore is the route's
// and the task's start check, not proof of the owner.
//
// Only a caller that has itself enforced the route's guards sets it: the
// restore routes of core/internal/backuphttp, after Guards.Interactive and
// Guards.Owner (or the 05-authn §4.8 fallback). The core.backup.stage-restore
// task cannot carry an interactive owner proof, so it never sets one and every
// restore it asks for is refused here, in the one place every caller passes.
type RestoreProof struct {
	Owner bool // an interactive credential with the owner's assurance (or the §4.8 fallback)
	Admin bool // the person holds core:admin
}

type proofKey struct{}

// WithRestoreProof returns a context that carries p for the Service's gate.
//
// It is a trust boundary: the Service believes the proof it finds, because
// nothing here can check one. The proof lives in the context, so any code that
// holds a request's context (a middleware, a goroutine started from a handler,
// a plugin handed the context) can mint a stronger one with this function, and
// nothing in the type system stops it. Call it only from the code that has
// itself enforced the route's guards, as the restore routes of
// core/internal/backuphttp do, on the context passed straight to the Service
// and never on one that outlives or is shared beyond that call. A task, a
// scheduled job, a module's hook and a CLI that has not proved its caller must
// not call it, and a review that finds another caller should treat it as an
// authorization bypass.
func WithRestoreProof(ctx context.Context, p RestoreProof) context.Context {
	return context.WithValue(ctx, proofKey{}, p)
}

// RestoreProofFrom is the proof ctx carries, the zero value when none.
func RestoreProofFrom(ctx context.Context) RestoreProof {
	p, _ := ctx.Value(proofKey{}).(RestoreProof)
	return p
}

// privileged names the options of o that plant or accept something an
// ordinary restore does not, in a fixed order, for the refusal's text.
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

// authorizeRestore is the one gate on staging a restore or a revert. It runs
// before the backup resource is taken or anything is read, so a refused caller
// learns nothing about the archives. Fail closed: without the owner proof
// nothing is staged, whatever the options and whoever the caller is; a foreign
// restore also needs core:admin.
func authorizeRestore(ctx context.Context, o RestoreOptions) error {
	p := RestoreProofFrom(ctx)
	if !p.Owner {
		if opts := privileged(o); len(opts) > 0 {
			return fmt.Errorf("%w: %s", ErrRestoreNotAuthorized, strings.Join(opts, ", "))
		}
		return ErrRestoreNotAuthorized
	}
	if o.Foreign && !p.Admin {
		return fmt.Errorf("%w: foreign also needs core:admin", ErrRestoreNotAuthorized)
	}
	return nil
}
