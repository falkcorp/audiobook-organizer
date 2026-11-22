// file: internal/books/identity_access_sameemail_boot_test.go
// version: 0.1.0
// guid: 8d3f5a17-2c64-4b90-a1e8-6f07b94c3d52
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/storage"
)

// The same-address shape of the production install (F4-08 B10e1c): two HEAD
// administrators hold one address, both proven, and only one of them is linked
// to Cloudflare Access, by the opaque `sub` the Access JWT carries (it holds no
// '@' and is not the address). The store is a HEAD store on disk that the flipped
// boot's M3 imports, so the pins run against the accounts core really holds.
//
// Every address is on example.test, every person a Batman character. Each case
// of the unlinked-address rules has its own address in the one store, so a
// single boot answers all of them, and a positive control (one address that
// does link) in the same boot proves the refusals are the rule's and not a
// fixture that cannot link at all.

const (
	seBruce   = "bruce.wayne@example.test"       // the shared address
	seSelina  = "selina.kyle@example.test"       // two proven holders, neither admin nor password
	seHarvey  = "harvey.dent@example.test"       // one proven holder, an admin without a password
	seOswald  = "oswald.cobblepot@example.test"  // one proven holder, no admin role, a password
	seEdward  = "edward.nygma@example.test"      // two unproven holders: an admin and a plain account
	seAlfred  = "alfred.pennyworth@example.test" // one proven holder, a plain account: the control
	seLucius  = "lucius.fox@example.test"        // the observer administrator
	seLuciusP = "batmobile-test-password-0123"

	// seBruceSub is the JWT sub of the linked administrator: opaque, no '@'.
	seBruceSub = "d3a91b6c-7f20-4e58-b1c4-0a52e8f61937"
)

// seAllowList admits every address of the fixture except the observer's.
const seAllowList = seBruce + "," + seSelina + "," + seHarvey + "," + seOswald + "," + seEdward + "," + seAlfred

// seIDs are the HEAD (and so core) user IDs of the fixture, by role in it.
type seIDs struct {
	bruceLinked, bruceTwin   string // the two administrators of the shared address
	selinaA, selinaB         string
	harvey, oswald           string
	edwardAdmin, edwardPlain string
	alfred, lucius           string
}

// seedRow rewrites the HEAD user row u:<id> through mutate (a raw edit: HEAD's
// own CreateUser refuses a second proven holder of an address, which is exactly
// the state this fixture needs).
func seedRowEdit(t *testing.T, s *database.PebbleStore, id string, mutate func(map[string]any)) {
	t.Helper()
	key := []byte("u:" + id)
	cur, closer, err := s.DB().Get(key)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(cur, &fields))
	require.NoError(t, closer.Close())
	mutate(fields)
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, s.DB().Set(key, raw, pebble.Sync))
}

// seUser creates a HEAD account holding email with the given proof ("" is
// unproven), roles and password state, and returns its ID.
func seUser(t *testing.T, s *database.PebbleStore, username, email, source string, roles []string, algo, hash string) string {
	t.Helper()
	u, err := s.CreateUser(username, "", algo, hash, roles, "active")
	require.NoError(t, err)
	seedRowEdit(t, s, u.ID, func(f map[string]any) {
		f["email"] = email
		if source == "" {
			delete(f, "email_source")
		} else {
			f["email_source"] = source
		}
	})
	return u.ID
}

