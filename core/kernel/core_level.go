// file: core/kernel/core_level.go
// version: 1.0.0
// guid: 3d9a52c1-7e84-4b06-a1f3-c58e0d27b946
// last-edited: 2026-11-19

package kernel

import (
	"github.com/falkcorp/mediacore/storage"
)

// Core's half of M7 (03 section 3.8 row M7; 05-storage section 5S.3.7; F3-04's
// "Flip mechanism"). A flip that adds a cutover step raises every owner's level
// it changes with a level step, one per level owner, named for the target level;
// books registers its own (internal/books/cutover.BooksLevelStep), the kernel
// registers core's here, when a manifest declares a cutover (Manifest.Cutover),
// so a build that declares none (hello, an app with no pre-core rows) raises no
// level and stamps nothing.
//
// The step is a versioned step like any other, so it needs a storage owner to
// carry it, and storage's own owner (core/storage) cannot be declared (it is
// storage's, and register refuses a spec for it). The kernel therefore owns one
// more core keyspace, core/levels, whose only purpose is to carry core's level
// steps: it holds no rows, and its applied records are the step's ledger. A
// later flip that raises core again adds core.level-<n> here at the owner's next
// contiguous version, keeps every earlier step as it was, and raises the
// owner's Level to <n> in the same PR (F3-04 rule 2).

// coreLevelsOwner is the storage owner that carries core's level steps. Its
// Level is core's declared level: every core subsystem shares the one "core"
// level (storage.Supported folds every core/<sub> owner's Level into it).
const coreLevelsOwner storage.Owner = "core/levels"

// CoreCutoverLevel is the core level F3's flip raises core to (05-storage
// section 5S.3.7): the Level coreLevelsOwner declares and the one level step
// core.level-1 raises core to. A build that declares no cutover declares none
// of it and keeps core's level 0. A later flip that raises core adds its own
// constant beside this one and never edits it.
const CoreCutoverLevel = 1

// coreLevelSteps are core's level steps in version order: one per flip that
// raised core's level, versions 1..n contiguous on coreLevelsOwner.
func coreLevelSteps() []storage.Migration {
	return []storage.Migration{storage.CoreLevelStep(1, CoreCutoverLevel)}
}

// coreLevelOwnerSpec is coreLevelsOwner's spec: no rows, the level steps, and
// the declared Level, the highest level any of them raises core to.
func coreLevelOwnerSpec() storage.OwnerSpec {
	steps := coreLevelSteps()
	level := 0
	for _, s := range steps {
		level = max(level, s.RaisesLevel)
	}
	return storage.OwnerSpec{
		Owner:   coreLevelsOwner,
		Spec:    storage.Spec{Keyspace: true, Level: level, Migrations: steps},
		Enabled: true,
	}
}

// CoreCutoverOwner is the storage owner core's level steps are registered on
// and the steps themselves, in version order, for the cutover-list check an app
// runs over the list it registers (internal/books/cutover.ValidateCutoverList:
// core's half sits next to books' in the list it validates). The values are
// what a build that declares a cutover registers; one that declares none
// registers neither.
func CoreCutoverOwner() (storage.Owner, []storage.Migration) {
	return coreLevelsOwner, coreLevelSteps()
}

// declaresCutover reports whether any compiled module's manifest declares R2
// cutover steps (Manifest.Cutover), enabled or not: a disabled module's
// cutover still runs (a disabled module's migration blocks its owner's later
// steps, 05-storage 5S.3.5), so core's level rises with it.
func (k *Kernel) declaresCutover() bool {
	for _, e := range k.entries {
		if e.man.Cutover != nil {
			return true
		}
	}
	return false
}
