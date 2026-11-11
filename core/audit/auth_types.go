// file: core/audit/auth_types.go
// version: 2.0.0
// guid: 0e5c72a8-91b3-4f46-8d27-4a08b6e1c937
// last-edited: 2026-11-10

package audit

import (
	"context"
	"log/slog"
)

// Throttled is the outcome of an authentication attempt refused before any
// credential work (429). The other outcomes of an attempt are Success,
// Failure (a bad credential, 401), Denied (a valid credential whose identity
// is not authorized, 403) and Error (the server could not decide, 5xx:
// distinct from Failure on purpose, because a 401 on a token refresh logs the
// client out, so a transient error must never read as a dead credential).
const Throttled Outcome = "throttled"

// AuthAttempt is one authentication attempt on an app compatibility surface
// that authenticates itself (05-authn §4.10; moved from ABO's
// internal/server/absauth/audit.go, 04 §8.5 F4). It is a log record, not a
// durable audit event: every attempt, success and failure, is logged with its
// source address. No field ever holds a credential: Reason is a fixed
// vocabulary, never an echo of input.
type AuthAttempt struct {
	// Action is the logical operation: "login", "refresh", "logout", "resolve".
	Action  string
	Outcome Outcome
	// Mode is which resolver decided (the surface's own vocabulary), empty
	// when nothing resolved.
	Mode string
	// SourceIP is the client address as the surface resolved it.
	SourceIP string
	// UserID and Username identify the subject when known.
	UserID   string
	Username string
	// Email is the identity claim of an asserted identity, when present.
	Email string
	// SessionID names the surface's session record involved, when known.
	SessionID string
	// Reason is a short machine-ish explanation ("bad-password",
	// "assertion-invalid", "refresh-unknown", ...).
	Reason string
	// Path is the request path.
	Path string
	// UserAgent is the client's user agent.
	UserAgent string
	// ServiceToken names a shared service credential (an access-proxy service
	// token's client ID) when the request carried one. It is NOT identity: it
	// names a credential a group shares, never a person, and is never used to
	// resolve a user. It is recorded on the same line as UserID and Username
	// on purpose: a stable token-to-person pairing that changes is the signal
	// of a compromised account or a leaked token, and it is the only
	// attribution a failed anonymous attempt will ever have.
	ServiceToken string
}

// AuthAttemptLog writes one surface's attempt records. The zero Log is the
// process's default logger at the time of each Record (slog.Default), which
// is where the surface logged before the move.
type AuthAttemptLog struct {
	// Log receives the records; nil is slog.Default().
	Log *slog.Logger
	// Event is the fixed "event" attribute every record of the surface
	// carries, so its whole trail is one log query away.
	Event string
	// Message is the record's message.
	Message string
}

// Record writes ev: Info on Success, Error on Error, Warn otherwise, so
// failures stand out without a level change. Every string is passed through
// the log barrier (CR, LF and controls escaped).
func (l AuthAttemptLog) Record(ev AuthAttempt) {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	attrs := []any{
		"event", l.Event,
		"action", sanitizeLogValue(ev.Action),
		"outcome", sanitizeLogValue(string(ev.Outcome)),
		"source_ip", sanitizeLogValue(ev.SourceIP),
	}
	for _, f := range []struct{ key, val string }{
		{"mode", ev.Mode},
		{"user_id", ev.UserID},
		{"username", ev.Username},
		{"email", ev.Email},
		// Right beside user_id and username: the pairing is the signal.
		{"service_token", ev.ServiceToken},
		{"session_id", ev.SessionID},
		{"reason", ev.Reason},
		{"path", ev.Path},
		{"user_agent", ev.UserAgent},
	} {
		if f.val != "" {
			attrs = append(attrs, f.key, sanitizeLogValue(f.val))
		}
	}
	level := slog.LevelWarn
	switch ev.Outcome {
	case Success:
		level = slog.LevelInfo
	case Error:
		level = slog.LevelError
	}
	log.Log(context.Background(), level, l.Message, attrs...)
}
