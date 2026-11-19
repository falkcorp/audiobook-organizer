// file: internal/books/cutover.go
// version: 1.0.1
// guid: 5a82d1c7-3e94-4f60-b7a8-0c19e6d4f253
// last-edited: 2026-11-19

package books

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// Books' half of F3's cutover (F3-07 d; 03 section 3.8 rows M2, M7, M8). The
// manifest declares three things together or none of them, on one predicate
// (declaresFlip): the level step books.level-1 on its storage spec with the
// spec's Level raised to match (M7), M2 in Manifest.Cutover, and M8 in
// Manifest.Rewrite. Core's half of M7 is the kernel's, registered when a
// manifest declares a cutover (kernel.CoreCutoverOwner).
//
// The flip is dark. Module() leaves booksModule.flip off, so the shipped
// manifest is the one before this slice and the boot is the old one. Declared
// without the config boot (app.Options.CoreConfigBoot) M2 would fail on its
// unbound importers, and a books level step runs on any boot through the
// versioned path, so the switch is books-side and single: slice (e) turns it on
// with the gate.

// booksLevelStepVersion is the version books.level-1 takes: books' next
// contiguous one, pinned. The cutover-list cell fails when
// database.NextMigrationVersion drifts from it, which is a versioned step added
// ahead of the level step that has already shipped.
const booksLevelStepVersion = database.LegacyCap + 3

// declaresFlip reports whether this manifest declares F3's cutover: the switch
// is on and Options has named the database (the steps read HEAD's paths, and
// the offline opens that never ran Options plan no cutover, so they never raise
// a level without M2 behind it).
func (m *booksModule) declaresFlip() bool { return m.flip && m.dbPath != "" }

// storageSpec is books' storage declaration: the R1 spec, plus the level step,
// its level and the retired claims of the rows M2 drains when the flip is
// declared (cutover.Flip.Spec). The step is appended after the observed ones:
// it writes nothing, so it must not run the HNSW observer.
func (m *booksModule) storageSpec() storage.Spec {
	spec := database.BooksSpec(m.beforeMigration)
	if m.declaresFlip() {
		spec = m.flipSteps(module.Importers{}).Spec(spec)
	}
	return spec
}

// cutoverSteps is Manifest.Cutover: nil unless the flip is declared. The func
// is called at P2 (and by Describe and the offline opens, whose importers
// refuse every call); it builds the steps and runs nothing.
func (m *booksModule) cutoverSteps() func(module.Importers) []datamigrate.DataMigration {
	if !m.declaresFlip() {
		return nil
	}
	return func(imp module.Importers) []datamigrate.DataMigration { return m.flipSteps(imp).Data }
}

// rewriteStep is Manifest.Rewrite (M8): nil unless the flip is declared.
func (m *booksModule) rewriteStep() *storage.RewriteStep {
	if !m.declaresFlip() {
		return nil
	}
	r := m.flipSteps(module.Importers{}).Rewrite
	return &r
}

// flipSteps is F3's list over imp: the one value the manifest registers and the
// cutover-list cell validates. Zero importers are for the parts that carry no
// importer (the level step, M8).
func (m *booksModule) flipSteps(imp module.Importers) cutover.Flip {
	return cutover.NewFlip(imp.Config, imp.Secrets, m.flipOptions(imp))
}

// flipOptions are M2's inputs: HEAD's state directory, database path and
// config.yaml, the legacy range and the key-regen switch the kernel binds at
// P4b through imp, and a logger that carries names and counts, never a value.
func (m *booksModule) flipOptions(imp module.Importers) cutover.FlipOptions {
	opts := cutover.ConfigStepOptions{
		StateDir:     m.stateDir,
		DatabasePath: m.dbPath,
		ConfigFile:   gapFillPath(m.dbPath),
		Logger:       logger.FromContext(context.Background()).With("subsystem", "books-cutover"),
	}
	if imp.Legacy != nil {
		opts.Legacy = func() storage.Reader { return imp.Legacy(cutover.BooksOwner) }
	}
	opts.AllowKeyRegen = imp.AllowKeyRegen
	return cutover.FlipOptions{LevelStepVersion: booksLevelStepVersion, Config: opts}
}
