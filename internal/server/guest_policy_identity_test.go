// file: internal/server/guest_policy_identity_test.go
// version: 0.1.0
// guid: 2a6f9d18-7c43-4e05-b8a1-d3f5c70e9b64
// last-edited: 2026-11-22

package server

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/stretchr/testify/require"
)

// guestUsersView is core's identity view as the guest-policy hook reads it: the
// accounts it holds, or an error from the read.
type guestUsersView struct {
	coreauth.Identities
	mu    *sync.Mutex
	users *[]*coreauth.User
	err   error
}

func (v guestUsersView) Users(_ context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(yield func(*coreauth.User, error) bool) {
		if v.err != nil {
			yield(nil, v.err)
			return
		}
		if v.users == nil {
			return
		}
		v.mu.Lock()
		all := append([]*coreauth.User(nil), *v.users...)
		v.mu.Unlock()
		for _, u := range all {
			if f.Status != "" && u.Status != f.Status {
				continue
			}
			if !yield(u, nil) {
				return
			}
		}
	}
}

type guestHook struct {
	srv   *Server
	store *database.PebbleStore
	cf    *fakeGuestPolicy
	users *[]*coreauth.User
	mu    *sync.Mutex
}

func (h guestHook) setUsers(us ...*coreauth.User) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.users = us
}

func newGuestHook(t *testing.T, cf *fakeGuestPolicy, readErr error) guestHook {
	t.Helper()
	saved := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
	config.Mutate(func(c *config.Config) { c.OwnerEmail = "alfred.pennyworth@example.test"; c.OAuthAllowedEmails = "" })
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/guest-hook.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	var mu sync.Mutex
	users := []*coreauth.User{}
	id := adoptIdentity(t, nil, nil, nil)
	id.Users = guestUsersView{mu: &mu, users: &users, err: readErr}
	srv := newTestServerWith(t, store, WithIdentity(id), WithAccessAllowList(func() (string, error) { return "", nil }))
	srv.guestPolicyOverride = cf
	return guestHook{srv: srv, store: store, cf: cf, users: &users, mu: &mu}
}

func guestUser(id, email string, verified bool, st coreauth.UserStatus) *coreauth.User {
	return &coreauth.User{ID: coreauth.UserID(id), Email: email, EmailVerified: verified, Status: st}
}

func (h guestHook) outcome(t *testing.T, inviteID string) *database.GuestPolicyOutcome {
	t.Helper()
	o, err := h.store.GetGuestPolicyOutcome(inviteID)
	require.NoError(t, err)
	return o
}

func (h guestHook) waitOutcome(t *testing.T, inviteID, status string) *database.GuestPolicyOutcome {
	t.Helper()
	var o *database.GuestPolicyOutcome
	require.Eventually(t, func() bool {
		o = h.outcome(t, inviteID)
		return o != nil && o.Status == status
	}, 5*time.Second, 10*time.Millisecond, "outcome of %s never became %s (is %+v)", inviteID, status, o)
	return o
}

func (h guestHook) waitRemoved(t *testing.T, email string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, removed := h.cf.snapshot()
		return len(removed) > 0 && removed[len(removed)-1] == email
	}, 5*time.Second, 10*time.Millisecond, "%s was never removed from the guest policy", email)
}

func (h guestHook) noCloudflareCall(t *testing.T) {
	t.Helper()
	// Anything started in the background has had its chance by the time the
	// server's tracked goroutines are idle.
	h.srv.bgWG.Wait()
	added, removed := h.cf.snapshot()
	require.Empty(t, added)
	require.Empty(t, removed)
}

const (
	gSelina = "selina.kyle@example.test"
	gUserID = "u-selina"
)

func accepted(email string, mailed, adminCreator bool) coreauth.InviteAccepted {
	return coreauth.InviteAccepted{InviteID: "inv-1", UserID: gUserID, CreatedBy: "u-bruce", Email: email,
		BindEmailOnAccept: mailed, CreatorHeldAdmin: adminCreator}
}

func (h guestHook) accept(t *testing.T, e coreauth.InviteAccepted) {
	t.Helper()
	h.srv.GuestPolicyLifecycle().InviteAccepted(t.Context(), e)
}

func TestGuestLifecycle_EveryEventHasACallback(t *testing.T) {
	h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
	l := h.srv.GuestPolicyLifecycle()
	v := reflect.ValueOf(l)
	for i := 0; i < v.NumField(); i++ {
		require.Falsef(t, v.Field(i).IsNil(), "Lifecycle.%s is nil: books ignores that account event", v.Type().Field(i).Name)
	}
}

