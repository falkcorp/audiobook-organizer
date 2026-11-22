// file: internal/books/cutover/registered_harness_verdicts_test.go
// version: 0.1.0
// guid: c41e7a96-3b58-4d02-9f71-e8a05d6b2c13
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The route-by-principal sweep (F4-08 B10d): every route of verdicts.json against
// every principal, on the registered path, compared with what R0 recorded. A cell
// where the answers differ is one of four things, and verdictRows names each:
//
//	classDrift   current HEAD (origin/main) answers as the flipped build does, not as
//	             R0 did: a commit after R0 (v0.223.0) changed the route. Verified from
//	             the route registration and its permission, see
//	             TestRegisteredHarness_R0DriftIsInTheRouteTable.
//	classKnown   an accepted narrowing, with the ruling that accepted it.
//	classB10y    refused by the flipped build where R0 and HEAD admit; not accepted. The
//	             parity assertion for these is in the last commits of the stack (red
//	             until slice B10y lands).
//	classRuling  a narrowing nobody has ruled on yet; the same, in its own commit.
//	classTightening  the auth-off tightening (registeredAuthOffTightening): R0 admitted
//	             the auth-off principal on an Interactive route, the flipped build does
//	             not, deliberately (SEC5-8). Not a verdictRows entry: its own table, held
//	             to the exact refusal code.
//
// TestRegisteredHarness_EveryVerdictIsR0sOrNamed fails on a mismatch no row names and
// on a row that no longer mismatches, so the tables cannot go stale in either
// direction: a fix that makes a cell match R0 again must delete its row.
//
// A core-native key (corekey:...) is held to its abk_ twin's verdict: it mismatches
// exactly where its twin does, and the row lists the twin only.

type verdictClass int

const (
	classDrift verdictClass = iota
	classKnown
	classB10y
	classRuling
	classTightening
)

type verdictRow struct {
	Method, Route string
	Class         verdictClass
	// Principals are the verdicts.json names whose cell mismatches R0's.
	Principals []string
	Why        string
}

