// file: internal/server/identity_count_bypass_test.go
// version: 0.1.0
// guid: 6d2f8b41-9a37-4c05-b8e2-1f74c0a95d36
// last-edited: 2026-11-28

package server

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
)

// Every cell here runs over a books store holding no user row, which is what
// M3 leaves on every migrated install: HEAD's count-gated first-run branches
// would all open on it. Under core identity none may (F4-08 fix-forward; the
// sites #5061 shipped with the count bypass reachable).

// coreAccounts is core's read-only identity view over a fixed set of users.
type coreAccounts map[coreauth.UserID]*coreauth.User

func (a coreAccounts) User(_ context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if u, ok := a[id]; ok {
		return u, nil
	}
	return nil, coreauth.ErrNotFound
}
func (a coreAccounts) UserByUsername(context.Context, string) (*coreauth.User, error) {
	return nil, errors.New("not used")
}
func (a coreAccounts) Users(context.Context, coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(func(*coreauth.User, error) bool) {}
}
func (a coreAccounts) PrincipalFrom(ctx context.Context) (*coreauth.Principal, bool) {
	return coreauth.PrincipalFrom(ctx)
}

// errCoreRead is a read of core's accounts that failed for a reason other than
// "no such account" (a store outage, not a missing user).
var errCoreRead = errors.New("core account store: connection refused")

// flakyAccounts is coreAccounts whose read of the accounts in failing fails
// with errCoreRead rather than ErrNotFound.
type flakyAccounts struct {
	coreAccounts
	failing map[coreauth.UserID]bool
}

func (a flakyAccounts) User(ctx context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if a.failing[id] {
		return nil, errCoreRead
	}
	return a.coreAccounts.User(ctx, id)
}

// roleCompat is core's Compat whose PrincipalFor compiles each user's current
// roles to the scopes roles names, as core's rbac would.
type roleCompat struct {
	adoptCompat
	roles map[coreauth.UserID][]rbac.Permission
}

func (c *roleCompat) PrincipalFor(_ context.Context, u *coreauth.User, _ []rbac.Permission, epoch int64, _ coreauth.ClientInfo) (*coreauth.Principal, error) {
	// Record what core was asked, as adoptCompat does: the credential epoch
	// is what core checks the account's session generation against.
	c.users = append(c.users, u.ID)
	c.epochs = append(c.epochs, epoch)
	scopes, ok := c.roles[u.ID]
	if !ok {
		return nil, coreauth.ErrInactiveUser
	}
	return &coreauth.Principal{User: u, Method: "books.abs", Perms: adoptChecker(scopes)}, nil
}

const (
	gordonID  = "01JGORDON0000000000000000A" // admin: holds users.manage and library.view
	robinID   = "01JROBIN00000000000000000B" // viewer: library.view only
	selinaID  = "01JSELINA0000000000000000C" // disabled in core
	barbaraID = "01JBARBARA000000000000000E" // pending in core (not yet activated)
	ivyID     = "01JIVY0000000000000000000F" // an account whose read in core fails

	gordonEpoch int64 = 7 // Gordon's credential epoch in core: not the zero value
)

