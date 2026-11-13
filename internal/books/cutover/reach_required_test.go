// file: internal/books/cutover/reach_required_test.go
// version: 1.1.0
// guid: 91d4b7e3-2c58-4a06-b3f9-6e1a8d0c5b27
// last-edited: 2026-11-13

package cutover

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// The assertions 05-rbac section 12 and the brief make beyond "after equals R0
// except the listed rows": each is checked on its own, so it fails under its own
// name when the diff is clean for another reason.

func reachIndex(t *testing.T, vd *reachR0Doc, name string) int {
	t.Helper()
	i := slices.Index(vd.Principals, name)
	if i < 0 {
		t.Fatalf("no recorded principal %q", name)
	}
	return i
}

// Viewer sessions and every key whose compiled checker holds a non-Confining
// scope keep their playlist writes (SEC-4): R0 let them, and so must R2. A key
// whose checker is empty (an fp-worker user's key scoped outside the worker
// scope, 05-rbac 6.3) holds none, and its loss is a listed expected difference.
func TestReachOracle_ViewerSessionsAndKeysWithANonConfiningScopeKeepPlaylistWrites(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	_, whos, r2 := reachRun(t, pd, vd, atoms, atoms)
	var routes []string
	for _, lr := range reachManifest() {
		if lr.Method != "GET" && strings.HasPrefix(lr.GinPath, "/api/v1/playlists") {
			routes = append(routes, lr.Method+" "+lr.GinPath)
		}
	}
	if len(routes) < 7 {
		t.Fatalf("found %d playlist write routes in the manifest, want at least 7: %v", len(routes), routes)
	}
	r0 := map[string]string{}
	for _, r := range vd.Routes {
		r0[r.key()] = r.Verdict
	}
	checked := 0
	for i, w := range whos {
		isKey := strings.HasPrefix(w.name, "key:")
		if w.name != "session:viewer" && !(isKey && w.chk != nil && !w.chk.confined && len(w.chk.set) > 0) {
			continue
		}
		for _, k := range routes {
			if r0[k][i] != 'A' {
				continue // R0 did not let this principal in: nothing to keep
			}
			checked++
			if r2.verdict[k][i] != 'A' {
				t.Errorf("%s lost %s: R0 A, R2 %c", w.name, k, r2.verdict[k][i])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no cell checked: the playlist routes or the principals changed shape")
	}
	if i := reachIndex(t, vd, "session:viewer"); r0[routes[0]][i] != 'A' {
		t.Errorf("R0 data: the viewer session no longer writes playlists (%s)", routes[0])
	}
}

// An every-atom or empty-list key of an admin still approves every run it
// approved on R0 (05-rbac 6.3): the cutover gives it core:admin plus every other
// row's scopes. An every-atom key of an editor gets the intersection: no admin
// flag, and exactly its owner's role set.
func TestReachOracle_AdminKeysStillApproveAndEditorKeysGetTheIntersection(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, whos, r2 := reachRun(t, pd, vd, atoms, atoms)
	if len(vd.Approvals) == 0 {
		t.Fatal("the recording holds no awaiting run to approve")
	}
	byName := map[string]reachWho{}
	for _, w := range whos {
		byName[w.name] = w
	}
	for _, name := range []string{"key:admin:every", "key:admin:empty"} {
		i := reachIndex(t, vd, name)
		approved := 0
		for _, a := range vd.Approvals {
			if a.Verdict[i] != 'A' {
				continue
			}
			approved++
			def, _ := a.Run["def_id"].(string)
			if got := r2.verdict[reachTaskApproveMeth+" "+def][i]; got != 'A' {
				t.Errorf("%s approved a run of %s on R0 and not on R2", name, def)
			}
		}
		if approved == 0 {
			t.Errorf("%s approved nothing on R0: the data no longer shows the claim", name)
		}
		if w := byName[name]; w.chk == nil || !w.chk.admin {
			t.Errorf("%s: the cutover left no core:admin on an admin-owned every-atom key", name)
		}
	}
	for _, c := range res.cells {
		if strings.HasPrefix(c.route, reachTaskApproveMeth+" ") && c.r0 == 'A' && c.r2 == 'D' {
			t.Errorf("%s lost an approval: %s", c.principal, c.route)
		}
	}
	editorKey, editor := byName["key:editor:every"], byName["session:editor"]
	if editorKey.chk == nil || editor.chk == nil {
		t.Fatal("key:editor:every or session:editor missing from the principals")
	}
	if editorKey.chk.admin {
		t.Error("an editor-owned every-atom key carries the admin flag")
	}
	if _, ok := editorKey.chk.set[rbac.CoreAdmin]; ok {
		t.Error("an editor-owned every-atom key holds core:admin")
	}
	// The key's scopes are the union of the atom rows' scopes plus the ambient
	// ones, intersected with the owner's role set: what the role holds outside
	// that union (core:system.status) the key does not hold.
	union := map[rbac.Permission]struct{}{}
	for _, a := range atoms {
		for _, sc := range a.Scopes {
			union[sc] = struct{}{}
		}
	}
	for _, sc := range reachAmbient() {
		union[sc] = struct{}{}
	}
	want := map[rbac.Permission]struct{}{}
	for sc := range editor.chk.set {
		if _, ok := union[sc]; ok {
			want[sc] = struct{}{}
		}
	}
	if !permSetsEqual(editorKey.chk.set, want) {
		t.Errorf("an editor-owned every-atom key holds %v, the editor's role set within the rows' union is %v", sortedPerms(editorKey.chk.set), sortedPerms(want))
	}
	if _, ok := editor.chk.set["core:system.status"]; !ok {
		t.Error("the editor session no longer holds core:system.status: the intersection check above has lost its witness")
	}
}

func permSetsEqual(a, b map[rbac.Permission]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func sortedPerms(m map[rbac.Permission]struct{}) []rbac.Permission {
	out := make([]rbac.Permission, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
