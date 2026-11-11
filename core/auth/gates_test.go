// file: core/auth/gates_test.go
// version: 1.0.0
// guid: ee01bbff-d4fe-4a64-9053-998f722fa081
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The five early-fix gate tests (05-authn §10, §11.1; 05-rbac §12), each a
// property over seeded random inputs. A failure logs its seed; rerun it with
// AUTH_PROP_SEED=<seed>.

func propRand(t *testing.T) *rand.Rand {
	t.Helper()
	seed := uint64(time.Now().UnixNano())
	if s := os.Getenv("AUTH_PROP_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("AUTH_PROP_SEED: %v", err)
		}
		seed = v
	}
	t.Logf("property seed %d (rerun with AUTH_PROP_SEED=%d)", seed, seed)
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

// pick returns 1..most distinct members of pool, sorted.
func pick(r *rand.Rand, pool []rbac.Permission, most int) []rbac.Permission {
	n := 1 + r.IntN(min(most, len(pool)))
	out := make([]rbac.Permission, 0, n)
	for _, i := range r.Perm(len(pool))[:n] {
		out = append(out, pool[i])
	}
	slices.Sort(out)
	return out
}

// Early fix 1: whatever sessions exist and whichever are revoked, a listing
// carries only public IDs: never a token, never a token's hash, never an
// mcs_ value, in its JSON or its Go rendering. It lists exactly the open
// sessions and marks the presenting one Current.
func TestSessions_ListNeverReturnsToken(t *testing.T) {
	r := propRand(t)
	f := newFx(t)
	ctx := context.Background()
	users := []*auth.User{f.user("bruce", rbac.RoleAdmin), f.user("dick", "gotham.detective"), f.user("barbara", rbac.RoleViewer)}
	origins := []auth.SessionOrigin{auth.OriginPassword, auth.OriginTempLogin, auth.OriginInvite, auth.OriginOIDC("wayne-sso")}
	var secrets []string
	open := map[auth.UserID]map[string]bool{}
	revoked := 0
	for range 60 {
		u := users[r.IntN(len(users))]
		is, err := f.svc.IssueSession(ctx, u, origins[r.IntN(len(origins))], r.IntN(2) == 0, auth.ClientInfo{
			Addr:      netip.AddrFrom4([4]byte{192, 0, 2, byte(1 + r.IntN(250))}),
			UserAgent: fmt.Sprintf("Batcomputer/%d", r.IntN(9)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(is.Token, "mcs_") || is.PublicID == "" || strings.Contains(is.Token, is.PublicID) {
			t.Fatalf("issued session %q / %q", is.Token, is.PublicID)
		}
		secrets = append(secrets, is.Token, auth.HashHex(auth.Token(is.Token)))
		if open[u.ID] == nil {
			open[u.ID] = map[string]bool{}
		}
		open[u.ID][is.PublicID] = true
		if r.IntN(5) == 0 {
			for pid := range open[u.ID] {
				if err := f.svc.RevokeSession(ctx, f.session(u), pid); err != nil {
					t.Fatal(err)
				}
				delete(open[u.ID], pid)
				revoked++
				break
			}
		}
	}
	for _, u := range users {
		current := ""
		for pid := range open[u.ID] {
			current = pid
			break
		}
		p := f.session(u)
		p.CredentialID = current
		var views []auth.SessionView
		for v, err := range f.svc.Sessions(auth.WithPrincipal(ctx, p), u.ID) {
			if err != nil {
				t.Fatal(err)
			}
			views = append(views, v)
		}
		js, err := json.Marshal(views)
		if err != nil {
			t.Fatal(err)
		}
		dump := string(js) + fmt.Sprintf("%+v %#v", views, views)
		if strings.Contains(dump, "mcs_") {
			t.Fatalf("%s's session listing carries an mcs_ value", u.Username)
		}
		for _, sec := range secrets {
			if strings.Contains(dump, sec) {
				t.Fatalf("%s's session listing carries a token or a token's hash", u.Username)
			}
		}
		listed, currents := map[string]bool{}, 0
		for _, v := range views {
			listed[v.PublicID] = true
			if v.Current {
				currents++
				if v.PublicID != current {
					t.Errorf("%s: session %s marked Current, the request presented %s", u.Username, v.PublicID, current)
				}
			}
		}
		if want := open[u.ID]; !maps.Equal(listed, want) && !(len(listed) == 0 && len(want) == 0) {
			t.Errorf("%s: listed %v, open %v", u.Username, slices.Sorted(maps.Keys(listed)), slices.Sorted(maps.Keys(want)))
		}
		if current != "" && currents != 1 {
			t.Errorf("%s: %d sessions marked Current, want 1", u.Username, currents)
		}
	}
	if got := f.jrnl.count(journal.SessionRevoked); got != revoked {
		t.Errorf("journal holds %d session.revoked lines, %d sessions were revoked", got, revoked)
	}
}

// Early fix 2 (SEC9-5): a key mints only scopes its effective set holds at
// that moment: its own scopes intersected with its owner's roles as stored
// now. When the owner's roles change behind the key so that set is empty,
// the key mints nothing, whatever it asks for.
func TestAPIKey_EmptyEffectiveKeyCannotMint(t *testing.T) {
	r := propRand(t)
	f := newFx(t)
	ctx := context.Background()
	pool := []rbac.Permission{"gotham:cases.read", "gotham:patrol", "gotham:signal"}
	// What each role grants of the pool: the seeds and DefaultGrants of the
	// fixture, written out so the oracle does not ask rbac.
	grants := map[rbac.RoleName][]rbac.Permission{
		"gotham.detective": {"gotham:patrol", "gotham:cases.read"},
		rbac.RoleViewer:    {"gotham:patrol"},
		rbac.RoleEditor:    {"gotham:patrol", "gotham:signal"},
		roleCommissioner:   {"gotham:patrol", "gotham:signal", "gotham:cases.read"},
	}
	roleNames := slices.Sorted(maps.Keys(grants))
	granted := func(roles []rbac.RoleName, p rbac.Permission) bool {
		for _, rn := range roles {
			if slices.Contains(grants[rn], p) {
				return true
			}
		}
		return false
	}
	empty := 0
	for trial := range 80 {
		u := f.user(fmt.Sprintf("robin%d", trial), rbac.RoleEditor, "gotham.detective") // holds the whole pool
		keyScopes := pick(r, pool, len(pool))
		_, k := f.mint(f.session(u), u.ID, keyScopes...)
		var after []rbac.RoleName
		for _, rn := range roleNames {
			if r.IntN(3) == 0 {
				after = append(after, rn)
			}
		}
		u = f.setRoles(u.ID, after...)
		kp := f.keyPrincipal(u, k)
		var effective []rbac.Permission
		for _, p := range keyScopes {
			if granted(after, p) {
				effective = append(effective, p)
			}
		}
		want := pick(r, pool, len(pool))
		allowed := len(effective) > 0
		for _, p := range want {
			allowed = allowed && slices.Contains(effective, p)
		}
		_, _, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Name: "grapnel", Scopes: want})
		switch {
		case allowed && err != nil:
			t.Fatalf("trial %d: key %v, roles %v, effective %v: minting %v failed: %v", trial, keyScopes, after, effective, want, err)
		case !allowed && !errors.Is(err, auth.ErrScopeEscalation):
			t.Fatalf("trial %d: key %v, roles %v, effective %v: minting %v = %v, want ErrScopeEscalation", trial, keyScopes, after, effective, want, err)
		}
		if len(effective) == 0 {
			empty++
			for _, p := range pool {
				if _, _, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{p}}); !errors.Is(err, auth.ErrScopeEscalation) {
					t.Fatalf("trial %d: a key with an empty effective set minted %s: %v", trial, p, err)
				}
			}
		}
	}
	if empty == 0 {
		t.Fatal("no trial left a key with an empty effective set; the property was not exercised")
	}
}

