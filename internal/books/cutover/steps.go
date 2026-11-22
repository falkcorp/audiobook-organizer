// file: internal/books/cutover/steps.go
// version: 1.1.0
// guid: 7e3c91a4-0d52-4b87-9f16-b84a2c5e0d37
// last-edited: 2026-11-19

package cutover

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m5"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// F3's cutover list, built once (F3-07 d). The books module registers it in its
// manifest (the level step in Spec.Migrations, the data steps in Manifest.Cutover,
// M8 in Manifest.Rewrite) and the cutover-list cell of the books suite validates
// the very value the manifest registers, so the check cannot drift from what
// ships. A later flip adds its steps here, before M8, and never edits the ones
// below (F3-04's "Flip mechanism"): the identity flip (F4-08) is the second,
// books.level-2 and M3, behind FlipOptions.Identity; the tasks flip (F5-18) is
// the third, books.level-3, M4 with its activity rewrite and M5, behind
// FlipOptions.Tasks.

// FlipOptions are what building the cutover list needs besides the importers.
type FlipOptions struct {
	// LevelStepVersion is books' next contiguous version, the one books.level-1
	// takes (a constant the module pins, checked against books' versioned steps
	// by the cutover-list cell). books.level-2, when Identity is set, takes the
	// version after it.
	LevelStepVersion int
	// Config is M2's inputs; its LevelStepVersion is set from LevelStepVersion.
	Config ConfigStepOptions
	// Identity, when set, adds the identity flip (F4-08): books.level-2 and M3.
	// Nil leaves the list as F3's flip alone. ABO leaves it nil until the
	// release that turns the identity cutover on.
	Identity *IdentityFlipOptions
	// Tasks, when set, adds the tasks flip (F5-18): books.level-3, M4 (the runs
	// and then the activity rewrite) and M5. It is built over the identity flip
	// (level 3 follows level 2, and M4 runs After M3): NewFlip refuses Tasks
	// without Identity. Nil leaves the list as the identity flip's, or F3's.
	Tasks *TasksFlipOptions
}

// TasksFlipOptions are M4's and M5's inputs: their configs, built by the caller
// over the importers and the books-side readers only the booted core has.
// Their Importer, Legacy and Runs members may be late forwarders that refuse
// until the kernel binds them (P4b); the shape reads none of them.
type TasksFlipOptions struct {
	// Runs is M4's config (the operation families into core's task store).
	Runs m4.Config
	// Activity is the activity rewrite's config.
	Activity m4.ActivityConfig
	// Log is M5's config (the system log into core's journal).
	Log m5.Config
}

// IdentityFlipOptions are M3's inputs: its config, built by the caller over
// the importers and the catalog only the booted core has. Config.StepID may be
// left empty (IdentityStep).
type IdentityFlipOptions struct {
	Config m3.Config
}

// Flip is books' half of the cutover list: the level steps in order, the data
// steps in 03 section 3.8's order, and M8.
type Flip struct {
	// Levels are books' level steps in the order they run and are registered:
	// books.level-1, then books.level-2 when the identity flip is on, at
	// contiguous versions, for Spec.Migrations.
	Levels []storage.Migration
	// Data are the cutover data steps in order (M2, then M3 when the identity
	// flip is on; M4 to M6 as their rows land), each behind the level step of
	// its RaisesLevel, for Manifest.Cutover. NewFlipShape leaves it empty.
	Data []datamigrate.DataMigration
	// InPlace are the steps registered with the data steps that are not in the
	// cutover list: M4's activity rewrite, a Breaking in-place rewrite of rows
	// that stay books' read-write claim, so it is no Cutover step
	// (ValidateCutoverList admits only Cutover steps among the data entries).
	// It raises the same level as M4 and runs After it, and M8 runs After it.
	// For Manifest.Cutover, after Data.
	InPlace []datamigrate.DataMigration
	// Rewrite is M8, for Manifest.Rewrite, after every data step.
	Rewrite storage.RewriteStep

	// identity is whether the identity flip is on (FlipOptions.Identity set).
	identity bool
	// tasks is whether the tasks flip is on (FlipOptions.Tasks set).
	tasks bool
}