var verdictRows = []verdictRow{
	{Method: "POST", Route: "/api/v1/operations/reconcile", Class: classDrift, Principals: []string{"key:admin:settings", "key:editor:empty", "key:editor:every", "session:editor"},
		Why: "R0 drift: ed8d80f4b (2026-11-18) moved POST /operations/reconcile from scan.trigger to settings.manage (TestReconcileApplyRoute_NeedsSettingsManage)"},
	{Method: "POST", Route: "/api/v1/system/factory-reset", Class: classDrift, Principals: []string{"anonymous", "authoff", "key:admin:empty", "key:admin:every", "key:admin:settings", "key:admin:users", "key:admin:view", "key:admin:worker", "key:custom-all:empty", "key:custom-all:every", "key:custom-subset:empty", "key:custom-subset:outside", "key:editor:empty", "key:editor:every", "key:editor:settings", "key:editor:users", "key:editor:view", "key:editor:worker", "key:fp-worker:empty", "key:fp-worker:every", "key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view", "key:fp-worker:worker", "key:viewer:empty", "key:viewer:every", "key:viewer:settings", "key:viewer:users", "key:viewer:view", "key:viewer:worker", "session:custom-all", "session:custom-subset", "session:custom-unknown", "session:editor", "session:fp-worker", "session:viewer"},
		Why: "R0 drift: removed on main (wire_system_routes.go), the reset runs only offline; no route answers, so there is no authorization step to refuse"},
	{Method: "POST", Route: "/api/v1/system/reset", Class: classDrift, Principals: []string{"anonymous", "authoff", "key:admin:empty", "key:admin:every", "key:admin:settings", "key:admin:users", "key:admin:view", "key:admin:worker", "key:custom-all:empty", "key:custom-all:every", "key:custom-subset:empty", "key:custom-subset:outside", "key:editor:empty", "key:editor:every", "key:editor:settings", "key:editor:users", "key:editor:view", "key:editor:worker", "key:fp-worker:empty", "key:fp-worker:every", "key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view", "key:fp-worker:worker", "key:viewer:empty", "key:viewer:every", "key:viewer:settings", "key:viewer:users", "key:viewer:view", "key:viewer:worker", "session:custom-all", "session:custom-subset", "session:custom-unknown", "session:editor", "session:fp-worker", "session:viewer"},
		Why: "R0 drift: removed on main (wire_system_routes.go), the reset runs only offline; no route answers, so there is no authorization step to refuse"},
	{Method: "DELETE", Route: "/api/v1/audiobooks/:id/status", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "DELETE", Route: "/api/v1/auth/api-keys/:id", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "DELETE", Route: "/api/v1/auth/sessions/:id", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "DELETE", Route: "/api/v1/books/:id/status", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "DELETE", Route: "/api/v1/playlists/:id", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "DELETE", Route: "/api/v1/playlists/:id/books/:bookID", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/events", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/audiobooks/:id/position", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/audiobooks/:id/state", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/auth/api-keys", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/auth/api-keys/:id", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/auth/me", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/auth/sessions", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/books/:id/position", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/books/:id/state", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/me/:status", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/playlists/:id", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/api/v1/playlists/:id/export.m3u", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "GET", Route: "/metrics", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "PATCH", Route: "/api/v1/audiobooks/:id/status", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "PATCH", Route: "/api/v1/books/:id/status", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/audiobooks/:id/position", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/audiobooks/:id/status/repair", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/auth/bootstrap", Class: classKnown, Principals: []string{"anonymous", "authoff", "key:admin:empty", "key:admin:every", "key:admin:settings", "key:admin:users", "key:admin:view", "key:admin:worker", "key:custom-all:empty", "key:custom-all:every", "key:custom-subset:empty", "key:custom-subset:outside", "key:editor:empty", "key:editor:every", "key:editor:settings", "key:editor:users", "key:editor:view", "key:editor:worker", "key:fp-worker:empty", "key:fp-worker:every", "key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view", "key:fp-worker:worker", "key:viewer:empty", "key:viewer:every", "key:viewer:settings", "key:viewer:users", "key:viewer:view", "key:viewer:worker", "session:admin", "session:custom-all", "session:custom-subset", "session:custom-unknown", "session:editor", "session:fp-worker", "session:viewer"},
		Why: "accepted 11-22 06:29 (coordinator, bootstrap ruling option 2): under identity the books route refuses every caller up front, 401 invalid bootstrap token, before it reads the body (R0's 400 was the empty probe body); the token is redeemed at core's /api/core/v1/auth/bootstrap and the cutover deletes HEAD's token rows"},
	{Method: "POST", Route: "/api/v1/auth/logout", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/books/:id/position", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/books/:id/status/repair", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/playlists", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/playlists/:id/books", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/playlists/:id/materialize", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "POST", Route: "/api/v1/playlists/:id/reorder", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "PUT", Route: "/api/v1/playlists/:id", Class: classKnown, Principals: []string{"key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view"},
		Why: "accepted 11-22 (coordinator): a role-Confined fp-worker key gets no ambient scopes (core:self, core:metrics.read, books:reading) and the route needs one"},
	{Method: "DELETE", Route: "/api/v1/master-lists/:id", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/admin/debug/book-files/:id", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/admin/debug/books/:id", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/admin/debug/edits", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/admin/debug/lookup", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/cache/stats/keys", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/deluge/discover", Class: classB10y, Principals: []string{"authoff", "key:admin:empty", "key:admin:every", "key:custom-all:empty", "key:custom-all:every", "session:admin", "session:custom-all"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/master-lists", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/master-lists/elsewhere", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/users/:id/footprint", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "GET", Route: "/api/v1/users/merges", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "PATCH", Route: "/api/v1/admin/debug/book-files/:id", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "PATCH", Route: "/api/v1/admin/debug/books/:id", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "POST", Route: "/api/v1/admin/debug/edits/:edit_id/undo", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "POST", Route: "/api/v1/admin/recompact-digests", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "POST", Route: "/api/v1/cache/invalidate", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "POST", Route: "/api/v1/deluge/discover/import", Class: classB10y, Principals: []string{"authoff", "key:admin:empty", "key:admin:every", "key:custom-all:empty", "key:custom-all:every", "session:admin", "session:custom-all"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "POST", Route: "/api/v1/maintenance/wipe", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "POST", Route: "/api/v1/master-lists", Class: classB10y, Principals: []string{"key:admin:empty", "key:admin:every"},
		Why: "key:admin:{every,empty} (and the deluge cells): refused where R0 admitted; slice B10y"},
	{Method: "DELETE", Route: "/api/v1/users/invites/:id", Class: classB10y, Principals: []string{"authoff"},
		Why: "auth-off: refused where R0 admitted and it is not the SEC5-8 tightening (the route is not Interactive-guarded); a fix, slice B10y"},
	{Method: "GET", Route: "/api/v1/users/invites", Class: classB10y, Principals: []string{"authoff"},
		Why: "auth-off: refused where R0 admitted and it is not the SEC5-8 tightening (the route is not Interactive-guarded); a fix, slice B10y"},
	{Method: "DELETE", Route: "/api/v1/users/invites/:id", Class: classRuling, Principals: []string{"key:admin:empty", "key:admin:every", "key:admin:users", "key:custom-all:empty", "key:custom-all:every"},
		Why: "narrowing not yet ruled on"},
	{Method: "GET", Route: "/api/v1/users/invites", Class: classRuling, Principals: []string{"key:admin:empty", "key:admin:every", "key:admin:users", "key:custom-all:empty", "key:custom-all:every"},
		Why: "narrowing not yet ruled on"},
	{Method: "POST", Route: "/api/v1/auth/api-keys", Class: classRuling, Principals: []string{"key:admin:empty", "key:admin:every", "key:admin:settings", "key:admin:users", "key:admin:view", "key:custom-all:empty", "key:custom-all:every", "key:custom-subset:empty", "key:custom-subset:outside", "key:editor:empty", "key:editor:every", "key:editor:settings", "key:editor:users", "key:editor:view", "key:editor:worker", "key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:view", "key:viewer:empty", "key:viewer:every", "key:viewer:settings", "key:viewer:users", "key:viewer:view", "key:viewer:worker"},
		Why: "narrowing not yet ruled on"},
	{Method: "POST", Route: "/api/v1/backup/restore", Class: classRuling, Principals: []string{"session:custom-all"},
		Why: "narrowing not yet ruled on"},
}

// authOffTightening is a route that R0 recorded as open to the auth-off principal and
// the flipped build refuses it: the deliberate auth-off tightening of SEC5-8 (05-rbac
// §4.11(a), 05-authn §4.11, R2's release note 09-abo-on-core.md §9.2.6). HEAD's
// credential guard is a pass-through with auth off, and the auth-disabled principal is
// non-interactive, so a route whose guard is Interactive refuses it. These are expected
// differences, not defects; each cell is held to its exact answer by
// TestRegisteredHarness_AuthOffTighteningRefusesInteractiveRoutes.
type authOffTightening struct {
	Method, Route string
	// Code is the refusal's code: interactive_required from stage 13, or FORBIDDEN where
	// the handler itself refuses.
	Code string
}

// authOffTightenings flattens the oracle's registeredAuthOffTightening table
// (registered_reach_eval_test.go: the one source of the 12 cells, each row with its
// citation) into the route-and-code pairs the sweep and the exact-refusal test read.
func authOffTightenings() []authOffTightening {
	var out []authOffTightening
	for _, row := range registeredAuthOffTightening.rows {
		for _, r := range row.routes {
			method, route, _ := strings.Cut(r, " ")
			out = append(out, authOffTightening{Method: method, Route: route, Code: row.code})
		}
	}
	return out
}

// recordedRoute is one row of verdicts.json.
type recordedRoute struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	ProbePath string `json:"probe_path"`
	Verdict   string `json:"verdict"`
}

