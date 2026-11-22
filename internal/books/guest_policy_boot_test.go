// file: internal/books/guest_policy_boot_test.go
// version: 0.1.0
// guid: 4f2a9c71-5d03-4b86-a1e7-9b60d83c2e45
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// The Cloudflare Access guest policy against core's account events, at full boot
// (F4-08 B10r): a real kernel, core's real accounts, invites, sessions and merge,
// books' registered auth.Lifecycle, and a fake only of Cloudflare's API.
//
// One flipped boot over a seeded HEAD store answers every case, in order, each its
// own named subtest. Each guest of the fixture is a HEAD account whose accept-time
// sync added their address to the guest policy (the grant's cf_added), so the
// first thing every removal below proves is that the fact survived the cutover
// (M3 carries it into books' own guestpolicy: row). Every address is on
// example.test and every person a Batman character.

const (
	gpAdminName = "bruce.wayne"
	gpAdminMail = "bruce.wayne@example.test"
	gpSecondAdm = "lucius.fox"
	gpPass      = "gordon-oracle-batcave-1939!"

	gpSelina  = "selina.kyle@example.test"       // disabled
	gpHarvey  = "harvey.dent@example.test"       // deleted
	gpOswald  = "oswald.cobblepot@example.test"  // changes their own address
	gpEdward  = "edward.nygma@example.test"      // has their address changed by an admin
	gpPamela  = "pamela.isley@example.test"      // disabled, but another verified holder keeps it
	gpHarleen = "harleen.quinzel@example.test"   // disabled, the other holder is unverified
	gpAlfred  = "alfred.pennyworth@example.test" // disabled, on the Access allow-list
	gpDamian  = "damian.wayne@example.test"      // disabled, the owner's address
	gpTalia   = "talia.alghul@example.test"      // disabled while Cloudflare refuses the removal
	gpJim     = "jim.gordon@example.test"        // put on the policy by hand: no cf_added
	gpBarbara = "barbara.gordon@example.test"    // accepts a mailed invitation
	gpDick    = "dick.grayson@example.test"      // accepts an invitation that was not mailed
	gpJason   = "jason.todd@example.test"        // accepts, creator demoted before
	gpTim     = "tim.drake@example.test"         // accepts while Cloudflare refuses the add
)

// gpHeadGuests are the seeded HEAD guests: username, address.
var gpHeadGuests = [][2]string{
	{"selina.kyle", gpSelina}, {"harvey.dent", gpHarvey}, {"oswald.cobblepot", gpOswald},
	{"edward.nygma", gpEdward}, {"pamela.isley", gpPamela}, {"harleen.quinzel", gpHarleen},
	{"alfred.pennyworth", gpAlfred}, {"damian.wayne", gpDamian}, {"talia.alghul", gpTalia},
}

func gpSeed(t *testing.T, s *database.PebbleStore, ids map[string]string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(gpPass), bcrypt.MinCost)
	require.NoError(t, err)
	admin, viewer := []string{"admin"}, []string{"viewer"}
	ids[gpAdminName] = seUser(t, s, gpAdminName, gpAdminMail, "setup", admin, "bcrypt", string(hash))
	ids[gpSecondAdm] = seUser(t, s, gpSecondAdm, "lucius.fox@example.test", "setup", admin, "bcrypt", string(hash))
	grant := func(email, userID string, cfAdded bool) {
		raw, err := json.Marshal(map[string]any{"user_id": userID, "granted_at": "2026-11-10T11:00:00Z", "cf_added": cfAdded})
		require.NoError(t, err)
		require.NoError(t, s.DB().Set([]byte("inviteallow:"+email), raw, pebble.Sync))
	}
	for _, g := range gpHeadGuests {
		ids[g[0]] = seUser(t, s, g[0], g[1], "invite", viewer, "bcrypt", string(hash))
		grant(g[1], ids[g[0]], true)
	}
	// A second holder of an address: proven for pamela (so her entry is kept) and
	// unproven for harleen (typed into a profile, which proves nothing).
	ids["poison.ivy"] = seUser(t, s, "poison.ivy", gpPamela, "admin", viewer, "bcrypt", string(hash))
	ids["harley.quinn"] = seUser(t, s, "harley.quinn", gpHarleen, "", viewer, "bcrypt", string(hash))
	// Jim's address was put on the policy by hand: HEAD's grant for him has no cf_added.
	ids["jim.gordon"] = seUser(t, s, "jim.gordon", gpJim, "invite", viewer, "bcrypt", string(hash))
	grant(gpJim, ids["jim.gordon"], false)
	// The Access allow-list is a stored setting M2 carries into the books key.
	require.NoError(t, s.SetSetting("config_blob", `{"oauth_allowed_emails":"`+gpAlfred+`"}`, "json", false))
}

