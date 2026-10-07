// file: internal/auth/owner.go
// version: 1.0.0
// guid: 3b8e1d47-6f2a-4c95-a0d3-9e7c4b5f2a18
// last-edited: 2026-10-07

package auth

import (
	"context"
	"strings"
)

// OWNER PROOF (owner decision 2026-10-07). An owner-only action (Repairs
// owner apply) is honoured only for a request that carries a VERIFIED
// Cloudflare Access JWT whose email is the configured owner_email. A password,
// OAuth, temp-login or invite session is never enough, and neither is the
// unsigned Cf-Access-Authenticated-User-Email header.
//
// WHY Access and nothing else: every other identity is one this server can
// create or reset itself — a password can be changed or stolen, an admin can
// be created, an OAuth allowlist edited, a temp-login minted. The Access
// identity is issued by Cloudflare against the owner's own IdP account; the
// server can only verify it, never make one. So a stolen password or a newly
// created admin cannot pass this check.

// WithAccessEmail records the email of the verified Cloudflare Access JWT the
// request carried. Only the Access middleware calls it, after verifying the
// JWT; an empty email clears it (a later stage that replaced the identity,
// such as an API key, must not inherit it).
func WithAccessEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, accessEmailKey, strings.TrimSpace(email))
}

// AccessEmailFromContext returns the email WithAccessEmail recorded, or "".
func AccessEmailFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	e, _ := ctx.Value(accessEmailKey).(string)
	return e
}

// OwnerProofWhyNot returns "" when the request in ctx is the owner: its auth
// method is cf_access and its verified Access email equals ownerEmail
// (case-insensitively). Otherwise it returns why not, in words for the UI.
// host is the public host to name in the sign-in hint. It fails closed: an
// unset ownerEmail refuses everyone.
func OwnerProofWhyNot(ctx context.Context, ownerEmail, host string) string {
	owner := strings.TrimSpace(ownerEmail)
	if owner == "" {
		return "Owner actions are turned off: no owner email is configured (owner_email; set it from a signed-in session)"
	}
	if MethodFromContext(ctx) != MethodCFAccess {
		hint := "Cloudflare Access"
		if host != "" {
			hint += " (" + host + ")"
		}
		return "Owner actions need you to sign in through " + hint + "; a password, SSO, temp-login or invite session, or an API key, is not enough"
	}
	email := AccessEmailFromContext(ctx)
	if email == "" || !strings.EqualFold(email, owner) {
		return "Owner actions need the owner's Cloudflare Access sign-in; this request is signed in as " + orUnknown(email)
	}
	return ""
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown identity"
	}
	return s
}