// drainedWorld is a siteWorld over a store with no user row whose identity
// reads core's accounts: Gordon (admin), Robin (viewer), Selina (disabled) and
// Barbara (pending).
func drainedWorld(t *testing.T) *siteWorld {
	t.Helper()
	w := newSiteWorld(t, false, true, string(auth.PermLibraryView), string(auth.PermUsersManage))
	n, err := w.store.CountUsers()
	require.NoError(t, err)
	require.Zero(t, n, "precondition: books' store holds no user row, as M3 leaves it")
	w.id.Users = flakyAccounts{failing: map[coreauth.UserID]bool{ivyID: true}, coreAccounts: coreAccounts{
		gordonID:  {ID: gordonID, Username: "gordon", Status: coreauth.StatusActive, CredentialEpoch: gordonEpoch},
		robinID:   {ID: robinID, Username: "robin", Status: coreauth.StatusActive},
		selinaID:  {ID: selinaID, Username: "selina", Status: coreauth.StatusDisabled},
		barbaraID: {ID: barbaraID, Username: "barbara", Status: coreauth.StatusPending},
	}}
	w.id.Compat = &roleCompat{roles: map[coreauth.UserID][]rbac.Permission{
		gordonID: {"test:" + rbac.Permission(auth.PermUsersManage), "test:" + rbac.Permission(auth.PermLibraryView)},
		robinID:  {"test:" + rbac.Permission(auth.PermLibraryView)},
		// Selina's roles still compile here, so the cell sees the active-status
		// check itself refuse her, not the compat handle.
		selinaID: {"test:" + rbac.Permission(auth.PermLibraryView)},
		// Barbara's and Ivy's too: only status, or the failed read, refuses them.
		barbaraID: {"test:" + rbac.Permission(auth.PermUsersManage), "test:" + rbac.Permission(auth.PermLibraryView)},
		ivyID:     {"test:" + rbac.Permission(auth.PermUsersManage), "test:" + rbac.Permission(auth.PermLibraryView)},
	}}
	return w
}

// TestCountBypass_UnmarkedContextHoldsNothing: permHolds and the ops v3 Holds
// on a server under core identity, asked about a context the identity's Bind
// did not mark, hold nothing; HEAD's chain on the same empty store holds
// everything (its first-run bypass), which is the branch they must not reach.
func TestCountBypass_UnmarkedContextHoldsNothing(t *testing.T) {
	w := newSiteWorld(t, false, true, string(auth.PermLibraryView))
	anon := ginOf(context.Background())
	require.True(t, w.head.permHolds()(anon, auth.PermLibraryView), "precondition: HEAD's first-run bypass holds on an empty store")
	assert.False(t, w.core.permHolds()(anon, auth.PermLibraryView), "permHolds took HEAD's first-run bypass under core identity")
	assert.False(t, w.core.opsV3Authz().Holds(ginOf(context.Background()), auth.PermLibraryView),
		"the ops v3 Holds took HEAD's first-run bypass under core identity")
}

// TestCountBypass_ConfigFirstRunIsClosed: under core identity there is no
// first-run config window, whatever books' (drained) user count says; HEAD's
// window is open on the same store.
func TestCountBypass_ConfigFirstRunIsClosed(t *testing.T) {
	w := newSiteWorld(t, false, true)
	require.False(t, config.Snapshot().SetupComplete, "precondition: setup is not complete")
	require.True(t, w.head.configFirstRun(), "precondition: HEAD's window is open on an empty store")
	assert.False(t, w.core.configFirstRun(), "the first-run config window opened under core identity")
}

