// file: internal/books/cutover/reach_status_class_test.go
// version: 1.0.0
// guid: 7964c506-60dc-4cf8-99e0-307a4da4d29a
// last-edited: 2026-11-28

package cutover_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The status-class half of the reach oracle (F4-08 fix-forward).
//
// Verdict() maps only 401 and 403 to D, so a route that moved from 200 to 410 (a
// retired route) or back kept the letter A and the oracle could not see it. The oracle
// now also compares the class of the status (StatusClass) with the one R0 recorded.
// These cells hold that on synthetic data, with no boot: compareToR0 and diffProblems
// take plain structs.

// TestStatusClass pins the classes: the ones a change of must show, and the pairs inside
// one class that must not.
func TestStatusClass(t *testing.T) {
	for class, statuses := range map[string][]int{
		"2xx":    {200, 201, 202, 204},
		"3xx":    {301, 302, 307},
		"denied": {401, 403},
		"404":    {404},
		"410":    {410},
		"4xx":    {400, 405, 409, 422, 429},
		"5xx":    {500, 501, 502, 503},
		"other":  {0, 100, 600},
	} {
		for _, st := range statuses {
			if got := StatusClass(st); got != class {
				t.Errorf("StatusClass(%d) = %q, want %q", st, got, class)
			}
		}
	}
	if (Result{Status: http.StatusGone}).Class() != "410" {
		t.Error("Result.Class does not read the status")
	}
}

// classRecording is a one-route recording of R0: route "POST /x", principals p1..p3,
// R0's letters and statuses as given.
func classRecording(verdict string, status ...int) r0Recording {
	return r0Recording{
		Principals: []string{"p1", "p2", "p3"},
		Routes:     []r0Route{{Method: "POST", Path: "/x", Verdict: verdict, Status: status}},
	}
}

// classPass is what the flip answered for "POST /x" as p1, p2, p3 with the letters
// Verdict() gives.
func classPass(statuses ...int) *oraclePass {
	pass := &oraclePass{cells: map[string]map[string]oracleCell{"POST /x": {}}, recorded: map[string]string{}}
	for i, st := range statuses {
		name := []string{"p1", "p2", "p3"}[i]
		pass.recorded[name] = name
		pass.cells["POST /x"][name] = oracleCell{letter: (Result{Status: st}).Verdict(), status: st}
	}
	return pass
}

// TestReachCompare_ClassChangeIsADiff: a 200 to 410 flip, and a 410 to 200 flip, are
// differences although both letters are A; a move inside a class is not.
func TestReachCompare_ClassChangeIsADiff(t *testing.T) {
	r0 := classRecording("AAA", 200, 410, 200)
	diffs, compared := compareToR0(r0, classPass(410, 200, 204))
	if compared != 3 {
		t.Errorf("compared %d cells, want 3", compared)
	}
	var got []string
	for _, d := range diffs {
		got = append(got, d.principal+":"+d.r0Class+">"+StatusClass(d.status))
		if d.r0 != 'A' || d.flip != 'A' {
			t.Errorf("%s: want the letter A on both sides, got %c and %c", d, d.r0, d.flip)
		}
	}
	// p3's 200 to 204 stays inside 2xx and is no difference.
	if want := []string{"p1:2xx>410", "p2:410>2xx"}; !slices.Equal(got, want) {
		t.Errorf("differing cells = %v, want %v", got, want)
	}

	// Inside one class: no difference. A refusal's 401 to 403 stays denied, a 400 to a
	// 422 stays a client error, and a 502 to a 500 a server error.
	for _, c := range []struct {
		name      string
		verdict   string
		r0, flip  int
		wantDiffs int
	}{
		{"401 to 403", "D", 401, 403, 0},
		{"400 to 422", "A", 400, 422, 0},
		{"502 to 500", "A", 502, 500, 0},
		{"400 to 404", "A", 400, 404, 1},
		{"404 to 200", "A", 404, 200, 1},
		{"200 to 500", "A", 200, 500, 1},
		{"403 to 401", "D", 403, 401, 0},
	} {
		d, _ := compareToR0(classRecording(c.verdict+"AA", c.r0, 200, 200), classPass(c.flip, 200, 200))
		if len(d) != c.wantDiffs {
			t.Errorf("%s: %d differing cells %v, want %d", c.name, len(d), d, c.wantDiffs)
		}
	}

	// A recording with no status for the cell (the task pseudo-routes of an older file)
	// is compared by letter alone, and must not panic.
	if d, _ := compareToR0(classRecording("AAA"), classPass(410, 200, 200)); len(d) != 0 {
		t.Errorf("a cell with no recorded status differs: %v", d)
	}
	// The letters still decide when they differ.
	if d, _ := compareToR0(classRecording("AAA", 200, 200, 200), classPass(403, 200, 200)); len(d) != 1 || d[0].flip != 'D' {
		t.Errorf("a 200 to 403 flip: %v", d)
	}
}