// An emailed accept by an admin adds the address and records CFAdded; HEAD's two
// skip reasons leave Cloudflare alone and say why.
func TestGuestLifecycle_AcceptGatesAreHEADs(t *testing.T) {
	t.Run("granted", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		h.setUsers(guestUser(gUserID, gSelina, true, coreauth.StatusActive))
		h.accept(t, accepted(gSelina, true, true))
		h.waitOutcome(t, "inv-1", database.InviteGuestPolicyAdded)
		added, _ := h.cf.snapshot()
		require.Equal(t, []string{gSelina}, added)
		g, err := h.store.GuestPolicyGrants().GetInviteEmailGrant(gSelina)
		require.NoError(t, err)
		require.NotNil(t, g)
		require.True(t, g.CFAdded)
		require.Equal(t, gUserID, g.UserID)
	})
	t.Run("not emailed", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		h.accept(t, accepted(gSelina, false, true))
		o := h.outcome(t, "inv-1")
		require.NotNil(t, o)
		require.Equal(t, database.InviteGuestPolicySkipped, o.Status)
		require.Contains(t, o.Detail, "not emailed")
		h.noCloudflareCall(t)
	})
	t.Run("creator not admin", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		h.accept(t, accepted(gSelina, true, false))
		o := h.outcome(t, "inv-1")
		require.NotNil(t, o)
		require.Equal(t, database.InviteGuestPolicySkipped, o.Status)
		require.Contains(t, o.Detail, "without the admin role")
		h.noCloudflareCall(t)
	})
	t.Run("no address", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		h.accept(t, accepted("", true, true))
		require.Nil(t, h.outcome(t, "inv-1"))
		h.noCloudflareCall(t)
	})
	t.Run("already present is never removed later", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.AlreadyPresent}, nil)
		h.setUsers(guestUser(gUserID, gSelina, true, coreauth.StatusActive))
		h.accept(t, accepted(gSelina, true, true))
		h.waitOutcome(t, "inv-1", database.InviteGuestPolicyAlreadyPresent)
		h.srv.GuestPolicyLifecycle().StatusChanged(t.Context(), coreauth.StatusChanged{UserID: gUserID, Email: gSelina, From: coreauth.StatusActive, To: coreauth.StatusDisabled})
		h.srv.bgWG.Wait()
		_, removed := h.cf.snapshot()
		require.Empty(t, removed)
	})
	t.Run("cloudflare failure is recorded and fails nothing", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{err: errors.New("cloudflare 500")}, nil)
		h.setUsers(guestUser(gUserID, gSelina, true, coreauth.StatusActive))
		h.accept(t, accepted(gSelina, true, true))
		o := h.waitOutcome(t, "inv-1", database.InviteGuestPolicyFailed)
		require.Contains(t, o.Detail, "cloudflare 500")
	})
	t.Run("sync not configured", func(t *testing.T) {
		h := newGuestHook(t, nil, nil)
		h.srv.guestPolicyOverride = nil
		h.accept(t, accepted(gSelina, true, true))
		o := h.outcome(t, "inv-1")
		require.NotNil(t, o)
		require.Equal(t, database.InviteGuestPolicyNotConfigured, o.Status)
	})
}

// A disable or an e-mail change that landed between core's commit and the
// callback's own write finds no row to revoke, so the callback reads the account
// back: one that no longer stands is not added.
func TestGuestLifecycle_AcceptOfAnAccountThatNoLongerStandsAddsNothing(t *testing.T) {
	for name, users := range map[string][]*coreauth.User{
		"disabled meanwhile": {guestUser(gUserID, gSelina, true, coreauth.StatusDisabled)},
		"deleted meanwhile":  {},
		"address changed":    {guestUser(gUserID, "oswald.cobblepot@example.test", true, coreauth.StatusActive)},
	} {
		t.Run(name, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
			h.setUsers(users...)
			h.accept(t, accepted(gSelina, true, true))
			o := h.outcome(t, "inv-1")
			require.NotNil(t, o)
			require.Equal(t, database.InviteGuestPolicyRevoked, o.Status)
			h.noCloudflareCall(t)
			g, err := h.store.GuestPolicyGrants().GetInviteEmailGrant(gSelina)
			require.NoError(t, err)
			require.Nil(t, g, "the bookkeeping row of a revoked access was left behind")
		})
	}
	t.Run("account unreadable", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, errors.New("scan failed"))
		h.accept(t, accepted(gSelina, true, true))
		o := h.outcome(t, "inv-1")
		require.NotNil(t, o)
		require.Equal(t, database.InviteGuestPolicyFailed, o.Status)
		h.noCloudflareCall(t)
	})
}

