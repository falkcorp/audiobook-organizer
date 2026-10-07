// file: internal/server/apikey_expiry_stamp.go
// version: 1.0.0
// guid: 03e89930-38be-47ce-a24e-71825969fb5b
// last-edited: 2026-10-07

package server

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// apiKeyExpiryStampDoneKey records that the one-time expiry stamp for keys
// created before every key had an expiry has run (2026-10-07).
const apiKeyExpiryStampDoneKey = "system:migration:apikey_expiry_stamp_v1_done"

// apiKeyExpiryStampStore is what stampNeverExpiringAPIKeys needs.
type apiKeyExpiryStampStore interface {
	ListAllAPIKeys() ([]database.APIKey, error)
	SetAPIKeyExpiry(id string, at time.Time) error
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// apiKeyExpiryStampResult reports what one stampNeverExpiringAPIKeys call did.
type apiKeyExpiryStampResult struct {
	AlreadyDone bool
	Found       int // non-revoked keys with no expiry
	Stamped     int
	FlagSet     bool
}

// stampNeverExpiringAPIKeys gives every non-revoked API key that has no
// expiry one of now + handlers.DefaultAPIKeyTTL, once, and logs each key it
// finds (id, name, user, created) so the owner can see which integrations
// need a new key before then.
//
// Rules:
//   - Inactive keys are stamped too: reactivating one would otherwise bring
//     back a key that never expires. Revoked keys can never authenticate and
//     are skipped.
//   - The done flag is set only when the listing was complete and every write
//     succeeded; otherwise the next start retries. A retry is harmless: a
//     stamped key no longer has a nil expiry.
//   - Once the flag is set, a never-expiring key found later (a restored
//     database, say) is logged at WARN and left alone.
func stampNeverExpiringAPIKeys(store apiKeyExpiryStampStore, now time.Time) (apiKeyExpiryStampResult, error) {
	var res apiKeyExpiryStampResult
	if store == nil {
		return res, nil
	}
	if s, err := store.GetSetting(apiKeyExpiryStampDoneKey); err == nil && s != nil && s.Value != "" {
		res.AlreadyDone = true
	}

	keys, err := store.ListAllAPIKeys()
	unreadable, partial := database.UnreadableMemberCount(err)
	if err != nil && !partial {
		return res, fmt.Errorf("list api keys: %w", err)
	}

	var never []database.APIKey
	for _, k := range keys {
		if k.ExpiresAt == nil && k.Status != "revoked" && k.RevokedAt == nil {
			never = append(never, k)
		}
	}
	res.Found = len(never)

	if res.AlreadyDone {
		for _, k := range never {
			slog.Warn("api key has no expiry (the one-time expiry stamp already ran; set one by rotating it)",
				"key_id", k.ID, "name", k.Name, "user", k.UserID, "created", k.CreatedAt, "status", k.Status)
		}
		return res, nil
	}

	expiresAt := now.Add(handlers.DefaultAPIKeyTTL)
	if len(never) > 0 {
		slog.Warn("api keys with no expiry found; giving each one a one-time expiry",
			"count", len(never), "expires_at", expiresAt)
	}
	failed := 0
	for _, k := range never {
		if err := store.SetAPIKeyExpiry(k.ID, expiresAt); err != nil {
			failed++
			slog.Error("api key expiry stamp: write failed; will retry next start",
				"key_id", k.ID, "name", k.Name, "user", k.UserID, "err", err)
			continue
		}
		res.Stamped++
		slog.Warn("api key had no expiry; it now expires (create or rotate a replacement before then)",
			"key_id", k.ID, "name", k.Name, "user", k.UserID, "created", k.CreatedAt,
			"status", k.Status, "expires_at", expiresAt)
	}

	if partial {
		slog.Error("api key expiry stamp: some key records could not be read; not marking done, will retry next start",
			"unreadable", unreadable, "err", err)
		return res, nil
	}
	if failed > 0 {
		return res, nil
	}
	if err := store.SetSetting(apiKeyExpiryStampDoneKey, now.UTC().Format(time.RFC3339), "string", false); err != nil {
		return res, fmt.Errorf("record expiry stamp done flag: %w", err)
	}
	res.FlagSet = true
	slog.Info("api key expiry stamp done", "stamped", res.Stamped, "expires_at", expiresAt)
	return res, nil
}

// logBootstrapKeyTTLConfig logs, once at startup, the bootstrap key lifetime
// in effect and any warning about how it was configured (a deprecated days
// value, a cap, an unparseable duration).
func logBootstrapKeyTTLConfig() {
	cfg := config.Snapshot()
	ttl, warning := cfg.ResolveBootstrapKeyTTL()
	if warning != "" {
		slog.Warn("bootstrap key lifetime config: "+warning, "ttl", ttl.String())
		return
	}
	slog.Info("bootstrap key lifetime", "ttl", ttl.String())
}