// Early fix 3: over random walks of status changes by the owner, an admin and
// a key, revoked is terminal (every other target is ErrKeyRevoked and the
// stored status and revocation time never move), needs_confirmation goes
// only to active (interactive) or revoked, and needs_confirmation is never a
// target.
func TestAPIKey_RevokedIsTerminal(t *testing.T) {
	r := propRand(t)
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick", "gotham.detective")
	_, actorKey := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	actors := []*auth.Principal{f.session(dick), f.session(bruce), f.keyPrincipal(dick, actorKey)}
	targets := []auth.KeyStatus{auth.KeyActive, auth.KeyDisabled, auth.KeyRevoked, auth.KeyNeedsConfirmation}
	for trial := range 40 {
		_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
		cur := auth.KeyActive
		if r.IntN(3) == 0 { // the post-restore hook left it unconfirmed
			k = f.key(k.ID)
			k.Status, k.Version = auth.KeyNeedsConfirmation, k.Version+1
			f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, k) })
			cur = auth.KeyNeedsConfirmation
		}
		var revokedAt time.Time
		for step := range 12 {
			to := targets[r.IntN(len(targets))]
			actor := actors[r.IntN(len(actors))]
			var want error
			next := cur
			switch {
			case to == auth.KeyNeedsConfirmation:
				want = auth.ErrBadInput
			case cur == auth.KeyRevoked && to != auth.KeyRevoked:
				want = auth.ErrKeyRevoked
			case cur == auth.KeyNeedsConfirmation && to == auth.KeyDisabled:
				want = auth.ErrKeyRevoked
			case to == auth.KeyActive && cur != auth.KeyActive && !actor.Method.Interactive():
				want = auth.ErrDenied // any change into active is an interactive write
			default:
				next = to
			}
			err := f.svc.SetAPIKeyStatus(ctx, actor, k.ID, to)
			if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("trial %d step %d: %s -> %s by %s = %v, want %v", trial, step, cur, to, actor.Method, err, want)
			}
			got := f.key(k.ID)
			if cur == auth.KeyRevoked && auth.MoreRestrictive(got.Status, cur) != got.Status {
				t.Fatalf("trial %d step %d: a revoked key became %s", trial, step, got.Status)
			}
			cur = next
			if got.Status != cur {
				t.Fatalf("trial %d step %d: stored %s, want %s", trial, step, got.Status, cur)
			}
			if cur == auth.KeyRevoked {
				if revokedAt.IsZero() {
					revokedAt = got.RevokedAt
				}
				if revokedAt.IsZero() || !got.RevokedAt.Equal(revokedAt) {
					t.Fatalf("trial %d step %d: revoked_at %v, first %v", trial, step, got.RevokedAt, revokedAt)
				}
			}
		}
	}
	if f.jrnl.count(journal.APIKeyRevoked) == 0 {
		t.Fatal("no walk revoked a key; the property was not exercised")
	}
}