// NewFlipShape is the part of the cutover list that needs no importers: the
// level steps, books' Spec.Level and claims (Spec) and M8 with its After. Data
// is empty. The manifest's storage spec and Manifest.Rewrite come from here,
// because they are built with importers that refuse every call, where M3 (which
// needs the importers and the catalog to be constructed) cannot be; NewFlip
// adds the data steps to the very same shape, so the two cannot drift.
func NewFlipShape(opts FlipOptions) Flip {
	f := Flip{Levels: []storage.Migration{BooksLevelStep(opts.LevelStepVersion, BooksCutoverLevel)}}
	ids := []string{ConfigCutoverID}
	if opts.Identity != nil {
		f.identity = true
		f.Levels = append(f.Levels, BooksLevelStep(opts.LevelStepVersion+1, BooksIdentityLevel, BooksLevelStepRef(opts.LevelStepVersion)))
		ids = append(ids, IdentityCutoverID)
		// Execution order is not list order here. Storage makes every data
		// step without a Barrier depend on its owner's highest versioned step
		// (stepGraph, core/storage/migrate.go), so on a store that meets both
		// flips at once books.level-2 runs before M2: level-1, level-2, M2,
		// M3, M8. Entries keeps the list order ValidateCutoverList checks
		// (level-1, M2, level-2, M3). Stamping level 2 before M2 only makes
		// the F3 build refuse the store sooner, and M2's RaisesLevel 1 is then
		// a no-op stamp, so M2 takes no Barrier. level-2 cannot be After M2:
		// with the implicit edge that is a cycle.
	}
	if opts.Tasks != nil {
		f.tasks = true
		// Contiguous after the earlier level steps (level-2 when the identity flip
		// is on, which NewFlip requires). The same execution-order note as above
		// applies: storage runs it before M2 and M3 on a store that meets every
		// flip at once, and M4 and M5 take level 3 only after it.
		v := opts.LevelStepVersion + len(f.Levels)
		f.Levels = append(f.Levels, BooksLevelStep(v, BooksTasksLevel, BooksLevelStepRef(v-1)))
		ids = append(ids, TasksCutoverID, TasksActivityRewriteID, LogCutoverID)
	}
	after := make([]storage.StepRef, 0, len(ids))
	for _, id := range ids {
		after = append(after, storage.StepRef{Owner: BooksOwner, DataMigration: id})
	}
	f.Rewrite = RewriteStep(after...)
	return f
}

// NewFlip builds books' half of the cutover list over the importers the kernel
// hands Manifest.Cutover: NewFlipShape plus the data steps. It does no work and
// reads nothing the steps do not read when they run, so the manifest, Describe
// and the offline opens may call it with importers that refuse every call. The
// one error is IdentityStep's (M3's config is incomplete or misplaced), which
// only an Identity option can produce.
func NewFlip(cfg config.Importer, sec secrets.Importer, opts FlipOptions) (Flip, error) {
	opts.Config.LevelStepVersion = opts.LevelStepVersion
	f := NewFlipShape(opts)
	f.Data = []datamigrate.DataMigration{ConfigStep(cfg, sec, opts.Config)}
	if opts.Identity != nil {
		m3Step, err := IdentityStep(opts.Identity.Config, BooksIdentityLevel,
			BooksLevelStepRef(opts.LevelStepVersion+1),
			storage.StepRef{Owner: BooksOwner, DataMigration: ConfigCutoverID})
		if err != nil {
			return Flip{}, fmt.Errorf("cutover: the identity flip: %w", err)
		}
		f.Data = append(f.Data, m3Step)
	}
	if opts.Tasks != nil {
		if opts.Identity == nil {
			return Flip{}, errors.New("cutover: the tasks flip is built over the identity flip (books.level-3 follows books.level-2 and M4 runs after M3): FlipOptions.Tasks needs FlipOptions.Identity")
		}
		levelRef := BooksLevelStepRef(opts.LevelStepVersion + 2)
		m3Ref := storage.StepRef{Owner: BooksOwner, DataMigration: IdentityCutoverID}
		m4Steps, err := m4.Steps(opts.Tasks.Runs, opts.Tasks.Activity,
			m4.StepIDs{Runs: TasksCutoverID, Activity: TasksActivityRewriteID},
			BooksTasksLevel, []storage.StepRef{levelRef, m3Ref})
		if err != nil {
			return Flip{}, fmt.Errorf("cutover: the tasks flip: %w", err)
		}
		// M5 follows M4's runs, the order of 03 section 3.8; the activity rewrite
		// and M5 touch disjoint families.
		m5Step, err := m5.New(opts.Tasks.Log, LogCutoverID, BooksTasksLevel,
			[]storage.StepRef{levelRef, {Owner: BooksOwner, DataMigration: TasksCutoverID}})
		if err != nil {
			return Flip{}, fmt.Errorf("cutover: the tasks flip: %w", err)
		}
		f.Data = append(f.Data, m4Steps[0], m5Step)
		f.InPlace = append(f.InPlace, m4Steps[1])
	}
	return f, nil
}