// recordedVerdicts reads R0's recorded routes and the principal order of their
// verdict strings.
func recordedVerdicts(t testing.TB) (principals []string, routes []recordedRoute) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), reachRel, "verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vf struct {
		Principals []string        `json:"principals"`
		Routes     []recordedRoute `json:"routes"`
	}
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	return vf.Principals, vf.Routes
}

// sweepCells runs every recorded route as every principal of the boot that has a
// recorded name (a core key under its twin's) and calls cell with the answer and R0's
// letter. The session-ending routes go last: logging out is what they do.
func sweepCells(t *testing.T, h *Registered, only func(*Principal) bool, cell func(r recordedRoute, p *Principal, res Result, r0 byte)) {
	t.Helper()
	principals, routes := recordedVerdicts(t)
	ends := func(path string) bool {
		return strings.Contains(path, "logout") || strings.Contains(path, "password") || strings.Contains(path, "sessions")
	}
	for pass := 0; pass < 2; pass++ {
		for _, r := range routes {
			if ends(r.Path) != (pass == 1) {
				continue
			}
			for _, p := range h.Principals {
				i := slices.Index(principals, p.Recorded())
				if i < 0 || !only(p) {
					continue
				}
				res, err := h.Do(p, r.Method, r.ProbePath, nil)
				if err != nil {
					// The ABS surface (OwnAuth) is not a stage-12/13 route; its cells
					// are in the ABS tests.
					if errors.Is(err, ErrOwnAuth) {
						continue
					}
					t.Fatalf("%s %s as %s: %v", r.Method, r.Path, p.Name, err)
				}
				cell(r, p, res, r.Verdict[i])
			}
		}
	}
}

