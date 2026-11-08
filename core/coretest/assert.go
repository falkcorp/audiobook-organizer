// file: core/coretest/assert.go
// version: 1.0.0
// guid: 59cf8223-d373-45d6-8bc7-8a8068d0d1ed
// last-edited: 2026-11-07

package coretest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// AssertPermission checks that path answers 403 for a user lacking perm and
// does not answer 401/403 for a user holding exactly perm (R6 per route).
// Core authentication lands at R2's identity flip (F4-08 fills this body);
// until then it fails the test, because R1's router authorises no native
// route from a principal (every non-Public native route is refused at Build).
func AssertPermission(t testing.TB, h *Harness, method, path string, perm rbac.Permission) {
	t.Helper()
	t.Fatalf("coretest: AssertPermission needs core authentication, which lands at R2's identity flip (%s %s, %s)",
		method, path, perm)
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
//     under its Manifest.LegacyPrefixes.
//
// It never calls the module's code: when the first three cells find the
// module live, it sends no request, because its handlers would run. Its
// native routes are not enumerated (that would mean calling Register); a
// test that knows them probes them too, as TestDisabledModuleInert probes
// hello's greeting.
//
// The spec's other cells need subsystems R1 does not have, and the rows that
// land them add them here: task kinds and schedules, held runs and WithRuns
// (F2-16), GET /api/core/v1/modules, RecordingFS, the outbox backlog, the
// op-ledger gate (COR-13) and an unchanged keyspace (storage, R1-20).
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
