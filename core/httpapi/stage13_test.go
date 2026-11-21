// file: core/httpapi/stage13_test.go
// version: 0.1.0
// guid: 5b46251e-3aa8-4d69-80d4-ad6ffdf5c172
// last-edited: 2026-11-20

package httpapi

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
)

// s13Catalog is core's scopes plus books' (05-rbac §6.1), with
// books:fingerprint.worker the one Confining scope, as the identity cutover's
// rig declares them.
func s13Catalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	specs := rbac.CoreScopes()
	for _, n := range []string{"view", "edit", "delete", "organize", "scan", "integrations.manage", "settings.manage", "playlists.create",
		"collections.manage", "request", "request.approve", "reading", "admin.view", "admin.debug"} {
		specs = append(specs, rbac.PermissionSpec{Name: rbac.Permission("books:" + n), Title: n, Description: "books " + n})
	}
	specs = append(specs, rbac.PermissionSpec{Name: "books:fingerprint.worker", Title: "worker", Description: "fingerprint worker", Confining: true})
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func batman() *auth.User { return &auth.User{ID: "u-batman", Username: "batman"} }

// s13Session is an interactive session over role patterns.
func s13Session(cat *rbac.Catalog, patterns ...rbac.Permission) *auth.Principal {
	return &auth.Principal{User: batman(), Method: auth.MethodSession, CredentialID: "s-cave",
		Perms: authz.Compile(rbac.CompileInput{Interactive: true, RolePatterns: patterns}, cat)}
}

// s13Delegated is a temp-login session: may change credentials, not interactive.
func s13Delegated(cat *rbac.Catalog, patterns ...rbac.Permission) *auth.Principal {
	p := s13Session(cat, patterns...)
	p.Method = auth.MethodSessionDelegated
	p.Perms = authz.Compile(rbac.CompileInput{RolePatterns: patterns}, cat)
	return p
}

// s13Key is an API key: the owner's role patterns narrowed to scopes.
func s13Key(cat *rbac.Catalog, roles []rbac.Permission, scopes ...rbac.Permission) *auth.Principal {
	return &auth.Principal{User: batman(), Method: auth.MethodAPIKey, CredentialID: "k-belt",
		Perms: authz.Compile(rbac.CompileInput{Scopes: scopes, RolePatterns: roles}, cat)}
}

// s13Owner is an identity-extension sign-in carrying the owner assurance.
func s13Owner(cat *rbac.Catalog, patterns ...rbac.Permission) *auth.Principal {
	return &auth.Principal{User: batman(), Method: "ext:belfry", Assurance: auth.AssuranceOwner,
		Perms: authz.Compile(rbac.CompileInput{Interactive: true, RolePatterns: patterns}, cat)}
}

func s13AuthOff(cat *rbac.Catalog) *auth.Principal {
	return &auth.Principal{Method: auth.MethodAuthDisabled, Perms: authz.AuthDisabled(cat)}
}

// s13Atoms is the test's own copy of books' atom table (05-rbac §6.1): each
// HEAD atom and the native scopes whose AllOf is its route requirement. The
// atoms are string literals on purpose, and every atom a legacy manifest route
// names is here. playlists.create gates no route (zero route requirement).
func s13Atoms() map[string][]rbac.Permission {
	return map[string][]rbac.Permission{
		"library.view":          {"books:view", "core:tasks.view"},
		"library.edit_metadata": {"books:edit"},
		"library.delete":        {"books:delete"},
		"library.organize":      {"books:organize"},
		"scan.trigger":          {"books:scan"},
		"integrations.manage":   {"core:integrations.manage", "books:integrations.manage"},
		"users.manage":          {"core:users.manage", "core:roles.view"},
		"settings.manage": {"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"},
		"collections.manage": {"books:collections.manage"},
		"requests.create":    {"books:request"},
		"requests.approve":   {"books:request.approve"},
		"fingerprint.worker": {"books:fingerprint.worker"},
	}
}

