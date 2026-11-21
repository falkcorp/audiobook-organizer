// file: core/kernel/verified_method.go
// version: 0.1.0
// guid: 2f9a6c31-7d48-4e05-b1c3-58e0d4a7f962
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"iter"
	"slices"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// verifiedMethodRule is the verified-method rule the kernel hands
// taskhttp.Deps.VerifiedMethod once core auth is wired (F4-08 A11, dark until
// B3; 05-rbac §4.10 "Approval", SEC-24, today's rule at opsv3/runs.go:395-410).
// It lifts F4-05's model of it (core/internal/taskhttp/realauth_test.go
// kernelRule) into the kernel, built from what the kernel holds: the app's
// registered identity extensions and auth's key records.
//
// A principal verifies when it is
//
//   - an interactive session (auth.Method.Interactive);
//   - a method of an identity extension registered with
//     MethodSpec.Interactive (Cloudflare Access);
//   - an API key whose own record names one of the requirement's scopes
//     literally: never through core:admin, never through an implication, never
//     a key the principal's user does not own.
//
// Everything else does not: no principal or no user, a delegated session, the
// auth-disabled principal, a non-interactive extension, a module's compat
// method, an unknown method, and a key whose record cannot be found or read
// (fail closed). keys is auth.Service.APIKeys. taskhttp calls the rule without a
// context, so the key scan runs under context.Background(); it is one owner's
// keys, read from the local store.
func verifiedMethodRule(extensions []auth.Extension, keys func(ctx context.Context, owner auth.UserID) iter.Seq2[*auth.APIKey, error]) func(*auth.Principal, rbac.Requirement) bool {
	interactive := make(map[auth.Method]bool, len(extensions))
	for _, e := range extensions {
		if e.Spec.Interactive {
			interactive[e.Spec.Method] = true
		}
	}
	return func(p *auth.Principal, need rbac.Requirement) bool {
		switch {
		case p == nil || p.User == nil:
			return false
		case p.Method.Interactive(), interactive[p.Method]:
			return true
		case p.Method != auth.MethodAPIKey || keys == nil || p.CredentialID == "":
			return false
		}
		for k, err := range keys(context.Background(), p.User.ID) {
			if err != nil {
				return false
			}
			if k.ID == p.CredentialID {
				return k.Owner == p.User.ID && slices.ContainsFunc(need.Permissions(), func(s rbac.Permission) bool {
					return slices.Contains(k.Scopes, s)
				})
			}
		}
		return false
	}
}