func seedSameEmail(t *testing.T, s *database.PebbleStore, ids *seIDs) {
	t.Helper()
	pw, err := bcrypt.GenerateFromPassword([]byte(seLuciusP), bcrypt.DefaultCost)
	require.NoError(t, err)
	admin, viewer := []string{"admin"}, []string{"viewer"}
	const noPass, noHash = "oauth", ""

	ids.bruceLinked = seUser(t, s, "bruce.wayne", seBruce, "setup", admin, "bcrypt", string(pw))
	ids.bruceTwin = seUser(t, s, "batman", seBruce, "admin", admin, noPass, noHash)
	// HEAD's identity link: provider cfaccess, subject the opaque JWT sub, on the
	// first administrator only.
	_, err = s.CreateOAuthIdentity(&database.OAuthIdentity{Provider: cfaccessProvider, Subject: seBruceSub, Email: seBruce, UserID: ids.bruceLinked})
	require.NoError(t, err)

	ids.selinaA = seUser(t, s, "selina.kyle", seSelina, "setup", viewer, noPass, noHash)
	ids.selinaB = seUser(t, s, "catwoman", seSelina, "invite", viewer, noPass, noHash)
	ids.harvey = seUser(t, s, "harvey.dent", seHarvey, "setup", admin, noPass, noHash)
	ids.oswald = seUser(t, s, "oswald.cobblepot", seOswald, "setup", viewer, "bcrypt", string(pw))
	ids.edwardAdmin = seUser(t, s, "edward.nygma", seEdward, "", admin, noPass, noHash)
	ids.edwardPlain = seUser(t, s, "riddler", seEdward, "", viewer, noPass, noHash)
	ids.alfred = seUser(t, s, "alfred.pennyworth", seAlfred, "setup", viewer, noPass, noHash)
	ids.lucius = seUser(t, s, "lucius.fox", seLucius, "setup", admin, "bcrypt", string(pw))

	// HEAD's allow-list is a stored setting; M2 carries it into
	// modules.books.cfaccess.allowed_emails, where it outranks the environment, so
	// the list is seeded where an installed library holds it.
	require.NoError(t, s.SetSetting("config_blob", `{"oauth_allowed_emails":`+strconv.Quote(seAllowList)+`}`, "json", false))
}

// seAssertions are the Access assertions of the cases, each a verified identity.
func seAssertions() accessAssertions {
	mint := func(sub, email string) *legacyoauth.IdentityClaims {
		return &legacyoauth.IdentityClaims{Provider: cfaccessProvider, Subject: sub, Email: email, EmailVerified: true}
	}
	return accessAssertions{
		// The linked administrator by the JWT sub: the production assertion.
		"bruce-sub": mint(seBruceSub, seBruce),
		// The same person under the e-mail as the subject: the form
		// verifier.go's empty-sub fallback would produce. It must not match the link.
		"bruce-email-subject": mint(seBruce, seBruce),
		// A subject core has never seen, for the same address.
		"bruce-fresh": mint("0f6e2d84-1b73-4c95-a8d0-93e17b5c4a26", seBruce),
		"selina":      mint("sub-selina", seSelina),
		"harvey":      mint("sub-harvey", seHarvey),
		"oswald":      mint("sub-oswald", seOswald),
		"edward":      mint("sub-edward", seEdward),
		"alfred":      mint("sub-alfred", seAlfred),
	}
}

// seBoot is the flipped boot over the seeded HEAD store: Access the way in, the
// allow-list every address of the fixture except the observer's (seeded where an
// installed library holds it, the stored OAUTH allow-list), the verifier the late
// handle bound to seAssertions. The boot waits for M3 and for ready.
type seBoot struct {
	*runningApp
	fx  *flipFixture
	ids *seIDs
}

func bootSameEmail(t *testing.T) *seBoot {
	t.Helper()
	ids := &seIDs{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { seedSameEmail(t, s, ids) }, "")
	t.Cleanup(SetIdentityFlipForTest(true))
	t.Setenv("ABO_BOOKS_IDENTITY_OWNER_EMAIL", seBruce)
	r := runComposedIn(t, fx.stateDir, func(cfg *config.Config) {
		cfg.DatabasePath = fx.dbPath
		cfg.RootDir = fx.dbDir
		cfg.EnableAuth = true
	}, nil, func(o *app.Options) {
		o.StorageFS = storage.FS{}
		o.StateDir = fx.stateDir
		o.Env = []string{storagePathEnv + "=" + fx.dbPath}
		// sandboxStateDirAt wrote a fixed key; the rows are sealed under this one.
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(fx.stateDir), fx.key, 0o600))
	}, nil)
	require.Eventually(t, func() bool {
		ready, err := shapedReady(r.composed)
		require.NoError(t, err)
		return ready
	}, 2*time.Minute, 50*time.Millisecond, "/health never reported ready")
	done, err := r.app.StorageAdmin().DataMigrationDone(context.Background(), cutover.IdentityCutoverID)
	require.NoError(t, err)
	require.True(t, done, "M3 did not run on the seeded store: the fixture proves nothing")
	require.NoError(t, r.mod.idw.verifier.Bind(seAssertions()))
	return &seBoot{runningApp: r, fx: fx, ids: ids}
}