// TestCountBypass_OpActorHoldsResolvesTheActorThroughCore: a job's actor is
// read through core and holds what its current roles hold in core, capped at
// what its request held. No first-run bypass (books' empty store), no
// widening, and a demotion narrows. A job with no actor, or a disabled or
// unknown one, is refused with its reason rather than run narrowed to nothing
// (a diagnostics export would otherwise finish with an empty archive). The
// admin positive case proves the path is not simply closed.
func TestCountBypass_OpActorHoldsResolvesTheActorThroughCore(t *testing.T) {
	w := drainedWorld(t)
	ctx := context.Background()
	stamped := []string{string(auth.PermUsersManage), string(auth.PermLibraryView)}
	op := func(id string, actor *string, perms []string) string {
		now := time.Now().UTC()
		require.NoError(t, w.store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
			Params: `{}`, ActorUserID: actor, ActorPermissions: perms, QueuedAt: now,
		}))
		return id
	}
	gordon, robin, selina, barbara, ivy, ghost := gordonID, robinID, selinaID, barbaraID, ivyID, "01JGHOST00000000000000000D"

	holds := func(opID string) func(auth.Permission) bool {
		t.Helper()
		h, err := w.core.opActorHolds(ctx, opID)
		require.NoError(t, err)
		return h
	}
	admin := holds(op("op-gordon", &gordon, stamped))
	assert.True(t, admin(auth.PermUsersManage), "an admin's job lost users.manage")
	assert.True(t, admin(auth.PermLibraryView))

	viewer := holds(op("op-robin", &robin, stamped))
	assert.False(t, viewer(auth.PermUsersManage), "a viewer's job held users.manage: a first-run bypass or no role cap")
	assert.True(t, viewer(auth.PermLibraryView))

	capped := holds(op("op-gordon-narrow", &gordon, []string{string(auth.PermLibraryView)}))
	assert.False(t, capped(auth.PermUsersManage), "the job held more than its request did")

	// A stamped EMPTY set (non-nil, no elements) is a request that held
	// nothing: it caps the job at nothing, where only an unstamped row (nil,
	// written before 2026-10-26) is capped by the roles alone.
	nothing := holds(op("op-gordon-nothing", &gordon, []string{}))
	assert.False(t, nothing(auth.PermUsersManage), "a request that held nothing queued a job that held users.manage")
	assert.False(t, nothing(auth.PermLibraryView), "a request that held nothing queued a job that held library.view")
	unstamped := holds(op("op-gordon-unstamped", &gordon, nil))
	assert.True(t, unstamped(auth.PermUsersManage), "an unstamped row is capped by the roles alone, as HEAD caps it")

	// A job no account can be resolved for fails with the reason, never runs
	// narrowed to nothing (an export would "succeed" with an empty archive).
	for name, tc := range map[string]struct {
		opID string
		want error
	}{
		"no actor": {op("op-none", nil, stamped), errNoExportActor},
		"disabled": {op("op-selina", &selina, stamped), errExportActorNotStanding},
		"pending":  {op("op-barbara", &barbara, stamped), errExportActorNotStanding},
		"unknown":  {op("op-ghost", &ghost, stamped), errExportActorNotStanding},
	} {
		h, err := w.core.opActorHolds(ctx, tc.opID)
		assert.ErrorIsf(t, err, tc.want, "%s actor: the job was not refused with its reason", name)
		assert.Nilf(t, h, "%s actor: a permission check came back with the refusal", name)
	}

	// A failed read of the account is an error, not "holds nothing" and not
	// "no such account": core could not say, so the job fails with the read
	// error rather than finishing with an empty archive or running on a guess.
	h, err := w.core.opActorHolds(ctx, op("op-ivy", &ivy, stamped))
	require.Error(t, err, "a failed read of the actor's account did not fail the job")
	assert.ErrorIs(t, err, errCoreRead)
	assert.NotErrorIs(t, err, errExportActorNotStanding, "a read failure was reported as an account that does not stand")
	assert.Nil(t, h, "a permission check came back with the read failure")
}

// TestCountBypass_OpActorHoldsRefusesAPrincipalWithNoChecker: a principal core
// hands back with no permission checker is a job no account can be resolved
// for (errExportActorNotStanding), never an empty permission set that lets the
// export finish with an empty archive.
func TestCountBypass_OpActorHoldsRefusesAPrincipalWithNoChecker(t *testing.T) {
	w := drainedWorld(t)
	w.id.Compat = &adoptCompat{p: &coreauth.Principal{Method: "books.abs"}}
	gordon := gordonID
	require.NoError(t, w.store.InsertOperationV2(database.OperationV2Row{
		ID: "op-nochecker", DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
		Params: `{}`, ActorUserID: &gordon, ActorPermissions: []string{string(auth.PermUsersManage)}, QueuedAt: time.Now().UTC(),
	}))
	h, err := w.core.opActorHolds(context.Background(), "op-nochecker")
	require.Error(t, err, "a principal with no checker resolved to an empty permission set")
	assert.ErrorIs(t, err, errExportActorNotStanding)
	assert.Nil(t, h)
}