type gpEnv struct {
	*runningApp
	cf    *gpCF
	tap   *gpTap
	ids   map[string]string // username -> core user id (M3 keeps HEAD's ids)
	admin *http.Cookie      // bruce.wayne's session
}

func gpBoot(t *testing.T) *gpEnv {
	t.Helper()
	cf := newGPCF(gpSelina, gpHarvey, gpOswald, gpEdward, gpPamela, gpHarleen, gpAlfred, gpDamian, gpTalia, gpJim)
	gpInstallCF(t, cf)
	ids := map[string]string{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { gpSeed(t, s, ids) }, "")
	t.Cleanup(SetIdentityFlipForTest(true))
	tap := &gpTap{}
	r := runComposedIn(t, fx.stateDir, func(cfg *config.Config) {
		cfg.DatabasePath = fx.dbPath
		cfg.RootDir = fx.dbDir
		cfg.EnableAuth = true
	}, nil, func(o *app.Options) {
		o.StorageFS = storage.FS{}
		o.StateDir = fx.stateDir
		// Under the flip the Cloudflare settings and the owner's address are operator
		// settings of core's layered config, whose environment layer is the App's
		// Env (not the test process's).
		o.Env = []string{
			storagePathEnv + "=" + fx.dbPath,
			"ABO_BOOKS_CFGUESTS_ACCOUNT_ID=" + gpCFAccount,
			"ABO_BOOKS_CFGUESTS_GUESTS_POLICY_ID=" + gpCFPolicy,
			"ABO_BOOKS_CFGUESTS_POLICY_API_TOKEN=" + gpCFToken,
			"ABO_BOOKS_IDENTITY_OWNER_EMAIL=" + gpDamian,
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

func (e *gpEnv) h() http.Handler { return e.app.Handler() }

func (e *gpEnv) gp(t *testing.T) database.GuestPolicyStore {
	t.Helper()
	gp, ok := database.AsCapability[database.GuestPolicyStore](e.store)
	require.True(t, ok, "the store has no guest-policy capability")
	return gp
}

// row is books' bookkeeping for an address, nil when there is none.
func (e *gpEnv) row(t *testing.T, email string) *database.InviteEmailGrant {
	t.Helper()
	g, err := e.gp(t).GuestPolicyGrants().GetInviteEmailGrant(email)
	require.NoError(t, err)
	return g
}

func (e *gpEnv) outcome(t *testing.T, inviteID string) *database.GuestPolicyOutcome {
	t.Helper()
	o, err := e.gp(t).GetGuestPolicyOutcome(inviteID)
	require.NoError(t, err)
	return o
}

func (e *gpEnv) disable(t *testing.T, username string) {
	t.Helper()
	rec := idDo(t, e.h(), http.MethodPost, "/api/core/v1/users/"+e.ids[username]+"/disable", nil, e.admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// logs is what the app logged about guest access or a lifecycle hook, for a
// failure message: the callbacks never fail an account event, so a callback that
// went wrong shows only here.
func (e *gpEnv) logs() string {
	var out []string
	for _, l := range strings.Split(e.newLog.String(), "\n") {
		if strings.Contains(l, "guest") || strings.Contains(l, "lifecycle") || strings.Contains(l, "invite accept") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// gone waits for the background revoke to take the address out of the policy.
func (e *gpEnv) gone(t *testing.T, email string) {
	t.Helper()
	require.Eventually(t, func() bool { return !e.cf.has(email) }, 20*time.Second, 25*time.Millisecond,
		"%s is still on the Cloudflare guest policy; log:\n%s", email, e.logs())
}

// invite is POST /users/invites as ck for an address; it returns the invite's public
// id and the token its link carries.
func (e *gpEnv) invite(t *testing.T, ck *http.Cookie, username, email string) (id, token string) {
	t.Helper()
	var created struct {
		Link   string `json:"link"`
		Invite struct {
			ID string `json:"id"`
		} `json:"invite"`
	}
	rec := gpDoJSON(t, e.h(), http.MethodPost, "/api/core/v1/users/invites",
		map[string]string{"username": username, "email": email, "role": "viewer"}, ck, &created)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotEmpty(t, created.Invite.ID, rec.Body.String())
	i := strings.LastIndex(created.Link, "token=")
	require.GreaterOrEqual(t, i, 0, created.Link)
	return created.Invite.ID, created.Link[i+len("token="):]
}

// markDelivered is what the mailer does after the mail went out: an administrator
// marks the invite as emailed to its address. The principal is test-built (the
// route books' mailer calls is B10t's).
func (e *gpEnv) markDelivered(t *testing.T, inviteID string) {
	t.Helper()
	admin := e.tap.adminHandle()
	require.NotNil(t, admin, "core handed the module no AuthAdmin")
	require.NoError(t, admin.MarkInviteEmailDelivered(gpAdminCtx(context.Background(), e.ids[gpAdminName]), inviteID))
}

func (e *gpEnv) accept(t *testing.T, token string) {
	t.Helper()
	rec := post(t, e.h(), "/api/core/v1/auth/invites/accept", map[string]string{"token": token, "password": gpPass}, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func (e *gpEnv) waitOutcome(t *testing.T, inviteID, status string) *database.GuestPolicyOutcome {
	t.Helper()
	var o *database.GuestPolicyOutcome
	require.Eventually(t, func() bool {
		o = e.outcome(t, inviteID)
		return o != nil && o.Status == status
	}, 20*time.Second, 25*time.Millisecond, "invite %s never recorded the outcome %q (last: %+v); log:\n%s", inviteID, status, o, e.logs())
	return o
}

func TestGuestPolicyIdentity_FullBoot(t *testing.T) {
	e := gpBoot(t)
	h := e.h()

	// The coordinator's acceptance test: a HEAD store holding a guest the sync added
	// to Cloudflare is cut over, the guest is disabled under core identity, and
	// Cloudflare sees the removal. Without M3's carry of cf_added the revoke finds
	// no record that books added the address, and leaves it.
	t.Run("01_cutover_of_a_CFAdded_guest_then_disable_removes_the_cloudflare_entry", func(t *testing.T) {
		g := e.row(t, gpSelina)
		require.NotNil(t, g, "M3 left no guest-policy record of the cf_added grant")
		require.True(t, g.CFAdded)
		require.Equal(t, e.ids["selina.kyle"], g.UserID)
		require.True(t, e.cf.has(gpSelina), "the fixture's policy does not hold the guest: the removal would prove nothing")
		sess := idLogin(t, h, "selina.kyle", gpPass)

		e.disable(t, "selina.kyle")

		e.gone(t, gpSelina)
		assert.Nil(t, e.row(t, gpSelina), "the bookkeeping outlived the revoke")
		assert.Contains(t, e.cf.removals(), gpSelina)
		me := idDo(t, h, http.MethodGet, "/api/core/v1/me", nil, sess)
		assert.Equal(t, http.StatusUnauthorized, me.Code, "a disabled guest's session is still accepted: %s", me.Body.String())
	})

	t.Run("02_books_registered_exactly_one_lifecycle", func(t *testing.T) {
		require.NotNil(t, e.tap.lifecycle, "core handed no lifecycle handle to the module")
		require.ErrorIs(t, e.tap.again, coreauth.ErrLifecycleRegistered,
			"a second registration was accepted: books registered no lifecycle of its own")
	})

	t.Run("03_deleting_a_guest_removes_the_cloudflare_entry", func(t *testing.T) {
		rec := idDo(t, h, http.MethodDelete, "/api/core/v1/users/"+e.ids["harvey.dent"], nil, e.admin)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		e.gone(t, gpHarvey)
		assert.Nil(t, e.row(t, gpHarvey))
	})

	t.Run("04_a_guest_changing_their_own_address_removes_the_old_entry", func(t *testing.T) {
		sess := idLogin(t, h, "oswald.cobblepot", gpPass)
		rec := idDo(t, h, http.MethodPatch, "/api/core/v1/me", map[string]string{"email": "oswald.penguin@example.test"}, sess)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		e.gone(t, gpOswald)
		assert.Nil(t, e.row(t, gpOswald))
	})

	t.Run("05_an_admin_changing_a_guests_address_removes_the_old_entry", func(t *testing.T) {
		admin := e.tap.adminHandle()
		require.NotNil(t, admin)
		require.NoError(t, admin.SetUserEmail(gpAdminCtx(context.Background(), e.ids[gpAdminName]),
			coreauth.UserID(e.ids["edward.nygma"]), "edward.riddler@example.test"))
		e.gone(t, gpEdward)
		assert.Nil(t, e.row(t, gpEdward))
	})

	t.Run("06_another_verified_holder_keeps_the_entry", func(t *testing.T) {
		e.disable(t, "pamela.isley")
		// The bookkeeping is the disabled account's and goes; the entry is the other
		// holder's reason to be there and stays (asserted at the end, once every
		// background revoke has drained).
		assert.Nil(t, e.row(t, gpPamela))
	})

	t.Run("07_an_unverified_holder_keeps_nothing", func(t *testing.T) {
		e.disable(t, "harleen.quinzel")
		e.gone(t, gpHarleen)
	})

	t.Run("08_the_access_allow_list_keeps_the_entry", func(t *testing.T) {
		e.disable(t, "alfred.pennyworth")
		assert.Nil(t, e.row(t, gpAlfred))
	})

	t.Run("09_the_owners_address_keeps_the_entry", func(t *testing.T) {
		e.disable(t, "damian.wayne")
		assert.Nil(t, e.row(t, gpDamian))
	})

	t.Run("10_an_address_added_by_hand_is_never_removed", func(t *testing.T) {
		require.Nil(t, e.row(t, gpJim), "a grant without cf_added got a record")
		e.disable(t, "jim.gordon")
	})

	t.Run("11_a_cloudflare_failure_on_removal_is_logged_and_never_fails_the_disable", func(t *testing.T) {
		e.cf.setFail(true)
		t.Cleanup(func() { e.cf.setFail(false) })
		before := e.cf.failed()
		e.disable(t, "talia.alghul") // 200: the account event is not undone by Cloudflare
		require.Eventually(t, func() bool { return e.cf.failed() > before }, 20*time.Second, 25*time.Millisecond,
			"the removal never reached Cloudflare")
		e.cf.setFail(false)
		assert.Nil(t, e.row(t, gpTalia), "HEAD deletes the grant before it calls Cloudflare")
		assert.True(t, e.cf.has(gpTalia), "the entry vanished though Cloudflare refused the write")
	})

	t.Run("12_a_mailed_invitation_accepted_adds_the_address", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "barbara.gordon", gpBarbara)
		e.markDelivered(t, id)
		e.accept(t, tok)
		o := e.waitOutcome(t, id, database.InviteGuestPolicyAdded)
		assert.Equal(t, gpUser(t, e, "barbara.gordon"), o.UserID)
		assert.True(t, e.cf.has(gpBarbara))
		require.Eventually(t, func() bool {
			g := e.row(t, gpBarbara)
			return g != nil && g.CFAdded
		}, 20*time.Second, 25*time.Millisecond, "the add was not noted on the bookkeeping, so a later revoke could not remove it")
	})

	t.Run("13_an_invitation_that_was_not_mailed_is_skipped", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "dick.grayson", gpDick)
		e.accept(t, tok)
		o := e.waitOutcome(t, id, database.InviteGuestPolicySkipped)
		assert.Contains(t, o.Detail, "not emailed")
		assert.False(t, e.cf.has(gpDick))
		assert.Nil(t, e.row(t, gpDick))
	})

	// Core refuses the link of an invite whose creator lost admin before it was
	// accepted, so HEAD's "created by someone without the admin role" outcome has no
	// route to books through core: the accept never happens. (The callback still
	// holds the gate, pinned in the server package.)
	t.Run("14_an_invitation_whose_creator_lost_admin_cannot_be_accepted", func(t *testing.T) {
		second := idLogin(t, h, gpSecondAdm, gpPass)
		id, tok := e.invite(t, second, "jason.todd", gpJason)
		e.markDelivered(t, id)
		rec := idDo(t, h, http.MethodPut, "/api/core/v1/users/"+e.ids[gpSecondAdm]+"/roles", map[string]any{"roles": []string{"viewer"}}, e.admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		acc := post(t, h, "/api/core/v1/auth/invites/accept", map[string]string{"token": tok, "password": gpPass}, false)
		require.Equal(t, http.StatusUnauthorized, acc.Code, acc.Body.String())
		// Core's refusal, by name: HEAD accepted such an invitation and skipped the
		// grant; core invalidates it (a known difference, nothing to grant or revoke).
		assert.Contains(t, acc.Body.String(), "link_invalid")
		assert.Nil(t, e.outcome(t, id))
		assert.False(t, e.cf.has(gpJason))
		assert.Nil(t, e.row(t, gpJason))
	})

	t.Run("15_a_cloudflare_failure_on_add_is_recorded_and_never_fails_the_accept", func(t *testing.T) {
		e.cf.setFail(true)
		t.Cleanup(func() { e.cf.setFail(false) })
		id, tok := e.invite(t, e.admin, "tim.drake", gpTim)
		e.markDelivered(t, id)
		e.accept(t, tok) // 200: the account exists whatever Cloudflare says
		e.waitOutcome(t, id, database.InviteGuestPolicyFailed)
		e.cf.setFail(false)
		assert.False(t, e.cf.has(gpTim))
		assert.Equal(t, "active", gpStatus(t, e, "tim.drake"))
	})

	// Every revoke and add above is a background goroutine; the app's stop waits for
	// them, so what is left is the final state, where a kept entry is proven kept.
	require.NoError(t, e.stop())

	t.Run("16_final_policy_holds_exactly_the_kept_and_the_hand_added_and_the_new", func(t *testing.T) {
		want := []string{gpAlfred, gpBarbara, gpDamian, gpJim, gpPamela, gpTalia}
		slices.Sort(want)
		assert.Equal(t, want, e.cf.addresses())
		removed := e.cf.removals()
		slices.Sort(removed)
		wantRemoved := []string{gpEdward, gpHarleen, gpHarvey, gpOswald, gpSelina}
		slices.Sort(wantRemoved)
		assert.Equal(t, wantRemoved, removed, "Cloudflare saw a removal it should not have, or missed one")
		added := e.cf.additions()
		assert.Equal(t, []string{gpBarbara}, added, "Cloudflare saw an add it should not have")
	})
}

// gpUser is a core user's id by username, from the admin's listing.
func gpUser(t *testing.T, e *gpEnv, username string) string {
	t.Helper()
	return idUserID(t, e.h(), e.admin, username)
}

// gpStatus is a core user's status by username.
func gpStatus(t *testing.T, e *gpEnv, username string) string {
	t.Helper()
	rec := idDo(t, e.h(), http.MethodGet, "/api/core/v1/users/"+gpUser(t, e, username), nil, e.admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var u struct {
		Status string `json:"status"`
		Data   struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &u), rec.Body.String())
	return u.Status + u.Data.Status
}

// Every event core reports is one books acts on: a nil field would be a hole the
// next core change walks through unnoticed.
func TestGuestPolicyLifecycle_EveryEventHasACallback(t *testing.T) {
	lc := (*server.Server)(nil).AccountLifecycle()
	v := reflect.ValueOf(lc)
	require.Equal(t, 5, v.NumField(), "core's Lifecycle changed shape: books has not decided what to do with the new event")
	for i := 0; i < v.NumField(); i++ {
		assert.False(t, v.Field(i).IsNil(), "Lifecycle.%s is not set", v.Type().Field(i).Name)
	}
}

func TestGuestPolicyWire_NoHandleIsARefusalNeverASkip(t *testing.T) {
	require.ErrorIs(t, registerAccountLifecycle(module.Deps{}, nil), errAccountLifecycle)
}

// Under the flip a module core hands no lifecycle handle is refused at boot, with
// the rest of the identity handles: a guest's Cloudflare entry would otherwise
// outlive their account with nothing to say so.
func TestGuestPolicyBoot_RefusesWithoutALifecycleHandle(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	mod, opts, _ := composedOptions(t, false, "", func(c *config.Config) { c.EnableAuth = true }, nil)
	a, err := app.New(t.Context(), opts, gpNoLifecycle{mod})
	require.Error(t, err, "app.New booted without a lifecycle handle")
	require.Nil(t, a)
	Release(mod)
	require.ErrorIs(t, err, errIdentityDeps)
	require.Contains(t, err.Error(), "AuthLifecycle")
}

// gpMerged is the guest of the merge test: accepted through a mailed invitation, so
// the sync added their address by itself in this boot.
const gpMerged = "dick.grayson@example.test"

// A guest merged into another account is gone for good: core disables the source
// and clears its address, and books takes the address out of the guest policy.
// This is its own boot: with an owner configured (the big boot's) the merge route
// wants an owner-verified Access sign-in, which this harness does not mint.
func TestGuestPolicyIdentity_MergedAwayGuestLosesTheCloudflareEntry(t *testing.T) {
	// The client never empties the policy: one address put there by hand stays.
	cf := newGPCF(gpJim)
	gpInstallCF(t, cf)
	t.Setenv("ABO_BOOKS_CFGUESTS_ACCOUNT_ID", gpCFAccount)
	t.Setenv("ABO_BOOKS_CFGUESTS_GUESTS_POLICY_ID", gpCFPolicy)
	t.Setenv("ABO_BOOKS_CFGUESTS_POLICY_API_TOKEN", gpCFToken)
	t.Cleanup(SetIdentityFlipForTest(true))
	tap := &gpTap{}
	r := runComposedWith(t, func(cfg *config.Config) { cfg.EnableAuth = true }, nil, nil, nil,
		func(m module.Module) module.Module { return gpTapModule{Module: m, tap: tap} })
	h := r.app.Handler()
	adminName, adminPass := redeemBootstrap(t, r.composed)
	ck := idLogin(t, h, adminName, adminPass)
	e := &gpEnv{runningApp: r, cf: cf, tap: tap, ids: map[string]string{}, admin: ck}
	e.ids[gpAdminName] = idUserID(t, h, ck, adminName)

	// The survivor has an address of its own, so the merge drops the source's. (A
	// survivor with none adopts the source's verified address, and the entry then
	// stays: an active verified holder still has it, which is the keep rule.)
	me := idDo(t, h, http.MethodPatch, "/api/core/v1/me", map[string]string{"email": gpAdminMail}, ck)
	require.Equal(t, http.StatusOK, me.Code, me.Body.String())

	id, tok := e.invite(t, ck, "dick.grayson", gpMerged)
	e.markDelivered(t, id)
	e.accept(t, tok)
	e.waitOutcome(t, id, database.InviteGuestPolicyAdded)
	require.True(t, cf.has(gpMerged), "the guest was never added: the removal would prove nothing")
	guest := gpUser(t, e, "dick.grayson")

	path := "/api/v1/users/" + guest + "/merge-into/" + e.ids[gpAdminName]
	var plan struct {
		Digest string `json:"digest"`
		Data   struct {
			Digest string `json:"digest"`
		} `json:"data"`
	}
	pv := gpDoJSON(t, h, http.MethodPost, path+"/preview", nil, ck, &plan)
	require.Equal(t, http.StatusOK, pv.Code, pv.Body.String())
	ap := idDo(t, h, http.MethodPost, path+"/apply", map[string]any{"digest": plan.Digest + plan.Data.Digest}, ck)
	require.Equal(t, http.StatusAccepted, ap.Code, ap.Body.String())
	um := r.srv.UserMerge()
	require.NotNil(t, um)
	require.Eventually(t, func() bool {
		js, err := um.ListJournals()
		return err == nil && len(js) == 1 && js[0].Status == "applied"
	}, 60*time.Second, 100*time.Millisecond, "the merge never applied")

	e.gone(t, gpMerged)
	assert.Nil(t, e.row(t, gpMerged))
	assert.Equal(t, []string{gpMerged}, cf.removals())
}
