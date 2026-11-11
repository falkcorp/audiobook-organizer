// file: internal/books/cutover/levelsteps.go
// version: 1.0.0
// guid: 28c83ba3-7f5e-47ec-a11d-cb43589d1f81
// last-edited: 2026-11-11

package cutover

import (
	"github.com/falkcorp/mediacore/storage"
)

// BooksLevelStep is books' half of M7 (03 §3.8 row M7; 05-storage §5S.3.7 "The
// level step procedure"): a Breaking versioned step with no writes of its own
// that raises books to level, named books.level-<level> (storage.LevelStepName).
// version is books' next contiguous version (its versioned steps start at
// LegacyCap+1, so F3's is the one after books' last), level the flip's, which
// the same PR declares as books' Spec.Level; F3's is BooksCutoverLevel. after
// holds the steps it must follow.
//
// The legacy projection M7 stages (.storage-format = LegacyMaxFormat+1, the
// marker and the sidecar) is not this step's: it is books' Spec.LegacyGuard
// (store.BooksSpec), which storage runs in the level step's stamp batch for
// an owner that declares one. Nothing here registers the step: F3-07 does,
// next to core's half (storage.CoreLevelStep), and the data step it guards
// names it in its After through BooksLevelStepRef.
//
// Each later flip that raises books' level calls it again at books' next
// contiguous version with its own level constant and keeps this call as it
// was (F3-04's "Flip mechanism").
func BooksLevelStep(version, level int, after ...storage.StepRef) storage.Migration {
	return storage.LevelStep(BooksOwner, version, level, after...)
}

// BooksLevelStepRef is the StepRef a data step names in its After to run
// behind books' level step at version.
func BooksLevelStepRef(version int) storage.StepRef {
	return storage.StepRef{Owner: BooksOwner, Version: version}
}

// RewriteStep is books' M8 (03 §3.8 row M8; 05-storage §5S.3.13 "M8: the
// rewrite"): the declaration of the store rewrite, ID RewriteID, fixed for all
// of R2 (D15). F3-07 hands it to the kernel (DataMigrationsConfig.Rewrite)
// last, after every registered migration, and a later flip inserts its steps
// before it and never renames, replaces or removes it.
//
// The brief named its return type datamigrate.DataMigration; F3-04's M8
// invocation API returns storage.RewriteStep (NewRewriteStep), which the
// kernel's DataMigrationsConfig.Rewrite takes, so that is what this returns.
func RewriteStep(after ...storage.StepRef) storage.RewriteStep {
	return storage.NewRewriteStep(BooksOwner, RewriteID, after...)
}
