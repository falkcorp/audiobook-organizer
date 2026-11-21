// file: core/coretest/assert.go
// version: 1.0.1
// guid: 59cf8223-d373-45d6-8bc7-8a8068d0d1ed
// last-edited: 2026-11-11

package coretest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// AssertPermission checks R6 for one route: path answers 403 to a user
// lacking perm and neither 401 nor 403 to a user holding exactly perm.
//
// Both users are ephemeral, seeded in the harness through core auth like
// WithUser's: the lacking user is an authenticated, active user with no role
// at all, and the holding user's one role holds perm and nothing else. Each
// signs in interactively, so a route that carries an Interactive guard is
// judged on its permission and not refused for the credential's kind. A 401
// from the lacking user is a failure, not a pass: it means the credential did
// not authenticate, so the 403 the check wants was never tested. method and
// path are sent as given, with no body; a route whose handler needs input
// answers 4xx other than 401 and 403 to the holding user, which passes.
func AssertPermission(t testing.TB, h *Harness, method, path string, perm rbac.Permission) {
	t.Helper()
	if h.id == nil {
		t.Fatalf("coretest: AssertPermission needs the identity boot, which the harness declares when a module declares storage; none does (%s %s, %s)", method, path, perm)
		return
	}
	lacking := h.ephemeral(t, lackingName)
	holding := h.ephemeral(t, holdingName(perm), perm)
	status := func(c *Client) int {
		req, err := http.NewRequestWithContext(t.Context(), method, path, nil)
		if err != nil {
			t.Fatalf("coretest: AssertPermission: %s %s: %v", method, path, err)
			return 0
		}
		resp := c.Do(req)
		_ = resp.Body.Close() // an in-memory recording
		return resp.StatusCode
	}
	switch got := status(lacking); got {
	case http.StatusForbidden:
	case http.StatusUnauthorized:
		t.Errorf("coretest: AssertPermission: %s %s answered 401 to a signed-in user lacking %s, want 403 (the credential did not authenticate)", method, path, perm)
	default:
		t.Errorf("coretest: AssertPermission: %s %s answered %d to a user lacking %s, want 403", method, path, got, perm)
	}
	switch got := status(holding); got {
	case http.StatusUnauthorized, http.StatusForbidden:
		t.Errorf("coretest: AssertPermission: %s %s answered %d to a user holding exactly %s, want neither 401 nor 403", method, path, got, perm)
	}
}

// AssertRoutesHavePermissions walks the built router (R6, archtest twin): it
// runs archtest.RoutesHavePermissions over App.Routes().
func AssertRoutesHavePermissions(t testing.TB, h *Harness) {
	t.Helper()
	archtest.RoutesHavePermissions(t, h.app.Routes())
}

// AssertModuleInert proves gate G1 for a disabled module, over the cells that
// exist at release R1:
//
//   - the App reports the module disabled;
//   - the kernel never called its Register, so no hook or worker of it was
//     recorded, let alone run (06 §6.3 A5);
//   - no route of the built router is the module's;
//   - no request answers anything but 404 (not 403) under /api/<id>/ or
//     under its Manifest.LegacyPrefixes;
//   - its persisted runs, every kind of its TaskLedger, are listed as held
//     and none was dispatched (F2-16; the cell is empty without runs).
//
// It never calls the module's code: when the first three cells find the
// module live, it sends no request, because its handlers would run. Its
// native routes are not enumerated (that would mean calling Register); a
// test that knows them probes them too, as TestDisabledModuleInert probes
// hello's greeting.
//
// The spec's other cells need subsystems R1 does not have, and the rows that
// land them add them here: task schedules, GET /api/core/v1/modules,
// RecordingFS, the outbox backlog, the op-ledger gate (COR-13: HEAD's op
// ledger has no F2 equivalent) and an unchanged keyspace (storage, R1-20).
// The held runs are listed through the Client, not GET /api/core/v1/tasks/runs,
// which is not mounted yet (F2-16's notes).
func AssertModuleInert(t testing.TB, h *Harness, id module.ID) {
	t.Helper()
	m, ok := h.mods[id]
	if !ok {
		t.Fatalf("coretest: AssertModuleInert(%q): no module with that ID was passed to WithModules", id)
		return
	}
	live := false
	for _, s := range h.app.Modules() {
		if s.ID == id && s.Enabled {
			t.Errorf("coretest: AssertModuleInert(%q): the module is enabled; G1 holds for a disabled module", id)
			live = true
		}
	}
	if w := h.watched[id]; w != nil && w.registered.Load() {
		t.Errorf("coretest: AssertModuleInert(%q): Register ran; a disabled module's Register, and every hook and worker it records, is never invoked (06 §6.3 A5)", id)
		live = true
	}
	for ri := range h.app.Routes() {
		if ri.Owner == string(id) {
			t.Errorf("coretest: AssertModuleInert(%q): route %s %s (%s) is registered for the module",
				id, ri.Method, ri.Pattern, ri.Name)
			live = true
		}
	}
	if live {
		t.Errorf("coretest: AssertModuleInert(%q): the module is live, so no request was sent to its paths", id)
		return
	}
	assertHeldRuns(t, h, id, m.Manifest())
	client := h.Anonymous()
	for _, p := range inertProbes(id, m.Manifest()) {
		req, err := http.NewRequestWithContext(t.Context(), p.method, p.path, nil)
		if err != nil {
			t.Errorf("coretest: AssertModuleInert(%q): %s %s: %v", id, p.method, p.path, err)
			continue
		}
		resp := client.Do(req)
		// The body is an in-memory recording: closing it releases nothing,
		// but a response is closed wherever it is taken.
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("coretest: AssertModuleInert(%q): %s %s answered %d, want 404 (G1: no route of a disabled module answers; 404, not 403)",
				id, p.method, p.path, resp.StatusCode)
		}
	}
}

// probe is one request AssertModuleInert sends.
type probe struct{ method, path string }

// inertProbes is every request AssertModuleInert sends for module id: GET and
// POST on its API root and its v1 base, and on each legacy prefix (and one
// path below a subtree prefix).
func inertProbes(id module.ID, man module.Manifest) []probe {
	var out []probe
	both := func(path string) {
		out = append(out, probe{http.MethodGet, path}, probe{http.MethodPost, path})
	}
	root := "/api/" + string(id) + "/"
	both(root)
	both(root + "v1/")
	for _, p := range man.LegacyPrefixes {
		both(p)
		if strings.HasSuffix(p, "/") {
			both(p + "x")
		}
	}
	return out
}