// seCoreUser is core's account as the admin list shows it.
type seCoreUser struct {
	ID            string   `json:"id"`
	Username      string   `json:"username"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Roles         []string `json:"roles"`
	Status        string   `json:"status"`
	HasPassword   bool     `json:"has_password"`
	MergedInto    string   `json:"merged_into_user_id"`
}

// observer is the seeded administrator signed in through core's login.
func (b *seBoot) observer(t *testing.T) *coreAdmin {
	t.Helper()
	rec := post(t, b.app.Handler(), "/api/core/v1/auth/login", map[string]string{"username": "lucius.fox", "password": seLuciusP}, false)
	require.Equal(t, http.StatusOK, rec.Code, "the imported observer cannot sign in: %s", rec.Body.String())
	a := &coreAdmin{t: t, r: b.runningApp}
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") {
			a.cookie = ck
		}
	}
	require.NotNil(t, a.cookie, "no session cookie")
	return a
}

// accounts is every core user by ID.
func (b *seBoot) accounts(t *testing.T, a *coreAdmin) map[string]seCoreUser {
	t.Helper()
	rec := a.do(http.MethodGet, "/api/core/v1/users")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Items []seCoreUser `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotEmpty(t, body.Items, rec.Body.String())
	out := map[string]seCoreUser{}
	for _, u := range body.Items {
		out[u.ID] = u
	}
	return out
}

// coreMe is GET /api/core/v1/me under an Access assertion: the status and the
// user's ID (empty when refused), and the body.
func coreMe(t *testing.T, r *runningApp, assertion string) (int, string, string) {
	t.Helper()
	// Each request comes from its own peer address: the observer's listings share
	// 192.0.2.10 with accessGet, and the refused sign-ins here would otherwise
	// spend its failed-attempt budget (the listing then answers 429).
	seMeCalls++
	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/me", nil)
	req.RemoteAddr = fmt.Sprintf("192.0.2.%d:41000", 100+seMeCalls%100)
	req.Header.Set(cfaccess.CFAccessHeader, assertion)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me), rec.Body.String())
	}
	return rec.Code, me.User.ID, rec.Body.String()
}

// seMeCalls numbers coreMe's requests; the subtests run in order, never in
// parallel.
var seMeCalls int

// seResolve asks core's Access resolver, as the extension does, for the account
// of a verified identity. Calling it directly is how a test reads the REASON of a
// refusal: the route answers every refusal with the same 401, and an address that
// is on the allow-list can still be refused for not being allowed (which would
// pass a status check and prove nothing about the e-mail rule).
func (b *seBoot) resolve(t *testing.T, assertion string) (string, error) {
	t.Helper()
	cl, ok := seAssertions()[assertion]
	require.True(t, ok, "no assertion %q", assertion)
	u, err := b.mod.idw.policy.Resolve(context.Background(), oidc.Policy{}, oidc.Claims{
		Provider: cl.Provider, Subject: cl.Subject, Email: cl.Email, EmailVerified: true,
	})
	if err != nil {
		require.Nil(t, u, "a refusal returned an account too")
		return "", err
	}
	return string(u.ID), nil
}

// seRefused is the e-mail rule's refusal of an assertion, by both entry points:
// the resolver says the address belongs to an account this provider may not link
// (auth.ErrEmailInUse, not "not allowed"), and the route then answers 401.
func (b *seBoot) refused(t *testing.T, assertion string) {
	t.Helper()
	_, err := b.resolve(t, assertion)
	require.ErrorIs(t, err, auth.ErrEmailInUse, "%s: not refused as an address in use", assertion)
	code, id, body := coreMe(t, b.runningApp, assertion)
	require.True(t, refused(code), "%s: the route answered %d (user %q) after the resolver refused: %s", assertion, code, id, body)
	require.Empty(t, id, "%s", assertion)
}

// seIdentityRows is every core cfaccess identity row of the closed store, by
// subject: the user it links.
func seIdentityRows(t *testing.T, dbPath string) map[string]string {
	t.Helper()
	const prefix = "c/auth/identity\x00\x01cfaccess\x00\x01"
	out := map[string]string{}
	for k, v := range rawKVs(t, dbPath) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var row struct{ Subject, UserID string }
		require.True(t, strings.HasPrefix(v, "j"), "identity row %q is not a JSON value: %q", k, v)
		require.NoError(t, json.Unmarshal([]byte(v[1:]), &row), k)
		out[row.Subject] = row.UserID
	}
	return out
}

