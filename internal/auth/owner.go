// file: internal/auth/owner.go
// version: 1.2.0
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
// request carried, exactly as the JWT states it (no trimming or folding: what
// is compared must be what Cloudflare signed). Only the Access middleware
// calls it, after verifying the JWT; an empty email clears it (a later stage
// that replaced the identity, such as an API key, must not inherit it).
func WithAccessEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, accessEmailKey, email)
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
// method is cf_access and its verified Access email is ownerEmail
// (IsOwnerEmail). Otherwise it returns why not, in words for the UI.
// host is the public host to name in the sign-in hint. It fails closed: an
// unset ownerEmail refuses everyone.
func OwnerProofWhyNot(ctx context.Context, ownerEmail, host string) string {
	if strings.TrimSpace(ownerEmail) == "" {
		return "Owner actions are turned off: no owner email is configured (set OWNER_EMAIL on the server, or set owner_email while signed in through Cloudflare Access as that email)"
	}
	if MethodFromContext(ctx) != MethodCFAccess {
		hint := "Cloudflare Access"
		if host != "" {
			hint += " (" + host + ")"
		}
		return "Owner actions need you to sign in through " + hint + "; a password, SSO, temp-login or invite session, or an API key, is not enough"
	}
	email := AccessEmailFromContext(ctx)
	if !IsOwnerEmail(email, ownerEmail) {
		return "Owner actions need the owner's Cloudflare Access sign-in; this request is signed in as " + orUnknown(email)
	}
	return ""
}

// IsOwnerEmail reports whether claim, the email a verified Cloudflare Access
// JWT states, names the configured owner mailbox. It is the one comparison
// every owner check uses (the request proof here and the grant re-check when
// repairs.apply redeems a grant).
//
// ASCII only, ASCII case only. It used strings.EqualFold until the 2026-10-07
// review: EqualFold applies Unicode simple folding, under which U+212A KELVIN
// SIGN equals "k" and U+017F LONG S equals "s", so a DIFFERENT IdP account
// named "\u212Aate@example.test" passed as the owner "kate@example.test". Any
// byte outside printable ASCII on either side now refuses, the claim is never
// trimmed (Cloudflare signed it as is), and only the configured value has its
// surrounding ASCII spaces removed. An empty value on either side refuses, so
// an unset owner never matches an empty claim.
func IsOwnerEmail(claim, ownerEmail string) bool {
	owner := strings.Trim(ownerEmail, " \t\r\n")
	if claim == "" || owner == "" || len(claim) != len(owner) {
		return false
	}
	for i := 0; i < len(claim); i++ {
		a, b := claim[i], owner[i]
		if a <= ' ' || a > '~' || b <= ' ' || b > '~' {
			return false
		}
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown identity"
	}
	return s
}
