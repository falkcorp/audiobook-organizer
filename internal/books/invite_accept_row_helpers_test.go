// file: internal/books/invite_accept_row_helpers_test.go
// version: 0.1.0
// guid: b17e4d09-3a62-4c85-9f10-d8e2a5c76b31
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// Helpers of the accepted-invite row cells (F4-08 B10x2). Every name starts with
// ac so a sibling step's helpers in this package cannot collide with them.

const (
	acKate = "kate.kane@example.test" // a HEAD account that typed this address in unproven
	acLuke = "luke.fox@example.test"
	acWill = "will.magnus@example.test"
)

// acSeedHead is the HEAD store every ac cell boots over: the first administrator and
// one account that holds an address unproven (so a mailed invitation for that address
// displaces it). seed adds a cell's own rows (HEAD's used invites).
func acSeedHead(t *testing.T, s *database.PebbleStore, ids map[string]string, seed func(*database.PebbleStore, map[string]string)) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(gpPass), bcrypt.MinCost)
	require.NoError(t, err)
	ids[gpAdminName] = seUser(t, s, gpAdminName, gpAdminMail, "setup", []string{"admin"}, "bcrypt", string(hash))
	ids["kate.kane"] = seUser(t, s, "kate.kane", acKate, "", []string{"viewer"}, "bcrypt", string(hash))
	if seed != nil {
		seed(s, ids)
	}
}

// acBoot is one flipped boot over a seeded HEAD store (M3 runs on it), with Cloudflare
// faked and the guest-policy settings in place: gpBoot with its own, smaller seed.
func acBoot(t *testing.T, seed func(*database.PebbleStore, map[string]string)) *gpEnv {
	t.Helper()
	cf := newGPCF()
	gpInstallCF(t, cf)
	ids := map[string]string{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { acSeedHead(t, s, ids, seed) }, "")
	tap := &gpTap{}
	r := runComposedIn(t, fx.stateDir, func(cfg *config.Config) {
		cfg.DatabasePath = fx.dbPath
		cfg.RootDir = fx.dbDir
		cfg.EnableAuth = true
	}, nil, func(o *app.Options) {
		o.StorageFS = storage.FS{}
		o.StateDir = fx.stateDir
		o.Env = []string{
			storagePathEnv + "=" + fx.dbPath,
			"ABO_BOOKS_CFGUESTS_ACCOUNT_ID=" + gpCFAccount,
			"ABO_BOOKS_CFGUESTS_GUESTS_POLICY_ID=" + gpCFPolicy,
			"ABO_BOOKS_CFGUESTS_POLICY_API_TOKEN=" + gpCFToken,
			"ABO_BOOKS_IDENTITY_OWNER_EMAIL=" + gpAdminMail,
		}
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(fx.stateDir), fx.key, 0o600))
	}, nil, func(m module.Module) module.Module { return gpTapModule{Module: m, tap: tap} })
	require.Eventually(t, func() bool {
		ready, err := shapedReady(r.composed)
		require.NoError(t, err)
		return ready
	}, 2*time.Minute, 50*time.Millisecond, "/health never reported ready")
	done, err := r.app.StorageAdmin().DataMigrationDone(context.Background(), cutover.IdentityCutoverID)
	require.NoError(t, err)
	require.True(t, done, "M3 did not run on the seeded store: the fixture proves nothing")
	return &gpEnv{runningApp: r, cf: cf, tap: tap, ids: ids, admin: idLogin(t, r.app.Handler(), gpAdminName, gpPass)}
}

// acRow is books' accepted-invite row for an invite id, nil when there is none, read
// as the bytes the store holds.
func acRow(t *testing.T, e *gpEnv, inviteID string) *database.InviteAccept {
	t.Helper()
	ps := database.AsPebbleStore(e.store)
	require.NotNil(t, ps, "no Pebble store behind the app")
	raw, err := ps.GetRaw("acceptedinvite:" + inviteID)
	require.NoError(t, err)
	if raw == nil {
		return nil
	}
	var a database.InviteAccept
	require.NoError(t, json.Unmarshal(raw, &a), string(raw))
	return &a
}

// acCoreUser is core's own record of an account, through the actor-checked admin read
// (the principal is test-built, as markDelivered's is).
func acCoreUser(t *testing.T, e *gpEnv, userID string) *coreauth.User {
	t.Helper()
	admin := e.tap.adminHandle()
	require.NotNil(t, admin, "core handed the module no AuthAdmin")
	u, err := admin.AdminUser(gpAdminCtx(context.Background(), e.ids[gpAdminName]), coreauth.UserID(userID))
	require.NoError(t, err)
	return u
}

// acAcceptedUserID is the id of the account an accept created, by username.
func acAcceptedUserID(t *testing.T, e *gpEnv, username string) string {
	t.Helper()
	return gpUser(t, e, username)
}