// seedAdded is a guest the sync added to Cloudflare, in the bookkeeping.
func (h guestHook) seedAdded(t *testing.T, email string) {
	t.Helper()
	require.NoError(t, h.store.PutGuestPolicyGrant(database.GuestPolicyGrant{Email: email, UserID: gUserID, InviteID: "inv-1", GrantedAt: time.Now()}))
	ok, err := h.store.GuestPolicyGrants().SetInviteEmailGrantCFAdded(email, gUserID)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestGuestLifecycle_EventsThatEndAccessRemoveTheEntry(t *testing.T) {
	ev := map[string]func(l coreauth.Lifecycle, ctx context.Context){
		"disabled": func(l coreauth.Lifecycle, ctx context.Context) {
			l.StatusChanged(ctx, coreauth.StatusChanged{UserID: gUserID, Email: gSelina, From: coreauth.StatusActive, To: coreauth.StatusDisabled})
		},
		"email changed": func(l coreauth.Lifecycle, ctx context.Context) {
			l.EmailChanged(ctx, coreauth.EmailChanged{UserID: gUserID, From: gSelina, To: "oswald.cobblepot@example.test"})
		},
		"deleted": func(l coreauth.Lifecycle, ctx context.Context) {
			l.UserDeleted(ctx, coreauth.UserDeleted{UserID: gUserID, Email: gSelina})
		},
		"merged away": func(l coreauth.Lifecycle, ctx context.Context) {
			l.UserMergedAway(ctx, coreauth.UserMergedAway{UserID: gUserID, Email: gSelina, MergedInto: "u-other"})
		},
	}
	for name, fire := range ev {
		t.Run(name, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
			h.seedAdded(t, gSelina)
			fire(h.srv.GuestPolicyLifecycle(), t.Context())
			h.waitRemoved(t, gSelina)
			g, err := h.store.GuestPolicyGrants().GetInviteEmailGrant(gSelina)
			require.NoError(t, err)
			require.Nil(t, g)
			// A second delivery (the HEAD handler twin, a re-fired event) finds nothing.
			fire(h.srv.GuestPolicyLifecycle(), t.Context())
			h.srv.bgWG.Wait()
			_, removed := h.cf.snapshot()
			require.Len(t, removed, 1)
		})
	}
	t.Run("re-enable does nothing", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		h.seedAdded(t, gSelina)
		h.srv.GuestPolicyLifecycle().StatusChanged(t.Context(), coreauth.StatusChanged{UserID: gUserID, Email: gSelina, From: coreauth.StatusDisabled, To: coreauth.StatusActive})
		h.noCloudflareCall(t)
	})
	t.Run("a grant the sync did not add is dropped, never removed from Cloudflare", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		require.NoError(t, h.store.PutGuestPolicyGrant(database.GuestPolicyGrant{Email: gSelina, UserID: gUserID, GrantedAt: time.Now()}))
		h.srv.GuestPolicyLifecycle().UserDeleted(t.Context(), coreauth.UserDeleted{UserID: gUserID, Email: gSelina})
		h.noCloudflareCall(t)
	})
	t.Run("a cloudflare failure on remove fails nothing", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{removeErr: errors.New("cloudflare 500")}, nil)
		h.seedAdded(t, gSelina)
		h.srv.GuestPolicyLifecycle().UserDeleted(t.Context(), coreauth.UserDeleted{UserID: gUserID, Email: gSelina})
		h.waitRemoved(t, gSelina)
	})
}

// The keep rules are HEAD's: owner, Access allow-list, and another ACTIVE holder
// whose address is VERIFIED. A holder who only typed the address in does not keep it.
func TestGuestLifecycle_KeepRules(t *testing.T) {
	cases := []struct {
		name     string
		email    string
		others   []*coreauth.User
		readErr  error
		wantKeep bool
	}{
		{"no other holder", gSelina, nil, nil, false},
		{"owner", "alfred.pennyworth@example.test", nil, nil, true},
		{"another active verified holder", gSelina, []*coreauth.User{guestUser("u-other", "SELINA.kyle@example.test", true, coreauth.StatusActive)}, nil, true},
		{"another holder only typed it in", gSelina, []*coreauth.User{guestUser("u-other", gSelina, false, coreauth.StatusActive)}, nil, false},
		{"another verified holder is disabled", gSelina, []*coreauth.User{guestUser("u-other", gSelina, true, coreauth.StatusDisabled)}, nil, false},
		{"a verified holder of another address", gSelina, []*coreauth.User{guestUser("u-other", "oswald.cobblepot@example.test", true, coreauth.StatusActive)}, nil, false},
		{"the read fails", gSelina, nil, errors.New("scan failed"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, tc.readErr)
			h.setUsers(tc.others...)
			h.seedAdded(t, tc.email)
			h.srv.GuestPolicyLifecycle().UserDeleted(t.Context(), coreauth.UserDeleted{UserID: gUserID, Email: tc.email})
			h.srv.bgWG.Wait()
			if tc.wantKeep {
				h.noCloudflareCall(t)
				return
			}
			h.waitRemoved(t, tc.email)
		})
	}
	t.Run("on the Access allow-list", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		h.srv.accessAllowList = func() (string, error) { return "batgirl@example.test, " + gSelina, nil }
		h.seedAdded(t, gSelina)
		h.srv.GuestPolicyLifecycle().UserDeleted(t.Context(), coreauth.UserDeleted{UserID: gUserID, Email: gSelina})
		h.noCloudflareCall(t)
	})
}
