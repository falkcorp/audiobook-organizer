// file: internal/books/cutover/registered_harness_parity_test.go
// version: 0.1.0
// guid: 904e2c25-5524-4675-bb4e-aa44f0039974
// last-edited: 2026-11-22

package cutover_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// The parity assertions for the narrowings the sweep names but does not accept.
//
// EveryVerdictIsR0sOrNamed keeps the tables honest: it fails on an unnamed difference
// and on a named one that no longer differs. It cannot say that a named difference is
// wrong, because naming it is what makes the sweep pass. The tests here say it: each
// holds the cells of one class of verdictRows to R0's verdict, so they are red while the
// narrowing exists and go green when the fix lands and the rows are deleted (the sweep
// requires the deletion). They sit last in the stack so the commits that must pass come
// first.

// parityMisses runs the cells rows name on h and returns, one line each, those whose
// verdict is not the one R0 recorded for the principal. A principal not on h (the
// auth-off one on the auth-on boot, and the reverse) is skipped by only. A core key is
// held to its abk_ twin's verdict, as the sweep does.
func parityMisses(t *testing.T, h *Registered, rows []verdictRow, only func(*Principal) bool) []string {
	t.Helper()
	principals, routes := recordedVerdicts(t)
	var out []string
	for _, row := range rows {
		ri := slices.IndexFunc(routes, func(r recordedRoute) bool { return r.Method == row.Method && r.Path == row.Route })
		if ri < 0 {
			t.Fatalf("%s %s is not a route R0 recorded", row.Method, row.Route)
		}
		rec := routes[ri]
		for _, name := range row.Principals {
			for _, pn := range []string{name, coreKeyName(name)} {
				p := h.byName[pn]
				if p == nil || !only(p) {
					continue
				}
				pi := slices.Index(principals, p.Recorded())
				if pi < 0 {
					t.Fatalf("%s has no recorded verdict", p.Name)
				}
				res, err := h.Do(p, rec.Method, rec.ProbePath, nil)
				if errors.Is(err, ErrOwnAuth) {
					continue
				}
				if err != nil {
					t.Fatalf("%s %s as %s: %v", rec.Method, rec.Path, p.Name, err)
				}
				if got, want := res.Verdict(), rec.Verdict[pi]; got != want {
					out = append(out, fmt.Sprintf("%s %s as %s: R0 %c, now %d %s", rec.Method, rec.Path, p.Name, want, res.Status, res.Code))
				}
			}
		}
	}
	return out
}

// onAuthOn and onAuthOff are the principals of each boot.
func onAuthOn(p *Principal) bool  { return p != nil && p.Kind != "authoff" }
func onAuthOff(p *Principal) bool { return p != nil && p.Kind == "authoff" }

// TestRegisteredHarness_ParityMissesSeesADifference is parityMisses's positive control:
// a helper that returned nothing for every input would make the red tests below pass
// vacuously the day they were meant to catch something. Rows naming cells that do
// differ from R0 (the accepted fp-worker narrowing; a route R0 recorded and the flipped
// build refuses to its key) must come back, and a cell that answers as R0 did must not.
func TestRegisteredHarness_ParityMissesSeesADifference(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	var known []verdictRow
	for _, r := range verdictRows {
		if r.Class == classKnown {
			known = append(known, r)
		}
	}
	if len(known) == 0 {
		t.Fatal("no classKnown rows to use as the positive control")
	}
	if got := parityMisses(t, h, known, onAuthOn); len(got) == 0 {
		t.Error("parityMisses found nothing for the accepted fp-worker narrowing: it cannot see a difference")
	} else {
		// Every cell of the rows comes back, none invented: the helper and the sweep agree.
		want := 0
		for _, r := range known {
			for _, name := range r.Principals {
				for _, pn := range []string{name, coreKeyName(name)} {
					if onAuthOn(h.byName[pn]) {
						want++
					}
				}
			}
		}
		if len(got) != want {
			t.Errorf("parityMisses returned %d cells for rows naming %d: %v", len(got), want, got)
		}
	}
	same := []verdictRow{{Method: "GET", Route: "/api/v1/audiobooks", Principals: []string{"session:viewer", "key:viewer:view"}}}
	if got := parityMisses(t, h, same, onAuthOn); len(got) != 0 {
		t.Errorf("parityMisses reported a cell that answers as R0 did: %v", got)
	}
}

// TestRegisteredHarness_UnruledNarrowingsHoldR0 holds the classRuling cells to R0: keys
// on POST /auth/api-keys (refused with "API keys cannot change ... keys of other
// users"), keys on the invite list and delete routes, and a custom-role session on
// backup restore (core:admin is required). Nobody has ruled on these. They are red
// until each is either fixed or accepted: accepting one moves its row to classKnown
// with the ruling that accepted it, and the row is deleted from here by that move.
func TestRegisteredHarness_UnruledNarrowingsHoldR0(t *testing.T) {
	var rows []verdictRow
	for _, r := range verdictRows {
		if r.Class == classRuling {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		t.Skip("no unruled narrowing is left")
	}
	h := bootRegistered(t, registeredConfig{})
	for _, miss := range parityMisses(t, h, rows, onAuthOn) {
		t.Error(miss)
	}
}

// TestRegisteredHarness_B10yRoutesHoldR0Parity holds the classB10y cells to R0: the
// key:admin:{every,empty} and custom cells on admin/debug, cache, master-lists, user
// merges and footprint, digest and wipe routes, the deluge discover and import cells,
// and the auth-off invite routes. R0 and HEAD admit them, the flipped build refuses
// them, and nobody accepted that; slice B10y is the fix. The test is red until it lands
// and skips once the rows are deleted (the sweep requires deleting a row that no longer
// differs).
func TestRegisteredHarness_B10yRoutesHoldR0Parity(t *testing.T) {
	var rows []verdictRow
	for _, r := range verdictRows {
		if r.Class == classB10y {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		t.Skip("slice B10y has landed: no row is left")
	}
	t.Run("auth on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		for _, miss := range parityMisses(t, h, rows, onAuthOn) {
			t.Error(miss)
		}
	})
	t.Run("auth off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		for _, miss := range parityMisses(t, h, rows, onAuthOff) {
			t.Error(miss)
		}
	})
}

// TestRegisteredHarness_B10zCoreKeyReachesMe is the ambient-scope parity ruling: a core
// mck_ key made from a role that does not hold core:admin carries no core:self (an
// ambient scope no creator holds to give), and /api/core/v1/me needs it, so the flipped
// build refuses a key that HEAD's /auth/me answers. Parity is the ruling; B10z owns the
// fix. Red until it lands. When it does, TestRegisteredHarness_CredentialKindOnCoreMe's
// "core:self refused" case pins the old answer and must be changed to this one.
func TestRegisteredHarness_B10zCoreKeyReachesMe(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	n := 0
	for _, p := range h.Principals {
		if !p.Core || p.Twin == "key:admin:every" || p.Twin == "key:admin:empty" {
			continue
		}
		n++
		res, err := h.Do(p, "GET", "/api/core/v1/me", nil)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if res.Status != 200 {
			t.Errorf("%s: GET /api/core/v1/me %d %s, want 200 with the key's own identity", p.Name, res.Status, res.excerpt())
		}
	}
	if n == 0 {
		t.Fatal("no core key without core:admin on this boot: the cell checks nothing")
	}
}
