// file: internal/books/tasks_flip_test.go
// version: 0.1.0
// guid: 37daf04e-c619-49ad-8e9e-ac9b44e8fae1
// last-edited: 2026-11-27

package books

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/storage"
)

// TestTasksFlip_OffByDefault: nothing in a plain test binary, and so nothing in
// a shipped one, turns the tasks predicate on. Until the F5-18 switch the
// manifest is the identity flip's: books.level-2 is the last level step, there
// is no books.level-3, and Spec.Level is the identity level.
func TestTasksFlip_OffByDefault(t *testing.T) {
	if tasksFlipOn() {
		t.Fatal("the tasks predicate is on with no selector call: ABO would run on core's executor before the F5-18 switch")
	}
	if flipShapeOptions().Tasks != nil {
		t.Fatal("flipShapeOptions asks for the tasks flip with the predicate off")
	}
	man := flipOn().Manifest()
	if _, ok := versionOf(man.Storage.Migrations, cutover.BooksTasksLevelStepID); ok {
		t.Errorf("the manifest declares %s with the predicate off", cutover.BooksTasksLevelStepID)
	}
	if man.Storage.Level != cutover.BooksIdentityLevel {
		t.Errorf("Spec.Level = %d with the predicate off, want %d", man.Storage.Level, cutover.BooksIdentityLevel)
	}
}

// TestTasksFlip_SelectorMovesTheManifest: with the predicate on, books.level-3
// follows books.level-2 at the next version and Spec.Level is the last level
// step's.
func TestTasksFlip_SelectorMovesTheManifest(t *testing.T) {
	t.Cleanup(SetTasksFlipForTest(true))
	if flipShapeOptions().Tasks == nil {
		t.Fatal("flipShapeOptions does not ask for the tasks flip with the predicate on")
	}
	on := flipOn().Manifest().Storage
	lvl3, ok := versionOf(on.Migrations, cutover.BooksTasksLevelStepID)
	if !ok {
		t.Fatalf("no %s in the manifest with the predicate on", cutover.BooksTasksLevelStepID)
	}
	if lvl3.Version != booksLevelStepVersion+2 || lvl3.RaisesLevel != cutover.BooksTasksLevel || on.Level != cutover.BooksTasksLevel {
		t.Errorf("level-3 = %+v, Spec.Level %d; want version %d raising to %d", lvl3, on.Level, booksLevelStepVersion+2, cutover.BooksTasksLevel)
	}
}

// TestTasksFlip_StandaloneOpensFollowThePredicate: a standalone open (a CLI or
// restore) is at the tasks level, with both level steps, only with the predicate
// on.
func TestTasksFlip_StandaloneOpensFollowThePredicate(t *testing.T) {
	has := func(s storage.Spec, id string) bool {
		return slices.ContainsFunc(s.Migrations, func(m storage.Migration) bool { return m.Name == id })
	}
	off := StandaloneSpec(flipOn().Manifest().Storage)
	if off.Level != cutover.BooksIdentityLevel || has(off, cutover.BooksTasksLevelStepID) {
		t.Errorf("StandaloneSpec with the predicate off: level %d, level-3 present %v; want %d and absent", off.Level, has(off, cutover.BooksTasksLevelStepID), cutover.BooksIdentityLevel)
	}
	t.Cleanup(SetTasksFlipForTest(true))
	spec := StandaloneSpec(flipOn().Manifest().Storage)
	if spec.Level != cutover.BooksTasksLevel {
		t.Errorf("StandaloneSpec level = %d with the predicate on, want %d", spec.Level, cutover.BooksTasksLevel)
	}
	for _, id := range []string{cutover.BooksIdentityLevelStepID, cutover.BooksTasksLevelStepID} {
		if !has(spec, id) {
			t.Errorf("StandaloneSpec carries no %s with the predicate on", id)
		}
	}
}
