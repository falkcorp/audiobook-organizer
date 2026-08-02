// file: internal/server/absauth/audit.go
// version: 1.1.0
// guid: 0e5c72a8-91b3-4f46-8d27-4a08b6e1c937
// last-edited: 2026-08-02

package absauth

import "log/slog"

// AuditEventName is the fixed slog attribute every ABS auth record carries, so the
// whole audit trail is one grep/log-query away: `event=abs.auth`.
const AuditEventName = "abs.auth"

// Outcome classifies an authentication attempt.
type Outcome string

const (
	// OutcomeSuccess — identity resolved and the request proceeds.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailure — bad credential (401): wrong password, unverifiable
	// assertion, unknown or dead refresh token.
	OutcomeFailure Outcome = "failure"
	// OutcomeDenied — credential was VALID but the identity is not authorized
	// (403): a Cloudflare-verified email that is not on the allowlist, an inactive
	// user, or a session that belongs to someone else.
	OutcomeDenied Outcome = "denied"
	// OutcomeThrottled — rejected before any credential work (429).
	OutcomeThrottled Outcome = "throttled"
	// OutcomeError — the server could not decide (5xx). Distinct from failure on
	// purpose: §1.7.3 item 3 requires that a transient error NOT be reported as a
	// dead credential, because a 401 on /auth/refresh force-logs-out the client.
	OutcomeError Outcome = "error"
)

// AuditEvent is one authentication attempt on the ABS surface.
type AuditEvent struct {
	// Action is the logical operation: "login", "refresh", "logout", "resolve".
	Action string
	// Outcome is the classification above.
	Outcome Outcome
	// Mode is which resolver decided: "cf" (Cloudflare Access assertion) or
	// "jwt" (our own bearer token), empty when nothing resolved.
	Mode string
	// SourceIP is the client address. Trustworthy because the router runs with
	// SetTrustedProxies(nil), so X-Forwarded-For cannot spoof it.
	SourceIP string
	// UserID / Username identify the subject when known.
	UserID   string
	Username string
	// Email is the identity claim from a Cloudflare assertion, when present.
	Email string
	// SessionID is the abs_sess record involved, when known.
	SessionID string
	// Reason is a short machine-ish explanation ("bad-password",
	// "assertion-invalid", "email-not-allowed", "refresh-unknown", ...). It must
	// never contain a credential.
	Reason string
	// Path is the request path.
	Path string
	// UserAgent is the client's UA string.
	UserAgent string
	// ServiceToken is the Cloudflare service token's `common_name` (its Client ID)
	// when the request carried one.
	//
	// 🔴 NOT IDENTITY. It names a CREDENTIAL shared by a group of people, never a
	// person, and must never be used to resolve a user — identity comes from SSO.
	// It is recorded for two reasons, and the SECOND is why it belongs on the same
	// line as UserID/Username rather than in a line of its own:
	//
	//  1. per-group activity is visible at all ("has the `testing` token been used
	//     in three weeks?"), including on FAILED attempts, where it is the only
	//     attribution there will ever be — an anonymous assertion never becomes a
	//     UserID;
	//  2. token↔person is normally stable, so `family` token + a friend's SSO
	//     identity is a tripwire for either a compromised Google account or a leaked
	//     token. That anomaly is only visible when both fields appear TOGETHER;
	//     emitting them on separate lines destroys the signal.
	ServiceToken string
}

// Audit writes one structured audit record for an authentication attempt.
//
// Every attempt is logged — success AND failure — with its source IP (spec §3.6).
// Successes are Info; a 5xx is Error; everything else is Warn so failures stand out
// without a log-level change. Credentials (passwords, tokens, assertions) are never
// included: the Reason field is a fixed vocabulary, not an echo of input.
func Audit(ev AuditEvent) {
	attrs := []any{
		"event", AuditEventName,
		"action", ev.Action,
		"outcome", string(ev.Outcome),
		"source_ip", ev.SourceIP,
	}
	if ev.Mode != "" {
		attrs = append(attrs, "mode", ev.Mode)
	}
	if ev.UserID != "" {
		attrs = append(attrs, "user_id", ev.UserID)
	}
	if ev.Username != "" {
		attrs = append(attrs, "username", ev.Username)
	}
	if ev.Email != "" {
		attrs = append(attrs, "email", ev.Email)
	}
	// Emitted right beside user_id/username on purpose — the PAIRING is the signal
	// (see the field comment). Also emitted on failures, where it is the only
	// attribution available.
	if ev.ServiceToken != "" {
		attrs = append(attrs, "service_token", ev.ServiceToken)
	}
	if ev.SessionID != "" {
		attrs = append(attrs, "session_id", ev.SessionID)
	}
	if ev.Reason != "" {
		attrs = append(attrs, "reason", ev.Reason)
	}
	if ev.Path != "" {
		attrs = append(attrs, "path", ev.Path)
	}
	if ev.UserAgent != "" {
		attrs = append(attrs, "user_agent", ev.UserAgent)
	}

	switch ev.Outcome {
	case OutcomeSuccess:
		slog.Info("abs auth attempt", attrs...)
	case OutcomeError:
		slog.Error("abs auth attempt", attrs...)
	default:
		slog.Warn("abs auth attempt", attrs...)
	}
}
