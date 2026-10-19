// file: internal/server/abs_session_backfill_test.go
// version: 1.0.0
// guid: d1fc921f-b28d-416b-840d-a23c93b17c6c
// last-edited: 2026-10-19

package server

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// wireABSRoutes resolves these through AsCapability and os.Exits at boot when one
// is missing, so a signature drift must fail the build, not production.
var (
	_ abshandler.Store                  = (*database.PebbleStore)(nil)
	_ servermiddleware.ABSIdentityStore = (*database.PebbleStore)(nil)
	_ absSessionExpiryBackfiller        = (*database.PebbleStore)(nil)
)

func TestBackfillABSSessionExpiry_RaisesLegacyRowsToLoginPlusIdleTTL(t *testing.T) {
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name   string
		maxTTL string
		want   time.Duration // from CreatedAt
	}{
		{name: "uncapped", want: 60 * day},
		{name: "capped", maxTTL: "1080h", want: 45 * day},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := database.NewPebbleStoreInMemory("db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: "0123456789abcdef0123456789abcdef", SessionMaxTTL: tc.maxTTL})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			created := now.Add(-29 * day)
			if err := store.CreateABSSession(&database.ABSSession{
				ID: "s1", UserID: "u1", RefreshTokenHash: "h1", Generation: 1,
				CreatedAt: created, LastUsedAt: created, ExpiresAt: created.Add(30 * day),
			}); err != nil {
				t.Fatal(err)
			}

			backfillABSSessionExpiry(store, cfg, now)

			s, _ := store.GetABSSession("s1")
			if want := created.Add(tc.want); !s.ExpiresAt.Equal(want) {
				t.Fatalf("ExpiresAt = %v, want login+%v = %v", s.ExpiresAt, tc.want, want)
			}
		})
	}
}
