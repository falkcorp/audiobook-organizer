// file: internal/arch/progress_update_guard_test.go
// version: 0.1.0
// guid: 3f78624a-78ed-43e1-8f86-d65bee8ab5e5
// last-edited: 2026-11-28

package arch

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// setThenNoteProblems finds, in the non-test Go files (path to source), a
// Progress().Set( call followed within four lines by a Progress().Note( call.
//
// It pins one spelling only: the chained rc.Progress().Set/Note pair the
// built-in kinds' env closures used. A Progress held in a variable
// (p := rc.Progress(); p.Set(...); p.Note(...)), a pair more than four lines
// apart and a pair split across helpers all pass it. It is a ratchet on the
// closures F5-18 P1 round 2 moved to Update, not a proof that no run does the
// pair.
func setThenNoteProblems(files map[string][]byte) []string {
	var out []string
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		lines := strings.Split(string(files[p]), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "Progress().Set(") {
				continue
			}
			for j := i + 1; j < len(lines) && j <= i+4; j++ {
				if strings.Contains(lines[j], "Progress().Note(") {
					out = append(out, p+":"+strconv.Itoa(i+1)+": Progress().Set followed by Progress().Note at line "+strconv.Itoa(j+1)+"; use Progress().Update")
				}
			}
		}
	}
	return out
}

// TestNoSetThenNote: a kind that reports a position and a status line calls
// rc.Progress().Update once. Set followed by Note is two updates, and the
// executor throttles progress writes, so the Set's due tick restarts the window
// and the note reaches the stream only at the window's end (F5-18 P1 round 2).
func TestNoSetThenNote(t *testing.T) {
	if probs := setThenNoteProblems(productionGoFiles(t)); len(probs) > 0 {
		t.Fatalf("%d Set-then-Note pair(s):\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// TestSetThenNoteGuard_CatchesThePair: the guard fails on the pair and passes
// the combined call, so a change that loosens it is caught here.
func TestSetThenNoteGuard_CatchesThePair(t *testing.T) {
	pair := []byte("package env\nfunc f() {\n\trc.Progress().Set(1, 2)\n\trc.Progress().Note(\"Joker\")\n}\n")
	update := []byte("package env\nfunc f() {\n\trc.Progress().Update(1, 2, \"Joker\")\n}\n")
	if probs := setThenNoteProblems(map[string][]byte{"internal/plugins/gotham/env.go": pair}); len(probs) != 1 || !strings.Contains(probs[0], "env.go:3") {
		t.Fatalf("the pair: problems %v, want one at env.go:3", probs)
	}
	if probs := setThenNoteProblems(map[string][]byte{"internal/plugins/gotham/env.go": update}); len(probs) != 0 {
		t.Fatalf("the combined call flagged: %v", probs)
	}
}