// classRow is a table row accepting p1's 200 to 410 flip.
func classRow(r0Class string) diffRow {
	return diffRow{routes: []string{"POST /x"}, who: []string{"p1"}, r0: 'A', flip: 'A', status: http.StatusGone, r0Class: r0Class, why: "synthetic"}
}

func classTable(rows ...diffRow) diffTable {
	return diffTable{name: "synthetic", want: len(rows), rows: rows}
}

func neverRefused(Result, int, string) bool { return true }

func hasProblem(problems []string, sub string) bool {
	return slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, sub) })
}

// TestReachDiffs_ClassChangeFailsUnlessRecorded: the comparison fails on a 200 to 410
// flip no table lists, passes once a row records exactly it, and fails again when the
// row is wrong about the class, the status, or no longer differs.
func TestReachDiffs_ClassChangeFailsUnlessRecorded(t *testing.T) {
	h := &Registered{}
	flipped, _ := compareToR0(classRecording("AAA", 200, 200, 200), classPass(410, 200, 200))
	back, _ := compareToR0(classRecording("AAA", 410, 200, 200), classPass(200, 200, 200))
	same, _ := compareToR0(classRecording("AAA", 200, 200, 200), classPass(200, 200, 200))
	if len(flipped) != 1 || len(back) != 1 || len(same) != 0 {
		t.Fatalf("setup: %d, %d, %d differing cells, want 1, 1, 0", len(flipped), len(back), len(same))
	}

	// 200 to 410, and 410 to 200: unlisted.
	for name, diffs := range map[string][]oracleDiff{"200 to 410": flipped, "410 to 200": back} {
		if p := h.diffProblems(diffs, nil, neverRefused); !hasProblem(p, "unlisted change of status class") {
			t.Errorf("%s with no row: %v, want the unlisted change of status class", name, p)
		}
	}
	// Recorded by a row: clean.
	if p := h.diffProblems(flipped, []diffTable{classTable(classRow("2xx"))}, neverRefused); len(p) != 0 {
		t.Errorf("200 to 410 recorded by a row: %v", p)
	}
	// The row is for the other direction, or names the wrong class.
	if p := h.diffProblems(back, []diffTable{classTable(classRow("2xx"))}, neverRefused); len(p) == 0 {
		t.Error("a row recording 200 to 410 accepted a 410 to 200 flip")
	}
	if p := h.diffProblems(flipped, []diffTable{classTable(classRow("404"))}, neverRefused); !hasProblem(p, "answered class 404") {
		t.Errorf("a row naming the wrong R0 class: %v", p)
	}
	// A row that does not name a change of class, or names none: the table is wrong.
	if p := h.diffProblems(flipped, []diffTable{classTable(classRow(""))}, neverRefused); !hasProblem(p, "names no change of class") {
		t.Errorf("a same-letter row with no R0 class: %v", p)
	}
	if p := h.diffProblems(flipped, []diffTable{classTable(classRow("410"))}, neverRefused); !hasProblem(p, "names no change of class") {
		t.Errorf("a same-letter row whose R0 class is its flip class: %v", p)
	}
	// The flip answers another status of the same class as the row's: the exact status counts.
	other, _ := compareToR0(classRecording("AAA", 200, 200, 200), classPass(404, 200, 200))
	if p := h.diffProblems(other, []diffTable{classTable(classRow("2xx"))}, neverRefused); !hasProblem(p, "admits with 410") {
		t.Errorf("a row for 410 accepted a 404: %v", p)
	}
	// The revert: the route answers 200 again while the row still lists it.
	if p := h.diffProblems(same, []diffTable{classTable(classRow("2xx"))}, neverRefused); !hasProblem(p, "listed difference no longer differs") {
		t.Errorf("a row for a cell that answers as R0 did: %v", p)
	}
}

// TestRecordedPrincipalsAreTheRecording holds recordedPrincipals, the list the tables
// of "every principal" are built from, to verdicts.json: the same names in the same
// order, the auth-disabled principal last and left out. The logout list's exclusions
// are all in it, so a renamed principal fails here and not as a miscounted table.
func TestRecordedPrincipalsAreTheRecording(t *testing.T) {
	r0 := loadR0Recording(t)
	want := r0.Principals[:len(r0.Principals)-1] // loadR0Recording holds the last one to Authoff
	if !slices.Equal(recordedPrincipals, want) {
		t.Errorf("recordedPrincipals differs from verdicts.json's principals:\n%v\n%v", recordedPrincipals, want)
	}
	if got := len(recordedPrincipals) - len(logoutAnswers200); got != 9 {
		t.Errorf("logoutAnswers200 leaves out %d principals, want 9", got)
	}
	for _, name := range logoutAnswers200 {
		if !slices.Contains(recordedPrincipals, name) {
			t.Errorf("logoutAnswers200 names %q, which verdicts.json does not record", name)
		}
	}
}
