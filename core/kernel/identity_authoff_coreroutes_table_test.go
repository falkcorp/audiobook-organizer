// file: core/kernel/identity_authoff_coreroutes_table_test.go
// version: 0.1.0
// guid: e33bbb5b-70d5-4e75-90b9-2b4448b1d2b1
// last-edited: 2026-11-21

package kernel

// Batman names only. F4-08, the core-route half of F4-07's reach oracle
// (09 section 9.2.6): the EXPECTED table. Each row is what 05-authn 4.11 and
// 05-rbac 4.11 / 6.2 say the auth-disabled principal gets from one core route,
// written from the spec text, never from what a boot answered. A route with no
// row, a row with no route, and a probe that matches no row all fail by name
// (TestAuthOffCoreRoutes_*).
//
// Two spec readings of "4.11 (a)-(d)" exist and the rows cite the text, not a
// letter alone. 05-rbac 4.11 lists the auth-off tightenings as (a) every
// Guards.Interactive route, (b) the settings.manage system detail and log
// routes, (c) the backup routes, (d) the credential-metadata and audit routes.
// 05-authn 4.11 has four bullets: the checker, the Interactive refusal,
// "Credential metadata needs a user" (SEC-3) and the start-up refusal on a
// remote-reachable install. Neither says a route's verdict differs between a
// loopback-only and a remote-reachable auth-off install: they differ in the
// start-up refusal (ErrAuthDisabledRemote) and in the posture health reports
// (auth_disabled_remote), which the posture tests pin. Every row therefore has
// one Want, held in all three postures.
//
// No backup, health, log, task, updater or event route is mounted by the
// identity boot at this base, so 05-rbac 4.11 (b) and (c) have no cells; a
// route mounted later without a row here fails TestAuthOffCoreRoutes_
// EveryCoreRouteHasARow, which is how those clauses get their rows.

// offWant is what a row says the probe shows.
type offWant int

const (
	// offAllow: the request reaches the handler and is not answered 401 or 403
	// (the brief's rule: 401/403 is a denial, any other status below 500 is an
	// allow).
	offAllow offWant = iota + 1
	// offDeny: the auth-disabled principal is refused 401 or 403, with Code.
	offDeny
	// offHandler: a Public route (or a route admitted by stage 13) whose
	// handler then answers the probe itself 401 or 403 with Code, because the
	// probe carries no token, no valid setup state and so on. It is not the
	// router's reach, so it reads as an allow of the route, and the status is
	// the one the brief's letter rule calls a denial. Rows of this kind are the
	// pinned list, as F4-07's reachRecorderLimits is.
	offHandler
)

func (w offWant) String() string {
	switch w {
	case offAllow:
		return "allow"
	case offDeny:
		return "deny"
	case offHandler:
		return "handler-refusal"
	}
	return "unset"
}

// offRow is one expected cell of the table.
type offRow struct {
	// Route is "METHOD /pattern" as Router.Routes lists it. Name names a
	// variant of the route's probe (a different body for the same route).
	Route, Name string
	// Body is the probe's JSON body; "" sends {} on a write and nothing on a
	// read. Bodies are valid for the handler's own Validate, so a handler's
	// own refusal is reached: {} on a route that validates its body answers 422
	// before the handler's user check (reported as an observation).
	Body string
	Want offWant
	// Code is the problem code of the refusal (Want offDeny, offHandler).
	Code string
	// Clause is the spec text that gives the row its verdict.
	Clause string
	// Head is R0's recorded verdict for the auth-off principal on the
	// equivalent route (internal/books/rbacreach/verdicts.json, principal
	// "authoff"), "A" past the authorization decision or "D" 401/403; "" when
	// R0 has no equivalent. HeadRoute is that R0 route.
	Head, HeadRoute string
	// Tightens is the spec clause that makes a route R0 allowed (Head "A") a
	// denial now. Beyond marks the one tightening the spec text does not list.
	Tightens string
	Beyond   bool
}

// key is the row's identity in the table.
func (r offRow) key() string {
	if r.Name == "" {
		return r.Route
	}
	return r.Route + " [" + r.Name + "]"
}