// s13Resolver resolves s13Atoms; users.manage has the read-side entry
// core:users.view, and playlists.create resolves to the zero requirement as
// books' table does.
func s13Resolver(atom string, readSide bool) (rbac.Requirement, bool) {
	if readSide {
		if atom == "users.manage" {
			return rbac.Need("core:users.view"), true
		}
		return rbac.Requirement{}, false
	}
	if atom == "playlists.create" {
		return rbac.Requirement{}, true
	}
	scopes, ok := s13Atoms()[atom]
	if !ok {
		return rbac.Requirement{}, false
	}
	return rbac.AllOf(scopes...), true
}

type s13Want struct {
	status   int // 0 = admitted
	code     string
	required string
	clear    bool
}

func s13Check(t *testing.T, d AuthzDecision, w s13Want) {
	t.Helper()
	if d.ClearCookie != w.clear {
		t.Errorf("ClearCookie = %v, want %v", d.ClearCookie, w.clear)
	}
	if w.status == 0 {
		if d.Refusal != nil {
			t.Fatalf("refused %d %s (required %q), want admitted", d.Refusal.Status, d.Refusal.Code, d.Refusal.required)
		}
		return
	}
	if d.Refusal == nil {
		t.Fatalf("admitted, want %d %s", w.status, w.code)
	}
	if d.Refusal.Status != w.status || d.Refusal.Code != w.code || string(d.Refusal.required) != w.required {
		t.Fatalf("refused %d %s required %q, want %d %s required %q",
			d.Refusal.Status, d.Refusal.Code, d.Refusal.required, w.status, w.code, w.required)
	}
}

