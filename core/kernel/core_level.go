// file: core/kernel/core_level.go
// version: 1.1.0
// guid: 3d9a52c1-7e84-4b06-a1f3-c58e0d27b946
// last-edited: 2026-11-19

package kernel

import (
	"github.com/falkcorp/mediacore/module"
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

// CoreOwners are the storage owners core registers beside the modules' own, for
// the build mods compose: core/levels when any manifest declares a cutover
// (Manifest.Cutover, enabled or not: a disabled module's cutover still runs, so
// core's level rises with it), none otherwise. One source for the kernel's open
// (storageOwners), the --print-storage-levels fast exit (core's level in the
// vector make rollback's guard compares) and the cutover-list cell an app runs
// over the list it registers (core's level step is in its Migrations).
func CoreOwners(mods ...module.Module) []storage.OwnerSpec {
	mans := make([]module.Manifest, 0, len(mods))
	for _, m := range mods {
		if m != nil {
			mans = append(mans, m.Manifest())
		}
	}
	return coreOwnersOf(mans)
}

// coreOwnersOf is CoreOwners over manifests the caller already read.
func coreOwnersOf(mans []module.Manifest) []storage.OwnerSpec {
	for _, man := range mans {
		if man.Cutover != nil {
			return []storage.OwnerSpec{coreLevelOwnerSpec()}
		}
	}
	return nil
}

// coreOwners is coreOwnersOf over the kernel's compiled modules' manifests as
// P1 read them.
func (k *Kernel) coreOwners() []storage.OwnerSpec {
	mans := make([]module.Manifest, 0, len(k.entries))
	for _, e := range k.entries {
		mans = append(mans, e.man)
	}
	return coreOwnersOf(mans)
}
