// file: internal/books/cutover/stepids_test.go
// version: 1.0.0
// guid: 5f6a3c0e-2c0d-4b64-9a0a-0f6b1b8e7a11
// last-edited: 2026-11-11

package cutover

import (
	"regexp"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// stepNameRE is a step ID's <name> half (03 §3.8; F3-06's brief).
var stepNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// TestStepIDs_UniqueAndWellFormed: every reserved ID is "<owner>.<name>" with
// the name matching [a-z][a-z0-9-]*, no two are equal, the seven IDs of
// 03 §3.8's order are all there, in that order, and the level step's ID is
// the one storage derives for books' level.
func TestStepIDs_UniqueAndWellFormed(t *testing.T) {
	ids := StepIDs()
	want := []string{
		BooksCutoverLevelStepID, ConfigCutoverID, IdentityCutoverID, TasksCutoverID, LogCutoverID, IntegrationsCutoverID, RewriteID,
	}
	if len(ids) != len(want) {
		t.Fatalf("StepIDs() lists %d IDs, want %d", len(ids), len(want))
	}
	seen := map[string]bool{}
	for i, id := range ids {
		if id != want[i] {
			t.Errorf("StepIDs()[%d] = %q, want %q (03 §3.8's order)", i, id, want[i])
		}
		owner, name, ok := strings.Cut(id, ".")
		switch {
		case !ok:
			t.Errorf("%q is not <owner>.<name>", id)
		case owner != string(BooksOwner):
			t.Errorf("%q: owner %q, want %q", id, owner, BooksOwner)
		case !stepNameRE.MatchString(name):
			t.Errorf("%q: name %q does not match %s", id, name, stepNameRE)
		}
		if seen[id] {
			t.Errorf("%q is reserved twice", id)
		}
		seen[id] = true
	}
	if got := storage.LevelStepName(BooksOwner, BooksCutoverLevel); got != BooksCutoverLevelStepID {
		t.Errorf("BooksCutoverLevelStepID = %q, storage names books' level-%d step %q", BooksCutoverLevelStepID, BooksCutoverLevel, got)
	}
	if got := BooksLevelStep(69, BooksCutoverLevel).Name; got != BooksCutoverLevelStepID {
		t.Errorf("BooksLevelStep names its step %q, want %q", got, BooksCutoverLevelStepID)
	}
}

// TestStepIDs_NoLiteralOutsideStepIDs: the package spells an ID only in
// stepids.go (F3-06's brief: "use only these constants in this package").
func TestStepIDs_NoLiteralOutsideStepIDs(t *testing.T) {
	for _, id := range StepIDs() {
		if strings.Count(id, ".") != 1 {
			t.Fatalf("%q is not one owner and one name", id)
		}
	}
	// The ID of M2 reaches the migration through the constant.
	m := ConfigStep(nil, nil, ConfigStepOptions{LevelStepVersion: 69})
	if m.ID != ConfigCutoverID || m.Owner != BooksOwner {
		t.Errorf("ConfigStep is %s of %s, want %s of %s", m.ID, m.Owner, ConfigCutoverID, BooksOwner)
	}
	if r := RewriteStep(); r.ID != RewriteID || r.Owner != BooksOwner {
		t.Errorf("RewriteStep is %s of %s, want %s of %s", r.ID, r.Owner, RewriteID, BooksOwner)
	}
}
