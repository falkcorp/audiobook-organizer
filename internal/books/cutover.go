// file: internal/books/cutover.go
// version: 1.1.2
// guid: 5a82d1c7-3e94-4f60-b7a8-0c19e6d4f253
// last-edited: 2026-11-19

package books

import (
	"context"
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// Books' half of F3's cutover (F3-07 d; 03 section 3.8 rows M2, M7, M8). The
// manifest declares three things together or none of them, on one predicate
// (declaresFlip): the level step books.level-1 on its storage spec with the
// spec's Level raised to match (M7), M2 in Manifest.Cutover, and M8 in
// Manifest.Rewrite. Core's half of M7 is the kernel's, registered when a
// manifest declares a cutover (kernel.CoreCutoverOwner). Every boot of the
// module over a database declares them: the config boot they need is core's
// own and always runs.

// flipConfigNames are the config lists app.Options carries: the retired env
// names and file keys (HEAD's names, which core config replaces) and the OPENAI
// ecosystem bindings, with the predicate that lets core config place those
// bindings (and the integrations values M2 stages) onto paths no registered
// section owns (cutover.ConfigPassthrough).
func flipConfigNames() (retired []coreconfig.RetiredName, bindings []coreconfig.EnvName, passthrough func(coreconfig.Path) bool) {
	return cutover.RetiredConfigNames(), cutover.ConfigEnvBindings(), cutover.ConfigPassthrough
}

// booksLevelStepVersion is the version books.level-1 takes: books' next
// contiguous one, pinned. The cutover-list cell fails when
// nextBooksVersion (the tests' count of the R1 steps) drifts from it, which is a versioned step added
// ahead of the level step that has already shipped.
const booksLevelStepVersion = database.LegacyCap + 3

// StandaloneSpec is books' storage spec for the openers that plan no manifest
// (the op-runner child's store, the restore stager's verify of an archive): the
// R1 spec with the level step and the level the serve boot raises the store to
// (cutover.Flip.Spec). Without it those opens refuse a flipped store as written
// by a newer build.
func StandaloneSpec(base storage.Spec) storage.Spec {
	// Only the level steps and the claims are applied (Flip.Spec reads nothing
	// else): the data steps need M2's inputs, which a standalone open has none
	// of and never runs, so this is the shape alone (cutover.NewFlipShape).
	return cutover.NewFlipShape(flipShapeOptions()).Spec(base)
}

// StandaloneOptions is o with the flip's storage declarations added for an open
// that plans no manifest: books' spec through StandaloneSpec and core's level
// owner (kernel.CoreOwners), so a store the serve boot raised opens at both of
// its levels.
func StandaloneOptions(o database.StandaloneOptions) database.StandaloneOptions {
	o.Spec = StandaloneSpec
	m := Module().(*booksModule)
	m.dbPath = "standalone" // declaresFlip needs a database named; only the declaration is read
	o.ExtraOwners = append(slices.Clone(o.ExtraOwners), kernel.CoreOwners(m)...)
	return o
}

// FastExitModule is books' module as main's app.FastExit reads it
// (--print-storage-levels, --print-capabilities): the manifest a serve boot
// hands app.New, the flip's level step included. Module() alone names no
// database and so declares no flip (declaresFlip); a level vector printed from
// it would read books 0 for a build that raises the store to the cutover
// level. Only the declaration is read: nothing opens the placeholder path.
func FastExitModule() module.Module {
	m := Module().(*booksModule)
	m.dbPath = "fast-exit"
	return m
}

// declaresFlip reports whether this manifest declares F3's cutover: Options
// has named the database (the steps read HEAD's paths, and a module built
// without one, the help listing's, plans no cutover, so a dry run over it never
// raises a level without M2 behind it).
func (m *booksModule) declaresFlip() bool { return m.dbPath != "" }

// storageSpec is books' storage declaration: the R1 spec, plus the level step,
// its level and the retired claims of the rows M2 drains when the flip is
// declared (cutover.Flip.Spec). The step is appended after the observed ones:
// it writes nothing, so it must not run the HNSW observer.
func (m *booksModule) storageSpec() storage.Spec {
	spec := database.BooksSpec(m.beforeMigration)
	if m.declaresFlip() {
		spec = cutover.NewFlipShape(flipShapeOptions()).Spec(spec)
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
	return func(imp module.Importers) []datamigrate.DataMigration {
		f, err := m.flipSteps(imp)
		if err != nil {
			// Manifest.Cutover has no error return, and the only error NewFlip
			// has is IdentityStep's, which needs FlipOptions.Identity: flipOptions
			// sets none, so this is unreachable until the release that turns the
			// identity cutover on (F4-08 B10), which must give this func an error
			// path before it sets the option. Until then a refusal is a programming
			// error and fails loudly, as ConfigStep does on its own misuse, rather
			// than registering a list with M3 missing.
			panic(fmt.Sprintf("books: building the cutover list: %v", err))
		}
		return f.Data
	}
}

// rewriteStep is Manifest.Rewrite (M8): nil unless the flip is declared.
func (m *booksModule) rewriteStep() *storage.RewriteStep {
	if !m.declaresFlip() {
		return nil
	}
	r := cutover.NewFlipShape(flipShapeOptions()).Rewrite
	return &r
}

// flipSteps is the cutover list over imp: the one value the manifest registers
// and the cutover-list cell validates. The parts that carry no importer (the
// level steps, M8) come from the same value's pure shape, flipShapeOptions, so the
// manifest can build them with zero importers.
func (m *booksModule) flipSteps(imp module.Importers) (cutover.Flip, error) {
	return cutover.NewFlip(imp.Config, imp.Secrets, m.flipOptions(imp))
}

// flipShapeOptions is the part of flipOptions the shape reads: the level step's
// version and the identity switch. ABO leaves Identity nil until B10 turns the
// identity cutover on, so the list is F3's flip alone (books.level-1, M2, M8).
func flipShapeOptions() cutover.FlipOptions {
	return cutover.FlipOptions{LevelStepVersion: booksLevelStepVersion}
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
	if imp.OperatorSet != nil {
		opts.OperatorSet = func(p string) (string, bool) { return imp.OperatorSet(coreconfig.Path(p)) }
	}
	fo := flipShapeOptions()
	fo.Config = opts
	return fo
}