// 05-rbac §12: an admin's key holds only its scopes. For every permission the
// catalog declares, the key's checker holds it only if a scope of the key
// names or implies it (and holds every non-Sensitive one it names), never
// core:admin, and every action
// on another user refuses the key, whatever scopes it carries, because it is
// not an interactive sign-in.
func TestAdminChecks_HonourKeyScopes(t *testing.T) {
	r := propRand(t)
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce", rbac.RoleAdmin)
	dick := f.user("dick", "gotham.detective")
	cat := f.rb.Catalog()
	var pool []rbac.Permission
	for spec := range cat.All() {
		if spec.Name != rbac.CoreAdmin {
			pool = append(pool, spec.Name)
		}
	}
	_, dickKey := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	dickSession, err := f.svc.IssueSession(ctx, dick, auth.OriginPassword, false, auth.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	for trial := range 25 {
		scopes := pick(r, pool, 3)
		if trial == 0 {
			scopes = []rbac.Permission{"core:users.manage"} // the scope the actions below check
		}
		_, k := f.mint(f.session(bruce), bruce.ID, scopes...)
		kp := f.keyPrincipal(bruce, k)
		reach := implied(cat, scopes)
		for spec := range cat.All() {
			has, named := kp.Perms.Has(spec.Name), slices.Contains(scopes, spec.Name)
			if has && !reach[spec.Name] {
				t.Fatalf("an admin key scoped %v holds %s, which no scope of it names or implies", scopes, spec.Name)
			}
			if named && !spec.Sensitive && !has {
				t.Fatalf("an admin key scoped %v lacks its own %s", scopes, spec.Name)
			}
		}
		if kp.Perms.Has(rbac.CoreAdmin) {
			t.Fatalf("an admin key scoped %v holds core:admin", scopes)
		}
		if _, _, err := f.svc.CreateAPIKey(ctx, kp, auth.KeySpec{Owner: dick.ID, Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}}); !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("key %v minted for another user: %v", scopes, err)
		}
		if err := f.svc.SetAPIKeyStatus(ctx, kp, dickKey.ID, auth.KeyDisabled); !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("key %v disabled another user's key: %v", scopes, err)
		}
		if err := f.svc.RevokeSession(ctx, kp, dickSession.PublicID); !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("key %v revoked another user's session: %v", scopes, err)
		}
	}
	// The same admin through an interactive sign-in may.
	if err := f.svc.SetAPIKeyStatus(ctx, f.session(bruce), dickKey.ID, auth.KeyDisabled); err != nil {
		t.Fatalf("the admin's session could not disable the key: %v", err)
	}
}

