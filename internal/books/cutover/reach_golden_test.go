// file: internal/books/cutover/reach_golden_test.go
// version: 1.0.1
// guid: e41c7a05-92d3-4b68-a1f7-5c08d3b6e294
// last-edited: 2026-11-18

package cutover

import (
	"fmt"
	"slices"
	"sort"
	"testing"
)

// reachGoldenCells is every cell on which R0's recorded answer and R2's differ,
// written out. The expected-difference rows assert how many cells each claims and
// both letters, but three of them are unsaturated (05-rbac 4.11(a) over the
// interactive routes, 6.3 over the authenticated-only routes, the rotate row over
// the key principals), so a swap of equal count between two routes of one row
// would pass the rows. This list makes the claim exact (round-2 NIT-4): the
// oracle's cells are these and no others. Generated from the oracle at the
// recorded R0 data, then read against the rows; a change to it is a decision about
// reach, never an edit to make a gate pass.
var reachGoldenCells = []string{
	"DELETE /api/v1/audiobooks/:id/status / key:fp-worker:settings: R0 A, R2 D",
	"DELETE /api/v1/audiobooks/:id/status / key:fp-worker:users: R0 A, R2 D",
	"DELETE /api/v1/audiobooks/:id/status / key:fp-worker:view: R0 A, R2 D",
	"DELETE /api/v1/auth/api-keys/:id / authoff: R0 D, R2 A",
	"DELETE /api/v1/auth/api-keys/:id / key:fp-worker:settings: R0 A, R2 D",
	"DELETE /api/v1/auth/api-keys/:id / key:fp-worker:users: R0 A, R2 D",
	"DELETE /api/v1/auth/api-keys/:id / key:fp-worker:view: R0 A, R2 D",
	"DELETE /api/v1/auth/sessions/:id / authoff: R0 D, R2 A",
	"DELETE /api/v1/auth/sessions/:id / key:fp-worker:settings: R0 A, R2 D",
	"DELETE /api/v1/auth/sessions/:id / key:fp-worker:users: R0 A, R2 D",
	"DELETE /api/v1/auth/sessions/:id / key:fp-worker:view: R0 A, R2 D",
	"DELETE /api/v1/books/:id/status / key:fp-worker:settings: R0 A, R2 D",
	"DELETE /api/v1/books/:id/status / key:fp-worker:users: R0 A, R2 D",
	"DELETE /api/v1/books/:id/status / key:fp-worker:view: R0 A, R2 D",
	"DELETE /api/v1/playlists/:id / key:fp-worker:settings: R0 A, R2 D",
	"DELETE /api/v1/playlists/:id / key:fp-worker:users: R0 A, R2 D",
	"DELETE /api/v1/playlists/:id / key:fp-worker:view: R0 A, R2 D",
	"DELETE /api/v1/playlists/:id/books/:bookID / key:fp-worker:settings: R0 A, R2 D",
	"DELETE /api/v1/playlists/:id/books/:bookID / key:fp-worker:users: R0 A, R2 D",
	"DELETE /api/v1/playlists/:id/books/:bookID / key:fp-worker:view: R0 A, R2 D",
	"GET /api/events / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/events / key:fp-worker:users: R0 A, R2 D",
	"GET /api/events / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/audiobooks/:id/position / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/audiobooks/:id/position / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/audiobooks/:id/position / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/audiobooks/:id/state / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/audiobooks/:id/state / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/audiobooks/:id/state / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/auth/api-keys / authoff: R0 D, R2 A",
	"GET /api/v1/auth/api-keys / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/auth/api-keys / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/auth/api-keys / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/auth/api-keys/:id / authoff: R0 D, R2 A",
	"GET /api/v1/auth/api-keys/:id / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/auth/api-keys/:id / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/auth/api-keys/:id / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/auth/me / authoff: R0 D, R2 A",
	"GET /api/v1/auth/me / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/auth/me / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/auth/me / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/auth/sessions / authoff: R0 D, R2 A",
	"GET /api/v1/auth/sessions / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/auth/sessions / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/auth/sessions / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/books/:id/position / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/books/:id/position / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/books/:id/position / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/books/:id/state / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/books/:id/state / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/books/:id/state / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/me/:status / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/me/:status / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/me/:status / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/playlists/:id / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/playlists/:id / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/playlists/:id / key:fp-worker:view: R0 A, R2 D",
	"GET /api/v1/playlists/:id/export.m3u / key:fp-worker:settings: R0 A, R2 D",
	"GET /api/v1/playlists/:id/export.m3u / key:fp-worker:users: R0 A, R2 D",
	"GET /api/v1/playlists/:id/export.m3u / key:fp-worker:view: R0 A, R2 D",
	"GET /metrics / key:fp-worker:settings: R0 A, R2 D",
	"GET /metrics / key:fp-worker:users: R0 A, R2 D",
	"GET /metrics / key:fp-worker:view: R0 A, R2 D",
	"PATCH /api/v1/admin/debug/book-files/:id / key:admin:empty: R0 A, R2 D",
	"PATCH /api/v1/admin/debug/book-files/:id / key:admin:every: R0 A, R2 D",
	"PATCH /api/v1/admin/debug/books/:id / key:admin:empty: R0 A, R2 D",
	"PATCH /api/v1/admin/debug/books/:id / key:admin:every: R0 A, R2 D",
	"PATCH /api/v1/audiobooks/:id/status / key:fp-worker:settings: R0 A, R2 D",
	"PATCH /api/v1/audiobooks/:id/status / key:fp-worker:users: R0 A, R2 D",
	"PATCH /api/v1/audiobooks/:id/status / key:fp-worker:view: R0 A, R2 D",
	"PATCH /api/v1/books/:id/status / key:fp-worker:settings: R0 A, R2 D",
	"PATCH /api/v1/books/:id/status / key:fp-worker:users: R0 A, R2 D",
	"PATCH /api/v1/books/:id/status / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/admin/debug/edits/:edit_id/undo / key:admin:empty: R0 A, R2 D",
	"POST /api/v1/admin/debug/edits/:edit_id/undo / key:admin:every: R0 A, R2 D",
	"POST /api/v1/audiobooks/:id/position / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/audiobooks/:id/position / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/audiobooks/:id/position / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/audiobooks/:id/status/repair / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/audiobooks/:id/status/repair / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/audiobooks/:id/status/repair / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/auth/api-keys / authoff: R0 D, R2 A",
	"POST /api/v1/auth/api-keys / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/auth/api-keys / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/auth/api-keys / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/auth/api-keys/:id/rotate / key:admin:empty: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:admin:every: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:admin:settings: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:admin:users: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:admin:view: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:custom-all:empty: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:custom-all:every: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:custom-subset:empty: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:custom-subset:outside: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:editor:empty: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:editor:every: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:editor:settings: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:editor:users: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:editor:view: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:editor:worker: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:viewer:empty: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:viewer:every: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:viewer:settings: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:viewer:users: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:viewer:view: R0 D, R2 A",
	"POST /api/v1/auth/api-keys/:id/rotate / key:viewer:worker: R0 D, R2 A",
	"POST /api/v1/auth/logout / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/auth/logout / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/auth/logout / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/auth/temp-tokens / authoff: R0 A, R2 D",
	"POST /api/v1/backup/restore / authoff: R0 A, R2 D",
	"POST /api/v1/backup/restore / session:custom-all: R0 A, R2 D",
	"POST /api/v1/books/:id/position / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/books/:id/position / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/books/:id/position / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/books/:id/status/repair / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/books/:id/status/repair / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/books/:id/status/repair / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/import-paths / authoff: R0 A, R2 D",
	"POST /api/v1/operations/reconcile / key:admin:settings: R0 D, R2 A",
	"POST /api/v1/operations/reconcile / key:editor:empty: R0 A, R2 D",
	"POST /api/v1/operations/reconcile / key:editor:every: R0 A, R2 D",
	"POST /api/v1/operations/reconcile / session:editor: R0 A, R2 D",
	"POST /api/v1/playlists / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/playlists / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/playlists / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/playlists/:id/books / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/playlists/:id/books / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/playlists/:id/books / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/playlists/:id/materialize / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/playlists/:id/materialize / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/playlists/:id/materialize / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/playlists/:id/reorder / key:fp-worker:settings: R0 A, R2 D",
	"POST /api/v1/playlists/:id/reorder / key:fp-worker:users: R0 A, R2 D",
	"POST /api/v1/playlists/:id/reorder / key:fp-worker:view: R0 A, R2 D",
	"POST /api/v1/tools/:name/install / authoff: R0 A, R2 D",
	"POST /api/v1/update/apply / authoff: R0 A, R2 D",
	"POST /api/v1/users/:id/deactivate / authoff: R0 A, R2 D",
	"POST /api/v1/users/:id/reactivate / authoff: R0 A, R2 D",
	"POST /api/v1/users/:id/reset-password / authoff: R0 A, R2 D",
	"POST /api/v1/users/invite / authoff: R0 A, R2 D",
	"POST /api/v1/users/invites/:id/resend / authoff: R0 A, R2 D",
	"POST /api/v3/ops/runs/:id/approve / authoff: R0 D, R2 A",
	"POST /auth/refresh / anonymous: R0 D, R2 A",
	"POST /auth/refresh / authoff: R0 D, R2 A",
	"POST /auth/refresh / key:admin:empty: R0 D, R2 A",
	"POST /auth/refresh / key:admin:every: R0 D, R2 A",
	"POST /auth/refresh / key:admin:settings: R0 D, R2 A",
	"POST /auth/refresh / key:admin:users: R0 D, R2 A",
	"POST /auth/refresh / key:admin:view: R0 D, R2 A",
	"POST /auth/refresh / key:admin:worker: R0 D, R2 A",
	"POST /auth/refresh / key:custom-all:empty: R0 D, R2 A",
	"POST /auth/refresh / key:custom-all:every: R0 D, R2 A",
	"POST /auth/refresh / key:custom-subset:empty: R0 D, R2 A",
	"POST /auth/refresh / key:custom-subset:outside: R0 D, R2 A",
	"POST /auth/refresh / key:editor:empty: R0 D, R2 A",
	"POST /auth/refresh / key:editor:every: R0 D, R2 A",
	"POST /auth/refresh / key:editor:settings: R0 D, R2 A",
	"POST /auth/refresh / key:editor:users: R0 D, R2 A",
	"POST /auth/refresh / key:editor:view: R0 D, R2 A",
	"POST /auth/refresh / key:editor:worker: R0 D, R2 A",
	"POST /auth/refresh / key:fp-worker:empty: R0 D, R2 A",
	"POST /auth/refresh / key:fp-worker:every: R0 D, R2 A",
	"POST /auth/refresh / key:fp-worker:settings: R0 D, R2 A",
	"POST /auth/refresh / key:fp-worker:users: R0 D, R2 A",
	"POST /auth/refresh / key:fp-worker:view: R0 D, R2 A",
	"POST /auth/refresh / key:fp-worker:worker: R0 D, R2 A",
	"POST /auth/refresh / key:viewer:empty: R0 D, R2 A",
	"POST /auth/refresh / key:viewer:every: R0 D, R2 A",
	"POST /auth/refresh / key:viewer:settings: R0 D, R2 A",
	"POST /auth/refresh / key:viewer:users: R0 D, R2 A",
	"POST /auth/refresh / key:viewer:view: R0 D, R2 A",
	"POST /auth/refresh / key:viewer:worker: R0 D, R2 A",
	"POST /auth/refresh / session:admin: R0 D, R2 A",
	"POST /auth/refresh / session:custom-all: R0 D, R2 A",
	"POST /auth/refresh / session:custom-subset: R0 D, R2 A",
	"POST /auth/refresh / session:custom-unknown: R0 D, R2 A",
	"POST /auth/refresh / session:editor: R0 D, R2 A",
	"POST /auth/refresh / session:fp-worker: R0 D, R2 A",
	"POST /auth/refresh / session:viewer: R0 D, R2 A",
	"POST /login / anonymous: R0 D, R2 A",
	"POST /login / authoff: R0 D, R2 A",
	"POST /login / key:admin:empty: R0 D, R2 A",
	"POST /login / key:admin:every: R0 D, R2 A",
	"POST /login / key:admin:settings: R0 D, R2 A",
	"POST /login / key:admin:users: R0 D, R2 A",
	"POST /login / key:admin:view: R0 D, R2 A",
	"POST /login / key:admin:worker: R0 D, R2 A",
	"POST /login / key:custom-all:empty: R0 D, R2 A",
	"POST /login / key:custom-all:every: R0 D, R2 A",
	"POST /login / key:custom-subset:empty: R0 D, R2 A",
	"POST /login / key:custom-subset:outside: R0 D, R2 A",
	"POST /login / key:editor:empty: R0 D, R2 A",
	"POST /login / key:editor:every: R0 D, R2 A",
	"POST /login / key:editor:settings: R0 D, R2 A",
	"POST /login / key:editor:users: R0 D, R2 A",
	"POST /login / key:editor:view: R0 D, R2 A",
	"POST /login / key:editor:worker: R0 D, R2 A",
	"POST /login / key:fp-worker:empty: R0 D, R2 A",
	"POST /login / key:fp-worker:every: R0 D, R2 A",
	"POST /login / key:fp-worker:settings: R0 D, R2 A",
	"POST /login / key:fp-worker:users: R0 D, R2 A",
	"POST /login / key:fp-worker:view: R0 D, R2 A",
	"POST /login / key:fp-worker:worker: R0 D, R2 A",
	"POST /login / key:viewer:empty: R0 D, R2 A",
	"POST /login / key:viewer:every: R0 D, R2 A",
	"POST /login / key:viewer:settings: R0 D, R2 A",
	"POST /login / key:viewer:users: R0 D, R2 A",
	"POST /login / key:viewer:view: R0 D, R2 A",
	"POST /login / key:viewer:worker: R0 D, R2 A",
	"POST /login / session:admin: R0 D, R2 A",
	"POST /login / session:custom-all: R0 D, R2 A",
	"POST /login / session:custom-subset: R0 D, R2 A",
	"POST /login / session:custom-unknown: R0 D, R2 A",
	"POST /login / session:editor: R0 D, R2 A",
	"POST /login / session:fp-worker: R0 D, R2 A",
	"POST /login / session:viewer: R0 D, R2 A",
	"PUT /api/v1/playlists/:id / key:fp-worker:settings: R0 A, R2 D",
	"PUT /api/v1/playlists/:id / key:fp-worker:users: R0 A, R2 D",
	"PUT /api/v1/playlists/:id / key:fp-worker:view: R0 A, R2 D",
	"PUT /api/v1/plugins/:id/settings / authoff: R0 A, R2 D",
}

// reachGoldenProblems is what the golden list fails on, as text.
func reachGoldenProblems(res reachDiffResult) []string {
	var got []string
	for _, c := range res.cells {
		got = append(got, c.String())
	}
	sort.Strings(got)
	want := slices.Clone(reachGoldenCells)
	sort.Strings(want)
	if slices.Equal(got, want) {
		return nil
	}
	in := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range list {
			m[s] = true
		}
		return m
	}
	gotSet, wantSet := in(got), in(want)
	var out []string
	for _, c := range got {
		if !wantSet[c] {
			out = append(out, "a cell the oracle shows and the golden list does not: "+c)
		}
	}
	for _, c := range want {
		if !gotSet[c] {
			out = append(out, "a golden cell the oracle no longer shows: "+c)
		}
	}
	if len(got) != len(want) {
		out = append(out, fmt.Sprintf("the oracle shows %d differing cells, the golden list holds %d", len(got), len(want)))
	}
	return out
}

func TestReachOracle_TheDifferingCellsAreExactlyTheGoldenList(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _ := reachCompare(t, pd, vd, atoms, atoms)
	for _, p := range reachGoldenProblems(res) {
		t.Error(p)
	}
}