// The clauses.
const (
	clPublic        = "05-authn 4.13 table (Public) and 05-http-api 4.2 row 13 step 2: a Public route needs no principal, so stage 13 admits"
	clPublicHandler = clPublic + "; the handler then refuses a probe that carries no valid token or state (not the router's reach)"
	clAnyAuth       = "05-rbac 4.10 and 4.11: AnyAuthenticated is core:self or a non-Confining scope, and the auth-disabled checker holds core:self (neither AuthRequired nor Sensitive)"
	clOpenAPI       = "05-http-api 7 (the document is AnyAuthenticated unless core.http.openapi.public) with 05-rbac 4.11: the auth-disabled principal holds core:self"
	clView          = "05-rbac 4.11 bullet 1 and 05-authn 4.13 (no guard on the row): the auth-disabled checker holds every scope that is neither AuthRequired nor Sensitive, and core:users.view and core:roles.view are ClassView only"
	clInteractive   = "05-rbac 4.11 (a) and 05-authn 4.11 bullet 2 (SEC5-8): a Guards.Interactive route is refused because the auth-disabled principal is non-interactive (Method.MayChangeCredentials is false)"
	clNeedUser      = "05-authn 4.11 bullet 3 (SEC-3) and 05-rbac 4.11 (d): credential metadata needs a user, the auth-disabled principal has none, so 401 authentication_required before the handler acts"
	clAudit         = "05-rbac 4.11 (d) and 6.1: core:audit.view is AuthRequired, so the auth-disabled checker never holds it and the audit query and export answer 401"
	clReset         = "05-rbac 6.2 (factory-reset row) and 4.11 bullet 1: core:admin is AuthRequired and the route carries Guards.Interactive and Guards.Owner; Interactive refuses first"
	clSettings      = "05-config 1.3.7 (cited by confighttp/routes.go): settings keep their auth-off reach, AnyAuthenticated with the per-section and per-key checks inside the handler"
	clProtect       = "05-config 1.3.7 and 05-authn 4.11 (SEC4-8): the auth-disabled principal changes any key below ProtectAdmin and none at or above it; core.auth.mode is never changed by it"
	clUIReport      = "07 7.5.2 (AnyAuthenticated, no guard; uihttp/uihttp.go 101-106): the shell of an auth-off install still reports, a report is not a credential change"
	// clBeyond is the one cell no clause lists: GET /users/invites. 05-authn 4.13
	// has no row for it and 05-rbac 4.11 (a)-(d) do not name it; authhttp's
	// spec marks it needUser, a tightening of HEAD's open listing that the spec
	// text does not state. It is reported, not blocked.
	clBeyond = "NOT LISTED by 05-authn 4.13's table or 05-rbac 4.11 (a)-(d): authhttp marks GET /users/invites needUser, so it is stricter than the spec text (a tightening beyond the spec, reported)"
)