// TestStage13_Decision is every refusal of row 13 and the admits beside them.
// Each refusal row fails when its check is removed (mutation report in the PR).
func TestStage13_Decision(t *testing.T) {
	cat := s13Catalog(t)
	admin := s13Session(cat, rbac.CoreAdmin)
	robin := s13Session(cat, "books:view", "core:tasks.view", "core:self")
	bearer := &auth.Failure{Code: auth.CodeRevoked}
	cookie := &auth.Failure{Code: auth.CodeExpired, Cookie: true}
	opts := AuthzOptions{OwnerRequired: true, ResolveAtom: s13Resolver, Catalog: cat}
	notRequired := opts
	notRequired.OwnerRequired = false
	noResolver := opts
	noResolver.ResolveAtom = nil
	noCatalog := opts
	noCatalog.Catalog = nil
	answering := func(r rbac.Requirement) AuthzOptions {
		o := opts
		o.ResolveAtom = func(string, bool) (rbac.Requirement, bool) { return r, true }
		return o
	}
	interactive := Guards{Interactive: true}
	owner := Guards{Owner: true}
	cases := []struct {
		name string
		req  AuthzRequest
		opts AuthzOptions
		want s13Want
	}{
		// 1. The recorded failure.
		{"bearer failure on a native route", AuthzRequest{Perm: rbac.Need("books:view"), Failure: bearer}, opts, s13Want{401, "revoked", "", false}},
		{"cookie failure on a native route clears", AuthzRequest{Perm: rbac.Need("books:view"), Failure: cookie}, opts, s13Want{401, "expired", "", true}},
		{"bearer failure on a Public route", AuthzRequest{Perm: rbac.Public, Failure: bearer}, opts, s13Want{401, "revoked", "", false}},
		{"cookie failure on a Public route is anonymous and clears", AuthzRequest{Perm: rbac.Public, Failure: cookie}, opts, s13Want{0, "", "", true}},
		{"failure wins over a principal", AuthzRequest{Perm: rbac.Need("books:view"), Failure: bearer, Principal: admin}, opts, s13Want{401, "revoked", "", false}},
		{"unknown failure code is invalid_credential", AuthzRequest{Perm: rbac.Need("books:view"), Failure: &auth.Failure{Code: "joker"}}, opts, s13Want{401, "invalid_credential", "", false}},
		{"empty failure code is invalid_credential", AuthzRequest{Perm: rbac.Need("books:view"), Failure: &auth.Failure{}}, opts, s13Want{401, "invalid_credential", "", false}},
		{"inactive_user passes through", AuthzRequest{Perm: rbac.Need("books:view"), Failure: &auth.Failure{Code: auth.CodeInactiveUser}}, opts, s13Want{401, "inactive_user", "", false}},
		{"password_reset_required passes through", AuthzRequest{Perm: rbac.Need("books:view"), Failure: &auth.Failure{Code: auth.CodeResetRequired}}, opts, s13Want{401, "password_reset_required", "", false}},
		{"invalid_credential passes through", AuthzRequest{Perm: rbac.Need("books:view"), Failure: &auth.Failure{Code: auth.CodeInvalidCredential}}, opts, s13Want{401, "invalid_credential", "", false}},

		// 2-3. Public, and no principal.
		{"Public anonymous", AuthzRequest{Perm: rbac.Public}, opts, s13Want{}},
		{"no principal", AuthzRequest{Perm: rbac.Need("books:view")}, opts, s13Want{401, "authentication_required", "", false}},
		{"no principal on AnyAuthenticated", AuthzRequest{Perm: rbac.AnyAuthenticated}, opts, s13Want{401, "authentication_required", "", false}},
		{"Public with a guard needs a principal", AuthzRequest{Perm: rbac.Public, Guards: interactive}, opts, s13Want{401, "authentication_required", "", false}},
		{"Public with a guard: cookie failure is 401", AuthzRequest{Perm: rbac.Public, Guards: owner, Failure: cookie}, opts, s13Want{401, "expired", "", true}},

		// 4. Guards.
		{"Interactive refuses a key", AuthzRequest{Perm: rbac.Need(rbac.CoreAdmin), Guards: interactive, Principal: s13Key(cat, []rbac.Permission{rbac.CoreAdmin}, rbac.CoreAdmin)}, opts, s13Want{403, "interactive_required", "", false}},
		{"Interactive refuses auth-disabled", AuthzRequest{Perm: rbac.Need("books:view"), Guards: interactive, Principal: s13AuthOff(cat)}, opts, s13Want{403, "interactive_required", "", false}},
		{"Interactive admits a delegated session", AuthzRequest{Perm: rbac.Need("books:view"), Guards: interactive, Principal: s13Delegated(cat, "books:view")}, opts, s13Want{}},
		{"Owner required refuses an admin without assurance", AuthzRequest{Perm: rbac.Need(rbac.CoreAdmin), Guards: owner, Principal: admin}, opts, s13Want{403, "owner_required", "", false}},
		{"Owner required admits the owner", AuthzRequest{Perm: rbac.Need("books:view"), Guards: owner, Principal: s13Owner(cat, "books:view")}, opts, s13Want{}},
		{"Owner required: the owner still needs Perm", AuthzRequest{Perm: rbac.Need("books:delete"), Guards: owner, Principal: s13Owner(cat, "books:view")}, opts, s13Want{403, "permission_denied", "books:delete", false}},
		// Today an identity extension's method never passes MayChangeCredentials
		// (core methods only; auth exposes no MethodSpec lookup), so the
		// Access-verified owner is refused a route that is Interactive as well as
		// Owner. Pinned so B4 sees it; reported in the PR, not widened here.
		{"Interactive plus Owner refuses an extension owner", AuthzRequest{Perm: rbac.Need("books:view"), Guards: Guards{Interactive: true, Owner: true}, Principal: s13Owner(cat, "books:view")}, opts, s13Want{403, "interactive_required", "", false}},
		{"Owner fallback refuses a key admin", AuthzRequest{Perm: rbac.Need("books:view"), Guards: owner, Principal: s13Key(cat, []rbac.Permission{rbac.CoreAdmin}, rbac.CoreAdmin)}, notRequired, s13Want{403, "interactive_required", "", false}},
		{"Owner fallback refuses a non-admin session", AuthzRequest{Perm: rbac.Need("books:view"), Guards: owner, Principal: robin}, notRequired, s13Want{403, "permission_denied", "core:admin", false}},
		{"Owner fallback refuses auth-disabled", AuthzRequest{Perm: rbac.Need("books:view"), Guards: owner, Principal: s13AuthOff(cat)}, notRequired, s13Want{403, "interactive_required", "", false}},
		{"Owner fallback refuses a delegated session without admin", AuthzRequest{Perm: rbac.Need("books:view"), Guards: owner, Principal: s13Delegated(cat, "books:view")}, notRequired, s13Want{403, "permission_denied", "core:admin", false}},
		{"Owner fallback admits an admin session", AuthzRequest{Perm: rbac.Need("books:view"), Guards: owner, Principal: admin}, notRequired, s13Want{}},

		// 5. Perm.
		{"Perm admits", AuthzRequest{Perm: rbac.Need("books:view"), Principal: robin}, opts, s13Want{}},
		{"Perm names the missing member of an AllOf", AuthzRequest{Perm: rbac.AllOf("books:view", "books:edit"), Principal: robin}, opts, s13Want{403, "permission_denied", "books:edit", false}},
		{"Perm names the first member of an AnyOf", AuthzRequest{Perm: rbac.AnyOf("books:edit", "books:delete"), Principal: robin}, opts, s13Want{403, "permission_denied", "books:edit", false}},
		{"a key narrows its owner", AuthzRequest{Perm: rbac.Need("books:edit"), Principal: s13Key(cat, []rbac.Permission{rbac.CoreAdmin}, "books:view")}, opts, s13Want{403, "permission_denied", "books:edit", false}},
		{"a confined key is refused AnyAuthenticated", AuthzRequest{Perm: rbac.AnyAuthenticated, Principal: s13Key(cat, []rbac.Permission{rbac.CoreAdmin}, "books:fingerprint.worker")}, opts, s13Want{403, "permission_denied", "", false}},
		{"a principal with no checker holds nothing", AuthzRequest{Perm: rbac.AnyAuthenticated, Principal: &auth.Principal{User: batman(), Method: auth.MethodSession}}, opts, s13Want{403, "permission_denied", "", false}},
		{"a Delegated requirement is refused", AuthzRequest{Perm: rbac.Delegated("abs"), Principal: admin}, opts, s13Want{403, "permission_denied", "", false}},
		{"the zero requirement is refused", AuthzRequest{Perm: rbac.Requirement{}, Principal: admin}, opts, s13Want{403, "permission_denied", "", false}},
		{"a Sensitive scope refused to a key is interactive_required", AuthzRequest{Perm: rbac.Need("core:roles.manage"), Principal: s13Key(cat, []rbac.Permission{rbac.CoreAdmin}, rbac.CoreAdmin)}, opts, s13Want{403, "interactive_required", "", false}},
		{"without a catalog it is permission_denied", AuthzRequest{Perm: rbac.Need("core:roles.manage"), Principal: s13Key(cat, []rbac.Permission{rbac.CoreAdmin}, rbac.CoreAdmin)}, noCatalog, s13Want{403, "permission_denied", "core:roles.manage", false}},
		{"a Sensitive scope an interactive session lacks is permission_denied", AuthzRequest{Perm: rbac.Need("core:roles.manage"), Principal: robin}, opts, s13Want{403, "permission_denied", "core:roles.manage", false}},
		{"auth-disabled refused an AuthRequired requirement is 401", AuthzRequest{Perm: rbac.Need(rbac.CoreAdmin), Principal: s13AuthOff(cat)}, opts, s13Want{401, "authentication_required", "", false}},
		{"auth-disabled refused a Sensitive requirement is 401", AuthzRequest{Perm: rbac.Need("core:roles.manage"), Principal: s13AuthOff(cat)}, opts, s13Want{401, "authentication_required", "", false}},
		{"auth-disabled admitted a plain scope", AuthzRequest{Perm: rbac.Need("books:edit"), Principal: s13AuthOff(cat)}, opts, s13Want{}},

		// 5. Legacy kinds, resolved before Satisfies.
		{"legacy atom resolved and admitted", AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: robin}, opts, s13Want{}},
		{"legacy atom resolved and refused", AuthzRequest{Perm: rbac.LegacyAtom("library.edit_metadata"), Principal: robin}, opts, s13Want{403, "permission_denied", "books:edit", false}},
		{"legacy atom with no resolver refuses an admin", AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: admin}, noResolver, s13Want{403, "permission_denied", "", false}},
		{"unknown legacy atom refuses an admin", AuthzRequest{Perm: rbac.LegacyAtom("joker.laugh"), Principal: admin}, opts, s13Want{403, "permission_denied", "", false}},
		{"an atom with no route requirement refuses an admin", AuthzRequest{Perm: rbac.LegacyAtom("playlists.create"), Principal: admin}, opts, s13Want{403, "permission_denied", "", false}},
		{"a resolver answering Public is refused", AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: robin}, answering(rbac.Public), s13Want{403, "permission_denied", "", false}},
		{"a resolver answering AnyAuthenticated is refused", AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: robin}, answering(rbac.AnyAuthenticated), s13Want{403, "permission_denied", "", false}},
		{"a resolver answering a legacy kind is refused", AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: admin}, answering(rbac.LegacyAtom("library.view")), s13Want{403, "permission_denied", "", false}},
		{"a resolver answering AnyOf is judged", AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: robin}, answering(rbac.AnyOf("books:edit", "books:view")), s13Want{}},
		{"legacy check read side admits the read-side scope", AuthzRequest{Perm: rbac.LegacyCheck("users.manage", true), Principal: s13Session(cat, "core:users.view")}, opts, s13Want{}},
		{"legacy check write side refuses the read-side scope", AuthzRequest{Perm: rbac.LegacyCheck("users.manage", false), Principal: s13Session(cat, "core:users.view")}, opts, s13Want{403, "permission_denied", "core:users.manage", false}},
		{"legacy check read side unknown for an atom is refused", AuthzRequest{Perm: rbac.LegacyCheck("library.view", true), Principal: admin}, opts, s13Want{403, "permission_denied", "", false}},
		{"auth-disabled keeps HEAD's s.perm reach on settings.manage", AuthzRequest{Perm: rbac.LegacyAtom("settings.manage"), Principal: s13AuthOff(cat)}, opts, s13Want{}},
		{"auth-disabled is refused a legacy check it would satisfy", AuthzRequest{Perm: rbac.LegacyCheck("library.view", false), Principal: s13AuthOff(cat)}, opts, s13Want{401, "authentication_required", "", false}},
		{"auth-disabled is refused an unresolvable atom with 401", AuthzRequest{Perm: rbac.LegacyAtom("joker.laugh"), Principal: s13AuthOff(cat)}, opts, s13Want{401, "authentication_required", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { s13Check(t, Authorize(c.req, c.opts), c.want) })
	}
}

