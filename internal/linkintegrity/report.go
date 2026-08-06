// file: internal/linkintegrity/report.go
// version: 1.0.0
// guid: 4e0b9d63-27af-4c81-95b2-8f16a30c7d2e
// last-edited: 2026-08-05

// Package linkintegrity holds the shared vocabulary for the "First Aid" library
// validation + repair pass: the shapes a broken book/file link can take, and a
// report type every phase folds its counts into.
//
// WHY A SHARED REPORT TYPE. The library already had four maintenance ops that
// each detected one failure mode and each reported in its own format
// (reconcile-scan, orphan-book-files-cleanup, file-integrity-check, and the
// regroup producer). Nothing tied their numbers together, so it was impossible
// to answer "is the library healthy?" without running four ops and reconciling
// by hand. Worse, running them in the wrong ORDER silently produces garbage —
// see the ordering note below. This package is the connective tissue.
//
// 🔴 THE ORDERING CONSTRAINT THIS PACKAGE EXISTS TO ENCODE.
// maintenance.regroup-shattered-ai derives each candidate's DurationSec by
// summing its book_file rows. Its series-guard (membersAreBookLength) is what
// stops distinct novels being merged into one book. A 2026-08-05 whole-library
// survey found 17,149 of 44,886 books (38.2%) have ZERO book_file rows — and
// 97.5% of the then-current review queue was made of them. For those books
// DurationSec is 0, so the guard CANNOT FIRE and the classifier is reasoning on
// blank evidence. Relink must therefore run BEFORE regroup. Encoding that as an
// ordered pipeline, rather than four independently-runnable ops, is the point.
//
// Pure data + pure functions: no I/O, no store access, fully unit-testable.
package linkintegrity

import (
	"fmt"
	"sort"
	"strings"
)

// Shape is what a book's FilePath actually resolves to on disk. It is the first
// branch every repair decision takes, because the three shapes have genuinely
// different remedies — conflating them is how a relink turns into an over-merge.
type Shape string

const (
	// ShapeFile — FilePath names a single audio file that exists. The book needs
	// exactly ONE book_file row. 16,027 of 17,149 (93.5%) measured 2026-08-05.
	ShapeFile Shape = "file"

	// ShapeDirectory — FilePath names a directory that exists. The audio is
	// inside it, so the count of rows to create is unknown until the folder is
	// read, and whether those files are ONE book or MANY is a judgement call.
	// 1,029 of 17,149 (6.0%).
	ShapeDirectory Shape = "directory"

	// ShapeMissing — FilePath resolves to nothing. Not this package's problem to
	// fix: maintenance.reconcile-scan already owns "book points at vanished
	// file", and an offline mount looks identical to deleted audio. Report only
	// (owner decision D4). 93 of 17,149 (0.5%).
	ShapeMissing Shape = "missing"
)

// Disposition is what First Aid proposes to DO about one finding.
//
// 🔴 THERE IS NO "DELETE" DISPOSITION, DELIBERATELY. Deleting a redundant book
// row is not idempotent: the audio stays on disk, and rescan regenerates a book
// for any file that no book_file row claims — so the deleted rows come straight
// back. (Blocking the file hash via the DoNotImport list suppresses the symptom
// but makes real audio permanently unrecoverable, which is worse.) The library
// converges only if every file keeps an owning book_file row, so duplicates are
// resolved by RE-ASSOCIATION — combine, then version-group — never removal.
type Disposition string

const (
	// DispositionLink — create the missing book_file row(s). Unambiguous.
	DispositionLink Disposition = "link"

	// DispositionReview — real finding, but the correct action needs a human.
	// Every ambiguous directory lands here (owner decision D1).
	DispositionReview Disposition = "review"

	// DispositionVersionGroup — this book duplicates another; resolve by
	// combining its files into one book and version-grouping with the reference.
	DispositionVersionGroup Disposition = "version-group"

	// DispositionReportOnly — surfaced for visibility, First Aid takes no action.
	DispositionReportOnly Disposition = "report-only"
)

// Finding is one book First Aid has something to say about.
type Finding struct {
	BookID   string      `json:"book_id"`
	Title    string      `json:"title"`
	FilePath string      `json:"file_path"`
	Shape    Shape       `json:"shape"`
	Action   Disposition `json:"action"`

	// Reason is the one-line human explanation, and it must name the EVIDENCE,
	// not just the verdict. "review: 11 files with 11 distinct title stems —
	// likely separate novels" is workable; "review: ambiguous" is the failure
	// mode this whole effort exists to correct (a queue where 762 of 777 rows
	// carried one identical generic string).
	Reason string `json:"reason"`

	// AudioFileCount / SubdirCount are populated for ShapeDirectory only and are
	// the raw evidence behind an auto-link-vs-review call.
	AudioFileCount int `json:"audio_file_count,omitempty"`
	SubdirCount    int `json:"subdir_count,omitempty"`

	// DuplicateOfBookID is set when this book's tracks were matched against a
	// better-assembled reference book (the "assembled copy already exists"
	// shape). Empty otherwise.
	DuplicateOfBookID string `json:"duplicate_of_book_id,omitempty"`
}