// offCoreRows is the table of every core route the identity boot mounts, one
// row per route, plus named variants of PATCH /settings for the keys the
// auth-disabled principal must not change.
var offCoreRows = []offRow{
	// ---- the shell, modules and documents ------------------------------------
	{Route: "GET /api/core/v1/modules", Want: offAllow, Clause: clAnyAuth},
	{Route: "GET /api/core/v1/openapi/{file}", Want: offAllow, Clause: clOpenAPI},
	{Route: "POST /api/core/v1/ui/diagnostics", Body: `{"kind":"ui-missing","module":"alfred"}`, Want: offAllow, Clause: clUIReport},
	{Route: "POST /api/core/v1/ui/csp-report", Want: offAllow, Clause: clPublic},

	// ---- Public identity entry routes ----------------------------------------
	{Route: "GET /api/core/v1/auth/status", Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "GET /api/v1/auth/status"},
	{Route: "POST /api/core/v1/auth/setup", Want: offHandler, Code: "setup_closed", Clause: clPublicHandler, Head: "A", HeadRoute: "POST /api/v1/auth/setup"},
	{Route: "POST /api/core/v1/auth/bootstrap", Want: offHandler, Code: "bootstrap_invalid", Clause: clPublicHandler, Head: "A", HeadRoute: "POST /api/v1/auth/bootstrap"},
	{Route: "GET /api/core/v1/auth/providers", Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "GET /api/v1/auth/oauth-providers"},
	{Route: "POST /api/core/v1/auth/invites/lookup", Body: offInviteLookupBody, Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "GET /api/v1/auth/invite/:token"},
	{Route: "POST /api/core/v1/auth/login", Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "POST /api/v1/auth/login"},
	{Route: "POST /api/core/v1/auth/logout", Want: offAllow, Clause: clAnyAuth, Head: "A", HeadRoute: "POST /api/v1/auth/logout"},
	{Route: "POST /api/core/v1/auth/invites/accept", Want: offHandler, Code: "link_invalid", Clause: clPublicHandler, Head: "A", HeadRoute: "POST /api/v1/auth/accept-invite"},
	{Route: "POST /api/core/v1/auth/temp-login/consume", Want: offHandler, Code: "link_invalid", Clause: clPublicHandler},
	{Route: "GET /api/core/v1/auth/oidc/{provider}/start", Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "GET /api/v1/auth/oauth/:provider/start"},
	{Route: "GET /api/v1/auth/oauth/{provider}/start", Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "GET /api/v1/auth/oauth/:provider/start"},
	{Route: "GET /api/v1/auth/oauth/{provider}/callback", Want: offAllow, Clause: clPublic, Head: "A", HeadRoute: "GET /api/v1/auth/oauth/:provider/callback"},

	// ---- the caller's own account: a user is required -------------------------
	{Route: "GET /api/core/v1/me", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "GET /api/v1/auth/me"},
	{Route: "PATCH /api/core/v1/me", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "D", HeadRoute: "PATCH /api/v1/auth/me"},
	{Route: "PUT /api/core/v1/me/password", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "D", HeadRoute: "PUT /api/v1/auth/me/password"},
	{Route: "GET /api/core/v1/me/sessions", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "GET /api/v1/auth/sessions"},
	{Route: "DELETE /api/core/v1/me/sessions/{publicID}", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "D", HeadRoute: "DELETE /api/v1/auth/sessions/:id"},
	{Route: "GET /api/core/v1/me/api-keys", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "GET /api/v1/auth/api-keys"},
	{Route: "POST /api/core/v1/me/api-keys", Body: offKeyCreateBody, Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "POST /api/v1/auth/api-keys"},
	{Route: "PATCH /api/core/v1/me/api-keys/{id}", Body: offKeyStatusBody, Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "PATCH /api/v1/auth/api-keys/:id"},
	{Route: "DELETE /api/core/v1/me/api-keys/{id}", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "DELETE /api/v1/auth/api-keys/:id"},

	// ---- API keys of other users, by ID ----------------------------------------
	{Route: "GET /api/core/v1/api-keys", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "GET /api/v1/auth/api-keys"},
	{Route: "GET /api/core/v1/api-keys/{id}", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "GET /api/v1/auth/api-keys/:id"},
	{Route: "PATCH /api/core/v1/api-keys/{id}", Body: offKeyStatusBody, Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "PATCH /api/v1/auth/api-keys/:id"},
	{Route: "DELETE /api/core/v1/api-keys/{id}", Want: offDeny, Code: "authentication_required", Clause: clNeedUser, Head: "D", HeadRoute: "DELETE /api/v1/auth/api-keys/:id"},
	{Route: "GET /api/core/v1/users/{id}/api-keys", Want: offDeny, Code: "authentication_required", Clause: clNeedUser},
	{Route: "POST /api/core/v1/users/{id}/api-keys", Want: offDeny, Code: "interactive_required", Clause: clInteractive},

	// ---- users ------------------------------------------------------------------
	{Route: "GET /api/core/v1/users", Want: offAllow, Clause: clView, Head: "A", HeadRoute: "GET /api/v1/users"},
	{Route: "GET /api/core/v1/users/{id}", Want: offAllow, Clause: clView},
	{Route: "GET /api/core/v1/users/invites", Want: offDeny, Code: "authentication_required", Clause: clBeyond, Head: "A", HeadRoute: "GET /api/v1/users/invites", Tightens: clBeyond, Beyond: true},
	{Route: "POST /api/core/v1/users/invites", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "A", HeadRoute: "POST /api/v1/users/invite", Tightens: clInteractive},
	{Route: "DELETE /api/core/v1/users/invites/{id}", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "A", HeadRoute: "DELETE /api/v1/users/invites/:id", Tightens: clInteractive},
	{Route: "POST /api/core/v1/users/{id}/disable", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "A", HeadRoute: "POST /api/v1/users/:id/deactivate", Tightens: clInteractive},
	{Route: "POST /api/core/v1/users/{id}/enable", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "A", HeadRoute: "POST /api/v1/users/:id/reactivate", Tightens: clInteractive},
	{Route: "DELETE /api/core/v1/users/{id}", Want: offDeny, Code: "interactive_required", Clause: clInteractive},
	{Route: "POST /api/core/v1/users/{id}/reset-password", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "A", HeadRoute: "POST /api/v1/users/:id/reset-password", Tightens: clInteractive},
	{Route: "PUT /api/core/v1/users/{id}/roles", Want: offDeny, Code: "interactive_required", Clause: clInteractive},
	{Route: "POST /api/core/v1/users/{id}/temp-login", Want: offDeny, Code: "interactive_required", Clause: clInteractive, Head: "A", HeadRoute: "POST /api/v1/auth/temp-tokens", Tightens: clInteractive},
	{Route: "GET /api/core/v1/users/{id}/sessions", Want: offDeny, Code: "authentication_required", Clause: clNeedUser},
	{Route: "DELETE /api/core/v1/users/{id}/sessions/{publicID}", Want: offDeny, Code: "interactive_required", Clause: clInteractive},

	// ---- roles ------------------------------------------------------------------
	{Route: "GET /api/core/v1/roles", Want: offAllow, Clause: clView},
	{Route: "GET /api/core/v1/roles/{name}", Want: offAllow, Clause: clView},
	{Route: "POST /api/core/v1/roles", Want: offDeny, Code: "interactive_required", Clause: clInteractive},
	{Route: "PATCH /api/core/v1/roles/{name}", Want: offDeny, Code: "interactive_required", Clause: clInteractive},
	{Route: "DELETE /api/core/v1/roles/{name}", Want: offDeny, Code: "interactive_required", Clause: clInteractive},
	{Route: "POST /api/core/v1/roles/{name}/accept-seed", Want: offDeny, Code: "interactive_required", Clause: clInteractive},

	// ---- audit ------------------------------------------------------------------
	{Route: "GET /api/core/v1/audit", Want: offDeny, Code: "authentication_required", Clause: clAudit},
	{Route: "GET /api/core/v1/audit/export", Want: offDeny, Code: "authentication_required", Clause: clAudit},

	// ---- settings ---------------------------------------------------------------
	{Route: "GET /api/core/v1/settings/schema", Want: offAllow, Clause: clSettings},
	{Route: "GET /api/core/v1/settings", Want: offAllow, Clause: clSettings, Head: "A", HeadRoute: "GET /api/v1/config"},
	{Route: "PATCH /api/core/v1/settings", Body: `{}`, Want: offAllow, Clause: clSettings, Head: "A", HeadRoute: "PUT /api/v1/config"},
	// The keys the auth-disabled principal must never change: owner-protected
	// core.auth.mode, admin-protected core.auth.disabled_allow_remote. The
	// handler refuses each, which is the clause's whole content, so the route's
	// own probe above shows the route open and these show the keys closed.
	{Route: "PATCH /api/core/v1/settings", Name: "core.auth.mode, protect owner", Body: `{"set":{"core.auth.mode":"\"required\""}}`,
		Want: offDeny, Code: "protection_required", Clause: clProtect},
	{Route: "PATCH /api/core/v1/settings", Name: "core.auth.disabled_allow_remote, protect admin", Body: `{"set":{"core.auth.disabled_allow_remote":false}}`,
		Want: offDeny, Code: "protection_required", Clause: clProtect},

	// ---- the store reset --------------------------------------------------------
	{Route: "POST /api/core/v1/system/factory-reset", Want: offDeny, Code: "interactive_required", Clause: clReset, Head: "D", HeadRoute: "POST /api/v1/system/factory-reset"},
}

