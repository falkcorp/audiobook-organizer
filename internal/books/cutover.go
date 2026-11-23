// file: internal/books/cutover.go
// version: 1.1.2
// guid: 5a82d1c7-3e94-4f60-b7a8-0c19e6d4f253
// last-edited: 2026-11-19

package books

import (
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// What books keeps of F3's cutover once R3 has deleted its steps (R3-02e-1; 03
// section 3.8 rows M2, M7, M8). The data steps M2-M6 and the rewrite M8 are
// gone: a store the R2 build left has them done (the upgrade floor, floor.go,
// refuses every other non-empty store), and a fresh install records them with
// the floor's Requires. What a store at level 1 still needs from this build is
// the level it was raised to and the steps the raise left in its ledger, so
// the manifest declares, on one predicate (declaresFlip): the level step
// books.level-1 on its storage spec with the spec's Level raised to match, the
// four retired secret claims, and an empty Manifest.Cutover. The level step
// stays registered because a fresh install records every registered version
// as applied, so a fresh store and an R2 store hold the same ledger; the
// Cutover stays non-nil because core registers its own level owner
// (kernel.CoreOwners) for a manifest that declares one, and a store the
// R2 build stamped holds core's level step in that owner's ledger.

// flipConfigNames are the config lists app.Options carries: the retired env
// names and file keys (HEAD's names, which core config replaces) and the OPENAI
// ecosystem bindings, with the predicate that lets core config place those
// bindings onto paths no registered section owns (cutover.ConfigPassthrough).
func flipConfigNames() (retired []coreconfig.RetiredName, bindings []coreconfig.EnvName, passthrough func(coreconfig.Path) bool) {
	return cutover.RetiredConfigNames(), cutover.ConfigEnvBindings(), cutover.ConfigPassthrough
}

// booksLevelStepVersion is the version books.level-1 takes: books' next
// contiguous one, pinned. TestManifest_LevelStepIsBooksNextVersion fails when
// a versioned step is added ahead of the level step that has already shipped.
const booksLevelStepVersion = database.LegacyCap + 3

// booksLevel is the level the cutover raised books to, and booksLevelStepID
// the name of the step that raised it (storage.LevelStepName). Both are written
// in every store the R2 build left, so neither changes.
const (
	booksLevel       = 1
	booksLevelStepID = "books.level-1"
)

// retiredSecretClaims are the four settings rows M2 drained into the secret
// store. A store the R2 build left holds none of them, so their claims are
// retired: books reads and writes none of them, and the key guard refuses a
// row that appears under one.
var retiredSecretClaims = []string{
	"setting:openai_api_key", "setting:google_books_api_key",
	"setting:hardcover_api_token", "setting:basic_auth_password",
}

// withBooksLevel is base (books' spec without the level) with books.level-1
// appended after its versioned steps, Spec.Level raised to match, and the four
// drained secret rows' claims retired. The blob stays a read-write claim (M6
// deleted its rows but the claim covers the family, 05-storage section 5S.9.1).
func withBooksLevel(base storage.Spec) storage.Spec {
	spec := base
	spec.Migrations = append(slices.Clone(base.Migrations), storage.LevelStep(database.BooksOwner, booksLevelStepVersion, booksLevel))
	spec.Level = booksLevel
	spec.Claims = slices.Clone(base.Claims)
	for i := range spec.Claims {
		if c := &spec.Claims[i]; c.Key != "" && slices.Contains(retiredSecretClaims, c.Key) {
			c.Mode = storage.ClaimRetired
		}
	}
	return spec
}

// StandaloneSpec is books' storage spec for the openers that plan no manifest
// (the op-runner child's store, the restore stager's verify of an archive): the
// R1 spec with the level step and the level the serve boot raised the store to.
// Without it those opens refuse a store at level 1 as written by a newer build.
func StandaloneSpec(base storage.Spec) storage.Spec {
	return withUpgradeFloor(withBooksLevel(base))
}

// StandaloneOptions is o with the level declarations added for an open that
// plans no manifest: books' spec through StandaloneSpec and core's level owner
// (kernel.CoreOwners), so a store the serve boot raised opens at both of its
// levels.
func StandaloneOptions(o database.StandaloneOptions) database.StandaloneOptions {
	o.Spec = StandaloneSpec
	m := Module().(*booksModule)
	m.dbPath = "standalone" // declaresFlip needs a database named; only the declaration is read
	o.ExtraOwners = append(slices.Clone(o.ExtraOwners), kernel.CoreOwners(m)...)
	return o
}

// FastExitModule is books' module as main's app.FastExit reads it
// (--print-storage-levels, --print-capabilities): the manifest a serve boot
// hands app.New, the level step included. Module() alone names no database and
// so declares no level (declaresFlip); a level vector printed from it would
// read books 0 for a build that serves a store at level 1. Only the
// declaration is read: nothing opens the placeholder path.
func FastExitModule() module.Module {
	m := Module().(*booksModule)
	m.dbPath = "fast-exit"
	return m
}

// declaresFlip reports whether this manifest declares books' level: Options
// has named the database (a module built without one, the help listing's,
// plans no level, so a dry run over it never raises one).
func (m *booksModule) declaresFlip() bool { return m.dbPath != "" }

// storageSpec is books' storage declaration: the R1 spec, plus the level step,
// its level and the retired claims when the level is declared. The step is
// appended after the observed ones: it writes nothing, so it must not run the
// HNSW observer.
func (m *booksModule) storageSpec() storage.Spec {
	spec := database.BooksSpec(m.beforeMigration)
	if m.declaresFlip() {
		spec = withBooksLevel(spec)
	}
	return spec
}

// cutoverSteps is Manifest.Cutover: nil unless the level is declared, and then
// a func that returns no step. Books owns no cutover data step at R3; the
// non-nil value is what makes the kernel register core's level owner, so a
// store the R2 build stamped at core level 1 still opens.
func (m *booksModule) cutoverSteps() func(module.Importers) []datamigrate.DataMigration {
	if !m.declaresFlip() {
		return nil
	}
	return func(module.Importers) []datamigrate.DataMigration { return nil }
}
