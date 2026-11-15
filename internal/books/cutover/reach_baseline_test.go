// file: internal/books/cutover/reach_baseline_test.go
// version: 1.2.0
// guid: 3b8f1c64-d702-4e95-a6c3-0e47b9d1f258
// last-edited: 2026-11-15

package cutover

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// What the recorded baseline is a baseline of, and what the oracle leaves out of
// the comparison, each pinned so that neither can change without a decision.

const reachOverlayDir = "internal/books/testdata/r2corpus/overlay"

// reachOverlayDigest is overlay_digest of scripts/r2corpus/record_r0_reach.py: the
// sha256 of, for each file of the store in slash-path order, its path, a NUL, the
// sha256 of its content and a newline. Mode and mtime are not part of it.
func reachOverlayDigest(t testing.TB) string {
	t.Helper()
	root := filepath.Join(repoRoot(t), filepath.FromSlash(reachOverlayDir))
	var rels []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil || len(rels) == 0 {
		t.Fatalf("the overlay store %s: %d files, %v", reachOverlayDir, len(rels), err)
	}
	sort.Strings(rels)
	h := sha256.New()
	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		h.Write([]byte(rel + "\x00" + hex.EncodeToString(sum[:]) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// R0's answers were recorded over one overlay store. If the store changes the
// baseline no longer describes the corpus M3 migrates, so the recording must be
// redone (record_r0_reach.py --check names the procedure), not the digest edited.
func TestReachBaseline_WasRecordedOverTheCommittedOverlayStore(t *testing.T) {
	_, vd := reachLoad(t)
	if got := reachOverlayDigest(t); got != vd.OverlaySHA256 {
		t.Errorf("the overlay store %s hashes to %s; verdicts.json was recorded over %q. Re-record R0's reach (scripts/r2corpus/record_r0_reach.py, see its docstring) before changing the store",
			reachOverlayDir, got, vd.OverlaySHA256)
	}
}

// The recording names the run that wrote it, so a reader can find the log.
func TestReachBaseline_NamesTheRunThatWroteIt(t *testing.T) {
	_, vd := reachLoad(t)
	for _, k := range []string{"recorder_commit", "workflow", "run_id", "run_attempt", "runner_image", "python"} {
		if vd.Recorded[k] == "" {
			t.Errorf("verdicts.json recorded.%s is empty", k)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(vd.Recorded["recorder_commit"]) {
		t.Errorf("recorded.recorder_commit %q is not a commit", vd.Recorded["recorder_commit"])
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(vd.Recorded["run_id"]) {
		t.Errorf("recorded.run_id %q is not a run id", vd.Recorded["run_id"])
	}
}

// reachABSOnlyButGated are R0 routes the route dump flags abs_only (an ABS client
// calls them) that R2's manifest still gates by a normal requirement, all of them
// Public: the infra probes, the login family, the OpenID entry points and the
// two cover and public-track reads. They are compared, not delegated.
var reachABSOnlyButGated = []string{
	"GET /api/items/:id/cover",
	"GET /auth/openid",
	"GET /auth/openid/browser",
	"GET /auth/openid/callback",
	"GET /ping",
	"GET /public/session/:id/track/:index",
	"GET /status",
	"HEAD /api/items/:id/cover",
	"POST /auth/openid/browser",
	"POST /auth/refresh",
	"POST /login",
}

// reachDelegatedNewerThanR0 are routes R2 delegates to the ABS handler's own token
// check that R0's router at the pinned tag does not have, so there is no recorded
// answer to compare with.
var reachDelegatedNewerThanR0 = []string{
	"GET /api/authors/:id/image",
	"GET /api/libraries/:libraryId/stats",
	"HEAD /api/authors/:id/image",
	"POST /api/items/batch/get",
}

// reachDelegatedProblems is what the delegated-set pin fails on, as text.
func reachDelegatedProblems(vd *reachR0Doc, res reachDiffResult) []string {
	var out []string
	r0 := map[string]bool{}
	absOnly := map[string]bool{}
	for _, r := range vd.Routes {
		r0[r.key()] = true
		if r.ABSOnly {
			absOnly[r.key()] = true
		}
	}
	gated := map[string]bool{}
	for _, k := range reachABSOnlyButGated {
		gated[k] = true
		if !absOnly[k] {
			out = append(out, k+" is listed as abs_only in R0 and is not")
		}
	}
	perm := map[string]rbac.Requirement{}
	for _, lr := range reachManifest() {
		perm[lr.Method+" "+lr.GinPath] = lr.Perm
	}
	for k := range gated {
		if perm[k].Kind() != rbac.KindPublic {
			out = append(out, fmt.Sprintf("%s: R0 flags it abs_only and R2 gates it by %v, which is not Public", k, perm[k]))
		}
	}
	want := map[string]bool{}
	for k := range absOnly {
		if !gated[k] {
			want[k] = true
		}
	}
	for _, k := range reachDelegatedNewerThanR0 {
		if r0[k] {
			out = append(out, k+" is listed as newer than R0, and R0 has it")
		}
		want[k] = true
	}
	got := map[string]bool{}
	for _, k := range res.delegated {
		got[k] = true
	}
	for k := range got {
		if !want[k] {
			out = append(out, k+" is delegated to the ABS handler on R2 and is neither an R0 abs_only route nor listed as newer than R0")
		}
	}
	for k := range want {
		if !got[k] {
			out = append(out, k+" is an R0 abs_only route (or newer than R0) that R2 no longer delegates, and is not listed as Public-gated")
		}
	}
	if len(want) != 68 || len(got) != 68 {
		out = append(out, fmt.Sprintf("delegated routes: want %d, got %d; the spec's count is 68", len(want), len(got)))
	}
	sort.Strings(out)
	return out
}

// The delegated set left out of the comparison is exactly R0's ABS-only routes,
// less the ones R2 gates as Public (listed), plus the routes newer than R0
// (listed). A route that becomes Delegated, or stops being, fails here: it would
// otherwise leave the oracle without a word, because a delegated route is
// counted and not compared.
func TestReachBaseline_DelegatedSetIsR0sABSOnlyRoutesWithTheListedExceptions(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _ := reachCompare(t, pd, vd, atoms, atoms)
	for _, p := range reachDelegatedProblems(vd, res) {
		t.Error(p)
	}
}

// reachUniformNotFoundProblems is what the uniform-404 pin fails on, as text.
func reachUniformNotFoundProblems(vd *reachR0Doc) []string {
	var out []string
	if len(vd.UniformNotFound) != 6 {
		out = append(out, fmt.Sprintf("the recording lists %d uniform-404 routes, want the 6 the review pinned: %v", len(vd.UniformNotFound), vd.UniformNotFound))
	}
	perm := map[string]rbac.Requirement{}
	for _, lr := range reachManifest() {
		perm[lr.Method+" "+lr.GinPath] = lr.Perm
	}
	for _, k := range vd.UniformNotFound {
		p, ok := perm[k]
		if !ok {
			out = append(out, k+": a uniform-404 route R2's manifest does not have")
			continue
		}
		if p.Kind() != rbac.KindPublic && p.Kind() != rbac.KindDelegated {
			out = append(out, fmt.Sprintf("%s answered 404 to every principal on R0 and R2 gates it by %v: the oracle never saw an authorization decision on it", k, p))
		}
	}
	return out
}

// A route whose 404 was the same for every principal shows no authorization
// decision at all (the probe's dummy id failed first), and it records A for the
// anonymous principal too, so it can never show a gain. Each such route must be
// open or delegated on R2; anything else would be a gate the oracle never saw.
func TestReachBaseline_UniformNotFoundRoutesAreOpenOrDelegated(t *testing.T) {
	_, vd := reachLoad(t)
	for _, p := range reachUniformNotFoundProblems(vd) {
		t.Error(p)
	}
}

// reachCountProblems is what the size pin fails on, as text.
func reachCountProblems(vd *reachR0Doc, res reachDiffResult) []string {
	got := map[string]int{
		"R0 routes recorded":                     vd.RouteCount,
		"manifest routes compared":               res.routesCompared,
		"routes delegated to the ABS handler":    len(res.delegated),
		"routes only R0 has":                     len(res.r0Only),
		"routes only R2 has":                     len(res.r2Only),
		"cells compared (task pseudo-routes in)": res.compared,
		"cells that differ":                      len(res.cells),
	}
	want := map[string]int{
		"R0 routes recorded":                     572,
		"manifest routes compared":               505,
		"routes delegated to the ABS handler":    68,
		"routes only R0 has":                     3,
		"routes only R2 has":                     1,
		"cells compared (task pseudo-routes in)": 27145,
		"cells that differ":                      217,
	}
	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var diff []string
	for _, k := range keys {
		if got[k] != want[k] {
			diff = append(diff, k+": got "+strconv.Itoa(got[k])+", want "+strconv.Itoa(want[k]))
		}
	}
	return diff
}

// The oracle's own size: R0's routes, the manifest routes it compared, the ones
// it left out and the ones only one side has. A route added to the manifest, or
// dropped, fails here and in the pinned one-sided list, so it cannot enter
// without someone deciding what it permits.
func TestReachOracle_RouteAndCellCountsArePinned(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _ := reachCompare(t, pd, vd, atoms, atoms)
	if diff := reachCountProblems(vd, res); len(diff) > 0 {
		t.Errorf("the oracle changed size; every route added or dropped needs a permission decision in the manifest and a place in the pinned lists, then these counts move with it:\n  %s", strings.Join(diff, "\n  "))
	}
}

// reachServerErrorReasons names the cause of every route R0 answers 5xx to for a
// probe that was let through: the same closed vocabulary as OK_5XX_REASONS in
// scripts/r2corpus/record_r0_reach.py, kept here by hand so that changing the
// recorder's list without this one (or the reverse) fails. A 5xx is a handler
// that was reached and could not finish in the throwaway store, never a
// refusal, and the token says which of its failures it was.
var reachServerErrorReasons = map[string]string{
	"POST /api/v1/ai/scans":                                     "ai-scan-pipeline-not-configured",
	"POST /api/v1/ai/scans/:id/cancel":                          "ai-scan-pipeline-not-configured",
	"POST /api/v1/ai/test-connection":                           "connection-test-failed",
	"PUT /api/v1/audiobooks/:id":                                "failed-to-update-audiobook",
	"POST /api/v1/audiobooks/bulk-write-back":                   "enqueue-failed",
	"POST /api/v1/audiobooks/:id/apply-metadata":                "failed-to-apply-metadata",
	"POST /api/v1/audiobooks/:id/mark-no-match":                 "failed-to-mark-no-match",
	"POST /api/v1/audiobooks/:id/parse-with-ai":                 "recovered-panic",
	"POST /api/v1/audiobooks/:id/quarantine":                    "quarantine-failed",
	"DELETE /api/v1/audiobooks/:id/quarantine":                  "unquarantine-failed",
	"POST /api/v1/audiobooks/:id/retired-versions/:vid/restore": "restore-not-journaled",
	"DELETE /api/v1/backup/:filename":                           "failed-to-delete-backup",
	"POST /api/v1/dedup/fingerprint-rescan":                     "no-fingerprint-backend",
	"GET /api/v1/fingerprint/worker/hello":                      "worker-not-running",
	"POST /api/v1/fingerprint/worker/lease/:id/release":         "worker-not-running",
	"POST /api/v1/fingerprint/worker/lease/:id/renew":           "worker-not-running",
	"POST /api/v1/update/check":                                 "failed-to-check-for-updates",
	"GET /api/v1/deluge/labels":                                 "deluge-unreachable",
	"POST /api/v1/deluge/test-connection":                       "deluge-unreachable",
	"GET /api/v1/deluge/torrents":                               "deluge-unreachable",
	"GET /api/v1/deluge/discover":                               "deluge-unreachable",
	"POST /api/v1/operations/:id/revert":                        "failed-to-revert-operation",
	"POST /api/v1/update/apply":                                 "failed-to-apply-update",
}

// Every 5xx cell carries the token the list names for its route, and no other
// cell carries one, so a 5xx whose cause changed (a different handler failure,
// or a refusal that now looks like one) cannot be recorded unnoticed.
func TestReachBaseline_EveryServerErrorCellCarriesItsListedReason(t *testing.T) {
	_, vd := reachLoad(t)
	seen := map[string]bool{}
	for _, r := range vd.Routes {
		if len(r.Status) != len(vd.Principals) {
			t.Fatalf("%s: %d statuses for %d principals", r.key(), len(r.Status), len(vd.Principals))
		}
		var has5xx bool
		for _, s := range r.Status {
			if s >= 500 {
				has5xx = true
			}
		}
		if !has5xx {
			if len(r.Reason) != 0 {
				t.Errorf("%s: has no 5xx cell but records reasons %v", r.key(), r.Reason)
			}
			continue
		}
		seen[r.key()] = true
		want, listed := reachServerErrorReasons[r.key()]
		if !listed {
			t.Errorf("%s: answers 5xx but reachServerErrorReasons names no reason for it; decide what failed before adding one", r.key())
			continue
		}
		if len(r.Reason) != len(r.Status) {
			t.Errorf("%s: %d reasons for %d cells", r.key(), len(r.Reason), len(r.Status))
			continue
		}
		for i, s := range r.Status {
			switch got := r.Reason[i]; {
			case s >= 500 && got != want:
				t.Errorf("%s %s: 5xx recorded with reason %q, the list names %q", r.key(), vd.Principals[i], got, want)
			case s < 500 && got != "":
				t.Errorf("%s %s: status %d carries reason %q", r.key(), vd.Principals[i], s, got)
			}
		}
	}
	// A listed route that no cell reaches is the recorder's to keep (R0 may answer
	// it 2xx or 4xx in a throwaway store); it must at least still be a route.
	have := map[string]bool{}
	for _, r := range vd.Routes {
		have[r.key()] = true
	}
	for k := range reachServerErrorReasons {
		if !have[k] {
			t.Errorf("reachServerErrorReasons lists %s, which is not a recorded route", k)
		}
	}
	if len(seen) == 0 {
		t.Error("no route answered 5xx; the reason check would pass on an empty recording")
	}
}