// sweepMismatches lists the cells of a boot whose verdict is not R0's, keyed
// "METHOD route|principal" with a core key under its own name.
func sweepMismatches(t *testing.T, h *Registered, only func(*Principal) bool) map[string]Result {
	t.Helper()
	got := map[string]Result{}
	sweepCells(t, h, only, func(r recordedRoute, p *Principal, res Result, r0 byte) {
		if res.Verdict() != r0 {
			got[r.Method+" "+r.Path+"|"+p.Name] = res
		}
	})
	return got
}

// expected expands verdictRows into the cells they name, keyed like
// sweepMismatches: each abk_ principal with its core twin where it has one. The
// auth-off tightening's cells are included when classTightening is asked for.
func expected(h *Registered, classes ...verdictClass) map[string]*verdictRow {
	out := map[string]*verdictRow{}
	if slices.Contains(classes, classTightening) {
		for _, tt := range authOffTightenings() {
			out[tt.Method+" "+tt.Route+"|"+Authoff] = &verdictRow{Method: tt.Method, Route: tt.Route, Class: classTightening, Principals: []string{Authoff}, Why: "auth-off tightening (SEC5-8)"}
		}
	}
	for i := range verdictRows {
		row := &verdictRows[i]
		if !slices.Contains(classes, row.Class) {
			continue
		}
		for _, name := range row.Principals {
			out[row.Method+" "+row.Route+"|"+name] = row
			if twin := coreKeyName(name); h.byName[twin] != nil {
				out[row.Method+" "+row.Route+"|"+twin] = row
			}
		}
	}
	return out
}

