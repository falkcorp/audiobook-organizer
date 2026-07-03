// file: internal/server/apikey_expiry_sweep.go
// version: 1.0.0
// guid: 481f65a7-e54a-4be2-b43e-d6d992fcbd60
// last-edited: 2026-07-03

package server

import (
	"log/slog"
	"time"
)

const (
	// apiKeyExpirySweepInterval is coarse on purpose — this is observability,
	// not a hot path.
	apiKeyExpirySweepInterval = 6 * time.Hour
	// apiKeyExpiryWarnWindow is how far ahead of expiry a key starts getting
	// warned about.
	apiKeyExpiryWarnWindow = 7 * 24 * time.Hour
)

// warnExpiringAPIKeys is a low-frequency, observability-only background
// sweep (SEC-1/PROC-6): it logs slog.Warn for active API keys approaching
// expiry, and a one-time-per-process deprecation warning for legacy active
// keys that have no expiry at all (ExpiresAt == nil). It NEVER rejects,
// revokes, or otherwise modifies a key — enforcement remains solely in the
// pre-existing middleware check (internal/server/middleware/auth.go,
// `key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt)`), which already
// treats a nil ExpiresAt as "never expires". This function must not change
// that behavior.
//
// It runs forever until s.bgCtx is canceled (intended to be started from
// Server.Start via `s.bgWG.Add("apikey-expiry-sweep"); go func() { defer
// s.bgWG.Done("apikey-expiry-sweep"); s.warnExpiringAPIKeys() }()`, mirroring
// the other long-lived store-touching goroutines) ticking every
// apiKeyExpirySweepInterval and logging slog.Warn for:
//   - active keys with ExpiresAt == nil (legacy, never-expiring keys) — once
//     per key ID per process, so restarts re-warn but a long-running process
//     doesn't spam every tick.
//   - active keys with ExpiresAt != nil that are within apiKeyExpiryWarnWindow
//     of expiring (and not yet expired) — once per key ID until expiry is
//     observed to have moved (e.g. the key was rotated), tracked by the
//     expiry timestamp itself so a rotation naturally re-arms the warning.
//
// This goroutine owns its state exclusively (no shared mutable state with
// other goroutines beyond the store), so no mutex is needed.
func (s *Server) warnExpiringAPIKeys() {
	store := s.Store()
	if store == nil {
		return
	}

	// warnedLegacy tracks key IDs already warned about having no expiry at
	// all. warnedExpiring maps key ID -> the ExpiresAt value last warned
	// about, so a rotation (which changes ExpiresAt) re-arms the warning.
	warnedLegacy := map[string]bool{}
	warnedExpiring := map[string]time.Time{}

	ticker := time.NewTicker(apiKeyExpirySweepInterval)
	defer ticker.Stop()

	sweep := func() {
		keys, err := store.ListAllAPIKeys()
		if err != nil {
			slog.Warn("apikey expiry sweep: failed to list keys", "err", err)
			return
		}
		now := time.Now()
		for _, k := range keys {
			if k.Status != "active" {
				continue
			}
			if k.ExpiresAt == nil {
				if !warnedLegacy[k.ID] {
					warnedLegacy[k.ID] = true
					slog.Warn("api key has no expiry (legacy) — set one via rotate",
						"key_id", k.ID, "user_id", k.UserID, "name", k.Name)
				}
				continue
			}
			if now.After(*k.ExpiresAt) {
				// Already expired — the middleware handles rejection; the
				// sweep has nothing new to warn about for this key.
				continue
			}
			if k.ExpiresAt.Sub(now) > apiKeyExpiryWarnWindow {
				continue
			}
			if last, ok := warnedExpiring[k.ID]; ok && last.Equal(*k.ExpiresAt) {
				continue
			}
			warnedExpiring[k.ID] = *k.ExpiresAt
			slog.Warn("api key approaching expiry",
				"key_id", k.ID, "user_id", k.UserID, "name", k.Name,
				"expires_at", k.ExpiresAt.Format(time.RFC3339))
		}
	}

	for {
		select {
		case <-s.bgCtx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