// offControlRows are routes of the fixture module "alfred", one per guard and
// per scope class, so each stage-13 refusal has a cell no handler repeats: the
// core packages re-check Interactive, user and ownership themselves, so a stage
// 13 fault shows on core routes as a changed code at most, never a changed
// letter, and no mounted core route carries Guards.Session (the diagnostics
// bundle download, 05-health 5H.5, is not mounted at this base).
var offControlRows = []offRow{
	{Route: "GET /api/alfred/v1/relay", Want: offAllow,
		Clause: "05-rbac 4.11 bullet 1: a module scope that is neither AuthRequired nor Sensitive is held by the auth-disabled checker"},
	{Route: "POST /api/alfred/v1/relay-lock", Want: offDeny, Code: "interactive_required", Clause: clInteractive},
	{Route: "POST /api/alfred/v1/relay-session", Want: offDeny, Code: "interactive_required",
		Clause: "05-authn 4.11 bullet 2 and httpapi.Guards.Session: only the user's own interactive sign-in (Method.Interactive) passes; the auth-disabled principal is neither interactive nor a user"},
	{Route: "POST /api/alfred/v1/relay-owner", Want: offDeny, Code: "interactive_required",
		Clause: "05-authn 4.8 and 4.11: Guards.Owner without an OwnerVerifier falls back to Interactive plus core:admin, which the auth-disabled principal is not"},
	{Route: "GET /api/alfred/v1/vault", Want: offDeny, Code: "authentication_required",
		Clause: "05-rbac 4.11 bullet 1: a route whose only scope is AuthRequired is answered 401 with auth disabled"},
	{Route: "GET /api/alfred/v1/vault-any", Want: offDeny, Code: "authentication_required",
		Clause: "05-rbac 4.11 bullet 1 and 6.1: the auth-disabled checker never holds an AuthRequired scope, also when the requirement is an AnyOf, which the AllOf drop rule does not reach"},
}

// The probe bodies the table names.
const (
	offKeyCreateBody = `{"name":"batarang","scopes":["alfred:signal"]}`
	offKeyStatusBody = `{"status":"disabled"}`
	// offInviteLookupBody is replaced by the fixture with the real invite
	// token at probe time.
	offInviteLookupBody = `{"token":"@invite-token@"}`
)