// Spec is books' storage spec with its half of the flip applied to base (books'
// R1 spec): every level step appended after the R1 versioned steps, Spec.Level
// raised to the last, and the claims of the rows the data steps drain retired.
// M2's are the four legacy secret rows; the blob stays a read-write claim (M2
// rewrites it and M6 deletes it, 05-storage section 5S.9.1), and a cutover
// step's Source may sit only inside retired claims plus its Residual keys, so
// the four rows M2 drains must be retired for its registration to be accepted.
// With the identity flip on, M3's are the identity families (m3.SourcePrefixes)
// and its two bootstrap rows, which booksclaims.go declares no claim for at R1
// (ownership table row 8): each becomes an exact-key retired claim here. The
// With the tasks flip on, M4's are the operation families (m4.SourcePrefixes),
// the sub-claims of the shared setting: family its exact rows and interval
// clocks live under (m4.SourceKeys), and M5's the syslog: family. The cutover
// rig builds its spec through this too, so the rig and the shipped manifest
// cannot drift.
func (f Flip) Spec(base storage.Spec) storage.Spec {
	spec := base
	spec.Migrations = append(slices.Clone(base.Migrations), f.Levels...)
	if n := len(f.Levels); n > 0 {
		spec.Level = f.Levels[n-1].RaisesLevel
	}
	spec.Claims = slices.Clone(base.Claims)
	drained := map[string]bool{}
	for _, n := range headSecretRows() {
		drained[string(settingKey(n))] = true
	}
	var retiredPrefixes []string
	if f.identity {
		retiredPrefixes = m3.SourcePrefixes()
	}
	if f.tasks {
		retiredPrefixes = append(retiredPrefixes, m5.Prefix)
	}
	for i := range spec.Claims {
		c := &spec.Claims[i]
		if (c.Key != "" && drained[c.Key]) || (c.Prefix != "" && slices.Contains(retiredPrefixes, c.Prefix)) {
			c.Mode = storage.ClaimRetired
		}
		// M4's families are claimed one by one under longer prefixes (opv3:ix: has
		// four), so a claim is M4's when it lies under one of its source prefixes.
		if f.tasks && c.Prefix != "" && underAny(c.Prefix, m4.SourcePrefixes()) {
			c.Mode = storage.ClaimRetired
		}
	}
	if f.identity {
		for _, k := range m3.SourceKeys() {
			spec.Claims = append(spec.Claims, storage.Claim{Key: k, Mode: storage.ClaimRetired, Serialized: true, Description: "HEAD's bootstrap row (ownership table row 8)"})
		}
	}
	if f.tasks {
		// The shared setting: family stays read-write; the rows M4 moves are retired
		// as sub-claims of it, the interval clocks by prefix and the rest by exact
		// key (5T.8.3 step 3: of the shared family only the run keys move).
		for _, p := range m4.SourcePrefixes() {
			if strings.HasPrefix(p, "setting:") {
				spec.Claims = append(spec.Claims, storage.Claim{Prefix: p, Mode: storage.ClaimRetired, Description: "M4 source: " + p})
			}
		}
		for _, k := range m4.SourceKeys() {
			spec.Claims = append(spec.Claims, storage.Claim{Key: k, Mode: storage.ClaimRetired, Description: "M4 source: " + k})
		}
	}
	return spec
}

// underAny reports whether p starts with one of prefixes.
func underAny(p string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(q string) bool { return strings.HasPrefix(p, q) })
}

// Entries is the list ValidateCutoverList checks, in registration order: core's
// level entries first (the kernel's half of M7, passed by the caller because
// this package is below the kernel), then, for each data step, the books level
// steps up to the level it raises that are not yet listed, then the data step,
// and M8 last. A level step no data step guards is listed after the last one,
// where ValidateCutoverList reports it.
func (f Flip) Entries(core ...ListEntry) []ListEntry {
	out := append([]ListEntry(nil), core...)
	next := 0
	for _, d := range f.Data {
		for next < len(f.Levels) && f.Levels[next].RaisesLevel <= d.RaisesLevel {
			out = append(out, LevelEntry(BooksOwner, f.Levels[next]))
			next++
		}
		out = append(out, DataEntry(d))
	}
	for ; next < len(f.Levels); next++ {
		out = append(out, LevelEntry(BooksOwner, f.Levels[next]))
	}
	return append(out, RewriteEntry(f.Rewrite))
}