// implied is scopes closed over the catalog's Implies.
func implied(cat *rbac.Catalog, scopes []rbac.Permission) map[rbac.Permission]bool {
	out := map[rbac.Permission]bool{}
	todo := slices.Clone(scopes)
	for len(todo) > 0 {
		p := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if out[p] {
			continue
		}
		out[p] = true
		if spec, ok := cat.Lookup(p); ok {
			todo = append(todo, spec.Implies...)
		}
	}
	return out
}

// Early fix 6 (SEC6-3, R6-5): a last-use touch never undoes a revocation. The
// exact race first (a touch read the key while it was active, the revoke
// commits, the touch commits after), then a touch loop racing a revoke at a
// random tick. The store opens inside the bubble, as taskstest's do: the
// engine's goroutines must belong to it.
func TestRevokeRacesTouch_NeverUnrevokes(t *testing.T) {
	r := propRand(t)
	synctest.Test(t, func(t *testing.T) {
		f := newFx(t)
		ctx := context.Background()
		dick := f.user("dick", "gotham.detective")
		addr := netip.MustParseAddr("192.0.2.7")

		_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
		b := f.st.NewBatch()
		if err := f.st.TouchAPIKey(b, k.ID, f.clock(), addr); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.RevokeAPIKey(ctx, f.session(dick), k.ID); err != nil {
			t.Fatal(err)
		}
		err := b.Commit(ctx, storage.NoSync)
		_ = b.Close()
		if !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("a touch staged before the revoke committed after it: %v", err)
		}
		if got := f.key(k.ID); got.Status != auth.KeyRevoked || got.UseCount != 0 {
			t.Fatalf("after the raced touch: %s, use count %d", got.Status, got.UseCount)
		}

		for trial := range 25 {
			_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					default:
					}
					b := f.st.NewBatch()
					if err := f.st.TouchAPIKey(b, k.ID, f.clock(), addr); err == nil {
						_ = b.Commit(ctx, storage.NoSync) // a conflicting touch is dropped: it is advisory
					}
					_ = b.Close()
					time.Sleep(time.Millisecond)
				}
			}()
			time.Sleep(time.Duration(r.IntN(20)) * time.Millisecond)
			if err := f.svc.RevokeAPIKey(ctx, f.session(dick), k.ID); err != nil {
				t.Fatalf("trial %d: revoke: %v", trial, err)
			}
			at := f.key(k.ID)
			time.Sleep(5 * time.Millisecond) // touches keep arriving after the revoke
			close(stop)
			<-done
			got := f.key(k.ID)
			if got.Status != auth.KeyRevoked || !got.RevokedAt.Equal(at.RevokedAt) ||
				got.UseCount != at.UseCount || !got.LastUsedAt.Equal(at.LastUsedAt) {
				t.Fatalf("trial %d: after the revoke the key moved: %+v -> %+v", trial, at, got)
			}
		}
	})
}