// TestCountBypass_DiagnosticsDownloadWithNoUserViewIsAnError: an identity given
// no read of core's accounts cannot check the account an export names, so the
// download is an error (500), not served unchecked.
func TestCountBypass_DiagnosticsDownloadWithNoUserViewIsAnError(t *testing.T) {
	w := drainedWorld(t)
	w.id.Users = nil
	narrowed := []string{string(auth.PermUsersManage)}
	res := diagnostics.ExportResult{ZipPath: "/nonexistent.zip", NarrowedTo: &narrowed}
	gordon := gordonID
	gordonU := &database.User{ID: gordonID, Username: "gordon", Status: "active"}
	wide, wideC := adoptPrincipal(coreauth.MethodSession, gordonU, "test:"+rbac.Permission(auth.PermUsersManage))
	refusal, err := w.core.diagnosticsDownloadCheck()(ginOf(w.ctxOf(true, nil, wide, wideC)),
		&database.OperationV2Row{ID: "op-export", ActorUserID: &gordon}, res)
	require.Error(t, err, "an export was served without reading the account it was built for")
	assert.ErrorIs(t, err, errNoCoreUserView)
	assert.Empty(t, refusal)
}

// TestCountBypass_OpActorHoldsAsksCoreAtTheAccountsEpoch: the job's principal
// is compiled at the account's current credential epoch in core, not at zero:
// core checks the epoch against the account's session generation.
func TestCountBypass_OpActorHoldsAsksCoreAtTheAccountsEpoch(t *testing.T) {
	w := drainedWorld(t)
	compat, ok := w.id.Compat.(*roleCompat)
	require.True(t, ok)
	gordon := gordonID
	now := time.Now().UTC()
	require.NoError(t, w.store.InsertOperationV2(database.OperationV2Row{
		ID: "op-epoch", DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
		Params: `{}`, ActorUserID: &gordon, ActorPermissions: []string{string(auth.PermUsersManage)}, QueuedAt: now,
	}))
	_, err := w.core.opActorHolds(context.Background(), "op-epoch")
	require.NoError(t, err)
	assert.Equal(t, []coreauth.UserID{gordonID}, compat.users)
	assert.Equal(t, []int64{gordonEpoch}, compat.epochs, "the principal was compiled at a credential epoch other than the account's")
}