// TestStage13_LegacyAtomRandomRolesMatchTheSuccessor is the custom-role half
// of the reach oracle over random native roles (05-http-api 8.1, SEC4-2): for
// every legacy atom of the table and random role patterns (sessions, keys,
// delegated sessions, confined keys, partial settings.manage roles,
// wildcards, admin, auth off), stage 13 admits a LegacyAtom route exactly when
// the atom's native successor, AllOf its table scopes, admits the principal.
// Both outcomes must occur for every atom, so neither side is vacuous.
func TestStage13_LegacyAtomRandomRolesMatchTheSuccessor(t *testing.T) {
	cat := s13Catalog(t)
	atoms := s13Atoms()
	names := make([]string, 0, len(atoms))
	for a := range atoms {
		names = append(names, a)
	}
	slices.Sort(names)
	var concrete []rbac.Permission
	for spec := range cat.All() {
		concrete = append(concrete, spec.Name)
	}
	slices.Sort(concrete)
	wild := []rbac.Permission{"books:*", "core:*", "core:system.*", "core:tasks.*", "books:request.*"}
	settings := atoms["settings.manage"]
	rng := rand.New(rand.NewPCG(13, 2026))
	pick := func(pool []rbac.Permission, n int) []rbac.Permission {
		out := make([]rbac.Permission, 0, n)
		for range n {
			out = append(out, pool[rng.IntN(len(pool))])
		}
		return out
	}
	roles := func() []rbac.Permission {
		switch rng.IntN(10) {
		case 0: // a custom role holding all but one of settings.manage's scopes
			drop := rng.IntN(len(settings))
			return append(slices.Clone(settings[:drop]), settings[drop+1:]...)
		case 1:
			return append(pick(concrete, rng.IntN(4)), rbac.CoreAdmin)
		case 2:
			return append(pick(concrete, rng.IntN(6)), pick(wild, 1+rng.IntN(2))...)
		case 3: // a whole atom's scopes, sometimes plus noise
			return append(slices.Clone(atoms[names[rng.IntN(len(names))]]), pick(concrete, rng.IntN(3))...)
		default:
			return pick(concrete, rng.IntN(12))
		}
	}
	opts := AuthzOptions{OwnerRequired: true, ResolveAtom: s13Resolver, Catalog: cat}
	admits, denies := map[string]int{}, map[string]int{}
	const rounds = 600
	for i := range rounds {
		var p *auth.Principal
		switch rng.IntN(6) {
		case 0:
			p = s13Key(cat, roles(), pick(concrete, 1+rng.IntN(8))...)
		case 1:
			p = s13Delegated(cat, roles()...)
		case 2:
			p = s13Key(cat, roles(), "books:fingerprint.worker")
		case 3:
			if rng.IntN(4) == 0 {
				p = s13AuthOff(cat)
				break
			}
			fallthrough
		default:
			p = s13Session(cat, roles()...)
		}
		for _, atom := range names {
			got := Authorize(AuthzRequest{Perm: rbac.LegacyAtom(atom), Principal: p}, opts).Admitted()
			successor := p.Perms.Satisfies(rbac.AllOf(atoms[atom]...))
			if got && !successor {
				t.Fatalf("round %d: %s admitted %s (perms %v), which its successor refuses", i, atom, p.Method, p.Perms.Concrete())
			}
			if !got && successor {
				t.Fatalf("round %d: %s refused %s (perms %v), which its successor admits", i, atom, p.Method, p.Perms.Concrete())
			}
			if got {
				admits[atom]++
			} else {
				denies[atom]++
			}
		}
	}
	for _, atom := range names {
		if admits[atom] == 0 || denies[atom] == 0 {
			t.Errorf("%s: %d admits, %d denies over %d rounds; both must occur", atom, admits[atom], denies[atom], rounds)
		}
	}
	if len(names) < 12 {
		t.Fatalf("the table names %d atoms, want every route atom", len(names))
	}
}

