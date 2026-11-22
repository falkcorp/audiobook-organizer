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

// parityMisses runs the cells rows name on h, each as the request R0's recorder sent
// (recordedRequest), and returns, one line each, those whose verdict is not the one
// R0 recorded for the principal. A principal not on h (the
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
				path, body := recordedRequest(rec, p)
				res, err := h.Do(p, rec.Method, path, body)
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

// onAuthOn picks the principals of the auth-on boot, onAuthOff the auth-off boot's.
func onAuthOn(p *Principal) bool { return p != nil && p.Kind != "authoff" }

func onAuthOff(p *Principal) bool { return p != nil && p.Kind == "authoff" }

// TestRegisteredHarness_ParityMissesSeesADifference is parityMisses's positive control:
// a helper that returned nothing for every input would make the red tests below pass
// vacuously the day they were meant to catch something. Rows naming cells that do
// differ from R0 (the accepted fp-worker narrowing; a route R0 recorded and the flipped
// build refuses to its key) must come back, and a cell that answers as R0 did must not.
func TestRegisteredHarness_ParityMissesSeesADifference(t *testing.T) {
	h := bootAsRecorded(t, false)
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

// TestRegisteredHarness_UnruledNarrowingsHoldR0 holds the classRuling cells to R0: a
// narrowing nobody has ruled on is red here until it is fixed (its row deleted) or
// accepted (its row moved to classKnown with the ruling that accepted it). The rows it
// was written for are all gone: the invite and restore cells were fixed in B10y, and
// the 21 key cells on POST /auth/api-keys were the sweep's empty probe body (with R0's
// {} they reach the handler, B10u2; the three fp-worker cells are the accepted
// fp-worker narrowing). It skips while no row is left and goes red when one is added.
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
	h := bootAsRecorded(t, false)
	for _, miss := range parityMisses(t, h, rows, onAuthOn) {
		t.Error(miss)
	}
}

// TestRegisteredHarness_B10yRoutesHoldR0Parity holds the classB10y cells to R0, on
// both boots: a cell R0 and HEAD admit, the flipped build refuses, and nobody accepted.
// It was written red (B10d-h2) for the cells slice B10y was to fix. B10y fixed them,
// except three groups that were never flip bugs (B10u2): the deluge discover pair was
// the sweep's corpus with discovery off (R0 recorded it on), and the debug edit routes'
// admin-key cells are the ruled Sensitive narrowing (classKnown). With no row left it
// skips; a classB10y row added later turns it red until the fix deletes the row (the
// sweep requires deleting a row that no longer differs).
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
		h := bootAsRecorded(t, false)
		for _, miss := range parityMisses(t, h, rows, onAuthOn) {
			t.Error(miss)
		}
	})
	t.Run("auth off", func(t *testing.T) {
		h := bootAsRecorded(t, true)
		for _, miss := range parityMisses(t, h, rows, onAuthOff) {
			t.Error(miss)
		}
	})
}