// TestCountBypass_DiagnosticsDownloadNeedsTheRecordedScope: under core
// identity an export narrowed to users.manage is refused to a caller without
// it (no first-run allowance on books' empty store), served to one with it,
// refused when its account is disabled or gone in core, and allowed for core's
// auth-disabled principal (core's auth off, not books' EnableAuth).
func TestCountBypass_DiagnosticsDownloadNeedsTheRecordedScope(t *testing.T) {
	w := drainedWorld(t)
	check := w.core.diagnosticsDownloadCheck()
	narrowed := []string{string(auth.PermUsersManage)}
	res := diagnostics.ExportResult{ZipPath: "/nonexistent.zip", NarrowedTo: &narrowed}
	gordon, selina, barbara, ivy, ghost := gordonID, selinaID, barbaraID, ivyID, "01JGHOST00000000000000000D"
	row := func(actor *string) *database.OperationV2Row {
		return &database.OperationV2Row{ID: "op-export", ActorUserID: actor}
	}
	robinU := &database.User{ID: robinID, Username: "robin", Status: "active"}
	gordonU := &database.User{ID: gordonID, Username: "gordon", Status: "active"}
	narrow, narrowC := adoptPrincipal(coreauth.MethodSession, robinU, "test:"+rbac.Permission(auth.PermLibraryView))
	wide, wideC := adoptPrincipal(coreauth.MethodSession, gordonU, "test:"+rbac.Permission(auth.PermUsersManage))

	refusal, err := check(ginOf(w.ctxOf(true, nil, narrow, narrowC)), row(&gordon), res)
	require.NoError(t, err)
	assert.NotEmpty(t, refusal, "a caller without users.manage was served an export narrowed to it")

	refusal, err = check(ginOf(w.ctxOf(true, nil, nil, nil)), row(&gordon), res)
	require.NoError(t, err)
	assert.NotEmpty(t, refusal, "an anonymous caller was served an export")

	refusal, err = check(ginOf(w.ctxOf(true, nil, wide, wideC)), row(&gordon), res)
	require.NoError(t, err)
	assert.Empty(t, refusal, "the admin was refused its own export")

	// Only an active account's export is served: disabled and pending are both
	// refused, as is one that is gone, each with a refusal and no error.
	for name, actor := range map[string]*string{"disabled": &selina, "pending": &barbara, "gone": &ghost} {
		refusal, err = check(ginOf(w.ctxOf(true, nil, wide, wideC)), row(actor), res)
		require.NoErrorf(t, err, "%s", name)
		assert.NotEmptyf(t, refusal, "an export built for a %s account was served", name)
	}

	// A failed read of the account (not "not found") is an error the download
	// answers 500 to: nothing is served on a guess.
	refusal, err = check(ginOf(w.ctxOf(true, nil, wide, wideC)), row(&ivy), res)
	require.Error(t, err, "a failed read of the export's account did not fail the download check")
	assert.ErrorIs(t, err, errCoreRead)
	assert.Empty(t, refusal, "a refusal came back beside the read error")

	// An export narrowed to two permissions is served only to a caller who
	// holds every one of them, not the first.
	both := []string{string(auth.PermLibraryView), string(auth.PermUsersManage)}
	two := diagnostics.ExportResult{ZipPath: "/nonexistent.zip", NarrowedTo: &both}
	refusal, err = check(ginOf(w.ctxOf(true, nil, narrow, narrowC)), row(&gordon), two)
	require.NoError(t, err)
	assert.NotEmpty(t, refusal, "a caller holding only library.view was served an export narrowed to library.view and users.manage")
	refusal, err = check(ginOf(w.ctxOf(true, nil, wide, wideC)), row(&gordon), two)
	require.NoError(t, err)
	assert.NotEmpty(t, refusal, "a caller holding only users.manage was served an export narrowed to library.view and users.manage")
	full, fullC := adoptPrincipal(coreauth.MethodSession, gordonU,
		"test:"+rbac.Permission(auth.PermLibraryView), "test:"+rbac.Permission(auth.PermUsersManage))
	refusal, err = check(ginOf(w.ctxOf(true, nil, full, fullC)), row(&gordon), two)
	require.NoError(t, err)
	assert.Empty(t, refusal, "a caller holding both permissions was refused")

	none := res
	none.NarrowedTo = nil
	refusal, err = check(ginOf(w.ctxOf(true, nil, wide, wideC)), row(&gordon), none)
	require.NoError(t, err)
	assert.NotEmpty(t, refusal, "an export with no recorded scope was served")

	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
	refusal, err = check(ginOf(w.ctxOf(true, nil, off, offC)), row(&gordon), res)
	require.NoError(t, err)
	assert.Empty(t, refusal, "core's auth-off principal was refused (HEAD allows every download with auth off)")
}

// TestCountBypass_SessionStillStandsAsksCore: the stream recheck under core
// identity is core's Refresh; with no principal it does not stand, whatever
// books' (empty) user count says, where HEAD's no-accounts allowance would.
func TestCountBypass_SessionStillStandsAsksCore(t *testing.T) {
	w := newSiteWorld(t, false, true)
	require.True(t, w.head.sessionStillStands()(ginOf(context.Background())), "precondition: HEAD stands on an empty store")
	assert.False(t, w.core.sessionStillStands()(ginOf(w.ctxOf(true, nil, nil, nil))), "a stream with no principal stood under core identity")
}