// s13Problem decodes a refusal's body.
func s13Problem(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem body %q: %v", rec.Body.String(), err)
	}
	return p
}

// TestStage13_Middleware applies the decision over a handler: a refusal is a
// problem and never reaches the handler, an admit does, the session cookie is
// cleared when the decision says so, and core.auth.owner.required is read per
// request with nil meaning required.
func TestStage13_Middleware(t *testing.T) {
	cat := s13Catalog(t)
	clear := func(*http.Request) *http.Cookie {
		return &http.Cookie{Name: "mediacore_session", Value: "", Path: "/", MaxAge: -1}
	}
	if _, err := Stage13(rbac.Need("books:view"), Guards{}, Stage13Options{}); !errors.Is(err, ErrStage13Options) {
		t.Fatalf("Stage13 with no ClearCookie: err = %v, want ErrStage13Options", err)
	}
	serve := func(t *testing.T, mw func(http.Handler) http.Handler, p *auth.Principal) (*httptest.ResponseRecorder, bool) {
		t.Helper()
		reached := false
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }))
		r := httptest.NewRequest(http.MethodGet, "/api/core/v1/gotham", nil)
		if p != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec, reached
	}
	mw, err := Stage13(rbac.Need("books:edit"), Guards{}, Stage13Options{ClearCookie: clear, ResolveAtom: s13Resolver, Catalog: cat})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("refusal is a problem and stops", func(t *testing.T) {
		rec, reached := serve(t, mw, s13Session(cat, "books:view"))
		if reached || rec.Code != http.StatusForbidden {
			t.Fatalf("reached=%v status=%d, want a 403 that stops", reached, rec.Code)
		}
		if p := s13Problem(t, rec); p.Code != "permission_denied" || p.Required != "books:edit" {
			t.Fatalf("problem %+v, want permission_denied naming books:edit", p)
		}
	})
	t.Run("no principal is 401", func(t *testing.T) {
		rec, reached := serve(t, mw, nil)
		if reached || rec.Code != http.StatusUnauthorized || s13Problem(t, rec).Code != "authentication_required" {
			t.Fatalf("reached=%v status=%d body=%s", reached, rec.Code, rec.Body)
		}
	})
	t.Run("admit reaches the handler", func(t *testing.T) {
		if rec, reached := serve(t, mw, s13Session(cat, "books:edit")); !reached || rec.Code != http.StatusOK {
			t.Fatalf("reached=%v status=%d", reached, rec.Code)
		}
	})
	t.Run("owner required by default", func(t *testing.T) {
		mw, err := Stage13(rbac.Need("books:view"), Guards{Owner: true}, Stage13Options{ClearCookie: clear})
		if err != nil {
			t.Fatal(err)
		}
		rec, reached := serve(t, mw, s13Session(cat, rbac.CoreAdmin))
		if reached || rec.Code != http.StatusForbidden || s13Problem(t, rec).Code != "owner_required" {
			t.Fatalf("reached=%v status=%d body=%s", reached, rec.Code, rec.Body)
		}
	})
	t.Run("owner required is read per request", func(t *testing.T) {
		required := false
		mw, err := Stage13(rbac.Need("books:view"), Guards{Owner: true}, Stage13Options{ClearCookie: clear, OwnerRequired: func() bool { return required }})
		if err != nil {
			t.Fatal(err)
		}
		if _, reached := serve(t, mw, s13Session(cat, rbac.CoreAdmin)); !reached {
			t.Fatal("fallback: an interactive admin was refused")
		}
		required = true
		if rec, reached := serve(t, mw, s13Session(cat, rbac.CoreAdmin)); reached || s13Problem(t, rec).Code != "owner_required" {
			t.Fatalf("after the owner is set: reached=%v body=%s", reached, rec.Body)
		}
	})
	cookieCleared := func(rec *httptest.ResponseRecorder) bool {
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == "mediacore_session" && ck.MaxAge < 0 {
				return true
			}
		}
		return false
	}
	withFailure := func(f auth.Failure) func(*http.Request) (*auth.Failure, *auth.Principal) {
		return func(*http.Request) (*auth.Failure, *auth.Principal) { return &f, nil }
	}
	for _, c := range []struct {
		name    string
		perm    rbac.Requirement
		fail    auth.Failure
		status  int
		reached bool
		cleared bool
	}{
		{"cookie failure on Public: anonymous, cleared", rbac.Public, auth.Failure{Code: auth.CodeExpired, Cookie: true}, http.StatusOK, true, true},
		{"cookie failure on a native route: 401, cleared", rbac.Need("books:view"), auth.Failure{Code: auth.CodeExpired, Cookie: true}, http.StatusUnauthorized, false, true},
		{"bearer failure on Public: 401, nothing cleared", rbac.Public, auth.Failure{Code: auth.CodeRevoked}, http.StatusUnauthorized, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			mw, err := stage13(c.perm, Guards{}, Stage13Options{ClearCookie: clear}, withFailure(c.fail))
			if err != nil {
				t.Fatal(err)
			}
			rec, reached := serve(t, mw, nil)
			if rec.Code != c.status || reached != c.reached || cookieCleared(rec) != c.cleared {
				t.Fatalf("status=%d reached=%v cleared=%v, want %d %v %v", rec.Code, reached, cookieCleared(rec), c.status, c.reached, c.cleared)
			}
			if c.status == http.StatusUnauthorized && s13Problem(t, rec).Code != c.fail.Code {
				t.Fatalf("code %q, want %q", s13Problem(t, rec).Code, c.fail.Code)
			}
		})
	}
	t.Run("stage 12's real context outcome", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if f, p := stage12Outcome(r); f != nil || p != nil {
			t.Fatalf("empty context: %v %v", f, p)
		}
		robin := s13Session(cat, "books:view")
		if _, p := stage12Outcome(r.WithContext(auth.WithPrincipal(r.Context(), robin))); p != robin {
			t.Fatal("bound principal not read")
		}
	})
}