// PhaseResult is one pipeline stage's tally. Every stage reports the same four
// numbers so the whole pipeline reconciles, and so a stage that silently skipped
// work is visible rather than hidden behind a single "done" line.
type PhaseResult struct {
	Name     string `json:"name"`
	Examined int    `json:"examined"`
	Actioned int    `json:"actioned"`
	Skipped  int    `json:"skipped"`
	Errors   int    `json:"errors"`

	// Applied distinguishes a dry run from a real one. First Aid is dry-run by
	// default (owner decision D3) and this is what proves which happened.
	Applied bool `json:"applied"`

	Findings []Finding `json:"findings,omitempty"`
}

// Reconciles reports whether every examined item was accounted for. A phase that
// examined 100 and reports 30 actioned / 20 skipped / 0 errors has lost 50 —
// which is exactly the silent-filtering bug class the existing ops' RECONCILE
// log lines were added to catch (see fs_regroup_xml.go, regroup_shattered_ai.go).
func (p PhaseResult) Reconciles() bool {
	return p.Actioned+p.Skipped+p.Errors == p.Examined
}

// Report is the whole First Aid run.
type Report struct {
	// LibraryTotal is the book count the run started from. Every phase's
	// Examined is checked against it so a truncated scan (the memdb 2×limit cap
	// bug, #1647) cannot masquerade as a clean bill of health.
	LibraryTotal int           `json:"library_total"`
	Phases       []PhaseResult `json:"phases"`
	DryRun       bool          `json:"dry_run"`
}

// ShapeCounts tallies findings by Shape across every phase.
func (r Report) ShapeCounts() map[Shape]int {
	out := map[Shape]int{}
	for _, p := range r.Phases {
		for _, f := range p.Findings {
			out[f.Shape]++
		}
	}
	return out
}

// ActionCounts tallies findings by proposed Disposition across every phase.
func (r Report) ActionCounts() map[Disposition]int {
	out := map[Disposition]int{}
	for _, p := range r.Phases {
		for _, f := range p.Findings {
			out[f.Action]++
		}
	}
	return out
}

// UnreconciledPhases names every phase whose numbers do not add up. A non-empty
// result means the run cannot be trusted as a health verdict, regardless of how
// good the individual counts look.
func (r Report) UnreconciledPhases() []string {
	var bad []string
	for _, p := range r.Phases {
		if !p.Reconciles() {
			bad = append(bad, p.Name)
		}
	}
	return bad
}

// Summary renders the operator-facing one-block digest. Deterministic ordering
// (phases in pipeline order, maps sorted) so two runs diff cleanly.
func (r Report) Summary() string {
	var b strings.Builder
	mode := "DRY RUN — no writes"
	if !r.DryRun {
		mode = "APPLIED"
	}
	fmt.Fprintf(&b, "First Aid (%s) — library=%d books\n", mode, r.LibraryTotal)
	for _, p := range r.Phases {
		flag := ""
		if !p.Reconciles() {
			flag = "  ⚠️ DOES NOT RECONCILE"
		}
		fmt.Fprintf(&b, "  %-32s examined=%-7d actioned=%-7d skipped=%-7d errors=%d%s\n",
			p.Name, p.Examined, p.Actioned, p.Skipped, p.Errors, flag)
	}
	if sc := r.ShapeCounts(); len(sc) > 0 {
		keys := make([]string, 0, len(sc))
		for k := range sc {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, sc[Shape(k)]))
		}
		fmt.Fprintf(&b, "  shapes: %s\n", strings.Join(parts, " "))
	}
	if ac := r.ActionCounts(); len(ac) > 0 {
		keys := make([]string, 0, len(ac))
		for k := range ac {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, ac[Disposition(k)]))
		}
		fmt.Fprintf(&b, "  actions: %s\n", strings.Join(parts, " "))
	}
	if bad := r.UnreconciledPhases(); len(bad) > 0 {
		fmt.Fprintf(&b, "  ⚠️ UNRECONCILED PHASES: %s\n", strings.Join(bad, ", "))
	}
	return b.String()
}
