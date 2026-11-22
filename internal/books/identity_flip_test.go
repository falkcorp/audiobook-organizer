// file: internal/books/identity_flip_test.go
// version: 0.1.0
// guid: 8c2e6b40-1a97-4d53-b6e8-3f0d7a95c214
// last-edited: 2026-11-21

package books

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/storage"
)

// TestIdentityFlip_OffByDefault: nothing in a plain test binary, and so nothing
// in a shipped one, turns the identity predicate on. Until B10g flips it, the
// manifest is F3's alone: books.level-1, no books.level-2, and no retired
// identity claim.
func TestIdentityFlip_OffByDefault(t *testing.T) {
	if identityOn() {
		t.Fatal("the identity predicate is on with no selector call: ABO would run under core identity before B10g")
	}
	if flipShapeOptions().Identity != nil {
		t.Fatal("flipShapeOptions asks for the identity flip with the predicate off")
	}
	man := flipOn().Manifest()
	if flipShapeOptions().Tasks != nil {
		t.Fatal("flipShapeOptions asks for the tasks flip with the predicate off")
	}
	for _, id := range []string{cutover.BooksIdentityLevelStepID, cutover.BooksTasksLevelStepID} {
		if _, ok := versionOf(man.Storage.Migrations, id); ok {
			t.Errorf("the manifest declares %s with the predicate off", id)
		}
	}
	if man.Storage.Level != cutover.BooksCutoverLevel {
		t.Errorf("Spec.Level = %d with the predicate off, want %d", man.Storage.Level, cutover.BooksCutoverLevel)
	}
}

// TestIdentityFlip_SelectorMovesTheManifest: the selector turns the predicate on
// for the manifest's storage declaration (books.level-2 at the next contiguous
// version, Spec.Level raised to BooksIdentityLevel, the identity families
// retired), and the restore function puts the manifest back exactly.
func TestIdentityFlip_SelectorMovesTheManifest(t *testing.T) {
	off := flipOn().Manifest().Storage
	restore := SetIdentityFlipForTest(true)
	if !identityOn() {
		t.Fatal("SetIdentityFlipForTest(true) did not turn the predicate on")
	}
	on := flipOn().Manifest().Storage
	lvl, ok := versionOf(on.Migrations, cutover.BooksIdentityLevelStepID)
	if !ok {
		t.Fatalf("no %s in the manifest with the predicate on", cutover.BooksIdentityLevelStepID)
	}
	if lvl.Version != booksLevelStepVersion+1 || lvl.RaisesLevel != cutover.BooksIdentityLevel {
		t.Errorf("level-2 = %+v; want version %d raising to %d", lvl, booksLevelStepVersion+1, cutover.BooksIdentityLevel)
	}
	// The tasks flip rides the same switch (F5-18): books.level-3 follows level-2
	// and Spec.Level is the last level step's.
	lvl3, ok := versionOf(on.Migrations, cutover.BooksTasksLevelStepID)
	if !ok {
		t.Fatalf("no %s in the manifest with the predicate on", cutover.BooksTasksLevelStepID)
	}
	if lvl3.Version != booksLevelStepVersion+2 || lvl3.RaisesLevel != cutover.BooksTasksLevel || on.Level != cutover.BooksTasksLevel {
		t.Errorf("level-3 = %+v, Spec.Level %d; want version %d raising to %d", lvl3, on.Level, booksLevelStepVersion+2, cutover.BooksTasksLevel)
	}
	retired := func(s storage.Spec) (n int) {
		for _, c := range s.Claims {
			if c.Mode == storage.ClaimRetired {
				n++
			}
		}
		return n
	}
	if retired(on) <= retired(off) {
		t.Errorf("retired claims %d with the predicate on, %d off: the identity families are not retired", retired(on), retired(off))
	}
	restore()
	if identityOn() {
		t.Fatal("the restore function left the predicate on")
	}
	after := flipOn().Manifest().Storage
	if after.Level != off.Level || len(after.Migrations) != len(off.Migrations) || retired(after) != retired(off) {
		t.Errorf("manifest after restore = level %d, %d steps, %d retired; want the predicate-off manifest (%d, %d, %d)",
			after.Level, len(after.Migrations), retired(after), off.Level, len(off.Migrations), retired(off))
	}
}

// TestIdentityFlip_StandaloneOpensFollowThePredicate: the op-runner child's
// store and the restore stager's verify open the store through StandaloneSpec.
// With the predicate on it must carry books.level-2 too, or those opens refuse
// the store the serve boot raised as written by a newer build.
func TestIdentityFlip_StandaloneOpensFollowThePredicate(t *testing.T) {
	base := flipOn().Manifest().Storage
	t.Cleanup(SetIdentityFlipForTest(true))
	spec := StandaloneSpec(base)
	if spec.Level != cutover.BooksTasksLevel {
		t.Errorf("StandaloneSpec level = %d with the predicate on, want %d", spec.Level, cutover.BooksTasksLevel)
	}
	for _, id := range []string{cutover.BooksIdentityLevelStepID, cutover.BooksTasksLevelStepID} {
		if !slices.ContainsFunc(spec.Migrations, func(m storage.Migration) bool { return m.Name == id }) {
			t.Errorf("StandaloneSpec carries no %s with the predicate on", id)
		}
	}
}
