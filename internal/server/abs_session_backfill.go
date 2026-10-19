// file: internal/server/abs_session_backfill.go
// version: 1.0.0
// guid: 99f886ee-303a-4fce-8468-154f11f057ca
// last-edited: 2026-10-19

package server

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

// absSessionExpiryBackfiller is the store capability behind
// backfillABSSessionExpiry. PebbleStore implements it.
type absSessionExpiryBackfiller interface {
	BackfillABSSessionExpiry(now time.Time, floor func(createdAt time.Time) time.Time) (int, error)
}

var absSessionBackfillLog = logger.New("abs.session-backfill")

// backfillABSSessionExpiry moves ABS sessions written before sliding expiry onto
// the new schedule at boot, so none of them dies at its old fixed 30-day mark.
//
// Those rows carry ExpiresAt = login+30d, the same instant their access token
// expires. Sliding expiry moves ExpiresAt only on a refresh or an authenticated
// request, so without this a device left unused across that mark would hit
// token-expired, then session-not-live on refresh: the very logout being fixed.
// Raising each live row to cfg.SessionExpiry(CreatedAt, CreatedAt) (login plus the
// idle TTL, capped by the absolute limit when set) gives that 401 a live session to
// refresh against. Idempotent, and it never lowers an expiry, so it runs on every
// boot rather than being tracked as a one-shot migration.
//
// A missing capability or a store error is logged and boot continues: the ABS
// surface works without it, it just leaves those legacy rows on their old expiry.
func backfillABSSessionExpiry(store any, cfg *absauth.Config, now time.Time) {
	if cfg == nil || !cfg.Enabled {
		return
	}
	b, ok := database.AsCapability[absSessionExpiryBackfiller](store)
	if !ok {
		absSessionBackfillLog.Warn("abs: store cannot backfill session expiry; sessions created before sliding expiry keep their fixed 30-day expiry until next used")
		return
	}
	n, err := b.BackfillABSSessionExpiry(now, func(createdAt time.Time) time.Time {
		return cfg.SessionExpiry(createdAt, createdAt)
	})
	if err != nil {
		absSessionBackfillLog.Error("abs: session expiry backfill stopped after %d sessions: %v", n, err)
		return
	}
	if n > 0 {
		absSessionBackfillLog.Info("abs: moved %d existing sessions onto sliding expiry (login + idle TTL)", n)
	}
}