// TestSeedRolesAndTokens_WritesNoRetiredRowUnderCoreIdentity: under core
// identity the server's start-up writers leave books' user, role and API-key
// rows alone: the role and system-user seeds, the never-expiring key stamp and
// the startup read-only key are core's after M3 and M6, and on a migrated store
// each write would land in a retired claim and be refused on every start.
// HEAD's server over the same rows runs every one of them, which proves the
// fixture gives each writer something to do.
func TestSeedRolesAndTokens_WritesNoRetiredRowUnderCoreIdentity(t *testing.T) {
	type rows struct {
		users, roles, keys int
		stamped            bool
	}
	run := func(t *testing.T, underIdentity bool) rows {
		t.Helper()
		config.ResetStateDirForTest()
		t.Cleanup(config.ResetStateDirForTest)
		t.Setenv(config.SecureStateDirEnv, t.TempDir())
		store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/seed-writers.pebble")
		require.NoError(t, err)
		t.Cleanup(func() { store.Close() })
		// A role of its own that holds users.manage, so HEAD's read-only key
		// writer finds an admin whether or not the role seed ran.
		warden, err := store.CreateRole(&database.Role{Name: "warden", Permissions: []string{string(auth.PermUsersManage)}})
		require.NoError(t, err)
		gordon, err := store.CreateUser("gordon", "gordon@example.test", "bcrypt", "x", []string{warden.ID}, "active")
		require.NoError(t, err)
		key, err := store.CreateAPIKey(&database.APIKey{UserID: gordon.ID, Name: "bat-signal", TokenHash: "invented-hash-0001",
			Scopes: []string{string(auth.PermLibraryView)}, Status: "active", CreatedAt: time.Now().UTC()})
		require.NoError(t, err)
		require.Nil(t, key.ExpiresAt, "precondition: a key with no expiry for the stamp to find")

		var opts []ServerOption
		if underIdentity {
			opts = append(opts, WithIdentity(adoptIdentity(t, []string{string(auth.PermLibraryView)}, nil, nil,
				&database.User{ID: gordon.ID, Username: "gordon", Status: "active"})))
		}
		s := newTestServerWith(t, store, opts...)
		s.life.startConfig.writeStartupReadOnlyKey = true
		s.seedRolesAndTokens()

		var out rows
		out.users, err = store.CountUsers()
		require.NoError(t, err)
		roles, err := store.ListRoles()
		require.NoError(t, err)
		out.roles = len(roles)
		keys, err := store.ListAllAPIKeys()
		require.NoError(t, err)
		out.keys = len(keys)
		for _, k := range keys {
			if k.ID == key.ID {
				out.stamped = k.ExpiresAt != nil
			}
		}
		return out
	}

	head := run(t, false)
	require.Equal(t, 2, head.users, "precondition: HEAD seeds its system user beside gordon")
	require.Greater(t, head.roles, 1, "precondition: HEAD seeds its roles beside the fixture's")
	require.Equal(t, 2, head.keys, "precondition: HEAD mints the startup read-only key")
	require.True(t, head.stamped, "precondition: HEAD stamps the key with no expiry")

	core := run(t, true)
	assert.Equal(t, 1, core.users, "a start-up writer added a books user row under core identity")
	assert.Equal(t, 1, core.roles, "a start-up writer added a books role row under core identity")
	assert.Equal(t, 1, core.keys, "a start-up writer added a books API key under core identity")
	assert.False(t, core.stamped, "a start-up writer rewrote a books API key under core identity")
}

// TestCountBypass_RequireAuthRefusesAnAnonymousCaller: the server's auth
// middleware under core identity is the bridge's Authenticate, which refuses a
// request core did not authenticate; HEAD's RequireAuth on the same empty
// store lets it through (its first-run bypass). The route-level 401 on a
// booted server does not show this on its own, because every protected route
// also has a permission gate that refuses an anonymous caller.
func TestCountBypass_RequireAuthRefusesAnAnonymousCaller(t *testing.T) {
	w := newSiteWorld(t, false, true)
	serve := func(s *Server, bound bool) (aborted bool, status int) {
		c := ginOf(w.ctxOf(bound, nil, nil, nil))
		s.requireAuth()(c)
		return c.IsAborted(), c.Writer.Status()
	}
	aborted, _ := serve(w.head, false)
	require.False(t, aborted, "precondition: HEAD's RequireAuth lets an anonymous caller through on an empty store")
	aborted, status := serve(w.core, true)
	assert.True(t, aborted, "an anonymous caller passed the auth middleware under core identity")
	assert.Equal(t, http.StatusUnauthorized, status)
}