// s13Lax is a checker that satisfies every requirement of every kind, legacy
// and Delegated included. No core checker does; it shows Authorize refuses
// what it cannot resolve on its own, never because the checker happens to.
type s13Lax struct{}

func (s13Lax) Has(rbac.Permission) bool        { return true }
func (s13Lax) Satisfies(rbac.Requirement) bool { return true }
func (s13Lax) Confined() bool                  { return false }
func (s13Lax) Concrete() []rbac.Permission     { return nil }

// TestStage13_RefusesWhatItCannotResolveWhateverTheChecker: a legacy kind is
// resolved before any checker sees it (COR6-1), so a nil resolver, an unknown
// atom, an atom with no route requirement, a Delegated and the zero
// requirement are refused even to a checker that would admit them.
func TestStage13_RefusesWhatItCannotResolveWhateverTheChecker(t *testing.T) {
	lax := &auth.Principal{User: batman(), Method: auth.MethodSession, Perms: s13Lax{}}
	opts := AuthzOptions{OwnerRequired: true, ResolveAtom: s13Resolver}
	for _, c := range []struct {
		name string
		perm rbac.Requirement
		opts AuthzOptions
	}{
		{"nil resolver", rbac.LegacyAtom("library.view"), AuthzOptions{OwnerRequired: true}},
		{"nil resolver, legacy check", rbac.LegacyCheck("users.manage", true), AuthzOptions{OwnerRequired: true}},
		{"unknown atom", rbac.LegacyAtom("joker.laugh"), opts},
		{"no route requirement", rbac.LegacyAtom("playlists.create"), opts},
		{"resolver says not ok but answers", rbac.LegacyAtom("library.view"), AuthzOptions{OwnerRequired: true, ResolveAtom: func(string, bool) (rbac.Requirement, bool) { return rbac.Need("books:view"), false }}},
		{"Delegated", rbac.Delegated("abs"), opts},
		{"zero", rbac.Requirement{}, opts},
	} {
		t.Run(c.name, func(t *testing.T) {
			s13Check(t, Authorize(AuthzRequest{Perm: c.perm, Principal: lax}, c.opts), s13Want{403, "permission_denied", "", false})
		})
	}
	s13Check(t, Authorize(AuthzRequest{Perm: rbac.LegacyAtom("library.view"), Principal: lax}, opts), s13Want{})
}
