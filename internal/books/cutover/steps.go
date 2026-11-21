// file: internal/books/cutover/steps.go
// version: 1.1.0
// guid: 7e3c91a4-0d52-4b87-9f16-b84a2c5e0d37
// last-edited: 2026-11-19

package cutover

import (
	"slices"

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
// below (F3-04's "Flip mechanism").

// FlipOptions are what building F3's list needs besides the importers.
type FlipOptions struct {
	// LevelStepVersion is books' next contiguous version, the one books.level-1
	// takes (a constant the module pins, checked against books' versioned steps
	// by the cutover-list cell).
	LevelStepVersion int
	// Config is M2's inputs; its LevelStepVersion is set from LevelStepVersion.
	Config ConfigStepOptions
}

// Flip is books' half of F3's cutover list: M7's level step, the data steps in
// 03 section 3.8's order, and M8.
type Flip struct {
	// Level is books.level-1, registered on books' Spec.Migrations.
	Level storage.Migration
	// Data are the cutover data steps in order (M2 now; M3 to M6 as their rows
	// land), each behind Level, for Manifest.Cutover.
	Data []datamigrate.DataMigration
	// Rewrite is M8, for Manifest.Rewrite, after every data step.
	Rewrite storage.RewriteStep
}

// NewFlip builds books' half of F3's cutover list over the importers the kernel
// hands Manifest.Cutover. It is pure: it does no work and reads nothing the step
// does not read when it runs, so the manifest, Describe and the offline opens
// may call it with importers that refuse every call.
func NewFlip(cfg config.Importer, sec secrets.Importer, opts FlipOptions) Flip {
	opts.Config.LevelStepVersion = opts.LevelStepVersion
	level := BooksLevelStep(opts.LevelStepVersion, BooksCutoverLevel)
	data := []datamigrate.DataMigration{ConfigStep(cfg, sec, opts.Config)}
	after := make([]storage.StepRef, 0, len(data))
	for _, d := range data {
		after = append(after, storage.StepRef{Owner: d.Owner, DataMigration: d.ID})
	}
	return Flip{Level: level, Data: data, Rewrite: RewriteStep(after...)}
}

// Spec is books' storage spec with its half of F3's flip applied to base (books'
// R1 spec): books.level-1 appended after the R1 versioned steps, Spec.Level
// raised to match, and the four legacy secret rows' claims retired. The blob
// stays a read-write claim (M2 rewrites it and M6 deletes it, 05-storage
// section 5S.9.1), and a cutover step's Source may sit only inside retired
// claims plus its Residual keys, so the four rows M2 drains must be retired
// for its registration to be accepted. The cutover rig builds its spec through
// this too, so the rig and the shipped manifest cannot drift.
func (f Flip) Spec(base storage.Spec) storage.Spec {
	spec := base
	spec.Migrations = append(slices.Clone(base.Migrations), f.Level)
	spec.Level = BooksCutoverLevel
	spec.Claims = slices.Clone(base.Claims)
	drained := map[string]bool{}
	for _, n := range headSecretRows() {
		drained[string(settingKey(n))] = true
	}
	for i := range spec.Claims {
		if c := &spec.Claims[i]; c.Key != "" && drained[c.Key] {
			c.Mode = storage.ClaimRetired
		}
	}
	return spec
}

// Entries is the list ValidateCutoverList checks, in registration order: core's
// level entries first (the kernel's half of M7, passed by the caller because
// this package is below the kernel), books' level step, the data steps, and M8
// last.
func (f Flip) Entries(core ...ListEntry) []ListEntry {
	out := append([]ListEntry(nil), core...)
	out = append(out, LevelEntry(BooksOwner, f.Level))
	for _, d := range f.Data {
		out = append(out, DataEntry(d))
	}
	return append(out, RewriteEntry(f.Rewrite))
}