// TestRegisteredHarness_EveryVerdictIsR0sOrNamed is the sweep: on the auth-on and the
// auth-off boot, every cell's verdict is R0's, or verdictRows names it, and every cell
// verdictRows names does mismatch. The route table is the registered one, so a route
// added or re-permissioned after R0 shows here as an unexplained cell.
func TestRegisteredHarness_EveryVerdictIsR0sOrNamed(t *testing.T) {
	all := []verdictClass{classDrift, classKnown, classB10y, classRuling, classTightening}
	check := func(t *testing.T, h *Registered, only func(*Principal) bool) {
		want := expected(h, all...)
		got := sweepMismatches(t, h, only)
		for cell, res := range got {
			if want[cell] == nil {
				t.Errorf("%s: answers differently from R0 and no verdictRows entry names it (%d %s)", cell, res.Status, res.Code)
			}
		}
		for cell, row := range want {
			if !only(h.byName[cell[strings.LastIndex(cell, "|")+1:]]) {
				continue
			}
			if _, ok := got[cell]; !ok {
				t.Errorf("%s: verdictRows names it (%s) but it answers as R0 did: delete it from the row", cell, row.Why)
			}
		}
	}
	t.Run("auth on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		check(t, h, func(p *Principal) bool { return p != nil && p.Kind != "authoff" })
	})
	t.Run("auth off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		check(t, h, func(p *Principal) bool { return p != nil && p.Kind == "authoff" })
	})
}

// TestRegisteredHarness_R0DriftIsInTheRouteTable ties the classDrift rows to the
// code: the two facts they rest on are read from the routes the flipped build
// registers. R0's own recording is v0.223.0; the drift commits are after it.
func TestRegisteredHarness_R0DriftIsInTheRouteTable(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	ri := h.match(http.MethodPost, "/api/v1/operations/reconcile")
	if ri == nil || !strings.Contains(ri.Perm.String(), "settings.manage") {
		t.Errorf("POST /operations/reconcile: %+v, want a settings.manage permission (ed8d80f4b)", ri)
	}
	for _, route := range []string{"/api/v1/system/reset", "/api/v1/system/factory-reset"} {
		if ri := h.match(http.MethodPost, route); ri != nil {
			t.Errorf("POST %s is registered (%s): the reset runs only offline since it was removed from wire_system_routes.go", route, ri.Pattern)
		}
	}
	for _, row := range verdictRows {
		if row.Class == classDrift && !strings.HasPrefix(row.Why, "R0 drift:") {
			t.Errorf("%s %s: a drift row must say what changed", row.Method, row.Route)
		}
	}
}

// TestRegisteredHarness_AuthOffTighteningRefusesInteractiveRoutes holds each cell of
// registeredAuthOffTightening to its exact refusal on the auth-off boot (the sweep
// shows only that the verdict differs from R0's). That an operator can turn auth on and
// then reach these routes is TestRegisteredHarness_AuthOnByTheOperatorOverAStoredOff.
func TestRegisteredHarness_AuthOffTighteningRefusesInteractiveRoutes(t *testing.T) {
	off := bootRegistered(t, registeredConfig{AuthOff: true})
	cells := authOffTightenings()
	if len(cells) != registeredAuthOffTightening.want {
		t.Fatalf("the tightening table flattens to %d cells, its rows claim %d", len(cells), registeredAuthOffTightening.want)
	}
	for _, tt := range cells {
		ri := off.match(tt.Method, probePathFor(t, tt.Method, tt.Route))
		if ri == nil {
			t.Errorf("%s %s: not registered", tt.Method, tt.Route)
			continue
		}
		res, err := off.Do(off.Principal(Authoff), tt.Method, probePathFor(t, tt.Method, tt.Route), nil)
		if err != nil {
			t.Errorf("%s %s: %v", tt.Method, tt.Route, err)
			continue
		}
		if !off.Refused(res, http.StatusForbidden, tt.Code) {
			t.Errorf("%s %s as auth-off: %d %s, want 403 %s", tt.Method, tt.Route, res.Status, res.excerpt(), tt.Code)
		}
	}
}

// probePathFor is the recorded probe path of a route (its :params filled).
func probePathFor(t testing.TB, method, route string) string {
	t.Helper()
	_, routes := recordedVerdicts(t)
	for _, r := range routes {
		if r.Method == method && r.Path == route {
			return r.ProbePath
		}
	}
	t.Fatalf("%s %s is not a recorded route", method, route)
	return ""
}