// TestIdentityAccessSameEmail_Boot: the shared-address install at full boot. One
// boot, one store, one address per case, the cases run in order and each is its
// own named test. The route is GET /api/core/v1/me under an Access assertion; the
// observer is an administrator who signs in with a password and lists the users.
//
// Not covered here: the ABS surface's cf entry point. A stored ABS-on corpus does
// not boot under this harness (the test binary exits inside app.New before the
// first request), so the second Access entry point is left to the B10d harness.
func TestIdentityAccessSameEmail_Boot(t *testing.T) {
	b := bootSameEmail(t)
	obs := b.observer(t)
	ids := b.ids
	before := b.accounts(t, obs)
	var signedIn []string // every user ID any Access sign-in returned

	// (a) M3 and core keep both same-email administrators. Imported, each has its
	// own ID, its roles and its verified flag, and neither is merged into the other.
	t.Run("pin5a_core_keeps_both_same_email_admins", func(t *testing.T) {
		for name, id := range map[string]string{"linked": ids.bruceLinked, "twin": ids.bruceTwin} {
			u, ok := before[id]
			require.True(t, ok, "%s admin %s is not a core account", name, id)
			require.Equal(t, seBruce, u.Email, name)
			require.True(t, u.EmailVerified, "%s: the verified flag was lost", name)
			require.Equal(t, []string{"admin"}, u.Roles, name)
			require.Equal(t, "active", u.Status, name)
			require.Empty(t, u.MergedInto, "%s was merged at import", name)
		}
		require.NotEqual(t, ids.bruceLinked, ids.bruceTwin)
		require.Equal(t, "bruce.wayne", before[ids.bruceLinked].Username)
		require.Equal(t, "batman", before[ids.bruceTwin].Username)
		n := 0
		for _, u := range before {
			if u.Email == seBruce {
				n++
			}
		}
		require.Equal(t, 2, n, "the address is held by %d accounts, not the two administrators", n)
	})

	// 1. The JWT sub is the link's subject: the sign-in takes the linked fast path
	// and lands on the linked administrator. The address alone could not choose
	// between the two administrators (it is refused: pin 2), so only the link can
	// have answered.
	t.Run("pin1_jwt_sub_takes_linked_fast_path", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			code, id, body := coreMe(t, b.runningApp, "bruce-sub")
			require.Equal(t, http.StatusOK, code, body)
			require.Equal(t, ids.bruceLinked, id, "the sub-keyed sign-in did not land on the linked account")
			require.NotEqual(t, ids.bruceTwin, id)
			signedIn = append(signedIn, id)
		}
		got, err := b.resolve(t, "bruce-sub")
		require.NoError(t, err)
		require.Equal(t, ids.bruceLinked, got)
		require.Equal(t, before, b.accounts(t, obs), "a linked sign-in changed the user list")
	})

	// 2. The same person under an e-mail-shaped subject does not match the link:
	// it takes the e-mail path, which refuses with two proven administrators
	// holding the address, and nothing is created. Checked before the linked
	// sign-in's effects could matter and again after, and for a subject core has
	// never seen.
	t.Run("pin2_email_shaped_subject_misses_the_link", func(t *testing.T) {
		b.refused(t, "bruce-email-subject")
		b.refused(t, "bruce-fresh")
		require.Equal(t, before, b.accounts(t, obs), "a refused sign-in changed the user list")
		code, id, body := coreMe(t, b.runningApp, "bruce-sub")
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, ids.bruceLinked, id)
		b.refused(t, "bruce-email-subject")
		require.Equal(t, before, b.accounts(t, obs))
	})

	// 4. The unlinked cases. Each address has its own account(s); only the rule
	// under test differs, and the control (one plain verified holder) does link,
	// so the refusals are the rule's and not a fixture that cannot link.
	t.Run("pin4_unlinked_address_rules", func(t *testing.T) {
		t.Run("two_verified_holders_refused", func(t *testing.T) {
			b.refused(t, "selina")
			require.Equal(t, before, b.accounts(t, obs))
		})
		t.Run("admin_holder_refused", func(t *testing.T) {
			require.Equal(t, []string{"admin"}, before[ids.harvey].Roles, "the case needs an admin holder")
			require.False(t, before[ids.harvey].HasPassword, "the case isolates the admin rule")
			b.refused(t, "harvey")
			require.Equal(t, before, b.accounts(t, obs))
		})
		t.Run("password_holder_refused", func(t *testing.T) {
			require.True(t, before[ids.oswald].HasPassword, "the case needs a holder with a password")
			require.NotContains(t, before[ids.oswald].Roles, "admin", "the case isolates the password rule")
			b.refused(t, "oswald")
			require.Equal(t, before, b.accounts(t, obs))
		})
		t.Run("control_one_plain_verified_holder_links", func(t *testing.T) {
			code, id, body := coreMe(t, b.runningApp, "alfred")
			require.Equal(t, http.StatusOK, code, body)
			require.Equal(t, ids.alfred, id, "the control did not link to the one holder")
			signedIn = append(signedIn, id)
			require.Equal(t, before, b.accounts(t, obs), "linking an existing account changed the user list")
		})
		t.Run("both_unverified_creates_a_viewer_and_displaces_no_admin", func(t *testing.T) {
			require.False(t, before[ids.edwardAdmin].EmailVerified)
			require.False(t, before[ids.edwardPlain].EmailVerified)
			code, id, body := coreMe(t, b.runningApp, "edward")
			require.Equal(t, http.StatusOK, code, body)
			signedIn = append(signedIn, id)
			require.NotContains(t, []string{ids.edwardAdmin, ids.edwardPlain}, id, "the sign-in landed on an unproven holder")
			after := b.accounts(t, obs)
			require.Len(t, after, len(before)+1, "exactly one account is new")
			created, ok := after[id]
			require.True(t, ok)
			require.Equal(t, []string{"viewer"}, created.Roles, "an Access-created account is a viewer")
			require.Equal(t, seEdward, created.Email)
			require.Equal(t, before[ids.edwardAdmin], after[ids.edwardAdmin], "the unproven admin was displaced")
			require.Equal(t, []string{"admin"}, after[ids.edwardAdmin].Roles)
			require.Equal(t, seEdward, after[ids.edwardAdmin].Email, "the admin lost its address")
			require.Equal(t, "", after[ids.edwardPlain].Email, "the plain unproven holder keeps the address the new account now holds")
			require.Equal(t, "active", after[ids.edwardPlain].Status)
			for uid, u := range before {
				if uid == ids.edwardPlain {
					continue
				}
				require.Equal(t, u, after[uid], "account %s (%s) changed", uid, u.Username)
			}
		})
	})

	// 6. (c) No silent merge: no Access sign-in path joined the two administrators
	// (or anyone). Both are still separate active admins with their own IDs, no
	// account is merged into another, and no sign-in ever returned the twin.
	t.Run("pin6_no_access_path_merges_accounts", func(t *testing.T) {
		after := b.accounts(t, obs)
		for uid, u := range after {
			require.Empty(t, u.MergedInto, "account %s (%s) was merged", uid, u.Username)
		}
		for _, id := range []string{ids.bruceLinked, ids.bruceTwin} {
			require.Equal(t, before[id], after[id], "administrator %s changed", id)
		}
		require.NotContains(t, signedIn, ids.bruceTwin, "an Access sign-in landed on the unlinked twin")
		require.Contains(t, signedIn, ids.bruceLinked)
	})

	// 5. (a) the identity link sits only on the linked administrator, and no
	// refused sign-in created one. Read from the closed store.
	t.Run("pin5b_identity_link_only_on_the_linked_admin", func(t *testing.T) {
		require.NoError(t, b.stop())
		rows := seIdentityRows(t, b.fx.dbPath)
		require.Equal(t, ids.bruceLinked, rows[seBruceSub], "the HEAD link did not arrive on the linked administrator")
		for subject, uid := range rows {
			require.NotEqual(t, ids.bruceTwin, uid, "subject %q links the unlinked twin", subject)
		}
		for _, subject := range []string{seBruce, "0f6e2d84-1b73-4c95-a8d0-93e17b5c4a26", "sub-selina", "sub-harvey", "sub-oswald"} {
			require.NotContains(t, rows, subject, "a refused sign-in left a link")
		}
		require.Equal(t, ids.alfred, rows["sub-alfred"])
		require.Contains(t, signedIn, rows["sub-edward"], "the created viewer is not the one the link names")
	})
}
