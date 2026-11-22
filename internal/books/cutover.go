// file: internal/books/cutover.go
// version: 1.1.2
// guid: 5a82d1c7-3e94-4f60-b7a8-0c19e6d4f253
// last-edited: 2026-11-19

package books

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m5"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
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
			// Manifest.Cutover has no error return. The only error NewFlip has is
			// IdentityStep's, and the identity config's own (m3Config: importers
			// that cannot stage the step), which need FlipOptions.Identity: set only
			// while identityOn. An error there is not a panic (a boot must refuse,
			// not crash a Describe or an offline plan): it is recorded, the list is
			// empty, and Register refuses the boot with it (cutoverErr). Options has
			// already built and checked the declaration, so what reaches here is the
			// importers the kernel handed, which are its own late forwarders and
			// always carry the reads M3 needs; a failure is a wiring fault.
			m.cutoverErr.Store(&err)
			return nil
		}
		// The activity rewrite is no Cutover step, so it is not in Data; it registers
		// beside them (cutover.Flip.InPlace).
		return append(slices.Clone(f.Data), f.InPlace...)
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
	fo := m.flipOptions(imp)
	if fo.Identity != nil {
		d, err := m.identityDecl()
		if err != nil {
			return cutover.Flip{}, err
		}
		cfg, err := d.m3Config(imp)
		if err != nil {
			return cutover.Flip{}, err
		}
		fo.Identity.Config = cfg
	}
	return cutover.NewFlip(imp.Config, imp.Secrets, fo)
}

// identityDecl is the identity declaration: the one Options built and checked, or
// a fresh one for a module that never went through Options (Describe, the offline
// plan).
func (m *booksModule) identityDecl() (*identityDecl, error) {
	if m.idw != nil {
		return m.idw.decl, nil
	}
	return buildIdentityDecl()
}

// flipShapeOptions is the part of flipOptions the shape reads: the level step's
// version and the identity and tasks switches. Identity and Tasks are nil while
// identityOn is false (it is, until B10g), so the list is F3's flip alone
// (books.level-1, M2, M8). With it on the shape adds books.level-2 and the
// retired identity claims, and books.level-3 with the retired ops-family and
// syslog: claims: the tasks flip is built over the identity flip and rides its
// one switch, so the two cannot come apart (cutover.NewFlip refuses Tasks
// without Identity). M3's and M4's own configs are the manifest's, which the
// shape does not read.
func flipShapeOptions() cutover.FlipOptions {
	fo := cutover.FlipOptions{LevelStepVersion: booksLevelStepVersion}
	if identityOn() {
		fo.Identity = &cutover.IdentityFlipOptions{}
		fo.Tasks = &cutover.TasksFlipOptions{}
	}
	return fo
}

// scanStandDownKey is the concurrency key the library scan kinds run under, and
// so the key HEAD's scan stand-down marker holds once imported: books.library.scan
// and books.library.scan-full share it (05-tasks 5T.8.2; the native kinds
// prefix HEAD's "library.scan" key with "books.").
const scanStandDownKey tasks.Key = "books.library.scan"

// tasksStampAt is the instant every Requires stamp M4 imports carries. HEAD
// stored the dependency revision a completion was recorded at, never a time, so
// the stamp has no source time, and the step takes one instant chosen once, not
// read from the clock: a boot that resumes after a crash must import the stamps
// an uninterrupted one would. The date is the day the step was wired; it is
// earlier than every boot that can run the step, so a subject changed since is
// seen as changed since its stamp and its kind runs again, never skipped.
var tasksStampAt = time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)

// unboundRuns is the run reader of an Importers.Tasks that cannot read: every
// call refuses as an unbound importer does.
type unboundRuns struct{}

func (unboundRuns) Get(context.Context, tasks.RunID) (tasks.RunRecord, error) {
	return tasks.RunRecord{}, module.ErrImporterNotBound
}

// tasksFlipOptions is M4's and M5's configs over imp (the kernel's late
// forwarders: the tasks and log importers refuse until it binds them at P4b),
// books' legacy range and a logger that carries names and counts, never a value.
func tasksFlipOptions(imp module.Importers) *cutover.TasksFlipOptions {
	log := logger.FromContext(context.Background()).With("subsystem", "books-cutover")
	return &cutover.TasksFlipOptions{
		Runs: m4.Config{
			Importer: imp.Tasks,
			Legacy: func() storage.Reader {
				if imp.Legacy == nil {
					return nil
				}
				return imp.Legacy(cutover.BooksOwner)
			},
			// HEAD's reconcile, run by the step before it decodes a row. Retired
			// claims refuse its repairs, so it verifies (see
			// database.ReconcileOpsV3ForCutover).
			Reconcile: func(ctx context.Context) error {
				if imp.Legacy == nil {
					return errors.New("books: tasks cutover: no legacy range is bound")
				}
				lr, ok := imp.Legacy(cutover.BooksOwner).(storage.LegacyRange)
				if !ok {
					return errors.New("books: tasks cutover: books' legacy range is not bound")
				}
				_, err := database.ReconcileOpsV3ForCutover(ctx, lr)
				return err
			},
			ScanStandDownKey: scanStandDownKey,
			// HEAD wrote the maintenance window's date as the local calendar day.
			Location: time.Local,
			StampAt:  tasksStampAt,
			Logger:   log,
		},
		Activity: m4.ActivityConfig{
			// A repairs row's fixer is named only by the run M4 imported, which the
			// kernel's tasks importer reads back (module.Importers.Tasks).
			Runs: func() m4.RunReader {
				if r, ok := imp.Tasks.(m4.RunReader); ok {
					return r
				}
				return unboundRuns{}
			},
			Logger: log,
		},
		Log: m5.Config{Importer: imp.Log, Logger: log},
	}
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
	if fo.Tasks != nil {
		fo.Tasks = tasksFlipOptions(imp)
	}
	return fo
}
